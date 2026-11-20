// mock 接口层自测（Node 18+）：node 原型/shared/_自测/run.mjs
// 覆盖：计价各分支、开锁各失败、站内站外还车、降级准入、押金抵扣与退押、结算归月与调整项、对账公式、越权、留痕、跨端联动与持久、时间平移。
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { createApi } from '../api.js';
import * as R from '../mock/rules.js';
import { seeded, C, Y, ms, fmt, idCheckChar } from '../mock/util.js';
import { evidenceHash } from '../mock/admin.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const dataDir = path.resolve(here, '../../mockdata');
const cache = {};
const loadJson = async n => JSON.parse(cache[n] || (cache[n] = await readFile(path.join(dataDir, n + '.json'), 'utf8')));

function makeEnv({ start, kv, local, channel, debug = true }) {
  const clock = { t: new Date(start.replace(' ', 'T')).getTime() };
  const env = {
    loadJson, kv, local, channel, debug, latency: [0, 0], random: seeded(20261117),
    now: () => clock.t, onStorage: null,
  };
  return { env, clock };
}
function memKv() { const m = new Map(); return { getMany: async ks => Object.fromEntries(ks.map(k => [k, m.has(k) ? structuredClone(m.get(k)) : undefined])), setMany: async o => { for (const [k, v] of Object.entries(o)) m.set(k, structuredClone(v)); }, clear: async () => m.clear() }; }
function memLocal() { const m = new Map(); return { get: k => (m.has(k) ? m.get(k) : null), set: (k, v) => m.set(k, String(v)), remove: k => m.delete(k) }; }
class Bus { constructor() { this.subs = []; } make() { const ch = { onmessage: null, postMessage: m => { for (const c of this.subs) if (c !== ch && c.onmessage) setTimeout(() => c.onmessage({ data: structuredClone(m) }), 0); } }; this.subs.push(ch); return ch; } }

/* ---------------- 小测试框架 ---------------- */
const results = [];
let group = '';
async function test(name, fn) {
  try { await fn(); results.push({ group, name, ok: true }); }
  catch (e) { results.push({ group, name, ok: false, err: e && (e.stack || e.message || JSON.stringify(e)) }); }
}
const eq = (a, b, m) => { if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${m || '不相等'}：期望 ${JSON.stringify(b)}，实际 ${JSON.stringify(a)}`); };
const ok = (c, m) => { if (!c) throw new Error(m || '断言失败'); };
async function rejects(p, code, m) {
  try { await p; } catch (e) { if (e.code !== code) throw new Error(`${m || ''} 期望错误 ${code}，实际 ${e.code}：${e.message}`); return e; }
  throw new Error(`${m || ''} 期望错误 ${code}，实际成功`);
}
const failShape = e => { ok(e.message && e.message.length < 80, '原因应是一句话'); ok(e.charged === false, '应注明未收费'); ok(e.next && e.next.label && e.next.action, '应给出下一步'); };

/* ---------------- 实例：锚点后 2 小时（不平移，样例编号可直接对照） ---------------- */
const bus = new Bus();
const kv = memKv(), local = memLocal();
const A1 = makeEnv({ start: '2026-12-01 14:00:00', kv, local, channel: bus.make() });
const api = createApi(A1.env);
await api.ready();
const ctx = api._ctx;
const db = () => ctx.db;
const adv = min => { A1.clock.t += min * 60e3; };
const loginUser = id => api.system.debug.switchUser(id);
const loginAdmin = u => api.admin.login(u, 'Shifan@2026');
const V = id => db().vehicles.find(v => v.vehicleId === id);
const freePileStation = () => db().stations.stations.find(s => s.type === '有桩' && db().stations.piles.some(p => p.stationId === s.stationId && p.status === '在线' && !p.occupiedVehicleId) && !db().stations.piles.some(p => p.stationId === s.stationId && p.status !== '在线'));
const rentable = brand => db().vehicles.find(v => v.brandId === brand && v.status === '可用' && v.codeStatus === '已启用' && !v.pileId && !results.usedV?.has(v.vehicleId));
results.usedV = new Set();
const pickV = brand => { const v = db().vehicles.find(x => x.brandId === brand && x.status === '可用' && x.codeStatus === '已启用' && !results.usedV.has(x.vehicleId)); results.usedV.add(v.vehicleId); return v; };
const OUTSIDE = [104.0312, 30.5712];
const idOfAge = age => { const y = 2026 - age; const f = `510104${y}0101123`; return f + idCheckChar(f); };

/* ================= 1 计价 ================= */
group = '1 计价';
const vById = Object.fromEntries(db().pricing.versions.map(v => [v.versionId, v]));
await test('骑行费：起步含 3 分钟，之后按分钟（PR0002）', () => {
  const v = vById.PR0002;
  eq(Y(R.rideFeeC(v, 1)), 1.5); eq(Y(R.rideFeeC(v, 3)), 1.5); eq(Y(R.rideFeeC(v, 4)), 2.0); eq(Y(R.rideFeeC(v, 18)), 9.0);
  eq(R.billedMinutes(1), 1); eq(R.billedMinutes(60), 1); eq(R.billedMinutes(61), 2);
});
await test('SCHEMA 4.1 样例：18 分钟站外 → 应收 14.0，月卡 3.0，券 3.0，应付 8.0', () => {
  const card = { cardId: 'MCX', perOrderCap: 3.0 };
  const coupons = [{ couponId: 'C1', userId: 'U', amount: 3.0, issuedAt: '2026-10-20 00:00:00', expireAt: '2026-11-03 23:59:59', status: '未使用', usedOrderId: null }];
  const r = R.priceOrder({ version: vById.PR0002, durationSec: 18 * 60 - 20, inStation: false, card, coupons, userId: 'U', billAt: '2026-10-26 13:30:00' });
  eq([r.fee.rideFee, r.fee.dispatchFee, r.fee.receivable, r.fee.monthCardDeduct, r.fee.couponDeduct, r.fee.payable], [9.0, 5.0, 14.0, 3.0, 3.0, 8.0]);
});
await test('月卡只抵骑行费、不抵调度费；先月卡后券', () => {
  const r = R.priceOrder({ version: vById.PR0002, durationSec: 100, inStation: false, card: { cardId: 'M', perOrderCap: 3.0 }, coupons: [], userId: 'U', billAt: '2026-11-01 00:00:00' });
  eq([r.fee.rideFee, r.fee.dispatchFee, r.fee.monthCardDeduct, r.fee.payable], [1.5, 5.0, 1.5, 5.0]);
});
await test('BR-07 选券：抵扣额最大；同额先到期；每单一张', () => {
  const base = { userId: 'U', issuedAt: '2026-11-01 00:00:00', status: '未使用', usedOrderId: null };
  const cs = [{ ...base, couponId: 'A', amount: 2.0, expireAt: '2026-11-20 23:59:59' }, { ...base, couponId: 'B', amount: 3.0, expireAt: '2026-11-30 23:59:59' }, { ...base, couponId: 'C', amount: 3.0, expireAt: '2026-11-10 23:59:59' }];
  eq(R.chooseCoupon(cs, 'U', '2026-11-05 10:00:00', 1000).couponId, 'C', '同额先到期');
  eq(R.chooseCoupon(cs, 'U', '2026-11-05 10:00:00', 150).couponId, 'C', '应付小于面额时抵扣额相同，选先到期');
  eq(R.chooseCoupon(cs, 'U', '2026-11-15 10:00:00', 1000).couponId, 'B', '过期券不选');
  eq(R.chooseCoupon(cs, 'U', '2026-11-05 10:00:00', 0), null, '应付为 0 不用券');
});
await test('BR-03 超时后才报告开锁成功：各项为 0', () => {
  const r = R.priceOrder({ version: vById.PR0003, durationSec: 4, inStation: true, card: null, coupons: [], userId: 'U', billAt: '2026-12-01 00:00:00', zero: true });
  eq([r.fee.receivable, r.fee.payable, r.billedMinutes], [0, 0, 0]);
});
await test('历史回放：全部已结束订单按开锁时刻版本复算骑行费、调度费、应收、月卡抵扣', () => {
  let n = 0; const bad = [];
  for (const o of db().orders.filter(x => x.status === '已结束' && x.unlockResult === '成功')) {
    const v = R.versionAt(db().pricing.versions, o.requestAt);
    if (v.versionId !== o.pricingVersionId) bad.push(o.orderId + ' 版本');
    const card = R.cardFor(db().users.monthCards, o.userId, o.startAt);
    const r = R.priceOrder({ version: v, durationSec: o.durationSec, inStation: o.inStation, card, coupons: [], userId: o.userId, billAt: o.billPushedAt });
    if (r.fee.rideFee !== o.fee.rideFee || r.fee.dispatchFee !== o.fee.dispatchFee || r.fee.receivable !== o.fee.receivable || r.fee.monthCardDeduct !== o.fee.monthCardDeduct || r.billedMinutes !== o.billedMinutes) bad.push(o.orderId);
    n++;
  }
  eq(bad, [], `复算不一致（共 ${n} 单）`);
});
await test('BR-14 跨调价：10-14 23:51 开锁按 PR0001', () => eq(db().orders.find(o => o.orderId === 'OD2026101412805').pricingVersionId, 'PR0001'));

/* ================= 2 开锁各失败 ================= */
group = '2 开锁失败';
await test('码未启用（联调中品牌）：一句原因含启用时间 + 未收费 + 下一步 + 附近车', async () => {
  await loginUser('U100341');
  const s = await api.user.scan('SF27239795');
  eq(s.check.code, 'CODE_NOT_ENABLED'); ok(/起可骑/.test(s.check.message), s.check.message);
  const e = await rejects(api.user.unlock('SF27239795'), 'CODE_NOT_ENABLED'); failShape(e); ok(Array.isArray(e.nearby));
});
await test('品牌暂停可租：推荐附近其他品牌车', async () => {
  const e = await rejects(api.user.unlock(V('VH000481').govCode), 'BRAND_PAUSED'); failShape(e);
  ok(e.nearby.length > 0, '应有附近车'); ok(e.nearby.every(x => x.brandId !== 'BR02'), '不应推荐暂停品牌');
});
await test('码已失效（运营商已退出）', async () => { failShape(await rejects(api.user.unlock('SF71009215'), 'CODE_INVALID')); });
await test('未识别政府码', async () => { await rejects(api.user.scan('https://x.cn/q?c=ABC'), 'CODE_NOT_FOUND'); });
await test('有欠费：一键补付', async () => {
  await loginUser('U100036');
  const s = await api.user.scan(V('VH000004').govCode);
  eq(s.check.code, 'HAS_ARREARS'); eq(s.check.next.action, 'payArrears'); eq(s.check.amount, 11.0);
  failShape(await rejects(api.user.unlock('VH000004'), 'HAS_ARREARS'));
});
await test('押金不足：补缴或开通免押', async () => { await loginUser('U100054'); const e = await rejects(api.user.unlock('VH000004'), 'DEPOSIT_INSUFFICIENT'); failShape(e); eq(e.topupAmount, 3.0); });
await test('未开通免押或押金', async () => { await loginUser('U100087'); const e = await rejects(api.user.unlock('VH000004'), 'DEPOSIT_REQUIRED'); failShape(e); eq(e.next.action, 'openCreditFree'); });
await test('账户冻结：给出解冻方法', async () => { await loginUser('U100712'); const e = await rejects(api.user.unlock('VH000004'), 'ACCOUNT_FROZEN'); failShape(e); ok(/结清欠费/.test(e.unfreezeHow)); });
await test('未实名：进入首次引导', async () => { await loginUser('U100127'); failShape(await rejects(api.user.unlock('VH000004'), 'NEED_ONBOARDING')); });
await test('退押中：不能开新单（BR-24）', async () => { await loginUser('U101097'); failShape(await rejects(api.user.unlock('VH000004'), 'DEPOSIT_REFUNDING')); });
await test('运营商报告电量不足：未计费、记开锁失败单、发关怀券', async () => {
  await loginUser('U100341');
  const v = db().vehicles.find(x => x.status === '低电量' && x.codeStatus === '已启用' && x.brandId === 'BR01');
  const before = db().orders.length;
  const e = await rejects(api.user.unlock(v.govCode), 'UNLOCK_LOW_BATTERY'); failShape(e);
  const o = db().orders[before]; eq([o.status, o.unlockFailReason, o.fee, o.payStatus], ['开锁失败', '电量不足', null, '无需支付']);
  ok(e.careCoupon && e.careCoupon.amount === 1.0, '开锁失败关怀券');
});
await test('运营商报告车辆故障', async () => { const v = db().vehicles.find(x => x.status === '故障' && x.brandId === 'BR01'); failShape(await rejects(api.user.unlock(v.govCode), 'UNLOCK_FAULT')); });
await test('车辆无响应（离线）：开锁失败不计费（BR-02）', async () => { const v = db().vehicles.find(x => x.status === '离线' && x.brandId === 'BR01'); failShape(await rejects(api.user.unlock(v.govCode), 'UNLOCK_NO_RESPONSE')); });
await test('车辆被占用（骑行中的车）', async () => { const v = db().vehicles.find(x => x.status === '骑行中' && x.brandId === 'BR01'); failShape(await rejects(api.user.unlock(v.govCode), 'UNLOCK_OCCUPIED')); });
await test('超时后才报告开锁成功：0 元并结束（BR-03）', async () => {
  await api.system.debug.failNext('unlock.lateSuccess');
  const v = pickV('BR01');
  const e = await rejects(api.user.unlock(v.govCode), 'UNLOCK_NO_RESPONSE'); ok(e.lateSuccess); failShape(e);
  const o = db().orders.find(x => x.orderId === e.orderId);
  eq([o.status, o.unlockResult, o.endMethod, o.fee.receivable, o.payStatus], ['已结束', '超时后成功', '超时开锁·平台请求结束', 0, '无需支付']);
  eq(V(v.vehicleId).status, '可用', '车辆已锁回');
});

/* ================= 3 还车 ================= */
group = '3 还车';
let rideOrderId;
await test('免押老用户：入桩自动结束 → 月卡抵扣 → 自动扣款', async () => {
  await loginUser('U100341');
  const v = pickV('BR01');
  const s = await api.user.scan(v.govCode);
  ok(s.check.ok, JSON.stringify(s.check)); eq(s.price.versionId, 'PR0003'); ok(/起步 ¥1.5（含 3 分钟），之后每分钟 ¥0.5/.test(s.price.text), s.price.text);
  const r = await api.user.unlock(v.govCode);
  eq(r.status, '骑行中'); rideOrderId = r.orderId;
  adv(18);
  const live = await api.user.ride(); eq(live.liveBilledMinutes, 18); eq(live.liveRideFee, 9.0);
  const st = freePileStation();
  const res = await api.user.returnBike({ method: 'pile', stationId: st.stationId });
  const o = res.order;
  eq([o.endMethod, o.inStation, o.inStationBasis], ['入桩识别', true, '桩识别']);
  eq([o.fee.receivable, o.fee.monthCardDeduct, o.payStatus, o.payMethod], [9.0, 3.0, '已支付', '免押自动扣款']);
  eq(o.fee.channelPaid, o.fee.payable); ok(/已自动扣/.test(res.message), res.message);
  const raw = db().orders.find(x => x.orderId === o.orderId); eq(raw.endAt, raw.pileInAt, 'BR-01 以桩在位时刻结束'); ok(ms(raw.lockReportAt) > ms(raw.pileInAt));
});
await test('页面还车在站外：先提示调度费，确认后收取（BR-11）', async () => {
  const v = pickV('BR01'); await api.user.unlock(v.govCode); adv(7);
  const pv = await api.user.returnPreview({ location: OUTSIDE }); eq(pv.inStation, false); eq(pv.dispatchFee, 5.0); ok(pv.nearestStation.distanceM > 0);
  const e = await rejects(api.user.returnBike({ method: 'app', location: OUTSIDE }), 'DISPATCH_CONFIRM_REQUIRED'); eq(e.dispatchFee, 5.0);
  const r = await api.user.returnBike({ method: 'app', location: OUTSIDE, confirmDispatch: true });
  eq([r.order.inStation, r.order.inStationBasis, r.order.fee.rideFee, r.order.fee.dispatchFee, r.order.fee.receivable], [false, '站外', 3.5, 5.0, 8.5]);
  eq(r.order.fee.monthCardDeduct, 3.0, '月卡只抵骑行费');
  ok(r.order.bill.notes.some(n => /调度费/.test(n)), '账单中说明调度费');
});
await test('站外手动锁车也收调度费', async () => {
  const v = pickV('BR03'); await api.user.unlock(v.govCode); adv(3);
  const r = await api.user.returnBike({ method: 'lock', location: OUTSIDE });
  eq([r.order.endMethod, r.order.fee.dispatchFee, r.order.fee.rideFee], ['用户锁车', 5.0, 1.5]);
});
await test('桩满：站点内桩旁锁车按在点（BR-10）；推入桩位报桩满', async () => {
  const v = pickV('BR01'); await api.user.unlock(v.govCode); adv(5);
  await rejects(api.user.returnBike({ method: 'pile', stationId: 'ST0001' }), 'PILE_FULL');
  const r = await api.user.returnBike({ method: 'lock', stationId: 'ST0001' });
  eq([r.order.inStation, r.order.inStationBasis, r.order.fee.dispatchFee], [true, '桩满·围栏内', 0]);
});
await test('无桩站点：车端判定（BR-12）', async () => {
  const v = pickV('BR01'); await api.user.unlock(v.govCode); adv(4);
  const r = await api.user.returnBike({ method: 'app', stationId: 'ST0002' });
  eq([r.order.inStationBasis, r.order.endMethod, r.order.fee.dispatchFee], ['无桩站点·车端判定', '页面还车', 0]);
});
await test('入桩未锁：平台请求结束，按桩在位时刻计费（BR-09）', async () => {
  const v = pickV('BR01'); await api.user.unlock(v.govCode); adv(9);
  const st = freePileStation();
  const r = await api.user.returnBike({ method: 'pileNoLock', stationId: st.stationId });
  const o = db().orders.find(x => x.orderId === r.order.orderId);
  eq([o.endMethod, o.endAt === o.pileInAt], ['入桩未锁·平台请求结束', true]);
});
await test('桩与车端冲突以桩为准（BR-13）', async () => {
  const v = pickV('BR01'); await api.user.unlock(v.govCode); adv(6);
  const r = await api.user.returnBike({ method: 'pile', stationId: freePileStation().stationId, conflict: true });
  eq([r.order.inStation, r.order.inStationBasis], [true, '桩与车端冲突·以桩为准']);
});
await test('押金用户：结束页一键支付', async () => {
  await loginUser('U100249');
  const v = pickV('BR01'); await api.user.unlock(v.govCode); adv(10);
  const r = await api.user.returnBike({ method: 'lock', stationId: 'ST0002' });
  eq(r.order.payStatus, '待支付'); ok(r.order.bill.canPay); ok(r.order.bill.payDeadline);
  const p = await api.user.pay(r.order.orderId); eq(p.order.payStatus, '已支付'); eq(p.order.payMethod, '一键支付');
});
await test('免押扣款失败 → 记欠费 → 下次扫码一键补付 → 可开锁', async () => {
  await loginUser('U100426');
  await api.system.debug.failNext('payment.decline');
  const v = pickV('BR01'); await api.user.unlock(v.govCode); adv(15);
  const r = await api.user.returnBike({ method: 'lock', stationId: 'ST0002' });
  eq(r.order.payStatus, '欠费');
  const s = await api.user.scan(pickV('BR01').govCode); eq(s.check.code, 'HAS_ARREARS');
  const pa = await api.user.payArrears(); eq(pa.orders[0].payStatus, '欠费已补付');
  const s2 = await api.user.scan(pickV('BR01').govCode); ok(s2.check.ok);
});

/* ================= 4 价格联动（BR-14、BR-15） ================= */
group = '4 改价联动';
await test('主管部门改价立即生效：用户端扫码显示新价；改价前开锁的单按旧价', async () => {
  await loginUser('U100341');
  const v1 = pickV('BR01'); await api.user.unlock(v1.govCode); adv(1);
  await loginAdmin('zgbm.zhou');
  let evName = null; const off = api.on('pricing.changed', e => { evName = e.name; });
  const nv = await api.admin.pricing.create({ name: '分钟价调整', ride: { perMinute: 0.6 } }, '按主管部门会议纪要调整分钟价');
  off(); eq(nv.status, '生效中'); eq(evName, 'pricing.changed');
  const s = await api.user.scan(pickV('BR03').govCode); eq(s.price.perMinute, 0.6); eq(s.price.versionId, nv.versionId);
  adv(9);
  const r = await api.user.returnBike({ method: 'lock', stationId: 'ST0002' });
  eq(r.order.pricingVersionId, 'PR0003', '改价前开锁按旧版本'); eq(r.order.fee.rideFee, Y(150 + 7 * 50));
  const v3 = pickV('BR01'); await api.user.unlock(v3.govCode); adv(10);
  const r2 = await api.user.returnBike({ method: 'lock', stationId: 'ST0002' });
  eq(r2.order.pricingVersionId, nv.versionId); eq(r2.order.fee.rideFee, Y(150 + 7 * 60));
  ok(db().audit.some(a => a.action === '新增规则版本' && a.targetId === nv.versionId), '新增规则版本留痕');
});
await test('待生效版本到点生效；旧版本留存', async () => {
  const t = fmt(A1.clock.t + 30 * 60e3);
  const nv = await api.admin.pricing.create({ name: '调度费调整', effectiveAt: t, dispatchFee: 6.0 }, '调度费调整为 6 元');
  eq(nv.status, '待生效');
  adv(31);
  const cur = await api.admin.pricing.current(); eq(cur.versionId, nv.versionId); eq(cur.dispatchFee, 6.0);
  ok((await api.admin.pricing.list()).some(v => v.versionId === 'PR0001' && v.status === '已失效'));
  await rejects(api.admin.pricing.create({ name: 'x', effectiveAt: '2026-01-01 00:00:00' }, '回溯测试一下'), 'INVALID');
});

/* ================= 5 降级准入 ================= */
group = '5 降级';
let degradedOrder;
await test('支付通道故障：老用户只多一行"费用将稍后收取"', async () => {
  await api.system.setDegrade({ on: true, preset: '支付通道故障' });
  await loginUser('U100341');
  const s = await api.user.scan(pickV('BR01').govCode);
  ok(s.check.ok); eq(s.degrade.line, '费用将稍后收取'); eq(s.degrade.admission.category, '老用户'); eq(s.degrade.admission.notice, null);
  const r = await api.user.unlock(s.vehicle.govCode); eq(r.degraded.admission, '老用户'); degradedOrder = r.orderId;
  adv(12);
  const back = await api.user.returnBike({ method: 'lock', stationId: 'ST0002' });
  ok(back.deferred, '降级期间费用稍后收取'); eq(back.order.fee, null);
});
await test('降级：每用户未结降级单 1 单，不叠加', async () => { await rejects(api.user.unlock(pickV('BR01').govCode), 'DEGRADE_USER_LIMIT'); });
await test('降级：已知欠费不能开单', async () => { await loginUser('U100279'); await rejects(api.user.unlock(pickV('BR01').govCode), 'DEGRADE_REJECT_ARREARS'); });
await test('降级：退押中不能开单', async () => { await loginUser('U100479'); await rejects(api.user.unlock(pickV('BR01').govCode), 'DEGRADE_REJECT_REFUNDING'); });
await test('降级：押金不足（状态已知）按常态拒绝', async () => { await loginUser('U100054'); await rejects(api.user.unlock(pickV('BR01').govCode), 'DEPOSIT_INSUFFICIENT'); });
await test('退出降级 → 逐单闭合：免押扣款、三项齐全', async () => {
  const ev = await api.system.setDegrade({ on: false });
  const o = db().orders.find(x => x.orderId === degradedOrder);
  eq([o.degraded.closure.status, o.degraded.closure.payment, o.payStatus], ['已闭合', '已收款', '已支付']);
  ok(o.billPushedAt, '恢复时推送账单（A10）'); ok(ev.closure.closed >= 1);
});
await test('押金与免押查询不可用：押金状态未知的老用户可开，额度收紧（PR0003 30 分钟/¥15）', async () => {
  await api.system.setDegrade({ on: true, preset: '押金与免押查询不可用' });
  await loginUser('U100054');
  const s = await api.user.scan(pickV('BR01').govCode);
  eq(s.degrade.admission.category, '押金状态未知的老用户'); eq([s.degrade.admission.limitMinutes, s.degrade.admission.limitAmount], [30, 15]);
  const r = await api.user.unlock(s.vehicle.govCode);
  adv(31);
  await api.user.ride();
  const o = db().orders.find(x => x.orderId === r.orderId);
  ok(o.degraded.overLimitRemindedAt, '超时长提醒'); eq(o.status, '骑行中', '超限只提醒不结束');
  ok(db().ops.alerts.some(a => a.type === '降级单超限'), '通知值班人');
  ok(db().notices.some(n => n.userId === 'U100054' && n.title === '请尽快还车'));
  await api.user.returnBike({ method: 'lock', stationId: 'ST0002' });
});
await test('值班人收紧：紧档用户暂停降级开单；停止：全部不能开新单；放宽恢复', async () => {
  await loginAdmin('yw.he');
  await api.admin.ops.degrade.tighten('降级持续较久，先收紧');
  await loginUser('U100975');
  await rejects(api.user.unlock(pickV('BR01').govCode), 'DEGRADE_PAUSED_TIGHT');
  await api.admin.ops.degrade.stop('故障扩大，停止开新单');
  await loginUser('U100004');
  await rejects(api.user.unlock(pickV('BR01').govCode), 'DEGRADE_STOPPED');
  await api.admin.ops.degrade.relax('故障范围已明确，恢复降级开单');
  ok(db().audit.some(a => a.action === '收紧降级') && db().audit.some(a => a.action === '停止降级'));
  await api.system.setDegrade({ on: false });
});
let minorUser, adultUser;
await test('实名核验不可用：新用户先骑后验，看到单笔上限与后果', async () => {
  await api.system.setDegrade({ on: true, preset: '实名核验不可用' });
  const a = await api.user.authorize({ channel: '微信', phone: '13900001111' }); ok(a.isNew); minorUser = a.user.userId;
  const vr = await api.user.verifyRealName({ name: '王小明', idNo: idOfAge(15) });
  eq(vr.status, '待核验'); ok(vr.deferred); ok(/核验未通过将冻结账户并追缴/.test(vr.text));
  const s = await api.user.scan(pickV('BR01').govCode);
  eq(s.degrade.admission.category, '先骑后验新用户'); ok(/最长 30 分钟/.test(s.degrade.admission.notice));
  await api.user.unlock(s.vehicle.govCode); adv(8);
  await api.user.returnBike({ method: 'lock', stationId: 'ST0002' });
  const b = await api.user.authorize({ channel: '支付宝', phone: '13900002222' }); adultUser = b.user.userId;
  await api.user.verifyRealName({ name: '李大成', idNo: idOfAge(30) });
  const v2 = pickV('BR03'); await api.user.unlock(v2.govCode); adv(6);
  await api.user.returnBike({ method: 'lock', stationId: 'ST0002' });
});
await test('授权不可用时新登录用户不能开单（BR-27/BR-29）', async () => {
  await api.system.setDegrade({ on: false });
  await api.system.setDegrade({ on: true, preset: '授权不可用' });
  await rejects(api.user.authorize({ channel: '微信', phone: '13900003333' }), 'AUTH_UNAVAILABLE');
  await api.system.setDegrade({ on: false });
});
await test('恢复后补验：未满 16 周岁冻结并追缴；成年人通过（BR-34）', async () => {
  const m = db().users.users.find(u => u.userId === minorUser), ad = db().users.users.find(u => u.userId === adultUser);
  eq([m.realName.status, m.account.status], ['未通过', '冻结']); ok(m.arrears > 0, '本次费用进入追缴');
  eq(ad.realName.status, '已核验');
  const ev = db().ops.degradeEvents.find(e => e.orderIds.some(id => db().orders.find(o => o.orderId === id).userId === minorUser));
  eq(ev.closure.newUserVerification, { 未通过: 1, 通过: 1 });
});

/* ================= 6 押金 ================= */
group = '6 押金';
let depOrder;
await test('押金用户超期未付：自动从押金抵扣并事后告知（BR-22）', async () => {
  await loginUser('U100004');
  const v = pickV('BR01'); await api.user.unlock(v.govCode); adv(12);
  const r = await api.user.returnBike({ method: 'lock', stationId: 'ST0002' }); depOrder = r.order.orderId;
  eq(r.order.payStatus, '待支付');
  adv(24 * 60 + 1);
  const o = await api.user.order(depOrder);
  eq([o.payStatus, o.payMethod, o.fee.depositDeducted, o.fee.unpaid], ['押金抵扣', '押金抵扣', o.fee.payable, 0]);
  const u = db().users.users.find(x => x.userId === 'U100004');
  eq([u.deposit.status, u.deposit.balance], ['押金不足', Y(9900 - C(o.fee.payable))]);
  ok(db().notices.some(n => n.userId === 'U100004' && n.kind === '押金抵扣'), '事后告知');
  ok(db().audit.some(a => a.action === '抵扣押金' && a.targetId === depOrder), '抵扣押金留痕');
});
await test('押金不足后补缴（按缴纳时金额，BR-25）', async () => {
  const e = await rejects(api.user.unlock(pickV('BR01').govCode), 'DEPOSIT_INSUFFICIENT');
  const r = await api.user.topupDeposit(); eq(r.user.deposit.balance, 99.0); eq(r.user.deposit.status, '已缴');
});
await test('退押：骑行中不能退；退押后停止开新单；48 小时原路到账', async () => {
  const v = pickV('BR01'); await api.user.unlock(v.govCode);
  const e = await rejects(api.user.deposit.refund(), 'REFUND_BLOCKED'); eq(e.blockers[0].code, 'HAS_RIDING');
  adv(4); const back = await api.user.returnBike({ method: 'lock', stationId: 'ST0002' }); await api.user.pay(back.order.orderId);
  const r = await api.user.deposit.refund(); ok(r.refund.expectedAt > fmt(A1.clock.t)); ok(/预计/.test(r.text));
  await rejects(api.user.unlock(pickV('BR01').govCode), 'DEPOSIT_REFUNDING');
  adv(48 * 60 + 1);
  const d = await api.user.deposit.get(); eq([d.deposit.status, d.deposit.balance], ['已退', 0]);
  eq(R.depositLedger(db().payments.payments).balance, Y(db().users.users.reduce((s, u) => s + (u.deposit.mode === '押金' ? C(u.deposit.balance) : u.deposit.previous ? C(u.deposit.previous.balance) : 0), 0)), '押金专户余额 = 各用户押金余额之和');
});
await test('退押原路失败 → 用户填写本人收款账户 → 财务人工转账', async () => {
  await loginUser('U100116');
  await api.system.debug.failNext('deposit.refund.fail');
  const r = await api.user.deposit.refund();
  adv(48 * 60 + 1);
  const d = await api.user.deposit.get(); ok(d.awaitingPayee);
  const u = db().users.users.find(x => x.userId === 'U100116');
  await rejects(api.user.submitPayee({ paymentId: r.refund.paymentId, name: '某某', bank: '示范市商业银行', account: '6215880012345678' }), 'PAYEE_NAME_MISMATCH');
  await api.user.submitPayee({ paymentId: r.refund.paymentId, name: u.name[0] + '某'.repeat([...u.name].length - 1), bank: '示范市商业银行', account: '6215880012345678' });
  await loginAdmin('cw.lv');
  const f = await api.admin.finance.refunds.failures(); ok(f.some(x => x.paymentId === r.refund.paymentId && x.status === '待转账'));
  await api.admin.finance.refunds.transfer(r.refund.paymentId, {}, '原路退回失败，按用户提供账户人工转账');
  eq([u.deposit.status, u.deposit.balance], ['已退', 0]);
});
await test('注销：有欠费时先提示处理（R-48）', async () => {
  await loginUser('U100279');
  const c = await api.user.account.cancelCheck(); ok(!c.canCancel); ok(c.blockers.some(b => b.code === 'HAS_ARREARS'));
  await rejects(api.user.account.cancel(), 'CANCEL_BLOCKED');
  await api.user.payArrears();
  const r = await api.user.account.cancel(); ok(r.ok);
});

/* ================= 7 结算 ================= */
group = '7 结算';
const versionsById = () => Object.fromEntries(db().pricing.versions.map(v => [v.versionId, v]));
await test('复算原始结算单：按出单时刻重建 2026-10、2026-11 各运营商明细与调整项，合计一致', () => {
  const all = db().settlements.settlements;
  for (const month of ['2026-10', '2026-11']) for (const s of all.filter(x => x.month === month)) {
    const prior = all.filter(x => x.month < month);
    const b = R.buildSettlement({ orders: db().orders, payments: db().payments.payments, settlements: prior, disputes: db().settlements.disputes, versionsById: versionsById(), operatorId: s.operatorId, month, at: s.generatedAt });
    eq(b.details.length, s.details.length, `${s.settlementId} 明细条数`);
    eq(Y(b.details.reduce((t, d) => t + C(d.fee), 0)), s.totals.detailFee, `${s.settlementId} 明细服务费`);
    eq(Y(b.adjustments.reduce((t, a) => t + C(a.feeDelta), 0)), s.totals.adjustmentFee, `${s.settlementId} 调整项`);
    eq(b.adjustments.map(a => a.type).sort(), s.adjustments.map(a => a.type).sort(), `${s.settlementId} 调整项类型`);
  }
});
await test('BR-19 跨月：10-31 开锁、11-01 结束的单进 11 月结算单', () => {
  const s = db().settlements.settlements.find(x => x.settlementId === 'ST-M-202611-OP01');
  ok(s.details.some(d => d.orderId === 'OD2026103112719'));
});
await test('BR-17 同一运营商两品牌两比例合在一张单（10 月木棉 58%/60%）', () => {
  const s = db().settlements.settlements.find(x => x.settlementId === 'ST-M-202610-OP01');
  eq(s.brands.find(b => b.brandId === 'BR02').ratioBreakdown.map(r => r.ratio).sort(), [0.58, 0.6]);
});
await test('本期预览随新订单变化（运营商明细、结算预览）', async () => {
  await loginAdmin('qhcx.he');
  const ol = await api.admin.operator.orders.list({ pageSize: 500 });
  ok(ol.rows.some(r => r.orderId === rideOrderId), '运营商明细含刚骑完的订单'); ok(ol.rows.every(r => !('userId' in r)), '运营商看不到用户身份');
  const cur = await api.admin.operator.settlements.current();
  eq(cur.status, '未出单'); ok(cur.details.some(d => d.orderId === rideOrderId)); eq(cur.totals.payableFee, Y(C(cur.totals.detailFee) + C(cur.totals.adjustmentFee)));
  const d = cur.details.find(x => x.orderId === rideOrderId); eq(d.fee, Y(R.feeOf(C(d.base), d.ratio)));
});
await test('出单后退款 → 下一期"出单后退款"调整项（客服确认、原路退回）', async () => {
  const o = db().orders.find(x => x.settlement && x.settlement.settlementId === 'ST-M-202611-OP01' && x.payStatus === '已支付' && x.fee.channelPaid >= 3 && !x.ticketIds.length && x.refundStatus === '无');
  await loginUser(o.userId);
  const tk = await api.user.cs.open({ orderId: o.orderId, entry: '我的订单', text: '这单收费多了，麻烦退一点' });
  await loginAdmin('kf.su');
  const r = await api.admin.cs.refund.confirm(tk.ticketId, { amount: 2.0 }, '核实车辆提前停放，退 2 元');
  eq(r.pendingSupervisor, false); eq(r.payment.status, '成功');
  await loginAdmin('cw.lv');
  const pv = await api.admin.finance.settlements.preview('OP01');
  const adj = pv.adjustments.find(a => a.type === '出单后退款' && a.items.some(i => i.orderId === o.orderId));
  ok(adj, '应有出单后退款调整项'); const it = adj.items.find(i => i.orderId === o.orderId);
  eq(it.baseDelta, -2.0); eq(it.feeDelta, Y(R.feeOf(C(it.newBase), it.ratio) - R.feeOf(C(it.previousBase), it.ratio)));
});
await test('晚到补报：补录降级单锁车事实 → 下一期"晚到补报"调整项', async () => {
  await loginAdmin('yw.he');
  await api.admin.ops.closure.handle('OD2026112714209', { action: '补录锁车事实', endAt: '2026-11-27 21:30:44', stationId: null }, '用户自述 21:30 锁车，车辆轨迹一致');
  const o = db().orders.find(x => x.orderId === 'OD2026112714209');
  eq([o.status, o.settleMonth, o.degraded.closure.status], ['已结束', '2026-11', '已闭合']);
  await loginAdmin('cw.lv');
  const pv = await api.admin.finance.settlements.preview('OP02');
  ok(pv.adjustments.some(a => a.type === '晚到补报' && a.items.some(i => i.orderId === 'OD2026112714209')));
});
await test('异议：价格不受理；时长异议采纳 → 下一期"异议核实"调整项', async () => {
  await loginAdmin('clzx.shen');
  const s = db().settlements.settlements.find(x => x.settlementId === 'ST-M-202611-OP02');
  const od = s.details.find(d => !d.degraded && d.billedMinutes > 5 && !db().orders.find(o => o.orderId === d.orderId).pileInAt);
  await rejects(api.admin.operator.disputes.submit({ settlementId: s.settlementId, orderIds: [od.orderId], field: '价格', claim: '价格太低了不合理' }), 'PRICE_NOT_DISPUTABLE');
  const d = await api.admin.operator.disputes.submit({ settlementId: s.settlementId, orderIds: [od.orderId], field: '时长', claim: '我方记录骑行时长多 3 分钟' });
  ok(d.agentComparison.startsWith('对账智能体比对'));
  await loginAdmin('cw.lv');
  await api.admin.finance.disputes.decide(d.disputeId, { decision: '采纳', corrections: [{ orderId: od.orderId, minutes: od.billedMinutes + 3 }] }, '轨迹核实运营商主张成立');
  const pv = await api.admin.finance.settlements.preview('OP02');
  const adj = pv.adjustments.find(a => a.type === '异议核实');
  ok(adj && adj.items[0].baseDelta === Y(3 * C(versionsById()[od.pricingVersionId].ride.perMinute)), JSON.stringify(adj));
});
await test('结算单流程：未确认调整项不能审核 → 确认 → 审核 → 发送 → 到账', async () => {
  const id = 'ST-M-202611-OP01';
  await rejects(api.admin.finance.settlements.review(id), 'ADJUSTMENT_UNCONFIRMED');
  const s = db().settlements.settlements.find(x => x.settlementId === id);
  for (const a of s.adjustments) await api.admin.finance.settlements.confirmAdjustment(id, a.adjustmentId);
  await api.admin.finance.settlements.addAdjustment(id, { brandId: 'BR01', feeDelta: -1.25 }, '线下核对差错更正');
  eq(s.totals.payableFee, Y(C(s.totals.detailFee) + C(s.totals.adjustmentFee)));
  await api.admin.finance.settlements.review(id);
  await api.admin.finance.settlements.send(id);
  const r = await api.admin.finance.settlements.registerPayment(id, { bankTradeNo: 'HX202612031234567890' });
  eq(r.status, '已到账');
});
await test('财务手动生成上月结算单：月结束即可生成，自动出单不抢先、不重复；逐单归属、调整项进单', async () => {
  const before = db().settlements.settlements.length;
  await rejects(api.admin.finance.settlements.generate('2026-12'), 'MONTH_NOT_ENDED');
  A1.clock.t = new Date('2027-01-01T00:10:00').getTime();
  await api.admin.me();
  eq(db().settlements.settlements.length, before, '普通接口调用不触发出单');
  const made = await api.admin.finance.settlements.generate('2026-12');
  ok(made.length >= 2, `生成 ${made.length} 张`);
  const created = db().settlements.settlements.slice(before);
  ok(created.every(s => s.month === '2026-12' && s.generatedAt === '2027-01-01 00:10:00' && s.dueDate === '2027-01-06'));
  await rejects(api.admin.finance.settlements.generate('2026-12'), 'ALREADY_GENERATED');
  A1.clock.t = new Date('2027-01-01T00:31:00').getTime();
  await api.system.time();
  eq(db().settlements.settlements.length, before + made.length, '财务已手动生成，到点不再自动出单');
  const s1 = created.find(s => s.operatorId === 'OP01');
  ok(s1.details.some(d => d.orderId === rideOrderId)); eq(db().orders.find(o => o.orderId === rideOrderId).settlement.settlementId, s1.settlementId);
  ok(s1.adjustments.some(a => a.type === '出单后退款'));
  const s2 = created.find(s => s.operatorId === 'OP02');
  ok(s2.adjustments.some(a => a.type === '晚到补报') && s2.adjustments.some(a => a.type === '异议核实'));
  for (const s of created) eq(s.totals.payableFee, Y(s.brands.reduce((t, b) => t + C(b.detailFee) + C(b.adjustmentFee), 0)), '各品牌合计 = 应付');
  const card = created[0].monthCard; eq(Y(created.reduce((t, s) => t + s.brands.reduce((x, b) => x + C(b.monthCardShare), 0), 0)), card.platformCardOrders ? card.platformIncome : 0, '月卡分摊合计 = 月卡收入');
});

/* ================= 8 对账 ================= */
group = '8 对账';
await test('原始每日对账复算一致（订单实收、月卡款、押金抵扣、应到账、差异）', () => {
  const bad = [];
  for (const r of db().payments.reconciliation.filter(x => x.frozen && x.date <= '2026-11-30')) {
    const c = R.computeRecon(db().payments.payments, r.date);
    for (const k of ['orderNetReceived', 'monthCardIncome', 'depositDeducted', 'expected', 'diff']) if (c[k] !== r[k]) bad.push(`${r.date} ${k}`);
    if (c.channelActual.total !== r.channelActual.total) bad.push(`${r.date} 渠道`);
  }
  eq(bad, []);
});
await test('公式：应到账 = 订单实收 + 月卡款 − 押金抵扣；差异 = Σ差异项（含运行时新单与渠道未到账）', async () => {
  await loginUser('U100341');
  await api.system.debug.failNext('channel.missing');
  const v = pickV('BR01'); await api.user.unlock(v.govCode); adv(5);
  await api.user.returnBike({ method: 'lock', stationId: 'ST0002' });
  await loginAdmin('cw.lv');
  const day = fmt(A1.clock.t).slice(0, 10);
  const rec = (await api.admin.finance.recon.list({ pageSize: 500 })).rows.find(r => r.date === day);
  eq(rec.expected, Y(C(rec.orderNetReceived) + C(rec.monthCardIncome) - C(rec.depositDeducted)));
  eq(rec.diff, Y(rec.diffItems.reduce((s, i) => s + C(i.amount), 0)));
  ok(rec.diffItems.some(i => i.type === '渠道未到账' && i.status === '待处理'));
});
await test('全期核对：渠道到账 = 订单实收 + 月卡款 − 押金抵扣 + 人工转账退款 − 渠道未到账', () => {
  const P = db().payments.payments, ch = p => p.account === '骑行收入户' && (p.channel === '微信支付' || p.channel === '支付宝');
  const chan = P.filter(p => ch(p) && p.status === '成功' && p.channelSettledDate).reduce((s, p) => s + C(p.amount) * (p.direction === '收' ? 1 : -1), 0);
  const net = db().orders.filter(o => o.fee).reduce((s, o) => s + C(o.fee.netReceived), 0);
  const card = P.filter(p => p.type === '月卡购买' && p.status === '成功').reduce((s, p) => s + C(p.amount), 0);
  const dep = db().orders.filter(o => o.fee).reduce((s, o) => s + C(o.fee.depositDeducted), 0);
  const man = P.filter(p => p.type === '人工转账退款' && p.status === '成功').reduce((s, p) => s + C(p.amount), 0);
  const miss = P.filter(p => ch(p) && p.status === '成功' && !p.channelSettledDate).reduce((s, p) => s + C(p.amount) * (p.direction === '收' ? 1 : -1), 0);
  eq(chan, net + card - dep + man - miss);
});
await test('订单恒等式：应付 = 渠道已收 + 押金抵扣 + 未付；实收 = 渠道已收 + 押金抵扣 − 已退款', () => {
  const bad = db().orders.filter(o => o.fee && (C(o.fee.payable) !== C(o.fee.channelPaid) + C(o.fee.depositDeducted) + C(o.fee.unpaid) || C(o.fee.netReceived) !== C(o.fee.channelPaid) + C(o.fee.depositDeducted) - C(o.fee.refunded))).map(o => o.orderId);
  eq(bad, []);
});

/* ================= 9 权限与越权 ================= */
group = '9 权限';
await test('运营商只能看本运营商：查他家结算单、订单返回 SCOPE_DENIED', async () => {
  await loginAdmin('qhcx.he');
  await rejects(api.admin.operator.settlements.get('ST-M-202611-OP02'), 'SCOPE_DENIED');
  await rejects(api.admin.operator.orders.list({ operatorId: 'OP02' }), 'SCOPE_DENIED');
  await rejects(api.admin.operator.orders.get(db().orders.find(o => o.operatorId === 'OP02').orderId), 'SCOPE_DENIED');
  const own = await api.admin.operator.settlements.list(); ok(own.every(s => s.operatorId === 'OP01'));
});
await test('岗位越权：运营商调财务接口、财务改价格返回 FORBIDDEN', async () => {
  await rejects(api.admin.finance.settlements.list(), 'FORBIDDEN');
  await loginAdmin('cw.lv');
  await rejects(api.admin.pricing.create({ name: 'x' }, '财务尝试改价'), 'FORBIDDEN');
});
await test('已退出运营商账号只读', async () => {
  await loginAdmin('jtgx.wei');
  await rejects(api.admin.operator.disputes.submit({ settlementId: 'ST-M-202611-OP03', orderIds: [], field: '时长', claim: '时长有误差' }), 'READONLY');
  ok((await api.admin.operator.settlements.list()).length === 3);
});
await test('超额退款：客服提交主管确认，客服不能代主管确认', async () => {
  await loginAdmin('kf.su');
  const tk = 'TK202612010032';
  await rejects(api.admin.cs.refund.approveOverLimit(tk, '客服尝试越权确认'), 'FORBIDDEN');
  await loginAdmin('kf.tan');
  const r = await api.admin.cs.refund.approveOverLimit(tk, '核实桩满桩旁已停放，同意退款');
  eq(r.payment.status, '成功');
  ok(db().audit.some(a => a.action === '确认超额退款' && a.accountId === 'AC0303'));
});
await test('未登录调用管理端返回 ADMIN_NOT_LOGGED_IN；错误密码 LOGIN_FAILED', async () => {
  await api.admin.logout();
  await rejects(api.admin.me(), 'ADMIN_NOT_LOGGED_IN');
  await rejects(api.admin.login('zgbm.zhou', 'wrong'), 'LOGIN_FAILED');
});
await test('执法：证据不可修改；摘要可复核', async () => {
  await api.enforcement.login('zf031207', 'Shifan@2026');
  const s = await api.enforcement.scan('无码车'); eq(s.scanResult, '无政府码'); ok(s.needCapture);
  const s2 = await api.enforcement.scan('SF71009215'); eq(s2.scanResult, '码已失效');
  const loc = await api.enforcement.locate();
  const e = await api.enforcement.capture({ scanResult: '无政府码', ...loc, vehicleDescription: '车身"途迈"字样，无政府二维码', photos: ['photos/evidence-1.svg'] });
  await rejects(api.enforcement.update(e.evidenceId), 'EVIDENCE_LOCKED');
  await loginAdmin('zgbm.zhou');
  ok((await api.admin.evidence.verify(e.evidenceId)).ok, '新证据摘要');
  ok((await api.admin.evidence.verify('EV20260923001')).ok, '原始证据摘要');
  const ex = await api.admin.evidence.export({ scanResult: '无政府码' }); ok(ex.csv.includes(e.evidenceId));
});

/* ================= 10 留痕、强制结束、解冻 ================= */
group = '10 留痕';
await test('强制结束订单：按客服确认的时刻计费、锁车、留痕（R-13）', async () => {
  await loginAdmin('kf.zhong');
  const o = await api.admin.cs.forceEnd('OD2026120111070', { billedEndAt: '2026-12-01 08:02:40', stationId: 'ST0032', ticketId: 'TK202612010033' }, '用户已在站点内锁车，运营商未上报，按定位静止时刻结束');
  eq([o.status, o.endMethod, o.billedMinutes], ['已结束', '客服强制结束', 22]);
  ok(db().audit.some(a => a.action === '强制结束订单' && a.targetId === 'OD2026120111070'));
  eq(db().tickets.find(t => t.ticketId === 'TK202612010033').status, '已解决');
});
await test('人工解冻须写事由并留痕；自动解冻在结清后发生', async () => {
  await rejects(api.admin.cs.unfreeze('U101761', '短'), 'REASON_REQUIRED');
  await api.admin.cs.unfreeze('U101761', '用户补充材料核实已满 16 周岁');
  ok(db().audit.some(a => a.action === '解冻账户' && a.targetId === 'U101761'));
  await loginUser('U100712');
  await api.user.payArrears();
  eq(db().users.users.find(u => u.userId === 'U100712').account.status, '正常');
  ok(db().audit.some(a => a.action === '解冻账户' && a.targetId === 'U100712'));
});
await test('留痕查询：按人、类型、时间筛选', async () => {
  await loginAdmin('zgbm.zhou');
  const r = await api.admin.audit.list({ action: '新增规则版本', pageSize: 100 }); ok(r.rows.length >= 4 && r.rows.every(x => x.action === '新增规则版本'));
  const r2 = await api.admin.audit.list({ accountId: 'AC0901', pageSize: 100 }); ok(r2.rows.every(x => x.actorKind === '智能体'));
});
await test('接入：符合性测试全部通过 → 真车联调报告 → 接入负责人确认 → 码启用、车辆可租', async () => {
  await loginAdmin('gjcd.li');
  const run = await api.admin.operator.integration.startRun('BR05'); eq(run.summary.failed, 0);
  await api.admin.operator.integration.submitRealRides('BR05');
  await loginAdmin('yw.he');
  await rejects(api.admin.ops.acceptance.confirm('IN-BR05', '值班人尝试确认验收'), 'FORBIDDEN');
  await loginAdmin('yw.mao');
  await api.admin.ops.acceptance.confirm('IN-BR05', '报告完整，确认上线');
  const v = db().vehicles.find(x => x.vehicleId === 'VH001151'); eq([v.codeStatus, v.status], ['已启用', '可用']);
  await loginUser('U100341'); const s = await api.user.scan('SF27239795'); ok(s.check.ok);
});
await test('运营商上传清单 → 发码 → 执法可扫、用户可骑', async () => {
  await loginAdmin('qhcx.he');
  await rejects(api.admin.operator.vehicles.upload([{ brandId: 'BR03', plateNo: 'CL000001', model: 'x', pileTag: 'E28011AAAAAAAAAAAAAAAAAA' }]), 'VEHICLE_LIST_INVALID');
  const r = await api.admin.operator.vehicles.upload([{ brandId: 'BR01', plateNo: 'QH990001', model: '青禾 Q3 电单车', pileTag: 'E28011F4AAAABBBBCCCCDDDD' }]);
  const code = r.vehicles[0].govCode;
  eq((await api.enforcement.scan(code)).scanResult, '有效政府码');
  ok((await api.user.scan(code)).check.ok);
  ok(db().audit.some(a => a.action === '发放政府码' && a.accountId === 'AC0900'));
  eq(new Set(db().vehicles.map(v => v.vehicleId)).size, db().vehicles.length, '车辆编号不重复');
  ok(/^VH\d{6}$/.test(r.vehicles[0].vehicleId) && r.vehicles[0].vehicleId > 'VH001210', r.vehicles[0].vehicleId);
});
await test('运行智能体低于门槛只建议暂停；运维确认后才暂停；指标恢复自动回到已接入（R-43）', async () => {
  const st = () => db().operators.brands.find(b => b.brandId === 'BR03').accessStatus;
  const r = await api.system.setBrandHealth('BR03', { unlockSuccessRate: 0.85, availability: 0.9 });
  eq([r.action, st()], ['建议暂停可租', '已接入']);
  ok(db().ops.alerts.some(a => a.type === '暂停可租建议'));
  await loginAdmin('yw.he');
  ok((await api.admin.me()).todos.some(t => t.key === 'suspend'));
  const acc = await api.admin.ops.acceptance.list(); ok(acc.find(i => i.brandId === 'BR03').suspendSuggestion, '接入验收页带出建议');
  await api.admin.ops.confirmSuspend(r.suggestion.id, '核实开锁成功率确实偏低，同意暂停');
  eq(st(), '暂停可租');
  ok(db().audit.some(a => a.action === '暂停可租' && a.accountId === 'AC0401'), '留痕记真实运维');
  await rejects(api.user.unlock(pickV('BR03').govCode), 'BRAND_PAUSED');
  await api.system.setBrandHealth('BR03', { unlockSuccessRate: 0.97, availability: 0.9 });
  eq(st(), '已接入');
  const r2 = await api.system.setBrandHealth('BR01', { unlockSuccessRate: 0.80 });
  await api.admin.ops.dismissSuspend(r2.suggestion.id, '运营商已确认是短时网络抖动');
  eq(db().operators.brands.find(b => b.brandId === 'BR01').accessStatus, '已接入');
  await api.admin.ops.setAutoSuspend(true, '夜间无人值守时启用自动暂停');
  eq((await api.system.setBrandHealth('BR01', { unlockSuccessRate: 0.80 })).action, '暂停可租');
  await api.system.setBrandHealth('BR01', { unlockSuccessRate: 0.99 });
  await api.admin.ops.setAutoSuspend(false, '恢复为人工确认');
});
await test('值班人手动进入/退出降级：留痕记真实值班人与事由', async () => {
  await loginAdmin('yw.gu');
  const ev = await api.system.setDegrade({ on: true, preset: '支付通道故障', reason: '收单渠道通知微信支付回调异常' });
  eq(ev.enteredBy, 'AC0402');
  const au = db().audit.filter(a => a.action === '进入降级').pop(); eq([au.accountId, au.reason], ['AC0402', '收单渠道通知微信支付回调异常']);
  const ex = await api.system.setDegrade({ on: false, reason: '渠道确认已恢复' });
  eq(ex.exitedBy, 'AC0402');
  eq(db().audit.filter(a => a.action === '退出降级').pop().accountId, 'AC0402');
  await rejects(api.system.setDegrade({ on: true, preset: '支付通道故障', by: 'AC0201' }), 'FORBIDDEN');
});
await test('修改密码：旧密码校验、强度校验、留痕；新密码可登录', async () => {
  await loginAdmin('cw.jiang');
  await rejects(api.admin.changePassword('wrong-old', 'Abcd12345'), 'LOGIN_FAILED');
  await rejects(api.admin.changePassword('Shifan@2026', 'abcdefgh'), 'INVALID');
  await api.admin.changePassword('Shifan@2026', 'Caiwu2027ok');
  await api.admin.logout();
  await rejects(api.admin.login('cw.jiang', 'Shifan@2026'), 'LOGIN_FAILED');
  await api.admin.login('cw.jiang', 'Caiwu2027ok');
  ok(db().audit.some(a => a.action === '修改密码' && a.targetId === 'AC0202'));
});
await test('桩登记编号递增不重复；站点有桩时不能删除，空站点删除后不参与还车判定', async () => {
  await loginAdmin('zgbm.ye');
  const p1 = await api.admin.piles.create({ stationId: 'ST0003', location: db().stations.stations.find(s => s.stationId === 'ST0003').center }, '新增桩位一个');
  const p2 = await api.admin.piles.create({ stationId: 'ST0003', location: db().stations.stations.find(s => s.stationId === 'ST0003').center }, '新增桩位一个');
  ok(p1.pileId !== 'PL00001' && p2.pileId > p1.pileId, `${p1.pileId} ${p2.pileId}`);
  eq(new Set(db().stations.piles.map(p => p.pileId)).size, db().stations.piles.length, '桩编号不重复');
  const e = await rejects(api.admin.stations.remove('ST0003', '站点合并撤除'), 'STATION_IN_USE'); ok(/桩/.test(e.message));
  const fence = [[104.0405, 30.5905], [104.0412, 30.5905], [104.0412, 30.5910], [104.0405, 30.5910]];
  const ns = await api.admin.stations.create({ name: '测绘临时点', district: '西城区', type: '无桩', fence, capacity: 8 }, '临时停放点');
  await api.admin.stations.remove(ns.stationId, '临时停放点撤销');
  ok(!(await api.admin.stations.list({ pageSize: 500 })).rows.some(s => s.stationId === ns.stationId));
  ok(!(await api.user.stations()).stations.some(s => s.stationId === ns.stationId), '用户端地图不再显示');
  ok(db().audit.some(a => a.targetId === ns.stationId && /删除/.test(a.summary)));
});
await test('主管部门只读开锁成功率；用户端站点接口', async () => {
  await loginAdmin('zgbm.zhou');
  const m = await api.admin.ops.monitor({ hours: 24 });
  ok(m.limited && m.brands.length && m.brands[0].latest && m.brands[0].latest.unlockSuccessRate != null);
  ok(!('degrade' in m) && !('alerts' in m), '不开放其他运维信息');
  await rejects(api.admin.ops.degrade.status(), 'FORBIDDEN');
  ok((await api.admin.access.overview()).brands.find(b => b.brandId === 'BR01').unlockSuccessRate > 0);
  const g2 = await api.user.stations();
  ok(g2.stations.length >= 60 && g2.piles.length >= 402 && g2.stations[0].fence.length >= 4 && !('occupiedVehicleId' in g2.piles[0]));
});
await test('联调凭证轮换、吊销（吊销后不能测试）；模拟支付切换', async () => {
  await loginAdmin('gjcd.li');
  const r = await api.admin.operator.integration.rotateCredential('BR05', '定期轮换联调密钥');
  ok(r.secret && r.credentials.sandboxKeyMasked.endsWith(r.secret.slice(-4)));
  await api.admin.operator.integration.setMockPayment('余额不足', '测欠费路径');
  eq((await api.admin.operator.integration.sandbox()).paymentMode, '余额不足');
  await rejects(api.admin.operator.integration.setMockPayment('随便'), 'INVALID');
  await rejects(api.admin.operator.integration.rotateCredential('BR01', '越权轮换他家'), 'SCOPE_DENIED');
  await api.admin.operator.integration.revokeCredential('BR05', '人员离职吊销');
  await rejects(api.admin.operator.integration.startRun('BR05'), 'CREDENTIAL_DISABLED');
  ok(db().audit.some(a => a.action === '吊销联调凭证') && db().audit.some(a => a.action === '轮换联调凭证'));
});
await test('客服按类型列出冻结账户', async () => {
  await loginAdmin('kf.lu');
  const r = await api.admin.cs.listFrozenAccounts({ pageSize: 100 });
  for (const id of ['U101014', 'U101084', 'U101525', 'U101984', 'U102038']) ok(r.rows.some(x => x.userId === id && x.kind === '欠费' && x.autoUnfreeze), id);
  const k = await api.admin.cs.listFrozenAccounts({ kind: '核验' }); ok(k.rows.every(x => x.kind === '核验'));
  eq(r.counts.欠费 + r.counts.核验 + r.counts.其他, r.total);
  await loginAdmin('qhcx.he'); await rejects(api.admin.cs.listFrozenAccounts(), 'FORBIDDEN');
});
await test('所有写操作都有留痕（本次运行新增的留痕类型）', () => {
  const kinds = new Set(db().audit.filter(a => a.at >= '2026-12-01 14:00:00').map(a => a.action));
  for (const k of ['新增规则版本', '进入降级', '退出降级', '收紧降级', '抵扣押金', '申请退押', '确认退款', '确认超额退款', '人工转账', '强制结束订单', '解冻账户', '调整结算单', '审核结算单', '发送结算单', '登记到账', '生成结算单', '处理异议', '确认验收', '暂停可租', '恢复可租', '导出证据', '提交车辆清单', '发放政府码', '冻结账户', '注销账户', '闭合订单']) ok(kinds.has(k), `缺少留痕：${k}`);
});

/* ================= 11 跨端联动与持久 ================= */
group = '11 联动与持久';
await test('另一端（同源另一标签页）实时收到订单与改价事件，并读到新数据', async () => {
  const B = makeEnv({ start: fmt(A1.clock.t), kv, local, channel: bus.make() });
  const api2 = createApi(B.env);
  await api2.ready();
  const got = [];
  api2.on('*', e => got.push(e.name));
  await loginUser('U100341');
  const v = pickV('BR01');
  const r = await api.user.unlock(v.govCode);
  await new Promise(res => setTimeout(res, 20));
  ok(got.includes('order.created'), `收到事件：${got.join(',')}`);
  await api2.system.debug.switchUser('U100341');
  const ride = await api2.user.ride(); eq(ride.orderId, r.orderId);
  await api.user.returnBike({ method: 'lock', stationId: 'ST0002' });
});
await test('关闭再打开：新实例从本地存储读到运行中产生的数据', async () => {
  const B = makeEnv({ start: fmt(A1.clock.t), kv, local, channel: null });
  const api3 = createApi(B.env);
  await api3.ready();
  ok(api3._ctx.db.orders.length === db().orders.length && api3._ctx.db.pricing.versions.length === db().pricing.versions.length);
});
await test('非调试模式：调试接口与重置被拒绝', async () => {
  const B = makeEnv({ start: fmt(A1.clock.t), kv, local, channel: null, debug: false });
  const api4 = createApi(B.env);
  await rejects(api4.system.reset(), 'DEBUG_ONLY');
  await rejects(api4.system.debug.switchUser('U100341'), 'DEBUG_ONLY');
});

/* ================= 12 时间平移 ================= */
group = '12 时间平移';
await test('上午打开：整体平移到不晚于现在；编号与月份标签不平移；证据摘要仍可复核', async () => {
  const B = makeEnv({ start: '2026-11-17 09:00:00', kv: memKv(), local: memLocal(), channel: null });
  const a = createApi(B.env); await a.ready();
  const c = a._ctx;
  eq(c.meta.shiftDays, -15);
  const o = c.db.orders.find(x => x.orderId === 'OD2026103112719');
  eq(o.settleMonth, '2026-11'); ok(o.endAt.startsWith('2026-10-17'), o.endAt);
  const latest = c.db.orders.map(x => x.requestAt).sort().pop(); ok(latest <= '2026-11-17 09:00:00', latest);
  const e = c.db.evidence.evidence[0]; eq(await evidenceHash(e, c.label), e.sha256);
  await a.system.debug.switchUser('U100341');
  const v = c.db.vehicles.find(x => x.brandId === 'BR01' && x.status === '可用');
  const r = await a.user.unlock(v.govCode); ok(r.orderId.startsWith('OD20261202'), `新订单编号用标签日期：${r.orderId}`);
});

/* ================= 13 种子数据与到点任务 ================= */
group = '13 种子与到点';
await test('首次载入不触发冻结（欠费超期用户在种子里已冻结）；退款失败可直接人工转账', async () => {
  const B = makeEnv({ start: '2026-12-01 12:00:01', kv: memKv(), local: memLocal(), channel: null });
  const a = createApi(B.env); await a.ready();
  const n0 = a._ctx.db.audit.length;
  await a.system.time();
  eq(a._ctx.db.audit.slice(n0).filter(x => x.action === '账户冻结').map(x => x.targetId), [], '首次载入不应现冻');
  for (const id of ['U101014', 'U101084', 'U101525', 'U101984', 'U102038']) eq(a._ctx.db.users.users.find(u => u.userId === id).account.status, '冻结', id);
  await a.admin.login('cw.lv', 'Shifan@2026');
  const f = await a.admin.finance.refunds.failures();
  const r = f.find(x => x.type === '退款'); ok(r && r.status === '待转账' && r.payeeSubmitted.account, JSON.stringify(f));
  await a.admin.finance.refunds.transfer(r.paymentId, {}, '原路退回失败，按用户收款账户转账');
  eq(a._ctx.db.orders.find(o => o.orderId === r.orderId).refundStatus, '全额退款');
  ok(/E28011[0-9A-F]{18}/.test(a._ctx.db.integration.spec.sections.find(s => s.no === '2').example), '参考报文车辆标识 24 位');
});
await test('财务未手动生成时，api.system.time() 到次月 1 日 00:30 自动出单', async () => {
  const B = makeEnv({ start: '2026-12-01 14:00:00', kv: memKv(), local: memLocal(), channel: null });
  const a = createApi(B.env); await a.ready();
  await a.system.debug.switchUser('U100341');
  const v = a._ctx.db.vehicles.find(x => x.brandId === 'BR01' && x.status === '可用' && x.codeStatus === '已启用');
  const o = await a.user.unlock(v.govCode); B.clock.t += 8 * 60e3;
  await a.user.returnBike({ method: 'lock', stationId: 'ST0002' });
  const n = a._ctx.db.settlements.settlements.length;
  B.clock.t = new Date('2027-01-01T00:29:00').getTime(); await a.system.time();
  eq(a._ctx.db.settlements.settlements.length, n, '00:30 前不出单');
  B.clock.t = new Date('2027-01-01T00:31:00').getTime(); await a.user.me();
  eq(a._ctx.db.settlements.settlements.length, n, '其他接口不触发');
  await a.system.time();
  const s = a._ctx.db.settlements.settlements.slice(n);
  ok(s.length && s.every(x => x.month === '2026-12' && x.generatedAt === '2027-01-01 00:30:00'));
  ok(s.find(x => x.operatorId === 'OP01').details.some(d => d.orderId === o.orderId));
});

/* ---------------- 输出 ---------------- */
const pass = results.filter(r => r.ok).length;
const byGroup = {};
for (const r of results) { byGroup[r.group] = byGroup[r.group] || [0, 0]; byGroup[r.group][r.ok ? 0 : 1]++; }
console.log('\nmock 接口层自测');
for (const [g2, [p, f]] of Object.entries(byGroup)) console.log(`  ${g2.padEnd(14, ' ')} 通过 ${p}  失败 ${f}`);
for (const r of results.filter(x => !x.ok)) console.log(`\n✗ [${r.group}] ${r.name}\n  ${String(r.err).split('\n').slice(0, 4).join('\n  ')}`);
console.log(`\n合计 ${results.length} 项，通过 ${pass}，失败 ${results.length - pass}`);
process.exit(pass === results.length ? 0 : 1);

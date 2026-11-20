// 真实接口适配的联调自测（需本机后端与网关已启动并灌过种子：bash deploy/local/seed.sh）。
//   node 原型/shared/real/_自测/live.mjs [网关地址，默认 http://127.0.0.1:18300]
// 走 api.user / api.admin / api.enforcement 的真实适配，验证主链路：授权→实名→免押/缴押→扫码→开锁→骑行中→还车→账单→支付→订单；管理端登录与列表。
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { createApi } from '../../api.js';
import { createRealApi } from '../index.js';
import { seeded } from '../../mock/util.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const dataDir = path.resolve(here, '../../../mockdata');
const loadJson = async n => JSON.parse(await readFile(path.join(dataDir, n + '.json'), 'utf8'));
const memKv = () => { const m = new Map(); return { getMany: async ks => Object.fromEntries(ks.map(k => [k, m.has(k) ? structuredClone(m.get(k)) : undefined])), setMany: async o => { for (const [k, v] of Object.entries(o)) m.set(k, structuredClone(v)); }, clear: async () => m.clear() }; };
const memLocal = () => { const m = new Map(); return { get: k => (m.has(k) ? m.get(k) : null), set: (k, v) => m.set(k, String(v)), remove: k => m.delete(k) }; };

const BASE = process.argv[2] || 'http://127.0.0.1:18300';
const mock = createApi({ loadJson, kv: memKv(), local: memLocal(), channel: null, debug: false, latency: [0, 0], random: seeded(1), now: () => Date.now(), onStorage: null });
const store = memLocal();
const api = createRealApi(mock, { base: BASE, store, mirror: false });

let pass = 0, fail = 0;
const step = async (name, fn) => {
  try { const r = await fn(); pass++; console.log('  ok  ', name, r === undefined ? '' : '-> ' + String(JSON.stringify(r)).slice(0, 160)); return r; }
  catch (e) { fail++; console.log('  FAIL', name, '->', e.code || '', e.message); return null; }
};
const eq = (a, b, m) => { if (a !== b) throw new Error(`${m}：期望 ${b}，实际 ${a}`); };
const mustFail = async (p, code) => { try { await p; } catch (e) { eq(e.code, code, '错误码'); return e; } throw new Error('应失败但成功了'); };
const OP = 'http://127.0.0.1:18390'; // deploy/local/mock-operator.mjs（只发场景指令，不重启）
const opPost = (p, body) => fetch(OP + p, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) }).then(r => r.json());
const phone = () => '139' + String(Math.floor(Math.random() * 1e8)).padStart(8, '0');

console.log('== 用户端：免押用户');
await step('未登录 me() 为 null', async () => eq(await api.user.me(), null, 'me'));
await step('authorize 新用户', async () => { const r = await api.user.authorize({ channel: '微信', phone: phone() }); eq(r.isNew, true, 'isNew'); return r.user.userId; });
await step('verifyRealName', async () => { const r = await api.user.verifyRealName({ name: '王五', idNo: '110101199003071233' }); eq(r.status, '已核验', 'status'); eq(r.user.realName.status, '已核验', 'user'); });
await step('openCreditFree', async () => { const r = await api.user.openCreditFree({ provider: '微信支付分' }); eq(r.user.deposit.mode, '免押', 'mode'); return r.score; });
await step('stations 站点与桩', async () => { const s = await api.user.stations(); if (!s.stations.length) throw new Error('没有站点，先跑 seed.sh'); return s.stations.length; });
await step('scan 扫码确认（金额为元；不造电量）', async () => { const r = await api.user.scan('SF74087774'); eq(r.check.ok, true, 'check'); eq(r.vehicle.battery ?? null, null, '电量不应造假'); eq(r.price.startFee, 1.5, '起步价应为元'); eq(r.price.deposit, 199, '押金应为元'); return r.price.text; });
await step('nearby 附近车（不造位置）', async () => { const l = await api.user.nearby({ location: [104.066, 30.596], radiusM: 800 }); return l.length ? { n: l.length, first: Object.keys(l[0]) } : { n: 0 }; });
const ride = await step('unlock 开锁（订单带 unlockLocation）', async () => { const r = await api.user.unlock('SF74087774'); eq(r.status, '骑行中', 'status'); eq(typeof r.liveEstimate, 'number', 'liveEstimate'); return { orderId: r.orderId, unlock: r.unlockLocation, battery: r.battery }; }).then(x => x && x.orderId);
await step('me 带 ridingOrderId', async () => { const u = await api.user.me(); eq(u.ridingOrderId, ride, 'ridingOrderId'); });
await step('ride 骑行中（实时费用）', async () => { const r = await api.user.ride(); eq(r.status, '骑行中', 'status'); return { elapsedSec: r.elapsedSec, est: r.liveEstimate }; });
await step('ride 的定位字段（有则为数组，没有为 null）', async () => { const r = await api.user.ride(); return { cur: r.currentLocation, start: r.startLocation, battery: r.battery }; });
await step('returnPreview 站内', async () => { const r = await api.user.returnPreview({ location: [104.066, 30.596] }); eq(r.inStation, true, 'inStation'); return r.text; });
await step('returnBike 桩旁还车（指定站点与桩）', async () => { const r = await api.user.returnBike({ method: 'app', stationId: 'ST0001', pileId: 'PL00001', location: [104.0658, 30.596] }); eq(r.order.status, '已结束', 'status'); return { collect: r.collect, payable: r.order.fee && r.order.fee.payable, basis: r.order.inStationBasis, endAt: r.order.endAt }; });
await step('ride 结束后为 null', async () => eq(await api.user.ride(), null, 'ride'));
await step('orders 订单列表（含账单金额）', async () => { const l = await api.user.orders({ pageSize: 5 }); eq(l.rows[0].orderId, ride, '最新单'); return { total: l.total, fee: l.rows[0].fee, statusText: l.rows[0].statusText }; });
await step('order 订单详情（含支付流水）', async () => { const o = await api.user.order(ride); return { bill: o.bill && o.bill.headline, payments: o.payments.length }; });
await step('notices 通知', async () => (await api.user.notices()).length);
await step('logout', async () => { await api.user.logout(); eq(await api.user.me(), null, 'me'); });

console.log('== 用户端：押金用户，待支付到一键支付');
await api.user.authorize({ channel: '支付宝', phone: phone() });
await step('未实名开锁被拦', async () => { const e = await mustFail(api.user.unlock('SF81063428'), 'NEED_ONBOARDING'); eq(e.charged, false, 'charged'); return e.next && e.next.action; });
await api.user.verifyRealName({ name: '赵六', idNo: '110101199003071233' });
await step('未开通押金被拦（金额为元）', async () => { const e = await mustFail(api.user.unlock('SF81063428'), 'DEPOSIT_REQUIRED'); eq(e.depositAmount, 199, 'depositAmount'); });
await step('payDeposit', async () => { const r = await api.user.payDeposit(); eq(r.amount, 199, 'amount'); eq(r.user.deposit.mode, '押金', 'mode'); });
const o2 = await step('unlock', async () => (await api.user.unlock('SF81063428')).orderId);
await step('站外还车需确认调度费', async () => { const e = await mustFail(api.user.returnBike({ method: 'app', location: [104.07, 30.60] }), 'DISPATCH_CONFIRM_REQUIRED'); eq(e.dispatchFee, 5, 'dispatchFee'); });
const plate2 = (await api.user.ride()).plateNo;
await opPost('/mock/park', { plateNo: plate2, location: [104.0745, 30.6020], inStation: false });
await step('returnBike 站外还车（确认调度费）', async () => { const r = await api.user.returnBike({ method: 'app', location: [104.07, 30.60], confirmDispatch: true }); eq(r.order.inStation, false, 'inStation'); return { collect: r.collect, basis: r.order.inStationBasis, payable: r.order.fee && r.order.fee.payable }; });
await step('orders 待付款', async () => { const l = await api.user.orders({ status: '待付款' }); eq(l.rows[0].orderId, o2, 'orderId'); eq(l.rows[0].payStatus, '待支付', 'payStatus'); return l.rows[0].fee.unpaid; });
await step('pay 一键支付（pay-intent + 轮询账单）', async () => { const t0 = Date.now(); const r = await api.user.pay(o2); eq(r.order.payStatus, '已支付', 'payStatus'); return { amount: r.amount, ms: Date.now() - t0 }; });
await step('orders 待付款 已清空', async () => eq((await api.user.orders({ status: '待付款' })).rows.length, 0, '待付款'));
await step('deposit.get', async () => (await api.user.deposit.get()).mode);

console.log('== 用户端：开锁失败（运营商场景）');
await api.user.logout();
await api.user.authorize({ channel: '微信', phone: phone() });
await api.user.verifyRealName({ name: '孙七', idNo: '110101199003071233' });
await api.user.openCreditFree({ provider: '微信支付分' });
const near3 = await api.user.nearby({ location: [104.066, 30.596], radiusM: 2000 });
const code3 = near3[near3.length - 1].govCode, plate3 = near3[near3.length - 1].plateNo;
await opPost('/mock/scenario', { plateNo: plate3, unlock: '无响应' });
await step('开锁失败：车辆无响应（未扣费）', async () => { const e = await mustFail(api.user.unlock(code3), 'UNLOCK_NO_RESPONSE'); eq(e.charged, false, 'charged'); return e.message; });
await step('订单列表里有开锁失败单', async () => { const l = await api.user.orders({ status: '开锁失败' }); eq(l.rows[0].status, '开锁失败', 'status'); return { reason: l.rows[0].unlockFailReason, statusText: l.rows[0].statusText }; });
await opPost('/mock/scenario', { plateNo: plate3, unlock: '失败', failReason: '电量不足' });
await step('开锁失败：电量不足', async () => { await mustFail(api.user.unlock(code3), 'UNLOCK_LOW_BATTERY'); });
await api.user.logout();

console.log('== 管理端');
await step('未登录 me 报 ADMIN_NOT_LOGGED_IN', async () => { await mustFail(api.admin.me(), 'ADMIN_NOT_LOGGED_IN'); });
await step('错误密码 LOGIN_FAILED', async () => { await mustFail(api.admin.login('zgbm.zhou', 'x'), 'LOGIN_FAILED'); });
await step('login 主管部门', async () => (await api.admin.login('zgbm.zhou', 'Shifan@2026')).role);
await step('me 菜单与权限', async () => { const m = await api.admin.me(); return { menus: m.menus.length, perms: m.permissions.length }; });
await step('pricing.current（元）', async () => { const p = await api.admin.pricing.current(); eq(p.ride.startFee, 1.5, 'startFee'); return p.versionId; });
await step('stations.list', async () => (await api.admin.stations.list({ pageSize: 5 })).total);
await step('access.overview 运营商与品牌', async () => { const o = await api.admin.access.overview(); return Object.keys(o); });
await step('audit.list', async () => (await api.admin.audit.list({ pageSize: 3 })).total);
await step('令牌续期 POST /api/iam/token/refresh（新令牌可用、旧令牌作废）', async () => {
  const http = api._http, old = store.get('bike.real.adminToken');
  eq(await http.refresh('admin'), true, 'refresh');
  const nu = store.get('bike.real.adminToken'); if (nu === old) throw new Error('令牌没变');
  const exp = http.tokens.expiresAt('admin'); if (!(exp > Date.now() + 25 * 60e3 && exp < Date.now() + 31 * 60e3)) throw new Error('到期时刻不对 ' + exp);
  eq((await fetch(BASE + '/api/iam/me', { headers: { Authorization: 'Bearer ' + old } })).status, 401, '旧令牌');
  await api.admin.me(); return { expiresInMin: Math.round((exp - Date.now()) / 60e3) };
});
await step('到期前 5 分钟内的请求自动续期', async () => {
  const old = store.get('bike.real.adminToken');
  store.set('bike.real.adminExp', String(Date.now() + 2 * 60e3));
  await api.admin.me();
  if (store.get('bike.real.adminToken') === old) throw new Error('没有自动续期');
  return 'ok';
});
await step('续期失败（令牌已作废）清令牌并通知回登录页', async () => {
  let lost = null;
  const h2 = (await import('../http.js')).createHttp({ base: BASE, store, autoRefresh: false, onSessionLost: w => { lost = w; } });
  const keep = store.get('bike.real.adminToken');
  store.set('bike.real.adminToken', 'bad.token.value'); store.set('bike.real.adminExp', String(Date.now() + 60e3));
  await mustFail(h2.get('/api/iam/me', { who: 'admin' }), 'ADMIN_NOT_LOGGED_IN');
  eq(lost, 'admin', 'onSessionLost'); eq(store.get('bike.real.adminToken'), null, '令牌应被清除');
  store.set('bike.real.adminToken', keep); return 'ok';
});
await api.admin.logout();
await step('login 客服，orders.list（行程加账单）', async () => { await api.admin.login('kf.su', 'Shifan@2026'); const l = await api.admin.orders.list({ pageSize: 3 }); eq(l.rows.length > 0, true, '有订单'); return { total: l.total, row: { orderId: l.rows[0].orderId, payStatus: l.rows[0].payStatus, operatorName: l.rows[0].operatorName } }; });
await step('orders.get 详情', async () => { const l = await api.admin.orders.list({ pageSize: 1 }); const o = await api.admin.orders.get(l.rows[0].orderId); return { payments: o.payments.length, tickets: o.tickets.length }; });
await api.admin.logout();
await step('login 运营商，车辆清单（仅本运营商）', async () => { await api.admin.login('qhcx.he', 'Shifan@2026'); const l = await api.admin.operator.vehicles.list({ pageSize: 20 }); const r0 = l.rows[0] || {}; eq('battery' in r0 || 'location' in r0 || 'lastReportAt' in r0, false, '车辆清单不应再有电量位置'); return { total: l.total, first: r0.plateNo, pileTag: r0.pileTag, pileTagType: r0.pileTagType }; });
await api.admin.logout();

console.log('== 执法端');
await step('login 执法账号', async () => (await api.enforcement.login('zf031207', 'Shifan@2026')).name);
await step('me', async () => (await api.enforcement.me()).badgeNo);
await step('scan', async () => (await api.enforcement.scan('SF74087774')).scanResult);
await api.enforcement.logout();

console.log(`\n通过 ${pass}，失败 ${fail}`);
process.exit(fail ? 1 : 0);

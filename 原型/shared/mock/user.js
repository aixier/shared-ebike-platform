// 用户端接口（骑行用户，微信/支付宝小程序）：api.user.*
import { ApiError } from './errors.js';
import { C, Y, ms, fmt, addSec, addDays, addWorkdays, idValid, ageAt, maskId, maskName, maskPhone, maskAccount, inPolygon, centroid, hash32, dist, paginate } from './util.js';
import * as R from './rules.js';

const LEGAL_AGE = 16; // 电动自行车法定驾驶年龄
const md = t => (t ? `${+t.slice(5, 7)}月${+t.slice(8, 10)}日 ${t.slice(11, 16)}` : '');

export function createUserApi(ctx, D, V, A) {
  const db = () => ctx.db;
  const call = (name, fn, opts) => ctx.call('user.' + name, fn, opts);
  const me = () => { const s = ctx.session.get('User'); return s ? D.user(s.userId) : null; };
  const need = () => {
    const u = me();
    if (!u) throw new ApiError('NOT_LOGGED_IN');
    if (u.account.status === '已注销') throw new ApiError('ACCOUNT_CANCELLED');
    ctx._actor = u.userId;
    return u;
  };
  const authDown = ch => D.activeEvent() && (D.degrade().components || []).some(c => c === `${ch}授权` || c === '登录授权');
  const realNameDown = () => D.activeEvent() && (D.degrade().components || []).includes('实名核验');
  const depositUnknown = () => D.activeEvent() && (D.degrade().components || []).includes('押金与免押查询');

  /* ---------------- 准入判定 ---------------- */
  function vehicleBlock(v) {
    const b = D.brand(v.brandId);
    const near = () => D.nearby(v.location, v.vehicleId);
    if (v.codeStatus === '待启用') return new ApiError('CODE_NOT_ENABLED', `这辆车的码还未启用${v.codeEnableAt ? `，${md(v.codeEnableAt)} 起可骑` : ''}`, { codeEnableAt: v.codeEnableAt, nearby: near() });
    if (b.accessStatus === '暂停可租') return new ApiError('BRAND_PAUSED', `${b.displayName}暂停可租，附近其他品牌的车可以骑`, { nearby: near() });
    if (v.codeStatus === '已失效') return new ApiError('CODE_INVALID', null, { codeInvalidAt: v.codeInvalidAt, nearby: near() });
    if (b.accessStatus !== '已接入') return new ApiError('BRAND_NOT_AVAILABLE', null, { nearby: near() });
    return null;
  }
  /** 用户能否开新单。返回 {ok, error} 或 {ok:true, admission} */
  function userCheck(u) {
    const no = e => ({ ok: false, error: e });
    if (!u) return no(new ApiError('NOT_LOGGED_IN'));
    if (u.account.status === '已注销') return no(new ApiError('ACCOUNT_CANCELLED'));
    if (u.account.status === '冻结') return no(new ApiError('ACCOUNT_FROZEN', `账户已冻结：${u.account.freeze.reason}`, { unfreezeHow: u.account.freeze.unfreezeHow }));
    const riding = D.ridingOrder(u.userId);
    if (riding) return no(new ApiError('HAS_RIDING', null, { orderId: riding.orderId }));
    const ev = D.activeEvent();
    const rn = u.realName.status;
    if (rn === '未实名' || rn === '未通过') return no(new ApiError('NEED_ONBOARDING'));
    if (rn === '待核验' && !ev) return no(new ApiError('NEED_ONBOARDING', '完成实名核验后即可骑行', { next: { label: '去核验', action: 'verifyRealName' } }));
    const arrears = C(D.recomputeArrears(u));
    const unpaid = D.unpaidOrders(u.userId).filter(o => !D.hasPendingPayment(o));
    const d = u.deposit;
    if (!ev) { // 常态
      if (arrears > 0) return no(new ApiError('HAS_ARREARS', `你有 ¥${Y(arrears).toFixed(1)} 未付的骑行费用，补付后即可开锁`, { amount: Y(arrears) }));
      if (unpaid.length) return no(new ApiError('UNPAID_ORDER', `上一单还有 ¥${unpaid[0].fee.unpaid.toFixed(1)} 未支付，支付后即可开锁`, { orderId: unpaid[0].orderId, amount: unpaid[0].fee.unpaid }));
      const dep = depositCheck(u);
      if (dep) return no(dep);
      return { ok: true, admission: null };
    }
    // 降级开单（BR-27～BR-30）
    const cur = D.degrade();
    const v = D.currentVersion();
    if (cur.stopped) return no(new ApiError('DEGRADE_STOPPED'));
    if (arrears > 0) return no(new ApiError('DEGRADE_REJECT_ARREARS', null, { amount: Y(arrears) }));
    if (unpaid.length) return no(new ApiError('UNPAID_ORDER', null, { orderId: unpaid[0].orderId, amount: unpaid[0].fee.unpaid }));
    if (d.mode === '押金' && d.status === '退押中') return no(new ApiError('DEGRADE_REJECT_REFUNDING'));
    let category;
    if (rn === '待核验') category = '先骑后验新用户';
    else if (depositUnknown()) category = '押金状态未知的老用户';
    else {
      const dep = depositCheck(u);
      if (dep) return no(dep);
      category = '老用户';
    }
    const tight = category !== '老用户';
    const limits = tight ? v.riskLimits.tight : v.riskLimits.normal;
    if (tight && (cur.tightened || D.hourCount() >= v.riskLimits.hourlyDegradedOrders)) return no(new ApiError(category === '先骑后验新用户' ? 'DEGRADE_NEW_USER_UNAVAILABLE' : 'DEGRADE_PAUSED_TIGHT'));
    if (D.openDegradedCount(u.userId) >= v.riskLimits.perUserOpenDegradedOrders) return no(new ApiError('DEGRADE_USER_LIMIT'));
    const line = '费用将稍后收取';
    const notice = category === '先骑后验新用户'
      ? `实名核验稍后完成，你可以先骑，本次最长 ${limits.maxMinutes} 分钟或 ¥${limits.maxAmount.toFixed(1)}。核验未通过将冻结账户并追缴本次费用。`
      : category === '押金状态未知的老用户' ? `费用将稍后收取。本次最长 ${limits.maxMinutes} 分钟或 ¥${limits.maxAmount.toFixed(1)}，到时请尽快还车。` : null;
    return { ok: true, admission: { category, limitMinutes: limits.maxMinutes, limitAmount: limits.maxAmount, line, notice } };
  }
  function depositCheck(u) {
    const d = u.deposit, v = D.currentVersion();
    if (d.mode === '免押') return null;
    if (d.mode === '未开通') return new ApiError('DEPOSIT_REQUIRED', `开通信用免押或缴押金 ¥${v.deposit.amount.toFixed(1)} 后即可骑行`, { depositAmount: v.deposit.amount });
    if (d.status === '已缴') return null;
    if (d.status === '押金不足') { const gap = Y(C(d.amountPaid) - C(d.balance)); return new ApiError('DEPOSIT_INSUFFICIENT', `押金余额 ¥${Number(d.balance).toFixed(1)}，补缴 ¥${gap.toFixed(1)} 或开通免押后即可骑行`, { topupAmount: gap }); }
    if (d.status === '退押中') return new ApiError('DEPOSIT_REFUNDING');
    return new ApiError('DEPOSIT_REQUIRED', `开通信用免押或缴押金 ¥${v.deposit.amount.toFixed(1)} 后即可骑行`, { depositAmount: v.deposit.amount });
  }
  const findVehicle = code => {
    const s = String(code || '').trim();
    const m = /SF\d{8}/i.exec(s);
    if (m) return ctx.find(db().vehicles, 'govCode', m[0].toUpperCase());
    if (/^VH\d{6}$/.test(s)) return D.vehicle(s);
    return null;
  };
  const vehicleView = v => {
    const b = D.brand(v.brandId);
    return { vehicleId: v.vehicleId, govCode: v.govCode, plateNo: v.plateNo, model: v.model, vehicleType: v.vehicleType, battery: v.battery, location: v.location, stationId: v.stationId, stationName: V.stationName(v.stationId), brandId: b.brandId, brandName: b.name, displayName: b.displayName, color: b.color };
  };
  const errInfo = e => (e ? { ok: false, ...e.toJSON() } : { ok: true });

  /* ---------------- 骑行状态（实时计费） ---------------- */
  function rideView(o) {
    const v = D.versionsById()[o.pricingVersionId];
    const sec = Math.max(0, Math.round((ctx.nowMs() - ms(o.startAt)) / 1000));
    const mins = R.billedMinutes(sec);
    const ride = R.rideFeeC(v, mins);
    const card = R.cardFor(db().users.monthCards, o.userId, o.startAt);
    const cd = card ? Math.min(C(card.perOrderCap), ride) : 0;
    const veh = D.vehicle(o.vehicleId);
    const loc = currentLocation(o);
    const near = R.nearestStation(db().stations.stations, loc);
    const dg = o.degraded;
    return {
      ...V.userOrder(o), elapsedSec: sec, liveBilledMinutes: mins, liveRideFee: Y(ride), liveMonthCardDeduct: Y(cd), liveEstimate: Y(ride - cd),
      price: R.priceSummary(v), battery: veh ? veh.battery : null, currentLocation: loc,
      nearestStation: near ? { stationId: near.station.stationId, name: near.station.name, type: near.station.type, distanceM: near.distanceM, center: near.station.center, freePiles: R.freePiles(db().stations.piles, near.station.stationId).length } : null,
      overLimit: dg ? { reached: !!dg.overLimitRemindedAt || sec > dg.limitMinutes * 60 || ride > C(dg.limitAmount), remindedAt: dg.overLimitRemindedAt, text: `本次最长 ${dg.limitMinutes} 分钟或 ¥${Number(dg.limitAmount).toFixed(1)}，到时请尽快还车，骑行不会被中断` } : null,
    };
  }
  /** 骑行中车辆的当前位置（原型按订单号确定一个离开锁点数百米的位置） */
  function currentLocation(o) {
    const h = hash32(o.orderId);
    const ang = (h % 360) * Math.PI / 180, r = 150 + (h % 250);
    const base = o.startLocation || D.vehicle(o.vehicleId).location;
    return [+(base[0] + r * Math.cos(ang) / (111320 * Math.cos(base[1] * Math.PI / 180))).toFixed(6), +(base[1] + r * Math.sin(ang) / 110540).toFixed(6)];
  }
  const insidePoint = st => (inPolygon(st.center, st.fence) ? st.center : centroid(st.fence));

  /* ================= 接口 ================= */
  const api = {};

  /** 站点、围栏与桩（用户端地图用；不需要登录）。桩只给是否空闲，不给车辆信息 */
  api.stations = () => call('stations', () => {
    const st = db().stations;
    const live = st.stations.filter(s => s.status !== '已停用');
    const ids = new Set(live.map(s => s.stationId));
    return {
      region: st.region,
      stations: live.map(s => ({ stationId: s.stationId, name: s.name, district: s.district, type: s.type, center: s.center, fence: s.fence, capacity: s.capacity, freePiles: R.freePiles(st.piles, s.stationId).length, pileCount: st.piles.filter(p => p.stationId === s.stationId).length })),
      piles: st.piles.filter(p => ids.has(p.stationId)).map(p => ({ pileId: p.pileId, stationId: p.stationId, no: p.no, location: p.location, status: p.status, occupied: !!p.occupiedVehicleId })),
    };
  }, { latency: [120, 300] });

  /** 当前登录的用户（未登录返回 null） */
  api.me = () => call('me', () => { const u = me(); return u ? V.user(u) : null; });

  /** 授权登录（微信或支付宝授权手机号）。手机号已注册则登录，否则注册新用户 */
  api.authorize = ({ channel = '微信', phone } = {}) => call('authorize', () => {
    if (!['微信', '支付宝'].includes(channel)) throw new ApiError('INVALID', '只支持微信或支付宝授权');
    if (authDown(channel)) throw new ApiError('AUTH_UNAVAILABLE');
    let p = String(phone || '').replace(/\D/g, '');
    if (!p) p = '1' + ['35', '36', '37', '38', '39', '50', '51', '58', '59', '86', '87', '88', '89'][ctx.randInt(0, 12)] + String(ctx.randInt(10000000, 99999999));
    if (!/^1[3-9]\d{9}$/.test(p)) throw new ApiError('INVALID', '手机号格式不正确');
    let u = db().users.users.find(x => x.phone === p && x.account.status !== '已注销');
    let isNew = false;
    if (!u) {
      const t = ctx.now();
      u = {
        userId: ctx.ids.user(), name: null, phone: p, phoneMasked: maskPhone(p), channel, registeredAt: t, persona: '新用户·运行时注册',
        realName: { status: '未实名', verifiedAt: null, idMasked: null, adult: null, method: null },
        deposit: { mode: '未开通' }, arrears: 0, account: { status: '正常', freeze: null, history: [] }, cancelledAt: null,
        degradedSignupEventId: null, lastActiveAt: t,
      };
      db().users.users.push(u);
      ctx.dirty('users');
      isNew = true;
      ctx.emit('user.created', { userId: u.userId });
    }
    ctx.session.set('User', { userId: u.userId, channel });
    u.lastActiveAt = ctx.now(); ctx.dirty('users');
    return { user: V.user(u), isNew };
  });
  api.logout = () => call('logout', () => { ctx.session.set('User', null); return { ok: true }; });

  /** 实名核验（R-03）。实名服务故障且处于降级时进入先骑后验（BR-34） */
  api.verifyRealName = ({ name, idNo } = {}) => call('verifyRealName', () => {
    const u = need();
    if (u.realName.status === '已核验') throw new ApiError('ALREADY_DONE', '你已完成实名核验');
    const nm = String(name || '').trim();
    const id = String(idNo || '').trim().toUpperCase();
    if (!/^[一-龥·]{2,20}$/.test(nm)) throw new ApiError('INVALID', '请填写证件上的中文姓名');
    if (!idValid(id)) throw new ApiError('REALNAME_INVALID_ID');
    if (ctx.takeInjection('realname.mismatch')) throw new ApiError('REALNAME_NAME_MISMATCH');
    const t = ctx.now();
    const birth = `${id.slice(6, 10)}-${id.slice(10, 12)}-${id.slice(12, 14)}`;
    if (realNameDown()) {
      if (u.realName.status === '待核验') return { status: '待核验', deferred: true, deadline: u.realName.deadline };
      const ev = D.activeEvent();
      u.name = maskName(nm);
      u.realName = { status: '待核验', verifiedAt: null, idMasked: maskId(id), adult: null, method: '先骑后验（降级期间注册）', deadline: addDays(t, 7).slice(0, 10) + ' 23:59:59', _birth: birth };
      u.degradedSignupEventId = ev.eventId;
      ctx.dirty('users');
      const lim = D.currentVersion().riskLimits.tight;
      return { status: '待核验', deferred: true, deadline: u.realName.deadline, limitMinutes: lim.maxMinutes, limitAmount: lim.maxAmount, text: `实名核验稍后完成，你可以先骑，本次最长 ${lim.maxMinutes} 分钟或 ¥${lim.maxAmount.toFixed(1)}。核验未通过将冻结账户并追缴本次费用。` };
    }
    const age = ageAt(id, t);
    const wasPending = u.realName.status === '待核验';
    u.name = maskName(nm);
    if (age < LEGAL_AGE) {
      u.realName = { status: '未通过', verifiedAt: t, idMasked: maskId(id), adult: false, method: wasPending ? '先骑后验补验' : '在线实名核验', failReason: `未达法定驾驶年龄（${LEGAL_AGE} 周岁）` };
      if (wasPending || D.userOrders(u.userId).some(o => o.status !== '开锁失败')) {
        D.pursue(u);
        D.freeze(u, `先骑后验核验未通过（未达法定驾驶年龄（${LEGAL_AGE} 周岁））`, '完成实名核验（达到法定年龄）并结清欠费后自动解冻');
      }
      ctx.dirty('users');
      throw new ApiError('UNDER_AGE', `未满 ${LEGAL_AGE} 周岁，不能骑行电动自行车`);
    }
    const first = !u.realName.verifiedAt && !wasPending;
    u.realName = { status: '已核验', verifiedAt: t, idMasked: maskId(id), adult: true, method: wasPending ? '先骑后验补验' : '在线实名核验' };
    if (first && !db().users.coupons.some(c => c.userId === u.userId && c.templateId === 'CPT01')) D.issueCoupon(u, 'CPT01');
    D.autoUnfreeze(u);
    ctx.dirty('users');
    return { status: '已核验', deferred: false, user: V.user(u) };
  });

  /** 开通信用免押（R-16）。信用分达到当前规则门槛才可开通 */
  api.openCreditFree = ({ provider } = {}) => call('openCreditFree', () => {
    const u = need();
    if (u.realName.status !== '已核验') throw new ApiError('NEED_ONBOARDING', '完成实名核验后即可开通免押');
    if (u.deposit.mode === '免押') throw new ApiError('ALREADY_DONE', '你已开通信用免押');
    if (u.deposit.mode === '押金' && ['已缴', '押金不足'].includes(u.deposit.status)) throw new ApiError('REFUND_BLOCKED', `开通免押前请先申请退还押金 ¥${Number(u.deposit.balance).toFixed(1)}`, { next: { label: '申请退押', action: 'refundDeposit' } });
    const v = D.currentVersion();
    const prov = provider || (u.channel === '支付宝' ? '芝麻信用' : '微信支付分');
    let score = 560 + (hash32(u.userId + u.phone) % 240);
    if (ctx.takeInjection('credit.low')) score = v.creditFree.threshold - 1 - (hash32(u.userId) % 60);
    if (score < v.creditFree.threshold) throw new ApiError('CREDIT_SCORE_LOW', `${prov}暂未达到免押条件（需 ${v.creditFree.threshold} 分），缴押金 ¥${v.deposit.amount.toFixed(1)} 也可以骑行`, { score, threshold: v.creditFree.threshold, depositAmount: v.deposit.amount });
    const prev = u.deposit.mode === '押金' ? u.deposit : null;
    u.deposit = { mode: '免押', creditOpenedAt: ctx.now(), provider: prov, score, ...(prev ? { previous: prev } : {}) };
    ctx.dirty('users');
    return { user: V.user(u), score, threshold: v.creditFree.threshold };
  });

  /** 缴押金（R-15），金额按当前规则 */
  api.payDeposit = () => call('payDeposit', () => {
    const u = need();
    if (u.realName.status !== '已核验') throw new ApiError('NEED_ONBOARDING', '完成实名核验后即可缴押金');
    if (u.deposit.mode === '免押') throw new ApiError('ALREADY_DONE', '你已开通信用免押，不用再交押金');
    if (u.deposit.mode === '押金' && u.deposit.status !== '已退') throw new ApiError('ALREADY_DONE', '你已缴纳押金');
    const amt = D.currentVersion().deposit.amount;
    const p = D.pay({ type: '押金缴纳', account: '押金专户', userId: u.userId, amount: amt, channel: D.channelOf(u) });
    if (p.status !== '成功') throw new ApiError('PAY_DECLINED', `押金没有缴成功：${p.failReason || '渠道未返回结果'}`);
    u.deposit = { mode: '押金', amountPaid: amt, balance: amt, status: '已缴', paidAt: p.completedAt, paymentId: p.paymentId, topupPaymentIds: [], deductionPaymentIds: [], refund: null };
    ctx.dirty('users');
    return { user: V.user(u), paymentId: p.paymentId, amount: amt };
  });
  /** 补缴押金：被抵扣后补足到缴纳时金额（BR-25 不补差） */
  api.topupDeposit = () => call('topupDeposit', () => {
    const u = need();
    const d = u.deposit;
    if (d.mode !== '押金' || d.status !== '押金不足') throw new ApiError('NOTHING_TO_PAY', '押金余额充足，无需补缴');
    const gap = Y(C(d.amountPaid) - C(d.balance));
    const p = D.pay({ type: '押金补缴', account: '押金专户', userId: u.userId, amount: gap, channel: D.channelOf(u) });
    if (p.status !== '成功') throw new ApiError('PAY_DECLINED', `补缴没有成功：${p.failReason || '渠道未返回结果'}`);
    d.balance = d.amountPaid; d.status = '已缴'; d.topupPaymentIds.push(p.paymentId);
    ctx.dirty('users');
    return { user: V.user(u), paymentId: p.paymentId, amount: gap };
  });

  /** 扫码（R-04）：返回车辆、品牌、当前价格与能否开锁 */
  api.scan = code => call('scan', () => {
    const v = findVehicle(code);
    if (!v) throw new ApiError('CODE_NOT_FOUND');
    const u = me();
    const ver = D.currentVersion();
    const vb = vehicleBlock(v);
    const uc = vb ? null : userCheck(u);
    const ev = D.activeEvent();
    return {
      vehicle: vehicleView(v), brand: V.brand(D.brand(v.brandId)), price: R.priceSummary(ver),
      degrade: { active: !!ev, line: ev ? '费用将稍后收取' : null, admission: uc && uc.ok ? uc.admission : null },
      check: vb ? errInfo(vb) : uc.ok ? { ok: true } : errInfo(uc.error),
      nearby: vb ? vb.nearby : [],
    };
  });
  /** 附近可用车（半径按平台参数，默认 500 米） */
  api.nearby = ({ vehicleId, location, radiusM } = {}) => call('nearby', () => {
    const c = location || (vehicleId ? D.vehicle(vehicleId).location : null);
    if (!c) throw new ApiError('INVALID', '缺少位置');
    return D.nearby(c, vehicleId, radiusM);
  });

  /** 开锁（R-06、BR-01～BR-04）。成功返回骑行状态；失败以 ApiError 拒绝，含 charged、next、nearby */
  api.unlock = code => call('unlock', async () => {
    const v0 = findVehicle(code);
    if (!v0) throw new ApiError('CODE_NOT_FOUND');
    const vb = vehicleBlock(v0);
    if (vb) throw vb;
    const u = need();
    const uc = userCheck(u);
    if (!uc.ok) throw uc.error;
    const t0 = ctx.now();
    const ver = D.versionAt(t0);
    const inj = ctx.takeInjection('unlock.fail');
    const late = ctx.takeInjection('unlock.lateSuccess');
    // 平台请求运营商开锁（M-01），等待运营商报告（M-03）
    const slow = late || (!inj && v0.status === '离线') || (inj && inj.reason === '车辆无响应');
    await ctx.sleep(slow ? 6500 : 700 + ctx.rnd() * 900);
    const v = D.vehicle(v0.vehicleId);
    const t1 = ctx.now();
    const reason = inj ? (inj.reason || '车辆无响应') : late ? null
      : v.status === '离线' ? '车辆无响应' : v.status === '低电量' || v.battery < 10 ? '电量不足' : v.status === '故障' ? '车辆故障' : v.status === '骑行中' ? '车辆被占用' : null;
    const st = R.stationAt(db().stations.stations, v.location);
    const o = {
      orderId: ctx.ids.order(t0), userId: u.userId, vehicleId: v.vehicleId, brandId: v.brandId, operatorId: v.operatorId, channel: u.channel,
      pricingVersionId: ver.versionId, status: '骑行中', requestAt: t0, unlockResult: '成功', unlockFailReason: null, unlockReportAt: t1,
      failNotifiedAt: null, afterFailOrderId: null, startAt: t1, startStationId: st ? st.stationId : null, startLocation: v.location,
      endAt: null, lockReportAt: null, pileInAt: null, reportReceivedAt: null, lateReport: false, endMethod: null, endStationId: null, endPileId: null,
      endLocation: null, inStation: null, inStationBasis: null, vehicleSideInStation: null, distanceToStationM: null, dispatchPromptShown: false,
      durationSec: null, billedMinutes: null, distanceKm: null, billPushedAt: null, fee: null, payStatus: null, payMethod: null, arrearsPaidAt: null,
      refundStatus: null, refundFailedPaymentId: null, degraded: null, forceEnd: null, userClaimedEndAt: null, claimedStationId: null, anomalyNote: null,
      settleMonth: null, settlement: null, settlementNote: null, ticketIds: [], disputeIds: [],
    };
    const lastFail = D.userOrders(u.userId).filter(x => x.status === '开锁失败' && ms(x.requestAt) > ctx.nowMs() - 15 * 60e3).pop();
    db().orders.push(o);
    ctx.dirty('orders');
    if (late) { // BR-03：超时后才报告开锁成功 -> 立即请求结束，0 元
      Object.assign(o, { unlockResult: '超时后成功', failNotifiedAt: t1, status: '骑行中' });
      D.finishRide(o, { endAt: t1, lockReportAt: t1, reportReceivedAt: t1, endMethod: '超时开锁·平台请求结束', location: v.location, stationId: st ? st.stationId : null, inStation: true, basis: '车端判定', billAt: t1, distanceKm: 0 });
      D.notify(u.userId, '订单', '车辆在超时后才开锁，已为你结束', `订单 ${o.orderId} 车辆在超时后才开锁，平台已立即锁车并结束，本单 0 元。`, o.orderId);
      ctx.emit('order.created', { orderId: o.orderId, userId: u.userId, operatorId: o.operatorId });
      throw new ApiError('UNLOCK_NO_RESPONSE', null, { orderId: o.orderId, lateSuccess: true, nearby: D.nearby(v.location, v.vehicleId) });
    }
    if (reason) { // 开锁失败，不计费（BR-02）
      Object.assign(o, { status: '开锁失败', unlockResult: '失败', unlockFailReason: reason, unlockReportAt: reason === '车辆无响应' ? null : t1, failNotifiedAt: t1, startAt: null, payStatus: '无需支付' });
      const todayCare = db().users.coupons.some(c => c.userId === u.userId && c.templateId === 'CPT04' && c.issuedAt.slice(0, 10) === t1.slice(0, 10));
      let care = null;
      if (!todayCare) care = D.issueCoupon(u, 'CPT04');
      ctx.emit('order.created', { orderId: o.orderId, userId: u.userId, operatorId: o.operatorId });
      const code2 = { 车辆无响应: 'UNLOCK_NO_RESPONSE', 电量不足: 'UNLOCK_LOW_BATTERY', 车辆故障: 'UNLOCK_FAULT', 车辆被占用: 'UNLOCK_OCCUPIED' }[reason];
      const near = D.nearby(v.location, v.vehicleId);
      throw new ApiError(code2, `${({ 车辆无响应: '车辆没有响应', 电量不足: '车辆电量不足', 车辆故障: '车辆故障', 车辆被占用: '车辆正在被使用' })[reason]}，未扣费。${near.length ? `附近还有 ${near.length} 辆可用车` : '附近暂时没有可用车'}`, { orderId: o.orderId, nearby: near, careCoupon: care ? { couponId: care.couponId, name: care.name, amount: care.amount } : null });
    }
    if (lastFail) lastFail.afterFailOrderId = o.orderId;
    if (uc.admission) {
      const ev = D.activeEvent();
      o.degraded = { eventId: ev.eventId, admission: uc.admission.category, limitMinutes: uc.admission.limitMinutes, limitAmount: uc.admission.limitAmount, overLimitRemindedAt: null, closure: null };
      ev.orderIds.push(o.orderId);
      ev.admissionCounts[uc.admission.category] = (ev.admissionCounts[uc.admission.category] || 0) + 1;
      ev.maxHourlyOrders = Math.max(ev.maxHourlyOrders, D.hourCount());
      ev.maxUserOpenOrders = Math.max(ev.maxUserOpenOrders, D.openDegradedCount(u.userId));
      D.refreshEventClosure(ev);
      const lim = D.currentVersion().riskLimits.hourlyDegradedOrders;
      if (D.hourCount() >= Math.ceil(lim * 0.8) && !db().ops.alerts.some(a => a.degradeEventId === ev.eventId && a.type === '接近每小时降级单上限' && ms(a.at) > ctx.nowMs() - 3600e3)) {
        D.alert({ level: '警告', type: '接近每小时降级单上限', text: `近 1 小时降级单 ${D.hourCount()} 笔，上限 ${lim} 笔，请决定是否收紧`, degradeEventId: ev.eventId });
      }
      ctx.dirty('ops');
    }
    if (v.pileId) { const p = D.pile(v.pileId); if (p && p.occupiedVehicleId === v.vehicleId) p.occupiedVehicleId = null; ctx.dirty('stations'); }
    Object.assign(v, { status: '骑行中', stationId: null, pileId: null, lastReportAt: t1 });
    u.lastActiveAt = t1;
    ctx.dirty('vehicles', 'users');
    ctx.emit('order.created', { orderId: o.orderId, userId: u.userId, operatorId: o.operatorId, degraded: !!o.degraded });
    return rideView(o);
  }, { latency: [100, 200] });

  /** 当前骑行中订单（含实时计费）；没有时返回 null */
  api.ride = () => call('ride', () => { const u = need(); const o = D.ridingOrder(u.userId); return o ? rideView(o) : null; }, { latency: [80, 200] });

  /** 还车前预判：是否在站点、是否收调度费、最近站点、空桩数 */
  api.returnPreview = ({ stationId, location } = {}) => call('returnPreview', () => {
    const u = need();
    const o = D.ridingOrder(u.userId);
    if (!o) throw new ApiError('NO_RIDING');
    const loc = stationId ? insidePoint(D.station(stationId)) : (location || currentLocation(o));
    const ver = D.versionsById()[o.pricingVersionId];
    const r = R.decideReturn({ stations: db().stations.stations, piles: db().stations.piles, location: loc, method: 'app' });
    const st = r.station;
    return {
      location: loc, inStation: r.inStation, basis: r.basis, dispatchFee: r.inStation ? 0 : ver.dispatchFee,
      station: st ? { stationId: st.stationId, name: st.name, type: st.type, freePiles: R.freePiles(db().stations.piles, st.stationId).length } : null,
      nearestStation: r.nearest ? { stationId: r.nearest.station.stationId, name: r.nearest.station.name, distanceM: r.nearest.distanceM, center: r.nearest.station.center } : null,
      text: r.inStation ? (r.basis === '桩满·围栏内' ? '桩位已满，在站点内桩旁锁车即可，不收调度费' : '在站点内，可以还车') : `不在站点内，还车将收取调度费 ¥${Number(ver.dispatchFee).toFixed(1)}。最近站点「${r.nearest.station.name}」约 ${r.nearest.distanceM} 米`,
    };
  });

  /**
   * 还车（R-07、BR-08～BR-13）。
   * method：pile 推入桩位 | pileNoLock 推入桩位但未锁 | lock 手动锁车 | app 页面点［还车］
   * stationId / pileId / location 指定还车位置；app 方式在站外需 confirmDispatch=true
   */
  api.returnBike = ({ method = 'app', stationId, pileId, location, confirmDispatch = false, conflict = false } = {}) => call('returnBike', async () => {
    const u = need();
    const o = D.ridingOrder(u.userId);
    if (!o) throw new ApiError('NO_RIDING');
    if (!['pile', 'pileNoLock', 'lock', 'app'].includes(method)) throw new ApiError('INVALID', '还车方式不正确');
    let loc = location || currentLocation(o);
    if (stationId) { const st = D.station(stationId); if (!st) throw new ApiError('NOT_FOUND', '没有找到该站点'); loc = pileId && D.pile(pileId) ? D.pile(pileId).location : insidePoint(st); }
    const r = R.decideReturn({ stations: db().stations.stations, piles: db().stations.piles, location: loc, method, pileId, conflict });
    if (r.error) throw new ApiError(r.error, null, { stationId: r.station ? r.station.stationId : null });
    const ver = D.versionsById()[o.pricingVersionId];
    if (method === 'app' && !r.inStation && !confirmDispatch) {
      throw new ApiError('DISPATCH_CONFIRM_REQUIRED', `当前不在站点内，还车将收取调度费 ¥${Number(ver.dispatchFee).toFixed(1)}。最近站点「${r.nearest.station.name}」约 ${r.nearest.distanceM} 米`, { dispatchFee: ver.dispatchFee, nearestStation: { stationId: r.nearest.station.stationId, name: r.nearest.station.name, distanceM: r.nearest.distanceM, center: r.nearest.station.center } });
    }
    if (method !== 'lock') await ctx.sleep(600 + ctx.rnd() * 800); // 平台请求运营商锁车（M-02），等待锁车报告
    const t = ctx.now();
    const end = {
      location: r.pile ? r.pile.location : loc, stationId: r.station ? r.station.stationId : null, pileId: r.pile ? r.pile.pileId : null,
      inStation: r.inStation, basis: r.basis, vehicleSideInStation: r.vehicleSideInStation, distanceToStationM: r.distanceToStationM,
      dispatchPromptShown: method === 'app' && !r.inStation, reportReceivedAt: t, billAt: t,
    };
    if (method === 'pile') Object.assign(end, { pileInAt: addSec(t, -4), lockReportAt: t, endAt: addSec(t, -4), endMethod: '入桩识别' });
    else if (method === 'pileNoLock') Object.assign(end, { pileInAt: addSec(t, -9), lockReportAt: t, endAt: addSec(t, -9), endMethod: '入桩未锁·平台请求结束' });
    else Object.assign(end, { lockReportAt: t, endAt: t, endMethod: method === 'lock' ? '用户锁车' : '页面还车' });
    const res = D.finishRide(o, end);
    const view = V.userOrder(o);
    const f = o.fee;
    const msg = !f ? `已还车，${o.billedMinutes || Math.max(1, Math.ceil(o.durationSec / 60))} 分钟，费用将稍后收取`
      : o.payStatus === '已支付' ? `已还车，${o.billedMinutes} 分钟，已自动扣 ¥${f.payable.toFixed(1)}`
        : o.payStatus === '待支付' ? `已还车，${o.billedMinutes} 分钟，应付 ¥${f.unpaid.toFixed(1)}`
          : o.payStatus === '无需支付' ? `已还车，${o.billedMinutes} 分钟，本单无需支付` : `已还车，${o.billedMinutes} 分钟，自动扣款没有成功，已记为欠费 ¥${f.unpaid.toFixed(1)}`;
    return { order: view, deferred: !f, collect: res ? res.result : null, message: msg };
  }, { latency: [100, 250] });

  /** 我的订单（新到旧） */
  api.orders = ({ page = 1, pageSize = 20, status } = {}) => call('orders', () => {
    const u = need();
    let list = D.userOrders(u.userId);
    if (status) list = list.filter(o => (status === '待付款' ? ['待支付', '欠费'].includes(o.payStatus) : o.status === status));
    list = [...list].sort((a, b) => ms(b.requestAt) - ms(a.requestAt));
    const pg = paginate(list, { page, pageSize });
    return { ...pg, rows: pg.rows.map(V.userOrder) };
  });
  api.order = orderId => call('order', () => {
    const u = need();
    const o = D.order(orderId);
    if (!o || o.userId !== u.userId) throw new ApiError('NOT_FOUND', '没有找到该订单');
    const pays = db().payments.payments.filter(p => p.orderId === orderId).map(p => ({ paymentId: p.paymentId, type: p.type, amount: p.amount, channel: p.channel, status: p.status, failReason: p.failReason, createdAt: p.createdAt, completedAt: p.completedAt }));
    return { ...V.userOrder(o), payments: pays, tickets: db().tickets.filter(t => t.orderId === orderId).map(t => ({ ticketId: t.ticketId, category: t.category, status: t.status, createdAt: t.createdAt })) };
  });

  /** 一键支付（待支付）或补付（欠费） */
  api.pay = orderId => call('pay', () => {
    const u = need();
    const o = D.order(orderId);
    if (!o || o.userId !== u.userId) throw new ApiError('NOT_FOUND', '没有找到该订单');
    return payOne(u, o);
  });
  function payOne(u, o) {
    if (!o.fee || !['待支付', '欠费'].includes(o.payStatus) || C(o.fee.unpaid) <= 0) throw new ApiError('NOTHING_TO_PAY');
    if (D.hasPendingPayment(o)) throw new ApiError('CONFLICT', '该单有一笔扣款结果尚未返回，稍后再试');
    const arrears = o.payStatus === '欠费';
    const p = D.pay({ type: arrears ? '欠费补付' : '订单支付', userId: u.userId, orderId: o.orderId, amount: o.fee.unpaid, channel: D.channelOf(u) });
    if (p.status !== '成功') throw new ApiError('PAY_DECLINED', `支付没有成功：${p.failReason || '渠道未返回结果'}`, { paymentId: p.paymentId });
    o.fee.channelPaid = Y(C(o.fee.channelPaid) + C(p.amount)); R.settleFee(o.fee);
    if (arrears) { o.payStatus = '欠费已补付'; o.arrearsPaidAt = p.completedAt; } else { o.payStatus = '已支付'; if (o.payMethod !== '免押自动扣款') o.payMethod = '一键支付'; }
    D.recomputeArrears(u);
    D.autoUnfreeze(u);
    ctx.dirty('orders', 'users');
    ctx.emit('order.updated', { orderId: o.orderId, status: o.status, userId: u.userId, operatorId: o.operatorId });
    return { order: V.userOrder(o), paymentId: p.paymentId, amount: p.amount };
  }
  /** 一键补付全部欠费（BR-05） */
  api.payArrears = () => call('payArrears', () => {
    const u = need();
    const list = D.arrearsOrders(u.userId).filter(o => !D.hasPendingPayment(o));
    if (!list.length) throw new ApiError('NOTHING_TO_PAY', '没有需要补付的欠费');
    const done = [];
    for (const o of list) done.push(payOne(u, o));
    return { paid: Y(done.reduce((s, x) => s + C(x.amount), 0)), orders: done.map(x => x.order), user: V.user(u) };
  });

  /* ---------------- 月卡与优惠券（R-19、BR-07） ---------------- */
  api.monthCard = {
    get: () => call('monthCard.get', () => {
      const u = need();
      const v = D.currentVersion();
      const cards = db().users.monthCards.filter(c => c.userId === u.userId).sort((a, b) => ms(b.startAt) - ms(a.startAt));
      return { rule: { price: v.monthCard.price, days: v.monthCard.days, perOrderCap: v.monthCard.perOrderCap, appliesTo: v.monthCard.appliesTo, text: `${v.monthCard.days} 天内每单骑行费最多抵 ¥${v.monthCard.perOrderCap.toFixed(1)}（不抵调度费）` }, cards };
    }),
    buy: () => call('monthCard.buy', () => {
      const u = need();
      if (u.account.status !== '正常') throw new ApiError('ACCOUNT_FROZEN', `账户已冻结：${u.account.freeze.reason}`);
      if (u.realName.status !== '已核验') throw new ApiError('NEED_ONBOARDING', '完成实名核验后即可购买月卡');
      const v = D.currentVersion(), t = ctx.now();
      const active = db().users.monthCards.filter(c => c.userId === u.userId && ms(c.endAt) > ctx.nowMs()).sort((a, b) => ms(b.endAt) - ms(a.endAt))[0];
      const start = active ? active.endAt : t;
      const card = { cardId: ctx.ids.card(t), userId: u.userId, pricingVersionId: v.versionId, price: v.monthCard.price, perOrderCap: v.monthCard.perOrderCap, startAt: start, endAt: addDays(start, v.monthCard.days), paymentId: null, status: active ? '未生效' : '生效中', usedOrderIds: [] };
      const p = D.pay({ type: '月卡购买', userId: u.userId, cardId: card.cardId, amount: card.price, channel: D.channelOf(u) });
      if (p.status !== '成功') throw new ApiError('PAY_DECLINED', `月卡没有买成功：${p.failReason || '渠道未返回结果'}`);
      card.paymentId = p.paymentId;
      db().users.monthCards.push(card);
      ctx.dirty('users');
      return card;
    }),
  };
  const claimWindow = (tpl, now) => {
    if (!/领取/.test(tpl.issue)) return { open: false, why: '该券由系统自动发放' };
    const d = new Date(now.replace(' ', 'T'));
    const w = d.getDay(), h = d.getHours();
    const open = (w === 5 && h >= 17) || w === 6 || w === 0;
    const fri = new Date(d); fri.setDate(d.getDate() - ((w + 2) % 7)); fri.setHours(17, 0, 0, 0);
    return { open, why: open ? null : '每周五 17:00 开放领取', windowStart: fmt(fri) };
  };
  api.coupons = {
    list: ({ status } = {}) => call('coupons.list', () => {
      const u = need();
      let list = db().users.coupons.filter(c => c.userId === u.userId);
      if (status) list = list.filter(c => c.status === status);
      return list.sort((a, b) => (a.status === '未使用' ? 0 : 1) - (b.status === '未使用' ? 0 : 1) || ms(a.expireAt) - ms(b.expireAt));
    }),
    /** 当前规则下的券模板与能否领取 */
    templates: () => call('coupons.templates', () => {
      const u = me(); const now = ctx.now();
      return D.currentVersion().coupons.map(tpl => {
        const w = claimWindow(tpl, now);
        const got = u && db().users.coupons.some(c => c.userId === u.userId && c.templateId === tpl.templateId && (!w.windowStart || ms(c.issuedAt) >= ms(w.windowStart)));
        return { ...tpl, claimable: !!u && w.open && !got, why: !u ? '请先授权登录' : got ? '本期已领取' : w.why };
      });
    }),
    claim: templateId => call('coupons.claim', () => {
      const u = need();
      const tpl = D.currentVersion().coupons.find(c => c.templateId === templateId);
      if (!tpl) throw new ApiError('NOT_FOUND', '没有找到该优惠券');
      const w = claimWindow(tpl, ctx.now());
      if (!w.open) throw new ApiError('COUPON_NOT_OPEN', `该券当前不能领取：${w.why}`);
      if (db().users.coupons.some(c => c.userId === u.userId && c.templateId === templateId && ms(c.issuedAt) >= ms(w.windowStart))) throw new ApiError('COUPON_LIMIT');
      return D.issueCoupon(u, templateId, '用户领取');
    }),
  };

  /* ---------------- 押金（R-15～R-17，BR-23～BR-25） ---------------- */
  const refundBlockers = D.refundBlockers;
  api.deposit = {
    get: () => call('deposit.get', () => {
      const u = need();
      const d = u.deposit, v = D.currentVersion();
      const blockers = d.mode === '押金' && ['已缴', '押金不足'].includes(d.status) ? refundBlockers(u) : [];
      const prev = d.previous || null;
      const awaiting = [d.refund, prev && prev.refund].find(r => r && r.awaitingPayee) || null;
      return {
        mode: d.mode, deposit: d.mode === '押金' ? { amountPaid: d.amountPaid, balance: d.balance, status: d.status, paidAt: d.paidAt, refund: d.refund } : null,
        creditFree: d.mode === '免押' ? { provider: d.provider, openedAt: d.creditOpenedAt } : null,
        previousDeposit: prev ? { amountPaid: prev.amountPaid, status: prev.status, refund: prev.refund } : null,
        rule: { depositAmount: v.deposit.amount, creditFreeThreshold: v.creditFree.threshold, refundWorkdays: D.params().depositRefundWorkdays },
        canRefund: d.mode === '押金' && ['已缴', '押金不足'].includes(d.status) && C(d.balance) > 0 && !blockers.length,
        blockers, refundAmount: d.mode === '押金' ? d.balance : 0, expectedAtIfApplyNow: addWorkdays(ctx.now(), D.params().depositRefundWorkdays),
        awaitingPayee: !!awaiting, refundFailedPaymentId: awaiting ? awaiting.paymentId : null,
      };
    }),
    /** 申请退押：申请即停止开新单，原路退回，显示预计到账时间 */
    refund: () => call('deposit.refund', () => {
      const u = need();
      const r = D.requestDepositRefund(u, u.userId);
      return { refund: r, text: `押金 ¥${Number(r.amount).toFixed(1)} 将原路退回，预计 ${md(r.expectedAt)} 前到账。退押后需重新缴押或开通免押才能骑行。` };
    }),
  };

  /** 原路退回失败时，填写本人实名收款账户（R-11）。paymentId 为失败的退款或退押流水 */
  api.submitPayee = ({ paymentId, name, bank, account } = {}) => call('submitPayee', () => {
    const u = need();
    const p = D.payment(paymentId);
    if (!p || p.userId !== u.userId || p.status !== '失败' || !['退款', '退押'].includes(p.type)) throw new ApiError('NOT_FOUND', '没有找到需要填写收款账户的退款');
    const nm = String(name || '').trim();
    if (!u.name || !nm || nm[0] !== u.name[0] || [...nm].length !== [...u.name].length) throw new ApiError('PAYEE_NAME_MISMATCH');
    const acc = String(account || '').replace(/\s/g, '');
    if (!/^\d{12,19}$/.test(acc)) throw new ApiError('INVALID', '银行卡号格式不正确');
    if (!String(bank || '').trim()) throw new ApiError('INVALID', '请填写开户银行');
    p.payeeSubmitted = { name: maskName(nm), bank: String(bank).trim(), account: maskAccount(acc), submittedAt: ctx.now() };
    for (const tk of db().tickets.filter(t => t.awaitingPayee && t.userId === u.userId && (!t.orderId || t.orderId === p.orderId))) {
      tk.messages.push({ from: '用户', at: ctx.now(), text: `已填写收款账户：${p.payeeSubmitted.bank} ${p.payeeSubmitted.account}` });
      tk.awaitingPayee = false;
      ctx.dirty('tickets');
    }
    for (const dep of [u.deposit, u.deposit.previous]) if (dep && dep.refund && dep.refund.paymentId === p.paymentId) { dep.refund.awaitingPayee = false; ctx.dirty('users'); }
    ctx.dirty('payments');
    return { ok: true, payee: p.payeeSubmitted, text: '已提交，平台公司财务将人工转账，到账后通知你' };
  });

  /* ---------------- 账户与注销（R-14、R-48） ---------------- */
  const cancelBlockers = D.cancelBlockers;
  api.account = {
    get: () => call('account.get', () => {
      const u = need();
      const fz = u.account.freeze;
      const self = [];
      if (fz) {
        if (/欠费/.test(fz.reason) || C(u.arrears) > 0) self.push({ label: `补付欠费 ¥${Number(u.arrears).toFixed(1)}`, action: 'payArrears' });
        if (/核验/.test(fz.reason) && u.realName.status !== '已核验') self.push({ label: '重新实名核验', action: 'verifyRealName' });
        if (!/欠费|核验/.test(fz.reason)) self.push({ label: '联系客服解冻', action: 'contactCS' });
      }
      return { user: V.user(u), freeze: fz, selfService: self, cancelBlockers: cancelBlockers(u) };
    }),
    cancelCheck: () => call('account.cancelCheck', () => { const u = need(); const b = cancelBlockers(u); return { canCancel: !b.length, blockers: b }; }),
    cancel: ({ reason } = {}) => call('account.cancel', () => {
      const u = need();
      const t = D.cancelAccount(u, reason || '用户申请注销', u.userId);
      ctx.session.set('User', null);
      return { ok: true, cancelledAt: t };
    }),
  };

  /* ---------------- 通知 ---------------- */
  api.notices = ({ unreadOnly = false } = {}) => call('notices', () => {
    const u = need();
    return db().notices.filter(n => n.userId === u.userId && (!unreadOnly || !n.read)).sort((a, b) => ms(b.at) - ms(a.at));
  }, { latency: [80, 200] });
  api.markNoticesRead = (ids) => call('markNoticesRead', () => {
    const u = need();
    let n = 0;
    for (const x of db().notices) if (x.userId === u.userId && !x.read && (!ids || ids.includes(x.noticeId))) { x.read = true; n++; }
    if (n) ctx.dirty('notices');
    return { marked: n };
  });

  /* ---------------- 客服对话（R-10～R-12） ---------------- */
  api.cs = {
    tickets: () => call('cs.tickets', () => { const u = need(); return db().tickets.filter(t => t.userId === u.userId).sort((a, b) => ms(b.createdAt) - ms(a.createdAt)).map(publicTicket); }),
    ticket: id => call('cs.ticket', () => { const u = need(); const t = D.ticket(id); if (!t || t.userId !== u.userId) throw new ApiError('NOT_FOUND', '没有找到该对话'); return publicTicket(t); }),
    /** 新建对话：可带订单号（每单一键联系客服），首条留言由客服智能体即时回复 */
    open: ({ orderId = null, entry = '我的', text, category } = {}) => call('cs.open', () => {
      const u = need();
      const o = orderId ? D.order(orderId) : null;
      if (orderId && (!o || o.userId !== u.userId)) throw new ApiError('NOT_FOUND', '没有找到该订单');
      if (!String(text || '').trim()) throw new ApiError('INVALID', '请输入要咨询的问题');
      const t = ctx.now();
      const tk = {
        ticketId: ctx.ids.ticket(t), userId: u.userId, orderId, category: category || '费用咨询', entry, createdAt: t, status: '待处理', assignee: null, closedAt: null,
        messages: [{ from: '用户', at: t, text: String(text).trim() }], refundProposal: null, forceEnd: null,
        facts: o ? { orderId: o.orderId, startAt: o.startAt, endAt: o.endAt, billedMinutes: o.billedMinutes, distanceKm: o.distanceKm, receivable: o.fee ? o.fee.receivable : null, payable: o.fee ? o.fee.payable : null, netReceived: o.fee ? o.fee.netReceived : null, inStation: o.inStation, inStationBasis: o.inStationBasis } : null,
        actions: [], awaitingPayee: false,
      };
      agentAnswer(tk, text, true);
      db().tickets.push(tk);
      if (o) { o.ticketIds.push(tk.ticketId); ctx.dirty('orders'); }
      ctx.dirty('tickets');
      ctx.emit('ticket.changed', { ticketId: tk.ticketId, userId: u.userId });
      return publicTicket(tk);
    }),
    send: (ticketId, text) => call('cs.send', () => {
      const u = need();
      const tk = D.ticket(ticketId);
      if (!tk || tk.userId !== u.userId) throw new ApiError('NOT_FOUND', '没有找到该对话');
      if (!String(text || '').trim()) throw new ApiError('INVALID', '请输入内容');
      tk.messages.push({ from: '用户', at: ctx.now(), text: String(text).trim() });
      if (tk.status === '已解决') { tk.status = '处理中'; tk.closedAt = null; }
      agentAnswer(tk, text, false);
      ctx.dirty('tickets');
      ctx.emit('ticket.changed', { ticketId, userId: u.userId });
      return publicTicket(tk);
    }),
  };
  function agentAnswer(tk, text, first) {
    const r = A.csReply(tk, text);
    if (first && r.category) tk.category = r.category;
    tk.messages.push({ from: '客服智能体', at: addSec(ctx.now(), 2), text: r.text });
    if (r.proposal && !tk.refundProposal) tk.refundProposal = { ...r.proposal, proposedAt: addSec(ctx.now(), 2), proposedBy: 'AC0904', decision: '待确认', decidedBy: null, decidedAt: null, overLimit: C(r.proposal.amount) > C(D.params().csRefundLimit) };
    if (r.forceEndProposal) tk.forceEndProposal = r.forceEndProposal;
  }
  const publicTicket = t => ({ ticketId: t.ticketId, orderId: t.orderId, category: t.category, entry: t.entry, createdAt: t.createdAt, status: t.status, closedAt: t.closedAt, messages: t.messages, refundProposal: t.refundProposal ? { amount: t.refundProposal.amount, kind: t.refundProposal.kind, decision: t.refundProposal.decision } : null, awaitingPayee: !!t.awaitingPayee, refundFailedPaymentId: t.awaitingPayee ? (D.order(t.orderId) || {}).refundFailedPaymentId || null : null });

  return api;
}

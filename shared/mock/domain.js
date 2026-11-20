// 领域操作：订单结束与计价、收款、押金、通知、降级事件与闭合、到点任务。
// 被用户端、管理端、系统接口共用。所有写操作都标记 dirty 并按规则留痕。
import { ApiError } from './errors.js';
import { C, Y, ms, fmt, addSec, addDays, addWorkdays, dist, maskName, monthAdd, hash32 } from './util.js';
import * as R from './rules.js';

const REFUND_HOURS = 48; // 退押原路退回的模拟到账时长（假设）

export function createDomain(ctx) {
  const db = () => ctx.db;
  const D = {};

  /* ---------------- 查询 ---------------- */
  D.params = () => db().pricing.platformParams.items;
  D.versionsById = () => Object.fromEntries(db().pricing.versions.map(v => [v.versionId, v]));
  D.currentVersion = () => R.versionAt(db().pricing.versions, ctx.now());
  D.versionAt = t => R.versionAt(db().pricing.versions, t);
  D.brand = id => ctx.find(db().operators.brands, 'brandId', id);
  D.operator = id => ctx.find(db().operators.operators, 'operatorId', id);
  D.user = id => ctx.find(db().users.users, 'userId', id);
  D.order = id => ctx.find(db().orders, 'orderId', id);
  D.vehicle = id => ctx.find(db().vehicles, 'vehicleId', id);
  D.station = id => ctx.find(db().stations.stations, 'stationId', id);
  D.pile = id => ctx.find(db().stations.piles, 'pileId', id);
  D.payment = id => ctx.find(db().payments.payments, 'paymentId', id);
  D.account = id => ctx.find(db().accounts.accounts, 'accountId', id);
  D.coupon = id => ctx.find(db().users.coupons, 'couponId', id);
  D.card = id => ctx.find(db().users.monthCards, 'cardId', id);
  D.ticket = id => ctx.find(db().tickets, 'ticketId', id);
  D.event = id => ctx.find(db().ops.degradeEvents, 'eventId', id);
  D.degrade = () => db().ops.current;
  D.activeEvent = () => (db().ops.current.degraded ? D.event(db().ops.current.eventId) : null);
  D.actorName = id => {
    if (!id) return null;
    if (/^U\d+/.test(id)) return '用户本人';
    const a = D.account(id);
    return a ? a.name : id;
  };
  D.actorKind = id => { if (!id) return null; if (/^U\d+/.test(id)) return '用户'; const a = D.account(id); return a ? (a.role === '智能体' ? '智能体' : a.role === '系统' ? '系统' : '人') : '人'; };

  /** 可租判定（SCHEMA 5.2）：品牌已接入、码已启用、车辆可用 */
  D.rentable = v => { const b = D.brand(v.brandId); return !!b && b.accessStatus === '已接入' && v.codeStatus === '已启用' && v.status === '可用'; };
  D.nearby = (center, excludeId, radius) => R.nearby(db().vehicles, D.rentable, center, radius || D.params().nearbyRadiusM, excludeId, 5).map(({ v, d }) => {
    const b = D.brand(v.brandId);
    return { vehicleId: v.vehicleId, plateNo: v.plateNo, govCode: v.govCode, brandId: v.brandId, brandName: b.name, displayName: b.displayName, color: b.color, battery: v.battery, location: v.location, distanceM: Math.round(d), stationId: v.stationId, stationName: v.stationId ? D.station(v.stationId).name : null };
  });
  D.userOrders = uid => db().orders.filter(o => o.userId === uid);
  D.ridingOrder = uid => db().orders.find(o => o.userId === uid && o.status === '骑行中') || null;
  D.unpaidOrders = uid => db().orders.filter(o => o.userId === uid && o.payStatus === '待支付');
  D.arrearsOrders = uid => db().orders.filter(o => o.userId === uid && o.payStatus === '欠费');
  D.hasPendingPayment = o => db().payments.payments.some(p => p.orderId === o.orderId && p.status === '处理中');
  D.recomputeArrears = u => { u.arrears = Y(D.arrearsOrders(u.userId).reduce((s, o) => s + C(o.fee.unpaid), 0)); return u.arrears; };
  D.channelOf = u => (u.channel === '支付宝' ? '支付宝' : '微信支付');

  /* ---------------- 通知（事后告知、账单推送） ---------------- */
  D.notify = (userId, kind, title, text, orderId = null) => {
    const t = ctx.now();
    const n = { noticeId: ctx.ids.notice(t), userId, at: t, kind, title, text, orderId, read: false };
    db().notices.push(n);
    ctx.dirty('notices');
    ctx.emit('notice', { userId, noticeId: n.noticeId, title, kind });
    return n;
  };

  /* ---------------- 资金流水 ---------------- */
  /**
   * 新建一笔流水。opts: {type, account, direction, userId, orderId, cardId, amount(元), channel, status?, failReason?, note?, ticketId?, operatorAccountId?, payee?, relatedPaymentId?}
   * 渠道收款支持注入：payment.decline（失败）、payment.unknown（处理中）、channel.missing（渠道未到账）
   */
  D.pay = opts => {
    const t = ctx.now();
    const p = {
      paymentId: ctx.ids.payment(t), type: opts.type, account: opts.account || '骑行收入户', direction: opts.direction || '收',
      userId: opts.userId || null, orderId: opts.orderId || null, cardId: opts.cardId || null, amount: Y(C(opts.amount)),
      channel: opts.channel, channelTradeNo: null, status: opts.status || '成功', failReason: opts.failReason || null,
      createdAt: t, completedAt: null, channelSettledDate: null, relatedPaymentId: opts.relatedPaymentId || null,
      ticketId: opts.ticketId || null, operatorAccountId: opts.operatorAccountId || null, payee: opts.payee || null, note: opts.note || null,
    };
    const viaChannel = p.channel === '微信支付' || p.channel === '支付宝';
    if (viaChannel && p.direction === '收' && p.status === '成功' && !opts.noInject) {
      if (ctx.takeInjection('payment.decline')) { p.status = '失败'; p.failReason = '用户支付账户余额不足'; }
      else if (ctx.takeInjection('payment.unknown')) { p.status = '处理中'; p.note = '渠道未返回结果'; }
    }
    if (p.status === '成功') {
      p.completedAt = t;
      p.channelTradeNo = ctx.ids.tradeNo(p.channel, t, p.direction === '付');
      if (viaChannel && p.account === '骑行收入户') {
        const late = +t.slice(11, 13) === 23 && +t.slice(14, 16) >= 50;
        p.channelSettledDate = ctx.takeInjection('channel.missing') ? null : (late ? addDays(t, 1).slice(0, 10) : t.slice(0, 10));
      }
    } else if (p.status === '失败' && viaChannel) p.channelTradeNo = null;
    db().payments.payments.push(p);
    ctx.dirty('payments');
    ctx.emit('payment.created', { paymentId: p.paymentId, type: p.type, status: p.status, orderId: p.orderId, userId: p.userId });
    return p;
  };

  /* ---------------- 订单计价与收款（R-08、R-09、BR-07、BR-22） ---------------- */
  D.priceAndBill = (o, billAt) => {
    const u = D.user(o.userId);
    const v = D.versionsById()[o.pricingVersionId];
    const card = R.cardFor(db().users.monthCards, o.userId, o.startAt);
    const res = R.priceOrder({ version: v, durationSec: o.durationSec, inStation: o.inStation, card, coupons: db().users.coupons, userId: o.userId, billAt, zero: o.unlockResult === '超时后成功' });
    o.fee = res.fee;
    o.billedMinutes = res.billedMinutes;
    o.billPushedAt = billAt;
    if (res.coupon) Object.assign(res.coupon, { usedOrderId: o.orderId, usedAt: billAt, status: '已使用' });
    if (card && C(o.fee.monthCardDeduct) > 0 && !card.usedOrderIds.includes(o.orderId)) card.usedOrderIds.push(o.orderId);
    o.refundStatus = '无';
    ctx.dirty('orders', 'users');
    return { user: u, version: v };
  };
  /** 按常态方式收款：免押自动扣款；押金用户与先骑后验新用户推送一键支付 */
  D.collect = o => {
    const u = D.user(o.userId);
    const f = o.fee;
    if (C(f.payable) === 0) { o.payStatus = '无需支付'; o.payMethod = null; R.settleFee(f); return { result: '无需支付' }; }
    if (u.deposit.mode === '免押') {
      const p = D.pay({ type: '订单扣款', userId: u.userId, orderId: o.orderId, amount: f.payable, channel: D.channelOf(u) });
      o.payMethod = '免押自动扣款';
      if (p.status === '成功') {
        f.channelPaid = f.payable; o.payStatus = '已支付'; R.settleFee(f);
        D.notify(u.userId, '账单', '已还车，已自动扣款', `${o.billedMinutes} 分钟，已自动扣款 ¥${f.payable.toFixed(1)}`, o.orderId);
        return { result: '已收款', payment: p };
      }
      if (p.status === '处理中') { o.payStatus = '待支付'; R.settleFee(f); return { result: '结果未知', payment: p }; }
      o.payStatus = '欠费'; R.settleFee(f); D.recomputeArrears(u);
      D.notify(u.userId, '欠费', '自动扣款没有成功', `本单 ¥${f.payable.toFixed(1)} 已记为欠费，下次扫码时一键补付即可`, o.orderId);
      return { result: '已转欠费', payment: p };
    }
    o.payStatus = '待支付'; o.payMethod = '一键支付'; R.settleFee(f);
    D.notify(u.userId, '账单', '已还车，请支付本单费用', `${o.billedMinutes} 分钟，应付 ¥${f.payable.toFixed(1)}，请在 ${D.params().payWindowHours} 小时内一键支付`, o.orderId);
    return { result: '已推送一键支付' };
  };
  /** 结束一单：写结束事实、释放车辆、计价收款或（降级中）延后收款 */
  D.finishRide = (o, end) => {
    const v = D.vehicle(o.vehicleId);
    Object.assign(o, {
      status: '已结束', endAt: end.endAt, lockReportAt: end.lockReportAt || null, pileInAt: end.pileInAt || null,
      reportReceivedAt: end.reportReceivedAt || ctx.now(), lateReport: !!end.lateReport, endMethod: end.endMethod,
      endStationId: end.stationId || null, endPileId: end.pileId || null, endLocation: end.location,
      inStation: end.inStation, inStationBasis: end.basis, vehicleSideInStation: end.vehicleSideInStation ?? null,
      distanceToStationM: end.inStation ? null : (end.distanceToStationM ?? null), dispatchPromptShown: !!end.dispatchPromptShown,
      durationSec: Math.max(0, Math.round((ms(end.endAt) - ms(o.startAt)) / 1000)),
    });
    o.distanceKm = end.distanceKm != null ? end.distanceKm : Math.round(o.durationSec / 3600 * (11 + (hash32(o.orderId) % 40) / 10) * 10) / 10;
    o.settleMonth = ctx.labelMonth(o.endAt);
    if (v) {
      if (v.pileId) { const p0 = D.pile(v.pileId); if (p0 && p0.occupiedVehicleId === v.vehicleId) p0.occupiedVehicleId = null; }
      v.battery = Math.max(3, v.battery - Math.round(o.durationSec / 60 * 0.4));
      Object.assign(v, { status: v.battery < 15 ? '低电量' : '可用', location: end.location, stationId: end.stationId || null, pileId: end.pileId || null, lastReportAt: ctx.now() });
      if (end.pileId) D.pile(end.pileId).occupiedVehicleId = v.vehicleId;
      ctx.dirty('vehicles', 'stations');
    }
    ctx.dirty('orders');
    let collect = null;
    if (o.degraded && D.activeEvent() && D.activeEvent().eventId === o.degraded.eventId) {
      o.fee = null; o.payStatus = null; o.billPushedAt = null; // 费用稍后收取（A10）
      o.degraded.closure = { unlockResult: true, lockFact: true, payment: '未收款', status: '待闭合', missing: ['收款结果'], closedAt: null, note: '降级期间结束，平台恢复后收款' };
    } else if (o.degraded) {
      collect = D.closeDegradedOrder(o);
    } else {
      D.priceAndBill(o, end.billAt || ctx.now());
      collect = D.collect(o);
    }
    ctx.emit('order.updated', { orderId: o.orderId, status: o.status, userId: o.userId, operatorId: o.operatorId });
    return collect;
  };

  /* ---------------- 押金抵扣（BR-22，D-23 自动执行、事后告知） ---------------- */
  D.depositDeduct = o => {
    const u = D.user(o.userId), d = u.deposit, f = o.fee;
    if (d.mode !== '押金' || !['已缴', '押金不足'].includes(d.status) || C(d.balance) <= 0) return null;
    const amt = Math.min(C(f.unpaid), C(d.balance));
    if (amt <= 0) return null;
    const p = D.pay({ type: '押金抵扣', account: '押金专户', direction: '转', userId: u.userId, orderId: o.orderId, amount: Y(amt), channel: '押金专户划转', note: `一键支付期限（${D.params().payWindowHours} 小时）届满未付，按 BR-22 自动从押金抵扣，已通知用户` });
    f.depositDeducted = Y(C(f.depositDeducted) + amt); R.settleFee(f);
    d.balance = Y(C(d.balance) - amt);
    d.status = C(d.balance) < C(d.amountPaid) ? '押金不足' : '已缴';
    d.deductionPaymentIds.push(p.paymentId);
    o.payMethod = '押金抵扣';
    o.payStatus = C(f.unpaid) === 0 ? (C(f.channelPaid) === 0 ? '押金抵扣' : '已支付') : '欠费';
    D.recomputeArrears(u);
    ctx.audit({ action: '抵扣押金', targetType: 'order', targetId: o.orderId, accountId: 'AC0900', summary: `订单 ${o.orderId} 一键支付期限届满未付，自动从押金抵扣 ¥${Y(amt).toFixed(1)}`, reason: 'BR-22 押金只抵扣本人未付骑行费' });
    D.notify(u.userId, '押金抵扣', '已从押金抵扣本单费用', `订单 ${o.orderId} 超过 ${D.params().payWindowHours} 小时未支付，已自动从押金抵扣 ¥${Y(amt).toFixed(1)}，押金余额 ¥${d.balance.toFixed(1)}。补缴押金后即可继续骑行。`, o.orderId);
    ctx.dirty('orders', 'users');
    return p;
  };

  /* ---------------- 冻结与自动解冻（R-14） ---------------- */
  D.freeze = (u, reason, unfreezeHow, by = 'AC0900') => {
    if (u.account.status === '冻结') return;
    const t = ctx.now();
    u.account.status = '冻结';
    u.account.freeze = { reason, at: t, unfreezeHow };
    u.account.history.push({ action: '冻结', at: t, reason, by });
    ctx.audit({ action: '冻结账户', targetType: 'user', targetId: u.userId, accountId: by, summary: `冻结账户 ${u.userId}：${reason}`, reason });
    D.notify(u.userId, '账户', '账户已冻结', `${reason}。${unfreezeHow}。`);
    ctx.dirty('users');
  };
  /** 实名核验通过（达到法定年龄）且欠费结清后自动解冻；其他冻结原因需客服人工解冻 */
  D.autoUnfreeze = u => {
    if (u.account.status !== '冻结' || !u.account.freeze) return false;
    const r = u.account.freeze.reason || '';
    const auto = /欠费|核验/.test(r);
    if (!auto) return false;
    if (C(D.recomputeArrears(u)) > 0) return false;
    if (/核验/.test(r) && !(u.realName.status === '已核验' && u.realName.adult)) return false;
    const t = ctx.now();
    u.account.status = '正常';
    u.account.history.push({ action: '自动解冻', at: t, reason: /核验/.test(r) ? '已完成实名核验并结清欠费' : '欠费已结清', by: 'AC0900' });
    u.account.freeze = null;
    ctx.audit({ action: '解冻账户', targetType: 'user', targetId: u.userId, accountId: 'AC0900', summary: `账户 ${u.userId} 满足条件自动解冻`, reason: '欠费结清' });
    D.notify(u.userId, '账户', '账户已恢复正常', '已满足解冻条件，可以继续骑行。');
    ctx.dirty('users');
    return true;
  };

  /* ---------------- 退押与注销（BR-23、BR-24、R-48），用户本人与客服协助共用 ---------------- */
  D.refundBlockers = u => {
    const b = [];
    const r = D.ridingOrder(u.userId);
    if (r) b.push({ code: 'HAS_RIDING', message: '有一单正在骑行，还车后再处理', next: { label: '查看骑行中订单', action: 'goRide' }, orderId: r.orderId });
    const arr = C(D.recomputeArrears(u));
    if (arr > 0) b.push({ code: 'HAS_ARREARS', message: `有 ¥${Y(arr).toFixed(1)} 欠费未结清`, next: { label: '一键补付', action: 'payArrears' }, amount: Y(arr) });
    const up = D.unpaidOrders(u.userId);
    if (up.length) b.push({ code: 'UNPAID_ORDER', message: `有 ${up.length} 单待支付`, next: { label: '去支付', action: 'payOrder' }, orderId: up[0].orderId });
    return b;
  };
  D.cancelBlockers = u => {
    const b = D.refundBlockers(u);
    const d = u.deposit;
    if (d.mode === '押金' && ['已缴', '押金不足'].includes(d.status) && C(d.balance) > 0) b.push({ code: 'DEPOSIT_NOT_REFUNDED', message: `押金 ¥${Number(d.balance).toFixed(1)} 未退`, next: { label: '申请退押', action: 'refundDeposit' } });
    if (d.mode === '押金' && d.status === '退押中') b.push({ code: 'DEPOSIT_REFUNDING', message: '押金正在退还，到账后即可注销', next: { label: '查看押金', action: 'viewDeposit' } });
    return b;
  };
  D.requestDepositRefund = (u, by) => {
    const d = u.deposit;
    if (d.mode !== '押金' || !['已缴', '押金不足'].includes(d.status) || C(d.balance) <= 0) throw new ApiError('NOT_DEPOSIT_USER', d.mode === '押金' && d.status === '退押中' ? '押金正在退还中' : null);
    const blockers = D.refundBlockers(u);
    if (blockers.length) throw new ApiError('REFUND_BLOCKED', `暂不能退押：${blockers.map(x => x.message).join('；')}`, { blockers });
    const t = ctx.now();
    const p = D.pay({ type: '退押', account: '押金专户', direction: '付', userId: u.userId, amount: d.balance, channel: D.channelOf(u), status: '处理中', noInject: true, operatorAccountId: /^AC/.test(by) ? by : null });
    if (ctx.takeInjection('deposit.refund.fail')) { (db().runtime.failingRefunds = db().runtime.failingRefunds || []).push(p.paymentId); ctx.dirty('runtime'); }
    d.status = '退押中';
    d.refund = { appliedAt: t, stoppedAt: t, expectedAt: addWorkdays(t, D.params().depositRefundWorkdays), amount: d.balance, paymentId: p.paymentId, manualPaymentId: null, awaitingPayee: false };
    ctx.audit({ action: '申请退押', targetType: 'user', targetId: u.userId, accountId: by, summary: `${/^AC/.test(by) ? '客服协助' : '用户'}申请退押 ¥${Number(d.balance).toFixed(1)}，已停止开新单，原路退回中`, reason: /^AC/.test(by) ? '客服协助退押' : '用户申请' });
    ctx.dirty('users');
    return d.refund;
  };
  D.cancelAccount = (u, reason, by) => {
    const b = D.cancelBlockers(u);
    if (b.length) throw new ApiError('CANCEL_BLOCKED', `注销前还需处理：${b.map(x => x.message).join('；')}`, { blockers: b });
    const t = ctx.now();
    u.account.status = '已注销'; u.cancelledAt = t;
    u.account.history.push({ action: '注销', at: t, reason, by });
    ctx.audit({ action: '注销账户', targetType: 'user', targetId: u.userId, accountId: by, summary: `账户 ${u.userId} 注销${/^AC/.test(by) ? '（客服协助）' : ''}`, reason });
    ctx.dirty('users');
    return t;
  };

  /* ---------------- 优惠券 ---------------- */
  D.issueCoupon = (u, templateId, obtainedBy = '系统发放') => {
    const v = D.currentVersion();
    const tpl = v.coupons.find(c => c.templateId === templateId);
    if (!tpl) return null;
    const t = ctx.now();
    const exp = addDays(t, tpl.validDays - 1).slice(0, 10) + ' 23:59:59';
    const c = { couponId: ctx.ids.coupon(), userId: u.userId, templateId, name: tpl.name, amount: tpl.amount, issuedAt: t, expireAt: exp, obtainedBy, usedOrderId: null, usedAt: null, status: '未使用' };
    db().users.coupons.push(c);
    ctx.dirty('users');
    return c;
  };

  /* ---------------- 降级（R-31～R-35，BR-27～BR-34） ---------------- */
  D.hourCount = () => {
    const ev = D.activeEvent(); if (!ev) return 0;
    const t = ctx.nowMs() - 3600e3;
    return ev.orderIds.map(D.order).filter(o => o && ms(o.requestAt) >= t).length;
  };
  D.openDegradedCount = uid => db().orders.filter(o => o.userId === uid && o.degraded && (!o.degraded.closure || o.degraded.closure.status !== '已闭合')).length;
  /** 降级单闭合：按开锁时刻规则计价、常态方式收款、三项齐全才闭合（BR-31、BR-32） */
  D.closeDegradedOrder = o => {
    const t = ctx.now();
    const ev = D.event(o.degraded.eventId);
    if (o.status !== '已结束') {
      o.degraded.closure = { unlockResult: o.unlockResult === '成功', lockFact: false, payment: '未收款', status: '待人工', missing: ['锁车事实', '收款结果'], closedAt: null, note: o.status === '骑行中' ? '仍在骑行，还车后自动闭合' : '缺锁车事实，交人处理' };
      if (o.status === '骑行中') o.degraded.closure.status = '骑行中';
      ctx.dirty('orders');
      return null;
    }
    D.priceAndBill(o, t);
    const r = D.collect(o);
    const payment = r.result === '无需支付' ? '已收款' : r.result;
    const ok = payment !== '结果未知';
    o.degraded.closure = { unlockResult: true, lockFact: true, payment, status: ok ? '已闭合' : '待人工', missing: ok ? [] : ['收款结果'], closedAt: ok ? t : null, note: ok ? null : '渠道未返回扣款结果，待人工确认' };
    if (ev) D.refreshEventClosure(ev);
    ctx.dirty('orders', 'ops');
    return r;
  };
  D.refreshEventClosure = ev => {
    const os = ev.orderIds.map(D.order).filter(Boolean);
    const pending = os.filter(o => !o.degraded.closure || o.degraded.closure.status !== '已闭合');
    const pr = {};
    for (const o of os) if (o.degraded.closure && o.degraded.closure.status === '已闭合') pr[o.degraded.closure.payment] = (pr[o.degraded.closure.payment] || 0) + 1;
    for (const o of pending) if (o.degraded.closure) { const k = o.degraded.closure.payment === '结果未知' ? '结果未知' : '未收款'; pr[k] = (pr[k] || 0) + 1; }
    const nv = {};
    for (const uid of new Set(os.filter(o => o.degraded.admission === '先骑后验新用户').map(o => o.userId))) {
      const st = D.user(uid).realName.status; const k = st === '已核验' ? '通过' : st === '未通过' ? '未通过' : '待核验';
      nv[k] = (nv[k] || 0) + 1;
    }
    ev.orderCount = os.length;
    ev.receivableSum = Y(os.reduce((s, o) => s + (o.fee ? C(o.fee.receivable) : 0), 0));
    ev.closure = { status: ev.status === '进行中' ? '未开始' : (pending.length ? '部分待人工' : '已全部闭合'), closed: os.length - pending.length, pendingOrderIds: pending.map(o => o.orderId), paymentResults: pr, newUserVerification: nv };
    ctx.dirty('ops');
  };
  D.alert = ({ level, type, text, brandId = null, degradeEventId = null }) => {
    const t = ctx.now();
    const duty = db().accounts.accounts.filter(a => a.permissions.includes('degrade.control') && a.status === '正常').map(a => a.accountId);
    const a = { alertId: ctx.ids.alert(t), at: t, level, type, text, brandId, degradeEventId, notified: duty, ackBy: null, ackAt: null };
    db().ops.alerts.push(a);
    ctx.dirty('ops');
    ctx.emit('alert', { alertId: a.alertId, level, type, text });
    return a;
  };
  D.enterDegrade = ({ cause, components, by = 'AC0901', reason = null }) => {
    const cur = D.degrade();
    if (cur.degraded) return D.activeEvent();
    const t = ctx.now();
    const v = D.currentVersion();
    const ev = {
      eventId: ctx.ids.degrade(t), cause, components, enteredAt: t, exitedAt: null, durationMinutes: null, enteredBy: by, exitedBy: null, status: '进行中',
      orderIds: [], orderCount: 0, receivableSum: 0, admissionCounts: {}, limitsVersionId: v.versionId, limits: JSON.parse(JSON.stringify(v.riskLimits)),
      maxHourlyOrders: 0, maxUserOpenOrders: 0, overLimitReminders: 0,
      closure: { status: '未开始', closed: 0, pendingOrderIds: [], paymentResults: {}, newUserVerification: {} },
      timeline: by === 'AC0901' ? [
        { at: addSec(t, -180), by: 'AC0901', action: '检测到故障', text: `检测到${cause}，判断仍可请求运营商开锁` },
        { at: t, by: 'AC0901', action: '降级进入', text: '自动进入降级开单并通知值班人' },
      ] : [{ at: t, by, action: '降级进入', text: `值班人手动进入降级开单：${cause}${reason ? `（${reason}）` : ''}` }],
    };
    db().ops.degradeEvents.push(ev);
    db().ops.current = { ...cur, degraded: true, eventId: ev.eventId, lastDegradeEventId: ev.eventId, cause, components, tightened: false, stopped: false, asOf: t };
    D.alert({ level: '严重', type: '降级进入', text: `已${by === 'AC0901' ? '自动' : '由值班人'}进入降级开单：${cause}`, degradeEventId: ev.eventId });
    ctx.audit({ action: '进入降级', targetType: 'degrade', targetId: ev.eventId, accountId: by, summary: `${by === 'AC0901' ? '运行智能体判断' : '值班人手动'}进入降级开单：${cause}`, reason: reason || `故障组件：${components.join('、')}` });
    ctx.dirty('ops');
    ctx.emit('degrade.changed', { degraded: true, eventId: ev.eventId });
    return ev;
  };
  D.exitDegrade = ({ by = 'AC0901', reason = null } = {}) => {
    const ev = D.activeEvent();
    if (!ev) throw new ApiError('DEGRADE_NOT_ACTIVE');
    const t = ctx.now();
    Object.assign(ev, { exitedAt: t, exitedBy: by, durationMinutes: Math.round((ms(t) - ms(ev.enteredAt)) / 60000), status: '已退出' });
    ev.timeline.push({ at: t, by, action: '降级退出', text: `${by === 'AC0901' ? '故障恢复' : '值班人确认故障恢复'}，退出降级开单，开始恢复闭合${reason ? `（${reason}）` : ''}` });
    db().ops.current = { ...db().ops.current, degraded: false, eventId: null, tightened: false, stopped: false, components: [], cause: null, asOf: t };
    ctx.audit({ action: '退出降级', targetType: 'degrade', targetId: ev.eventId, accountId: by, summary: `故障恢复，退出降级开单（持续 ${ev.durationMinutes} 分钟）`, reason });
    // 逐单闭合（BR-31、BR-32），再对先骑后验新用户补做实名核验（BR-34，未通过的追缴已推送的费用）
    for (const o of ev.orderIds.map(D.order)) if (o && (!o.degraded.closure || o.degraded.closure.status !== '已闭合')) D.closeDegradedOrder(o);
    for (const uid of new Set(ev.orderIds.map(D.order).filter(o => o && o.degraded.admission === '先骑后验新用户').map(o => o.userId))) D.reverify(D.user(uid));
    D.refreshEventClosure(ev);
    const c = ev.closure;
    ev.timeline.push({ at: ctx.now(), by: 'AC0902', action: c.pendingOrderIds.length ? '部分闭合' : '全部闭合', text: `降级单 ${ev.orderCount} 笔，${c.closed} 笔闭合${c.pendingOrderIds.length ? `，${c.pendingOrderIds.length} 笔待处理` : ''}` });
    ctx.dirty('ops');
    ctx.emit('degrade.changed', { degraded: false, eventId: ev.eventId });
    return ev;
  };
  /** 先骑后验补验：用注册时登记的证件信息重做核验；未通过冻结并追缴 */
  D.reverify = u => {
    if (!u || u.realName.status !== '待核验' || !u.realName._birth) return;
    const t = ctx.now();
    const age = ageFromBirth(u.realName._birth, t);
    if (age >= 16) {
      Object.assign(u.realName, { status: '已核验', verifiedAt: t, adult: true, method: '先骑后验补验' });
      delete u.realName.deadline;
      D.notify(u.userId, '实名', '实名核验已通过', '你的实名核验已完成，可以正常骑行。');
    } else {
      Object.assign(u.realName, { status: '未通过', verifiedAt: t, adult: false, method: '先骑后验补验', failReason: '未达法定驾驶年龄（16 周岁）' });
      D.pursue(u);
      D.freeze(u, '先骑后验核验未通过（未达法定驾驶年龄（16 周岁））', '完成实名核验（达到法定年龄）并结清欠费后自动解冻');
    }
    ctx.dirty('users');
  };
  /** 追缴：未付订单立即转欠费 */
  D.pursue = u => {
    for (const o of D.unpaidOrders(u.userId)) { o.payStatus = '欠费'; ctx.dirty('orders'); }
    D.recomputeArrears(u);
  };

  /* ---------------- 到点任务（每次接口调用前执行） ---------------- */
  D.tick = () => {
    const now = ctx.now(), n = ms(now), P = D.params();
    const d = db();
    // 1 计价版本到点生效
    const before = d.pricing.versions.map(v => v.status).join();
    R.refreshVersionChain(d.pricing.versions, now);
    if (before !== d.pricing.versions.map(v => v.status).join()) {
      const cv = D.currentVersion();
      for (const b of d.operators.brands) b.currentServiceFeeRatio = cv.serviceFee.ratios[b.brandId] ?? null;
      ctx.dirty('pricing', 'operators');
      ctx.emit('pricing.changed', { versionId: cv.versionId, reason: '到点生效' });
    }
    // 2 押金用户一键支付期限届满 -> 押金抵扣；其他用户 -> 欠费
    for (const o of d.orders) {
      if (o.payStatus !== '待支付' || !o.billPushedAt || n < ms(o.billPushedAt) + P.payWindowHours * 3600e3) continue;
      if (D.hasPendingPayment(o)) continue; // 渠道结果未知的单交人处理，不自动转欠费
      const u = D.user(o.userId);
      if (u.deposit.mode === '押金') D.depositDeduct(o);
      if (o.payStatus === '待支付') {
        o.payStatus = '欠费'; D.recomputeArrears(u); ctx.dirty('orders', 'users');
        D.notify(u.userId, '欠费', '本单已记为欠费', `订单 ${o.orderId} 超过 ${P.payWindowHours} 小时未支付，已记为欠费，补付后即可继续骑行`, o.orderId);
      }
    }
    // 3 退押到账（原路退回在申请后 48 小时完成，不晚于预计到账时间）
    for (const p of d.payments.payments) {
      if (p.type !== '退押' || p.status !== '处理中' || n < ms(p.createdAt) + REFUND_HOURS * 3600e3) continue;
      const u = D.user(p.userId);
      const dep = u.deposit.mode === '押金' ? u.deposit : u.deposit.previous;
      const fails = (d.runtime.failingRefunds || []).includes(p.paymentId);
      if (fails) {
        Object.assign(p, { status: '失败', failReason: '原支付账户已注销' });
        if (dep && dep.refund) dep.refund.awaitingPayee = true;
        D.notify(u.userId, '押金', '押金原路退回没有成功', '原支付账户已注销，请在"押金"页填写本人实名收款账户，平台公司将人工转账。');
      } else {
        Object.assign(p, { status: '成功', completedAt: addSec(p.createdAt, REFUND_HOURS * 3600) });
        p.channelTradeNo = ctx.ids.tradeNo(p.channel, p.completedAt, true);
        if (dep) { dep.balance = 0; dep.status = '已退'; }
        ctx.audit({ action: '退还押金', targetType: 'payment', targetId: p.paymentId, accountId: 'AC0900', summary: `用户 ${u.userId} 押金 ¥${p.amount.toFixed(1)} 原路退回到账` });
        D.notify(u.userId, '押金', '押金已退回', `押金 ¥${p.amount.toFixed(1)} 已原路退回。`);
      }
      ctx.dirty('payments', 'users');
    }
    // 4 券与月卡状态
    for (const c of d.users.coupons) if (c.status === '未使用' && ms(c.expireAt) < n) { c.status = '已过期'; ctx.dirty('users'); }
    for (const c of d.users.monthCards) {
      const st = n < ms(c.startAt) ? '未生效' : n < ms(c.endAt) ? '生效中' : '已过期';
      if (c.status !== st) { c.status = st; ctx.dirty('users'); }
    }
    // 5 降级单超时长或超金额：只提醒、不结束（6.7）
    for (const o of d.orders) {
      if (o.status !== '骑行中' || !o.degraded || o.degraded.overLimitRemindedAt) continue;
      const sec = (n - ms(o.startAt)) / 1000;
      const v = D.versionsById()[o.pricingVersionId];
      const amt = R.rideFeeC(v, R.billedMinutes(sec));
      if (sec > o.degraded.limitMinutes * 60 || amt > C(o.degraded.limitAmount)) {
        o.degraded.overLimitRemindedAt = now;
        const ev = D.event(o.degraded.eventId); if (ev) ev.overLimitReminders = (ev.overLimitReminders || 0) + 1;
        D.notify(o.userId, '骑行', '请尽快还车', `本次骑行已超过上限（${o.degraded.limitMinutes} 分钟或 ¥${Number(o.degraded.limitAmount).toFixed(1)}），请尽快就近还车。骑行不会被中断，继续计费。`, o.orderId);
        D.alert({ level: '警告', type: '降级单超限', text: `降级单 ${o.orderId} 超过单笔上限，已提醒用户还车`, degradeEventId: o.degraded.eventId });
        ctx.dirty('orders', 'ops');
      }
    }
    // 6 降级持续时长告警
    const ev = D.activeEvent();
    if (ev) {
      const lim = ev.limits.degradeDurationAlertMinutes;
      if (n - ms(ev.enteredAt) >= lim * 60e3 && !d.ops.alerts.some(a => a.degradeEventId === ev.eventId && a.type === '降级持续超阈值')) {
        D.alert({ level: '警告', type: '降级持续超阈值', text: `降级已持续 ${lim} 分钟，请决定是否收紧或停止`, degradeEventId: ev.eventId });
      }
    }
    // 7 欠费超期冻结；先骑后验逾期未核验冻结
    for (const u of d.users.users) {
      if (u.account.status !== '正常') continue;
      if (C(u.arrears) > 0) {
        const old = D.arrearsOrders(u.userId).find(o => o.billPushedAt && n - ms(o.billPushedAt) > P.arrearsFreezeDays * 864e5);
        if (old) D.freeze(u, `欠费追缴未结（超过 ${P.arrearsFreezeDays} 天）`, '结清欠费后自动解冻');
      }
      if (u.realName.status === '待核验' && u.realName.deadline && ms(u.realName.deadline) < n) D.freeze(u, '先骑后验逾期未完成实名核验', '完成实名核验并结清欠费后自动解冻');
    }
    // 8 每日收单对账（次日 02:10 出）
    const rc = d.payments.reconciliation;
    let last = rc.length ? rc[rc.length - 1].date : null;
    if (last) {
      for (;;) {
        const nextDate = addDays(last + ' 00:00:00', 1).slice(0, 10);
        if (n < ms(addDays(nextDate + ' 02:10:00', 1))) break;
        const r = R.computeRecon(d.payments.payments, nextDate);
        rc.push({ reconId: 'RC' + ctx.labelYMD(nextDate + ' 12:00:00'), generatedAt: addDays(nextDate + ' 02:10:00', 1), ...r, frozen: true });
        last = nextDate;
        ctx.dirty('payments');
      }
    }
  };

  return D;
}

export function ageFromBirth(birth, at) {
  const [y, m, d] = birth.split('-').map(Number);
  const t = new Date(at.replace(' ', 'T'));
  let a = t.getFullYear() - y;
  if (t.getMonth() + 1 < m || (t.getMonth() + 1 === m && t.getDate() < d)) a--;
  return a;
}
export { maskName, monthAdd, dist, fmt, addWorkdays };

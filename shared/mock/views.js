// 对外视图（DTO）：按受众裁剪字段并补上页面要直接显示的名称与说明。
import { C, Y, ms, addSec } from './util.js';
import * as R from './rules.js';

export function createViews(ctx, D) {
  const V = {};
  V.brand = b => {
    if (!b) return null;
    const op = D.operator(b.operatorId);
    const cv = D.currentVersion();
    return { brandId: b.brandId, name: b.name, displayName: b.displayName, color: b.color, vehicleType: b.vehicleType, operatorId: b.operatorId, operatorName: op ? op.shortName : null, accessStatus: b.accessStatus, statusSince: b.statusSince, vehicleCount: ctx.db.vehicles.filter(v => v.brandId === b.brandId).length, currentServiceFeeRatio: cv.serviceFee.ratios[b.brandId] ?? null };
  };
  V.stationName = id => (id ? (D.station(id) || {}).name || null : null);

  /** 订单状态的一句话 */
  V.orderStatusText = o => {
    if (o.status === '开锁失败') return '开锁失败 · 未扣费';
    if (o.status === '骑行中') return '骑行中';
    if (o.status === '待人工处理') return '处理中';
    if (o.unlockResult === '超时后成功') return '超时未开锁 · 0 元';
    if (o.degraded && !o.fee) return '费用将稍后收取';
    if (o.refundStatus === '全额退款') return '已全额退款';
    if (o.refundStatus === '部分退款') return '已部分退款';
    if (o.refundStatus === '原路失败·待人工转账') return '退款处理中';
    return { 无需支付: '无需支付', 已支付: '已支付', 待支付: '待支付', 押金抵扣: '已从押金抵扣', 欠费: '欠费', 欠费已补付: '已补付' }[o.payStatus] || '已结束';
  };
  V.bill = o => {
    if (!o.fee) return null;
    const cp = o.fee.couponId ? D.coupon(o.fee.couponId) : null;
    const f = o.fee;
    const notes = [];
    if (o.inStation === false) notes.push(`站点外还车，含调度费 ¥${f.dispatchFee.toFixed(1)}${o.distanceToStationM != null ? `（距最近站点 ${o.distanceToStationM} 米）` : ''}`);
    if (o.inStationBasis === '桩满·围栏内') notes.push('桩位已满，在站点内桩旁还车，不收调度费');
    if (o.inStationBasis === '桩故障或离线·车端判定') notes.push('桩暂时故障，按车辆定位判定在站点内，已正常还车');
    if (o.endMethod === '入桩未锁·平台请求结束') notes.push('车辆入桩后未上锁，平台已远程锁车并结束订单');
    if (o.unlockResult === '超时后成功') notes.push('车辆在超时后才开锁，平台已立即结束，本单 0 元');
    if (o.endMethod === '客服强制结束' && o.forceEnd) notes.push(`客服已远程结束，按 ${o.forceEnd.billedEndAt.slice(11, 16)} 还车计费`);
    if (o.payStatus === '押金抵扣' || C(f.depositDeducted) > 0) notes.push(`超过一键支付期限未付，已从押金抵扣 ¥${f.depositDeducted.toFixed(1)}`);
    const deadline = o.payStatus === '待支付' && o.billPushedAt ? addSec(o.billPushedAt, D.params().payWindowHours * 3600) : null;
    return {
      lines: R.billLines(o, { coupon: cp ? cp.name : null }), billedMinutes: o.billedMinutes, durationSec: o.durationSec,
      receivable: f.receivable, payable: f.payable, paid: Y(C(f.channelPaid) + C(f.depositDeducted)), unpaid: f.unpaid, refunded: f.refunded,
      payStatus: o.payStatus, payMethod: o.payMethod, payDeadline: deadline,
      canPay: (o.payStatus === '待支付' || o.payStatus === '欠费') && !D.hasPendingPayment(o) && C(f.unpaid) > 0,
      couponName: cp ? cp.name : null, monthCardId: f.monthCardId, notes,
      headline: o.payStatus === '已支付' && o.payMethod === '免押自动扣款' ? `已自动扣款 ¥${f.payable.toFixed(1)}` : o.payStatus === '待支付' ? `应付 ¥${f.unpaid.toFixed(1)}` : o.payStatus === '欠费' ? `欠费 ¥${f.unpaid.toFixed(1)}` : o.payStatus === '无需支付' ? '本单无需支付' : o.payStatus === '押金抵扣' ? `已从押金抵扣 ¥${f.depositDeducted.toFixed(1)}` : `已支付 ¥${Y(C(f.channelPaid) + C(f.depositDeducted)).toFixed(1)}`,
    };
  };
  const pick = (o, keys) => Object.fromEntries(keys.map(k => [k, o[k]]));
  const BASE = ['orderId', 'vehicleId', 'brandId', 'channel', 'pricingVersionId', 'status', 'requestAt', 'unlockResult', 'unlockFailReason', 'unlockReportAt', 'startAt', 'startStationId', 'startLocation', 'endAt', 'endMethod', 'endStationId', 'endPileId', 'endLocation', 'inStation', 'inStationBasis', 'distanceToStationM', 'durationSec', 'billedMinutes', 'distanceKm', 'billPushedAt', 'fee', 'payStatus', 'payMethod', 'arrearsPaidAt', 'refundStatus', 'forceEnd'];
  /** 用户本人看的订单 */
  V.userOrder = o => {
    const b = D.brand(o.brandId), v = D.vehicle(o.vehicleId);
    return {
      ...pick(o, BASE), ticketIds: o.ticketIds,
      brandName: b.name, displayName: b.displayName, plateNo: v ? v.plateNo : null, govCode: v ? v.govCode : null,
      startStationName: V.stationName(o.startStationId), endStationName: V.stationName(o.endStationId),
      degraded: o.degraded ? { admission: o.degraded.admission, limitMinutes: o.degraded.limitMinutes, limitAmount: o.degraded.limitAmount, overLimitRemindedAt: o.degraded.overLimitRemindedAt, feeLater: !o.fee, line: '费用将稍后收取' } : null,
      statusText: V.orderStatusText(o), bill: V.bill(o),
    };
  };
  /** 运营商看的订单：不含用户身份（NFR-11） */
  V.operatorOrder = o => {
    const v = D.vehicle(o.vehicleId);
    return {
      orderId: o.orderId, vehicleId: o.vehicleId, plateNo: v ? v.plateNo : null, brandId: o.brandId, brandName: D.brand(o.brandId).name,
      status: o.status, requestAt: o.requestAt, unlockResult: o.unlockResult, unlockFailReason: o.unlockFailReason,
      startAt: o.startAt, startLocation: o.startLocation, endAt: o.endAt, lockReportAt: o.lockReportAt, pileInAt: o.pileInAt, endLocation: o.endLocation,
      endMethod: o.endMethod, inStation: o.inStation, inStationBasis: o.inStationBasis, endStationName: V.stationName(o.endStationId),
      durationSec: o.durationSec, billedMinutes: o.billedMinutes, distanceKm: o.distanceKm, pricingVersionId: o.pricingVersionId,
      receivable: o.fee ? o.fee.receivable : null, dispatchFee: o.fee ? o.fee.dispatchFee : null,
      degraded: !!o.degraded, lateReport: o.lateReport, reportReceivedAt: o.reportReceivedAt,
      settleMonth: o.settleMonth, settlement: o.settlement, settlementNote: o.settlementNote, disputeIds: o.disputeIds,
    };
  };
  /** 平台公司（客服、财务、运维）看的订单 */
  V.adminOrder = o => {
    const u = D.user(o.userId);
    return { ...V.userOrder(o), ...pick(o, ['userId', 'operatorId', 'lockReportAt', 'pileInAt', 'reportReceivedAt', 'lateReport', 'vehicleSideInStation', 'dispatchPromptShown', 'refundFailedPaymentId', 'userClaimedEndAt', 'claimedStationId', 'anomalyNote', 'settleMonth', 'settlement', 'settlementNote', 'disputeIds']),
      degradedRaw: o.degraded, userName: u ? u.name : null, userPhoneMasked: u ? u.phoneMasked : null, operatorName: D.operator(o.operatorId).shortName };
  };
  V.user = u => {
    const riding = D.ridingOrder(u.userId);
    const now = ctx.nowMs();
    const card = ctx.db.users.monthCards.find(c => c.userId === u.userId && ms(c.startAt) <= now && now < ms(c.endAt)) || null;
    const coupons = ctx.db.users.coupons.filter(c => c.userId === u.userId && c.status === '未使用' && ms(c.expireAt) >= now);
    const realName = { status: u.realName.status, verifiedAt: u.realName.verifiedAt, idMasked: u.realName.idMasked, adult: u.realName.adult, method: u.realName.method, failReason: u.realName.failReason || null, deadline: u.realName.deadline || null };
    const dep = { ...u.deposit }; delete dep.previous;
    return {
      userId: u.userId, name: u.name, phoneMasked: u.phoneMasked, channel: u.channel, registeredAt: u.registeredAt,
      realName, deposit: dep, arrears: u.arrears, account: { status: u.account.status, freeze: u.account.freeze, history: u.account.history },
      cancelledAt: u.cancelledAt, ridingOrderId: riding ? riding.orderId : null, unpaidOrderIds: D.unpaidOrders(u.userId).map(o => o.orderId),
      monthCard: card ? { cardId: card.cardId, perOrderCap: card.perOrderCap, startAt: card.startAt, endAt: card.endAt, status: card.status } : null,
      usableCouponCount: coupons.length,
      unreadNotices: ctx.db.notices.filter(n => n.userId === u.userId && !n.read).length,
    };
  };
  return V;
}

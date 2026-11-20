// 业务规则计算（确定性部分：指标算术与状态判定）。只读传入的数据，不写存储。
// 口径：mockdata/SCHEMA.md 第 4 节；需求：v2 BR-01～BR-38。
import { C, Y, ms, fmt, dist, inPolygon, distToPolygon } from './util.js';

/* ================= 计价规则版本（BR-14、BR-15） ================= */
export function activeVersions(versions) {
  return versions.filter(v => !v.revokedAt).sort((a, b) => ms(a.effectiveAt) - ms(b.effectiveAt));
}
/** 时刻 t 有效的版本 */
export function versionAt(versions, t) {
  let cur = null;
  const x = ms(t);
  for (const v of activeVersions(versions)) if (ms(v.effectiveAt) <= x) cur = v;
  return cur;
}
/** 按当前时间重算每个版本的 status 与 effectiveUntil（不覆盖其他字段） */
export function refreshVersionChain(versions, now) {
  const act = activeVersions(versions), n = ms(now);
  act.forEach((v, i) => {
    const next = act[i + 1];
    v.effectiveUntil = next ? next.effectiveAt : null;
    v.status = ms(v.effectiveAt) > n ? '待生效' : (next && ms(next.effectiveAt) <= n ? '已失效' : '生效中');
  });
  versions.filter(v => v.revokedAt).forEach(v => { v.status = '已撤销'; v.effectiveUntil = null; });
  return versions;
}

/* ================= 计费 ================= */
export const billedMinutes = sec => Math.max(1, Math.ceil(sec / 60));
export function rideFeeC(v, minutes) {
  return C(v.ride.startFee) + Math.max(0, minutes - v.ride.startMinutes) * C(v.ride.perMinute);
}
export function priceSummary(v) {
  const r = v.ride;
  return {
    versionId: v.versionId, startFee: r.startFee, startMinutes: r.startMinutes, perMinute: r.perMinute, dispatchFee: v.dispatchFee,
    text: `起步 ¥${r.startFee.toFixed(1)}（含 ${r.startMinutes} 分钟），之后每分钟 ¥${r.perMinute.toFixed(1)}`,
    dispatchText: `站点外还车加收调度费 ¥${Number(v.dispatchFee).toFixed(1)}`,
    monthCard: { price: v.monthCard.price, days: v.monthCard.days, perOrderCap: v.monthCard.perOrderCap },
    deposit: v.deposit.amount, creditFreeThreshold: v.creditFree.threshold,
  };
}
/** 月卡：card.startAt ≤ 订单 startAt < card.endAt（SCHEMA 4.1） */
export function cardFor(cards, userId, startAt) {
  const t = ms(startAt);
  return cards.find(c => c.userId === userId && ms(c.startAt) <= t && t < ms(c.endAt)) || null;
}
/** BR-07：账单推送时刻有效、未使用的券中，抵扣额最大的一张，同额取先到期 */
export function chooseCoupon(coupons, userId, at, remainC) {
  if (remainC <= 0) return null;
  const t = ms(at);
  const cands = coupons.filter(c => c.userId === userId && !c.usedOrderId && c.status !== '已使用' && ms(c.issuedAt) <= t && t <= ms(c.expireAt));
  cands.sort((a, b) => (Math.min(C(b.amount), remainC) - Math.min(C(a.amount), remainC)) || (ms(a.expireAt) - ms(b.expireAt)));
  return cands[0] || null;
}
/**
 * 订单计价。返回 fee（元）与 billedMinutes、选中的券。
 * zero=true 用于 BR-03（超时后才报告开锁成功）：各项均为 0。
 */
export function priceOrder({ version, durationSec, inStation, card, coupons, userId, billAt, zero = false }) {
  const fee = { rideFee: 0, dispatchFee: 0, receivable: 0, monthCardId: null, monthCardDeduct: 0, couponId: null, couponDeduct: 0, payable: 0, channelPaid: 0, depositDeducted: 0, refunded: 0, unpaid: 0, netReceived: 0 };
  if (zero) return { fee, billedMinutes: 0, coupon: null };
  const mins = billedMinutes(durationSec);
  const ride = rideFeeC(version, mins);
  const disp = inStation ? 0 : C(version.dispatchFee);
  const rec = ride + disp;
  let cd = 0;
  if (card) cd = Math.min(C(card.perOrderCap), ride); // 只抵骑行费（假设 A3）
  const cp = chooseCoupon(coupons, userId, billAt, rec - cd);
  const cpd = cp ? Math.min(C(cp.amount), rec - cd) : 0;
  Object.assign(fee, {
    rideFee: Y(ride), dispatchFee: Y(disp), receivable: Y(rec),
    monthCardId: card ? card.cardId : null, monthCardDeduct: Y(cd),
    couponId: cp ? cp.couponId : null, couponDeduct: Y(cpd),
    payable: Y(rec - cd - cpd), unpaid: Y(rec - cd - cpd),
  });
  return { fee, billedMinutes: mins, coupon: cp };
}
/** 依据 fee 中的已收、抵扣、退款重算 unpaid 与 netReceived（恒等式见 SCHEMA 4.1） */
export function settleFee(fee) {
  fee.unpaid = Y(C(fee.payable) - C(fee.channelPaid) - C(fee.depositDeducted));
  fee.netReceived = Y(C(fee.channelPaid) + C(fee.depositDeducted) - C(fee.refunded));
  return fee;
}
/** 账单明细行，给页面直接渲染 */
export function billLines(o, names = {}) {
  const f = o.fee;
  if (!f) return [];
  const L = [{ key: 'rideFee', label: `骑行费（${o.billedMinutes} 分钟）`, amount: f.rideFee }];
  if (C(f.dispatchFee)) L.push({ key: 'dispatchFee', label: '调度费', amount: f.dispatchFee, note: '站点外还车' });
  if (C(f.monthCardDeduct)) L.push({ key: 'monthCardDeduct', label: '月卡抵扣', amount: -f.monthCardDeduct });
  if (C(f.couponDeduct)) L.push({ key: 'couponDeduct', label: names.coupon ? `优惠券（${names.coupon}）` : '优惠券', amount: -f.couponDeduct });
  L.push({ key: 'payable', label: '应付', amount: f.payable, total: true });
  if (C(f.depositDeducted)) L.push({ key: 'depositDeducted', label: '押金抵扣', amount: f.depositDeducted });
  if (C(f.refunded)) L.push({ key: 'refunded', label: '已退款', amount: -f.refunded });
  return L;
}

/* ================= 还车与在点（BR-08～BR-13） ================= */
export function stationAt(stations, pt) {
  return stations.find(s => s.status !== '已停用' && inPolygon(pt, s.fence)) || null;
}
export function nearestStation(stations, pt) {
  let best = null, bd = Infinity;
  for (const s of stations) {
    if (s.status === '已停用') continue;
    const d = distToPolygon(pt, s.fence);
    if (d < bd) { bd = d; best = s; }
  }
  return best ? { station: best, distanceM: Math.round(bd) } : null;
}
export function freePiles(piles, stationId) {
  return piles.filter(p => p.stationId === stationId && p.status === '在线' && !p.occupiedVehicleId);
}
/**
 * 还车判定。
 * method：pile 推入桩位并锁车 | pileNoLock 推入桩位未锁 | lock 用户手动锁车 | app 页面点［还车］
 * 返回 { inStation, basis, station, pile, distanceToStationM, nearest }
 */
export function decideReturn({ stations, piles, location, method, pileId, conflict = false }) {
  const st = stationAt(stations, location);
  const near = nearestStation(stations, location);
  if (method === 'pile' || method === 'pileNoLock') {
    if (!st || st.type !== '有桩') return { error: 'PILE_UNAVAILABLE', station: st, nearest: near };
    const free = freePiles(piles, st.stationId);
    const pile = pileId ? free.find(p => p.pileId === pileId) : free[0];
    if (!pile) {
      const any = piles.some(p => p.stationId === st.stationId && p.status === '在线' && !p.occupiedVehicleId);
      return { error: any || !piles.some(p => p.stationId === st.stationId && p.status !== '在线') ? 'PILE_FULL' : 'PILE_UNAVAILABLE', station: st, nearest: near };
    }
    return { inStation: true, basis: conflict ? '桩与车端冲突·以桩为准' : '桩识别', vehicleSideInStation: conflict ? false : null, station: st, pile, distanceToStationM: null, nearest: near };
  }
  if (!st) return { inStation: false, basis: '站外', station: null, pile: null, distanceToStationM: near ? near.distanceM : null, nearest: near };
  if (st.type === '无桩') return { inStation: true, basis: '无桩站点·车端判定', station: st, pile: null, distanceToStationM: null, nearest: near };
  const sp = piles.filter(p => p.stationId === st.stationId);
  const free = sp.filter(p => p.status === '在线' && !p.occupiedVehicleId);
  if (free.length) return { inStation: true, basis: '车端判定', station: st, pile: null, distanceToStationM: null, nearest: near };
  const broken = sp.some(p => p.status !== '在线');
  return { inStation: true, basis: broken ? '桩故障或离线·车端判定' : '桩满·围栏内', station: st, pile: null, distanceToStationM: null, nearest: near };
}

/* ================= 服务费（SCHEMA 4.2，BR-18） ================= */
export function feeOf(baseC, ratio) { return Math.floor(baseC * ratio + 0.5 + 1e-9); }
export function refundedBy(payments, orderId, t) {
  const x = ms(t);
  return payments.reduce((s, p) => s + (p.orderId === orderId && (p.type === '退款' || p.type === '人工转账退款') && p.status === '成功' && ms(p.completedAt) <= x ? C(p.amount) : 0), 0);
}
/** 订单在时刻 t 的服务费基数（分） */
export function baseAt(o, t, payments, corrections) {
  const f = o.fee;
  if (!f || C(f.receivable) === 0) return 0;
  const rf = refundedBy(payments, o.orderId, t);
  if (rf > 0 && rf >= C(f.channelPaid) + C(f.depositDeducted)) return 0;
  const x = ms(t);
  return C(f.receivable) - rf + (corrections[o.orderId] || []).reduce((s, c) => s + (ms(c.at) <= x ? C(c.baseDelta) : 0), 0);
}
export function ratioOf(o, versionsById) {
  const v = versionsById[o.pricingVersionId];
  return v && v.serviceFee.ratios[o.brandId] != null ? v.serviceFee.ratios[o.brandId] : 0;
}
/** 已采纳异议的基数更正：{orderId: [{at, baseDelta, disputeId}]} */
export function correctionsMap(settlements, disputes) {
  const adjItems = {};
  for (const s of settlements) for (const a of s.adjustments) adjItems[a.adjustmentId] = a.items;
  const m = {};
  for (const d of disputes) {
    if (d.status !== '已采纳') continue;
    const list = d.corrections || (adjItems[d.adjustmentId] || []).map(it => ({ orderId: it.orderId, baseDelta: it.baseDelta }));
    for (const c of list) (m[c.orderId] = m[c.orderId] || []).push({ at: d.decidedAt, baseDelta: c.baseDelta, disputeId: d.disputeId });
  }
  return m;
}
/** 每单最近一次结算记录：{orderId: {base, fee, at, settlementId}} */
export function settledIndex(settlements) {
  const idx = {};
  for (const s of [...settlements].sort((a, b) => ms(a.generatedAt) - ms(b.generatedAt))) {
    for (const d of s.details) idx[d.orderId] = { base: C(d.base), fee: C(d.fee), at: s.generatedAt, settlementId: s.settlementId };
    for (const a of s.adjustments) for (const it of a.items) {
      const prev = idx[it.orderId] || { fee: 0 };
      idx[it.orderId] = { base: C(it.newBase), fee: prev.fee + C(it.feeDelta), at: s.generatedAt, settlementId: idx[it.orderId] ? idx[it.orderId].settlementId : s.settlementId };
    }
  }
  return idx;
}

/**
 * 生成（或预览）某运营商某结算月的结算单内容。
 * ctx: { orders, payments, settlements, disputes, versionsById, brands, monthCards, labelMonth(t), operatorId, month, at, allBrandsCardOrders? }
 */
export function buildSettlement(x) {
  const { orders, payments, settlements, disputes, versionsById, operatorId, month, at } = x;
  const corr = correctionsMap(settlements, disputes);
  const idx = settledIndex(settlements);
  const atMs = ms(at);
  const settledIds = new Set(Object.keys(idx));
  const mine = orders.filter(o => o.operatorId === operatorId && o.status === '已结束' && o.fee);
  const details = [];
  for (const o of mine) {
    if (o.settleMonth !== month || settledIds.has(o.orderId) || !o.reportReceivedAt || ms(o.reportReceivedAt) > atMs) continue;
    const b = baseAt(o, at, payments, corr), r = ratioOf(o, versionsById);
    details.push({
      orderId: o.orderId, brandId: o.brandId, startAt: o.startAt, endAt: o.endAt, billedMinutes: o.billedMinutes, distanceKm: o.distanceKm,
      degraded: !!o.degraded, pricingVersionId: o.pricingVersionId, receivable: o.fee.receivable,
      refundedAtGeneration: Y(refundedBy(payments, o.orderId, at)), base: Y(b), ratio: r, fee: Y(feeOf(b, r)),
      remark: o.degraded ? '降级单' : (C(o.fee.receivable) === 0 ? '0 元订单' : null),
    });
  }
  details.sort((a, b) => ms(a.endAt) - ms(b.endAt));
  // 调整项
  const groups = new Map();
  const push = (type, o, item, key, reasonHint) => {
    const k = `${type}|${o.brandId}|${key || ''}`;
    if (!groups.has(k)) groups.set(k, { type, brandId: o.brandId, items: [], reasonHint });
    groups.get(k).items.push(item);
  };
  for (const o of mine) {
    const r = ratioOf(o, versionsById);
    if (!settledIds.has(o.orderId)) {
      if (o.settleMonth && o.settleMonth < month && o.reportReceivedAt && ms(o.reportReceivedAt) <= atMs) {
        const nb = baseAt(o, at, payments, corr);
        push('晚到补报', o, { orderId: o.orderId, endAt: o.endAt, previousBase: 0, newBase: Y(nb), baseDelta: Y(nb), ratio: r, feeDelta: Y(feeOf(nb, r)) });
      }
      continue;
    }
    const last = idx[o.orderId];
    if (ms(last.at) >= atMs) continue;
    const nb = baseAt(o, at, payments, corr);
    if (nb === last.base) continue;
    const disp = (corr[o.orderId] || []).find(c => ms(c.at) > ms(last.at) && ms(c.at) <= atMs);
    push(disp ? '异议核实' : '出单后退款', o, { orderId: o.orderId, endAt: o.endAt, previousBase: Y(last.base), newBase: Y(nb), baseDelta: Y(nb - last.base), ratio: r, feeDelta: Y(feeOf(nb, r) - last.fee) }, disp ? disp.disputeId : '', disp ? disp.disputeId : null);
  }
  const adjustments = [...groups.values()].map(g => ({
    type: g.type, brandId: g.brandId, items: g.items, disputeId: g.reasonHint,
    baseDelta: Y(g.items.reduce((s, i) => s + C(i.baseDelta), 0)), feeDelta: Y(g.items.reduce((s, i) => s + C(i.feeDelta), 0)),
  }));
  return { details, adjustments };
}
export function settlementTotals(s, brandIds) {
  const sum = (arr, k) => Y(arr.reduce((t, d) => t + C(d[k]), 0));
  const detailFee = sum(s.details, 'fee'), adjustmentFee = sum(s.adjustments, 'feeDelta');
  s.totals = {
    orderCount: s.details.length, receivableSum: sum(s.details, 'receivable'), baseSum: sum(s.details, 'base'),
    detailFee, adjustmentFee, payableFee: Y(C(detailFee) + C(adjustmentFee)),
  };
  const prevBrands = Object.fromEntries((s.brands || []).map(b => [b.brandId, b]));
  s.brands = brandIds.filter(b => s.details.some(d => d.brandId === b) || s.adjustments.some(a => a.brandId === b) || prevBrands[b]).map(b => {
    const bd = s.details.filter(d => d.brandId === b);
    const groups = {};
    for (const d of bd) {
      const g = groups[d.ratio] = groups[d.ratio] || { ratio: d.ratio, pricingVersionIds: [], orderCount: 0, baseSum: 0, fee: 0 };
      if (!g.pricingVersionIds.includes(d.pricingVersionId)) g.pricingVersionIds.push(d.pricingVersionId);
      g.orderCount++; g.baseSum += C(d.base); g.fee += C(d.fee);
    }
    const prev = prevBrands[b] || {};
    return {
      brandId: b, orderCount: bd.length, receivableSum: sum(bd, 'receivable'), baseSum: sum(bd, 'base'), detailFee: sum(bd, 'fee'),
      ratioBreakdown: Object.values(groups).map(g => ({ ...g, pricingVersionIds: g.pricingVersionIds.sort(), baseSum: Y(g.baseSum), fee: Y(g.fee) })),
      adjustmentFee: sum(s.adjustments.filter(a => a.brandId === b), 'feeDelta'),
      monthCardOrderCount: prev.monthCardOrderCount || 0, monthCardShare: prev.monthCardShare || 0,
    };
  });
  return s;
}
/** 月卡收入按单数分摊到品牌（最大余数法，到分） */
export function allocateMonthCard(incomeC, counts) {
  const total = Object.values(counts).reduce((a, b) => a + b, 0);
  const out = {};
  if (!total) { for (const k of Object.keys(counts)) out[k] = 0; return out; }
  const raw = Object.entries(counts).map(([k, n]) => ({ k, exact: incomeC * n / total }));
  raw.forEach(r => { out[r.k] = Math.floor(r.exact); });
  let left = incomeC - Object.values(out).reduce((a, b) => a + b, 0);
  raw.sort((a, b) => (b.exact - Math.floor(b.exact)) - (a.exact - Math.floor(a.exact)));
  for (const r of raw) { if (left <= 0) break; out[r.k]++; left--; }
  return out;
}

/* ================= 收单对账（SCHEMA 4.5，R-25） ================= */
const INCOME_IN = ['订单扣款', '订单支付', '欠费补付'];
const isChannel = p => p.account === '骑行收入户' && (p.channel === '微信支付' || p.channel === '支付宝');
const sign = p => (p.direction === '收' ? 1 : -1);
export function computeRecon(payments, date) {
  const actual = { 微信支付: 0, 支付宝: 0 };
  for (const p of payments) if (isChannel(p) && p.status === '成功' && p.channelSettledDate === date) actual[p.channel] += C(p.amount) * sign(p);
  const ev = payments.filter(p => p.status === '成功' && p.completedAt && p.completedAt.slice(0, 10) === date);
  const s = f => ev.filter(f).reduce((t, p) => t + C(p.amount), 0);
  const op = s(p => p.account === '骑行收入户' && INCOME_IN.includes(p.type));
  const card = s(p => p.type === '月卡购买'), dep = s(p => p.type === '押金抵扣');
  const ro = s(p => p.type === '退款'), rm = s(p => p.type === '人工转账退款');
  const net = op + dep - ro - rm, exp = net + card - dep, tot = actual['微信支付'] + actual['支付宝'];
  const items = [];
  for (const p of ev) {
    if (p.type === '人工转账退款') items.push({ type: '人工转账退款不经收单渠道', amount: Y(C(p.amount)), paymentId: p.paymentId, orderId: p.orderId, status: '已处理', handledBy: 'AC0902', note: '银行转账退回，收单渠道无对应流水' });
    else if (isChannel(p) && !p.channelSettledDate) items.push({ type: '渠道未到账', amount: Y(-C(p.amount) * sign(p)), paymentId: p.paymentId, orderId: p.orderId, status: '待处理', handledBy: null, note: '平台记录成功，渠道对账单无此笔，待向渠道核实' });
    else if (isChannel(p) && p.channelSettledDate !== date) items.push({ type: '跨日到账', amount: Y(-C(p.amount) * sign(p)), paymentId: p.paymentId, orderId: p.orderId, status: '已处理', handledBy: 'AC0902', note: `渠道于 ${p.channelSettledDate} 入账` });
  }
  for (const p of payments) if (isChannel(p) && p.status === '成功' && p.channelSettledDate === date && p.completedAt && p.completedAt.slice(0, 10) !== date)
    items.push({ type: '跨日到账', amount: Y(C(p.amount) * sign(p)), paymentId: p.paymentId, orderId: p.orderId, status: '已处理', handledBy: 'AC0902', note: `${p.completedAt.slice(0, 10)} 发生、本日入账` });
  return {
    date, channelActual: { wechat: Y(actual['微信支付']), alipay: Y(actual['支付宝']), total: Y(tot) },
    orderPayments: Y(op), depositDeducted: Y(dep), refundsOriginal: Y(ro), refundsManual: Y(rm), orderNetReceived: Y(net),
    monthCardIncome: Y(card), expected: Y(exp), diff: Y(tot - exp), diffItems: items,
    status: reconStatus(items),
  };
}
export function reconStatus(items) {
  if (!items.length) return '平';
  return items.some(i => i.status === '待处理') ? '差异待处理' : '差异已处理';
}

/* ================= 押金专户 ================= */
export function depositLedger(payments) {
  const s = t => payments.filter(p => p.account === '押金专户' && p.type === t && p.status === '成功').reduce((a, p) => a + C(p.amount), 0);
  const paid = s('押金缴纳'), top = s('押金补缴'), ded = s('押金抵扣'), ref = s('退押'), man = s('人工转账退押');
  const pending = payments.filter(p => p.type === '退押' && p.status === '处理中').reduce((a, p) => a + C(p.amount), 0);
  return { paidIn: Y(paid), toppedUp: Y(top), deducted: Y(ded), refunded: Y(ref), manualRefunded: Y(man), balance: Y(paid + top - ded - ref - man), refundingAmount: Y(pending) };
}

/* ================= 附近车辆 ================= */
export function nearby(vehicles, rentable, center, radiusM, excludeId, limit = 5) {
  return vehicles.filter(v => v.vehicleId !== excludeId && rentable(v))
    .map(v => ({ v, d: dist(center, v.location) })).filter(x => x.d <= radiusM)
    .sort((a, b) => a.d - b.d).slice(0, limit);
}

export const nowStr = d => fmt(d);

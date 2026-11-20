// 平台公司财务：月结算单、调整项、异议、收单对账、押金专户、欠费与退款失败（R-11、R-18、R-23、R-25）。
import { ApiError } from './errors.js';
import { C, Y, ms, fmt, addDays, addSec, monthAdd, paginate, clone, csv } from './util.js';
import * as R from './rules.js';

export function createFinanceApi(ctx, D, V, A, g, refundFailures) {
  const db = () => ctx.db;
  const S = () => db().settlements;

  /* ================= 结算单生成（BR-17～BR-20） ================= */
  const adjReason = (type, brandId, items, disputeId) => {
    const b = D.brand(brandId).name;
    if (type === '晚到补报') return `${b} 上期结束、出单后才收到运营商补报的 ${items.length} 单，按原结算月计价后计入本期`;
    if (type === '异议核实') return `${b} 异议 ${disputeId} 经对账智能体比对、财务核实采纳，${items.length} 单服务费基数更正`;
    return `${b} 上期已结算的 ${items.length} 单在出单后发生退款，按退款后金额更正`;
  };
  /** 组装一张结算单（不落库）。at 为出单时刻（真实时间） */
  function assemble(operatorId, month, at, persistIds) {
    const b = R.buildSettlement({ orders: db().orders, payments: db().payments.payments, settlements: S().settlements, disputes: S().disputes, versionsById: D.versionsById(), operatorId, month, at });
    const ym = month.replace('-', '');
    const s = {
      settlementId: `ST-M-${ym}-${operatorId}`, operatorId, month, generatedAt: at, dueDate: addDays(at, 5).slice(0, 10), status: persistIds ? '待审核' : '未出单',
      reviewedBy: null, reviewedAt: null, sentAt: null, paidAt: null, bankTradeNo: null, invoiceStatus: null,
      totals: null, monthCard: null, brands: [], details: b.details,
      adjustments: b.adjustments.map((x, i) => ({ adjustmentId: `ADJ-${ym}-${operatorId}-${String(i + 1).padStart(2, '0')}`, type: x.type, brandId: x.brandId, items: x.items, baseDelta: x.baseDelta, feeDelta: x.feeDelta, reason: adjReason(x.type, x.brandId, x.items, x.disputeId), disputeId: x.disputeId || null, createdBy: 'AC0902', createdAt: at, confirmedBy: null, confirmedAt: null })),
      note: D.operator(operatorId).status === '已退出' ? `运营商已退出，本期为最后一期结算` : null,
    };
    R.settlementTotals(s, D.operator(operatorId).brandIds);
    return s;
  }
  /** 月卡收入按骑行单数分摊到品牌（R-19 默认规则；假设 A1：只作收入归属展示，不进服务费） */
  function applyMonthCard(list, month) {
    const income = db().payments.payments.filter(p => p.type === '月卡购买' && p.status === '成功' && ctx.labelMonth(p.completedAt) === month).reduce((s, p) => s + C(p.amount), 0);
    const counts = {};
    for (const o of db().orders) if (o.settleMonth === month && o.status === '已结束' && o.fee && C(o.fee.monthCardDeduct) > 0) counts[o.brandId] = (counts[o.brandId] || 0) + 1;
    const shares = R.allocateMonthCard(income, counts);
    const total = Object.values(counts).reduce((a, b) => a + b, 0);
    for (const s of list) {
      s.monthCard = { platformIncome: Y(income), allocationRule: D.currentVersion().monthCard.allocation, platformCardOrders: total, note: '服务费基数取订单应收，月卡收入分摊仅作收入归属展示，不进入服务费计算' };
      for (const b of s.brands) { b.monthCardOrderCount = counts[b.brandId] || 0; b.monthCardShare = Y(shares[b.brandId] || 0); }
    }
  }
  D.previewSettlement = operatorId => {
    const month = ctx.labelMonth(ctx.now());
    const s = assemble(operatorId, month, ctx.now(), false);
    applyMonthCard([s], month);
    s.previewNote = `本期（${month}）尚未出单，按截至现在的订单与事件实时汇总；次月 1 日 00:30 出单`;
    return s;
  };
  D.generateSettlements = (month, at) => {
    const created = [];
    for (const op of db().operators.operators) {
      if (S().settlements.some(s => s.operatorId === op.operatorId && s.month === month)) continue;
      const s = assemble(op.operatorId, month, at, true);
      if (!s.details.length && !s.adjustments.length) continue;
      created.push(s);
    }
    applyMonthCard(created, month);
    for (const s of created) {
      S().settlements.push(s);
      for (const d of s.details) { const o = D.order(d.orderId); o.settlement = { settlementId: s.settlementId, kind: '明细', adjustmentId: null }; }
      for (const a of s.adjustments) for (const it of a.items) {
        const o = D.order(it.orderId);
        if (a.type === '晚到补报') { o.settlement = { settlementId: s.settlementId, kind: '调整项', adjustmentId: a.adjustmentId }; o.settlementNote = `出单后才收到补报，计入 ${month} 结算单调整项`; }
        else o.settlementNote = `${a.type === '异议核实' ? '异议核实' : '出单后退款'}，差额计入 ${month} 结算单调整项 ${a.adjustmentId}`;
        if (a.type === '异议核实' && a.disputeId) { const d = S().disputes.find(x => x.disputeId === a.disputeId); if (d) d.adjustmentId = a.adjustmentId; }
      }
      ctx.audit({ action: '生成结算单', targetType: 'settlement', targetId: s.settlementId, accountId: 'AC0902', summary: `生成 ${D.operator(s.operatorId).shortName} ${month} 结算单，应付服务费 ¥${s.totals.payableFee.toFixed(2)}`, at });
    }
    if (created.length) { ctx.dirty('settlements', 'orders'); ctx.emit('settlement.changed', { month, created: created.map(s => s.settlementId) }); }
    return created;
  };
  /** 到点自动出单：标签时间到次月 1 日 00:30 */
  D.autoSettle = () => {
    const months = S().settlements.map(s => s.month).sort();
    let m = monthAdd(months[months.length - 1], 1);
    for (;;) {
      const boundary = `${monthAdd(m, 1)}-01 00:30:00`;
      if (ctx.label(ctx.now()) < boundary) break;
      D.generateSettlements(m, ctx.realOf(boundary));
      m = monthAdd(m, 1);
    }
  };

  const sView = (s, withDetails) => {
    const x = clone(s);
    x.operatorName = D.operator(s.operatorId).shortName;
    x.brands = (x.brands || []).map(b => ({ ...b, brandName: D.brand(b.brandId).name }));
    x.adjustments = x.adjustments.map(a => ({ ...a, brandName: D.brand(a.brandId).name, createdByName: D.actorName(a.createdBy), confirmedByName: D.actorName(a.confirmedBy) }));
    x.overdue = !s.paidAt && s.dueDate && ctx.now().slice(0, 10) > s.dueDate;
    x.reviewedByName = D.actorName(s.reviewedBy);
    if (!withDetails) { x.detailCount = s.details.length; delete x.details; }
    return x;
  };
  const getS = id => ctx.get(S().settlements, 'settlementId', id, '结算单');
  const recalc = s => R.settlementTotals(s, D.operator(s.operatorId).brandIds);

  const settlements = {
    list: (f = {}) => g.call('finance.settlements.list', 'settlement.manage', () => {
      const list = S().settlements.filter(s => (!f.month || s.month === f.month) && (!f.operatorId || s.operatorId === f.operatorId) && (!f.status || s.status === f.status));
      return [...list].sort((a, b) => (a.month === b.month ? (a.operatorId < b.operatorId ? -1 : 1) : a.month < b.month ? 1 : -1)).map(s => sView(s, false));
    }),
    get: id => g.call('finance.settlements.get', 'settlement.manage', () => sView(getS(id), true)),
    /** 本期实时预览（尚未出单） */
    preview: operatorId => g.call('finance.settlements.preview', 'settlement.manage', () => sView(D.previewSettlement(ctx.get(db().operators.operators, 'operatorId', operatorId, '运营商').operatorId), true)),
    /** 手动出单：该结算月已结束（标签时间到次月 1 日 00:30）且尚未生成 */
    generate: month => g.call('finance.settlements.generate', 'settlement.manage', () => {
      if (!/^\d{4}-\d{2}$/.test(month || '')) throw new ApiError('INVALID', '结算月格式为 YYYY-MM');
      if (S().settlements.some(s => s.month === month)) throw new ApiError('ALREADY_GENERATED');
      if (ctx.label(ctx.now()) < `${monthAdd(month, 1)}-01 00:00:00`) throw new ApiError('MONTH_NOT_ENDED', `${month} 结算月尚未结束，${monthAdd(month, 1)}-01 起可生成`);
      return D.generateSettlements(month, ctx.now()).map(s => sView(s, false));
    }, { write: true }),
    confirmAdjustment: (id, adjustmentId, note) => g.call('finance.settlements.confirmAdjustment', 'settlement.adjust', a => {
      const s = getS(id);
      if (s.status !== '待审核') throw new ApiError('SETTLEMENT_STATE');
      const adj = s.adjustments.find(x => x.adjustmentId === adjustmentId);
      if (!adj) throw new ApiError('NOT_FOUND', '没有找到该调整项');
      if (adj.confirmedBy) throw new ApiError('ALREADY_DONE', '该调整项已确认');
      adj.confirmedBy = a.accountId; adj.confirmedAt = ctx.now();
      ctx.audit({ action: '调整结算单', targetType: 'settlement', targetId: id, summary: `确认调整项 ${adjustmentId}（${adj.type}，服务费 ${adj.feeDelta >= 0 ? '+' : ''}${adj.feeDelta.toFixed(2)}）`, reason: note || adj.reason });
      ctx.dirty('settlements');
      return sView(s, false);
    }, { write: true }),
    /** 人工录入调整项（须写事由）。feeDelta 为服务费增减（元，两位小数） */
    addAdjustment: (id, { brandId, feeDelta, orderIds = [] } = {}, reason) => g.call('finance.settlements.addAdjustment', 'settlement.adjust', a => {
      const why = ctx.reason(reason);
      const s = getS(id);
      if (s.status !== '待审核') throw new ApiError('SETTLEMENT_STATE', '只有待审核的结算单可以录入调整项');
      if (!D.operator(s.operatorId).brandIds.includes(brandId)) throw new ApiError('INVALID', '品牌不属于该运营商');
      const n = Number(feeDelta);
      if (!isFinite(n) || n === 0 || Math.abs(n * 100 - Math.round(n * 100)) > 1e-6) throw new ApiError('INVALID', '调整金额须为非零、最多两位小数');
      const t = ctx.now();
      const adj = { adjustmentId: `ADJ-${s.month.replace('-', '')}-${s.operatorId}-${String(s.adjustments.length + 1).padStart(2, '0')}`, type: '人工调整', brandId, items: [], orderIds, baseDelta: 0, feeDelta: Y(C(n)), reason: why, createdBy: a.accountId, createdAt: t, confirmedBy: a.accountId, confirmedAt: t };
      s.adjustments.push(adj);
      recalc(s);
      ctx.audit({ action: '调整结算单', targetType: 'settlement', targetId: id, summary: `录入调整项 ${adj.adjustmentId}，服务费 ${n >= 0 ? '+' : ''}${n.toFixed(2)}`, reason: why });
      ctx.dirty('settlements'); ctx.emit('settlement.changed', { settlementId: id });
      return sView(s, false);
    }, { write: true }),
    review: (id, note) => g.call('finance.settlements.review', 'settlement.manage', a => {
      const s = getS(id);
      if (s.status !== '待审核') throw new ApiError('SETTLEMENT_STATE');
      if (s.adjustments.some(x => !x.confirmedBy)) throw new ApiError('ADJUSTMENT_UNCONFIRMED');
      Object.assign(s, { status: '已审核', reviewedBy: a.accountId, reviewedAt: ctx.now() });
      ctx.audit({ action: '审核结算单', targetType: 'settlement', targetId: id, summary: `审核通过 ${id}，应付服务费 ¥${s.totals.payableFee.toFixed(2)}`, reason: note || null });
      ctx.dirty('settlements'); ctx.emit('settlement.changed', { settlementId: id });
      return sView(s, false);
    }, { write: true }),
    send: id => g.call('finance.settlements.send', 'settlement.manage', () => {
      const s = getS(id);
      if (s.status !== '已审核') throw new ApiError('SETTLEMENT_STATE', '审核通过后才能发送给运营商');
      Object.assign(s, { status: '已发送', sentAt: ctx.now() });
      ctx.audit({ action: '发送结算单', targetType: 'settlement', targetId: id, summary: `结算单 ${id} 已发送给${D.operator(s.operatorId).shortName}` });
      ctx.dirty('settlements'); ctx.emit('settlement.changed', { settlementId: id });
      return sView(s, false);
    }, { write: true }),
    registerPayment: (id, { bankTradeNo, paidAt } = {}) => g.call('finance.settlements.registerPayment', 'settlement.manage', () => {
      const s = getS(id);
      if (s.status !== '已发送') throw new ApiError('SETTLEMENT_STATE', '发送给运营商后才能登记到账');
      if (!/^[A-Z0-9]{8,32}$/.test(String(bankTradeNo || ''))) throw new ApiError('INVALID', '请填写银行流水号');
      const at = paidAt || ctx.now();
      Object.assign(s, { status: '已到账', paidAt: at, bankTradeNo, invoiceStatus: '待开票' });
      ctx.audit({ action: '登记到账', targetType: 'settlement', targetId: id, summary: `登记 ${id} 到账 ¥${s.totals.payableFee.toFixed(2)}${at.slice(0, 10) > s.dueDate ? '（晚于到账期限）' : ''}` });
      ctx.dirty('settlements'); ctx.emit('settlement.changed', { settlementId: id });
      return sView(s, false);
    }, { write: true }),
    export: id => g.call('finance.settlements.export', 'settlement.manage', () => {
      const s = getS(id);
      const cols = [{ title: '订单号', key: 'orderId' }, { title: '品牌', value: d => D.brand(d.brandId).name }, { title: '结束', key: 'endAt' }, { title: '计费分钟', key: 'billedMinutes' }, { title: '订单应收', key: 'receivable' }, { title: '服务费基数', key: 'base' }, { title: '比例', key: 'ratio' }, { title: '服务费', key: 'fee' }, { title: '备注', key: 'remark' }];
      return { fileName: `${id}.csv`, csv: csv(cols, s.details) };
    }),
  };

  /* ================= 异议处理（R-23） ================= */
  const disputes = {
    list: (f = {}) => g.call('finance.disputes.list', 'dispute.decide', () => S().disputes.filter(d => (!f.status || d.status === f.status) && (!f.operatorId || d.operatorId === f.operatorId)).sort((a, b) => ms(b.submittedAt) - ms(a.submittedAt)).map(d => ({ ...clone(d), operatorName: D.operator(d.operatorId).shortName, submittedByName: D.actorName(d.submittedBy), decidedByName: D.actorName(d.decidedBy) }))),
    get: id => g.call('finance.disputes.get', 'dispute.decide', () => {
      const d = ctx.get(S().disputes, 'disputeId', id, '异议');
      return { ...clone(d), operatorName: D.operator(d.operatorId).shortName, orders: d.orderIds.map(x => V.adminOrder(D.order(x))) };
    }),
    /**
     * 财务确认：decision 为 采纳 | 驳回 | 无需调整。采纳时长异议须给出核实后的分钟数：corrections=[{orderId, minutes}]
     * 采纳只更正服务费基数，不向用户补收；差额进下一期调整项（BR-19）
     */
    decide: (id, { decision, corrections = [] } = {}, reason) => g.call('finance.disputes.decide', 'dispute.decide', a => {
      const why = ctx.reason(reason);
      const d = ctx.get(S().disputes, 'disputeId', id, '异议');
      if (d.status !== '待核实') throw new ApiError('ALREADY_DONE', '该异议已处理');
      const map = { 采纳: '已采纳', 驳回: '已驳回', 无需调整: '无需调整' };
      if (!map[decision]) throw new ApiError('INVALID', '处理结果只能是采纳、驳回或无需调整');
      if (decision === '采纳') {
        if (d.field === '里程') throw new ApiError('INVALID', '里程不参与计价，采纳不会改变服务费，请选择"无需调整"');
        if (!corrections.length) throw new ApiError('INVALID', '采纳时请填写每单核实后的分钟数');
        d.corrections = corrections.map(c => {
          const o = D.order(c.orderId);
          if (!o || !d.orderIds.includes(c.orderId) || !o.fee) throw new ApiError('INVALID', `订单 ${c.orderId} 不在该异议中`);
          const mins = Math.round(Number(c.minutes));
          if (!(mins >= 1 && mins <= 600)) throw new ApiError('INVALID', '核实后的分钟数不正确');
          const v = D.versionsById()[o.pricingVersionId];
          const newRec = R.rideFeeC(v, mins) + C(o.fee.dispatchFee);
          return { orderId: c.orderId, minutes: mins, baseDelta: Y(newRec - C(o.fee.receivable)) };
        });
      }
      Object.assign(d, { status: map[decision], decidedAt: ctx.now(), decidedBy: a.accountId, decisionReason: why });
      ctx.audit({ action: '处理异议', targetType: 'dispute', targetId: id, summary: `异议 ${id} ${map[decision]}${d.corrections ? `，基数更正 ${d.corrections.map(c => `${c.orderId} ${c.baseDelta >= 0 ? '+' : ''}${c.baseDelta.toFixed(1)}`).join('、')}，计入下一期调整项` : ''}`, reason: why });
      ctx.dirty('settlements'); ctx.emit('dispute.changed', { disputeId: id });
      return clone(d);
    }, { write: true }),
  };

  /* ================= 收单对账（R-25，G3 第①条） ================= */
  function reconList() {
    const rc = db().payments.reconciliation;
    const out = [...rc];
    const last = rc.length ? rc[rc.length - 1].date : null;
    const today = ctx.now().slice(0, 10);
    if (last) for (let d = addDays(last + ' 00:00:00', 1).slice(0, 10); d <= today; d = addDays(d + ' 00:00:00', 1).slice(0, 10)) {
      out.push({ reconId: 'RC' + ctx.labelYMD(d + ' 12:00:00'), generatedAt: null, ...R.computeRecon(db().payments.payments, d), status: '当日未出', frozen: false });
    }
    return out;
  }
  const recon = {
    list: (f = {}) => g.call('finance.recon.list', 'recon.manage', () => {
      const all = reconList().filter(r => g.inRange(r.date, f.from, f.to) && (!f.status || r.status === f.status));
      const pg = paginate([...all].reverse(), f);
      const sum = k => Y(all.reduce((s, r) => s + C(k(r)), 0));
      return { ...pg, totals: { channelActual: sum(r => r.channelActual.total), orderNetReceived: sum(r => r.orderNetReceived), monthCardIncome: sum(r => r.monthCardIncome), depositDeducted: sum(r => r.depositDeducted), expected: sum(r => r.expected), diff: sum(r => r.diff) }, formula: '应到账 = 订单实收 + 月卡款 − 押金抵扣；差异 = 渠道实到 − 应到账' };
    }),
    get: date => g.call('finance.recon.get', 'recon.manage', () => {
      const r = reconList().find(x => x.date === date || x.reconId === date);
      if (!r) throw new ApiError('NOT_FOUND', '没有该日的对账记录');
      return { ...clone(r), payments: db().payments.payments.filter(p => (p.completedAt && p.completedAt.slice(0, 10) === r.date) || p.channelSettledDate === r.date).filter(p => p.account === '骑行收入户' || p.type === '押金抵扣') };
    }),
    /** 处理差异项（如向渠道核实后补到账、确认已冲正），写处理说明 */
    handleDiff: (reconId, index, note) => g.call('finance.recon.handleDiff', 'recon.manage', a => {
      const why = ctx.reason(note);
      const r = db().payments.reconciliation.find(x => x.reconId === reconId);
      if (!r) throw new ApiError('CONFLICT', '当日对账次日 02:10 出具后才能处理差异');
      const it = r.diffItems[index];
      if (!it) throw new ApiError('NOT_FOUND', '没有找到该差异项');
      if (it.status === '已处理') throw new ApiError('ALREADY_DONE', '该差异已处理');
      Object.assign(it, { status: '已处理', handledBy: a.accountId, note: why });
      r.status = R.reconStatus(r.diffItems);
      ctx.audit({ action: '处理对账差异', targetType: 'payment', targetId: it.paymentId || reconId, summary: `处理 ${r.date} 收单对账差异（${it.type} ${it.amount >= 0 ? '+' : ''}${Number(it.amount).toFixed(1)}）`, reason: why });
      ctx.dirty('payments');
      return clone(r);
    }, { write: true }),
  };

  /* ================= 押金专户（R-18） ================= */
  const deposit = {
    summary: () => g.call('finance.deposit.summary', 'deposit.view', () => {
      const L = R.depositLedger(db().payments.payments);
      const us = db().users.users.filter(u => u.deposit.mode === '押金');
      const by = k => us.filter(u => u.deposit.status === k).length;
      const income = db().payments.payments.filter(p => p.account === '骑行收入户' && p.status === '成功').reduce((s, p) => s + C(p.amount) * (p.direction === '收' ? 1 : -1), 0);
      return { ledger: L, users: { 已缴: by('已缴'), 押金不足: by('押金不足'), 退押中: by('退押中'), 已退: by('已退') }, userBalanceSum: Y(us.reduce((s, u) => s + C(u.deposit.balance), 0)), rideIncomeNet: Y(income), note: '押金专户与骑行收入户分开记账；押金只抵扣本人未付骑行费，不用于支付服务费' };
    }),
    flows: (f = {}) => g.call('finance.deposit.flows', 'deposit.view', () => {
      const list = db().payments.payments.filter(p => p.account === '押金专户' && (!f.type || p.type === f.type) && (!f.status || p.status === f.status) && (!f.userId || p.userId === f.userId) && g.inRange(p.createdAt, f.from, f.to));
      const pg = paginate([...list].sort((a, b) => ms(b.createdAt) - ms(a.createdAt)), f);
      return { ...pg, rows: pg.rows.map(p => ({ ...clone(p), userName: p.userId ? D.user(p.userId).name : null, userPhoneMasked: p.userId ? D.user(p.userId).phoneMasked : null })) };
    }),
  };

  /* ================= 欠费清单、退款失败与人工转账（R-09、R-11） ================= */
  const arrears = {
    list: (f = {}) => g.call('finance.arrears.list', ['arrears.view', 'user.view'], () => {
      const rows = db().users.users.filter(u => C(u.arrears) > 0).map(u => {
        const os = D.arrearsOrders(u.userId);
        const oldest = os.map(o => o.billPushedAt).filter(Boolean).sort()[0] || null;
        return { userId: u.userId, name: u.name, phoneMasked: u.phoneMasked, channel: u.channel, arrears: u.arrears, orderCount: os.length, orderIds: os.map(o => o.orderId), oldestAt: oldest, days: oldest ? Math.floor((ctx.nowMs() - ms(oldest)) / 864e5) : 0, accountStatus: u.account.status, degradedOrders: os.filter(o => o.degraded).length };
      });
      const pg = paginate(rows.sort((a, b) => b.days - a.days), f);
      return { ...pg, totals: { users: rows.length, amount: Y(rows.reduce((s, r) => s + C(r.arrears), 0)) } };
    }),
  };
  const refunds = {
    failures: () => g.call('finance.refunds.failures', ['refund.manualTransfer', 'deposit.view'], () => refundFailures().map(p => {
      const u = D.user(p.userId);
      const tk = p.ticketId ? D.ticket(p.ticketId) : null;
      return { paymentId: p.paymentId, type: p.type, userId: p.userId, userName: u.name, userPhoneMasked: u.phoneMasked, orderId: p.orderId, amount: p.amount, failReason: p.failReason, createdAt: p.createdAt, ticketId: p.ticketId, payeeSubmitted: p.payeeSubmitted || null, status: p.payeeSubmitted ? '待转账' : '待用户填写收款账户', ticketStatus: tk ? tk.status : null };
    })),
    /** 录入人工转账结果。payee 缺省时用用户已提交的收款账户 */
    transfer: (paymentId, { payee, tradeNo } = {}, reason) => g.call('finance.refunds.transfer', 'refund.manualTransfer', a => {
      const why = ctx.reason(reason);
      const p = ctx.get(db().payments.payments, 'paymentId', paymentId, '流水');
      if (!refundFailures().some(x => x.paymentId === paymentId)) throw new ApiError('CONFLICT', '该笔不是待人工转账的退款');
      const pe = payee || p.payeeSubmitted;
      if (!pe || !pe.name || !pe.bank || !pe.account) throw new ApiError('INVALID', '用户尚未提供本人实名收款账户');
      const isDep = p.type === '退押';
      const t = ctx.now();
      const m = D.pay({ type: isDep ? '人工转账退押' : '人工转账退款', account: isDep ? '押金专户' : '骑行收入户', direction: '付', userId: p.userId, orderId: p.orderId, amount: p.amount, channel: '银行转账', relatedPaymentId: p.paymentId, ticketId: p.ticketId, operatorAccountId: a.accountId, payee: { name: pe.name, bank: pe.bank, account: pe.account }, noInject: true });
      if (tradeNo) m.channelTradeNo = String(tradeNo);
      const u = D.user(p.userId);
      if (isDep) {
        const dep = u.deposit.mode === '押金' ? u.deposit : u.deposit.previous;
        if (dep) { dep.balance = 0; dep.status = '已退'; if (dep.refund) Object.assign(dep.refund, { manualPaymentId: m.paymentId, awaitingPayee: false }); }
      } else {
        const o = D.order(p.orderId);
        o.fee.refunded = Y(C(o.fee.refunded) + C(p.amount)); R.settleFee(o.fee);
        o.refundStatus = C(o.fee.refunded) >= C(o.fee.channelPaid) + C(o.fee.depositDeducted) ? '全额退款' : '部分退款';
        const tk = p.ticketId ? D.ticket(p.ticketId) : null;
        if (tk) { tk.awaitingPayee = false; tk.actions.push({ type: '人工转账', paymentId: m.paymentId, at: t, by: a.accountId }); tk.messages.push({ from: '客服', at: t, text: `已通过银行转账退回 ¥${p.amount.toFixed(1)}，请留意到账。` }); tk.status = '已解决'; tk.closedAt = t; ctx.dirty('tickets'); }
        ctx.dirty('orders');
      }
      ctx.audit({ action: '人工转账', targetType: 'payment', targetId: m.paymentId, summary: `原路退回失败（${paymentId}），人工转账 ¥${p.amount.toFixed(1)} 至 ${pe.bank} ${pe.account}`, reason: why });
      D.notify(u.userId, isDep ? '押金' : '退款', isDep ? '押金已通过银行转账退回' : '退款已通过银行转账退回', `¥${p.amount.toFixed(1)} 已转入你提供的 ${pe.bank} ${pe.account}。`, p.orderId);
      ctx.dirty('users', 'payments');
      return clone(m);
    }, { write: true }),
  };

  return { settlements, disputes, recon, deposit, arrears, refunds };
}

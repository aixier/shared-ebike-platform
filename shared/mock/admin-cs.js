// 平台公司客服：用户与订单查询、工单、退款（智能体提议 + 客服确认）、强制结束、人工解冻、协助退押与注销
// （R-10、R-11、R-12、R-13、R-14、R-17、R-48；BR-06）。
import { ApiError } from './errors.js';
import { C, Y, ms, addSec, paginate, clone, inPolygon, centroid } from './util.js';
import * as R from './rules.js';

export function createCsApi(ctx, D, V, A, g) {
  const db = () => ctx.db;
  const userView = u => ({ ...V.user(u), phone: u.phone, persona: undefined, orderCount: D.userOrders(u.userId).length, ticketCount: db().tickets.filter(t => t.userId === u.userId).length });
  const tView = t => ({ ...clone(t), userName: (D.user(t.userId) || {}).name, userPhoneMasked: (D.user(t.userId) || {}).phoneMasked, assigneeName: D.actorName(t.assignee), refundLimit: D.params().csRefundLimit, order: t.orderId ? V.adminOrder(D.order(t.orderId)) : null });
  const getT = id => ctx.get(db().tickets, 'ticketId', id, '工单');

  /** 执行一笔原路退款（BR-06）。注入 refund.fail 时原路失败 → 待用户提供收款账户（R-11） */
  function executeRefund(tk, amount, acc, overLimit) {
    const o = D.order(tk.orderId);
    if (!o || !o.fee) throw new ApiError('INVALID', '该工单没有可退款的订单');
    const refundable = C(o.fee.channelPaid) - C(o.fee.refunded);
    const amt = C(amount);
    if (amt <= 0) throw new ApiError('INVALID', '退款金额须大于 0');
    if (amt > refundable) throw new ApiError('INVALID', `超过可原路退回的金额 ¥${Y(refundable).toFixed(1)}`);
    const orig = db().payments.payments.filter(p => p.orderId === o.orderId && ['订单扣款', '订单支付', '欠费补付'].includes(p.type) && p.status === '成功').pop();
    const fail = ctx.takeInjection('refund.fail');
    const p = D.pay({ type: '退款', direction: '付', userId: o.userId, orderId: o.orderId, amount: Y(amt), channel: orig.channel, relatedPaymentId: orig.paymentId, ticketId: tk.ticketId, operatorAccountId: acc.accountId, status: fail ? '失败' : '成功', failReason: fail ? '原支付账户已注销，渠道拒绝原路退回' : null, noInject: true });
    const t = ctx.now();
    tk.actions.push({ type: overLimit ? '超额退款确认' : '退款', paymentId: p.paymentId, at: t, by: acc.accountId });
    if (fail) {
      o.refundStatus = '原路失败·待人工转账'; o.refundFailedPaymentId = p.paymentId;
      tk.awaitingPayee = true; tk.status = '处理中';
      tk.messages.push({ from: '客服', at: t, text: `退款 ¥${Y(amt).toFixed(1)} 原路退回没有成功（原支付账户已注销）。请在下方填写本人实名的银行收款账户，平台公司将人工转账。` });
      D.notify(o.userId, '退款', '退款原路退回没有成功', '请在客服对话里填写本人实名收款账户，平台公司将人工转账。', o.orderId);
    } else {
      o.fee.refunded = Y(C(o.fee.refunded) + amt); R.settleFee(o.fee);
      o.refundStatus = C(o.fee.refunded) >= C(o.fee.channelPaid) + C(o.fee.depositDeducted) ? '全额退款' : '部分退款';
      tk.status = '已解决'; tk.closedAt = t;
      tk.messages.push({ from: '客服', at: t, text: `已原路退回 ¥${Y(amt).toFixed(1)}，一般 1～3 天到账。` });
      D.notify(o.userId, '退款', '退款已原路退回', `订单 ${o.orderId} 退款 ¥${Y(amt).toFixed(1)} 已原路退回。`, o.orderId);
    }
    ctx.dirty('orders', 'tickets');
    ctx.emit('order.updated', { orderId: o.orderId, userId: o.userId, operatorId: o.operatorId });
    return p;
  }

  return {
    /** 按手机号、用户编号、订单号、车号查询 */
    search: q => g.call('cs.search', 'user.view', () => {
      const s = String(q || '').trim();
      if (!s) throw new ApiError('INVALID', '请输入手机号、订单号或用户编号');
      const users = db().users.users.filter(u => u.phone === s || u.userId === s || u.phoneMasked === s || (s.length >= 4 && /^\d+$/.test(s) && u.phone.endsWith(s)));
      const orders = db().orders.filter(o => o.orderId === s || (D.vehicle(o.vehicleId) || {}).plateNo === s.toUpperCase()).slice(0, 20);
      for (const o of orders) if (!users.some(u => u.userId === o.userId)) users.push(D.user(o.userId));
      return { users: users.slice(0, 20).map(userView), orders: orders.map(V.adminOrder) };
    }),
    /** 冻结账户列表。kind：欠费（结清后自动解冻）| 核验（实名核验未通过或逾期）| 其他（需人工解冻）；q 按手机号尾号、用户编号 */
    listFrozenAccounts: (f = {}) => g.call('cs.listFrozenAccounts', ['account.unfreeze', 'user.view'], () => {
      const kindOf = r => (/欠费/.test(r) ? '欠费' : /核验/.test(r) ? '核验' : '其他');
      const all = db().users.users.filter(u => u.account.status === '冻结').map(u => {
        const fz = u.account.freeze || {};
        const kind = kindOf(fz.reason || '');
        return { userId: u.userId, name: u.name, phoneMasked: u.phoneMasked, channel: u.channel, freeze: clone(fz), kind, autoUnfreeze: kind !== '其他', arrears: u.arrears, realNameStatus: u.realName.status, frozenDays: fz.at ? Math.floor((ctx.nowMs() - ms(fz.at)) / 864e5) : null };
      });
      const rows = all.filter(r => (!f.kind || r.kind === f.kind) && (!f.q || r.userId === f.q || r.phoneMasked.endsWith(String(f.q).slice(-4)) || db().users.users.find(u => u.userId === r.userId).phone.endsWith(f.q)));
      const pg = paginate(rows.sort((a, b) => ms(b.freeze.at) - ms(a.freeze.at)), f);
      return { ...pg, counts: { 欠费: all.filter(r => r.kind === '欠费').length, 核验: all.filter(r => r.kind === '核验').length, 其他: all.filter(r => r.kind === '其他').length } };
    }),
    user: userId => g.call('cs.user', 'user.view', () => {
      const u = ctx.get(db().users.users, 'userId', userId, '用户');
      return { ...userView(u), deposit: clone(u.deposit), cancelBlockers: D.cancelBlockers(u), refundBlockers: D.refundBlockers(u), orders: D.userOrders(userId).sort((a, b) => ms(b.requestAt) - ms(a.requestAt)).slice(0, 50).map(V.adminOrder), tickets: db().tickets.filter(t => t.userId === userId).map(tView), monthCards: db().users.monthCards.filter(c => c.userId === userId), coupons: db().users.coupons.filter(c => c.userId === userId), payments: db().payments.payments.filter(p => p.userId === userId).sort((a, b) => ms(b.createdAt) - ms(a.createdAt)).slice(0, 50) };
    }),
    tickets: {
      list: (f = {}) => g.call('cs.tickets.list', 'order.view', () => {
        const list = db().tickets.filter(t => (!f.status || [].concat(f.status).includes(t.status)) && (!f.category || t.category === f.category) && (!f.userId || t.userId === f.userId) && (!f.assignee || t.assignee === f.assignee) && (!f.q || [t.ticketId, t.orderId, t.userId].includes(f.q)));
        const pg = paginate([...list].sort((a, b) => ms(b.createdAt) - ms(a.createdAt)), f);
        return { ...pg, rows: pg.rows.map(tView) };
      }),
      get: id => g.call('cs.tickets.get', 'order.view', () => tView(getT(id))),
      /** 客服回复用户；第一次回复即接手工单 */
      reply: (id, text) => g.call('cs.tickets.reply', 'order.view', a => {
        const tk = getT(id);
        if (!String(text || '').trim()) throw new ApiError('INVALID', '请输入回复内容');
        tk.messages.push({ from: '客服', at: ctx.now(), text: String(text).trim() });
        if (tk.status === '待处理') tk.status = '处理中';
        tk.assignee = tk.assignee || a.accountId;
        ctx.dirty('tickets'); ctx.emit('ticket.changed', { ticketId: id, userId: tk.userId });
        return tView(tk);
      }, { write: true }),
      close: (id, reason) => g.call('cs.tickets.close', 'order.view', a => {
        const why = ctx.reason(reason);
        const tk = getT(id);
        if (tk.status === '已解决') throw new ApiError('ALREADY_DONE', '工单已解决');
        if (tk.status === '待主管确认') throw new ApiError('CONFLICT', '该工单的退款待客服主管确认');
        Object.assign(tk, { status: '已解决', closedAt: ctx.now(), assignee: tk.assignee || a.accountId });
        tk.messages.push({ from: '客服', at: ctx.now(), text: why });
        ctx.dirty('tickets'); ctx.emit('ticket.changed', { ticketId: id, userId: tk.userId });
        return tView(tk);
      }, { write: true }),
    },
    refund: {
      /**
       * 确认退款：按智能体提议（或客服改后的金额）原路退回。
       * 超出客服授权额度（平台参数 csRefundLimit）时，非主管账号转为"待主管确认"，返回 {pendingSupervisor:true}
       */
      confirm: (ticketId, { amount } = {}, reason) => g.call('cs.refund.confirm', 'refund.confirm', a => {
        const why = ctx.reason(reason);
        const tk = getT(ticketId);
        if (tk.status === '已解决') throw new ApiError('ALREADY_DONE', '工单已解决');
        const prop = tk.refundProposal;
        const amt = amount != null ? Number(amount) : prop ? prop.amount : NaN;
        if (!(amt > 0)) throw new ApiError('INVALID', '请填写退款金额');
        const over = C(amt) > C(D.params().csRefundLimit);
        tk.assignee = tk.assignee || a.accountId;
        tk.refundProposal = { ...(prop || { kind: '部分退款', basis: why, proposedAt: ctx.now(), proposedBy: a.accountId }), amount: Y(C(amt)), overLimit: over };
        if (over && !g.can(a, 'refund.confirm.overLimit')) {
          Object.assign(tk.refundProposal, { decision: '待主管确认', decidedBy: null, decidedAt: null });
          tk.status = '待主管确认';
          tk.messages.push({ from: '客服', at: ctx.now(), text: `退款 ¥${Y(C(amt)).toFixed(1)} 超过客服授权额度，已提交客服主管确认，确认后原路退回，会在这里通知你。` });
          ctx.audit({ action: '确认退款', targetType: 'order', targetId: tk.orderId, summary: `工单 ${ticketId} 退款 ¥${Y(C(amt)).toFixed(1)} 超出授权额度，提交客服主管确认`, reason: why });
          ctx.dirty('tickets'); ctx.emit('ticket.changed', { ticketId, userId: tk.userId });
          return { pendingSupervisor: true, ticket: tView(tk) };
        }
        Object.assign(tk.refundProposal, { decision: '已确认', decidedBy: a.accountId, decidedAt: ctx.now() });
        const p = executeRefund(tk, amt, a, over);
        ctx.audit({ action: over ? '确认超额退款' : '确认退款', targetType: 'order', targetId: tk.orderId, summary: `工单 ${ticketId} 退款 ¥${Y(C(amt)).toFixed(1)}${p.status === '成功' ? '，已原路退回' : '，原路退回失败，待用户提供收款账户'}`, reason: why });
        ctx.emit('ticket.changed', { ticketId, userId: tk.userId });
        return { pendingSupervisor: false, payment: clone(p), ticket: tView(tk) };
      }, { write: true }),
      /** 客服主管确认超额退款 */
      approveOverLimit: (ticketId, reason) => g.call('cs.refund.approveOverLimit', 'refund.confirm.overLimit', a => {
        const why = ctx.reason(reason);
        const tk = getT(ticketId);
        if (tk.status !== '待主管确认' || !tk.refundProposal) throw new ApiError('CONFLICT', '该工单没有待主管确认的退款');
        Object.assign(tk.refundProposal, { decision: '已确认', decidedBy: a.accountId, decidedAt: ctx.now() });
        const p = executeRefund(tk, tk.refundProposal.amount, a, true);
        ctx.audit({ action: '确认超额退款', targetType: 'order', targetId: tk.orderId, summary: `客服主管确认工单 ${ticketId} 超额退款 ¥${tk.refundProposal.amount.toFixed(1)}`, reason: why });
        ctx.emit('ticket.changed', { ticketId, userId: tk.userId });
        return { payment: clone(p), ticket: tView(tk) };
      }, { write: true }),
      reject: (ticketId, reason) => g.call('cs.refund.reject', 'refund.confirm', a => {
        const why = ctx.reason(reason);
        const tk = getT(ticketId);
        if (!tk.refundProposal) throw new ApiError('CONFLICT', '该工单没有退款提议');
        if (tk.status === '待主管确认' && !g.can(a, 'refund.confirm.overLimit')) throw new ApiError('OVER_LIMIT_NEEDS_SUPERVISOR', '该退款待客服主管处理');
        Object.assign(tk.refundProposal, { decision: '已驳回', decidedBy: a.accountId, decidedAt: ctx.now() });
        tk.status = '已解决'; tk.closedAt = ctx.now();
        tk.messages.push({ from: '客服', at: ctx.now(), text: why });
        ctx.audit({ action: '驳回退款', targetType: 'order', targetId: tk.orderId, summary: `驳回工单 ${ticketId} 的退款提议`, reason: why });
        ctx.dirty('tickets'); ctx.emit('ticket.changed', { ticketId, userId: tk.userId });
        return tView(tk);
      }, { write: true }),
    },
    /** 强制结束订单并锁车（R-13）：按 billedEndAt 计费；须写事由 */
    forceEnd: (orderId, { billedEndAt, stationId, ticketId } = {}, reason) => g.call('cs.forceEnd', 'order.forceEnd', a => {
      const why = ctx.reason(reason);
      const o = ctx.get(db().orders, 'orderId', orderId, '订单');
      if (o.status !== '骑行中') throw new ApiError('NOT_RIDING');
      const tk = ticketId ? getT(ticketId) : db().tickets.find(t => t.orderId === orderId && t.status !== '已解决') || null;
      const end = billedEndAt || (tk && tk.forceEndProposal && tk.forceEndProposal.billedEndAt) || ctx.now();
      if (ms(end) < ms(o.startAt) || ms(end) > ctx.nowMs() + 1000) throw new ApiError('INVALID', '计费终点须在开锁之后、现在之前');
      const sid = stationId !== undefined ? stationId : (tk && tk.forceEndProposal ? tk.forceEndProposal.stationId : o.claimedStationId);
      const v = D.vehicle(o.vehicleId);
      const st = sid ? D.station(sid) : null;
      const loc = st ? (inPolygon(st.center, st.fence) ? st.center : centroid(st.fence)) : v.location;
      const r = R.decideReturn({ stations: db().stations.stations, piles: db().stations.piles, location: loc, method: 'lock' });
      const t = ctx.now();
      o.forceEnd = { at: t, billedEndAt: end, by: a.accountId, reason: why, ticketId: tk ? tk.ticketId : null };
      D.finishRide(o, { endAt: end, lockReportAt: t, reportReceivedAt: t, endMethod: '客服强制结束', location: loc, stationId: r.station ? r.station.stationId : null, inStation: r.inStation, basis: r.basis, distanceToStationM: r.distanceToStationM, billAt: t });
      if (tk) {
        tk.forceEnd = { at: t, by: a.accountId, reason: why, billedEndAt: end };
        tk.actions.push({ type: '强制结束', at: t, by: a.accountId });
        tk.messages.push({ from: '客服', at: t, text: `已为你结束订单并锁车，按 ${end.slice(11, 16)} 还车计费，共 ${o.billedMinutes} 分钟${o.fee ? `，应付 ¥${o.fee.payable.toFixed(1)}` : ''}。` });
        tk.status = '已解决'; tk.closedAt = t; tk.assignee = tk.assignee || a.accountId;
        ctx.dirty('tickets');
      }
      ctx.audit({ action: '强制结束订单', targetType: 'order', targetId: orderId, summary: `强制结束订单 ${orderId} 并锁车，按 ${end.slice(0, 16)} 计费 ${o.billedMinutes} 分钟`, reason: why });
      return V.adminOrder(o);
    }, { write: true }),
    /** 人工解冻（R-14 中自动解冻以外的情形） */
    unfreeze: (userId, reason) => g.call('cs.unfreeze', 'account.unfreeze', a => {
      const why = ctx.reason(reason);
      const u = ctx.get(db().users.users, 'userId', userId, '用户');
      if (u.account.status !== '冻结') throw new ApiError('CONFLICT', '该账户未冻结');
      const before = clone(u.account.freeze);
      u.account.status = '正常';
      u.account.history.push({ action: '人工解冻', at: ctx.now(), reason: why, by: a.accountId });
      u.account.freeze = null;
      ctx.audit({ action: '解冻账户', targetType: 'user', targetId: userId, summary: `人工解冻账户 ${userId}（原因：${before.reason}）`, reason: why, before, after: null });
      D.notify(userId, '账户', '账户已解冻', '客服已为你解冻账户，可以继续骑行。');
      ctx.dirty('users');
      return userView(u);
    }, { write: true }),
    /** 协助退押（R-17） */
    assistRefundDeposit: (userId, reason) => g.call('cs.assistRefundDeposit', 'deposit.assist', a => {
      ctx.reason(reason);
      const u = ctx.get(db().users.users, 'userId', userId, '用户');
      return D.requestDepositRefund(u, a.accountId);
    }, { write: true }),
    /** 协助注销（R-48）：有欠费、骑行中或未退押金时返回 CANCEL_BLOCKED 与待处理事项 */
    assistCancel: (userId, reason) => g.call('cs.assistCancel', 'account.cancel.assist', a => {
      const why = ctx.reason(reason);
      const u = ctx.get(db().users.users, 'userId', userId, '用户');
      if (u.account.status === '已注销') throw new ApiError('ALREADY_DONE', '该账户已注销');
      D.cancelAccount(u, why, a.accountId);
      return userView(u);
    }, { write: true }),
  };
}

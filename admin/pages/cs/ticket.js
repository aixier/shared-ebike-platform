// 工单处理抽屉：客服智能体依订单事实提议，客服确认、修改或驳回；超额转主管；原路失败转财务。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $, $$, el, chip, dash, fmtDt, billHtml, failToast, errText, myPerms, waitText, minutesSince, openOrder, openUser } from './lib.js';

const FROM = { 用户: ['用户', ''], 客服智能体: ['客服智能体', 'is-agent'], 客服: ['客服', 'is-staff'] };
const QUICK = ['已收到，正在核实订单，稍后给你答复', '已核实订单事实，处理结果会在这里通知你', '请补充发生问题的大致时间和地点'];

export const money1 = v => Number(v).toFixed(1);

export function chatHtml(messages) {
  return `<div class="cs-chat">${messages.map(m => {
    const [name, cls] = FROM[m.from] || [m.from, ''];
    return `<div class="cs-msg ${cls}"><div class="m"><b>${esc(name)}</b><span>${ui.datetime(m.at, true)}</span></div>${esc(m.text)}</div>`;
  }).join('')}</div>`;
}

function factsHtml(t, o) {
  const f = t.facts;
  if (!o) return '<div class="cs-hint">此工单不涉及具体订单，请在「账户处理」中办理。</div>';
  return `<div class="cs-box"><div class="cs-kv">
      <div><div class="k">订单</div><div class="v"><button type="button" class="cs-link" data-order="${esc(o.orderId)}">${esc(o.orderId)}</button> ${chip(o.status)}</div></div>
      <div><div class="k">车辆</div><div class="v">${esc(o.plateNo)} · ${esc(o.brandName)}</div></div>
      <div><div class="k">开锁 / 结束</div><div class="v">${fmtDt(o.startAt)} → ${fmtDt(o.endAt)}</div></div>
      <div><div class="k">计费</div><div class="v">${o.billedMinutes != null ? `${o.billedMinutes} 分钟 · ${ui.km(o.distanceKm || 0)}` : '—'}</div></div>
      <div class="full"><div class="k">在点依据</div><div class="v">${dash(f && f.inStationBasis || o.inStationBasis)}${o.inStation === false ? '（站外，收调度费）' : ''}</div></div>
    </div>
    <div style="margin-top:8px">${billHtml(o.bill)}</div></div>`;
}

/** 打开工单处理抽屉。opts.onChange：处理后让外层列表刷新 */
export function openTicket(ticketId, opts = {}) {
  const dr = ui.drawer({ title: `工单 ${ticketId}`, sub: '处理依据来自订单事实', body: ui.skeleton('lines', 10), wide: true, footer: [], onClose: () => opts.onChange && opts.onChange() });
  const foot = $('.a-drawer-foot', dr.el);
  let perms = null;

  async function render() {
    foot.innerHTML = '';
    dr.setBody(ui.skeleton('lines', 10));
    let t, o = null;
    try {
      perms = perms || await myPerms();
      t = await api.admin.cs.tickets.get(ticketId);
      o = t.orderId ? await api.admin.orders.get(t.orderId) : null;
    } catch (e) {
      dr.setBody(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: render }));
      return;
    }
    const p = t.refundProposal;
    const refundable = o && o.fee ? Math.max(0, Math.round((o.fee.channelPaid - o.fee.refunded) * 10) / 10) : 0;
    const limit = t.refundLimit;
    const canConfirm = perms.has('refund.confirm');
    const isSup = perms.has('refund.confirm.overLimit');
    const body = el('<div style="display:contents"></div>');
    const waiting = t.status === '已解决' ? '' : `已等待 ${waitText(minutesSince(t.createdAt))}`;
    let proposalHtml = '';
    if (p && p.decision === '已确认' || (p && p.decision === '已驳回')) {
      const ok = p.decision === '已确认';
      proposalHtml = `<div class="cs-agent"><div class="who">客服智能体提议 → ${ok ? '已确认' : '已驳回'}</div>
        <div class="say">${p.kind}：<b>${ui.money(p.amount)}</b>。${esc(p.basis)}</div>
        <div class="cs-hint">${ok ? '确认' : '驳回'}：${fmtDt(p.decidedAt)}${p.overLimit ? ' · 超额退款，经客服主管确认' : ''}。用户端已同步收到结果。</div></div>`;
      if (t.awaitingPayee) proposalHtml += `<div class="a-alert"><span class="grow"><b>原路退回没有成功，已转财务。</b>已通知用户在客服对话里填写本人实名收款账户，用户提交后由财务在「欠费与退款失败」页人工转账。</span></div>`;
    } else if (p) {
      const pending = p.decision === '待主管确认';
      proposalHtml = `<div class="cs-agent"><div class="who">客服智能体提议${pending ? ' · 等待客服主管确认' : ' · 等你确认'}</div>
        <div class="say"><span class="big num">${ui.money(p.amount)}</span> <span class="chip brand">${esc(p.kind)}</span></div>
        <div class="say">${esc(p.basis)}</div>
        <details class="cs-basis"><summary>依据</summary><ul>
          <li>订单 ${esc(t.orderId)}：计费 ${t.facts.billedMinutes} 分钟，应付 ${ui.money(t.facts.payable)}，实收 ${ui.money(t.facts.netReceived)}</li>
          <li>在点依据：${esc(t.facts.inStationBasis || '—')}</li>
          <li>可原路退回上限 ${ui.money(refundable)}（渠道已收 − 已退款，押金抵扣部分不在线退）</li>
          <li>客服授权额度 ${ui.money(limit)}${p.amount > limit ? '，本次提议超出，需客服主管确认' : '，本次提议在额度内'}</li>
          <li>提议由客服智能体生成：${fmtDt(p.proposedAt)}，规则版本 ${esc(o ? o.pricingVersionId : '—')}</li></ul></details></div>`;
      if (!pending) {
        proposalHtml += `<form class="a-form" id="cs-refund-form" novalidate><div class="a-item"><label class="req" for="cs-amt">退款金额（元）</label>
          <div class="a-input-group"><input id="cs-amt" name="amount" class="a-input" value="${money1(p.amount)}" inputmode="decimal" style="max-width:140px"><span class="unit">不超过 ${ui.money(refundable)}</span></div>
          <span class="tip" id="cs-amt-tip"></span></div></form>`;
      }
    } else if (o && o.fee && refundable > 0 && t.status !== '已解决') {
      proposalHtml = `<div class="cs-agent"><div class="who">客服智能体判断</div><div class="say">订单事实与用户描述一致，暂不建议退款。如你核实后确需退款，可展开下方人工发起。</div>
        <details class="cs-basis" id="cs-manual"><summary>人工发起退款</summary>
        <form class="a-form" id="cs-refund-form" novalidate style="margin-top:8px"><div class="a-item"><label class="req" for="cs-amt">退款金额（元）</label><div class="a-input-group"><input id="cs-amt" name="amount" class="a-input" inputmode="decimal" style="max-width:140px"><span class="unit">不超过 ${ui.money(refundable)}</span></div><span class="tip" id="cs-amt-tip"></span></div></form></details></div>`;
    } else if (!o) {
      proposalHtml = '';
    }
    body.innerHTML = `
      <div class="cs-row">${chip(t.status)} <span class="chip mute">${esc(t.category)}</span><span class="cs-muted">${esc(t.userName || '')} ${esc(t.userPhoneMasked || '')} · ${esc(t.entry)}入口 · ${fmtDt(t.createdAt)}${waiting ? ` · ${waiting}` : ''}${t.assigneeName ? ` · 处理人 ${esc(t.assigneeName)}` : ''}</span>
        <button type="button" class="cs-link cs-right" data-user="${esc(t.userId)}">查看用户</button></div>
      ${proposalHtml ? `<div class="cs-sec"><h3>退款</h3>${proposalHtml}</div>` : ''}
      <div class="cs-sec"><h3>订单事实</h3>${factsHtml(t, o)}</div>
      <div class="cs-sec"><h3>对话</h3>${chatHtml(t.messages)}</div>
      ${t.status !== '已解决' ? `<div class="cs-sec"><h3>回复用户</h3><div class="cs-quick">${QUICK.map(q => `<button type="button" class="a-btn a-btn-default is-sm" data-q="${esc(q)}">${esc(q.slice(0, 10))}…</button>`).join('')}</div>
        <textarea id="cs-reply" class="a-textarea cs-textarea" placeholder="直接输入要发给用户的话，用户在小程序客服对话里看到"></textarea>
        <div><button type="button" class="a-btn a-btn-default" id="cs-send">发送回复</button></div></div>` : ''}`;
    dr.setBody(body);
    $$('[data-order]', body).forEach(b => b.addEventListener('click', () => openOrder(b.dataset.order)));
    $$('[data-user]', body).forEach(b => b.addEventListener('click', () => openUser(b.dataset.user, { openTicket: id => openTicket(id, opts) })));
    $$('[data-q]', body).forEach(b => b.addEventListener('click', () => { const ta = $('#cs-reply', body); ta.value = b.dataset.q; ta.focus(); }));

    const tip = $('#cs-amt-tip', body), amtEl = $('#cs-amt', body);
    const readAmt = () => {
      const v = amtEl ? amtEl.value.trim() : '';
      if (v === '') return { err: '请填写退款金额' };
      const n = Number(v);
      if (!isFinite(n) || n <= 0) return { err: '退款金额须是大于 0 的数字' };
      if (Math.abs(n * 10 - Math.round(n * 10)) > 1e-6) return { err: '金额最多一位小数' };
      if (n > refundable + 1e-6) return { err: `超过可原路退回的 ${ui.money(refundable)}` };
      return { n };
    };
    const hint = () => {
      if (!amtEl) return;
      const r = readAmt();
      const item = amtEl.closest('.a-item');
      if (r.err) { tip.textContent = ''; return; }
      item.classList.remove('is-error'); $('.err-msg', item) && $('.err-msg', item).remove();
      tip.textContent = r.n > limit
        ? (isSup ? `超出客服授权额度 ${ui.money(limit)}，你是客服主管，可直接确认` : `超出你的授权额度 ${ui.money(limit)}，确认后将提交客服主管`)
        : `在授权额度 ${ui.money(limit)} 内，确认后立即原路退回`;
    };
    amtEl && amtEl.addEventListener('input', hint);
    hint();

    // ---- 底部操作 ----
    const add = (label, kind, fn, why) => {
      const b = el(`<button type="button" class="a-btn a-btn-${kind}">${esc(label)}</button>`);
      if (why) ui.setDisabled(b, why[0], why[1]);
      b.addEventListener('click', () => fn(b));
      foot.append(b); return b;
    };
    const resolved = t.status === '已解决';
    if (!resolved && p && p.decision !== '已确认' && p.decision !== '已驳回') {
      if (p.decision === '待主管确认') {
        add('驳回提议', 'default', () => reject(), isSup ? null : ['该退款待客服主管处理', '请等主管确认，或提醒主管处理']);
        add(`主管确认退款 ${ui.money(p.amount)}`, 'primary', () => approve(), isSup ? null : ['只有客服主管能确认超额退款', '已提交主管，确认后会在对话里通知用户']);
      } else {
        add('驳回提议', 'default', () => reject(), canConfirm ? null : ['你的账号没有退款确认权限', '请换客服岗位处理']);
        add('确认退款', 'primary', () => confirmRefund(), canConfirm ? null : ['你的账号没有退款确认权限', '请换客服岗位处理']);
      }
    } else if (!resolved && !p && amtEl && refundable > 0) {
      add('人工发起退款', 'default', () => confirmRefund(), canConfirm ? null : ['你的账号没有退款确认权限', '请换客服岗位处理']);
    }
    if (!resolved && o && o.status === '骑行中') {
      add('去强制结束', 'primary', () => { dr.close(); location.hash = `#/cs/force-end?order=${encodeURIComponent(o.orderId)}`; }, perms && !perms.has('order.forceEnd') ? ['你的账号没有强制结束订单的权限', '请换客服岗位处理'] : null);
    }
    if (!resolved && t.status !== '待主管确认') add('结案', 'default', () => closeTicket());
    if (!foot.children.length) foot.append(el('<span class="cs-muted">工单已办结，没有待办操作</span>'));
    const send = $('#cs-send', body);
    send && send.addEventListener('click', async () => {
      const ta = $('#cs-reply', body);
      const text = ta.value.trim();
      const item = ta.parentElement;
      if (!text) { ta.focus(); ui.toast('请先输入回复内容', { type: 'warn', hint: '写一句话再发送' }); return; }
      send.setAttribute('aria-busy', 'true'); send.innerHTML = '<span class="a-spin"></span>发送中';
      try { await api.admin.cs.tickets.reply(ticketId, text); ui.toast('已发送，用户端同步可见', { type: 'ok' }); await render(); }
      catch (e) { send.removeAttribute('aria-busy'); send.textContent = '发送回复'; failToast(e); }
      void item;
    });

    // ---- 动作 ----
    async function confirmRefund() {
      const r = readAmt();
      if (r.err) {
        const det = amtEl.closest('details'); if (det) det.open = true;
        const item = amtEl.closest('.a-item');
        item.classList.add('is-error');
        $('.err-msg', item) || item.append(el(`<span class="err-msg" role="alert">${esc(r.err)}</span>`));
        $('.err-msg', item).textContent = r.err;
        amtEl.focus(); return;
      }
      const over = r.n > limit;
      let res;
      const ans = await ui.confirm({
        title: over && !isSup ? '提交客服主管确认' : '确认退款',
        danger: true,
        confirmText: over && !isSup ? '提交主管确认' : '确认退款',
        message: `<p>订单 <b>${esc(t.orderId)}</b>，退款 <b>${ui.money(r.n)}</b>。</p><div class="cs-script">用户将收到：<b>已原路退回 ${ui.money(r.n)}，一般 1～3 天到账。</b></div>`
          + (over && !isSup ? `<div class="a-alert">超出你的授权额度 ${ui.money(limit)}，提交后由客服主管确认，确认前不会退款。</div>` : ''),
        impact: over && !isSup ? `提交后工单转「待主管确认」，用户端看到“已提交主管确认”` : `原路退回 ${ui.money(r.n)} 至用户原支付账户；订单实收同步减少`,
        reasons: ['用户已按事实核实，按提议退款', '锁车报告延迟，按停止移动时刻重算', '多扣费用，核实后退回'],
        onConfirm: async reason => { res = await api.admin.cs.refund.confirm(ticketId, { amount: r.n }, reason); },
      });
      if (!ans) return;
      if (res.pendingSupervisor) ui.toast('已提交客服主管确认', { type: 'ok', hint: '确认后原路退回，用户端会同步看到进度' });
      else if (res.payment && res.payment.status === '失败') ui.toast('原路退回没有成功，已转财务', { type: 'warn', hint: '已通知用户填写本人收款账户，财务人工转账' });
      else ui.toast(`已原路退回 ${ui.money(r.n)}`, { type: 'ok', hint: '用户端已同步收到结果' });
      await render();
    }
    async function approve() {
      const ans = await ui.confirm({
        title: '确认超额退款', danger: true, confirmText: '确认退款',
        message: `<p>客服 ${esc(t.assigneeName || '—')} 提交的退款 <b>${ui.money(p.amount)}</b> 超过授权额度 ${ui.money(limit)}。</p><div class="cs-script">依据：${esc(p.basis)}</div>`,
        impact: `原路退回 ${ui.money(p.amount)} 至用户原支付账户`,
        reasons: ['核实订单事实，同意按提议退款', '锁车报告延迟，同意重算'],
        onConfirm: async reason => { await api.admin.cs.refund.approveOverLimit(ticketId, reason); },
      });
      if (!ans) return;
      ui.toast(`已确认超额退款 ${ui.money(p.amount)}`, { type: 'ok', hint: '用户端已同步收到结果；原路失败会自动转财务' });
      await render();
    }
    async function reject() {
      const ans = await ui.confirm({
        title: '驳回退款提议', danger: true, confirmText: '确认驳回',
        message: `<p>驳回后工单结案，不会退款。你写的事由会作为答复发给用户，请写成用户能看懂的话。</p>`,
        impact: `工单 ${ticketId} 结案，用户端看到不退款的说明`,
        reasons: ['核实后费用无误，按规则不予退款', '订单事实与描述不符，不予退款'],
        onConfirm: async reason => { await api.admin.cs.refund.reject(ticketId, reason); },
      });
      if (!ans) return;
      ui.toast('已驳回并结案', { type: 'ok', hint: '事由已发给用户并写入操作留痕' });
      await render();
    }
    async function closeTicket() {
      const ans = await ui.confirm({
        title: '结案', confirmText: '确认结案', danger: false,
        message: '<p>结案后用户端显示“已解决”，事由会作为最后一条答复发给用户。</p>',
        impact: `工单 ${ticketId} 结案`, reasons: ['问题已答复，用户无其他疑问', '已按用户要求办理完毕'],
        onConfirm: async reason => { await api.admin.cs.tickets.close(ticketId, reason); },
      });
      if (!ans) return;
      ui.toast('工单已结案', { type: 'ok', hint: '用户端同步显示已解决' });
      await render();
    }
  }
  render();
  return dr;
}

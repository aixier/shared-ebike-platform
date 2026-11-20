// 异议处理 #/fin/disputes：运营商对时长、里程的异议，对账智能体比对，财务确认或驳回（R-23，BR-16、BR-19）
import { ui, api, esc, money, chip, dt, mono, page, section, whoName } from './kit.js';

const FIN = () => api.admin.finance.disputes;
const KIND = { 待核实: 'warn', 已采纳: 'ok', 已驳回: 'err', 无需调整: 'mute' };
const DISPUTABLE = ['时长', '里程'];

const REASONS = {
  采纳: ['核实后平台桩位与锁车记录确有出入，按核实分钟数更正基数', '运营商证据成立，更正服务费基数'],
  驳回: ['平台桩位在位时刻与锁车记录一致，维持原计费', '运营商主张与订单事实不符，驳回'],
  无需调整: ['里程不参与计价，不影响服务费，无需调整', '差异不影响服务费基数，无需调整'],
};

function openDispute(ctx, id, onChanged) {
  const d = ctx.track(ui.drawer({ title: `异议 ${id}`, sub: '加载中…', body: ui.skeleton('lines', 8), wide: true }));
  const sub = ui.$('.a-drawer-head .sub', d.el);
  let x = null;

  async function load() {
    d.setBody(ui.skeleton('lines', 8));
    try {
      x = await FIN().get(id);
      if (x.decidedBy) x.decidedByName = await whoName(x.decidedBy);
      draw();
    }
    catch (e) { d.setBody(''); d.body.append(ui.retry({ text: `${e.message || '异议没有加载出来'}，没有产生任何变更`, onRetry: load })); }
  }

  function draw() {
    sub.textContent = `${x.operatorName} · ${x.settlementId} · ${x.field}异议`;
    const sug = (/建议(采纳|驳回|无需调整)/.exec(x.agentComparison || '') || [])[1] || '';
    const disputable = DISPUTABLE.includes(x.field);
    const pending = x.status === '待核实';
    const orders = x.orders || [];

    const orderRows = orders.map(o => {
      const rec = o.fee ? o.fee.receivable : null;
      return `<tr><td>${mono(o.orderId)}</td><td>${esc(dt(o.startAt))}</td><td>${esc(dt(o.endAt))}</td><td>${esc(o.pileInAt ? dt(o.pileInAt, true) : '—')}</td><td>${esc(o.lockReportAt ? dt(o.lockReportAt, true) : '—')}</td><td class="r num">${o.billedMinutes != null ? o.billedMinutes + ' 分钟' : '—'}</td><td class="r num">${o.distanceKm != null ? o.distanceKm + ' 公里' : '—'}</td><td class="r num">${rec != null ? money(rec) : '—'}</td></tr>`;
    }).join('');

    let decisionArea = '';
    if (!disputable) {
      decisionArea = `<div class="a-alert">价格由主管部门统一制定，不受理价格类异议。运营商只能就订单的时长、里程提出异议。本条无需处理。</div>`;
    } else if (pending) {
      decisionArea = `
        <div><div class="a-sec-title">财务确认</div>
        <form class="a-form" id="decide" novalidate>
          <div class="a-item"><label class="req">处理结果${sug ? `　<span class="chip brand" style="font-weight:500">智能体建议：${esc(sug)}</span>` : ''}</label>
            <div class="a-radio" style="flex-direction:column;align-items:flex-start;gap:8px">
              <label><input type="radio" name="dec" value="采纳"${x.field === '里程' ? ' disabled' : ''}> 采纳：按核实后的分钟数更正服务费基数${x.field === '里程' ? '（里程不参与计价，不可选）' : ''}</label>
              <label><input type="radio" name="dec" value="驳回"> 驳回：异议不成立，维持原结算</label>
              <label><input type="radio" name="dec" value="无需调整"> 无需调整：事实有出入，但不影响服务费</label>
            </div><span class="err-msg" hidden id="dec-err"></span></div>
          <div id="corr" hidden><div class="a-item"><label class="req">核实后的计费分钟（逐单）</label>
            <div style="display:grid;gap:6px">${orders.map(o => `<div class="a-input-group"><span class="mono" style="min-width:150px">${esc(o.orderId)}</span><input class="a-input" style="min-width:90px;width:110px" inputmode="numeric" data-m="${esc(o.orderId)}" value="${esc(o.billedMinutes ?? '')}" aria-label="${esc(o.orderId)} 核实分钟"><span class="unit">分钟（平台现计 ${o.billedMinutes ?? '—'} 分钟）</span></div>`).join('')}</div>
            <span class="tip">采纳只更正服务费基数，不向用户补收；差额自动进入下一期结算单的「异议核实」调整项。</span><span class="err-msg" hidden id="corr-err"></span></div></div>
          <div><button type="submit" class="a-btn a-btn-primary">确认处理…</button></div>
        </form></div>`;
    } else {
      decisionArea = `<div><div class="a-sec-title">处理结果</div>
        <dl class="a-dl"><dt>结果</dt><dd>${chip(x.status, KIND[x.status])}</dd><dt>处理人</dt><dd>${esc(x.decidedByName || '—')}</dd><dt>处理时间</dt><dd>${esc(dt(x.decidedAt))}</dd><dt>事由</dt><dd>${esc(x.decisionReason || '—')}</dd>
        ${x.corrections && x.corrections.length ? `<dt>基数更正</dt><dd>${x.corrections.map(c => `${mono(c.orderId)} 核实 ${c.minutes} 分钟，基数 ${money(c.baseDelta, { sign: true })}`).join('<br>')}</dd>` : ''}
        <dt>调整项</dt><dd>${x.adjustmentId ? mono(x.adjustmentId) + '（已计入结算单）' : x.status === '已采纳' ? '差额将在下一期结算单出单时计入「异议核实」调整项' : '无'}</dd></dl></div>`;
    }

    d.setBody(`
      <div>${chip(x.status, KIND[x.status])}　<span style="font-size:12px;color:var(--ink-3)">提交 ${esc(dt(x.submittedAt))}${x.submittedByName ? ' · ' + esc(x.submittedByName) : ''}</span></div>
      <div><div class="a-sec-title">运营商主张</div><div style="font-size:13px">${esc(x.operatorClaim || '—')}</div></div>
      <div><div class="a-sec-title">对账智能体比对结论　<span class="chip brand" style="font-weight:500">智能体按订单事实生成</span></div>
        <div class="a-alert is-info" style="display:block"><div>${esc(x.agentComparison || '尚未生成比对结论')}</div>${x.platformFact ? `<div style="margin-top:6px;font-size:12px;opacity:.85">依据：${esc(x.platformFact)}</div>` : ''}</div>
        <div style="font-size:12px;color:var(--ink-3);margin-top:4px">智能体只给出建议，是否采纳由财务确认并写明事由。</div></div>
      <div><div class="a-sec-title">订单事实（${orders.length} 单）</div>
        <div class="a-table-wrap"><div class="a-table-scroll"><table class="a-table"><thead><tr><th>订单号</th><th>开始</th><th>结束</th><th>桩在位</th><th>锁车报告</th><th class="r">计费</th><th class="r">里程</th><th class="r">订单应收</th></tr></thead><tbody>${orderRows || '<tr><td colspan="8" style="text-align:center;color:var(--ink-3)">没有关联订单</td></tr>'}</tbody></table></div></div></div>
      ${decisionArea}`);

    const form = ui.$('#decide', d.body);
    if (!form) return;
    const corr = ui.$('#corr', d.body);
    ui.$$('input[name=dec]', form).forEach(r => r.addEventListener('change', () => { corr.hidden = r.value !== '采纳'; ui.$('#dec-err', form).hidden = true; }));
    form.addEventListener('submit', async e => {
      e.preventDefault();
      const dec = (form.elements.dec.value || '');
      const derr = ui.$('#dec-err', form), cerr = ui.$('#corr-err', form);
      derr.hidden = true; cerr.hidden = true;
      if (!dec) { derr.hidden = false; derr.textContent = '请先选择处理结果'; return; }
      let corrections = [];
      if (dec === '采纳') {
        corrections = ui.$$('[data-m]', form).map(i => ({ orderId: i.dataset.m, minutes: Number(i.value) }));
        const bad = corrections.find(c => !(Number.isInteger(c.minutes) && c.minutes >= 1 && c.minutes <= 600));
        if (bad) { cerr.hidden = false; cerr.textContent = `${bad.orderId} 的核实分钟需为 1–600 的整数`; return; }
      }
      const r = await ui.confirm({
        title: `确认${dec}异议 ${x.disputeId}`, confirmText: `确认${dec}`, danger: dec === '驳回', reasons: REASONS[dec],
        message: `<div>${esc(x.operatorName)} 对 ${x.orderIds.length} 单${esc(x.field)}提出的异议，处理结果：<b>${esc(dec)}</b>。</div>${corrections.length ? `<div style="margin-top:6px">${corrections.map(c => `${esc(c.orderId)} 核实为 ${c.minutes} 分钟`).join('<br>')}</div>` : ''}<div style="margin-top:6px;font-size:12px;color:var(--ink-3)">事由会同时写入操作留痕，运营商能看到处理结果。</div>`,
        impact: dec === '采纳' ? `${x.settlementId} 对应订单的服务费基数更正，差额计入下一期调整项；不向用户补收` : `${x.settlementId} 维持原结算，不产生调整项`,
        onConfirm: reason => FIN().decide(x.disputeId, { decision: dec, corrections }, reason),
      });
      if (r) { ui.toast(`异议已${dec}`, { type: 'ok', hint: dec === '采纳' ? '差额已记入下一期结算单的调整项' : '处理结果已通知运营商' }); onChanged && onChanged(); await load(); }
    });
  }

  load();
}

export default page({
  path: '#/fin/disputes', title: '异议处理', menu: '异议处理', perm: 'dispute.decide',
  build(ctx) {
    const { el } = ctx;
    const st = { status: ctx.params.status || '', operatorId: '' };
    ctx.head('运营商对订单时长、里程的反馈；对账智能体先比对订单事实，财务确认后再调整');
    el.innerHTML = '<div class="a-alert is-info" style="margin-bottom:12px">价格由主管部门统一制定，不受理价格类异议。异议经采纳的，只更正服务费基数、不向用户补收，差额进下一期结算单调整项。</div><div id="main"></div>';
    let reload = () => {};
    reload = section(ui.$('#main', el), () => FIN().list({}), (all, b) => {
      const cnt = k => all.filter(x => x.status === k).length;
      const ops = [...new Map(all.map(x => [x.operatorId, x.operatorName])).entries()];
      b.innerHTML = `
        <div class="a-stats">${[['待核实', '需要财务确认'], ['已采纳', '已更正基数'], ['已驳回', '维持原结算'], ['无需调整', '不影响服务费']].map(([k, n]) => `<button type="button" class="a-stat${st.status === k ? ' is-on' : ''}" data-st="${k}"><div class="k">${k}</div><div class="v num">${cnt(k)}<small>条</small></div><div class="d">${n}</div></button>`).join('')}</div>
        <div class="a-filter">
          <div class="f"><label for="fs">状态</label><select class="a-select" id="fs"><option value="">全部状态</option>${Object.keys(KIND).map(k => `<option${st.status === k ? ' selected' : ''}>${k}</option>`).join('')}</select></div>
          <div class="f"><label for="fo">运营商</label><select class="a-select" id="fo"><option value="">全部运营商</option>${ops.map(([i, n]) => `<option value="${i}"${st.operatorId === i ? ' selected' : ''}>${esc(n)}</option>`).join('')}</select></div>
          <span class="grow"></span><button type="button" class="a-btn a-btn-text" id="rst">清空筛选</button></div>
        <div id="tb"></div>`;
      let table;
      const apply = () => { st.status = ui.$('#fs', b).value; st.operatorId = ui.$('#fo', b).value; ui.$$('[data-st]', b).forEach(x => x.classList.toggle('is-on', x.dataset.st === st.status)); table.goto(1); };
      ui.$('#fs', b).addEventListener('change', apply); ui.$('#fo', b).addEventListener('change', apply);
      ui.$('#rst', b).addEventListener('click', () => { ui.$('#fs', b).value = ''; ui.$('#fo', b).value = ''; apply(); });
      ui.$$('[data-st]', b).forEach(x => x.addEventListener('click', () => { ui.$('#fs', b).value = st.status === x.dataset.st ? '' : x.dataset.st; apply(); }));
      const open = r => openDispute(ctx, r.disputeId, () => reload(true));
      table = ui.table(ui.$('#tb', b), {
        rowKey: 'disputeId', pageSize: 10, pageSizes: [10, 20, 50], onRowClick: open, onAction: (a, r) => open(r),
        empty: { title: '没有符合条件的异议', text: '运营商提交异议后会出现在这里', action: '清空筛选', onAction: () => ui.$('#rst', b).click() },
        load: async ({ page, pageSize }) => {
          const rows = await FIN().list({ status: st.status || undefined, operatorId: st.operatorId || undefined });
          return { rows: rows.slice((page - 1) * pageSize, page * pageSize), total: rows.length };
        },
        columns: [
          { key: 'disputeId', title: '异议编号', render: r => mono(r.disputeId) },
          { key: 'operatorName', title: '运营商', render: r => esc(r.operatorName) },
          { key: 'settlementId', title: '结算单', render: r => mono(r.settlementId) },
          { key: 'field', title: '事项', render: r => esc(r.field) },
          { key: 'n', title: '订单数', align: 'right', render: r => String(r.orderIds.length) },
          { key: 'claim', title: '运营商主张', ellipsis: true, render: r => esc(r.operatorClaim || '') },
          { key: 'submittedAt', title: '提交时间', render: r => esc(dt(r.submittedAt)) },
          { key: 'status', title: '状态', render: r => chip(r.status, KIND[r.status] || 'mute') },
          { key: 'who', title: '处理人', render: r => esc(r.decidedByName || '—') },
          { key: 'act', title: '', align: 'right', width: '80px', render: r => `<button type="button" class="a-btn ${r.status === '待核实' ? 'a-btn-default' : 'a-btn-text'} is-sm" data-act="open">${r.status === '待核实' ? '去处理' : '查看'}</button>` },
        ],
      });
    });
    ctx.watch(['dispute.changed', 'settlement.changed'], () => { if (!document.querySelector('.a-drawer')) reload(true); });
  },
});

// 结算单管理 #/fin/settlements：出单、复算、调整项、审核、发送、到账登记（R-23，BR-17～BR-20，S-05）
import { ui, api, esc, fee2, money, chip, pct, dt, mono, feeOf, sum2, nowInfo, monthAdd, download, page, section, busy, failToast, askForm, REASON_RULE } from './kit.js';

const FIN = () => api.admin.finance.settlements;
const STATUS_KIND = { 待审核: 'warn', 已审核: 'brand', 已发送: 'brand', 已到账: 'ok', 未出单: 'mute' };
const FLOW = ['待审核', '已审核', '已发送', '已到账'];

const statusChip = s => chip(s.status, STATUS_KIND[s.status] || 'mute') + (s.overdue ? ' ' + chip('逾期未到账', 'err') : '');

/* ================= 抽屉：结算单详情与操作 ================= */
function openSettlement(ctx, ref, onChanged) {
  const isPreview = !!ref.previewOperatorId;
  const d = ctx.track(ui.drawer({ title: ref.title || '结算单', sub: '加载中…', body: ui.skeleton('lines', 8), wide: true }));
  const head = ui.$('.a-drawer-head h2', d.el), sub = ui.$('.a-drawer-head .sub', d.el);
  let s = null, detailTable = null;

  async function fetchIt() {
    d.setBody(ui.skeleton('lines', 8));
    try {
      s = isPreview ? await FIN().preview(ref.previewOperatorId) : await FIN().get(ref.settlementId);
      draw();
    } catch (e) {
      d.setBody('');
      d.body.append(ui.retry({ text: `${e.message || '结算单没有加载出来'}，没有产生任何变更`, onRetry: fetchIt }));
    }
  }

  async function refreshAfterWrite() {
    onChanged && onChanged();
    try { s = await FIN().get(s.settlementId); draw(); } catch { /* 列表已刷新，抽屉保留旧内容 */ }
  }

  function draw() {
    head.textContent = isPreview ? `${s.operatorName} · ${s.month} 本期预览` : `${s.operatorName} · ${s.month} 结算单`;
    sub.textContent = isPreview ? '尚未出单，按截至现在的订单与事件实时汇总' : `${s.settlementId} · 到账期限 ${s.dueDate}`;
    const t = s.totals || {};
    const details = s.details || [];
    const unconfirmed = s.adjustments.filter(a => !a.confirmedBy);

    /* 逐单复算 */
    const groups = new Map();
    for (const x of details) {
      const k = `${x.brandId}|${x.ratio}|${x.pricingVersionId}`;
      if (!groups.has(k)) groups.set(k, { brandId: x.brandId, ratio: x.ratio, ver: x.pricingVersionId, n: 0, bases: [], recalc: [], rec: [] });
      const g = groups.get(k);
      g.n++; g.bases.push(x.base); g.recalc.push(feeOf(x.base, x.ratio)); g.rec.push(x.fee);
    }
    const brandName = id => (s.brands.find(b => b.brandId === id) || {}).brandName || id;
    const gl = [...groups.values()].sort((a, b) => (a.brandId + a.ver).localeCompare(b.brandId + b.ver));
    const recalcTotal = sum2(gl.flatMap(g => g.recalc));
    const recTotal = sum2(details.map(x => x.fee));
    const okDetail = Math.abs(recalcTotal - (t.detailFee || 0)) < 0.005 && Math.abs(recTotal - (t.detailFee || 0)) < 0.005;
    const okPayable = Math.abs((t.detailFee || 0) + (t.adjustmentFee || 0) - (t.payableFee || 0)) < 0.005;

    /* 操作区 */
    const acts = [];
    if (!isPreview) {
      if (s.status === '待审核') {
        acts.push(`<button class="a-btn a-btn-default" data-do="adj">录入调整项</button>`);
        acts.push(unconfirmed.length
          ? `<button class="a-btn a-btn-primary" aria-disabled="true" data-why="还有 ${unconfirmed.length} 个调整项没有确认" data-next="在下方「调整项」里逐个确认后再审核">审核通过</button>`
          : `<button class="a-btn a-btn-primary" data-do="review">审核通过</button>`);
      }
      if (s.status === '已审核') acts.push(`<button class="a-btn a-btn-primary" data-do="send">发送给运营商</button>`);
      if (s.status === '已发送') acts.push(`<button class="a-btn a-btn-primary" data-do="pay">登记到账</button>`);
      acts.push(`<button class="a-btn a-btn-default" data-do="export">导出明细</button>`);
    }

    /* 流程 */
    const steps = [
      ['生成', s.generatedAt, '系统按次月 1 日 00:30 规则出单'],
      ['审核', s.reviewedAt, s.reviewedByName ? `审核人 ${s.reviewedByName}` : ''],
      ['发送给运营商', s.sentAt, ''],
      ['到账', s.paidAt, s.bankTradeNo ? `银行流水号 ${s.bankTradeNo}${s.paidAt && s.paidAt.slice(0, 10) > s.dueDate ? '（晚于到账期限）' : ''}` : ''],
    ];
    const flow = isPreview ? '' : `<div><div class="a-sec-title">处理进度</div><ul class="a-timeline">${steps.map(([n, at, note]) => `<li style="${at ? '' : 'opacity:.55'}"><div>${esc(n)}${at ? '' : '　<span style="color:var(--ink-3)">待办</span>'}</div><div class="t">${at ? esc(dt(at)) : '—'}${note ? ' · ' + esc(note) : ''}</div></li>`).join('')}</ul></div>`;

    d.setBody(`
      ${s.note ? `<div class="a-alert is-info">${esc(s.note)}</div>` : ''}
      ${isPreview ? `<div class="a-alert is-info">${esc(s.previewNote || '本期尚未出单')}。下面的调整项会在出单时一并进入结算单。</div>` : ''}
      <div style="display:flex;justify-content:space-between;align-items:center;gap:12px"><div>${isPreview ? chip('未出单', 'mute') : statusChip(s)}</div><div class="a-actions">${acts.join('')}</div></div>
      <div class="a-stats" style="margin:0;grid-template-columns:repeat(5,minmax(0,1fr))">
        <div class="a-stat"><div class="k">订单数</div><div class="v num">${ui.number(t.orderCount)}</div></div>
        <div class="a-stat"><div class="k">订单应收合计</div><div class="v num" style="font-size:20px">${money(t.receivableSum)}</div></div>
        <div class="a-stat"><div class="k">明细服务费</div><div class="v num" style="font-size:20px">${fee2(t.detailFee)}</div></div>
        <div class="a-stat"><div class="k">调整项合计</div><div class="v num" style="font-size:20px">${fee2(t.adjustmentFee, { sign: true })}</div></div>
        <div class="a-stat is-on" style="border-color:var(--orange-500);background:var(--orange-50)"><div class="k">应付服务费</div><div class="v num" style="font-size:20px">${fee2(t.payableFee)}</div></div>
      </div>
      ${flow}
      <div>
        <div class="a-sec-title">服务费复算（服务费 = 订单应收基数 × 品牌比例，逐单四舍五入到分）</div>
        <div class="a-alert ${okDetail && okPayable ? 'is-ok' : 'is-err'}" style="margin-bottom:8px">${okDetail && okPayable
          ? `按 ${ui.number(details.length)} 笔订单逐单复算，明细服务费 ${fee2(recalcTotal)}，与结算单一致；明细 ${fee2(t.detailFee)} ${s.adjustments.length ? '＋ 调整项 ' + fee2(t.adjustmentFee, { sign: true }) : ''} ＝ 应付 ${fee2(t.payableFee)}。`
          : `逐单复算为 ${fee2(recalcTotal)}，结算单记录为 ${fee2(t.detailFee)}，不一致。请先核对明细，再决定是否审核。`}</div>
        <div class="a-table-wrap"><div class="a-table-scroll"><table class="a-table"><thead><tr><th>品牌</th><th class="r">比例</th><th>价格版本</th><th class="r">订单数</th><th class="r">服务费基数</th><th class="r">逐单复算</th><th class="r">结算单记录</th><th>核对</th></tr></thead><tbody>
          ${gl.map(g => { const base = sum2(g.bases), rc = sum2(g.recalc), rd = sum2(g.rec), ok = Math.abs(rc - rd) < 0.005; return `<tr><td>${esc(brandName(g.brandId))}</td><td class="r num">${pct(g.ratio)}</td><td>${mono(g.ver)}</td><td class="r num">${g.n}</td><td class="r num">${money(base)}</td><td class="r num">${fee2(rc)}</td><td class="r num">${fee2(rd)}</td><td>${ok ? chip('一致', 'ok') : chip('相差 ' + fee2(rd - rc), 'err')}</td></tr>`; }).join('') || `<tr><td colspan="8" style="text-align:center;color:var(--ink-3)">本期没有明细订单</td></tr>`}
        </tbody></table></div></div>
        ${s.monthCard ? `<div style="font-size:12px;color:var(--ink-3);margin-top:6px">月卡款 ${money(s.monthCard.platformIncome)}（${esc(s.monthCard.allocationRule)}）：${s.brands.map(b => `${esc(b.brandName)} 分得 ${fee2(b.monthCardShare)}`).join('、')}。${esc(s.monthCard.note || '')}</div>` : ''}
      </div>
      <div>
        <div class="a-sec-title">调整项（${s.adjustments.length}）</div>
        ${s.adjustments.length ? s.adjustments.map(a => `
          <div class="a-panel" style="margin-bottom:8px"><div class="a-panel-body" style="padding:12px 14px;display:grid;gap:6px">
            <div style="display:flex;justify-content:space-between;gap:8px;align-items:center"><div>${chip(a.type, a.type === '人工调整' ? 'brand' : 'warn')} <b>${esc(a.brandName)}</b> <span class="num" style="margin-left:6px;font-weight:700">${fee2(a.feeDelta, { sign: true })}</span></div>
              <div>${a.confirmedBy ? chip('已确认 · ' + (a.confirmedByName || ''), 'ok') : (isPreview ? chip('出单后待确认', 'mute') : chip('待确认', 'warn'))}${!isPreview && !a.confirmedBy && s.status === '待审核' ? ` <button class="a-btn a-btn-default is-sm" data-confirm="${esc(a.adjustmentId)}">确认调整项</button>` : ''}</div></div>
            <div style="font-size:13px;color:var(--ink-2)">${esc(a.reason)}</div>
            <div style="font-size:12px;color:var(--ink-3)">${esc(a.adjustmentId || '')}${a.createdByName ? ' · 由 ' + esc(a.createdByName) + ' 提出' : ''}${a.createdAt ? ' · ' + esc(dt(a.createdAt)) : ''}</div>
            ${a.items && a.items.length ? `<details><summary style="cursor:pointer;font-size:12px;color:var(--orange-700)">查看 ${a.items.length} 笔订单依据</summary><div class="a-table-wrap" style="margin-top:6px"><div class="a-table-scroll"><table class="a-table"><thead><tr><th>订单号</th><th>结束时间</th><th class="r">原基数</th><th class="r">新基数</th><th class="r">比例</th><th class="r">服务费增减</th></tr></thead><tbody>${a.items.map(i => `<tr><td>${mono(i.orderId)}</td><td>${esc(dt(i.endAt))}</td><td class="r num">${money(i.previousBase)}</td><td class="r num">${money(i.newBase)}</td><td class="r num">${pct(i.ratio)}</td><td class="r num">${fee2(i.feeDelta, { sign: true })}</td></tr>`).join('')}</tbody></table></div></div></details>` : ''}
            ${a.orderIds && a.orderIds.length ? `<div style="font-size:12px;color:var(--ink-3)">涉及订单：${a.orderIds.map(esc).join('、')}</div>` : ''}
          </div></div>`).join('') : `<div style="font-size:13px;color:var(--ink-3)">${isPreview ? '目前没有待进单的调整项。' : '本单没有调整项。出单后到达的补报、退款与异议核实结果，会自动出现在下一期结算单的调整项里。'}</div>`}
      </div>
      <div>
        <div class="a-sec-title">逐单明细（${ui.number(details.length)} 笔）</div>
        <div class="a-filter" style="margin-bottom:8px;padding:8px 12px"><div class="f"><label for="dq">订单号</label><input id="dq" class="a-input a-search" placeholder="输入订单号的一部分" autocomplete="off"></div></div>
        <div id="dt"></div>
      </div>`);

    /* 明细表 */
    const dtEl = ui.$('#dt', d.body), q = ui.$('#dq', d.body);
    const recalc = r => feeOf(r.base, r.ratio);
    detailTable = ui.table(dtEl, {
      rowKey: 'orderId', pageSize: 10, pageSizes: [10, 20, 50], rows: details,
      empty: { title: '没有明细订单', text: '本结算月没有已到达的订单' },
      columns: [
        { key: 'orderId', title: '订单号', render: r => mono(r.orderId) },
        { key: 'brandId', title: '品牌', render: r => esc(brandName(r.brandId)) },
        { key: 'endAt', title: '结束时间', render: r => esc(dt(r.endAt)), sortable: true },
        { key: 'billedMinutes', title: '计费分钟', align: 'right', sortable: true, render: r => ui.number(r.billedMinutes) },
        { key: 'receivable', title: '订单应收', align: 'right', sortable: true, render: r => money(r.receivable) },
        { key: 'base', title: '服务费基数', align: 'right', render: r => money(r.base) },
        { key: 'ratio', title: '比例', align: 'right', render: r => pct(r.ratio) },
        { key: 'fee', title: '服务费', align: 'right', sortable: true, render: r => fee2(r.fee) },
        { key: 'chk', title: '复算', render: r => Math.abs(recalc(r) - r.fee) < 0.005 ? chip('一致', 'ok') : chip('应为 ' + fee2(recalc(r)), 'err') },
        { key: 'remark', title: '备注', ellipsis: true, render: r => esc(r.remark || (r.degraded ? '降级单' : '')) },
      ],
    });
    q.addEventListener('input', () => { const k = q.value.trim(); detailTable.setRows(k ? details.filter(x => x.orderId.includes(k)) : details); });

    /* 操作 */
    const on = (sel, fn) => { const b = ui.$(sel, d.body); b && b.addEventListener('click', () => busy(b, fn)); };
    on('[data-do=export]', async () => {
      try { const r = await FIN().export(s.settlementId); download(r.fileName, r.csv); ui.toast('明细已导出', { type: 'ok', hint: r.fileName }); } catch (e) { failToast(e); }
    });
    on('[data-do=adj]', () => addAdjustment());
    on('[data-do=review]', () => review());
    on('[data-do=send]', () => send());
    on('[data-do=pay]', () => registerPayment());
    ui.$$('[data-confirm]', d.body).forEach(b => b.addEventListener('click', () => busy(b, () => confirmAdj(b.dataset.confirm))));
  }

  const impactOf = (delta) => `${s.operatorName} ${s.month} 结算单应付服务费 ${fee2(s.totals.payableFee)}${delta != null ? ' → ' + fee2(Math.round((s.totals.payableFee + delta) * 100) / 100) : ''}`;

  async function addAdjustment() {
    const v = await askForm({
      title: '录入调整项',
      intro: '调整项只改服务费，不改订单，也不向用户收费。填写后还会再确认一次。',
      fields: [
        { name: 'brandId', label: '品牌', type: 'select', options: s.brands.map(b => ({ v: b.brandId, t: `${b.brandName}（比例 ${pct((b.ratioBreakdown[0] || {}).ratio)}）` })), required: true },
        { name: 'feeDelta', label: '服务费增减（元）', type: 'number', step: '0.01', required: true, placeholder: '例如 12.50，扣减填 -12.50', tip: '可为负数，最多两位小数，不能为 0' },
        { name: 'orderIds', label: '涉及订单号（选填）', placeholder: '多个订单号用逗号分隔' },
        { name: 'reason', label: '事由', type: 'textarea', required: true, placeholder: '写明为什么调整；运营商在结算单上也能看到' },
      ],
      rules: {
        brandId: { label: '品牌', required: true },
        feeDelta: { label: '服务费增减', required: true, type: 'number', custom: v => (v === 0 ? '调整金额不能为 0' : Math.abs(v * 100 - Math.round(v * 100)) > 1e-6 ? '金额最多两位小数' : Math.abs(v) > 100000 ? '金额超出合理范围，请核对' : '') },
        orderIds: { label: '订单号', custom: v => (v && v.split(/[,，\s]+/).filter(Boolean).some(x => !/^OD\d{10,}$/.test(x)) ? '订单号格式不对，应为 OD 开头的数字编号' : '') },
        reason: REASON_RULE,
      },
    });
    if (!v) return;
    const delta = Number(v.feeDelta);
    const orderIds = (v.orderIds || '').split(/[,，\s]+/).filter(Boolean);
    const bn = (s.brands.find(b => b.brandId === v.brandId) || {}).brandName;
    const r = await ui.confirm({
      title: '确认录入调整项', confirmText: '确认录入', requireReason: false,
      message: `<div>品牌：<b>${esc(bn)}</b>；服务费 <b class="num">${fee2(delta, { sign: true })}</b></div><div style="margin-top:6px">事由：${esc(v.reason)}</div>`,
      impact: impactOf(delta),
      onConfirm: () => FIN().addAdjustment(s.settlementId, { brandId: v.brandId, feeDelta: delta, orderIds }, v.reason),
    });
    if (r) { ui.toast('调整项已录入', { type: 'ok', hint: '事由已写入操作留痕' }); await refreshAfterWrite(); }
  }

  async function confirmAdj(adjId) {
    const a = s.adjustments.find(x => x.adjustmentId === adjId);
    const r = await ui.confirm({
      title: '确认调整项', confirmText: '确认调整项', requireReason: false,
      message: `<div>${esc(a.type)}：<b>${esc(a.brandName)}</b> 服务费 <b class="num">${fee2(a.feeDelta, { sign: true })}</b></div><div style="margin-top:6px;color:var(--ink-2)">${esc(a.reason)}</div><div style="margin-top:6px;font-size:12px;color:var(--ink-3)">该调整项由对账智能体按订单事实生成，确认后计入应付服务费。</div>`,
      impact: impactOf(),
      onConfirm: () => FIN().confirmAdjustment(s.settlementId, adjId),
    });
    if (r) { ui.toast('调整项已确认', { type: 'ok' }); await refreshAfterWrite(); }
  }

  async function review() {
    const r = await ui.confirm({
      title: '审核通过', confirmText: '审核通过', requireReason: false,
      message: `<div>应付服务费 <b class="num">${fee2(s.totals.payableFee)}</b>，其中调整项 ${fee2(s.totals.adjustmentFee, { sign: true })}。审核通过后不能再录入或确认调整项。</div>`,
      impact: impactOf(),
      onConfirm: () => FIN().review(s.settlementId),
    });
    if (r) { ui.toast('已审核通过', { type: 'ok', hint: '下一步：发送给运营商' }); await refreshAfterWrite(); }
  }

  async function send() {
    const r = await ui.confirm({
      title: '发送给运营商', confirmText: '确认发送', requireReason: false,
      message: `<div>发送后，${esc(s.operatorName)} 在自己的结算单页可以查看并下载本单，可就时长、里程提出异议。</div>`,
      impact: `${s.operatorName} ${s.month} 结算单，应付服务费 ${fee2(s.totals.payableFee)}`,
      onConfirm: () => FIN().send(s.settlementId),
    });
    if (r) { ui.toast('已发送给运营商', { type: 'ok', hint: `到账期限 ${s.dueDate}，到账后在这里登记` }); await refreshAfterWrite(); }
  }

  async function registerPayment() {
    const { today } = await nowInfo();
    const v = await askForm({
      title: '登记到账',
      intro: `${esc(s.operatorName)} ${esc(s.month)} 结算单，应付服务费 <b>${fee2(s.totals.payableFee)}</b>，到账期限 ${esc(s.dueDate)}。`,
      fields: [
        { name: 'bankTradeNo', label: '银行流水号', required: true, placeholder: '8–32 位字母或数字', tip: '以银行回单为准' },
        { name: 'paidDate', label: '到账日期', type: 'date', value: today, max: today, required: true },
      ],
      rules: {
        bankTradeNo: { label: '银行流水号', required: true, pattern: /^[A-Za-z0-9]{8,32}$/, message: '银行流水号为 8–32 位字母或数字' },
        paidDate: { label: '到账日期', required: true, custom: x => (x > today ? '到账日期不能晚于今天' : '') },
      },
    });
    if (!v) return;
    const { now } = await nowInfo();
    const paidAt = v.paidDate === now.slice(0, 10) ? now : `${v.paidDate} 12:00:00`;
    const late = v.paidDate > s.dueDate;
    const r = await ui.confirm({
      title: '确认登记到账', confirmText: '确认登记', requireReason: false,
      message: `<div>银行流水号 <b class="mono">${esc(v.bankTradeNo.toUpperCase())}</b>，到账日期 <b>${esc(v.paidDate)}</b>${late ? '，<span style="color:var(--err)">晚于到账期限 ' + esc(s.dueDate) + '</span>' : ''}。</div>`,
      impact: `${s.operatorName} ${s.month} 结算单标记为已到账 ${fee2(s.totals.payableFee)}`,
      onConfirm: () => FIN().registerPayment(s.settlementId, { bankTradeNo: v.bankTradeNo.toUpperCase(), paidAt }),
    });
    if (r) { ui.toast('到账已登记', { type: 'ok' }); await refreshAfterWrite(); }
  }

  fetchIt();
  return d;
}

/* ================= 页面 ================= */
export default page({
  path: '#/fin/settlements', title: '结算单管理', menu: '结算单管理', perm: 'settlement.manage',
  build(ctx) {
    const { el } = ctx;
    const st = { tab: 'list', month: '', operatorId: '', status: '' };
    const qs = ctx.params;
    if (qs.status) st.status = qs.status;
    if (qs.month) st.month = qs.month;

    const genBtn = ui.el('<button type="button" class="a-btn a-btn-primary" id="gen">生成结算单</button>');
    ctx.head('每月 1 日 00:30 按上一结算月出单，次月 6 日（含）前到账', [genBtn]);
    el.innerHTML = '<div id="tabs"></div><div id="pane"></div>';
    const pane = ui.$('#pane', el);
    let reloadPane = () => {};

    ui.tabs(ui.$('#tabs', el), [{ key: 'list', label: '结算单' }, { key: 'preview', label: '本期预览' }], st.tab, k => { st.tab = k; drawTab(); });

    function drawTab() { pane.innerHTML = ''; st.tab === 'list' ? drawList() : drawPreview(); }

    /* ---------- 结算单列表 ---------- */
    function drawList() {
      const box = ui.el('<div></div>'); pane.append(box);
      let table = null;
      reloadPane = section(box, () => FIN().list({}), (all, b) => {
        const months = [...new Set(all.map(x => x.month))].sort().reverse();
        const ops = [...new Map(all.map(x => [x.operatorId, x.operatorName])).entries()];
        const cnt = k => all.filter(x => x.status === k).length;
        const overdue = all.filter(x => x.overdue).length;
        b.innerHTML = `
          <div class="a-stats">
            ${[['待审核', cnt('待审核'), '审核前先确认调整项'], ['已审核', cnt('已审核'), '待发送给运营商'], ['已发送', cnt('已发送'), overdue ? `其中 ${overdue} 张已超过到账期限` : '待运营商到账'], ['已到账', cnt('已到账'), '已登记银行流水']].map(([k, n, note]) => `<button type="button" class="a-stat${st.status === k ? ' is-on' : ''}" data-st="${k}"><div class="k">${k}</div><div class="v num">${n}<small>张</small></div><div class="d${k === '已发送' && overdue ? ' down' : ''}">${note}</div></button>`).join('')}
          </div>
          <div class="a-filter">
            <div class="f"><label for="fm">结算月</label><select class="a-select" id="fm"><option value="">全部月份</option>${months.map(m => `<option value="${m}"${st.month === m ? ' selected' : ''}>${m}</option>`).join('')}</select></div>
            <div class="f"><label for="fo">运营商</label><select class="a-select" id="fo"><option value="">全部运营商</option>${ops.map(([id, n]) => `<option value="${id}"${st.operatorId === id ? ' selected' : ''}>${esc(n)}</option>`).join('')}</select></div>
            <div class="f"><label for="fs">状态</label><select class="a-select" id="fs"><option value="">全部状态</option>${FLOW.map(k => `<option${st.status === k ? ' selected' : ''}>${k}</option>`).join('')}</select></div>
            <span class="grow"></span><button type="button" class="a-btn a-btn-text" id="rst">清空筛选</button>
          </div><div id="tb"></div>`;
        const apply = () => { st.month = ui.$('#fm', b).value; st.operatorId = ui.$('#fo', b).value; st.status = ui.$('#fs', b).value; ui.$$('[data-st]', b).forEach(x => x.classList.toggle('is-on', x.dataset.st === st.status)); table.goto(1); };
        ['#fm', '#fo', '#fs'].forEach(s => ui.$(s, b).addEventListener('change', apply));
        ui.$('#rst', b).addEventListener('click', () => { ['#fm', '#fo', '#fs'].forEach(s => { ui.$(s, b).value = ''; }); apply(); });
        ui.$$('[data-st]', b).forEach(x => x.addEventListener('click', () => { ui.$('#fs', b).value = st.status === x.dataset.st ? '' : x.dataset.st; apply(); }));

        const sorters = { orders: r => r.totals.orderCount, receivable: r => r.totals.receivableSum, fee: r => r.totals.detailFee, adj: r => r.totals.adjustmentFee, payable: r => r.totals.payableFee, due: r => r.dueDate };
        const open = r => openSettlement(ctx, { settlementId: r.settlementId, title: `${r.operatorName} · ${r.month} 结算单` }, () => reloadPane(true));
        table = ui.table(ui.$('#tb', b), {
          rowKey: 'settlementId', pageSize: 10, pageSizes: [10, 20, 50],
          empty: { title: '没有符合条件的结算单', text: '换个月份或运营商看看，或点击右上角「生成结算单」', action: '清空筛选', onAction: () => ui.$('#rst', b).click() },
          onRowClick: open, onAction: (act, r) => open(r),
          load: async ({ page, pageSize, sort }) => {
            let rows = await FIN().list({ month: st.month || undefined, operatorId: st.operatorId || undefined, status: st.status || undefined });
            if (sort && sorters[sort.key]) { const f = sorters[sort.key]; rows = [...rows].sort((x, y) => (typeof f(x) === 'number' ? f(x) - f(y) : String(f(x)).localeCompare(String(f(y)))) * (sort.dir === 'asc' ? 1 : -1)); }
            return { rows: rows.slice((page - 1) * pageSize, page * pageSize), total: rows.length };
          },
          columns: [
            { key: 'settlementId', title: '结算单号', render: r => mono(r.settlementId) },
            { key: 'month', title: '结算月' },
            { key: 'operatorName', title: '运营商', render: r => esc(r.operatorName) },
            { key: 'orders', title: '订单数', align: 'right', sortable: true, render: r => ui.number(r.totals.orderCount) },
            { key: 'receivable', title: '订单应收', align: 'right', sortable: true, render: r => money(r.totals.receivableSum) },
            { key: 'fee', title: '明细服务费', align: 'right', sortable: true, render: r => fee2(r.totals.detailFee) },
            { key: 'adj', title: '调整项', align: 'right', sortable: true, render: r => `${fee2(r.totals.adjustmentFee, { sign: true })}${r.adjustments.some(a => !a.confirmedBy) ? ' <span title="有待确认的调整项" style="color:var(--warn)">●</span>' : ''}` },
            { key: 'payable', title: '应付服务费', align: 'right', sortable: true, render: r => `<b>${fee2(r.totals.payableFee)}</b>` },
            { key: 'due', title: '到账期限', sortable: true, render: r => esc(r.dueDate) },
            { key: 'status', title: '状态', render: statusChip },
            { key: 'act', title: '', align: 'right', width: '84px', render: () => '<button type="button" class="a-btn a-btn-text is-sm" data-act="open">查看明细</button>' },
          ],
        });
      });
    }

    /* ---------- 本期预览 ---------- */
    function drawPreview() {
      const box = ui.el('<div></div>'); pane.append(box);
      reloadPane = section(box, async () => {
        const all = await FIN().list({});
        const ops = [...new Map(all.map(x => [x.operatorId, x.operatorName])).entries()].filter(([id]) => true);
        const res = await Promise.allSettled(ops.map(([id]) => FIN().preview(id)));
        const rows = res.filter(r => r.status === 'fulfilled').map(r => r.value);
        if (!rows.length && res.length) throw res.find(r => r.status === 'rejected').reason;
        return rows;
      }, (rows, b) => {
        const month = rows[0] && rows[0].month;
        b.innerHTML = `<div class="a-alert is-info" style="margin-bottom:12px">本期（${esc(month || '')}）尚未出单，下表按截至现在的订单实时汇总。出单后到达的补报、退款与异议核实结果，会自动列在下面的「待进单调整项」，出单时一并计入。</div><div id="tb"></div>`;
        ui.table(ui.$('#tb', b), {
          rowKey: 'operatorId', rows, pageSize: 10,
          empty: { title: '本期还没有订单', text: '有订单产生后这里会实时汇总' },
          onRowClick: r => openSettlement(ctx, { previewOperatorId: r.operatorId, title: `${r.operatorName} · ${r.month} 本期预览` }),
          onAction: (a, r) => openSettlement(ctx, { previewOperatorId: r.operatorId, title: `${r.operatorName} · ${r.month} 本期预览` }),
          columns: [
            { key: 'operatorName', title: '运营商', render: r => esc(r.operatorName) },
            { key: 'month', title: '结算月' },
            { key: 'orders', title: '订单数', align: 'right', render: r => ui.number(r.totals.orderCount) },
            { key: 'receivable', title: '订单应收', align: 'right', render: r => money(r.totals.receivableSum) },
            { key: 'fee', title: '明细服务费', align: 'right', render: r => fee2(r.totals.detailFee) },
            { key: 'adjn', title: '待进单调整项', align: 'right', render: r => (r.adjustments.length ? `${r.adjustments.length} 项 · ${fee2(r.totals.adjustmentFee, { sign: true })}` : '无') },
            { key: 'payable', title: '预计应付', align: 'right', render: r => `<b>${fee2(r.totals.payableFee)}</b>` },
            { key: 'status', title: '状态', render: () => chip('未出单', 'mute') },
            { key: 'act', title: '', align: 'right', width: '84px', render: () => '<button type="button" class="a-btn a-btn-text is-sm" data-act="open">查看</button>' },
          ],
        });
      });
    }

    /* ---------- 手动生成 ---------- */
    genBtn.addEventListener('click', async e => {
      const btn = e.currentTarget;
      const { labelMonth } = await nowInfo();
      const opts = [1, 2, 3].map(k => monthAdd(labelMonth, -k));
      const v = await askForm({
        title: '生成结算单',
        intro: '系统每月 1 日 00:30 自动出上一结算月的结算单；这里用于手动补出。已经生成过的月份不会重复生成。',
        fields: [{ name: 'month', label: '结算月', type: 'select', options: opts.map((m, i) => ({ v: m, t: i === 0 ? `${m}（上月）` : m })), value: opts[0], required: true }],
        rules: { month: { label: '结算月', required: true } }, nextText: '下一步',
      });
      if (!v) return;
      let created = null, existed = false;
      const r = await ui.confirm({
        title: `生成 ${v.month} 结算单`, confirmText: '确认生成', requireReason: false,
        message: '<div>将为本结算月有订单的运营商各生成一张结算单，状态为「待审核」；订单按结束时刻归月，服务费按品牌比例逐单计算。</div>',
        impact: `${v.month} 结算月，全部运营商`,
        onConfirm: async () => {
          try { created = await FIN().generate(v.month); }
          catch (err) { if (err.code === 'ALREADY_GENERATED') { existed = true; return; } throw err; }
        },
      });
      if (!r) return;
      if (existed) {
        ui.toast(`${v.month} 的结算单已经生成过了`, { type: 'warn', hint: '已为你筛选到该月，可以直接查看' });
        st.month = v.month; st.tab = 'list'; ui.tabs(ui.$('#tabs', el), [{ key: 'list', label: '结算单' }, { key: 'preview', label: '本期预览' }], 'list', k => { st.tab = k; drawTab(); }); drawTab();
        return;
      }
      if (!created.length) { ui.toast(`${v.month} 没有可结算的订单`, { type: 'warn', hint: '该月没有已结束的订单，未生成结算单' }); return; }
      ui.toast(`已生成 ${created.length} 张 ${v.month} 结算单`, { type: 'ok', hint: '状态为待审核，请逐张核对后审核' });
      st.month = v.month; st.status = ''; st.tab = 'list';
      ui.tabs(ui.$('#tabs', el), [{ key: 'list', label: '结算单' }, { key: 'preview', label: '本期预览' }], 'list', k => { st.tab = k; drawTab(); });
      drawTab();
    });

    ctx.watch(['settlement.changed', 'dispute.changed', 'settlements.changed'], () => { if (!document.querySelector('.a-drawer')) reloadPane(true); });
    drawTab();
  },
});

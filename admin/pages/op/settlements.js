// 结算单（R-23、R-24、BR-16～BR-20）：月度结算单与逐单明细、各品牌小计、调整项与事由、下载；对时长与里程的异议反馈。
import { api, ui, esc, op, injectStyle, getMe, can, dt, dts, chip, chipRaw, fee2, monthLabel, setHead, btn, busy, fail, errText, kv, item, watch, saveFile, lacks } from './common.js';

const AGENT = { AC0902: '对账智能体' };
const STATUS_TEXT = {
  待核实: '已提交，等待平台公司财务核实',
  已采纳: '财务已采纳，差额计入下一期结算单的调整项',
  已驳回: '财务核实后未采纳，原结算保持不变',
  无需调整: '财务核实后无需调整，原结算保持不变',
};
const sign = (v, f = fee2) => (Number(v) > 0 ? `+${f(v)}` : f(v));

export default {
  path: '#/op/settlements', title: '结算单', menu: '结算单', perm: 'settlement.view.own',
  async mount(el, query, pctx) {
    injectStyle();
    el.innerHTML = '';
    let me;
    try { me = await getMe(); }
    catch (e) { el.append(ui.retry({ text: `${errText(e)}，页面没有加载出来`, onRetry: () => this.mount(el, query, pctx) })); return; }
    const S = op().settlements, D = op().disputes;
    const canDispute = can(me, 'dispute.submit.own'), canExport = can(me, 'settlement.export.own');
    const state = { tab: 'list', disputes: null };

    const bNew = btn('提交异议反馈', 'default', () => openPicker());
    if (!canDispute) lacks(bNew, '当前账号没有提交异议的权限', '请联系账号管理员调整授权');
    else if (me.readonly) ui.setDisabled(bNew, '运营商已退出，账号为只读', '如有疑问，请联系主管部门');
    setHead(el, pctx, '结算单', '每月 1 日出上月结算单，6 日（含）前到账；价格由主管部门统一制定，不受理异议，时长与里程可提出异议', [bNew]);

    const tabsBox = ui.el('<div></div>'); const view = ui.el('<div class="op-stack"></div>');
    el.append(tabsBox, view);
    const drawTabs = () => ui.tabs(tabsBox, [{ key: 'list', label: '结算单' }, { key: 'disputes', label: '异议反馈', count: state.disputes ? state.disputes.length : undefined }], state.tab, k => { state.tab = k; render(); });
    drawTabs();

    const refreshDisputes = async () => { try { state.disputes = await D.list(); drawTabs(); } catch { /* 数量取不到时标签不显示数字 */ } };
    const render = () => (state.tab === 'list' ? renderList() : renderDisputes());
    const off = watch(el, ['settlement.changed', 'dispute.changed'], async () => { await refreshDisputes(); render(); });
    pctx && pctx.onUnmount && pctx.onUnmount(off);

    /* ================= 结算单列表 ================= */
    function renderList() {
      view.innerHTML = '';
      const prev = ui.el('<div></div>'); const box = ui.el('<div></div>');
      view.append(prev, box);
      drawPreview(prev);
      ui.table(box, {
        rowKey: 'settlementId', pageSize: 10, pageSizes: [10, 20],
        columns: [
          { key: 'month', title: '结算月', render: r => `<span class="op-strong">${esc(monthLabel(r.month))}</span>` },
          { key: 'settlementId', title: '结算单号', render: r => `<span class="op-mono">${esc(r.settlementId)}</span>` },
          { key: 'orders', title: '订单数', align: 'right', render: r => ui.number(r.totals.orderCount) },
          { key: 'rec', title: '订单应收', align: 'right', render: r => ui.money(r.totals.receivableSum) },
          { key: 'df', title: '明细服务费', align: 'right', render: r => fee2(r.totals.detailFee) },
          { key: 'af', title: '调整项', align: 'right', render: r => (r.totals.adjustmentFee ? `<span style="color:${r.totals.adjustmentFee < 0 ? 'var(--err)' : 'var(--ok)'}">${sign(r.totals.adjustmentFee)}</span>` : '—') },
          { key: 'pf', title: '应付服务费', align: 'right', render: r => `<b>${fee2(r.totals.payableFee)}</b>` },
          { key: 'status', title: '状态', render: r => `${chip(r.status, 'settlement')}${r.overdue ? ' ' + chipRaw('已过到账期限', 'err') : ''}` },
          { key: 'dueDate', title: '到账期限', render: r => esc(r.dueDate || '—') },
          { key: 'act', title: '操作', align: 'right', render: r => `<span class="a-row-act"><button type="button" class="a-btn a-btn-text is-sm" data-act="view">查看明细</button><button type="button" class="a-btn a-btn-text is-sm" data-act="dl" ${canExport ? '' : 'aria-disabled="true" data-why="当前账号没有下载结算单的权限" data-next="请联系账号管理员调整授权"'}>下载</button></span>` },
        ],
        load: async ({ page, pageSize }) => { const all = await S.list(); return { rows: all.slice((page - 1) * pageSize, page * pageSize), total: all.length }; },
        onRowClick: r => openDetail(r.settlementId),
        onAction: (a, r, b) => (a === 'view' ? openDetail(r.settlementId) : download(r.settlementId, b)),
        empty: { title: '还没有结算单', text: '每月 1 日 00:30 出上月结算单，出单后可在这里查看' },
        summary: '共 {total} 张结算单',
      });
    }
    async function drawPreview(host) {
      host.innerHTML = '<div class="a-panel"><div class="a-panel-body">' + ui.skeleton('lines', 2) + '</div></div>';
      try {
        const c = await S.current();
        const p = ui.el(`<section class="a-panel"><div class="a-panel-head"><h3>本期预览 · ${esc(monthLabel(c.month))}</h3><span>${chip('未出单', 'settlement')}</span></div><div class="a-panel-body op-stack">
          <div class="op-kvgrid"><div><div class="k">已入账订单</div><div class="v">${ui.number(c.totals.orderCount)} 笔</div></div><div><div class="k">订单应收</div><div class="v">${ui.money(c.totals.receivableSum)}</div></div><div><div class="k">预计明细服务费</div><div class="v">${fee2(c.totals.detailFee)}</div></div><div><div class="k">待进单调整项</div><div class="v">${sign(c.totals.adjustmentFee)}</div></div></div>
          <div class="op-row"><span class="op-note" style="flex:1">${esc(c.previewNote || '本期尚未出单，数据随新订单、退款与补报实时变化。')}</span><button type="button" class="a-btn a-btn-default is-sm" data-prev>查看预览明细</button></div></div></section>`);
        ui.$('[data-prev]', p).addEventListener('click', () => openDetail(null, c));
        host.innerHTML = ''; host.append(p);
      } catch (e) { host.innerHTML = ''; host.append(ui.retry({ text: `本期预览没有加载出来：${errText(e)}`, onRetry: () => drawPreview(host) })); }
    }
    async function download(id, b) {
      if (!canExport) return;
      await busy(b, async () => {
        try { const r = await S.export(id); saveFile(r.fileName, r.csv); ui.toast('结算单已下载', { type: 'ok', hint: r.fileName }); }
        catch (e) { fail(e, '下载失败'); }
      });
    }

    /* ================= 结算单详情 ================= */
    async function openDetail(id, preview, opts = {}) {
      const d = ui.drawer({ title: preview ? `本期预览 · ${monthLabel(preview.month)}` : '结算单详情', sub: id || '尚未出单，仅供查看', body: ui.skeleton('lines', 10), wide: true, footer: preview ? [{ label: '关闭', kind: 'default' }] : [{ label: '下载结算单 CSV', kind: 'default', keepOpen: true, onClick: (L, b) => download(id, b) }, { label: '对订单提出异议', kind: 'primary', keepOpen: true, onClick: () => tbl && openDispute(s, tbl.getSelected().map(r => r.orderId)) }] });
      d.el.classList.add('op-xl');
      let s, tbl;
      if (!preview) {
        const fb = ui.$$('.a-drawer-foot button', d.el);
        if (!canExport) ui.setDisabled(fb[0], '当前账号没有下载结算单的权限', '请联系账号管理员调整授权');
        if (!canDispute) ui.setDisabled(fb[1], '当前账号没有提交异议的权限', '请联系账号管理员调整授权');
        else if (me.readonly) ui.setDisabled(fb[1], '运营商已退出，账号为只读', '如有疑问，请联系主管部门');
      }
      const draw = async () => {
        d.setBody(ui.skeleton('lines', 10));
        try { s = preview || await S.get(id); } catch (e) { d.setBody(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: draw })); return; }
        d.setBody(detailBody(s, !!preview));
        const host = ui.$('#op-dt', d.el);
        const rowsAll = s.details;
        const mk = rows => { tbl = ui.table(host, {
          rowKey: 'orderId', rows, pageSize: 20, selectable: !preview && canDispute && !me.readonly,
          bulkActions: [{ label: '对所选订单提出异议', onClick: sel => openDispute(s, sel.map(r => r.orderId)) }],
          columns: [
            { key: 'orderId', title: '订单号', render: r => `<span class="op-mono">${esc(r.orderId)}</span>` },
            { key: 'brandId', title: '品牌', render: r => esc((s.brands.find(b => b.brandId === r.brandId) || {}).brandName || r.brandId) },
            { key: 'endAt', title: '结束时刻', render: r => dt(r.endAt) },
            { key: 'billedMinutes', title: '计费分钟', align: 'right', render: r => `${r.billedMinutes}` },
            { key: 'distanceKm', title: '里程', align: 'right', render: r => ui.km(r.distanceKm) },
            { key: 'degraded', title: '降级单', render: r => (r.degraded ? chipRaw('降级单', 'warn') : '—') },
            { key: 'receivable', title: '订单应收', align: 'right', render: r => ui.money(r.receivable) },
            { key: 'refundedAtGeneration', title: '出单时已退款', align: 'right', render: r => (r.refundedAtGeneration ? ui.money(r.refundedAtGeneration) : '—') },
            { key: 'base', title: '服务费基数', align: 'right', render: r => ui.money(r.base) },
            { key: 'ratio', title: '比例', align: 'right', render: r => ui.percent(r.ratio, 0) },
            { key: 'fee', title: '服务费', align: 'right', render: r => `<b>${fee2(r.fee)}</b>` },
            { key: 'remark', title: '备注', render: r => esc(r.remark || '') },
          ],
          empty: { title: '没有符合条件的订单', text: preview ? '本期暂无已入账的订单' : '换个品牌或订单号试试' },
          summary: '共 {total} 笔订单',
        }); };
        mk(rowsAll);
        const q = ui.$('#op-dq', d.el), bsel = ui.$('#op-db', d.el);
        const apply = () => { const k = q.value.trim(), b = bsel.value; mk(rowsAll.filter(r => (!k || r.orderId.includes(k)) && (!b || r.brandId === b))); };
        let qt; q.addEventListener('input', () => { clearTimeout(qt); qt = setTimeout(apply, 250); }); bsel.addEventListener('change', apply);
        if (opts.preselect) openDispute(s, opts.preselect);
      };
      draw();
    }

    function detailBody(s, preview) {
      const T = s.totals;
      const brandRows = s.brands.map(b => {
        const ratios = (b.ratioBreakdown || []).map(r => `${ui.percent(r.ratio, 0)}（${esc(r.pricingVersionIds.join('、'))}，${r.orderCount} 单）`).join('<br>') || '—';
        return `<tr><td class="op-strong">${esc(b.brandName)}</td><td class="num">${ui.number(b.orderCount)}</td><td class="num">${ui.money(b.receivableSum)}</td><td class="num">${ui.money(b.baseSum)}</td><td>${ratios}</td><td class="num">${fee2(b.detailFee)}</td><td class="num">${b.adjustmentFee ? sign(b.adjustmentFee) : '—'}</td><td class="num"><b>${fee2(b.detailFee + b.adjustmentFee)}</b></td><td class="num">${fee2(b.monthCardShare)}</td></tr>`;
      }).join('');
      const adjHtml = s.adjustments.length ? s.adjustments.map(a => {
        const brand = (s.brands.find(b => b.brandId === a.brandId) || {}).brandName || a.brandId;
        const by = AGENT[a.createdBy] || '平台公司';
        return `<div class="a-panel" style="margin-bottom:10px"><div class="a-panel-head"><div class="op-row"><b>${esc(a.type)}</b><span class="op-note">${esc(brand)} · ${a.items.length} 单 · <span class="op-mono">${esc(a.adjustmentId)}</span></span></div><b style="color:${a.feeDelta < 0 ? 'var(--err)' : 'var(--ok)'}">${sign(a.feeDelta)}</b></div>
          <div class="a-panel-body op-stack" style="gap:8px"><div><span class="op-note">事由</span><div>${esc(a.reason)}</div></div>
          <div class="op-note">由${esc(by)}生成于 ${dts(a.createdAt)}；${a.confirmedAt ? `财务已确认（${dts(a.confirmedAt)}）` : '待财务确认，确认后随结算单生效'}</div>
          <table class="op-mini"><thead><tr><th>订单号</th><th>结束时刻</th><th class="r" style="text-align:right">原基数</th><th style="text-align:right">新基数</th><th style="text-align:right">比例</th><th style="text-align:right">服务费变化</th></tr></thead><tbody>
          ${a.items.map(i => `<tr><td class="op-mono">${esc(i.orderId)}</td><td>${dt(i.endAt)}</td><td class="num" style="text-align:right">${ui.money(i.previousBase)}</td><td class="num" style="text-align:right">${ui.money(i.newBase)}</td><td style="text-align:right">${ui.percent(i.ratio, 0)}</td><td class="num" style="text-align:right">${sign(i.feeDelta)}</td></tr>`).join('')}</tbody></table></div></div>`;
      }).join('') : '<p class="op-note">本期没有调整项。</p>';
      const brandOpts = s.brands.map(b => `<option value="${esc(b.brandId)}">${esc(b.brandName)}</option>`).join('');
      return ui.el(`<div style="display:contents">
        ${preview ? '<div class="op-banner is-info"><div class="grow">本期尚未出单，以下数据随新订单、退款与补报实时变化，不能作为结算依据；次月 1 日 00:30 出单后以结算单为准。</div></div>' : ''}
        <div class="op-row">${chip(s.status, 'settlement')}${s.overdue ? chipRaw('已过到账期限', 'err') : ''}<span class="op-note">${esc(s.operatorName)} · ${esc(monthLabel(s.month))}</span></div>
        <div class="op-kvgrid"><div><div class="k">订单数</div><div class="v">${ui.number(T.orderCount)}</div></div><div><div class="k">订单应收</div><div class="v">${ui.money(T.receivableSum)}</div></div><div><div class="k">服务费基数</div><div class="v">${ui.money(T.baseSum)}</div></div><div><div class="k">明细服务费</div><div class="v">${fee2(T.detailFee)}</div></div><div><div class="k">调整项</div><div class="v">${T.adjustmentFee ? sign(T.adjustmentFee) : '¥0.00'}</div></div><div style="background:var(--orange-50)"><div class="k">应付服务费</div><div class="v" style="color:var(--orange-700)">${fee2(T.payableFee)}</div></div></div>
        <div class="op-note">应付服务费 = Σ（品牌订单的服务费基数 × 该品牌比例）± 调整项。已退款的部分从服务费基数中扣除，全额退款的订单不计；${esc((s.monthCard && s.monthCard.note) || '')}</div>
        ${preview ? '' : `<div><div class="a-sec-title">流程与到账</div>${kv([['出单时间', dts(s.generatedAt)], ['财务审核', s.reviewedAt ? dts(s.reviewedAt) : '待审核'], ['发送给运营商', s.sentAt ? dts(s.sentAt) : '尚未发送'], ['到账期限', `${esc(s.dueDate)}（含）前`], ['到账', s.paidAt ? `${dts(s.paidAt)}${s.bankTradeNo ? `，银行流水 <span class="op-mono">${esc(s.bankTradeNo)}</span>` : ''}` : '尚未到账'], ['开票', esc(s.invoiceStatus || '到账后开具')]])}</div>`}
        <div><div class="a-sec-title">各品牌服务费小计</div><div style="overflow:auto"><table class="op-mini"><thead><tr><th>品牌</th><th style="text-align:right">订单数</th><th style="text-align:right">订单应收</th><th style="text-align:right">服务费基数</th><th>比例（价格版本）</th><th style="text-align:right">明细服务费</th><th style="text-align:right">调整项</th><th style="text-align:right">小计</th><th style="text-align:right">月卡收入分摊</th></tr></thead><tbody>${brandRows}
          <tr style="background:var(--surface-2)"><td><b>合计</b></td><td class="num">${ui.number(T.orderCount)}</td><td class="num">${ui.money(T.receivableSum)}</td><td class="num">${ui.money(T.baseSum)}</td><td></td><td class="num">${fee2(T.detailFee)}</td><td class="num">${T.adjustmentFee ? sign(T.adjustmentFee) : '—'}</td><td class="num"><b>${fee2(T.payableFee)}</b></td><td></td></tr></tbody></table></div><p class="op-note">月卡收入分摊仅用于说明收入归属，不计入服务费。</p></div>
        <div><div class="a-sec-title">调整项与事由</div>${adjHtml}</div>
        <div><div class="a-sec-title">逐单明细</div>
          <div class="a-filter" style="margin-bottom:8px"><div class="f grow"><label for="op-dq">订单号</label><input id="op-dq" class="a-input" style="width:100%" placeholder="输入订单号查找"></div><div class="f"><label for="op-db">品牌</label><select id="op-db" class="a-select"><option value="">全部品牌</option>${brandOpts}</select></div></div>
          ${preview ? '' : '<div class="op-note" style="margin-bottom:6px">勾选订单后，可对其时长或里程提出异议。</div>'}
          <div id="op-dt"></div></div></div>`);
    }

    /* ================= 异议反馈 ================= */
    async function renderDisputes() {
      view.innerHTML = '';
      const sel = ui.el('<div class="a-filter"><div class="f"><label for="op-ds">处理状态</label><select id="op-ds" class="a-select"><option value="">全部</option><option>待核实</option><option>已采纳</option><option>已驳回</option><option>无需调整</option></select></div><div class="f grow"><span class="op-note">对时长、里程有异议时提交；平台公司财务核实后，采纳的差额计入下一期结算单的调整项。价格不受理异议。</span></div></div>');
      const box = ui.el('<div></div>');
      view.append(sel, box);
      let list = [];
      const t = ui.table(box, {
        rowKey: 'disputeId', pageSize: 10, pageSizes: [10, 20],
        columns: [
          { key: 'disputeId', title: '异议编号', render: r => `<span class="op-mono">${esc(r.disputeId)}</span>` },
          { key: 'settlementId', title: '结算单', render: r => `<span class="op-mono">${esc(r.settlementId)}</span>` },
          { key: 'field', title: '类型', render: r => esc(r.field) },
          { key: 'orders', title: '涉及订单', render: r => `${r.orderIds.length} 单（<span class="op-mono">${esc(r.orderIds[0])}</span>${r.orderIds.length > 1 ? ' 等' : ''}）` },
          { key: 'status', title: '处理状态', render: r => chip(r.status, 'dispute') },
          { key: 'submittedAt', title: '提交时间', render: r => dt(r.submittedAt) },
          { key: 'decidedAt', title: '处理时间', render: r => dt(r.decidedAt) },
        ],
        load: async ({ page, pageSize }) => {
          list = await D.list(); state.disputes = list; drawTabs();
          const f = ui.$('#op-ds', sel).value; const rows = f ? list.filter(x => x.status === f) : list;
          return { rows: rows.slice((page - 1) * pageSize, page * pageSize), total: rows.length };
        },
        onRowClick: r => openDispute_detail(r),
        empty: { title: '还没有提交过异议', text: '在结算单明细里勾选订单，或点右上角「提交异议反馈」', action: canDispute && !me.readonly ? '提交异议反馈' : undefined, onAction: () => openPicker() },
        summary: '共 {total} 条异议',
      });
      ui.$('#op-ds', sel).addEventListener('change', () => t.goto(1));
    }
    function openDispute_detail(r) {
      const tl = [[r.submittedAt, '运营商提交异议', `${esc(r.submittedByName || '')}：${esc(r.operatorClaim)}`]];
      if (r.agentComparison) tl.push([r.submittedAt, '对账智能体比对订单事实', esc(r.platformFact || '')]);
      if (r.decidedAt) tl.push([r.decidedAt, `财务${r.status === '已采纳' ? '采纳' : r.status === '已驳回' ? '驳回' : '无需调整'}`, `${esc(r.decidedByName || '')}${r.adjustmentId ? `；已计入调整项 <span class="op-mono">${esc(r.adjustmentId)}</span>` : ''}`]);
      const body = ui.el(`<div style="display:contents">
        <div class="op-row">${chip(r.status, 'dispute')}<span class="op-note">${esc(STATUS_TEXT[r.status] || '')}</span></div>
        ${kv([['异议编号', `<span class="op-mono">${esc(r.disputeId)}</span>`], ['结算单', `<span class="op-mono">${esc(r.settlementId)}</span>`], ['异议类型', esc(r.field)], ['涉及订单', r.orderIds.map(o => `<span class="op-mono">${esc(o)}</span>`).join('<br>')], ['我方说明', esc(r.operatorClaim)], ['平台记录', esc(r.platformFact || '—')]])}
        ${r.agentComparison ? `<div><div class="a-sec-title"><span class="op-agent">智能体</span>对账智能体比对</div><div class="op-banner is-info"><div class="grow">${esc(r.agentComparison)}${r.agentSuggestion ? `<div style="margin-top:6px"><b>建议：</b>${esc(r.agentSuggestion)}</div>` : ''}<div class="op-note" style="margin-top:6px;color:inherit;opacity:.8">智能体只给出比对结论与建议，是否调整由财务确认。</div></div></div></div>` : ''}
        <div><div class="a-sec-title">处理过程</div><ul class="a-timeline">${tl.map(([at, k, s]) => `<li><div class="t">${dts(at)}</div><b>${k}</b>${s ? `<div class="op-note">${s}</div>` : ''}</li>`).join('')}</ul></div></div>`);
      ui.drawer({ title: r.disputeId, sub: `${r.field}异议 · ${r.status}`, body });
    }

    /* ================= 提交异议 ================= */
    /** 不带结算单进入：先选结算单 */
    async function openPicker() {
      let list;
      try { list = await S.list(); } catch (e) { fail(e, '结算单没有加载出来'); return; }
      if (!list.length) { ui.toast('还没有出单的结算单，暂无可提异议的订单', { type: 'warn' }); return; }
      const form = ui.el(`<form class="a-form">${item('选择结算单', `<select id="pk" name="id" class="a-select">${list.map(s => `<option value="${esc(s.settlementId)}">${esc(monthLabel(s.month))} · ${esc(s.settlementId)}（${esc(s.status)}）</option>`).join('')}</select>`, { id: 'pk', tip: '异议针对已出单结算单中的订单' })}</form>`);
      ui.modal({ title: '提交异议反馈', icon: false, body: form, actions: [{ label: '取消', kind: 'default' }, { label: '下一步', kind: 'primary', keepOpen: true, async onClick(L, b) {
        await busy(b, async () => { try { const s = await S.get(form.elements.id.value); L.close('ok'); openDispute(s, []); } catch (e) { fail(e, '结算单没有加载出来'); } });
      } }] });
    }

    async function openDispute(s, preOrders = []) {
      if (!canDispute) { ui.toast('当前账号没有提交异议的权限', { type: 'warn', hint: '请联系账号管理员调整授权' }); return; }
      if (me.readonly) { ui.toast('运营商已退出，账号为只读', { type: 'warn', hint: '如有疑问，请联系主管部门' }); return; }
      const facts = new Map();
      s.details.forEach(r => facts.set(r.orderId, `${r.billedMinutes} 分钟、${r.distanceKm} 公里`));
      s.adjustments.forEach(a => a.items.forEach(i => { if (!facts.has(i.orderId)) facts.set(i.orderId, '见调整项'); }));
      const picked = new Set(preOrders.filter(x => facts.has(x)));
      const form = ui.el(`<form class="a-form" novalidate>
        ${item('结算单', `<input class="a-input" value="${esc(monthLabel(s.month))} · ${esc(s.settlementId)}" disabled>`)}
        <div class="a-item"><label>异议类型</label><div class="a-radio"><label><input type="radio" name="field" value="时长" checked>时长</label><label><input type="radio" name="field" value="里程">里程</label><label><input type="radio" name="field" value="价格">价格</label></div>
          <div class="a-alert is-info" id="op-price" hidden role="status"><div class="grow"><b>价格不受理异议。</b>单价、调度费、优惠券、月卡与服务费比例都由主管部门统一制定，运营商侧不能调整。如对价格规则有意见，请向主管部门反馈；对订单的时长或里程有异议，请选择"时长"或"里程"。</div></div></div>
        <div class="a-item"><label class="req">有异议的订单</label><div class="op-tag-list" id="op-tags"></div>
          <div class="op-row"><input id="op-add" class="a-input" list="op-ol" placeholder="输入订单号，回车添加" style="flex:1" autocomplete="off"><datalist id="op-ol">${[...facts.keys()].slice(0, 400).map(o => `<option value="${esc(o)}">`).join('')}</datalist><button type="button" class="a-btn a-btn-default" id="op-addb">添加</button></div>
          <span class="tip" id="op-ofact"></span></div>
        ${item('我方的记录与说明', '<textarea id="op-claim" name="claim" class="a-textarea" maxlength="300" placeholder="写明你方记录的时长或里程，以及与平台记录不一致的原因，至少 6 个字"></textarea>', { req: true, id: 'op-claim' })}
        <div class="a-alert is-err" id="op-err" hidden role="alert"></div></form>`);
      const tags = ui.$('#op-tags', form), addIn = ui.$('#op-add', form), tipFact = ui.$('#op-ofact', form);
      const drawTags = () => {
        tags.innerHTML = [...picked].map(o => `<span class="op-tag"><span class="op-mono">${esc(o)}</span><button type="button" data-rm="${esc(o)}" aria-label="移除订单 ${esc(o)}">✕</button></span>`).join('') || '<span class="op-note">还没有选择订单</span>';
        tipFact.textContent = picked.size ? [...picked].slice(0, 3).map(o => `${o}：平台记录 ${facts.get(o)}`).join('；') + (picked.size > 3 ? ` 等 ${picked.size} 单` : '') : '可在结算单明细里勾选订单，或在这里输入订单号；最多 20 单';
        ui.$$('[data-rm]', tags).forEach(b => b.addEventListener('click', () => { picked.delete(b.dataset.rm); drawTags(); }));
      };
      const addOrder = () => {
        const v = addIn.value.trim(); if (!v) return;
        const err = ui.$('#op-err', form);
        if (!facts.has(v)) { err.hidden = false; err.textContent = `订单 ${v} 不在这张结算单里，请核对订单号`; ui.shake(err); return; }
        if (picked.size >= 20) { err.hidden = false; err.textContent = '一次最多对 20 单提出异议，请分次提交'; return; }
        err.hidden = true; picked.add(v); addIn.value = ''; drawTags();
      };
      ui.$('#op-addb', form).addEventListener('click', addOrder);
      addIn.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addOrder(); } });
      drawTags();
      let m;
      const priceBox = ui.$('#op-price', form);
      form.addEventListener('change', e => {
        if (e.target.name !== 'field') return;
        const price = e.target.value === 'price' || e.target.value === '价格';
        priceBox.hidden = !price;
        const b = ui.$$('.a-modal-foot button', m.el)[1];
        price ? ui.setDisabled(b, '价格由主管部门统一制定，不受理异议', '请选择"时长"或"里程"后再提交') : ui.setEnabled(b);
      });
      m = ui.modal({
        title: '提交异议反馈', icon: false, body: form,
        actions: [{ label: '取消', kind: 'default' }, {
          label: '提交异议', kind: 'primary', keepOpen: true,
          async onClick(L, b) {
            const err = ui.$('#op-err', form); err.hidden = true;
            const v = ui.validate(form, { claim: { label: '记录与说明', required: true, min: 6, max: 300 } });
            if (!picked.size) { err.hidden = false; err.textContent = '请至少选择一笔有异议的订单'; ui.shake(err); return; }
            if (!v.ok) return;
            const field = form.elements.field.value;
            await busy(b, async () => {
              try {
                const r = await D.submit({ settlementId: s.settlementId, orderIds: [...picked], field, claim: v.values.claim });
                L.close('ok'); showResult(r);
                await refreshDisputes(); if (state.tab === 'disputes') render();
              } catch (e) {
                err.hidden = false; err.textContent = e.code === 'PRICE_NOT_DISPUTABLE' ? e.message : `${errText(e)}。没有产生任何变更。`; ui.shake(err);
              }
            });
          },
        }],
      });
    }
    function showResult(r) {
      const m = ui.modal({
        title: '异议已提交', icon: '✓',
        body: ui.el(`<div style="display:contents"><p>异议编号 <b class="op-mono">${esc(r.disputeId)}</b>，涉及 ${r.orderIds.length} 单，类型：${esc(r.field)}。当前状态：${chip(r.status, 'dispute')}</p>
          <div class="op-banner is-info"><div class="grow"><div><span class="op-agent">智能体</span><b>对账智能体比对结果</b></div><div style="margin-top:6px">${esc(r.agentComparison || '')}</div>${r.agentSuggestion ? `<div style="margin-top:6px"><b>建议：</b>${esc(r.agentSuggestion)}</div>` : ''}</div></div>
          <p class="op-note">以上是智能体的比对结论，是否调整由平台公司财务核实后确认。采纳的差额会计入下一期结算单的调整项，并写明事由；你可以在"异议反馈"里跟踪处理状态。</p></div>`),
        actions: [{ label: '知道了', kind: 'default' }, { label: '查看处理状态', kind: 'primary', onClick: () => { state.tab = 'disputes'; drawTabs(); render(); } }],
      });
      return m;
    }

    /* 从订单明细跳来的异议 */
    const pre = (() => { try { const v = JSON.parse(sessionStorage.getItem('op.prefill') || 'null'); sessionStorage.removeItem('op.prefill'); return v; } catch { return null; } })();
    render(); refreshDisputes();
    if (pre && pre.settlementId) openDetail(pre.settlementId, null, { preselect: [pre.orderId] });
  },
};

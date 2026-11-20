// 收单对账 #/fin/reconcile：每日渠道到账 ＝ 订单实收 ＋ 月卡款 − 押金抵扣额，差异逐笔人工处理（R-25，S-05，G3 第①条）
import { ui, api, esc, money, chip, dt, mono, page, section, busy, askForm, REASON_RULE, nowInfo, whoName } from './kit.js';

const FIN = () => api.admin.finance.recon;
const KIND = { 平: 'ok', 差异已处理: 'mute', 差异待处理: 'err', 当日未出: 'warn' };
const HINT = {
  渠道未到账: '平台记录收款成功但收单渠道没有这一笔。请向微信支付、支付宝核实：补到账、冲正或确认为渠道故障后，写明处理结果。',
  跨日到账: '23:59 前的支付在次日入账，当日少、次日多，两日合计相符。核对次日到账后写明即可。',
  人工转账退款不经收单渠道: '原路退回失败后由财务银行转账，渠道没有流水。核对人工转账记录与转账凭证后写明即可。',
};
const diffColor = v => (Math.abs(v) > 0.001 ? 'color:var(--err);font-weight:600' : '');

function openRecon(ctx, ref, onChanged) {
  const d = ctx.track(ui.drawer({ title: `${ref.date} 收单对账`, sub: '加载中…', body: ui.skeleton('lines', 8), wide: true }));
  const sub = ui.$('.a-drawer-head .sub', d.el);
  let r = null;

  async function load() {
    d.setBody(ui.skeleton('lines', 8));
    try {
      r = await FIN().get(ref.reconId || ref.date);
      await Promise.all(r.diffItems.map(async it => { if (it.handledBy) it.handledByName = await whoName(it.handledBy); }));
      draw();
    }
    catch (e) { d.setBody(''); d.body.append(ui.retry({ text: `${e.message || '对账记录没有加载出来'}，没有产生任何变更`, onRetry: load })); }
  }

  function draw() {
    const ca = r.channelActual;
    const live = r.status === '当日未出';
    sub.textContent = live ? '当日对账次日 02:10 出具，现为实时计算' : `次日 02:10 出具 · ${r.reconId}`;
    const line = (k, v, o = {}) => `<tr><td${o.b ? ' style="font-weight:600"' : ''}>${k}</td><td class="r num"${o.b ? ' style="font-weight:600"' : ''}>${v}</td><td style="color:var(--ink-3);font-size:12px">${o.n || ''}</td></tr>`;
    d.setBody(`
      <div>${chip(r.status, KIND[r.status] || 'mute')}</div>
      <div><div class="a-sec-title">核对过程</div>
        <div class="a-table-wrap"><div class="a-table-scroll"><table class="a-table"><tbody>
          ${line('订单渠道收款', money(r.orderPayments), { n: '免押扣款、一键支付、欠费补付' })}
          ${line('＋ 押金抵扣', money(r.depositDeducted), { n: '押金专户划入骑行收入户' })}
          ${line('− 原路退款', money(r.refundsOriginal))}
          ${line('− 人工转账退款', money(r.refundsManual))}
          ${line('＝ 订单实收', money(r.orderNetReceived), { b: 1 })}
          ${line('＋ 月卡款', money(r.monthCardIncome))}
          ${line('− 押金抵扣额', money(r.depositDeducted), { n: '押金抵扣不经收单渠道' })}
          ${line('＝ 应到账', money(r.expected), { b: 1 })}
          ${line('收单渠道实到（微信 ' + money(ca.wechat) + '，支付宝 ' + money(ca.alipay) + '）', money(ca.total), { b: 1 })}
          <tr><td style="font-weight:600">差异（渠道实到 − 应到账）</td><td class="r num" style="${diffColor(r.diff)}">${money(r.diff, { sign: true })}</td><td></td></tr>
        </tbody></table></div></div></div>
      <div><div class="a-sec-title">差异项（${r.diffItems.length}）</div>
        ${live && r.diffItems.length ? '<div class="a-alert" style="margin-bottom:8px">当日对账尚未出具，差异暂不能处理，次日 02:10 后再来。</div>' : ''}
        ${r.diffItems.length ? r.diffItems.map((it, i) => `
          <div class="a-panel" style="margin-bottom:8px"><div class="a-panel-body" style="padding:12px 14px;display:grid;gap:6px">
            <div style="display:flex;justify-content:space-between;gap:8px;align-items:center"><div>${chip(it.type, it.status === '待处理' ? 'err' : 'mute')} <b class="num" style="margin-left:6px">${money(it.amount, { sign: true })}</b></div>
              <div>${it.status === '待处理' ? chip('待处理', 'warn') : chip('已处理', 'ok')}${it.status === '待处理' ? (live ? ` <button class="a-btn a-btn-default is-sm" aria-disabled="true" data-why="当日对账次日 02:10 才出具" data-next="明天上午再来处理这一笔">处理</button>` : ` <button class="a-btn a-btn-primary is-sm" data-i="${i}">处理差异</button>`) : ''}</div></div>
            <div style="font-size:12px;color:var(--ink-3)">${it.paymentId ? '流水 ' + esc(it.paymentId) : ''}${it.orderId ? ' · 订单 ' + esc(it.orderId) : ''}</div>
            <div style="font-size:13px;color:var(--ink-2)">${esc(it.note || HINT[it.type] || '')}</div>
            ${it.status === '已处理' ? `<div style="font-size:12px;color:var(--ink-3)">处理人 ${esc(it.handledByName || it.handledBy || '—')}</div>` : ''}
          </div></div>`).join('') : '<div style="font-size:13px;color:var(--ink-3)">当日渠道到账与应到账相符，没有差异。</div>'}
      </div>
      <div><div class="a-sec-title">当日相关流水（${(r.payments || []).length}）</div><div id="pt"></div></div>`);

    ui.table(ui.$('#pt', d.body), {
      rowKey: 'paymentId', rows: r.payments || [], pageSize: 10, pageSizes: [10, 20, 50],
      empty: { title: '当日没有相关流水', text: '' },
      columns: [
        { key: 'paymentId', title: '流水号', render: p => mono(p.paymentId) },
        { key: 'type', title: '类型' },
        { key: 'channel', title: '渠道' },
        { key: 'amount', title: '金额', align: 'right', render: p => money(p.direction === '付' ? -p.amount : p.amount) },
        { key: 'status', title: '状态', render: p => chip(p.status, p.status === '成功' ? 'ok' : p.status === '失败' ? 'err' : 'warn') },
        { key: 'completedAt', title: '完成时间', render: p => esc(dt(p.completedAt)) },
        { key: 'channelSettledDate', title: '渠道到账日', render: p => esc(p.channelSettledDate || '—') },
      ],
    });
    ui.$$('[data-i]', d.body).forEach(b => b.addEventListener('click', () => busy(b, () => handle(+b.dataset.i))));
  }

  async function handle(i) {
    const it = r.diffItems[i];
    const v = await askForm({
      title: '处理对账差异',
      intro: `${esc(r.date)} · ${esc(it.type)} · <b>${money(it.amount, { sign: true })}</b><br><span style="color:var(--ink-3)">${esc(HINT[it.type] || '')}</span>`,
      fields: [{ name: 'note', label: '处理说明', type: 'textarea', required: true, placeholder: '写明核实了什么、结果如何，例如：已向支付宝核实，次日 00:12 入账' }],
      rules: { note: { ...REASON_RULE, label: '处理说明' } },
    });
    if (!v) return;
    const res = await ui.confirm({
      title: '确认处理差异', confirmText: '确认已处理', requireReason: false,
      message: `<div>差异：${esc(it.type)} <b class="num">${money(it.amount, { sign: true })}</b>${it.orderId ? '，订单 ' + esc(it.orderId) : ''}</div><div style="margin-top:6px">处理说明：${esc(v.note)}</div>`,
      impact: `${r.date} 收单对账差异项标记为已处理，处理说明写入操作留痕`,
      onConfirm: () => FIN().handleDiff(r.reconId, i, v.note),
    });
    if (res) { ui.toast('差异已处理', { type: 'ok', hint: '处理说明已写入操作留痕' }); onChanged && onChanged(); await load(); }
  }
  load();
}

export default page({
  path: '#/fin/reconcile', title: '收单对账', menu: '收单对账', perm: 'recon.manage',
  build(ctx) {
    const { el } = ctx;
    const st = { from: '', to: '', status: ctx.params.status || '' };
    ctx.head('每日次日 02:10 出具；核对渠道到账与订单实收、月卡款、押金抵扣额');
    el.innerHTML = '<div id="main"></div>';
    const main = ui.$('#main', el);
    const reload = section(main, async () => {
      const { today } = await nowInfo();
      const pend = await FIN().list({ status: '差异待处理', pageSize: 200 });
      return { today, pend };
    }, ({ today, pend }, b) => {
      let totals = null, table;
      b.innerHTML = `
        <div class="a-panel" style="margin-bottom:12px"><div class="a-panel-body" id="eq" style="padding:12px 16px"></div></div>
        <div class="a-stats" id="stats"></div>
        <div class="a-filter">
          <div class="f"><label for="ff">起始日期</label><input class="a-input" type="date" id="ff" max="${today}" value="${esc(st.from)}"></div>
          <div class="f"><label for="ft">截止日期</label><input class="a-input" type="date" id="ft" max="${today}" value="${esc(st.to)}"></div>
          <div class="f"><label for="fs">状态</label><select class="a-select" id="fs"><option value="">全部状态</option>${Object.keys(KIND).map(k => `<option${st.status === k ? ' selected' : ''}>${k}</option>`).join('')}</select></div>
          <span class="grow"></span><button type="button" class="a-btn a-btn-text" id="rst">清空筛选</button></div>
        <div id="tb"></div>`;
      const paintTotals = () => {
        const t = totals; if (!t) return;
        const gap = Math.round((t.channelActual - t.expected) * 100) / 100;
        ui.$('#eq', b).innerHTML = `<div style="font-size:12px;color:var(--ink-3);margin-bottom:6px">核对公式（所选日期合计）：收单渠道到账合计 ＝ 订单实收合计 ＋ 月卡款 − 押金抵扣额</div>
          <div class="num" style="font-size:15px;display:flex;flex-wrap:wrap;gap:6px 14px;align-items:baseline"><span>渠道到账 <b>${money(t.channelActual)}</b></span><span>对照</span><span>订单实收 <b>${money(t.orderNetReceived)}</b></span><span>＋ 月卡款 <b>${money(t.monthCardIncome)}</b></span><span>− 押金抵扣额 <b>${money(t.depositDeducted)}</b></span><span>＝ 应到账 <b>${money(t.expected)}</b></span><span style="${diffColor(gap)}">差异 ${money(gap, { sign: true })}</span></div>
          <div style="font-size:12px;color:var(--ink-3);margin-top:6px">差异只可能来自三类：人工转账退款不经收单渠道、跨日到账、渠道未到账。其余日期应当相平。</div>`;
        const pendItems = pend.rows.reduce((n, x) => n + x.diffItems.filter(i => i.status === '待处理').length, 0);
        ui.$('#stats', b).innerHTML = `<div class="a-stat"><div class="k">渠道到账合计</div><div class="v num">${money(t.channelActual)}</div></div>
          <div class="a-stat"><div class="k">应到账合计</div><div class="v num">${money(t.expected)}</div></div>
          <div class="a-stat"><div class="k">差异合计</div><div class="v num" style="${diffColor(gap)}">${money(gap, { sign: true })}</div></div>
          <button type="button" class="a-stat${st.status === '差异待处理' ? ' is-on' : ''}" id="pendbtn"><div class="k">待处理差异</div><div class="v num">${pendItems}<small>项</small></div><div class="d${pendItems ? ' down' : ''}">${pend.total} 个日期，点击筛选</div></button>`;
        ui.$('#pendbtn', b).addEventListener('click', () => { ui.$('#fs', b).value = st.status === '差异待处理' ? '' : '差异待处理'; apply(); });
      };
      const apply = () => { st.from = ui.$('#ff', b).value; st.to = ui.$('#ft', b).value; st.status = ui.$('#fs', b).value; table.goto(1); };
      ['#ff', '#ft', '#fs'].forEach(s => ui.$(s, b).addEventListener('change', () => {
        if (ui.$('#ff', b).value && ui.$('#ft', b).value && ui.$('#ff', b).value > ui.$('#ft', b).value) { ui.toast('起始日期晚于截止日期', { type: 'warn', hint: '请调整日期范围' }); return; }
        apply();
      }));
      ui.$('#rst', b).addEventListener('click', () => { ['#ff', '#ft', '#fs'].forEach(s => { ui.$(s, b).value = ''; }); apply(); });
      const open = r => openRecon(ctx, r, () => reload(true));
      table = ui.table(ui.$('#tb', b), {
        rowKey: 'reconId', pageSize: 20, pageSizes: [10, 20, 50], onRowClick: open, onAction: (a, r) => open(r),
        summary: '共 {total} 天',
        empty: { title: '所选日期没有对账记录', text: '调整日期范围或清空筛选', action: '清空筛选', onAction: () => ui.$('#rst', b).click() },
        load: async ({ page, pageSize }) => {
          const res = await FIN().list({ from: st.from || undefined, to: st.to || undefined, status: st.status || undefined, page, pageSize });
          totals = res.totals; paintTotals();
          return { rows: res.rows, total: res.total };
        },
        columns: [
          { key: 'date', title: '日期', render: r => `<b>${esc(r.date)}</b>` },
          { key: 'wechat', title: '微信支付', align: 'right', render: r => money(r.channelActual.wechat) },
          { key: 'alipay', title: '支付宝', align: 'right', render: r => money(r.channelActual.alipay) },
          { key: 'total', title: '渠道到账', align: 'right', render: r => `<b>${money(r.channelActual.total)}</b>` },
          { key: 'net', title: '订单实收', align: 'right', render: r => money(r.orderNetReceived) },
          { key: 'card', title: '月卡款', align: 'right', render: r => money(r.monthCardIncome) },
          { key: 'dep', title: '押金抵扣额', align: 'right', render: r => money(r.depositDeducted) },
          { key: 'exp', title: '应到账', align: 'right', render: r => money(r.expected) },
          { key: 'diff', title: '差异', align: 'right', render: r => `<span style="${diffColor(r.diff)}">${money(r.diff, { sign: true })}</span>` },
          { key: 'status', title: '状态', render: r => chip(r.status, KIND[r.status] || 'mute') },
          { key: 'n', title: '差异项', align: 'right', render: r => (r.diffItems.length ? `${r.diffItems.filter(i => i.status === '待处理').length}/${r.diffItems.length}` : '—') },
          { key: 'act', title: '', align: 'right', width: '84px', render: r => `<button type="button" class="a-btn ${r.status === '差异待处理' ? 'a-btn-default' : 'a-btn-text'} is-sm" data-act="open">${r.status === '差异待处理' ? '去处理' : '查看'}</button>` },
        ],
      });
    });
    ctx.watch(['payments.changed', 'payment.created'], () => { if (!document.querySelector('.a-drawer')) reload(true); });
  },
});

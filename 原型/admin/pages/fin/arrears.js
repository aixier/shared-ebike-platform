// 欠费与退款失败 #/fin/arrears：欠费清单；原路退回失败后的人工转账（R-09、R-11，BR-22～BR-24）
import { ui, api, esc, money, chip, dt, mono, page, section, busy, askForm, REASON_RULE } from './kit.js';

const FIN = () => api.admin.finance;

/* ---------- 欠费详情 ---------- */
function openArrears(ctx, u, freezeDays) {
  const d = ctx.track(ui.drawer({ title: `${u.name || '用户'} 的欠费`, sub: `${u.userId} · ${u.phoneMasked}`, body: ui.skeleton('lines', 6), wide: true }));
  async function load() {
    d.setBody(ui.skeleton('lines', 6));
    try {
      const res = await api.admin.orders.list({ userId: u.userId, pageSize: 100 });
      const rows = res.rows.filter(o => o.fee && Number(o.fee.unpaid) > 0);
      d.setBody(`
        <div class="a-stats" style="margin:0"><div class="a-stat"><div class="k">欠费金额</div><div class="v num">${money(u.arrears)}</div></div><div class="a-stat"><div class="k">欠费订单</div><div class="v num">${u.orderCount}<small>单</small></div></div><div class="a-stat"><div class="k">账龄</div><div class="v num">${u.days}<small>天</small></div></div></div>
        <div class="a-alert ${u.accountStatus === '冻结' ? '' : 'is-info'}">${u.accountStatus === '冻结' ? '账户已冻结。用户补付欠费、满足条件后会自动解冻；财务不直接核销欠费，也不能解冻（人工解冻由客服处理）。' : `账户目前正常。欠费超过 ${freezeDays} 天，系统会自动冻结账户；用户下次扫码时会收到一键补付。`}</div>
        <div><div class="a-sec-title">欠费订单</div><div id="ot"></div></div>`);
      ui.table(ui.$('#ot', d.body), {
        rowKey: 'orderId', rows, pageSize: 10, pageSizes: [10, 20, 50],
        empty: { title: '没有欠费订单', text: '用户可能刚刚补付，刷新后再看' },
        columns: [
          { key: 'orderId', title: '订单号', render: o => mono(o.orderId) },
          { key: 'endAt', title: '结束时间', render: o => esc(dt(o.endAt)) },
          { key: 'brandName', title: '品牌', render: o => esc(o.brandName || '') },
          { key: 'payable', title: '订单应付', align: 'right', render: o => money(o.fee.payable) },
          { key: 'unpaid', title: '未付', align: 'right', render: o => `<b>${money(o.fee.unpaid)}</b>` },
          { key: 'billPushedAt', title: '账单推送', render: o => esc(dt(o.billPushedAt)) },
          { key: 'deg', title: '类型', render: o => (o.degraded ? chip('降级单', 'warn') : chip('正常单', 'mute')) },
        ],
      });
    } catch (e) { d.setBody(''); d.body.append(ui.retry({ text: `${e.message || '欠费订单没有加载出来'}，没有产生任何变更`, onRetry: load })); }
  }
  load();
}

/* ---------- 人工转账 ---------- */
async function transfer(f, onDone) {
  const isDep = f.type === '退押';
  const pe = f.payeeSubmitted;
  const v = await askForm({
    title: '录入人工转账结果',
    intro: `原路退回失败（${esc(f.failReason || '未知原因')}）。按用户本人实名收款账户转账后，在这里录入转账凭证。`,
    fields: [
      { name: 'who', label: '收款人（用户提交，本人实名）', type: 'readonly', html: `<span><b>${esc(pe.name)}</b>　${esc(pe.bank)}　<span class="mono">${esc(pe.account)}</span></span>` },
      { name: 'amt', label: '转账金额', type: 'readonly', html: `<b class="num">${money(f.amount)}</b>　<span style="color:var(--ink-3)">${isDep ? '从押金专户支出' : '从骑行收入户支出'}</span>` },
      { name: 'tradeNo', label: '转账凭证号', required: true, placeholder: '银行转账回单上的流水号，8–32 位字母或数字' },
      { name: 'reason', label: '事由', type: 'textarea', required: true, placeholder: '例如：原支付卡已注销，按用户提供的本人账户转账退回' },
    ],
    rules: {
      tradeNo: { label: '转账凭证号', required: true, pattern: /^[A-Za-z0-9]{8,32}$/, message: '转账凭证号为 8–32 位字母或数字' },
      reason: REASON_RULE,
    },
  });
  if (!v) return;
  const r = await ui.confirm({
    title: '确认已转账', confirmText: '确认录入', danger: true, requireReason: false,
    message: `<div>向 <b>${esc(pe.name)}</b>（${esc(pe.bank)} ${esc(pe.account)}）转账 <b class="num">${money(f.amount)}</b>，凭证号 <span class="mono">${esc(v.tradeNo.toUpperCase())}</span>。</div><div style="margin-top:6px">事由：${esc(v.reason)}</div><div style="margin-top:6px;font-size:12px;color:var(--ink-3)">录入后用户会收到到账通知，对应客服工单标记为已解决；此记录进入操作留痕，不能撤销。</div>`,
    impact: `${isDep ? '押金专户' : '骑行收入户'}支出 ${money(f.amount)}，流水 ${f.paymentId} 完成处理`,
    onConfirm: () => FIN().refunds.transfer(f.paymentId, { tradeNo: v.tradeNo.toUpperCase() }, v.reason),
  });
  if (r) { ui.toast('人工转账已录入', { type: 'ok', hint: '已写入操作留痕并通知用户' }); onDone && onDone(); }
}

export default page({
  path: '#/fin/arrears', title: '欠费与退款失败', menu: '欠费与退款失败', perm: ['arrears.view', 'refund.manualTransfer'],
  build(ctx) {
    const { el } = ctx;
    const st = { tab: ctx.params.tab === 'refund' ? 'refund' : 'arrears', q: '', acct: '', age: '' };
    ctx.head('欠费由用户补付后自动结清；原路退回失败的退款与退押，由财务人工转账');
    el.innerHTML = '<div id="main"></div>';
    const reload = section(ui.$('#main', el), async () => {
      const [ar, fl, prm] = await Promise.all([FIN().arrears.list({ pageSize: 2000 }), FIN().refunds.failures(), api.admin.pricing.params().catch(() => ({}))]);
      return { ar, fl, freezeDays: prm.arrearsFreezeDays || 30 };
    }, ({ ar, fl, freezeDays }, b) => {
      b.innerHTML = '<div id="tabs"></div><div id="pane"></div>';
      const pane = ui.$('#pane', b);
      const tabBar = () => ui.tabs(ui.$('#tabs', b), [{ key: 'arrears', label: '欠费清单', count: ar.totals.users }, { key: 'refund', label: '退款失败', count: fl.length }], st.tab, k => { st.tab = k; draw(); });
      tabBar();
      const draw = () => { pane.innerHTML = ''; st.tab === 'arrears' ? drawArrears() : drawRefund(); };

      function drawArrears() {
        const rows = ar.rows;
        const frozen = rows.filter(r => r.accountStatus === '冻结').length;
        const old = rows.filter(r => r.days >= freezeDays).length;
        pane.innerHTML = `
          <div class="a-stats"><div class="a-stat"><div class="k">欠费用户</div><div class="v num">${ar.totals.users}<small>人</small></div></div>
            <div class="a-stat"><div class="k">欠费总额</div><div class="v num">${money(ar.totals.amount)}</div><div class="d">待用户补付，下次扫码一键补付</div></div>
            <div class="a-stat"><div class="k">账户已冻结</div><div class="v num">${frozen}<small>人</small></div><div class="d">补付后自动解冻</div></div>
            <div class="a-stat"><div class="k">账龄 ≥ ${freezeDays} 天</div><div class="v num">${old}<small>人</small></div><div class="d${old ? ' down' : ''}">超期未付会冻结账户</div></div></div>
          <div class="a-filter">
            <div class="f"><label for="fq">用户</label><input class="a-input a-search" id="fq" placeholder="用户编号或姓名" autocomplete="off" value="${esc(st.q)}"></div>
            <div class="f"><label for="fa">账户状态</label><select class="a-select" id="fa"><option value="">全部</option><option${st.acct === '冻结' ? ' selected' : ''}>冻结</option><option${st.acct === '正常' ? ' selected' : ''}>正常</option></select></div>
            <div class="f"><label for="fg">账龄</label><select class="a-select" id="fg"><option value="">全部</option><option value="7"${st.age === '7' ? ' selected' : ''}>7 天以上</option><option value="${freezeDays}"${st.age === String(freezeDays) ? ' selected' : ''}>${freezeDays} 天以上</option></select></div>
            <span class="grow"></span><button type="button" class="a-btn a-btn-text" id="rst">清空筛选</button></div><div id="tb"></div>`;
        const t = ui.table(ui.$('#tb', pane), {
          rowKey: 'userId', rows, pageSize: 20, pageSizes: [10, 20, 50], sort: { key: 'days', dir: 'desc' },
          empty: { title: '没有符合条件的欠费用户', text: '清空筛选看全部', action: '清空筛选', onAction: () => ui.$('#rst', pane).click() },
          onRowClick: r => openArrears(ctx, r, freezeDays), onAction: (a, r) => openArrears(ctx, r, freezeDays),
          columns: [
            { key: 'user', title: '用户', render: r => `${esc(r.name || '未实名')}　${mono(r.userId)}` },
            { key: 'phoneMasked', title: '手机号', render: r => esc(r.phoneMasked) },
            { key: 'arrears', title: '欠费金额', align: 'right', sortable: true, render: r => `<b>${money(r.arrears)}</b>` },
            { key: 'orderCount', title: '欠费单数', align: 'right', sortable: true, render: r => String(r.orderCount) },
            { key: 'oldestAt', title: '最早欠费', render: r => esc(dt(r.oldestAt)) },
            { key: 'days', title: '账龄', align: 'right', sortable: true, render: r => `<span style="${r.days >= freezeDays ? 'color:var(--err);font-weight:600' : r.days >= 7 ? 'color:var(--warn);font-weight:600' : ''}">${r.days} 天</span>` },
            { key: 'accountStatus', title: '账户', render: r => chip(r.accountStatus === '冻结' ? '已冻结' : r.accountStatus, r.accountStatus === '冻结' ? 'err' : 'mute') },
            { key: 'degradedOrders', title: '降级单', align: 'right', render: r => (r.degradedOrders ? String(r.degradedOrders) : '—') },
            { key: 'act', title: '', align: 'right', width: '84px', render: () => '<button type="button" class="a-btn a-btn-text is-sm" data-act="open">查看订单</button>' },
          ],
        });
        const apply = () => {
          st.q = ui.$('#fq', pane).value.trim(); st.acct = ui.$('#fa', pane).value; st.age = ui.$('#fg', pane).value;
          t.setRows(rows.filter(r => (!st.q || (r.userId + (r.name || '')).includes(st.q)) && (!st.acct || r.accountStatus === st.acct) && (!st.age || r.days >= Number(st.age))));
        };
        ['#fq', '#fa', '#fg'].forEach(s => ui.$(s, pane).addEventListener(s === '#fq' ? 'input' : 'change', apply));
        ui.$('#rst', pane).addEventListener('click', () => { ui.$('#fq', pane).value = ''; ui.$('#fa', pane).value = ''; ui.$('#fg', pane).value = ''; apply(); });
        if (st.q || st.acct || st.age) apply();
      }

      function drawRefund() {
        const waitPayee = fl.filter(f => !f.payeeSubmitted).length, ready = fl.length - waitPayee;
        const total = Math.round(fl.reduce((s, f) => s + f.amount * 100, 0)) / 100;
        pane.innerHTML = `
          <div class="a-stats"><div class="a-stat"><div class="k">待转账</div><div class="v num">${ready}<small>笔</small></div><div class="d">用户已提交本人收款账户</div></div>
            <div class="a-stat"><div class="k">待用户填写收款账户</div><div class="v num">${waitPayee}<small>笔</small></div><div class="d">用户在小程序或客服对话里填写</div></div>
            <div class="a-stat"><div class="k">涉及金额</div><div class="v num">${money(total)}</div></div></div>
          <div class="a-alert is-info" style="margin-bottom:12px">退款一律原路退回；原路失败（如原卡已注销）后，须转入用户本人实名账户。转账完成后在这里录入凭证号与事由。</div><div id="tb"></div>`;
        ui.table(ui.$('#tb', pane), {
          rowKey: 'paymentId', rows: fl, pageSize: 10, pageSizes: [10, 20, 50],
          empty: { title: '没有退款失败的订单', text: '原路退回失败的退款、退押会出现在这里' },
          onAction: (a, f, btn) => busy(btn, () => transfer(f, () => reload(true))),
          columns: [
            { key: 'paymentId', title: '失败流水', render: f => mono(f.paymentId) },
            { key: 'type', title: '类型', render: f => chip(f.type, f.type === '退押' ? 'brand' : 'mute') },
            { key: 'user', title: '用户', render: f => `${esc(f.userName || '—')}　${mono(f.userId)}` },
            { key: 'orderId', title: '订单', render: f => (f.orderId ? mono(f.orderId) : '—') },
            { key: 'amount', title: '金额', align: 'right', render: f => `<b>${money(f.amount)}</b>` },
            { key: 'failReason', title: '失败原因', ellipsis: true, render: f => esc(f.failReason || '—') },
            { key: 'createdAt', title: '发生时间', render: f => esc(dt(f.createdAt)) },
            { key: 'payee', title: '收款账户', render: f => (f.payeeSubmitted ? `${esc(f.payeeSubmitted.name)} · ${esc(f.payeeSubmitted.bank)} ${esc(f.payeeSubmitted.account)}` : '<span style="color:var(--ink-3)">用户尚未提交</span>') },
            { key: 'status', title: '状态', render: f => chip(f.status, f.payeeSubmitted ? 'warn' : 'mute') },
            { key: 'act', title: '', align: 'right', width: '100px', render: f => (f.payeeSubmitted
              ? '<button type="button" class="a-btn a-btn-primary is-sm" data-act="transfer">录入转账</button>'
              : '<button type="button" class="a-btn a-btn-default is-sm" aria-disabled="true" data-why="用户还没有提交本人实名收款账户" data-next="等用户在小程序或客服对话里填写后再转账">录入转账</button>') },
          ],
        });
      }
      draw();
    });
    ctx.watch(['payments.changed', 'payment.created', 'users.changed'], () => { if (!document.querySelector('.a-drawer, .a-modal-wrap')) reload(true); });
  },
});

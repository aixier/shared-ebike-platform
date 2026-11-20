// 押金专户 #/fin/deposit：押金收支与余额，与骑行收入分开（R-18，BR-21～BR-25）
import { ui, api, esc, money, chip, dt, mono, page, section, busy, failToast, download, toCsv, nowInfo } from './kit.js';

const TYPES = ['押金缴纳', '押金补缴', '押金抵扣', '退押', '人工转账退押'];
const IN = new Set(['押金缴纳', '押金补缴']);
const signed = p => (IN.has(p.type) ? p.amount : -p.amount);
const SKIND = { 成功: 'ok', 失败: 'err', 处理中: 'warn' };

export default page({
  path: '#/fin/deposit', title: '押金专户', menu: '押金专户', perm: 'deposit.view',
  build(ctx) {
    const { el } = ctx;
    const st = { type: '', status: '', userId: '', from: '', to: '' };
    const expBtn = ui.el('<button type="button" class="a-btn a-btn-default">导出流水</button>');
    ctx.head('押金归用户所有，单独记账，不计入骑行收入，不用于支付服务费', [expBtn]);
    el.innerHTML = '<div id="sum"></div><div id="flt"></div><div id="tb"></div>';

    /* ---------- 余额与收支 ---------- */
    const reloadSum = section(ui.$('#sum', el), () => api.admin.finance.deposit.summary(), (s, b) => {
      const L = s.ledger;
      const check = Math.abs(L.balance - s.userBalanceSum) < 0.005;
      b.innerHTML = `
        <div class="a-stats" style="grid-template-columns:repeat(auto-fit,minmax(200px,1fr))">
          <div class="a-stat is-on" style="border-color:var(--orange-500);background:var(--orange-50)"><div class="k">押金专户余额</div><div class="v num">${money(L.balance)}</div><div class="d">${check ? '与用户押金余额合计一致' : '与用户押金余额合计 ' + money(s.userBalanceSum) + ' 不一致'}</div></div>
          <div class="a-stat"><div class="k">累计缴纳（含补缴）</div><div class="v num">${money(L.paidIn + L.toppedUp)}</div><div class="d">缴纳 ${money(L.paidIn)} ＋ 补缴 ${money(L.toppedUp)}</div></div>
          <div class="a-stat"><div class="k">累计抵扣欠费</div><div class="v num">${money(L.deducted)}</div><div class="d">仅抵扣本人未付骑行费</div></div>
          <div class="a-stat"><div class="k">累计退还</div><div class="v num">${money(L.refunded + L.manualRefunded)}</div><div class="d">原路 ${money(L.refunded)} ＋ 人工转账 ${money(L.manualRefunded)}</div></div>
          <div class="a-stat"><div class="k">退押处理中</div><div class="v num">${money(L.refundingAmount)}</div><div class="d">48 小时内到账</div></div>
        </div>
        <div class="a-panel" style="margin-bottom:12px"><div class="a-panel-body" style="display:flex;flex-wrap:wrap;gap:12px 32px;align-items:center;padding:12px 16px">
          <div><div style="font-size:12px;color:var(--ink-3)">骑行收入户净收款（与押金分开）</div><div class="num" style="font-size:18px;font-weight:700">${money(s.rideIncomeNet)}</div></div>
          <div class="num" style="font-size:13px;color:var(--ink-2)">缴押用户：${Object.entries(s.users).map(([k, n]) => `${k} <b>${ui.number(n)}</b> 人`).join('　')}</div>
          <div style="font-size:12px;color:var(--ink-3);flex:1;min-width:240px">${esc(s.note)}。押金抵扣是押金专户划入骑行收入户的唯一情形，事后已通知用户。</div></div></div>`;
    });

    /* ---------- 流水 ---------- */
    ui.$('#flt', el).innerHTML = `<div class="a-filter">
      <div class="f"><label for="ft">类型</label><select class="a-select" id="ft"><option value="">全部类型</option>${TYPES.map(t => `<option>${t}</option>`).join('')}</select></div>
      <div class="f"><label for="fs">状态</label><select class="a-select" id="fs"><option value="">全部状态</option><option>成功</option><option>处理中</option><option>失败</option></select></div>
      <div class="f"><label for="fu">用户编号</label><input class="a-input" id="fu" placeholder="如 U100341" autocomplete="off"></div>
      <div class="f"><label for="ff">起始日期</label><input class="a-input" type="date" id="ff"></div>
      <div class="f"><label for="fe">截止日期</label><input class="a-input" type="date" id="fe"></div>
      <span class="grow"></span><button type="button" class="a-btn a-btn-text" id="rst">清空筛选</button></div>`;
    const read = () => { st.type = ui.$('#ft', el).value; st.status = ui.$('#fs', el).value; st.userId = ui.$('#fu', el).value.trim().toUpperCase(); st.from = ui.$('#ff', el).value; st.to = ui.$('#fe', el).value; };
    const filters = () => ({ type: st.type || undefined, status: st.status || undefined, userId: st.userId || undefined, from: st.from || undefined, to: st.to || undefined });
    const table = ui.table(ui.$('#tb', el), {
      rowKey: 'paymentId', pageSize: 20, pageSizes: [10, 20, 50],
      empty: { title: '没有符合条件的押金流水', text: '换个类型或日期范围试试', action: '清空筛选', onAction: () => ui.$('#rst', el).click() },
      load: ({ page, pageSize }) => api.admin.finance.deposit.flows({ ...filters(), page, pageSize }).then(r => ({ rows: r.rows, total: r.total })),
      columns: [
        { key: 'paymentId', title: '流水号', render: p => mono(p.paymentId) },
        { key: 'createdAt', title: '发生时间', render: p => esc(dt(p.createdAt)) },
        { key: 'type', title: '类型', render: p => esc(p.type) },
        { key: 'user', title: '用户', render: p => `${mono(p.userId)} ${esc(p.userName || '')}` },
        { key: 'amount', title: '金额', align: 'right', render: p => `<span style="${IN.has(p.type) ? '' : 'color:var(--ink-2)'}">${money(signed(p), { sign: true })}</span>` },
        { key: 'channel', title: '渠道', render: p => esc(p.channel || '—') },
        { key: 'status', title: '状态', render: p => chip(p.status, SKIND[p.status] || 'mute') },
        { key: 'orderId', title: '关联订单', render: p => (p.orderId ? mono(p.orderId) : '—') },
        { key: 'note', title: '说明', ellipsis: true, render: p => esc(String(p.failReason || p.note || '').replace(/按\s*(?:BR|R)-\d+\s*/g, '按规则')) },
      ],
    });
    const apply = () => { read(); table.goto(1); };
    ['#ft', '#fs', '#ff', '#fe'].forEach(s => ui.$(s, el).addEventListener('change', () => {
      read(); if (st.from && st.to && st.from > st.to) { ui.toast('起始日期晚于截止日期', { type: 'warn', hint: '请调整日期范围' }); return; } table.goto(1);
    }));
    let tm; ui.$('#fu', el).addEventListener('input', () => { clearTimeout(tm); tm = setTimeout(apply, 350); });
    ctx.onCleanup(() => clearTimeout(tm));
    ui.$('#rst', el).addEventListener('click', () => { ['#ft', '#fs', '#fu', '#ff', '#fe'].forEach(s => { ui.$(s, el).value = ''; }); apply(); });

    /* ---------- 导出 ---------- */
    expBtn.addEventListener('click', () => busy(expBtn, async () => {
      try {
        const { today } = await nowInfo();
        const r = await api.admin.finance.deposit.flows({ ...filters(), page: 1, pageSize: 5000 });
        if (!r.rows.length) { ui.toast('当前筛选下没有可导出的流水', { type: 'warn', hint: '调整筛选条件后再导出' }); return; }
        const csv = toCsv(['流水号', '发生时间', '类型', '用户编号', '金额（元）', '渠道', '状态', '关联订单', '说明'], r.rows.map(p => [p.paymentId, p.createdAt, p.type, p.userId, signed(p).toFixed(1), p.channel, p.status, p.orderId, p.failReason || p.note]));
        download(`押金专户流水_${today}.csv`, csv);
        ui.toast(`已导出 ${r.rows.length} 条流水`, { type: 'ok', hint: `押金专户流水_${today}.csv` });
      } catch (e) { failToast(e); }
    }));
    ctx.watch(['payments.changed', 'payment.created', 'users.changed'], () => { reloadSum(true); table.reload(); });
  },
});

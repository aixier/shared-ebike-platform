// 财务首页 #/fin/home：本月结算进度、待处理异议、对账差异、欠费总额
import { ui, api, esc, fee2, money, chip, dt, page, section, nowInfo } from './kit.js';

const FIN = () => api.admin.finance;
const FLOW = [['待审核', 'warn'], ['已审核', 'brand'], ['已发送', 'brand'], ['已到账', 'ok']];

export default page({
  path: '#/fin/home', title: '财务首页', menu: '首页',
  build(ctx) {
    const { el } = ctx;
    ctx.head('需要你处理的事项');
    el.innerHTML = '<div id="main"></div>';
    nowInfo().then(t => ctx.head(`今天 ${t.today}，下面是需要你处理的事项`)).catch(() => {});

    const reload = section(ui.$('#main', el), async () => {
      const parts = await Promise.allSettled([
        FIN().settlements.list({}),
        FIN().disputes.list({ status: '待核实' }),
        FIN().recon.list({ status: '差异待处理', pageSize: 200 }),
        FIN().arrears.list({ pageSize: 1 }),
        FIN().refunds.failures(),
        FIN().deposit.summary(),
      ]);
      if (parts.every(p => p.status === 'rejected')) throw parts[0].reason;
      return parts;
    }, (parts, b) => {
      const [stl, dis, rec, arr, ref, dep] = parts;
      const ok = p => p.status === 'fulfilled';
      const bad = label => `<div class="a-stat" style="flex:1"><div class="k">${label}</div><div style="font-size:13px;color:var(--ink-2);margin:6px 0">没有加载出来</div><button type="button" class="a-btn a-btn-default is-sm" data-retry>重新加载</button></div>`;

      /* 结算进度：取最近一个结算月 */
      let stlCard = bad('本月结算进度'), month = '', stlAll = [];
      if (ok(stl)) {
        stlAll = stl.value;
        month = stlAll.reduce((m, s) => (s.month > m ? s.month : m), '');
        const cur = stlAll.filter(s => s.month === month);
        const n = cur.length, paid = cur.filter(s => s.status === '已到账').length, overdue = cur.filter(s => s.overdue).length;
        const due = cur.length ? cur[0].dueDate : '';
        stlCard = `<button type="button" class="a-stat" data-go="#/fin/settlements?month=${month}" style="width:100%"><div class="k">${esc(month || '本月')} 结算进度</div>
          <div class="v num">${paid}<small>/ ${n} 张已到账</small></div>
          <div style="display:flex;height:6px;border-radius:3px;overflow:hidden;background:var(--line-2);margin:8px 0 4px">${FLOW.map(([k], i) => { const c = cur.filter(s => s.status === k).length; return c ? `<span title="${k} ${c}" style="width:${c / n * 100}%;background:${['var(--warn)', 'var(--orange-500)', 'var(--orange-200)', 'var(--ok)'][i]}"></span>` : ''; }).join('')}</div>
          <div class="d${overdue ? ' down' : ''}">${FLOW.map(([k]) => `${k} ${cur.filter(s => s.status === k).length}`).join(' · ')}${due ? `；到账期限 ${esc(due)}` : ''}${overdue ? `；${overdue} 张已逾期` : ''}</div></button>`;
      }
      const disCard = ok(dis)
        ? `<button type="button" class="a-stat" data-go="#/fin/disputes?status=待核实" style="width:100%"><div class="k">待处理异议</div><div class="v num">${dis.value.length}<small>条</small></div><div class="d${dis.value.length ? ' down' : ''}">${dis.value.length ? '运营商对时长、里程提出，需财务核实' : '没有待核实的异议'}</div></button>` : bad('待处理异议');
      let recItems = [];
      if (ok(rec)) recItems = rec.value.rows.flatMap(r => r.diffItems.filter(i => i.status === '待处理').map(i => ({ ...i, date: r.date })));
      const recCard = ok(rec)
        ? `<button type="button" class="a-stat" data-go="#/fin/reconcile?status=差异待处理" style="width:100%"><div class="k">对账差异待处理</div><div class="v num">${recItems.length}<small>项</small></div><div class="d${recItems.length ? ' down' : ''}">${recItems.length ? `涉及 ${rec.value.total} 个日期，净差异 ${money(recItems.reduce((s, i) => s + i.amount, 0), { sign: true })}` : '渠道到账与订单实收均已相符'}</div></button>` : bad('对账差异待处理');
      const arrCard = ok(arr)
        ? `<button type="button" class="a-stat" data-go="#/fin/arrears" style="width:100%"><div class="k">欠费总额</div><div class="v num">${money(arr.value.totals.amount)}</div><div class="d">${arr.value.totals.users} 位用户${ok(ref) && ref.value.length ? `；另有 ${ref.value.length} 笔退款失败待转账` : ''}</div></button>` : bad('欠费总额');

      /* 待办 */
      const todo = [];
      for (const s of stlAll.filter(x => x.month === month && x.status !== '已到账')) {
        const act = s.status === '待审核' ? '核对调整项后审核' : s.status === '已审核' ? '发送给运营商' : '等待到账，到账后登记';
        todo.push({ w: s.overdue ? 0 : 2, href: `#/fin/settlements?month=${month}`, tag: chip(s.overdue ? '已逾期' : s.status, s.overdue ? 'err' : 'warn'), text: `${s.operatorName} ${s.month} 结算单 ${fee2(s.totals.payableFee)}`, note: `${act}；到账期限 ${s.dueDate}` });
      }
      if (ok(dis)) for (const d of dis.value.slice(0, 5)) todo.push({ w: 1, href: '#/fin/disputes?status=待核实', tag: chip('待核实', 'warn'), text: `${d.operatorName} 对 ${d.orderIds.length} 单${d.field}提出异议`, note: `${d.disputeId} · 提交 ${dt(d.submittedAt)}` });
      if (ok(rec)) for (const i of recItems.slice(0, 5)) todo.push({ w: 1, href: '#/fin/reconcile?status=差异待处理', tag: chip('对账差异', 'err'), text: `${i.date} ${i.type} ${money(i.amount, { sign: true })}`, note: i.orderId ? `订单 ${i.orderId}` : '' });
      if (ok(ref)) { const ready = ref.value.filter(f => f.payeeSubmitted); if (ready.length) todo.push({ w: 1, href: '#/fin/arrears?tab=refund', tag: chip('待转账', 'warn'), text: `${ready.length} 笔退款原路退回失败，用户已提交收款账户`, note: `合计 ${money(ready.reduce((s, f) => s + f.amount, 0))}` }); }
      todo.sort((a, c) => a.w - c.w);

      const depBox = ok(dep) ? `<div class="a-panel"><div class="a-panel-head"><h3>押金专户</h3><a href="#/fin/deposit">查看流水</a></div><div class="a-panel-body"><div style="font-size:12px;color:var(--ink-3)">专户余额</div><div class="num" style="font-size:24px;font-weight:700">${money(dep.value.ledger.balance)}</div><div style="font-size:12px;color:var(--ink-3);margin-top:6px">退押处理中 ${money(dep.value.ledger.refundingAmount)}。押金与骑行收入分开记账，不用于付服务费。</div></div></div>` : '';

      b.innerHTML = `<div class="a-stats" style="align-items:stretch">${[stlCard, disCard, recCard, arrCard].map(c => `<div style="display:flex">${c}</div>`).join('')}</div>
        <div style="display:grid;grid-template-columns:minmax(0,2fr) minmax(240px,1fr);gap:16px;align-items:start">
          <div class="a-panel"><div class="a-panel-head"><h3>待办事项</h3><span style="font-size:12px;color:var(--ink-3)">${todo.length ? `共 ${todo.length} 项` : ''}</span></div>
            <div>${todo.length ? todo.map(t => `<a href="${t.href}" style="display:flex;gap:12px;align-items:center;padding:10px 16px;border-bottom:1px solid var(--line-2);color:var(--ink);text-decoration:none"><span style="flex:none">${t.tag}</span><span style="flex:1;min-width:0"><span style="display:block;font-size:13px">${esc(t.text)}</span>${t.note ? `<span style="display:block;font-size:12px;color:var(--ink-3)">${esc(t.note)}</span>` : ''}</span><span style="color:var(--ink-3)">›</span></a>`).join('') : '<div class="a-empty" style="padding:32px"><div class="ic" aria-hidden="true">✓</div><h4>没有待办</h4><p>结算、异议、对账都已处理完</p></div>'}</div></div>
          ${depBox}
        </div>`;
      ui.$$('[data-go]', b).forEach(x => x.addEventListener('click', () => { location.hash = x.dataset.go; }));
      ui.$$('[data-retry]', b).forEach(x => x.addEventListener('click', () => reload()));
    }, { rows: 6 });
    ctx.watch(['settlement.changed', 'dispute.changed', 'payments.changed', 'payment.created', 'settlements.changed'], () => reload(true));
  },
});

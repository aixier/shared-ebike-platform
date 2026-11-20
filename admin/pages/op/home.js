// 首页：本运营商订单、结算与接入状态的关键数字，以及需要处理的事项。内容按账号授权显示。
import { api, ui, esc, op, injectStyle, getMe, can, dt, chip, chipRaw, fee2, monthLabel, panel, btn, errText, watch, nowStr, go, addDays, loadInto, setHead } from './common.js';

export default {
  path: '#/op/home', title: '首页', menu: '首页',
  async mount(el, query, pctx) {
    injectStyle();
    el.innerHTML = '';
    let me;
    try { me = await getMe(true); }
    catch (e) { el.append(ui.retry({ text: `${errText(e)}，页面没有加载出来`, onRetry: () => this.mount(el, query, pctx) })); return; }
    const O = op().orders, S = op().settlements, D = op().disputes, V = op().vehicles, I = op().integration;
    const bRefresh = btn('刷新', 'default', () => build());
    setHead(el, pctx, '首页', `${me.operator ? me.operator.name : ''} · ${me.account.name}（${me.account.role}）`, [bRefresh]);
    const root = ui.el('<div class="op-stack"></div>');
    el.append(root);
    const off = watch(el, ['order.created', 'order.updated', 'settlement.changed', 'dispute.changed', 'integration.changed', 'access.changed', 'vehicle.changed'], () => build(true), 1500);
    pctx && pctx.onUnmount && pctx.onUnmount(off);

    async function build(quiet) {
      if (!quiet) root.innerHTML = '';
      const today = (await nowStr()).slice(0, 10), yday = addDays(today, -1), from14 = addDays(today, -13), from7 = addDays(today, -6);
      const memo = {}; const get = (k, fn) => memo[k] || (memo[k] = fn());
      const P = {
        orders: () => get('orders', () => O.list({ from: from14, to: today, page: 1, pageSize: 1000 })),
        settles: () => get('settles', () => S.list()),
        current: () => get('current', () => S.current()),
        disputes: () => get('disputes', () => D.list()),
        vehicles: () => get('vehicles', () => V.list({ page: 1, pageSize: 1 })),
        integ: () => get('integ', () => I.get()),
      };
      const canOrders = can(me, 'order.view.own'), canSettle = can(me, 'settlement.view.own'), canVeh = can(me, 'vehicle.view.own'), canAcc = can(me, 'access.view.own') || can(me, 'acceptance.view.own');
      const next = ui.el('<div class="op-stack"></div>');
      if (!quiet) root.append(next);
      const blocks = [];
      const block = (title, headRight, loader, render, cls = '') => {
        const p = panel(title, '', headRight || ''); if (cls) p.classList.add(cls);
        const bd = ui.$('.a-panel-body', p); bd.classList.add('op-stack');
        blocks.push(loadInto(bd, loader, render, { rows: 3 }));
        return p;
      };

      /* 需要处理 */
      next.append(block('待办与提示', '', async () => {
        const r = await Promise.allSettled([canOrders && P.orders(), canSettle && P.settles(), canSettle && P.disputes(), canVeh && P.vehicles(), canAcc && P.integ()]);
        return r.map(x => (x.status === 'fulfilled' ? x.value : null));
      }, ([orders, settles, disputes, vehicles, integ], h) => {
        const items = [];
        const pend = (disputes || []).filter(d => d.status === '待核实');
        if (pend.length) items.push([`${pend.length} 条异议等待平台公司财务核实`, '核实结果会计入下一期结算单的调整项', '#/op/settlements', '查看异议']);
        const last = settles && settles[0];
        if (last && last.overdue) items.push([`${monthLabel(last.month)}结算单已过到账期限`, `应付服务费 ${fee2(last.totals.payableFee)}，期限 ${last.dueDate}`, '#/op/settlements', '查看结算单']);
        else if (last && ['待审核', '已审核', '已发送'].includes(last.status)) items.push([`${monthLabel(last.month)}结算单${last.status}`, `应付服务费 ${fee2(last.totals.payableFee)}，${last.dueDate} 前到账；可先核对明细`, '#/op/settlements', '核对明细']);
        (integ || []).forEach(i => {
          if (i.status === '暂停可租') items.push([`${i.brandName} 暂停可租`, '开锁成功率持续低于门槛；修复后系统自动恢复', '#/op/acceptance', '查看原因']);
          if (i.status === '联调中') {
            const run = i.latestRun;
            if (can(me, 'integration.conformance.own')) {
              if (!run) items.push([`${i.brandName} 还没有做符合性测试`, '在联调环境自助发起', '#/op/integration', '去联调']);
              else if (run.summary.failed) items.push([`${i.brandName} 符合性测试有 ${run.summary.failed} 项未通过`, '接入智能体已给出原因与修改建议', '#/op/integration', '查看建议']);
              else if (!i.acceptance || !i.acceptance.reportId) items.push([`${i.brandName} 符合性测试已全部通过`, '下一步：真车联调并提交结果', '#/op/acceptance', '去验收']);
            }
            if (i.acceptance && i.acceptance.result === '待确认') items.push([`${i.brandName} 验收报告等待接入负责人确认`, '确认通过后品牌即上线', '#/op/acceptance', '查看进度']);
          }
        });
        if (vehicles && vehicles.stats.pending) items.push([`${ui.number(vehicles.stats.pending)} 辆车的政府码待启用`, '品牌验收上线后统一启用', '#/op/vehicles', '查看车辆']);
        const dg = orders ? orders.rows.filter(r => r.degraded && r.requestAt.slice(0, 10) >= from7).length : 0;
        if (dg) items.push([`近 7 日有 ${dg} 笔降级单`, '已包含在订单明细中，结算时与普通订单一样处理', '#/op/orders', '查看订单']);
        if (!items.length) { h.append(ui.el('<div class="op-note" style="padding:8px 0">目前没有需要你处理的事项。</div>')); return; }
        h.append(ui.el(`<div>${items.map(([t, s, hash, a]) => `<div class="op-todo"><div class="t"><b>${esc(t)}</b><span class="op-note">${esc(s)}</span></div><button type="button" class="a-btn a-btn-default is-sm" data-go="${hash}">${esc(a)}</button></div>`).join('')}</div>`));
        ui.$$('[data-go]', h).forEach(b => b.addEventListener('click', () => go(b.dataset.go)));
      }));

      /* 订单 */
      if (canOrders) {
        const stats = ui.el('<div class="a-stats"></div>');
        const chart = ui.el('<div></div>');
        blocks.push(loadInto(stats, P.orders, r => {
          const by = {}; r.rows.forEach(x => { const d = x.requestAt.slice(0, 10); (by[d] = by[d] || []).push(x); });
          const cnt = d => (by[d] || []).length;
          const rec = d => (by[d] || []).reduce((s, x) => s + (x.receivable || 0), 0);
          const w = Array.from({ length: 7 }, (_, k) => addDays(today, -k));
          stats.innerHTML = [
            ['今日订单', `${cnt(today)}<small>笔</small>`, `昨日 ${cnt(yday)} 笔`],
            ['昨日订单应收', ui.money(rec(yday)), `${cnt(yday)} 笔订单`],
            ['近 7 日订单', `${w.reduce((s, d) => s + cnt(d), 0)}<small>笔</small>`, `应收 ${ui.money(w.reduce((s, d) => s + rec(d), 0))}`],
            ['近 7 日降级单', `${w.reduce((s, d) => s + (by[d] || []).filter(x => x.degraded).length, 0)}<small>笔</small>`, '平台故障期间开出'],
          ].map(([k, v, d]) => `<div class="a-stat"><div class="k">${k}</div><div class="v">${v}</div><div class="d">${d}</div></div>`).join('');
          const days = Array.from({ length: 14 }, (_, k) => addDays(today, k - 13));
          const mx = Math.max(1, ...days.map(cnt));
          chart.innerHTML = '';
          chart.append(panel('近 14 日订单数', `<div class="op-bars" role="img" aria-label="近 14 日每日订单数">${days.map(d => `<div class="b${d === today ? ' is-today' : ''}" style="height:${Math.max(4, cnt(d) / mx * 100)}%" title="${d}：${cnt(d)} 笔"></div>`).join('')}</div><div class="op-bars-x">${days.map(d => `<span>${d.slice(8)}</span>`).join('')}</div>`, '<button type="button" class="a-btn a-btn-text is-sm" data-go="#/op/orders">查看订单明细</button>'));
          ui.$('[data-go]', chart).addEventListener('click', () => go('#/op/orders'));
        }, { rows: 2 }));
        next.append(stats, chart);
      }

      /* 结算 */
      if (canSettle) {
        next.append(block('结算', '<button type="button" class="a-btn a-btn-text is-sm" data-go="#/op/settlements">查看结算单</button>', async () => {
          const [s, c, d] = await Promise.all([P.settles(), P.current(), P.disputes()]); return { s, c, d };
        }, ({ s, c, d }, h) => {
          const last = s[0];
          h.append(ui.el(`<div class="op-kvgrid">
            <div><div class="k">最新结算单</div><div class="v">${last ? esc(monthLabel(last.month)) : '—'}</div><div style="margin-top:4px">${last ? chip(last.status, 'settlement') : ''}</div></div>
            <div><div class="k">应付服务费</div><div class="v">${last ? fee2(last.totals.payableFee) : '—'}</div><div class="op-note">${last ? `${ui.number(last.totals.orderCount)} 单，调整项 ${last.totals.adjustmentFee ? fee2(last.totals.adjustmentFee) : '无'}` : ''}</div></div>
            <div><div class="k">到账期限</div><div class="v">${last ? esc(last.dueDate.slice(5)) : '—'}</div><div class="op-note">${last ? (last.paidAt ? `已于 ${dt(last.paidAt)} 到账` : last.overdue ? '已过期限' : '期限前到账') : ''}</div></div>
            <div><div class="k">本期预览（${esc(monthLabel(c.month))}）</div><div class="v">${fee2(c.totals.payableFee)}</div><div class="op-note">尚未出单，实时汇总</div></div>
            <div><div class="k">待核实异议</div><div class="v">${d.filter(x => x.status === '待核实').length}<small> 条</small></div><div class="op-note">共提交 ${d.length} 条</div></div></div>`));
        }));
      }

      /* 接入状态与车辆 */
      if (canAcc || canVeh) {
        const grid = ui.el('<div class="op-grid2"></div>');
        if (canAcc) grid.append(block('接入状态', '<button type="button" class="a-btn a-btn-text is-sm" data-go="#/op/acceptance">验收与上线</button>', P.integ, (integ, h) => {
          h.append(ui.el(`<div style="overflow:auto"><table class="op-mini" style="margin:0"><thead><tr><th>品牌</th><th>接入状态</th>${can(me, 'integration.conformance.own') ? '<th>最近测试</th>' : ''}<th>验收</th></tr></thead><tbody>${integ.map(i => `<tr><td class="op-strong">${esc(i.brandName)}</td><td>${chip(i.status, 'access')}</td>${can(me, 'integration.conformance.own') ? `<td>${i.latestRun ? `<span style="color:${i.latestRun.summary.failed ? 'var(--err)' : 'var(--ok)'}">${i.latestRun.summary.passed} / ${i.latestRun.summary.total} 通过</span>` : '尚未测试'}</td>` : ''}<td>${i.acceptance ? chip(i.acceptance.result === '未开始' ? '未开始' : i.acceptance.result, 'result') : '—'}</td></tr>`).join('')}</tbody></table></div>`));
        }));
        if (canVeh) grid.append(block('车辆与政府码', '<button type="button" class="a-btn a-btn-text is-sm" data-go="#/op/vehicles">车辆清单</button>', P.vehicles, (v, h) => {
          const s = v.stats;
          h.append(ui.el(`<div class="op-kvgrid"><div><div class="k">全部车辆</div><div class="v">${ui.number(s.total)}</div></div><div><div class="k">码已启用</div><div class="v" style="color:var(--ok)">${ui.number(s.enabled)}</div></div><div><div class="k">码待启用</div><div class="v">${ui.number(s.pending)}</div></div><div><div class="k">码已失效</div><div class="v">${ui.number(s.invalid)}</div></div></div>`));
        }));
        next.append(grid);
      }
      await Promise.allSettled(blocks);
      ui.$$('[data-go]', next).forEach(b => { if (!b.dataset.bound) { b.dataset.bound = '1'; b.addEventListener('click', () => go(b.dataset.go)); } });
      if (quiet) { root.innerHTML = ''; root.append(next); }
    }
    await build();
  },
};

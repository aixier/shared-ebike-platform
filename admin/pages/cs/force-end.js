// 强制结束订单：无法正常结束的订单，远程结束并锁车；必填事由，二次确认。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $, $$, el, loadCss, query, syncClock, bizNow, toMs, localInput, nowInput, fromInput, chip, fmtDt, waitText, minutesSince, watch, failToast, errText, myPerms, openOrder, openUser } from './lib.js';
import { chatHtml } from './ticket.js';

export default {
  path: '#/cs/force-end', title: '强制结束订单', menu: '强制结束订单', perm: 'order.view', sub: '订单无法正常结束时，核实后远程结束并锁车，按你确认的计费终点计费',
  async mount(root, params, pctx) {
    loadCss();
    root.innerHTML = '';
    const q = query(params);
    const page = el('<div class="cs-page"></div>');
    root.append(page);
    await syncClock();
    let perms = null; try { perms = await myPerms(); } catch { /* 无权限时按钮置灰 */ }
    const canEnd = perms ? perms.has('order.forceEnd') : false;
    let stations = [];
    try { stations = (await api.admin.stations.all()).stations; } catch { /* 站点取不到时只能选“站点外” */ }
    const stName = id => (stations.find(s => s.stationId === id) || {}).name || id || '—';

        const tabsBox = el('<div></div>');
    const filter = el(`<div class="a-filter">
      <div class="f"><label for="fe-q">订单号、车号或用户编号</label><input id="fe-q" class="a-input a-search" placeholder="例如 OD2026120111070" autocomplete="off"></div>
      <div class="f"><label for="fe-s">范围</label><select id="fe-s" class="a-select"><option value="abn">无法结束的订单</option><option value="all">全部骑行中的订单</option></select></div>
      <span class="grow"></span><button type="button" class="a-btn a-btn-default" id="fe-reset">重置</button><button type="button" class="a-btn a-btn-primary" id="fe-go">查询</button></div>`);
    const tbl = el('<div></div>');
    page.append(tabsBox, filter, tbl);

    let tab = 'todo', table = null, hist = null;
    const state = { q: '', scope: 'abn' };

    async function loadRiding() {
      const [os, ts] = await Promise.all([
        api.admin.orders.list({ status: '骑行中', q: state.q || undefined, pageSize: 100 }),
        api.admin.cs.tickets.list({ category: '订单无法结束', status: ['待处理', '处理中', '待主管确认'], pageSize: 100 }),
      ]);
      const byOrder = new Map(ts.rows.map(t => [t.orderId, t]));
      let rows = os.rows.map(o => ({ ...o, ticket: byOrder.get(o.orderId) || null, abnormal: !!(o.anomalyNote || byOrder.has(o.orderId)) }));
      if (state.scope === 'abn') rows = rows.filter(r => r.abnormal);
      rows.sort((a, b) => (b.ticket ? 1 : 0) - (a.ticket ? 1 : 0) || toMs(a.startAt) - toMs(b.startAt));
      return rows;
    }

    function drawTodo() {
      tbl.innerHTML = '';
      let cache = null;
      table = ui.table(tbl, {
        rowKey: 'orderId', pageSize: 10,
        empty: { title: state.scope === 'abn' ? '没有无法结束的订单' : '当前没有骑行中的订单', text: state.scope === 'abn' ? '用户反馈订单结束不了时，会在这里出现；也可以切到“全部骑行中的订单”查找' : '用户开锁后订单会出现在这里' },
        load: async ({ page: pg, pageSize }) => {
          if (pg === 1 || !cache) cache = await loadRiding();
          return { rows: cache.slice((pg - 1) * pageSize, pg * pageSize), total: cache.length };
        },
        columns: [
          { key: 'orderId', title: '订单号', render: r => `<span class="cs-mono">${esc(r.orderId)}</span>` },
          { key: 'plateNo', title: '车号', render: r => `${esc(r.plateNo)} <span class="cs-muted">${esc(r.brandName)}</span>` },
          { key: 'user', title: '用户', render: r => `${esc(r.userName || '—')} <span class="cs-muted">${esc(r.userPhoneMasked || '')}</span>` },
          { key: 'startAt', title: '开锁时间', render: r => fmtDt(r.startAt) },
          { key: 'ride', title: '已计时', align: 'right', render: r => `<span class="num">${waitText(minutesSince(r.startAt))}</span>` },
          { key: 'note', title: '异常说明', ellipsis: true, render: r => (r.abnormal ? `<span title="${esc(r.anomalyNote || '')}">${esc(r.anomalyNote || '用户反馈订单结束不了')}</span>` : '<span class="cs-muted">未发现异常</span>') },
          { key: 'ticket', title: '工单', render: r => (r.ticket ? `<span class="cs-mono">${esc(r.ticket.ticketId)}</span> ${chip(r.ticket.status)}` : '<span class="cs-muted">无</span>') },
          { key: 'op', title: '', render: r => `<div class="a-row-act"><button type="button" class="a-btn a-btn-${r.abnormal ? 'primary' : 'default'} is-sm" data-act="end">处理</button></div>` },
        ],
        onRowClick: r => openEnd(r),
        onAction: (a, r) => openEnd(r),
      });
    }
    function drawHist() {
      tbl.innerHTML = '';
      hist = ui.table(tbl, {
        rowKey: 'ticketId', pageSize: 10,
        empty: { title: '还没有强制结束记录', text: '办理后会记录事由与计费终点' },
        load: async ({ page: pg, pageSize }) => {
          const res = await api.admin.cs.tickets.list({ pageSize: 100 });
          const rows = res.rows.filter(t => t.forceEnd).sort((a, b) => toMs(b.forceEnd.at) - toMs(a.forceEnd.at));
          return { rows: rows.slice((pg - 1) * pageSize, pg * pageSize), total: rows.length };
        },
        columns: [
          { key: 'forceEnd', title: '办理时间', render: r => fmtDt(r.forceEnd.at) },
          { key: 'orderId', title: '订单号', render: r => `<span class="cs-mono">${esc(r.orderId)}</span>` },
          { key: 'ticketId', title: '工单', render: r => `<span class="cs-mono">${esc(r.ticketId)}</span>` },
          { key: 'billedEndAt', title: '计费终点', render: r => fmtDt(r.forceEnd.billedEndAt) },
          { key: 'billed', title: '计费', align: 'right', render: r => (r.order && r.order.billedMinutes != null ? `${r.order.billedMinutes} 分钟 · ${ui.money(r.order.fee ? r.order.fee.payable : 0)}` : '—') },
          { key: 'assigneeName', title: '办理人', render: r => esc(r.assigneeName || '—') },
          { key: 'reason', title: '事由', ellipsis: true, render: r => `<span title="${esc(r.forceEnd.reason)}">${esc(r.forceEnd.reason)}</span>` },
        ],
        onRowClick: r => openOrder(r.orderId),
      });
    }
    function drawTabs() {
      ui.tabs(tabsBox, [{ key: 'todo', label: '待处理订单' }, { key: 'hist', label: '办理记录' }], tab, k => {
        tab = k; filter.hidden = k !== 'todo';
        k === 'todo' ? drawTodo() : drawHist();
      });
    }

    /* ---------- 处理抽屉 ---------- */
    async function openEnd(row) {
      const dr = ui.drawer({ title: `强制结束订单 ${row.orderId}`, sub: '核实后结束订单并远程锁车', body: ui.skeleton('lines', 9), wide: true, footer: [] });
      const foot = $('.a-drawer-foot', dr.el);
      async function render() {
        foot.innerHTML = '';
        dr.setBody(ui.skeleton('lines', 9));
        let o, t = null, pr;
        try {
          o = await api.admin.orders.get(row.orderId);
          const tk = o.tickets && o.tickets.find(x => x.status !== '已解决');
          t = tk ? await api.admin.cs.tickets.get(tk.ticketId) : null;
          pr = await api.admin.pricing.current();
        } catch (e) { dr.setBody(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: render })); return; }
        if (o.status !== '骑行中') {
          dr.setBody(`<div class="a-alert is-ok"><span class="grow">这单已经不在骑行中（当前：${esc(o.status)}），不需要再强制结束。</span></div><div class="cs-sec"><h3>结束情况</h3><div class="cs-box">${o.forceEnd ? `${fmtDt(o.forceEnd.at)} 客服强制结束，按 ${fmtDt(o.forceEnd.billedEndAt)} 计费，事由：${esc(o.forceEnd.reason)}` : `${fmtDt(o.endAt)} 已结束（${esc(o.endMethod || '—')}）`}</div></div>`);
          return;
        }
        const claimed = o.userClaimedEndAt || '';
        const defEnd = claimed || o.startAt;
        const body = el('<div style="display:contents"></div>');
        const agentMsg = t ? [...t.messages].reverse().find(m => m.from === '客服智能体') : null;
        body.innerHTML = `
          <div class="cs-row">${chip('骑行中')} ${t ? chip(t.status) : ''}<span class="cs-muted">${esc(o.userName || '')} ${esc(o.userPhoneMasked || '')} · ${esc(o.plateNo)} · ${esc(o.brandName)}</span><button type="button" class="cs-link cs-right" data-user="${esc(o.userId)}">查看用户</button></div>
          <div class="cs-sec"><h3>订单事实</h3><div class="cs-box"><div class="cs-kv">
            <div><div class="k">开锁时间</div><div class="v">${fmtDt(o.startAt)}</div></div>
            <div><div class="k">已计时</div><div class="v">${waitText(minutesSince(o.startAt))}</div></div>
            <div><div class="k">锁车报告</div><div class="v">${o.lockReportAt ? fmtDt(o.lockReportAt) : '运营商没有上报锁车'}</div></div>
            <div><div class="k">用户自述结束</div><div class="v">${claimed ? `${fmtDt(claimed)}，${esc(stName(o.claimedStationId))}` : '—'}</div></div>
            <div class="full"><div class="k">异常说明</div><div class="v">${esc(o.anomalyNote || '用户反馈订单结束不了')}</div></div></div></div></div>
          ${t ? `<div class="cs-sec"><h3>用户反馈</h3>${chatHtml(t.messages.filter(m => m !== agentMsg))}</div>` : ''}
          <div class="cs-agent"><div class="who">客服智能体提议</div>
            <div class="say">${agentMsg ? esc(agentMsg.text) : '没有收到锁车报告，请核实车辆定位后确定计费终点。'}</div>
            <details class="cs-basis"><summary>依据</summary><ul>
              <li>运营商锁车报告：${o.lockReportAt ? '已收到' : '未收到，补报窗口内也没有补报'}</li>
              <li>用户自述锁车时刻与站点：${claimed ? `${ui.datetime(claimed)}，${esc(stName(o.claimedStationId))}` : '无'}</li>
              <li>计费终点会写入订单，用户端按它重新出账；规则版本 ${esc(o.pricingVersionId)}</li></ul></details></div>
          <form id="fe-form" class="a-form is-cols" novalidate>
            <div class="a-item"><label class="req" for="fe-end">计费终点</label><input id="fe-end" name="end" type="datetime-local" class="a-input" value="${localInput(defEnd)}" min="${localInput(o.startAt)}" max="${nowInput()}"><span class="tip">须在开锁之后、现在之前</span></div>
            <div class="a-item"><label for="fe-st">还车位置</label><select id="fe-st" name="station" class="a-select"><option value="">站点外（按车辆当前位置）</option>${stations.map(s => `<option value="${esc(s.stationId)}" ${s.stationId === o.claimedStationId ? 'selected' : ''}>${esc(s.name)}</option>`).join('')}</select><span class="tip">在站点内不收调度费</span></div>
          </form>
          <div class="cs-box is-soft" id="fe-est"></div>`;
        dr.setBody(body);
        $$('[data-user]', body).forEach(b => b.addEventListener('click', () => openUser(b.dataset.user)));
        const form = $('#fe-form', body);
        const est = $('#fe-est', body);
        const calc = () => {
          const end = fromInput(form.elements.end.value);
          const stId = form.elements.station.value;
          if (!end) { est.innerHTML = '<span class="cs-hint">选择计费终点后显示预估费用</span>'; return null; }
          const sec = (toMs(end) - toMs(o.startAt)) / 1000;
          if (!(sec >= 0)) { est.innerHTML = '<span class="cs-hint">计费终点不能早于开锁时间</span>'; return null; }
          const min = Math.max(1, Math.ceil(sec / 60));
          const r = pr.ride;
          const ride = r.startFee + Math.max(0, min - r.startMinutes) * r.perMinute;
          const disp = stId ? 0 : pr.dispatchFee;
          est.innerHTML = `<div class="cs-kv"><div><div class="k">计费时长</div><div class="v num">${min} 分钟</div></div><div><div class="k">预估应收（未含月卡与优惠券）</div><div class="v num">${ui.money(ride + disp)}</div></div></div><div class="cs-hint" style="margin-top:4px">骑行费 ${ui.money(ride)}${disp ? `，站点外调度费 ${ui.money(disp)}` : ''}；以系统最终账单为准</div>`;
          return { end, stId, min, total: ride + disp };
        };
        form.addEventListener('input', calc);
        calc();
        const btn = el('<button type="button" class="a-btn a-btn-danger">强制结束并锁车</button>');
        if (!canEnd) ui.setDisabled(btn, '你的账号没有强制结束订单的权限', '请换客服岗位处理');
        btn.addEventListener('click', async () => {
          const c = calc();
          const item = form.elements.end.closest('.a-item');
          if (!c || toMs(c.end) > bizNow().getTime() + 1000) {
            item.classList.add('is-error');
            $('.err-msg', item) || item.append(el('<span class="err-msg" role="alert"></span>'));
            $('.err-msg', item).textContent = !c ? '请填写计费终点，且不能早于开锁时间' : '计费终点不能晚于现在';
            form.elements.end.focus(); return;
          }
          item.classList.remove('is-error'); $('.err-msg', item) && $('.err-msg', item).remove();
          const ans = await ui.confirm({
            title: '强制结束并锁车', danger: true, confirmText: '确认强制结束',
            message: `<p>订单 <b>${esc(o.orderId)}</b>（${esc(o.plateNo)}）将按 <b>${ui.datetime(c.end)}</b> 计费 <b>${c.min} 分钟</b>，预估 <b>${ui.money(c.total)}</b>。</p><div class="cs-script">用户将收到：<b>已为你结束订单并锁车，按 ${c.end.slice(11, 16)} 还车计费，共 ${c.min} 分钟。</b></div>`,
            impact: `车辆 ${o.plateNo} 远程锁车；订单结束后用户端立即出账，不可撤销`,
            reasons: ['用户已在站点内锁车，运营商未上报，按用户自述时刻计费', '车辆定位自该时刻起未移动，按停止移动时刻计费'],
            onConfirm: async reason => { await api.admin.cs.forceEnd(o.orderId, { billedEndAt: c.end, stationId: c.stId || null, ticketId: t ? t.ticketId : undefined }, reason); },
          });
          if (!ans) return;
          ui.toast('订单已结束，车辆已锁', { type: 'ok', hint: '用户端已同步出账；事由写入操作留痕' });
          table && table.reload();
          await render();
        });
        foot.append(btn);
      }
      render();
    }

    drawTabs();
    drawTodo();
    $('#fe-go', filter).addEventListener('click', () => { state.q = $('#fe-q', filter).value.trim(); state.scope = $('#fe-s', filter).value; table.goto(1); });
    $('#fe-q', filter).addEventListener('keydown', e => { if (e.key === 'Enter') $('#fe-go', filter).click(); });
    $('#fe-s', filter).addEventListener('change', () => $('#fe-go', filter).click());
    $('#fe-reset', filter).addEventListener('click', () => { $('#fe-q', filter).value = ''; $('#fe-s', filter).value = 'abn'; $('#fe-go', filter).click(); });
    const stop = watch(page, ['order.updated', 'ticket.changed'], () => { (tab === 'todo' ? table : hist) && (tab === 'todo' ? table : hist).reload(); });
    if (q.order) openEnd({ orderId: q.order });
    void failToast;
    return () => stop();
  },
  unmount() {},
};

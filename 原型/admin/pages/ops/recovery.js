// 恢复闭合：恢复后降级单逐单匹配运营商报告，开锁结果、锁车事实、收款结果三项齐全才闭合；缺项的进入人工处理。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $, $$, el, loadCss, query, syncClock, bizNow, toMs, durText, fmtDt, chip, byTag, watch, errText, failToast, myPerms, localInput, nowInput, fromInput, kvRows } from './lib.js';

const ITEMS = [['开锁结果', '开锁结果'], ['锁车事实', '锁车事实'], ['收款结果', '收款结果']];
const threeHtml = c => `<span class="ops-three">${ITEMS.map(([k, label]) => { const miss = (c.missing || []).includes(k); return `<span class="chip ${miss ? 'err' : 'ok'}" title="${esc(label)}${miss ? '缺失' : '已有'}">${miss ? '✕' : '✓'} ${esc(label)}</span>`; }).join('')}</span>`;

export default {
  path: '#/ops/recovery', title: '恢复闭合', menu: '恢复闭合', perm: ['degrade.closure.view', 'degrade.closure.handle'],
  sub: '降级单恢复后逐单匹配运营商报告；开锁结果、锁车事实、收款结果三项齐全才闭合，缺项的在这里人工补齐',
  async mount(root, params) {
    loadCss();
    root.innerHTML = '';
    const q = query(params);
    const page = el('<div class="ops-page"></div>');
    root.append(page);
    await syncClock();
    let perms = null; try { perms = await myPerms(); } catch { /* 无权限时按钮置灰 */ }
    const canHandle = perms ? perms.has('degrade.closure.handle') : false;
    let stations = []; try { stations = (await api.admin.stations.all()).stations; } catch { /* 取不到时只能选站点外 */ }
    const stName = id => (stations.find(s => s.stationId === id) || {}).name || id || '—';
    let events = []; try { events = await api.admin.ops.degrade.events(); } catch { /* 事件取不到时只能看待人工列表 */ }

    const state = { tab: q.event ? 'event' : 'todo', eventId: q.event || (events[0] && events[0].eventId) || '' };
    const sumBox = el('<div></div>'), tabsBox = el('<div></div>');
    const filter = el(`<div class="a-filter"><div class="f"><label for="rc-ev">降级事件</label><select id="rc-ev" class="a-select" style="min-width:320px">${events.map(e => `<option value="${esc(e.eventId)}" ${e.eventId === state.eventId ? 'selected' : ''}>${esc(e.eventId)} · ${fmtDt(e.enteredAt)} · ${e.orderCount} 单</option>`).join('')}</select></div>
      <div class="f"><label for="rc-st">闭合状态</label><select id="rc-st" class="a-select"><option value="">全部</option><option value="待人工">待人工</option><option value="已闭合">已闭合</option></select></div>
      <span class="grow"></span><button type="button" class="a-btn a-btn-primary" id="rc-go">查看</button></div>`);
    const tbl = el('<div></div>');
    page.append(sumBox, tabsBox, filter, tbl);
    let table = null;

    function drawSummary() {
      const ev = events.find(e => e.eventId === state.eventId) || events[0];
      if (!ev) { sumBox.innerHTML = ''; return; }
      const c = ev.closure;
      const pay = Object.entries(c.paymentResults || {}).map(([k, v]) => `${esc(k)} ${v}`).join('，') || '—';
      const nv = Object.entries(c.newUserVerification || {}).map(([k, v]) => `${esc(k)} ${v}`).join('，') || '—';
      const pend = (c.pendingOrderIds || []).length;
      sumBox.innerHTML = `<div class="a-stats" style="margin:0">
        <div class="a-stat"><div class="k">${esc(ev.eventId)} 降级单</div><div class="v num">${ev.orderCount}<small>单</small></div><div class="d">${fmtDt(ev.enteredAt)} 起，${ev.status === '进行中' ? '降级进行中' : `持续 ${durText(ev.durationMinutes)}`}</div></div>
        <div class="a-stat"><div class="k">已闭合</div><div class="v num">${c.closed}<small>单</small></div><div class="d up">三项齐全</div></div>
        <div class="a-stat"><div class="k">待人工处理</div><div class="v num" style="${pend ? 'color:var(--err)' : ''}">${pend}<small>单</small></div><div class="d ${pend ? 'down' : 'up'}">${pend ? '缺项，需要人补齐' : '没有缺项'}</div></div>
        <div class="a-stat" style="grid-column:span 2"><div class="k">收款结果</div><div class="d" style="color:var(--ink);font-size:13px;margin-top:6px">${pay}</div><div class="k" style="margin-top:8px">新用户补验实名</div><div class="d" style="color:var(--ink);font-size:13px">${nv}</div></div></div>`;
    }
    async function refreshEvents() { try { events = await api.admin.ops.degrade.events(); drawSummary(); } catch { /* 汇总刷新失败不影响列表 */ } }

    function build() {
      filter.hidden = state.tab !== 'event';
      table = ui.table(tbl, {
        rowKey: 'orderId', pageSize: 10,
        empty: state.tab === 'todo' ? { title: '没有待人工处理的降级单', text: '降级单三项齐全后自动闭合；缺项的会出现在这里' } : { title: '这个事件没有符合条件的降级单', text: '换个闭合状态看看' },
        load: async ({ page: pg, pageSize, sort }) => {
          const st = $('#rc-st', filter).value;
          let rows = await api.admin.ops.closure.list(state.tab === 'todo' ? {} : { eventId: state.eventId, status: st || undefined });
          if (sort && sort.key) rows = [...rows].sort((a, b) => (sort.dir === 'asc' ? 1 : -1) * ((toMs(a[sort.key] || '1970-01-01 00:00:00')) - toMs(b[sort.key] || '1970-01-01 00:00:00')));
          return { rows: rows.slice((pg - 1) * pageSize, pg * pageSize), total: rows.length };
        },
        columns: [
          { key: 'orderId', title: '订单号', render: r => `<span class="ops-mono">${esc(r.orderId)}</span>` },
          { key: 'plate', title: '车辆', render: r => `${esc(r.plateNo)} <span class="ops-muted">${esc(r.brandName)}</span>` },
          { key: 'user', title: '用户', render: r => `<span class="ops-mono">${esc(r.userId)}</span> <span class="ops-muted">${esc(r.userPhoneMasked)}</span>` },
          { key: 'startAt', title: '开锁时间', sortable: true, render: r => fmtDt(r.startAt) },
          { key: 'admission', title: '准入', render: r => esc(r.admission) },
          { key: 'three', title: '三项齐全', render: r => threeHtml(r.closure) },
          { key: 'status', title: '状态', render: r => chip(r.closure.status, r.closure.status === '已闭合' ? 'ok' : 'warn') },
          { key: 'op', title: '', render: r => `<div class="a-row-act"><button type="button" class="a-btn a-btn-${r.closure.status === '待人工' ? 'primary' : 'default'} is-sm" data-act="h">${r.closure.status === '待人工' ? '处理' : '查看'}</button></div>` },
        ],
        onRowClick: r => openHandle(r),
        onAction: (a, r) => openHandle(r),
      });
    }
    function drawTabs() {
      ui.tabs(tabsBox, [{ key: 'todo', label: '待人工处理' }, { key: 'event', label: '按降级事件查看' }], state.tab, k => { state.tab = k; if (k === 'event') state.eventId = $('#rc-ev', filter).value || state.eventId; drawSummary(); build(); });
    }

    /* ---------- 处理抽屉 ---------- */
    function openHandle(row) {
      const dr = ui.drawer({ title: `降级单 ${row.orderId}`, sub: '逐项核对三项事实，缺项由人补齐', body: ui.skeleton('lines', 8), wide: true, footer: [] });
      const foot = $('.a-drawer-foot', dr.el);
      async function render() {
        foot.innerHTML = ''; foot.hidden = true;
        let o, r2;
        try {
          o = await api.admin.orders.get(row.orderId);
          const list = await api.admin.ops.closure.list({ eventId: row.eventId });
          r2 = list.find(x => x.orderId === row.orderId) || row;
        } catch (e) { dr.setBody(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: render })); return; }
        const c = r2.closure, missing = c.missing || [];
        const needLock = missing.includes('锁车事实'), needPay = missing.includes('收款结果');
        const pend = c.status === '待人工';
        const unknownPay = o.payments.find(p => p.status === '处理中');
        const claimed = o.userClaimedEndAt || '';
        const body = el('<div style="display:contents"></div>');
        let agent = '';
        if (pend) {
          const lines = [];
          if (needLock) lines.push(`锁车事实缺失：运营商在补报窗口（72 小时）内没有补报锁车报告。${claimed ? `用户自述 ${ui.datetime(claimed)} 已在站点内锁车，车辆定位与之一致，建议补录为该时刻。` : '请核对车辆定位后补录锁车时刻。'}`);
          if (needPay) lines.push(unknownPay ? `收款结果未知：渠道扣款 ${ui.money(unknownPay.amount)} 返回处理中，没有明确结果。建议先向渠道核实，再确认“已收款”或“未收款”；未收款会转为用户欠费。` : '收款结果缺失：该单锁车事实补齐并计价后，会按常态方式收款，缺锁车事实时无法收款。');
          agent = `<div class="ops-agent"><div class="who">恢复闭合 · 等你确认</div><div class="say">${lines.map(esc).join('<br>')}</div>
            <details class="ops-basis" open><summary>依据</summary><ul>
              <li>开锁结果：${c.unlockResult ? '运营商已报告成功' : '缺失'}；锁车事实：${c.lockFact ? '已有' : '缺失'}；收款结果：${missing.includes('收款结果') ? '缺失' : c.payment}</li>
              ${o.anomalyNote ? `<li>订单异常说明：${esc(o.anomalyNote)}</li>` : ''}${c.note ? `<li>闭合备注：${esc(c.note)}</li>` : ''}
              <li>准入 ${esc(r2.admission)}，单笔上限 ${r2.limitMinutes} 分钟 / ${ui.money(r2.limitAmount)}；规则按开锁时刻有效版本计价（${esc(o.pricingVersionId)}）</li></ul></details></div>`;
        }
        body.innerHTML = `<div class="ops-row">${chip(c.status, c.status === '已闭合' ? 'ok' : 'warn')} ${chip(o.status, o.status === '待人工处理' ? 'warn' : 'mute')} ${threeHtml(c)}</div>
          ${agent}
          <div class="ops-sec"><h3>订单事实</h3><div class="ops-box">${kvRows([
            ['用户', `${esc(o.userName || '—')} ${esc(o.userPhoneMasked || '')} <span class="ops-mono">${esc(o.userId)}</span>`], ['车辆', `${esc(o.plateNo)} · ${esc(o.brandName)}`],
            ['开锁请求', fmtDt(o.requestAt)], ['开锁成功', fmtDt(o.startAt)], ['结束时间', o.endAt ? `${fmtDt(o.endAt)}（${esc(o.endMethod || '—')}）` : '没有锁车事实'], ['计费', o.billedMinutes != null ? `${o.billedMinutes} 分钟，应收 ${ui.money(o.fee ? o.fee.receivable : 0)}` : '未计价'],
            ['付款', o.payStatus ? `${esc(o.payStatus)}${o.payMethod ? ` · ${esc(o.payMethod)}` : ''}` : '—'], ['降级事件', `<span class="ops-mono">${esc(r2.eventId)}</span>`],
          ])}</div></div>
          ${o.payments.length ? `<div class="ops-sec"><h3>资金流水</h3><div class="a-table-wrap"><div class="a-table-scroll"><table class="a-table"><thead><tr><th>流水号</th><th>类型</th><th class="r">金额</th><th>结果</th><th>时间</th></tr></thead><tbody>${o.payments.map(p => `<tr><td class="ops-mono">${esc(p.paymentId)}</td><td>${esc(p.type)}</td><td class="r num">${ui.money(p.amount)}</td><td>${chip(p.status, p.status === '成功' ? 'ok' : p.status === '失败' ? 'err' : 'warn')}</td><td>${fmtDt(p.createdAt)}</td></tr>`).join('')}</tbody></table></div></div></div>` : ''}
          ${pend && needLock ? `<div class="ops-sec"><h3>补录锁车事实</h3><form id="rc-lock" class="a-form is-cols" novalidate>
            <div class="a-item"><label class="req" for="rc-end">锁车时刻</label><input id="rc-end" name="end" type="datetime-local" class="a-input" value="${localInput(claimed)}" min="${localInput(o.startAt)}" max="${nowInput()}"><span class="tip">须在开锁之后、现在之前；按此时刻计价并收款</span></div>
            <div class="a-item"><label for="rc-st2">锁车位置</label><select id="rc-st2" name="station" class="a-select"><option value="">站点外（按车辆当前位置）</option>${stations.map(s => `<option value="${esc(s.stationId)}" ${s.stationId === o.claimedStationId ? 'selected' : ''}>${esc(s.name)}</option>`).join('')}</select><span class="tip">在站点内不收调度费</span></div></form></div>` : ''}
          ${pend && !needLock && needPay && unknownPay ? `<div class="ops-sec"><h3>确认收款结果</h3><form id="rc-pay" class="a-form" novalidate><div class="a-item"><label class="req">渠道核实结果</label><div class="a-radio"><label><input type="radio" name="result" value="已收款"> 已收款（渠道已扣款 ${ui.money(unknownPay.amount)}）</label><label><input type="radio" name="result" value="未收款"> 未收款（转为用户欠费）</label></div></div></form></div>` : ''}
          ${!pend ? `<div class="a-alert is-ok"><span class="grow">三项事实齐全，已闭合${c.closedAt ? `（${fmtDt(c.closedAt)}）` : ''}。${c.note ? esc(c.note) : ''}</span></div>` : ''}`;
        dr.setBody(body);
        if (pend) {
          foot.hidden = false;
          const btn = el(`<button type="button" class="a-btn a-btn-primary">${needLock ? '补录锁车事实并闭合' : '确认收款结果并闭合'}</button>`);
          if (!canHandle) ui.setDisabled(btn, '只有值班人能处理缺项的降级单', '请联系值班人处理');
          else if (!needLock && !(needPay && unknownPay)) ui.setDisabled(btn, '这单缺的收款结果需要先补齐锁车事实', '先补录锁车事实');
          btn.addEventListener('click', () => (needLock ? doLock() : doPay()));
          foot.append(btn);
        }
        async function doLock() {
          const form = $('#rc-lock', body);
          const end = fromInput(form.elements.end.value), st = form.elements.station.value;
          const item = form.elements.end.closest('.a-item');
          const msg = !end ? '请填写锁车时刻' : toMs(end) < toMs(o.startAt) ? '锁车时刻不能早于开锁时间' : toMs(end) > bizNow().getTime() + 60000 ? '锁车时刻不能晚于现在' : '';
          if (msg) { item.classList.add('is-error'); $('.err-msg', item) || item.append(el('<span class="err-msg" role="alert"></span>')); $('.err-msg', item).textContent = msg; form.elements.end.focus(); return; }
          item.classList.remove('is-error'); $('.err-msg', item) && $('.err-msg', item).remove();
          const ans = await ui.confirm({
            title: '补录锁车事实', danger: true, confirmText: '确认补录并计价收款',
            message: `<p>订单 <b>${esc(o.orderId)}</b> 将按 <b>${ui.datetime(end)}</b> 锁车计价，并按常态方式收款（免押自动扣款，押金用户推送一键支付）。</p><div class="ops-script">用户将收到账单：<b>本次骑行 ${Math.max(1, Math.ceil((toMs(end) - toMs(o.startAt)) / 60000))} 分钟</b>，费用以账单为准。</div>`,
            impact: `降级单 ${o.orderId} 结束并计价；缺的收款结果随之补齐；不可撤销`,
            reasons: ['运营商未补报，按用户自述时刻补录', '车辆定位自该时刻静止，按停止时刻补录'],
            onConfirm: async reason => { await api.admin.ops.closure.handle(o.orderId, { action: '补录锁车事实', endAt: end, stationId: st || undefined }, reason); },
          });
          if (!ans) return;
          ui.toast('已补录锁车事实', { type: 'ok', hint: '订单已计价收款，已写入操作留痕' });
          afterChange(); await render();
        }
        async function doPay() {
          const form = $('#rc-pay', body);
          const val = (form.querySelector('input[name=result]:checked') || {}).value;
          if (!val) { ui.toast('请先选择渠道核实结果', { type: 'warn', hint: '已收款或未收款，二选一' }); return; }
          const ans = await ui.confirm({
            title: `确认收款结果：${val}`, danger: true, confirmText: `确认${val}`,
            message: `<p>订单 <b>${esc(o.orderId)}</b> 的渠道扣款 ${ui.money(unknownPay.amount)} 记为 <b>${val}</b>。</p>${val === '未收款' ? '<div class="a-alert">记为未收款后，这笔费用转为用户欠费，用户下次开锁前需要补付。</div>' : '<div class="ops-script">用户账单状态改为已支付，不会重复扣款。</div>'}`,
            impact: val === '已收款' ? `订单 ${o.orderId} 记为已支付并闭合` : `订单 ${o.orderId} 转为用户欠费并闭合`,
            reasons: ['渠道回执核实：款项已到账', '渠道核实未扣款，转欠费追缴'],
            onConfirm: async reason => { await api.admin.ops.closure.handle(o.orderId, { action: '确认收款结果', result: val }, reason); },
          });
          if (!ans) return;
          ui.toast(`已确认${val}`, { type: 'ok', hint: '降级单已闭合，已写入操作留痕' });
          afterChange(); await render();
        }
      }
      render();
    }
    function afterChange() { table && table.reload(); refreshEvents(); }

    drawSummary(); drawTabs(); build();
    $('#rc-go', filter).addEventListener('click', () => { state.eventId = $('#rc-ev', filter).value; drawSummary(); table.goto(1); });
    $('#rc-ev', filter).addEventListener('change', () => { state.eventId = $('#rc-ev', filter).value; drawSummary(); table.goto(1); });
    $('#rc-st', filter).addEventListener('change', () => table.goto(1));
    const stop = watch(page, ['degrade.changed', 'order.updated'], () => { refreshEvents(); table && table.reload(); });
    void $$; void byTag;
    return () => stop();
  },
  unmount() {},
};

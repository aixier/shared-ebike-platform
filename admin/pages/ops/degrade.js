// 降级与限额：进入与退出降级、降级单量与累计金额、风控限额使用与接近提醒、值班人收紧或停止开新单。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $, $$, el, loadCss, syncClock, toMs, durText, fmtDt, chip, byTag, watch, errText, failToast, myPerms, goto, kvRows } from './lib.js';
import { enterDegrade, exitDegrade, controlDegrade } from './actions.js';

/** 运行智能体对限额使用的判断（固定话术） */
function limitAdvice(s) {
  const u = s.usage, L = s.limits;
  const ratio = L.hourlyDegradedOrders ? u.lastHourOrders / L.hourlyDegradedOrders : 0;
  if (s.stopped) return { tone: 'calm', say: '已停止开新单，已开锁的订单不受影响', basis: [`近 1 小时降级单 ${u.lastHourOrders} / ${L.hourlyDegradedOrders}`, `骑行中 ${u.riding} 单，累计金额 ${ui.money(u.amountSoFar)}`, '故障恢复后退出降级，降级单逐单闭合'] };
  if (ratio >= 1) return { tone: 'warn', kind: 'stop', say: '已达每小时降级单上限，建议停止开新单', basis: [`近 1 小时降级单 ${u.lastHourOrders} 单，已达上限 ${L.hourlyDegradedOrders} 单`, '达到上限后，先骑后验新用户与押金状态未知的老用户已自动暂停降级开单', `累计金额 ${ui.money(u.amountSoFar)}，骑行中 ${u.riding} 单`, '停止开新单不影响已开锁的订单'] };
  if (ratio >= 0.8) return { tone: 'warn', kind: 'tighten', say: '接近每小时降级单上限，建议收紧', basis: [`近 1 小时降级单 ${u.lastHourOrders} / ${L.hourlyDegradedOrders}（${Math.round(ratio * 100)}%）`, '收紧后先骑后验新用户与押金状态未知的老用户暂停开单，老用户照常', `累计金额 ${ui.money(u.amountSoFar)}`] };
  if (s.minutes >= L.degradeDurationAlertMinutes && !s.tightened) return { tone: 'warn', kind: 'tighten', say: `降级已持续 ${durText(s.minutes)}，超过 ${L.degradeDurationAlertMinutes} 分钟告警阈值，建议评估是否收紧`, basis: [`故障原因：${s.cause}`, `近 1 小时降级单 ${u.lastHourOrders} / ${L.hourlyDegradedOrders}，用量尚在限额内`, '若故障短时间内不会恢复，收紧可以控制坏账风险；若即将恢复，可继续观察'] };
  if (u.overLimitRiding > 0) return { tone: 'warn', say: `有 ${u.overLimitRiding} 单已超过单笔上限，已提醒用户尽快还车`, basis: ['超过单笔时长或金额上限时只提醒用户，不会远程结束骑行，超时继续计费', '这些订单恢复后按实际时长计价收款'] };
  return { tone: 'calm', say: '限额用量正常，保持降级开单', basis: [`近 1 小时降级单 ${u.lastHourOrders} / ${L.hourlyDegradedOrders}`, `已持续 ${durText(s.minutes)}（告警阈值 ${L.degradeDurationAlertMinutes} 分钟）`, `累计金额 ${ui.money(u.amountSoFar)}，骑行中 ${u.riding} 单`] };
}

export default {
  path: '#/ops/degrade', title: '降级与限额', menu: '降级与限额', perm: ['ops.monitor', 'degrade.closure.view', 'degrade.control'],
  sub: '故障时用户仍能开单，坏账由限额封顶；进入、退出、收紧、停止都由值班人二次确认并留痕',
  async mount(root) {
    loadCss();
    root.innerHTML = '';
    const page = el('<div class="ops-page"></div>');
    root.append(page);
    await syncClock();
    let perms = null; try { perms = await myPerms(); } catch { /* 无权限时按钮置灰 */ }
    const can = p => perms ? perms.has(p) : false;
    const body = el('<div class="ops-page"></div>');
    page.append(body);
    let historyTable = null;

    const needCtl = btn => { if (!can('degrade.control')) ui.setDisabled(btn, '只有值班人能进入、收紧、停止或退出降级开单', '请联系值班人处理'); return btn; };

    async function draw(silent) {
      if (!silent) body.innerHTML = `<div class="a-panel"><div class="a-panel-body">${ui.skeleton('lines', 5)}</div></div>${ui.skeleton('lines', 6)}`;
      let s, events;
      try { [s, events] = await Promise.all([api.admin.ops.degrade.status(), api.admin.ops.degrade.events()]); }
      catch (e) {
        if (!page.isConnected) return;
        if (silent) { failToast(e, '没有刷新，当前显示的是上次的数据'); return; }
        body.innerHTML = ''; body.append(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: () => draw(false) })); return;
      }
      if (!page.isConnected) return;
      const ev = s.event || events[0] || null;
      const L = s.limits, u = s.usage;
      body.innerHTML = '';

      // ---- 状态与操作 ----
      const tone = s.degraded ? (s.stopped ? 'is-err' : 'is-warn') : 'is-ok';
      const title = s.degraded ? (s.stopped ? '降级开单中 · 已停止开新单' : s.tightened ? '降级开单中 · 已收紧' : '降级开单中') : '正常开单';
      const bn = el(`<div class="ops-banner ${tone}" role="status"><i class="dot" aria-hidden="true"></i><div style="flex:1"><div class="t">${esc(title)}</div>
        <div class="s">${s.degraded ? `${esc(s.cause)} · 故障组件：${s.components.map(esc).join('、')} · 已持续 ${durText(s.minutes)}` : `当前没有降级。最近一次降级：${s.lastDegradeEventId ? esc(s.lastDegradeEventId) : '无'}`}</div></div><div class="ops-row" id="dg-btns"></div></div>`);
      const btns = $('#dg-btns', bn);
      const mk = (label, kind, fn, dis) => { const b = el(`<button type="button" class="a-btn a-btn-${kind}">${label}</button>`); needCtl(b); if (dis && !b.hasAttribute('aria-disabled')) ui.setDisabled(b, dis[0], dis[1]); b.addEventListener('click', async () => { if (await fn()) draw(true); }); btns.append(b); };
      if (!s.degraded) mk('进入降级开单', 'default', () => enterDegrade());
      else {
        mk('收紧', 'default', () => controlDegrade('tighten'), s.tightened || s.stopped ? ['已经收紧或停止', '需要放宽后才能重新收紧'] : null);
        mk('停止开新单', 'danger', () => controlDegrade('stop'), s.stopped ? ['已经停止开新单', '故障恢复后退出降级，或先放宽'] : null);
        mk('放宽', 'default', () => controlDegrade('relax'), !s.tightened && !s.stopped ? ['当前没有收紧或停止', '无需放宽'] : null);
        mk('退出降级', 'primary', async () => { const r = await exitDegrade({ pending: u.orders }); return !!r; });
      }
      body.append(bn);

      // ---- 用户端表现 ----
      const userSide = el(`<div class="a-alert is-info"><span class="grow"><b>用户端当前：</b>${!s.degraded ? '按正常准入开单，没有“费用将稍后收取”提示。' : s.stopped ? '所有用户都不能开新单，看到“平台暂时无法开新单，正在恢复”；骑行中的订单正常还车。' : s.tightened ? '老用户照常开单，只多一行“费用将稍后收取”；先骑后验新用户与押金状态未知的老用户看到“平台临时限制开新单”；已知欠费或退押中的用户不能开单。' : '老用户照常开单，只多一行“费用将稍后收取”；先骑后验新用户与押金状态未知的老用户按紧档额度开单；已知欠费或退押中的用户不能开单。'}</span></div>`);
      body.append(userSide);

      // ---- 用量 ----
      const ratio = L.hourlyDegradedOrders ? u.lastHourOrders / L.hourlyDegradedOrders : 0;
      const durRatio = L.degradeDurationAlertMinutes ? s.minutes / L.degradeDurationAlertMinutes : 0;
      const cls = r => (r >= 1 ? 'is-err' : r >= 0.8 ? 'is-warn' : '');
      const meter = (r, txt) => `<div class="ops-meter ${cls(r)}" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.min(100, Math.round(r * 100))}" aria-label="${esc(txt)}"><i style="width:${Math.min(100, r * 100)}%"></i></div>`;
      const stats = el(`<div class="a-stats" style="margin:0">
        <div class="a-stat"><div class="k">${s.degraded ? '本次降级单量' : '最近一次降级单量'}</div><div class="v num">${s.degraded ? u.orders : (ev ? ev.orderCount : 0)}<small>单</small></div><div class="d">${s.degraded ? `骑行中 ${u.riding} 单` : ev ? `${esc(ev.eventId)}` : '暂无降级记录'}</div></div>
        <div class="a-stat"><div class="k">${s.degraded ? '累计金额（含骑行中预估）' : '最近一次应收合计'}</div><div class="v num">${ui.money(s.degraded ? u.amountSoFar : (ev ? ev.receivableSum : 0))}</div><div class="d">未计月卡与优惠券</div></div>
        <div class="a-stat"><div class="k">近 1 小时降级单</div><div class="v num">${u.lastHourOrders}<small> / ${L.hourlyDegradedOrders}</small></div>${meter(ratio, '近 1 小时降级单占每小时上限的比例')}<div class="d ${ratio >= 1 ? 'down' : ''}">${ratio >= 1 ? '已达上限' : ratio >= 0.8 ? '接近上限' : '在限额内'}</div></div>
        <div class="a-stat"><div class="k">降级持续时长</div><div class="v num">${s.degraded ? s.minutes : 0}<small> 分钟</small></div>${meter(s.degraded ? durRatio : 0, '降级持续时长占告警阈值的比例')}<div class="d ${s.degraded && durRatio >= 1 ? 'down' : ''}">告警阈值 ${L.degradeDurationAlertMinutes} 分钟</div></div>
        <div class="a-stat"><div class="k">已超单笔上限的骑行</div><div class="v num">${u.overLimitRiding}<small>单</small></div><div class="d">只提醒用户还车，不远程结束</div></div></div>`);
      body.append(stats);

      // ---- 运行智能体判断 ----
      if (s.degraded) {
        const a = limitAdvice(s);
        const card = el(`<div class="ops-agent${a.tone === 'calm' ? ' is-calm' : ''}"><div class="who">运行智能体 · ${a.kind ? '等你决定' : '仅供知悉'}</div><div class="say">${esc(a.say)}</div><details class="ops-basis" ${a.kind ? 'open' : ''}><summary>依据</summary><ul>${a.basis.map(x => `<li>${esc(x)}</li>`).join('')}</ul></details>${a.kind ? '<div class="ops-row"></div>' : ''}</div>`);
        if (a.kind) {
          const ok = needCtl(el(`<button type="button" class="a-btn a-btn-primary">${a.kind === 'stop' ? '接受建议，停止开新单' : '接受建议，收紧'}</button>`));
          if (a.kind === 'tighten' && (s.tightened || s.stopped) && !ok.hasAttribute('aria-disabled')) ui.setDisabled(ok, '已经收紧或停止', '无需重复操作');
          ok.addEventListener('click', async () => { if (await controlDegrade(a.kind, { why: `运行智能体依据：${a.basis[0]}` })) draw(true); });
          const no = el('<button type="button" class="a-btn a-btn-default">继续观察</button>');
          no.addEventListener('click', () => { card.remove(); ui.toast('继续观察', { hint: '用量变化后智能体会重新判断' }); });
          $('.ops-row', card).append(ok, no);
        }
        body.append(card);
      }

      // ---- 限额与时间线 ----
      const grid = el('<div class="ops-grid"></div>');
      grid.innerHTML = `<div class="a-panel"><div class="a-panel-head"><h3>风控限额</h3><span class="ops-muted">规则 ${esc(s.limitsVersionId)} · 数值由主管部门录入，这里只读</span></div><div class="a-panel-body">
          <div class="a-table-wrap"><table class="a-table"><thead><tr><th>限额</th><th class="r">老用户</th><th class="r">紧档用户</th></tr></thead><tbody>
            <tr><td>单笔最长骑行</td><td class="r num">${L.normal.maxMinutes} 分钟</td><td class="r num">${L.tight.maxMinutes} 分钟</td></tr>
            <tr><td>单笔最高金额</td><td class="r num">${ui.money(L.normal.maxAmount)}</td><td class="r num">${ui.money(L.tight.maxAmount)}</td></tr>
            <tr><td>每用户未结降级单</td><td class="r num" colspan="2">${L.perUserOpenDegradedOrders} 单</td></tr>
            <tr><td>每小时降级单总量</td><td class="r num" colspan="2">${L.hourlyDegradedOrders} 单</td></tr>
            <tr><td>降级持续告警</td><td class="r num" colspan="2">${L.degradeDurationAlertMinutes} 分钟</td></tr></tbody></table></div>
          <div class="ops-muted" style="margin-top:8px">紧档用户指先骑后验的新用户与押金状态未知的老用户。超过单笔上限只提醒用户尽快还车，超时继续计费，不远程结束骑行。</div></div></div>
        <div class="a-panel"><div class="a-panel-head"><h3>${s.degraded ? '本次降级时间线' : '最近一次降级时间线'}</h3>${ev ? `<span class="ops-muted">${esc(ev.eventId)}</span>` : ''}</div><div class="a-panel-body" id="dg-tl"></div></div>`;
      body.append(grid);
      const tl = $('#dg-tl', grid);
      if (ev) tl.innerHTML = `<ol class="a-timeline" style="list-style:none;margin:0 0 0 6px">${ev.timeline.map(t => `<li><div class="t">${ui.datetime(t.at, true)} ${byTag(t.byKind)} ${esc(t.byName || '')}</div><b>${esc(t.action)}</b>：${esc(t.text)}</li>`).join('')}</ol>`;
      else tl.append(ui.empty({ icon: '🕘', title: '还没有降级记录', text: '进入降级开单后，进入、收紧、停止、退出都会记在这里' }));

      // ---- 历史 ----
      const hist = el('<div class="ops-sec"><h3>降级记录</h3><div id="dg-hist"></div></div>');
      body.append(hist);
      historyTable = ui.table($('#dg-hist', hist), {
        rowKey: 'eventId', pageSize: 5, pageSizes: [5, 10, 20], rows: events,
        empty: { title: '还没有降级记录', text: '进入降级开单后会记录在这里' },
        columns: [
          { key: 'eventId', title: '事件', render: e => `<span class="ops-mono">${esc(e.eventId)}</span>` },
          { key: 'enteredAt', title: '进入时间', sortable: true, render: e => fmtDt(e.enteredAt) },
          { key: 'durationMinutes', title: '持续', align: 'right', render: e => (e.status === '进行中' ? '进行中' : durText(e.durationMinutes)) },
          { key: 'cause', title: '原因', ellipsis: true, render: e => `<span title="${esc(e.cause)}">${esc(e.cause)}</span>` },
          { key: 'orderCount', title: '降级单', align: 'right', sortable: true, render: e => e.orderCount },
          { key: 'receivableSum', title: '应收合计', align: 'right', render: e => ui.money(e.receivableSum) },
          { key: 'closure', title: '闭合', render: e => chip(e.status === '进行中' ? '降级中' : e.closure.status, e.status === '进行中' ? 'warn' : e.closure.status === '已全部闭合' ? 'ok' : 'warn') },
        ],
        onRowClick: e => openEvent(e),
      });
    }

    function openEvent(e) {
      const cl = e.closure;
      const dr = ui.drawer({ title: `降级记录 ${e.eventId}`, sub: e.status === '进行中' ? '降级进行中' : `已退出，持续 ${durText(e.durationMinutes)}`, body: '', footer: [], wide: true });
      const rel = Object.entries(cl.paymentResults || {}).map(([k, v]) => `${esc(k)} ${v}`).join('，') || '—';
      const nv = Object.entries(cl.newUserVerification || {}).map(([k, v]) => `${esc(k)} ${v}`).join('，') || '—';
      const adm = Object.entries(e.admissionCounts || {}).map(([k, v]) => `${esc(k)} ${v}`).join('，') || '—';
      dr.setBody(`<div class="ops-box">${kvRows([['原因', esc(e.cause), true], ['故障组件', e.components.map(esc).join('、')], ['限额规则', `${esc(e.limitsVersionId)}（每小时 ${e.limits.hourlyDegradedOrders} 单，紧档单笔 ${e.limits.tight.maxMinutes} 分钟 / ${ui.money(e.limits.tight.maxAmount)}）`], ['进入', `${fmtDt(e.enteredAt)} ${byTag('智能体')} ${esc(e.enteredByName || '运行智能体')}`], ['退出', e.exitedAt ? fmtDt(e.exitedAt) : '进行中'], ['降级单', `${e.orderCount} 单，应收合计 ${ui.money(e.receivableSum)}`], ['准入分类', adm], ['每小时峰值', `${e.maxHourlyOrders} 单（上限 ${e.limits.hourlyDegradedOrders}）`], ['超限提醒用户还车', `${e.overLimitReminders} 次`], ['收款结果', rel], ['新用户补验实名', nv]])}</div>
        <div class="ops-sec"><h3>时间线</h3><ol class="a-timeline" style="list-style:none;margin:0 0 0 6px">${e.timeline.map(t => `<li><div class="t">${ui.datetime(t.at, true)} ${byTag(t.byKind)} ${esc(t.byName || '')}</div><b>${esc(t.action)}</b>：${esc(t.text)}</li>`).join('')}</ol></div>
        ${cl.pendingOrderIds && cl.pendingOrderIds.length ? `<div class="a-alert"><span class="grow">还有 ${cl.pendingOrderIds.length} 单缺项等待人工处理。</span><button type="button" class="a-link" id="dg-go-rec">去恢复闭合</button></div>` : ''}`);
      const go = $('#dg-go-rec', dr.el); go && go.addEventListener('click', () => { dr.close(); goto(`#/ops/recovery?event=${e.eventId}`); });
    }

    draw(false);
    const stop = watch(page, ['degrade.changed', 'order.created', 'order.updated', 'alert'], () => draw(true));
    const tick = setInterval(() => { if (!page.isConnected) clearInterval(tick); else draw(true); }, 30000);
    void $$; void toMs;
    return () => { stop(); clearInterval(tick); };
  },
  unmount() {},
};

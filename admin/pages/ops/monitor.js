// 运行监控：开锁成功率与各品牌可用率的时间序列、降级状态；运行智能体判断是否降级与暂停可租，附依据。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $, $$, el, loadCss, syncClock, toMs, durText, bizNow, fmtDt, pct, chip, accessChip, LEVEL_CHIP, watch, errText, failToast, myPerms, goto, kvRows } from './lib.js';
import { lineChart, fmtHour, fmtDay } from './chart.js';
import { pauseBrand, resumeBrand, enterDegrade, confirmSuggestion, dismissSuggestion, suggestionText } from './actions.js';

const RANGES = [{ key: '24h', label: '近 24 小时' }, { key: '72h', label: '近 72 小时' }, { key: '30d', label: '近 30 天' }];

/** 运行智能体的判断（固定话术，按指标事实生成）。返回建议卡片数据 */
export function buildAdvice(m) {
  const th = m.thresholds.pause, out = [];
  const need = Math.ceil(th.sustainMinutes / 60);
  const byBrand = {};
  m.hourly.forEach(r => (byBrand[r.brandId] = byBrand[r.brandId] || []).push(r));
  Object.values(byBrand).forEach(a => a.sort((x, y) => toMs(x.at) - toMs(y.at)));
  const breach = r => (r.unlockSuccessRate != null && r.unlockSuccessRate < th.unlockSuccessRate) || (r.availability != null && r.availability < th.availability);
  const sgOf = {};
  (m.suspendSuggestions || []).forEach(sg => { sgOf[sg.brandId] = sg; });
  for (const b of m.brands) {
    const sg = sgOf[b.brandId];
    if (sg && b.accessStatus === '已接入') { const t = suggestionText(sg); out.push({ kind: 'suggest', brand: b, sg, tone: 'warn', say: t.say, basis: [...t.basis, `该品牌可租车辆 ${b.rentableNow} / ${b.vehicles} 辆`] }); continue; }
    const pts = byBrand[b.brandId] || [];
    if (!pts.length) continue;
    const last = pts[pts.length - 1];
    let n = 0; for (let i = pts.length - 1; i >= 0 && breach(pts[i]); i--) n++;
    let ok = 0; for (let i = pts.length - 1; i >= 0 && !breach(pts[i]); i--) ok++;
    const nums = `开锁成功率 ${pct(last.unlockSuccessRate)}（门槛 ${pct(th.unlockSuccessRate, 0)}），可用率 ${pct(last.availability)}（门槛 ${pct(th.availability, 0)}）`;
    if (b.accessStatus === '已接入' && n > 0) {
      out.push({ kind: 'watch', brand: b, tone: 'calm', say: `「${b.name}」指标低于门槛，继续观察`, basis: [`最近一次指标（${fmtDt(last.at)}）：${nums}`, `已连续 ${n} 小时低于门槛，达到 ${th.sustainMinutes} 分钟会建议暂停可租`] });
    } else if (b.accessStatus === '暂停可租') {
      if (ok >= need) out.push({ kind: 'resume', brand: b, tone: 'warn', say: `建议恢复「${b.name}」可租`, basis: [`最近一次指标（${fmtDt(last.at)}）：${nums}`, `已连续 ${ok} 小时回到门槛以上`, b.pause ? `暂停原因：${b.pause.note}` : '', '恢复后品牌回到已接入，可租车辆立即在用户端出现'].filter(Boolean) });
      else out.push({ kind: 'keep', brand: b, tone: 'calm', say: `「${b.name}」仍低于门槛，建议保持暂停`, basis: [`最近一次指标（${fmtDt(last.at)}）：${nums}`, b.pause ? `暂停原因：${b.pause.note}` : '', b.pause ? `暂停自 ${fmtDt(b.pause.pausedAt)}，已 ${durText((bizNow().getTime() - toMs(b.pause.pausedAt)) / 60000)}` : ''].filter(Boolean) });
    }
  }
  // 平台层：是否需要降级开单
  const pd = m.platformDaily[m.platformDaily.length - 1];
  const T = m.thresholds;
  const bad = [];
  if (pd) {
    if (pd.entryAvailability < T.availabilityTarget) bad.push(`用户入口可用率 ${pct(pd.entryAvailability, 2)}，低于目标 ${pct(T.availabilityTarget, 1)}`);
    if (pd.unlockP90Sec > T.unlockP90TargetSec) bad.push(`开锁结果 P90 ${pd.unlockP90Sec} 秒，超过目标 ${T.unlockP90TargetSec} 秒`);
    if (pd.billPushP90Sec > T.billPushTargetSec) bad.push(`账单推送 P90 ${pd.billPushP90Sec} 秒，超过目标 ${T.billPushTargetSec} 秒`);
  }
  const d = m.degrade;
  if (d.degraded) {
    out.unshift({ kind: 'degrading', tone: 'warn', say: '已在降级开单中，请到「降级与限额」盯限额', basis: [`原因：${d.cause}`, `已持续 ${durText(d.minutes)}，降级单 ${d.usage.orders} 单，骑行中 ${d.usage.riding} 单`, `近 1 小时 ${d.usage.lastHourOrders} / ${d.usage.hourlyLimit} 单`] });
  } else if (bad.length) {
    out.unshift({ kind: 'degrade', tone: 'warn', say: '建议进入降级开单', basis: [...bad, '降级开单让用户在故障时仍能开锁：老用户只多一行“费用将稍后收取”，坏账由限额封顶'] });
  } else {
    out.unshift({ kind: 'normal', tone: 'calm', say: '平台运行正常，无需进入降级开单', basis: pd ? [`用户入口可用率 ${pct(pd.entryAvailability, 2)}（目标 ${pct(T.availabilityTarget, 1)}）`, `开锁结果 P90 ${pd.unlockP90Sec} 秒（目标 ${T.unlockP90TargetSec} 秒）`, `账单推送 P90 ${pd.billPushP90Sec} 秒（目标 ${T.billPushTargetSec} 秒）`, `数据日期 ${pd.date}；扫码所依赖的支付、实名、押金查询服务没有发出故障信号`] : ['暂无平台指标'] });
  }
  return out;
}

export default {
  path: '#/ops/monitor', title: '运行监控', menu: '运行监控', perm: 'ops.monitor',
  sub: '开锁成功率与各品牌可用率；运行智能体判断是否降级、是否暂停可租，并说明依据',
  async mount(root) {
    loadCss();
    root.innerHTML = '';
    const page = el('<div class="ops-page"></div>');
    root.append(page);
    await syncClock();
    let perms = null; try { perms = await myPerms(); } catch { /* 无权限时按钮置灰 */ }
    const can = p => perms ? perms.has(p) : false;
    const state = { range: '72h', m: null, dismissed: new Set(), charts: [] };

    const banner = el('<div></div>'), stats = el('<div></div>'), advice = el('<div class="ops-page"></div>');
    const chartsBox = el('<div class="ops-page"></div>');
    const brandsBox = el('<div class="ops-sec"><h3>各品牌当前状态</h3><div id="mon-brands"></div></div>');
    const alertsBox = el('<div class="ops-sec"><h3>告警</h3><div id="mon-alert-tabs"></div><div id="mon-alerts"></div></div>');
    page.append(banner, stats, advice, chartsBox, brandsBox, alertsBox);

    function seriesFor(metric) {
      const m = state.m;
      const brands = m.brands.filter(b => ['已接入', '暂停可租'].includes(b.accessStatus));
      const daily = state.range === '30d';
      let rows = daily ? m.daily : m.hourly;
      if (!daily) {
        const end = Math.max(...rows.map(r => toMs(r.at)));
        const span = (state.range === '24h' ? 24 : 72) * 3600e3;
        rows = rows.filter(r => toMs(r.at) >= end - span);
      }
      return brands.map(b => ({
        key: b.brandId, name: b.name, color: b.color,
        points: rows.filter(r => r.brandId === b.brandId).map(r => ({ x: toMs(daily ? `${r.date} 00:00:00` : r.at), y: r[metric] })).sort((a, c) => a.x - c.x),
      })).filter(s => s.points.length);
    }
    const legend = (series, fmt) => `<div class="ops-legend">${series.map(s => { const l = [...s.points].reverse().find(p => p.y != null); return `<span class="it"><i class="sw" style="background:${s.color}"></i>${esc(s.name)} <b>${l ? fmt(l.y) : '—'}</b></span>`; }).join('')}<span class="it"><i class="sw" style="background:var(--err)"></i>暂停门槛</span></div>`;

    function drawCharts() {
      state.charts.forEach(c => c.destroy()); state.charts = [];
      const m = state.m, th = m.thresholds.pause, daily = state.range === '30d';
      const defs = [
        { title: '开锁成功率', metric: 'unlockSuccessRate', th: th.unlockSuccessRate, note: `低于 ${pct(th.unlockSuccessRate, 0)} 且持续 ${th.sustainMinutes} 分钟，运行智能体建议暂停可租` },
        { title: '各品牌可用率', metric: 'availability', th: th.availability, note: `低于 ${pct(th.availability, 0)} 且持续 ${th.sustainMinutes} 分钟，运行智能体建议暂停可租` },
      ];
      chartsBox.innerHTML = '';
      const head = el('<div class="ops-row"><div id="mon-range"></div><span class="ops-muted ops-right" id="mon-asof"></span></div>');
      chartsBox.append(head);
      ui.tabs($('#mon-range', head), RANGES, state.range, k => { state.range = k; drawCharts(); });
      const end = m.hourly.length ? Math.max(...m.hourly.map(r => toMs(r.at))) : null;
      $('#mon-asof', head).textContent = end ? `指标由运行智能体上报，最近一次 ${fmtDt(new Date(end))}` : '';
      const grid = el('<div class="ops-grid" style="grid-template-columns:repeat(auto-fit,minmax(480px,1fr))"></div>');
      chartsBox.append(grid);
      defs.forEach(def => {
        const series = seriesFor(def.metric);
        const panel = el(`<div class="a-panel"><div class="a-panel-head"><h3>${def.title}</h3><span class="ops-muted">${esc(def.note)}</span></div><div class="a-panel-body"><div class="lg"></div><div class="ch"></div></div></div>`);
        grid.append(panel);
        $('.lg', panel).innerHTML = legend(series, v => pct(v));
        state.charts.push(lineChart($('.ch', panel), { series, thresholds: [{ v: def.th, label: pct(def.th, 0) }], xFmt: daily ? fmtDay : fmtHour, label: `${def.title}时间序列`, height: 230 }));
      });
    }

    function drawBanner() {
      const d = state.m.degrade;
      banner.innerHTML = '';
      const tone = d.degraded ? (d.stopped ? 'is-err' : 'is-warn') : 'is-ok';
      const title = d.degraded ? (d.stopped ? '降级开单中，已停止开新单' : d.tightened ? '降级开单中，已收紧' : '降级开单中') : '正常开单';
      const sub = d.degraded ? `${d.cause}，已持续 ${durText(d.minutes)}，降级单 ${d.usage.orders} 单` : `没有降级；今日开锁请求 ${state.m.today.requests} 次`;
      const b = el(`<div class="ops-banner ${tone}" role="status"><i class="dot" aria-hidden="true"></i><div><div class="t">${esc(title)}</div><div class="s">${esc(sub)}</div></div><span class="ops-right"></span></div>`);
      const btn = el(`<button type="button" class="a-btn a-btn-${d.degraded ? 'primary' : 'default'}">${d.degraded ? '去盯限额' : '查看降级与限额'}</button>`);
      btn.addEventListener('click', () => goto('#/ops/degrade'));
      $('.ops-right', b).append(btn);
      banner.append(b);
    }
    function drawStats() {
      const m = state.m, pd = m.platformDaily[m.platformDaily.length - 1], T = m.thresholds;
      const rate = m.today.unlockSuccessRate;
      stats.innerHTML = `<div class="a-stats" style="margin:0">
        <div class="a-stat"><div class="k">今日开锁成功率</div><div class="v num">${rate == null ? '—' : `${ui.number(rate * 100, 1)}`}<small>%</small></div><div class="d">${m.today.requests} 次请求，失败 ${m.today.unlockFailed} 次</div></div>
        <div class="a-stat"><div class="k">当前骑行中</div><div class="v num">${m.today.riding}<small>单</small></div><div class="d">包含降级单</div></div>
        <div class="a-stat"><div class="k">用户入口可用率</div><div class="v num">${pd ? ui.number(pd.entryAvailability * 100, 2) : '—'}<small>%</small></div><div class="d ${pd && pd.entryAvailability < T.availabilityTarget ? 'down' : 'up'}">目标 ${pct(T.availabilityTarget, 1)}${pd ? `（${pd.date}）` : ''}</div></div>
        <div class="a-stat"><div class="k">开锁结果 P90</div><div class="v num">${pd ? pd.unlockP90Sec : '—'}<small>秒</small></div><div class="d ${pd && pd.unlockP90Sec > T.unlockP90TargetSec ? 'down' : 'up'}">目标 ${T.unlockP90TargetSec} 秒内</div></div>
        <div class="a-stat"><div class="k">账单推送 P90</div><div class="v num">${pd ? pd.billPushP90Sec : '—'}<small>秒</small></div><div class="d ${pd && pd.billPushP90Sec > T.billPushTargetSec ? 'down' : 'up'}">目标 ${T.billPushTargetSec} 秒内</div></div></div>`;
    }

    function drawAdvice() {
      advice.innerHTML = '';
      const list = buildAdvice(state.m).filter(a => !state.dismissed.has(`${a.kind}:${a.brand ? a.brand.brandId : ''}`));
      const head = el('<div class="ops-sec"><h3>运行智能体的判断</h3></div>');
      advice.append(head);
      list.forEach(a => {
        const key = `${a.kind}:${a.brand ? a.brand.brandId : ''}`;
        const actionable = ['suggest', 'pause', 'resume', 'degrade'].includes(a.kind);
        const card = el(`<div class="ops-agent${a.tone === 'calm' ? ' is-calm' : ''}"><div class="who">运行智能体${actionable ? ' · 等你决定' : ' · 仅供知悉'}</div><div class="say">${esc(a.say)}</div>
          <details class="ops-basis"${actionable ? ' open' : ''}><summary>依据</summary><ul>${a.basis.map(x => `<li>${esc(x)}</li>`).join('')}</ul></details>
          ${actionable ? '<div class="ops-row"></div>' : ''}</div>`);
        if (actionable) {
          const row = $('.ops-row', card);
          const ok = el(`<button type="button" class="a-btn a-btn-primary">${a.kind === 'suggest' ? '确认暂停可租' : a.kind === 'pause' ? '接受建议，暂停可租' : a.kind === 'resume' ? '接受建议，恢复可租' : '接受建议，进入降级'}</button>`);
          const perm = a.kind === 'degrade' ? 'degrade.control' : 'access.pause';
          if (!can(perm)) ui.setDisabled(ok, a.kind === 'degrade' ? '只有值班人能进入降级开单' : '你的账号没有暂停或恢复可租的权限', '请联系有权限的值班人或接入负责人');
          ok.addEventListener('click', async () => {
            const done = a.kind === 'suggest' ? await confirmSuggestion(a.sg, { vehicles: a.brand.vehicles }) : a.kind === 'pause' ? await pauseBrand(a.brand) : a.kind === 'resume' ? await resumeBrand(a.brand) : await enterDegrade({ why: `运行智能体依据：${a.basis[0]}` });
            if (done) load(true);
          });
          if (a.kind === 'suggest') ok.className = 'a-btn a-btn-danger';
          const no = el(`<button type="button" class="a-btn a-btn-default">${a.kind === 'suggest' ? '驳回，保持可租' : '暂不处理'}</button>`);
          if (a.kind === 'suggest') {
            if (!can('access.pause')) ui.setDisabled(no, '你的账号没有处理暂停建议的权限', '请联系有权限的值班人或接入负责人');
            no.addEventListener('click', async () => { if (await dismissSuggestion(a.sg)) load(true); });
          } else no.addEventListener('click', () => { state.dismissed.add(key); drawAdvice(); ui.toast('已暂不处理', { hint: '指标变化后智能体会重新判断' }); });
          row.append(ok, no);
        }
        advice.append(card);
      });
    }

    function drawBrands() {
      const box = $('#mon-brands', brandsBox);
      box.innerHTML = '';
      const th = state.m.thresholds.pause;
      ui.table(box, {
        rowKey: 'brandId', pageSize: 10, rows: state.m.brands,
        columns: [
          { key: 'name', title: '品牌', render: b => `<i style="display:inline-block;width:10px;height:10px;border-radius:3px;background:${esc(b.color)};margin-right:6px"></i>${esc(b.name)}` },
          { key: 'operatorName', title: '运营商', render: b => esc(b.operatorName) },
          { key: 'accessStatus', title: '接入状态', render: b => accessChip(b.accessStatus) },
          { key: 'rate', title: '开锁成功率', align: 'right', sortable: true, sortValue: b => (b.latest ? b.latest.unlockSuccessRate : -1), render: b => (b.latest ? `<span style="${b.latest.unlockSuccessRate < th.unlockSuccessRate ? 'color:var(--err);font-weight:600' : ''}">${pct(b.latest.unlockSuccessRate)}</span>` : '—') },
          { key: 'avail', title: '可用率', align: 'right', sortable: true, sortValue: b => (b.latest ? b.latest.availability : -1), render: b => (b.latest ? `<span style="${b.latest.availability < th.availability ? 'color:var(--err);font-weight:600' : ''}">${pct(b.latest.availability)}</span>` : '—') },
          { key: 'rentableNow', title: '可租 / 总数', align: 'right', render: b => `${ui.number(b.rentableNow)} / ${ui.number(b.vehicles)}` },
          { key: 'pause', title: '暂停原因', ellipsis: true, render: b => (b.pause ? `<span title="${esc(b.pause.note)}">${esc(b.pause.metric)} ${pct(b.pause.value)} · ${esc(b.pause.note)}</span>` : '<span class="ops-muted">—</span>') },
        ],
        onRowClick: b => openBrand(b),
      });
    }

    function openBrand(b) {
      const th = state.m.thresholds.pause;
      const pts = state.m.hourly.filter(r => r.brandId === b.brandId).sort((x, y) => toMs(y.at) - toMs(x.at)).slice(0, 24);
      const foot = [];
      const dr = ui.drawer({ title: `${b.name}（${b.operatorName}）`, sub: `当前${b.accessStatus}`, body: '', footer: foot, wide: false });
      const body = el('<div style="display:contents"></div>');
      body.innerHTML = `<div class="ops-row">${accessChip(b.accessStatus)}<span class="ops-muted">可租 ${ui.number(b.rentableNow)} / ${ui.number(b.vehicles)} 辆</span></div>
        ${b.pause ? `<div class="ops-sec"><h3>暂停记录</h3><div class="ops-box">${kvRows([['暂停时间', fmtDt(b.pause.pausedAt)], ['判定指标', `${esc(b.pause.metric)} ${pct(b.pause.value)}（门槛 ${pct(b.pause.threshold, 0)}，持续 ${b.pause.sustainMinutes} 分钟）`], ['操作方', esc(b.pause.byName || (String(b.pause.by || '').startsWith('AC09') ? '运行智能体' : '运维'))], ['说明', esc(b.pause.note), true]])}</div></div>` : ''}
        <div class="ops-sec"><h3>近 24 小时指标</h3>${pts.length ? `<div class="a-table-wrap"><div class="a-table-scroll"><table class="a-table"><thead><tr><th>时间</th><th class="r">开锁成功率</th><th class="r">可用率</th><th class="r">可租车辆</th></tr></thead><tbody>${pts.map(r => `<tr><td>${fmtDt(r.at)}</td><td class="r num" style="${r.unlockSuccessRate != null && r.unlockSuccessRate < th.unlockSuccessRate ? 'color:var(--err)' : ''}">${pct(r.unlockSuccessRate)}</td><td class="r num" style="${r.availability != null && r.availability < th.availability ? 'color:var(--err)' : ''}">${pct(r.availability)}</td><td class="r num">${r.rentableVehicles ?? '—'}</td></tr>`).join('')}</tbody></table></div></div>` : '<div class="ops-muted">没有逐小时指标</div>'}</div>`;
      dr.setBody(body);
      const f = $('.a-drawer-foot', dr.el);
      const mk = (label, kind, why, fn) => { const x = el(`<button type="button" class="a-btn a-btn-${kind}">${label}</button>`); if (why) ui.setDisabled(x, why[0], why[1]); x.addEventListener('click', async () => { if (await fn()) { dr.close(); load(true); } }); f.append(x); };
      if (b.accessStatus === '已接入') mk('暂停可租', 'danger', can('access.pause') ? null : ['你的账号没有暂停可租的权限', '请联系值班人或接入负责人'], () => pauseBrand(b));
      else if (b.accessStatus === '暂停可租') mk('恢复可租', 'primary', can('access.pause') ? null : ['你的账号没有恢复可租的权限', '请联系值班人或接入负责人'], () => resumeBrand(b));
      f.hidden = !f.children.length;
    }

    let alertTab = 'open', alertTable = null;
    function drawAlerts() {
      const tabs = $('#mon-alert-tabs', alertsBox), box = $('#mon-alerts', alertsBox);
      ui.tabs(tabs, [{ key: 'open', label: '未确认' }, { key: 'all', label: '全部' }], alertTab, k => { alertTab = k; build(); });
      function build() {
        alertTable = ui.table(box, {
          rowKey: 'alertId', pageSize: 5, pageSizes: [5, 10, 20],
          empty: { title: alertTab === 'open' ? '没有未确认的告警' : '没有告警记录', text: '运行智能体发现异常时会通知值班人，并显示在这里' },
          load: ({ page: pg, pageSize }) => api.admin.ops.alerts.list({ page: pg, pageSize, acked: alertTab === 'open' ? false : undefined }),
          columns: [
            { key: 'at', title: '时间', render: r => fmtDt(r.at) },
            { key: 'level', title: '级别', render: r => chip(r.level, LEVEL_CHIP[r.level] || 'mute') },
            { key: 'type', title: '类型', render: r => esc(r.type) },
            { key: 'text', title: '内容', ellipsis: true, render: r => `<span title="${esc(r.text)}">${esc(r.text)}</span>` },
            { key: 'ack', title: '处理', render: r => (r.ackBy ? `<span class="ops-muted">${esc(r.ackByName || '')} 已确认</span>` : `<button type="button" class="a-btn a-btn-default is-sm" data-act="ack">确认知悉</button>`) },
          ],
          onAction: async (act, r, btn) => {
            btn.setAttribute('aria-busy', 'true'); btn.innerHTML = '<span class="a-spin"></span>确认中';
            try { await api.admin.ops.alerts.ack(r.alertId); ui.toast('已确认知悉', { type: 'ok' }); alertTable.reload(); load(true); }
            catch (e) { failToast(e); alertTable.reload(); }
          },
        });
      }
      build();
    }

    async function load(silent) {
      if (!silent) { stats.innerHTML = ui.skeleton('lines', 3); banner.innerHTML = ''; }
      try {
        const m = await api.admin.ops.monitor({ hours: 72, days: 30 });
        if (!page.isConnected) return;
        state.m = m;
        drawBanner(); drawStats(); drawAdvice(); drawCharts(); drawBrands();
        if (!silent) drawAlerts(); else alertTable && alertTable.reload();
      } catch (e) {
        if (!page.isConnected) return;
        if (!state.m) { page.innerHTML = ''; page.append(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: () => { page.innerHTML = ''; mountAgain(); } })); }
        else failToast(e, '指标没有刷新，当前显示的是上次的数据');
      }
    }
    function mountAgain() { page.append(banner, stats, advice, chartsBox, brandsBox, alertsBox); load(false); }
    load(false);
    const stop = watch(page, ['degrade.changed', 'access.changed', 'alert', 'ops.changed'], () => load(true));
    const tick = setInterval(() => { if (!page.isConnected) clearInterval(tick); else load(true); }, 60000);
    void kvRows; void $$;
    return () => { stop(); clearInterval(tick); state.charts.forEach(c => c.destroy()); };
  },
  unmount() {},
};

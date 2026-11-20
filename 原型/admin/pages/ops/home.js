// 运维首页：关键运行指标与待办。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $, $$, el, loadCss, syncClock, durText, fmtDt, pct, chip, accessChip, LEVEL_CHIP, watch, errText, goto } from './lib.js';

const ROUTE = { 未确认告警: '#/ops/monitor', 降级单待人工: '#/ops/recovery', 待确认的验收报告: '#/ops/acceptance', 待确认的暂停可租建议: '#/ops/acceptance' };

export default {
  path: '#/ops/home', title: '运维首页', menu: '首页', perm: 'ops.monitor',
  sub: '平台能不能用、运营商能不能接、故障时用户还能不能骑',
  async mount(root) {
    loadCss();
    root.innerHTML = '';
    const page = el('<div class="ops-page"></div>');
    root.append(page);
    const body = el('<div class="ops-page"></div>');
    page.append(body);

    async function draw() {
      body.innerHTML = `<div class="a-stats">${Array.from({ length: 5 }, () => '<div class="a-stat"><span class="skel" style="height:14px;width:60%"></span><span class="skel" style="height:28px;width:40%;margin-top:8px"></span></div>').join('')}</div>${ui.skeleton('lines', 6)}`;
      try {
        await syncClock();
        const [me, m, acc] = await Promise.all([
          api.admin.me(), api.admin.ops.monitor({ hours: 24, days: 7 }),
          api.admin.ops.acceptance.list().catch(() => []),
        ]);
        let closure = [];
        if (me.permissions.includes('degrade.closure.view') || me.permissions.includes('degrade.closure.handle')) closure = await api.admin.ops.closure.list().catch(() => []);
        if (!body.isConnected) return;
        const d = m.degrade, pd = m.platformDaily[m.platformDaily.length - 1], T = m.thresholds;
        const paused = m.brands.filter(b => b.accessStatus === '暂停可租');
        const pending = acc.filter(a => a.acceptance && a.acceptance.result === '待确认').length;
        const touring = acc.filter(a => a.status === '联调中').length;
        const stat = (k, v, small, dd, route, cls = '') => `<button type="button" class="a-stat" data-go="${route}"><div class="k">${k}</div><div class="v num">${v}${small ? `<small>${small}</small>` : ''}</div><div class="d ${cls}">${dd || '&nbsp;'}</div></button>`;
        body.innerHTML = `
          <div class="ops-banner ${d.degraded ? (d.stopped ? 'is-err' : 'is-warn') : 'is-ok'}" role="status"><i class="dot" aria-hidden="true"></i><div><div class="t">${d.degraded ? (d.stopped ? '降级开单中，已停止开新单' : d.tightened ? '降级开单中，已收紧' : '降级开单中') : '正常开单'}</div><div class="s">${d.degraded ? `${esc(d.cause)}，已持续 ${durText(d.minutes)}，近 1 小时降级单 ${d.usage.lastHourOrders} / ${d.usage.hourlyLimit}` : `没有降级；最近一次降级 ${esc(d.lastDegradeEventId || '无')}`}</div></div><span class="ops-right"><button type="button" class="a-btn a-btn-default" data-go="#/ops/degrade">降级与限额</button></span></div>
          <div class="a-stats" style="margin:0">
            ${stat('今日开锁成功率', m.today.unlockSuccessRate == null ? '—' : ui.number(m.today.unlockSuccessRate * 100, 1), '%', `${m.today.requests} 次请求，失败 ${m.today.unlockFailed} 次`, '#/ops/monitor')}
            ${stat('用户入口可用率', pd ? ui.number(pd.entryAvailability * 100, 2) : '—', '%', `目标 ${pct(T.availabilityTarget, 1)}`, '#/ops/monitor', pd && pd.entryAvailability < T.availabilityTarget ? 'down' : 'up')}
            ${stat('暂停可租品牌', paused.length, '个', paused.length ? paused.map(b => b.name).join('、') : '没有', '#/ops/acceptance', paused.length ? 'down' : 'up')}
            ${stat('降级单待人工', closure.filter(c => c.closure.status === '待人工').length, '单', '缺锁车事实或收款结果', '#/ops/recovery', closure.length ? 'down' : '')}
            ${stat('联调中的品牌', touring, '个', pending ? `${pending} 份验收报告待确认` : '没有待确认的验收', '#/ops/acceptance', pending ? 'down' : '')}
          </div>
          <div class="ops-grid">
            <div class="a-panel"><div class="a-panel-head"><h3>待办</h3></div><div class="a-panel-body" id="oh-todo"></div></div>
            <div class="a-panel"><div class="a-panel-head"><h3>各品牌当前</h3><span class="ops-muted">暂停门槛：开锁成功率 ${pct(T.pause.unlockSuccessRate, 0)} / 可用率 ${pct(T.pause.availability, 0)}</span></div><div class="a-panel-body" id="oh-brands"></div></div>
          </div>
          <div class="a-panel"><div class="a-panel-head"><h3>最近告警</h3><button type="button" class="a-btn a-btn-text" data-go="#/ops/monitor">全部告警</button></div><div class="a-panel-body" id="oh-alerts"></div></div>`;
        const todo = $('#oh-todo', body);
        const items = [...me.todos.map(t => ({ label: t.label, count: t.count, route: ROUTE[t.label] || '#/ops/monitor' }))];
        if (d.degraded) items.unshift({ label: '降级开单进行中，盯限额', count: d.usage.lastHourOrders, route: '#/ops/degrade', suffix: `/ ${d.usage.hourlyLimit}` });
        todo.innerHTML = items.map(t => `<div class="ops-todo"><div class="t">${esc(t.label)}</div><div class="ops-row"><b class="num">${t.count}${t.suffix ? ` ${esc(t.suffix)}` : ''}</b><button type="button" class="a-btn a-btn-default is-sm" data-go="${t.route}">去处理</button></div></div>`).join('');
        if (!items.length) todo.append(ui.empty({ icon: '✅', title: '现在没有待办', text: '有告警、缺项的降级单或待确认的验收时会出现在这里' }));
        const bb = $('#oh-brands', body);
        ui.table(bb, {
          rowKey: 'brandId', pageSize: 10, rows: m.brands,
          columns: [
            { key: 'name', title: '品牌', render: b => `<i style="display:inline-block;width:10px;height:10px;border-radius:3px;background:${esc(b.color)};margin-right:6px"></i>${esc(b.name)}` },
            { key: 'st', title: '状态', render: b => accessChip(b.accessStatus) },
            { key: 'rate', title: '开锁成功率', align: 'right', render: b => (b.latest ? `<span style="${b.latest.unlockSuccessRate < T.pause.unlockSuccessRate ? 'color:var(--err);font-weight:600' : ''}">${pct(b.latest.unlockSuccessRate)}</span>` : '—') },
            { key: 'av', title: '可用率', align: 'right', render: b => (b.latest ? `<span style="${b.latest.availability < T.pause.availability ? 'color:var(--err);font-weight:600' : ''}">${pct(b.latest.availability)}</span>` : '—') },
            { key: 'rn', title: '可租 / 总数', align: 'right', render: b => `${ui.number(b.rentableNow)} / ${ui.number(b.vehicles)}` },
          ],
          onRowClick: () => goto('#/ops/monitor'),
        });
        const al = $('#oh-alerts', body);
        if (m.alerts.length) al.innerHTML = m.alerts.slice(0, 5).map(a => `<div class="ops-todo"><div><div class="t">${chip(a.level, LEVEL_CHIP[a.level] || 'mute')} ${esc(a.type)} · ${esc(a.text)}</div><div class="s">${fmtDt(a.at)}</div></div></div>`).join('');
        else al.append(ui.empty({ icon: '🔔', title: '没有未确认的告警', text: '运行智能体发现异常时会通知值班人' }));
        $$('[data-go]', body).forEach(b => b.addEventListener('click', () => goto(b.dataset.go)));
      } catch (e) {
        body.innerHTML = '';
        body.append(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: draw }));
      }
    }
    draw();
    const stop = watch(page, ['degrade.changed', 'access.changed', 'alert', 'order.updated'], draw);
    return () => stop();
  },
  unmount() {},
};

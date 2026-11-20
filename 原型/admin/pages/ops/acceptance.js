// 接入验收：各品牌符合性测试、真车联调、验收确认（接入负责人确认即上线）；暂停可租品牌与原因、恢复。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $, $$, el, loadCss, syncClock, durText, bizNow, toMs, fmtDt, pct, chip, accessChip, byTag, watch, errText, failToast, myPerms, kvRows } from './lib.js';
import { pauseBrand, resumeBrand, confirmSuggestion, dismissSuggestion, suggestionText } from './actions.js';

const RESULT_CHIP = { 通过: 'ok', 待确认: 'warn', 未开始: 'mute' };
const SG_CHIP = { 待确认: 'warn', 已确认: 'ok', 已驳回: 'mute', 已失效: 'mute' };

export default {
  path: '#/ops/acceptance', title: '接入验收', menu: '接入验收', perm: ['ops.monitor', 'acceptance.confirm'],
  sub: '符合性测试全部通过、真车联调订单与锁车报告全部匹配后，接入负责人确认即上线；运行智能体的暂停可租建议也在这里确认或驳回',
  async mount(root) {
    loadCss();
    root.innerHTML = '';
    const page = el('<div class="ops-page"></div>');
    root.append(page);
    await syncClock();
    let perms = null; try { perms = await myPerms(); } catch { /* 无权限时按钮置灰 */ }
    const can = p => perms ? perms.has(p) : false;
    let tab = 'prog', table = null, list = [];
    const sugBox = el('<div class="ops-page"></div>'), tabsBox = el('<div></div>'), filter = el('<div></div>'), tbl = el('<div></div>');
    page.append(sugBox, tabsBox, filter, tbl);
    const reloadAll = () => { drawSuggest(); table && table.reload(); };

    /* ---------- 待确认的暂停可租建议：全页最显眼的确认点 ---------- */
    const sgButtons = (sg, after, vehicles) => {
      const ok = el('<button type="button" class="a-btn a-btn-danger">确认暂停可租</button>');
      const no = el('<button type="button" class="a-btn a-btn-default">驳回，保持可租</button>');
      if (!can('access.pause')) [ok, no].forEach(b => ui.setDisabled(b, '只有运维值班人或接入负责人能处理暂停建议', '请联系值班人或接入负责人'));
      ok.addEventListener('click', async () => { if (await confirmSuggestion(sg, { vehicles })) after(); });
      no.addEventListener('click', async () => { if (await dismissSuggestion(sg)) after(); });
      return [ok, no];
    };
    const sgCard = (sg, after, vehicles) => {
      const t = suggestionText(sg);
      const card = el(`<div class="ops-agent" role="region" aria-label="${esc(t.say)}"><div class="who">运行智能体 · 等你确认</div><div class="say">${esc(t.say)}</div>
        <details class="ops-basis" open><summary>依据</summary><ul>${t.basis.map(x => `<li>${esc(x)}</li>`).join('')}</ul></details>
        <div class="ops-muted">确认后该品牌车辆立即不可租，已在骑行的订单照常结束；驳回则保持可租。两种处理都会记录处理人与事由。</div>
        <div class="ops-row"></div></div>`);
      $('.ops-row', card).append(...sgButtons(sg, after, vehicles));
      return card;
    };
    async function drawSuggest() {
      let list = [];
      try { list = await api.admin.ops.suspendSuggestions({ status: '待确认' }); } catch { list = []; }
      if (!sugBox.isConnected) return;
      sugBox.innerHTML = '';
      list.forEach(sg => sugBox.append(sgCard(sg, () => { reloadAll(); }, null)));
      sugBox.hidden = !list.length;
    }

    const progress = a => {
      const ac = a.acceptance;
      const lr = a.latestRun;
      return { test: lr ? `${lr.summary.passed} / ${lr.summary.total}` : '—', rides: ac ? `${ac.realVehiclesRidden} / ${ac.realVehiclesRequired} 辆，${ac.matchedOrders} / ${ac.realOrders} 单匹配` : '—' };
    };

    function drawProg() {
      filter.className = 'a-filter';
      filter.innerHTML = `<div class="f"><label for="ac-st">接入状态</label><select id="ac-st" class="a-select"><option value="">全部</option>${['联调中', '已接入', '暂停可租', '未接入', '已退出'].map(s => `<option>${s}</option>`).join('')}</select></div>
        <div class="f"><label for="ac-q">品牌或运营商</label><input id="ac-q" class="a-input" placeholder="例如 市民骑行" autocomplete="off"></div><span class="grow"></span><button type="button" class="a-btn a-btn-default" id="ac-reset">重置</button><button type="button" class="a-btn a-btn-primary" id="ac-go">查询</button>`;
      filter.hidden = false;
      table = ui.table(tbl, {
        rowKey: 'integrationId', pageSize: 10,
        empty: { title: '没有符合条件的品牌', text: '换个状态或关键字试试' },
        load: async ({ page: pg, pageSize }) => {
          list = await api.admin.ops.acceptance.list();
          const st = $('#ac-st', filter).value, kw = $('#ac-q', filter).value.trim();
          const rows = list.filter(a => (!st || a.status === st) && (!kw || a.brandName.includes(kw) || a.operatorName.includes(kw)));
          return { rows: rows.slice((pg - 1) * pageSize, pg * pageSize), total: rows.length };
        },
        columns: [
          { key: 'brandName', title: '品牌', render: a => `<b>${esc(a.brandName)}</b>` },
          { key: 'operatorName', title: '运营商', render: a => `${esc(a.operatorName)}${a.accessVia === '车联网服务商代接' ? ' <span class="ops-muted">服务商代接</span>' : ''}` },
          { key: 'status', title: '接入状态', render: a => `${accessChip(a.status)}${a.suspendSuggestion ? ` ${chip('有暂停建议', 'warn')}` : ''}` },
          { key: 'days', title: '联调用时', align: 'right', render: a => (a.status === '联调中' && a.elapsedDays != null ? `<span style="${a.elapsedDays > a.targetDays ? 'color:var(--warn);font-weight:600' : ''}">${a.elapsedDays} / ${a.targetDays} 天</span>` : '<span class="ops-muted">—</span>') },
          { key: 'test', title: '符合性测试', align: 'right', render: a => { const p = progress(a); const lr = a.latestRun; return lr ? `<span style="${lr.summary.failed ? 'color:var(--err);font-weight:600' : ''}">${p.test}</span>` : '—'; } },
          { key: 'rides', title: '真车联调', render: a => esc(progress(a).rides) },
          { key: 'result', title: '验收', render: a => (a.acceptance ? chip(a.acceptance.result, RESULT_CHIP[a.acceptance.result] || 'mute') : '<span class="ops-muted">—</span>') },
          { key: 'op', title: '', render: a => { const hot = a.suspendSuggestion ? '处理暂停建议' : a.acceptance && a.acceptance.result === '待确认' ? '验收确认' : ''; return `<div class="a-row-act"><button type="button" class="a-btn a-btn-${hot ? 'primary' : 'default'} is-sm" data-act="o">${hot || '查看'}</button></div>`; } },
        ],
        onRowClick: a => openDetail(a),
        onAction: (act, a) => openDetail(a),
      });
      $('#ac-go', filter).addEventListener('click', () => table.goto(1));
      $('#ac-q', filter).addEventListener('keydown', e => { if (e.key === 'Enter') table.goto(1); });
      $('#ac-st', filter).addEventListener('change', () => table.goto(1));
      $('#ac-reset', filter).addEventListener('click', () => { $('#ac-st', filter).value = ''; $('#ac-q', filter).value = ''; table.goto(1); });
    }

    function drawPauses() {
      filter.className = ''; filter.innerHTML = ''; filter.hidden = true;
      table = ui.table(tbl, {
        rowKey: 'id', pageSize: 10,
        empty: { title: '还没有暂停可租记录', text: '确认运行智能体的暂停建议或人工暂停后，记录会出现在这里' },
        load: async ({ page: pg, pageSize }) => {
          const rows = await api.admin.ops.pauses();
          return { rows: rows.slice((pg - 1) * pageSize, pg * pageSize), total: rows.length };
        },
        columns: [
          { key: 'brandName', title: '品牌', render: p => `<b>${esc(p.brandName)}</b>` },
          { key: 'state', title: '状态', render: p => (p.resumedAt ? chip('已恢复', 'ok') : chip('暂停中', 'warn')) },
          { key: 'pausedAt', title: '暂停时间', render: p => fmtDt(p.pausedAt) },
          { key: 'metric', title: '判定指标', render: p => (p.value != null ? `${esc(p.metric)} ${pct(p.value)}（门槛 ${pct(p.threshold, 0)}）` : esc(p.metric)) },
          { key: 'by', title: '操作方', render: p => `${byTag(p.by && String(p.by).startsWith('AC09') ? '智能体' : '人')} ${esc(p.byName || '')}` },
          { key: 'note', title: '原因', ellipsis: true, render: p => `<span title="${esc(p.note)}">${esc(p.note)}</span>` },
          { key: 'resumedAt', title: '恢复时间', render: p => (p.resumedAt ? fmtDt(p.resumedAt) : '—') },
          { key: 'op', title: '', render: p => (p.resumedAt ? '' : '<div class="a-row-act"><button type="button" class="a-btn a-btn-primary is-sm" data-act="resume">恢复可租</button></div>') },
        ],
        onAction: async (act, p) => {
          const brand = { brandId: p.brandId, name: p.brandName };
          if (!can('access.pause')) { ui.toast('你的账号没有恢复可租的权限', { type: 'warn', hint: '请联系值班人或接入负责人' }); return; }
          if (await resumeBrand(brand, { note: `暂停原因：${esc(p.note)}` })) table.reload();
        },
      });
    }
    function drawSuggestLog() {
      filter.className = ''; filter.hidden = false; filter.innerHTML = '';
      const bar = el('<div class="ops-banner" style="margin-bottom:12px"><div><div class="t" id="sg-auto-t">运行智能体自动暂停</div><div class="s" id="sg-auto-s">读取中…</div></div><span class="ops-right"></span></div>');
      filter.append(bar);
      (async () => {
        try {
          const m = await api.admin.ops.monitor({ hours: 1, days: 1 });
          const on = !!m.autoPauseByAgent;
          $('#sg-auto-t', bar).textContent = on ? '运行智能体自动暂停：已开启' : '运行智能体自动暂停：已关闭';
          $('#sg-auto-s', bar).textContent = on ? '指标持续低于门槛时，运行智能体直接暂停可租，事后在这里留记录；可随时关闭，收回为人工确认' : '指标持续低于门槛时，运行智能体只提出建议，由值班人或接入负责人确认后才暂停';
          const b = el(`<button type="button" class="a-btn a-btn-default">${on ? '关闭自动暂停' : '开启自动暂停'}</button>`);
          if (!can('access.pause')) ui.setDisabled(b, '只有运维值班人或接入负责人能修改', '请联系值班人或接入负责人');
          b.addEventListener('click', async () => {
            const ans = await ui.confirm({
              title: on ? '关闭运行智能体自动暂停' : '开启运行智能体自动暂停', danger: !on, confirmText: on ? '确认关闭' : '确认开启',
              message: on ? '<p>关闭后，运行智能体只提出暂停可租建议，由值班人或接入负责人确认后才暂停。</p>' : '<p>开启后，品牌指标持续低于门槛时由运行智能体直接暂停可租，不再等人确认；暂停记录照常留痕，可随时恢复可租或关闭本开关。</p>',
              impact: on ? '此后的暂停可租都需人工确认' : '此后的暂停可租由运行智能体直接执行',
              reasons: on ? ['恢复人工确认，避免误暂停', '指标波动较大，改为人工把关'] : ['夜间无人值守，允许运行智能体直接暂停', '经评估门槛可靠，开启自动暂停'],
              onConfirm: async reason => { await api.admin.ops.setAutoSuspend(!on, reason); },
            });
            if (ans) { ui.toast(on ? '已关闭自动暂停' : '已开启自动暂停', { type: 'ok', hint: '修改已写入操作留痕' }); drawSuggestLog(); }
          });
          $('.ops-right', bar).append(b);
        } catch (e) { $('#sg-auto-s', bar).textContent = `${errText(e)}，开关状态没有读取到`; }
      })();
      table = ui.table(tbl, {
        rowKey: 'id', pageSize: 10,
        empty: { title: '还没有暂停可租建议', text: '品牌开锁成功率或可用率持续低于门槛时，运行智能体会提出建议并显示在这里' },
        load: async ({ page: pg, pageSize }) => {
          const rows = await api.admin.ops.suspendSuggestions();
          return { rows: rows.slice((pg - 1) * pageSize, pg * pageSize), total: rows.length };
        },
        columns: [
          { key: 'brandName', title: '品牌', render: x => `<b>${esc(x.brandName)}</b>` },
          { key: 'at', title: '提出时间', render: x => fmtDt(x.at) },
          { key: 'metric', title: '指标', align: 'right', render: x => `<span class="num">${esc(x.metric)} ${pct(x.value)}</span> <span class="ops-muted">门槛 ${pct(x.threshold)}</span>` },
          { key: 'status', title: '处理结果', render: x => chip(x.status, SG_CHIP[x.status] || 'mute') },
          { key: 'by', title: '处理人', render: x => (x.decidedAt ? `${byTag(x.decidedBy && String(x.decidedBy).startsWith('AC09') ? '智能体' : '人')} ${esc(x.decidedByName || '')}` : '<span class="ops-muted">—</span>') },
          { key: 'decidedAt', title: '处理时间', render: x => (x.decidedAt ? fmtDt(x.decidedAt) : '—') },
          { key: 'decisionReason', title: '事由', ellipsis: true, render: x => (x.decisionReason ? `<span title="${esc(x.decisionReason)}">${esc(x.decisionReason)}</span>` : '<span class="ops-muted">—</span>') },
          { key: 'op', title: '', render: x => (x.status === '待确认' ? '<div class="a-row-act"><button type="button" class="a-btn a-btn-primary is-sm" data-act="o">处理</button></div>' : '') },
        ],
        onAction: () => { sugBox.scrollIntoView({ block: 'start' }); const f = $('.ops-agent button', sugBox); f && f.focus(); },
      });
    }
    function drawTabs() {
      ui.tabs(tabsBox, [{ key: 'prog', label: '接入进度与验收' }, { key: 'sugg', label: '暂停可租建议' }, { key: 'pause', label: '暂停可租记录' }], tab, k => { tab = k; k === 'prog' ? drawProg() : k === 'sugg' ? drawSuggestLog() : drawPauses(); });
    }

    /* ---------- 详情抽屉 ---------- */
    function openDetail(a0) {
      const dr = ui.drawer({ title: `${a0.brandName}（${a0.operatorName}）`, sub: `接入 ${a0.integrationId}`, body: ui.skeleton('lines', 8), wide: true, footer: [] });
      const foot = $('.a-drawer-foot', dr.el);
      async function render() {
        foot.innerHTML = ''; foot.hidden = true;
        let a;
        try { a = (await api.admin.ops.acceptance.list()).find(x => x.integrationId === a0.integrationId); if (!a) throw new Error('没有找到该接入记录'); }
        catch (e) { dr.setBody(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: render })); return; }
        const ac = a.acceptance, lr = a.latestRun;
        const ready = ac && ac.result === '待确认';
        const testsOk = lr && lr.summary.failed === 0;
        const body = el('<div style="display:contents"></div>');
        const fails = lr ? lr.cases.filter(c => c.result === '失败') : [];
        let say, tone = 'calm', basis = [];
        if (ready) { say = '符合性测试与真车联调都已达标，可以确认验收，确认即上线'; tone = 'warn'; basis = [`符合性测试 ${lr.summary.passed} / ${lr.summary.total} 项通过（${esc(lr.runId)}）`, `真车联调 ${ac.realVehiclesRidden} / ${ac.realVehiclesRequired} 辆，实骑 ${ac.realOrders} 单，匹配 ${ac.matchedOrders} 单，锁车报告匹配 ${ac.lockReportsMatched} 单`, `开锁结果时限${ac.timeLimitOk ? '达标' : '未达标'}`, ac.summary]; }
        else if (ac && ac.result === '通过') { say = '已通过验收并上线'; basis = [ac.summary, `确认于 ${fmtDt(ac.confirmedAt)}`]; }
        else if (a.status === '联调中') {
          say = fails.length ? `还有 ${fails.length} 项符合性测试没有通过，暂不能进入真车联调` : testsOk ? '符合性测试已通过，等运营商提交真车联调结果' : '运营商还没有开始符合性测试';
          basis = [lr ? `最近一轮 ${esc(lr.runId)}（${fmtDt(lr.at)}）：${lr.summary.passed} / ${lr.summary.total} 项通过` : '没有测试记录', ...fails.map(f => `未通过：${f.name}——${f.failReason}`), `联调已 ${a.elapsedDays ?? 0} 天，目标 ${a.targetDays} 天内`, '验收标准：18 项符合性测试全部通过、时限达标、真车联调订单与锁车报告全部匹配'];
        } else if (a.status === '暂停可租') { say = '品牌暂停可租中，指标恢复后可恢复可租'; basis = ['暂停可租期间车辆不可租，已在骑行的订单照常结束']; }
        else if (a.status === '未接入') { say = '尚未开始联调，等运营商领取联调环境凭证后开始'; basis = ['接入状态只是数据，不代表经营许可']; }
        else { say = `品牌${a.status}`; basis = []; }
        const sg = a.suspendSuggestion;
        body.innerHTML = `<div class="ops-row">${accessChip(a.status)} ${ac ? chip(`验收${ac.result}`, RESULT_CHIP[ac.result] || 'mute') : ''}<span class="ops-muted">状态自 ${fmtDt(a.statusSince)}${a.status === '联调中' && a.elapsedDays != null ? ` · 联调已 ${a.elapsedDays} / ${a.targetDays} 天` : ''}</span></div>
          <div data-sg></div><div class="ops-agent${tone === 'calm' ? ' is-calm' : ''}"><div class="who">接入智能体${ready ? ' · 等接入负责人确认' : ' · 仅供知悉'}</div><div class="say">${esc(say)}</div><details class="ops-basis" ${ready ? 'open' : ''}><summary>依据</summary><ul>${basis.map(x => `<li>${esc(x)}</li>`).join('')}</ul></details></div>
          <div class="ops-sec"><h3>符合性测试${lr ? `（${esc(lr.runId)} · ${fmtDt(lr.at)}）` : ''}</h3>${lr ? `<div class="ops-row"><b class="num">${lr.summary.passed} / ${lr.summary.total}</b><span class="ops-muted">项通过</span>${lr.summary.failed ? chip(`${lr.summary.failed} 项未通过`, 'err') : chip('全部通过', 'ok')}</div>
            <div class="ops-test">${fails.map(f => `<div class="c bad"><b>${esc(f.group)} · ${esc(f.name)}</b><br>未通过原因：${esc(f.failReason || '—')}${f.suggestion ? `<br>修改建议：${esc(f.suggestion)}${f.suggestedBy ? `（接入智能体）` : ''}` : ''}</div>`).join('')}</div>
            <details class="ops-basis"><summary>查看全部 ${lr.cases.length} 项用例</summary><div class="a-table-wrap" style="margin-top:6px"><div class="a-table-scroll"><table class="a-table"><thead><tr><th>用例</th><th>分组</th><th>结果</th><th class="r">耗时</th></tr></thead><tbody>${lr.cases.map(c => `<tr><td>${esc(c.name)}</td><td>${esc(c.group)}</td><td>${chip(c.result, c.result === '通过' ? 'ok' : 'err')}</td><td class="r num">${c.durationMs} ms</td></tr>`).join('')}</tbody></table></div></div></details>` : '<div class="ops-box is-soft ops-muted">还没有测试记录。运营商在联调环境发起符合性测试后会显示在这里。</div>'}</div>
          <div class="ops-sec"><h3>真车联调</h3>${ac && ac.reportId ? `<div class="ops-box">${kvRows([['验收报告', `<span class="ops-mono">${esc(ac.reportId)}</span>`], ['提交', `${fmtDt(ac.submittedAt)} ${byTag('智能体')} 接入智能体`], ['真车', `${ac.realVehiclesRidden} / ${ac.realVehiclesRequired} 辆`], ['订单', `实骑 ${ac.realOrders} 单，匹配 ${ac.matchedOrders} 单`], ['锁车报告匹配', `${ac.lockReportsMatched} / ${ac.realOrders} 单`], ['时限', ac.timeLimitOk ? '开锁结果时限达标' : '开锁结果时限未达标'], ['结论', esc(ac.summary), true], ...(ac.confirmedAt ? [['确认', `${fmtDt(ac.confirmedAt)}（接入负责人）`]] : [])])}</div>` : '<div class="ops-box is-soft ops-muted">符合性测试全部通过后，运营商用参数规定数量的真车联调，接入智能体生成验收报告后显示在这里。</div>'}</div>
          <div class="ops-sec"><h3>状态变化</h3><ol class="a-timeline" style="list-style:none;margin:0 0 0 6px">${[...a.statusHistory].reverse().slice(0, 6).map(h => `<li><div class="t">${ui.datetime(h.at, true)}</div><b>${esc(h.status)}</b>：${esc(h.note || '')}</li>`).join('')}</ol></div>`;
        if (sg) {
          const t = suggestionText(sg);
          const box = $('[data-sg]', body);
          box.outerHTML = `<div class="ops-agent"><div class="who">运行智能体 · 等你确认</div><div class="say">${esc(t.say)}</div><details class="ops-basis" open><summary>依据</summary><ul>${t.basis.map(x => `<li>${esc(x)}</li>`).join('')}</ul></details><div class="ops-muted">在下方确认暂停或驳回；两种处理都会记录处理人与事由。</div></div>`;
        } else $('[data-sg]', body).remove();
        dr.setBody(body);
        foot.hidden = false;
        const brand = { brandId: a.brandId, name: a.brandName, vehicles: undefined };
        if (a.status === '已接入' && sg) {
          sgButtons(sg, async () => { reloadAll(); await render(); }, null).forEach(b => foot.append(b));
        } else if (a.status === '已接入') {
          const b = el('<button type="button" class="a-btn a-btn-danger">暂停可租</button>');
          if (!can('access.pause')) ui.setDisabled(b, '你的账号没有暂停可租的权限', '请联系值班人或接入负责人');
          b.addEventListener('click', async () => { if (await pauseBrand(brand)) { reloadAll(); await render(); } });
          foot.append(b);
        }
        if (a.status === '暂停可租') {
          const b = el('<button type="button" class="a-btn a-btn-primary">恢复可租</button>');
          if (!can('access.pause')) ui.setDisabled(b, '你的账号没有恢复可租的权限', '请联系值班人或接入负责人');
          b.addEventListener('click', async () => { if (await resumeBrand(brand)) { reloadAll(); await render(); } });
          foot.append(b);
        }
        if (a.status === '联调中') {
          const b = el('<button type="button" class="a-btn a-btn-primary">验收确认，上线</button>');
          if (!can('acceptance.confirm')) ui.setDisabled(b, '只有接入负责人能确认验收', '请联系接入负责人');
          else if (!ready) ui.setDisabled(b, testsOk ? '真车联调结果还没有提交' : '符合性测试还没有全部通过', testsOk ? '等运营商提交真车联调结果后再确认' : '等运营商修改并重新通过测试');
          b.addEventListener('click', async () => {
            const ans = await ui.confirm({
              title: `确认验收，${a.brandName}上线`, danger: true, confirmText: '确认验收并上线',
              message: `<p>确认后，<b>${esc(a.brandName)}</b>立即上线，待启用的政府码启用，车辆可被用户扫码租用。</p><div class="ops-script">${esc(ac.summary)}</div>`,
              impact: `${a.brandName} 状态改为已接入；该品牌全部待启用的车辆码立即启用，可租`,
              reasons: ['符合性测试全部通过，真车联调订单与锁车报告全部匹配，确认验收', '验收报告核对无误，确认上线'],
              onConfirm: async reason => { await api.admin.ops.acceptance.confirm(a.integrationId, reason); },
            });
            if (!ans) return;
            ui.toast(`${a.brandName}已上线`, { type: 'ok', hint: '车辆码已启用，用户立即可以扫码租用；验收确认已留痕' });
            table.reload(); await render();
          });
          foot.append(b);
        }
        foot.hidden = !foot.children.length;
      }
      render();
    }

    drawSuggest(); drawTabs(); drawProg();
    const stop = watch(page, ['access.changed', 'integration.changed', 'ops.changed'], () => reloadAll());
    void $$; void durText; void bizNow; void toMs; void failToast;
    return () => stop();
  },
  unmount() {},
};

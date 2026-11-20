// 验收与上线（R-40～R-43）：验收进度、真车联调结果、验收报告、上线状态与接入状态。
import { api, ui, esc, op, injectStyle, getMe, getBrands, can, dt, dts, chip, chipRaw, kv, panel, btn, busy, fail, errText, watch, saveFile, nowStr, go, loadInto, setHead } from './common.js';

export default {
  path: '#/op/acceptance', title: '验收与上线', menu: '验收与上线', perm: ['acceptance.view.own', 'access.view.own'],
  async mount(el, query, pctx) {
    injectStyle();
    el.innerHTML = '';
    setHead(el, pctx, '验收与上线', '测试全部通过、真车联调全部匹配，经平台公司接入负责人确认后即上线；接入状态只记录接入事实，不代表经营许可', []);
    const host = ui.el('<div class="op-stack"></div>');
    el.append(host);
    const state = { brandId: null };
    const draw = () => loadInto(host, async () => {
      const me = await getMe();
      const [integs, brands, now] = await Promise.all([op().integration.get(), getBrands(true), nowStr()]);
      return { me, integs, brands, now };
    }, ({ me, integs, brands, now }, h) => {
      if (!integs.length) { h.append(ui.empty({ icon: '○', title: '还没有接入记录', text: '平台公司为你的品牌建立接入记录后，这里会显示验收进度' })); return; }
      if (!state.brandId || !integs.some(i => i.brandId === state.brandId)) state.brandId = integs[0].brandId;
      const tabsBox = ui.el('<div></div>'); const body = ui.el('<div class="op-stack"></div>');
      if (integs.length > 1) h.append(tabsBox);
      h.append(body);
      const show = () => { body.innerHTML = ''; drawBrand(body, integs.find(x => x.brandId === state.brandId), brands, me, now, draw); };
      if (integs.length > 1) ui.tabs(tabsBox, integs.map(i => ({ key: i.brandId, label: `${i.brandName}（${i.status}）` })), state.brandId, k => { state.brandId = k; show(); });
      show();
    }, { rows: 8 });
    const off = watch(el, ['integration.changed', 'access.changed'], () => draw(), 800);
    pctx && pctx.onUnmount && pctx.onUnmount(off);
    await draw();
  },
};

function steps(i) {
  const a = i.acceptance || {}, run = i.latestRun, v = i.vehicleList;
  const online = ['已接入', '暂停可租'].includes(i.status);
  const runOk = !!(run && run.summary.failed === 0);
  const ridesOk = !!(a.realOrders && a.matchedOrders === a.realOrders && a.realVehiclesRidden >= a.realVehiclesRequired);
  const list = [
    { t: '联调凭证', s: i.credentials ? `${dt(i.credentials.issuedAt)} 发放` : '尚未发放', done: online || !!i.credentials },
    { t: '符合性测试', s: run ? `${run.summary.passed} / ${run.summary.total} 项通过` : '尚未测试', done: online || runOk },
    { t: '车辆清单与换码', s: v && v.vehicleCount ? `${ui.number(v.vehicleCount)} 辆，已发码 ${ui.number(v.codesIssued)}` : '尚未提交', done: online || !!(v && v.vehicleCount) },
    { t: '真车联调', s: a.realOrders ? `${a.realVehiclesRidden} 辆，${a.matchedOrders} / ${a.realOrders} 单匹配` : `需 ${a.realVehiclesRequired || 20} 辆真车`, done: online || ridesOk },
    { t: '验收报告', s: a.reportId ? (a.result === '通过' ? '已确认' : '已生成') : '待生成', done: online || !!a.reportId },
    { t: '确认上线', s: online ? '已上线' : a.result === '待确认' ? '等待接入负责人确认' : '待确认', done: online || a.result === '通过' },
  ];
  const k = list.findIndex(x => !x.done);
  list.forEach((x, n) => { x.now = n === k; });
  return list;
}

function drawBrand(body, i, brands, me, now, refresh) {
  const a = i.acceptance || {};
  const br = brands.find(b => b.brandId === i.brandId) || {};
  const online = ['已接入', '暂停可租'].includes(i.status);
  const days = i.startedAt ? Math.max(0, Math.floor((new Date(now.replace(' ', 'T')) - new Date(i.startedAt.replace(' ', 'T'))) / 864e5)) : null;

  /* 状态 */
  if (i.status === '暂停可租' && br.pause) {
    const p = br.pause;
    body.append(ui.el(`<div class="op-banner is-warn" role="status"><div class="grow"><div class="op-row"><b>${esc(i.brandName)} 已暂停可租</b><span>自 ${dts(p.pausedAt)}</span></div>
      <div style="margin-top:6px">${esc(p.metric)} ${ui.percent(p.value, 1)}，低于门槛 ${ui.percent(p.threshold, 0)}，已持续 ${ui.number(p.sustainMinutes)} 分钟。暂停期间用户扫码会看到说明与附近其他品牌的车辆；已在骑行的订单正常结束。</div>
      <div class="op-note" style="margin-top:6px;color:inherit;opacity:.85">${esc(p.note || '')}</div><div style="margin-top:6px"><b>需要你做的：</b>修复品牌自身的问题。指标恢复后，系统自动回到「已接入」，不需要申请。</div></div></div>`));
  }
  const headBanner = ui.el(`<div class="op-banner ${i.status === '已接入' ? 'is-ok' : i.status === '联调中' ? 'is-info' : ''}"><div class="grow"><div class="op-row"><b>${esc(i.brandName)}</b>${chip(i.status, 'access')}<span class="op-note">${dt(i.statusSince)} 起</span></div>
    <div class="op-note" style="margin-top:4px">${online ? `品牌已上线，车辆${i.status === '暂停可租' ? '暂停可租中' : '开放可租'}。` : i.status === '联调中' ? `${days != null ? `已联调 ${days} 天，目标 ${i.targetDays} 天内完成验收。` : ''}` : i.status === '已退出' ? '品牌已退出，其车辆的政府码已失效，账号为只读。' : '尚未开始接入。'}</div></div></div>`);
  body.append(headBanner);

  /* 进度 */
  const st = steps(i);
  body.append(panel('验收进度', `<div class="op-steps">${st.map((x, n) => `<div class="op-step ${x.done ? 'is-done' : x.now ? 'is-now' : ''}"><div class="dot">${x.done ? '✓' : n + 1}</div><div class="t">${esc(x.t)}</div><div class="s">${esc(x.s)}</div></div>`).join('')}</div>`));

  /* 真车联调结果 */
  const run = i.latestRun;
  const runOk = !!(run && run.summary.failed === 0);
  const canSubmit = can(me, 'integration.conformance.own');
  const bSub = btn('提交真车联调结果', 'primary', () => submit());
  const why = me.readonly ? ['运营商已退出，账号为只读', '如有疑问，请联系主管部门']
    : !canSubmit ? ['真车联调结果由技术对接人员提交', '请通知本运营商的技术对接人员']
      : i.status !== '联调中' ? [`品牌当前为${i.status}，不需要提交`, '']
        : a.result === '待确认' ? ['验收报告已提交，等待接入负责人确认', '确认后品牌即上线']
          : !runOk ? ['符合性测试还没有全部通过', '先到「联调与测试」修复失败项并重新测试'] : null;
  if (why) ui.setDisabled(bSub, why[0], why[1]);
  const hasRides = a.realOrders > 0;
  const rides = panel('真车联调结果', '', '<span class="a-actions"></span>');
  if (i.status === '联调中') ui.$('.a-actions', rides).append(bSub);
  const rb = ui.$('.a-panel-body', rides);
  rb.classList.add('op-stack');
  if (hasRides) {
    const matchOk = a.matchedOrders === a.realOrders, lockOk = a.lockReportsMatched === a.realOrders;
    rb.innerHTML = `<div class="op-kvgrid">
      <div><div class="k">真车</div><div class="v">${a.realVehiclesRidden} / ${a.realVehiclesRequired} 辆</div></div>
      <div><div class="k">联调订单</div><div class="v">${a.realOrders} 单</div></div>
      <div><div class="k">订单匹配</div><div class="v" style="color:${matchOk ? 'var(--ok)' : 'var(--err)'}">${a.matchedOrders} / ${a.realOrders}</div></div>
      <div><div class="k">锁车报告匹配</div><div class="v" style="color:${lockOk ? 'var(--ok)' : 'var(--err)'}">${a.lockReportsMatched} / ${a.realOrders}</div></div>
      <div><div class="k">开锁时限</div><div class="v" style="color:${a.timeLimitOk ? 'var(--ok)' : 'var(--err)'}">${a.timeLimitOk ? '达标' : '未达标'}</div></div></div>
      <div class="op-note">验收通过标准：符合性测试全部通过、时限达标、真车联调的订单与锁车报告全部匹配。</div>`;
  } else {
    rb.append(ui.empty({ icon: '○', title: '还没有真车联调结果', text: runOk ? '符合性测试已全部通过。用规定数量的真车实际骑行后，点右上角「提交真车联调结果」。' : '符合性测试全部通过后，才能用真车联调。先到「联调与测试」完成测试。', action: !runOk && canSubmit ? '去联调与测试' : undefined, onAction: () => go('#/op/integration') }));
  }
  body.append(rides);

  async function submit() {
    const res = await ui.confirm({
      title: '提交真车联调结果', requireReason: false, confirmText: '确认提交',
      message: `<p>提交后，接入智能体会汇总符合性测试与真车联调结果，生成验收报告，交平台公司接入负责人确认。</p><p>请确认：车辆清单已提交、车上政府码已换贴，且真车联调已按规定数量实骑完成。</p>`,
      impact: `${i.brandName} 的验收报告将进入待确认状态；确认通过后品牌上线，运营商须同时关闭自有扫码入口`,
      onConfirm: async () => { await op().integration.submitRealRides(i.brandId); },
    });
    if (res) { ui.toast('已提交，验收报告已生成', { type: 'ok', hint: '等待平台公司接入负责人确认' }); refresh(); }
  }

  /* 验收报告 + 上线状态 */
  const two = ui.el('<div class="op-grid2"></div>');
  const rep = panel('验收报告', '', '<span class="a-actions"></span>');
  const rbody = ui.$('.a-panel-body', rep); rbody.classList.add('op-stack');
  if (a.reportId) {
    const bDl = btn('下载验收报告', 'default', () => {
      const lines = [`验收报告 ${a.reportId}`, '', `品牌：${i.brandName}`, `接入状态：${i.status}`, `生成：接入智能体，${a.submittedAt || '—'}`, '',
        `符合性测试：轮次 ${a.conformanceRunId || '—'}${run ? `，通过 ${run.summary.passed} / ${run.summary.total} 项` : ''}`,
        `真车联调：${a.realVehiclesRidden} / ${a.realVehiclesRequired} 辆，实骑 ${a.realOrders} 单，订单匹配 ${a.matchedOrders} 单，锁车报告匹配 ${a.lockReportsMatched} 单，开锁时限${a.timeLimitOk ? '达标' : '未达标'}`, '',
        `结论：${a.result}${a.confirmedAt ? `，接入负责人于 ${a.confirmedAt} 确认` : '，等待接入负责人确认'}`, '', a.summary || ''];
      saveFile(`${a.reportId}_验收报告.txt`, '﻿' + lines.join('\r\n'), 'text/plain;charset=utf-8');
      ui.toast('验收报告已下载', { type: 'ok', hint: `${a.reportId}_验收报告.txt` });
    });
    ui.$('.a-actions', rep).append(bDl);
    rbody.innerHTML = kv([['报告编号', `<span class="op-mono">${esc(a.reportId)}</span>`], ['生成', `<span class="op-agent">接入智能体</span>${dts(a.submittedAt)}`], ['结论', `${chip(a.result, 'result')}`],
      ['接入负责人确认', a.confirmedAt ? `已于 ${dts(a.confirmedAt)} 确认通过` : a.result === '待确认' ? '等待确认' : '—']]) + `<div class="op-banner is-info"><div class="grow">${esc(a.summary || '')}</div></div><div class="op-note">验收通过由平台公司接入负责人确认；智能体只负责汇总与说明。</div>`;
  } else {
    rbody.append(ui.empty({ icon: '○', title: '还没有验收报告', text: '提交真车联调结果后，由接入智能体生成，交接入负责人确认' }));
  }
  const live = panel('上线状态', '', '');
  const lb = ui.$('.a-panel-body', live); lb.classList.add('op-stack');
  const v = i.vehicleList;
  lb.innerHTML = kv([
    ['接入状态', `${chip(i.status, 'access')} <span class="op-note">${dt(i.statusSince)} 起</span>`],
    ['车辆可租', i.status === '已接入' ? '已开放可租' : i.status === '暂停可租' ? '暂停可租中，恢复后自动开放' : i.status === '联调中' ? '验收确认后开放' : '不可租'],
    ['车辆清单', v && v.submittedAt ? `${ui.number(v.vehicleCount)} 辆，${dt(v.submittedAt)} 提交` : '尚未提交'],
    ['政府码', v && v.vehicleCount ? `已发放 ${ui.number(v.codesIssued)} 个，${esc(v.codeStatus || '')}` : '提交清单后发放'],
    ['自有扫码入口', online ? '应已关闭，用户统一扫政府码' : '上线时由你们关闭，用户统一扫政府码'],
  ]) + `<div><div class="a-sec-title">接入状态记录</div><ul class="a-timeline">${[...(i.statusHistory || [])].reverse().map(h => `<li><div class="t">${dts(h.at)}</div>${chip(h.status, 'access')} <span class="op-note">${esc(h.note || '')}</span></li>`).join('')}</ul></div>`;
  two.append(rep, live); body.append(two);
}

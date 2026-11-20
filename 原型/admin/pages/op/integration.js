// 联调与测试（R-38、R-39）：联调环境凭证、模拟车辆与用户、一键发起符合性测试、逐项结果与失败原因及修改建议。
import { api, ui, esc, op, injectStyle, getMe, can, dt, dts, chip, chipRaw, kv, panel, btn, busy, fail, errText, watch, copyText, nowStr, lacks, go, loadInto, setHead } from './common.js';

const secs = ms => `${(ms / 1000).toFixed(1)} 秒`;

export default {
  path: '#/op/integration', title: '联调与测试', menu: '联调与测试', perm: 'integration.sandbox.own',
  async mount(el, query, pctx) {
    injectStyle();
    el.innerHTML = '';
    setHead(el, pctx, '联调与测试', '在联调环境里用模拟车辆、模拟用户和模拟支付自助联调，全程不产生任何真实收费', []);
    const host = ui.el('<div class="op-stack"></div>');
    el.append(host);
    const state = { brandId: null };

    const load = async () => {
      const me = await getMe();
      const [integs, sandbox] = await Promise.all([op().integration.get(), op().integration.sandbox()]);
      return { me, integs, sandbox };
    };
    const draw = async () => {
      await loadInto(host, load, ({ me, integs, sandbox }, h) => {
        if (!integs.length) { h.append(ui.empty({ icon: '○', title: '还没有接入记录', text: '平台公司为你的品牌建立接入记录后，这里会出现联调环境与测试入口' })); return; }
        if (!state.brandId || !integs.some(i => i.brandId === state.brandId)) state.brandId = integs[0].brandId;
        const tabsBox = ui.el('<div></div>'); const body = ui.el('<div class="op-stack"></div>');
        if (integs.length > 1) h.append(tabsBox);
        h.append(body);
        const show = () => { const i = integs.find(x => x.brandId === state.brandId); body.innerHTML = ''; drawBrand(body, i, sandbox, me, draw); };
        if (integs.length > 1) ui.tabs(tabsBox, integs.map(i => ({ key: i.brandId, label: `${i.brandName}（${i.status}）` })), state.brandId, k => { state.brandId = k; show(); });
        show();
      }, { rows: 8 });
    };
    const off = watch(el, ['integration.changed', 'access.changed'], () => draw(), 800);
    pctx && pctx.onUnmount && pctx.onUnmount(off);
    await draw();
  },
};

async function drawBrand(body, i, sandbox, me, refresh) {
  const cred = (sandbox.credentials || []).find(c => c.brandId === i.brandId) || i.credentials;
  const canRun = can(me, 'integration.conformance.own');
  const blocked = ['已退出', '未接入'].includes(i.status) ? `品牌${i.status}，不能发起测试` : cred.status === '停用' ? '联调凭证已停用，不能发起测试' : me.readonly ? '运营商已退出，账号为只读' : !canRun ? '当前账号没有发起测试的权限' : '';
  const blockedNext = ['已退出', '未接入'].includes(i.status) ? '如有疑问，请联系平台公司接入负责人' : cred.status === '停用' ? '请联系平台公司接入负责人重新开通' : me.readonly ? '如有疑问，请联系主管部门' : '请联系账号管理员调整授权';
  const now = await nowStr().catch(() => null);
  const days = now && i.startedAt ? Math.max(0, Math.floor((new Date(now.replace(' ', 'T')) - new Date(i.startedAt.replace(' ', 'T'))) / 864e5)) : null;

  /* 状态条 */
  body.append(ui.el(`<div class="op-banner ${i.status === '已接入' ? 'is-ok' : i.status === '联调中' ? 'is-info' : 'is-warn'}"><div class="grow"><div class="op-row"><b>${esc(i.brandName)}</b>${chip(i.status, 'access')}<span>${esc(i.accessVia)}${i.providerId ? '（车联网服务商代接）' : ''}</span></div>
    <div style="margin-top:4px" class="op-note">${i.startedAt ? `${dt(i.startedAt)} 开始接入${days != null ? `，已用 ${days} 天，目标 ${i.targetDays} 天内完成验收` : ''}` : '尚未开始接入'}。${i.status === '已接入' ? '品牌已上线，联调环境仍可用于回归测试。' : ''}</div></div></div>`));

  const slotTest = ui.el('<div></div>'); body.append(slotTest);

  /* 凭证 */
  const credBox = ui.el('<div></div>');
  const bRot = btn('轮换密钥', 'default', () => rotate()), bRev = btn('吊销凭证', 'danger', () => revoke());
  const apiRot = typeof op().integration.rotateCredential === 'function', apiRev = typeof op().integration.revokeCredential === 'function';
  const credWhy = cred.status === '停用' ? '凭证已吊销，不能再操作' : me.readonly ? '运营商已退出，账号为只读' : '';
  const setupBtn = (b, has, what) => {
    if (credWhy) ui.setDisabled(b, credWhy, cred.status === '停用' ? '如需恢复，请联系平台公司接入负责人' : '如有疑问，请联系主管部门');
    else if (!has) ui.setDisabled(b, `平台暂未开放自助${what}`, `需要${what}时，请联系平台公司接入负责人办理`);
  };
  setupBtn(bRot, apiRot, '轮换'); setupBtn(bRev, apiRev, '吊销');
  credBox.append(panel('联调环境凭证', kv([
    ['联调凭证编号', `<span class="op-mono">${esc(cred.sandboxAppId)}</span> <button type="button" class="a-btn a-btn-text is-sm" data-cp="${esc(cred.sandboxAppId)}">复制</button>`],
    ['联调密钥', `<span class="op-mono">${esc(cred.sandboxKeyMasked)}</span> <span class="op-note">完整密钥仅在签发或轮换时显示一次</span>`],
    ['状态', `${chip(cred.status, 'result')}${cred.status === '停用' && cred.disabledAt ? ` <span class="op-note">${dt(cred.disabledAt)} 起停用</span>` : ''}`],
    ['签发时间', dt(cred.issuedAt)],
    ['联调环境地址', `<span class="op-mono">${esc(sandbox.baseUrl)}</span> <button type="button" class="a-btn a-btn-text is-sm" data-cp="${esc(sandbox.baseUrl)}">复制</button>`],
    ['正式凭证', cred.prodAppId ? `<span class="op-mono">${esc(cred.prodAppId)}</span>` : '验收通过、上线时发放'],
  ]), `<span class="a-actions"></span>`));
  ui.$('.a-actions', credBox).append(bRot, bRev);
  ui.$$('[data-cp]', credBox).forEach(b => b.addEventListener('click', () => copyText(b.dataset.cp, '内容')));
  body.append(credBox);

  async function rotate() {
    let out = null;
    const res = await ui.confirm({
      title: '轮换联调密钥', confirmText: '确认轮换',
      message: '<p>轮换后，旧密钥立即失效，使用旧密钥的联调请求会被拒绝。新密钥只显示一次，请立即保存到你们的配置里。</p>',
      impact: `${i.brandName} 的联调凭证 ${cred.sandboxAppId}，不影响正式环境`,
      reasons: ['密钥可能泄露', '人员变动', '定期轮换'],
      onConfirm: async reason => { out = await op().integration.rotateCredential(i.brandId, reason); },
    });
    if (res) {
      ui.toast('联调密钥已轮换', { type: 'ok', hint: '旧密钥已失效，请使用新密钥重新联调' });
      const secret = out && (out.secret || out.sandboxKey);
      if (secret) ui.modal({ title: '新的联调密钥', icon: false, body: `<p>密钥只显示这一次，请立即复制保存。</p><div class="op-code">${esc(secret)}</div>`, actions: [{ label: '复制密钥', kind: 'default', keepOpen: true, onClick: () => copyText(secret, '密钥') }, { label: '我已保存', kind: 'primary' }] });
      refresh();
    }
  }
  async function revoke() {
    const res = await ui.confirm({
      title: '吊销联调凭证', danger: true, confirmText: '确认吊销',
      message: '<p>吊销后，这套联调凭证立即停用，联调环境的所有请求都会被拒绝，符合性测试也无法发起。恢复需要联系平台公司接入负责人。</p>',
      impact: `${i.brandName} 的联调凭证 ${cred.sandboxAppId}`,
      reasons: ['密钥已泄露', '不再联调', '人员离职'],
      onConfirm: async reason => { await op().integration.revokeCredential(i.brandId, reason); },
    });
    if (res) { ui.toast('联调凭证已吊销', { type: 'ok' }); refresh(); }
  }

  /* 模拟车辆与用户 */
  const sim = ui.el('<div class="op-grid2"></div>');
  const vp = panel('模拟车辆', '', ''); const up = panel('模拟用户与模拟支付', '', '');
  const vb = ui.$('.a-panel-body', vp); vb.style.padding = '0'; ui.$('.a-panel-body', up).classList.add('op-stack');
  vb.innerHTML = `<div style="overflow:auto"><table class="op-mini" style="margin:0;border:0"><thead><tr><th>编号</th><th>车号</th><th>桩识别标识</th><th>行为</th></tr></thead><tbody>${sandbox.simVehicles.map(v => `<tr><td class="op-mono">${esc(v.simVehicleId)}</td><td class="op-strong">${esc(v.plateNo)}</td><td class="op-mono">${esc(v.pileTag)}</td><td>${esc(v.behavior)}</td></tr>`).join('')}</tbody></table></div>`;
  ui.$('.a-panel-body', up).innerHTML = `<div style="overflow:auto"><table class="op-mini" style="margin:0"><thead><tr><th>编号</th><th>类型</th></tr></thead><tbody>${sandbox.simUsers.map(u => `<tr><td class="op-mono">${esc(u.simUserId)}</td><td>${esc(u.kind)}</td></tr>`).join('')}</tbody></table></div><div class="op-note">${esc(sandbox.simPayment)}</div><div class="op-note">${esc(sandbox.note)}</div>`;
  /* 模拟支付切换：只影响本运营商的联调环境 */
  const pay = ui.el(`<div class="op-stack" style="gap:6px"><div class="op-strong">模拟支付结果</div>
    <div class="a-radio" role="radiogroup" aria-label="模拟支付结果">${[['正常', '扣款全部成功'], ['余额不足', '扣款返回失败，用来测欠费路径']].map(([v, d]) => `<label><input type="radio" name="op-pay-${esc(i.brandId)}" value="${v}" ${(sandbox.paymentMode || '正常') === v ? 'checked' : ''}>${v}<span class="op-note">（${d}）</span></label>`).join('')}</div>
    <div class="op-note" data-pay-note>当前：${esc(sandbox.paymentMode || '正常')}。切换立即生效，只影响本运营商的联调环境，不影响正式环境。</div></div>`);
  const payWhy = me.readonly ? ['运营商已退出，账号为只读', '如有疑问，请联系主管部门'] : !can(me, 'integration.sandbox.own') ? ['当前账号没有修改联调设置的权限', '请联系账号管理员调整授权'] : null;
  ui.$$('input', pay).forEach(r => {
    if (payWhy) { r.closest('label').setAttribute('aria-disabled', 'true'); r.closest('label').dataset.why = payWhy[0]; r.closest('label').dataset.next = payWhy[1]; }
    r.addEventListener('change', async () => {
      const prev = sandbox.paymentMode || '正常';
      if (payWhy) { ui.$$('input', pay).forEach(x => { x.checked = x.value === prev; }); ui.toast(payWhy[0], { type: 'warn', hint: payWhy[1] }); return; }
      const note = ui.$('[data-pay-note]', pay);
      ui.$$('input', pay).forEach(x => { x.disabled = true; });
      note.textContent = '正在切换…';
      try {
        const out = await op().integration.setMockPayment(r.value, `联调切换模拟支付为${r.value}`);
        sandbox.paymentMode = out.paymentMode;
        ui.toast(`模拟支付已切换为「${out.paymentMode}」`, { type: 'ok', hint: out.paymentMode === '余额不足' ? '联调环境的扣款将返回失败，测完记得切回正常' : '联调环境的扣款将全部成功' });
      } catch (e) {
        ui.$$('input', pay).forEach(x => { x.checked = x.value === prev; });
        fail(e, '模拟支付没有切换');
      } finally {
        ui.$$('input', pay).forEach(x => { x.disabled = false; });
        note.textContent = `当前：${sandbox.paymentMode || '正常'}。切换立即生效，只影响本运营商的联调环境，不影响正式环境。`;
      }
    });
  });
  ui.$('.a-panel-body', up).append(pay);
  sim.append(vp, up); body.append(sim);

  /* 符合性测试 */
  const tp = panel('符合性测试', '', '<span class="a-actions"></span>');
  const bRun = btn('发起符合性测试', 'primary', b => run(b));
  if (blocked) ui.setDisabled(bRun, blocked, blockedNext);
  ui.$('.a-actions', tp).append(bRun);
  const tpBody = ui.$('.a-panel-body', tp); tpBody.style.padding = '0';
  slotTest.append(tp);
  let runs = [], sel = null, view = 'all';

  async function loadRuns(keepSel) {
    tpBody.innerHTML = `<div style="padding:16px">${ui.skeleton('lines', 5)}</div>`;
    try { runs = await op().integration.runs({ brandId: i.brandId }); }
    catch (e) { tpBody.innerHTML = ''; tpBody.append(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: () => loadRuns(keepSel) })); return; }
    sel = keepSel && runs.find(r => r.runId === keepSel) ? keepSel : runs[0] && runs[0].runId;
    drawRuns();
  }
  function drawRuns() {
    tpBody.innerHTML = '';
    if (!runs.length) { tpBody.append(ui.empty({ icon: '○', title: '还没有测试记录', text: '点右上角「发起符合性测试」，系统会在联调环境里逐项执行并给出结果' })); return; }
    const r = runs.find(x => x.runId === sel) || runs[0];
    const failed = r.cases.filter(c => c.result === '失败');
    const head = ui.el(`<div style="padding:12px 16px;display:flex;flex-direction:column;gap:12px;border-bottom:1px solid var(--line)">
      <div class="op-row"><div class="op-big" style="color:${failed.length ? 'var(--err)' : 'var(--ok)'}">${r.summary.passed} / ${r.summary.total}</div><div><div class="op-strong">${failed.length ? `${failed.length} 项未通过` : '全部通过'}</div><div class="op-note">${dts(r.at)} · ${esc(r.env)}</div></div>
        <span style="flex:1"></span><label class="op-note" for="op-runsel">测试轮次</label><select id="op-runsel" class="a-select" style="min-width:260px">${runs.map((x, k) => `<option value="${esc(x.runId)}" ${x.runId === r.runId ? 'selected' : ''}>第 ${runs.length - k} 轮 · ${esc(ui.datetime(x.at))} · 通过 ${x.summary.passed}/${x.summary.total}</option>`).join('')}</select></div>
      ${failed.length ? '' : (i.status === '联调中' ? `<div class="op-banner is-ok"><div class="grow">符合性测试已全部通过。下一步：提交车辆清单并完成换码，用真车联调后提交验收。</div><button type="button" class="a-btn a-btn-primary is-sm" data-go>去验收与上线</button></div>` : '')}
      <div class="op-row"><div class="a-tabs" id="op-ct" style="margin:0;border:0"></div></div></div>`);
    tpBody.append(head);
    ui.$('#op-runsel', head).addEventListener('change', e => { sel = e.target.value; view = 'all'; drawRuns(); });
    ui.$('[data-go]', head)?.addEventListener('click', () => go('#/op/acceptance'));
    ui.tabs(ui.$('#op-ct', head), [{ key: 'all', label: '全部用例', count: r.cases.length }, { key: 'fail', label: '仅未通过', count: failed.length }], view, k => { view = k; drawCases(r); });
    tpBody.append(ui.el('<div id="op-cases"></div>'));
    drawCases(r);
  }
  function drawCases(r) {
    const box = ui.$('#op-cases', tpBody); if (!box) return;
    const list = view === 'fail' ? r.cases.filter(c => c.result === '失败') : r.cases;
    if (!list.length) { box.innerHTML = ''; box.append(ui.empty({ icon: '✓', title: '没有未通过的用例', text: '这一轮的全部用例都通过了' })); return; }
    let g = null, out = '';
    list.forEach(c => {
      if (c.group !== g) { g = c.group; out += `<div class="op-case-group">${esc(g)}</div>`; }
      out += `<div class="op-case"><span class="id">${esc(c.caseId)}</span><span>${esc(c.name)}</span><span>${chip(c.result, 'result')}</span><span class="dur">${secs(c.durationMs)}</span>
        ${c.result === '失败' ? `<div class="why"><div><b>失败原因：</b>${esc(c.failReason || '未返回原因')}</div>${c.suggestion ? `<div class="fix"><span class="op-agent">接入智能体</span><b>修改建议：</b>${esc(c.suggestion)}</div>` : ''}<div class="op-note" style="color:inherit;opacity:.8">建议依据接入规范与本次测试结果生成，请对照规范正文核对；修改后重新发起测试。<button type="button" class="op-link" data-docs>查看接入规范</button></div></div>` : ''}</div>`;
    });
    box.innerHTML = out;
    ui.$$('[data-docs]', box).forEach(b => b.addEventListener('click', () => go('#/op/docs')));
  }
  async function run(b) {
    if (b.dataset.busy) return;
    const old = b.innerHTML;
    b.dataset.busy = '1'; b.setAttribute('aria-busy', 'true'); b.innerHTML = '<span class="a-spin"></span>测试进行中';
    ui.setDisabled(b, '测试正在进行，请稍候', '结果出来后会自动显示');
    tpBody.innerHTML = `<div class="op-pending"><span class="a-spin"></span><div><div class="op-strong">正在联调环境中逐项执行</div><div class="op-note">覆盖开锁、结束骑行、锁车事件、车辆清单、重复与延迟请求、补报、降级单与接入安全，请不要关闭页面</div></div></div>${ui.skeleton('lines', 6)}`;
    try {
      const [r] = await Promise.all([op().integration.startRun(i.brandId), new Promise(res => setTimeout(res, 1500))]);
      ui.toast(r.summary.failed ? `测试完成：${r.summary.failed} 项未通过` : `测试完成：${r.summary.total} 项全部通过`, { type: r.summary.failed ? 'warn' : 'ok', hint: r.summary.failed ? '逐项查看失败原因与修改建议' : '可以提交车辆清单并准备真车联调' });
      view = r.summary.failed ? 'fail' : 'all';
      await loadRuns(r.runId);
      tp.scrollIntoView({ block: 'nearest', behavior: 'auto' });
    } catch (e) {
      fail(e, '测试没有完成');
      await loadRuns(sel);
    } finally {
      b.innerHTML = old; delete b.dataset.busy; b.removeAttribute('aria-busy');
      if (blocked) ui.setDisabled(b, blocked, blockedNext); else ui.setEnabled(b);
    }
  }
  loadRuns();
}

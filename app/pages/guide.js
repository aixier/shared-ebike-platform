// 首次使用引导（R-03 R-15 R-16；S-02）：授权、实名、免押开通或缴押、开锁，一页走完，不超过 4 步。
import { ui, api, esc, app, skin, failCard, paySheet, unfreezeSheet, plain } from '../kit.js';

let S = null;
const NAME_RE = /^[一-龥·]{2,20}$/, ID_RE = /^\d{17}[\dXx]$/;

export default {
  route: '#/guide',
  title: '开通骑行',
  async mount(el, p) {
    S = { el, code: p.code || '', pick: 'credit', rule: null, busy: false, fillDone: false };
    el.innerHTML = `<div class="m-body">${ui.skeleton('card', 3)}</div><div class="m-bottom"></div>`;
    await draw();
  },
  unmount() { S = null; },
};

const finish = (credit) => {
  const q = S.code ? `?code=${encodeURIComponent(S.code)}&autounlock=1` : '';
  app().replace('#/scan' + q);
};

async function draw(msg) {
  const my = S;
  let me = null;
  try { me = await api.user.me(); } catch (e) { my.el.innerHTML = ''; my.el.append(ui.retry({ text: '开通信息没有加载出来，请检查网络后重试', onRetry: () => draw() })); return; }
  if (S !== my) return;
  const ok1 = !!me;
  const rn = me && me.realName.status;
  const ok2 = ok1 && rn === '已核验';
  const ok3 = ok2 && (me.deposit.mode === '免押' || me.deposit.mode === '押金');
  if (ok3) { finish(); return; }
  if (ok2 && !ok3 && !my.rule) { try { my.rule = (await api.user.deposit.get()).rule; } catch { /* 规则取不到时按推荐项展示 */ } if (S !== my) return; }
  const step = !ok1 ? 1 : !ok2 ? 2 : 3;
  const el = my.el;
  const prov = me && me.channel === '支付宝' ? '芝麻信用' : '微信支付分';
  const idle = skin() === 'alipay' ? '支付宝' : '微信';
  const done = (n, t, s) => `<div class="sh-guide-step is-done"><div class="h"><span class="n">✓</span><b>${t}</b><span>${s}</span></div></div>`;
  const todo = (n, t, s) => `<div class="sh-guide-step is-todo"><div class="h"><span class="n">${n}</span><b>${t}</b><span>${s}</span></div></div>`;
  let cards = '';
  cards += ok1 ? done(1, '授权手机号', `${esc(me.phoneMasked)} 已授权`) : `<div class="sh-guide-step is-active"><div class="h"><span class="n">1</span><b>授权手机号</b></div><p class="m-hint">用${idle}授权手机号登录，不用输入验证码。</p></div>`;
  if (ok2) cards += done(2, '实名核验', `${esc(me.name || '')} ${esc(me.realName.idMasked || '')}`);
  else if (step === 2) cards += `<div class="sh-guide-step is-active" id="rnCard"><div class="h"><span class="n">2</span><b>实名核验</b><span>需年满 16 周岁</span></div>
      <form class="m-fields" id="rnForm" novalidate autocomplete="off">
        <label class="m-field"><span class="m-lab">姓名</span><input name="name" autocomplete="name" placeholder="与身份证一致" maxlength="20"></label>
        <label class="m-field"><span class="m-lab">身份证号</span><input name="idNo" inputmode="text" autocomplete="off" placeholder="18 位身份证号" maxlength="18"></label>
      </form>
      <button type="button" class="sh-link" id="fillBtn">使用${idle}已实名的信息填入</button>
      <p class="m-hint">仅用于核验年龄与身份，不会向运营商提供。</p></div>`;
  else cards += todo(2, '实名核验', '');
  if (ok3) cards += done(3, '免押或押金', me.deposit.mode === '免押' ? '已开通免押' : '已缴押金');
  else if (step === 3) {
    const amt = my.rule ? my.rule.depositAmount : 199, thr = my.rule ? my.rule.creditFreeThreshold : 0;
    cards += `<div class="sh-guide-step is-active"><div class="h"><span class="n">3</span><b>免押或押金</b><span>二选一</span></div>
      <button type="button" class="sh-opt" role="radio" data-opt="credit" aria-checked="${my.pick === 'credit'}"><span class="r"></span><span><b>信用免押 <span class="chip brand">推荐</span></b><span class="s">用${prov}开通${thr ? `，${thr} 分以上即可` : ''}，不用交押金</span></span></button>
      <button type="button" class="sh-opt" role="radio" data-opt="deposit" aria-checked="${my.pick === 'deposit'}"><span class="r"></span><span><b>缴押金 ${ui.money(amt)}</b><span class="s">押金存平台专户，退押后原路退回</span></span></button>
      <div id="stepMsg"></div></div>`;
  } else cards += todo(3, '免押或押金', '');
  cards += todo(4, '开锁', '开通后自动开锁，开始计时');

  const label = !ok1 ? '一键授权手机号' : step === 2 ? '核验并继续' : my.pick === 'credit' ? '开通免押并开锁' : `缴押金 ${ui.money(my.rule ? my.rule.depositAmount : 199)} 并开锁`;
  el.innerHTML = `<div class="m-body"><div class="m-steps" aria-label="第 ${step} 步，共 4 步">${[1, 2, 3, 4].map(i => `<i class="${i <= step ? 'is-on' : ''}"></i>`).join('')}</div>
    <div class="m-section-title" style="margin-top:0">第 ${step} 步，共 4 步 · ${['', '授权手机号', '实名核验', '免押或押金'][step]}</div>${cards}</div>
    <div class="m-bottom"><button type="button" class="m-btn m-btn-primary" id="goBtn">${label}</button></div>`;
  const btn = ui.$('#goBtn', el);
  ui.$$('[data-opt]', el).forEach(b => b.addEventListener('click', () => { my.pick = b.dataset.opt; draw(true); }));
  if (msg && typeof msg === 'object' && step === 3) {
    const host = ui.$('#stepMsg', el);
    // 信用分不足：已替用户切到“缴押金”，提示一句原因，下一步就是底部唯一的主按钮
    const f = ui.el(`<div class="m-notice" role="alert"><span class="m-ic">i</span><span>${esc(msg.message)}${msg.code === 'CREDIT_SCORE_LOW' ? '。已为你切换到缴押金，点下方按钮即可' : ''}</span></div>`);
    host.append(f); ui.shake(f);
  }
  if (!ok1) btn.addEventListener('click', () => doAuth(btn));
  else if (step === 2) {
    btn.addEventListener('click', () => doVerify(btn));
    ui.$('#fillBtn', el).addEventListener('click', fillIn);
    ui.$('#rnForm', el).addEventListener('submit', e => { e.preventDefault(); doVerify(btn); });
    setTimeout(() => { const i = ui.$('#rnForm input', el); i && S === my && i.focus({ preventScroll: true }); }, 120);
  } else btn.addEventListener('click', () => doDeposit(btn, me));
}

function busy(btn, text) { S.busy = true; btn.setAttribute('aria-busy', 'true'); btn.dataset.old = btn.textContent; btn.innerHTML = `<span class="m-spin"></span>${text}`; }
function idle(btn) { S.busy = false; btn.removeAttribute('aria-busy'); btn.textContent = btn.dataset.old || btn.textContent; }

async function doAuth(btn) {
  if (S.busy) return;
  const u = await app().authorize();
  if (u) { ui.toast('已授权手机号', { type: 'ok' }); await draw(true); }
}

function fillIn() {
  const idle = skin() === 'alipay' ? '支付宝' : '微信';
  ui.mDialog({ title: `使用${idle}实名信息？`, text: `将把你在${idle}已实名的姓名和身份证号填入，核验前可以修改。`, actions: [
    { label: '不用了', kind: 'secondary' },
    { label: '允许', kind: 'primary', onClick() {
      const f = ui.$('#rnForm', S.el); if (!f) return;
      f.elements.name.value = '张伟'; f.elements.idNo.value = '110101199003071233';
      ui.$$('.m-field.is-error', f).forEach(x => x.classList.remove('is-error')); ui.$$('.m-field-msg', f).forEach(x => x.remove());
      ui.toast('已填入，请核对后继续');
    } },
  ] });
}

async function doVerify(btn) {
  if (S.busy) return;
  const f = ui.$('#rnForm', S.el);
  const r = ui.validate(f, {
    name: { label: '姓名', required: true, pattern: NAME_RE, message: '请填写证件上的中文姓名' },
    idNo: { label: '身份证号', required: true, pattern: ID_RE, message: '身份证号应为 18 位' },
  });
  if (!r.ok) { ui.shake(f); return; }
  busy(btn, '核验中');
  const my = S;
  try {
    const res = await api.user.verifyRealName({ name: r.values.name, idNo: r.values.idNo.toUpperCase() });
    if (S !== my) return;
    ui.vibrate();
    if (res.deferred) {
      ui.mDialog({ title: '实名核验稍后完成', text: res.text || '你可以先骑，核验未通过将冻结账户并追缴本次费用。', actions: [{ label: '知道了，去开锁', kind: 'primary', onClick: () => app().replace('#/scan' + (my.code ? `?code=${encodeURIComponent(my.code)}` : '')) }] });
      idle(btn); return;
    }
    my.busy = false;
    await draw(true);
  } catch (e) {
    if (S !== my) return;
    idle(btn);
    if (e.code === 'UNDER_AGE') { blocked(e); return; }
    const card = ui.$('#rnCard', S.el);
    if (e.code === 'REALNAME_INVALID_ID') {
      const item = f.elements.idNo.closest('.m-field'); item.classList.add('is-error');
      item.after(ui.el(`<div class="m-field-msg" role="alert">${esc(e.message)}</div>`)); f.elements.idNo.focus(); ui.shake(f); return;
    }
    const old = ui.$('.m-fail', card); old && old.remove();
    const fc = failCard(plain(e, { next: e.next || { label: '重新输入', action: 'retry' } }), { sub: '', onNext: () => { fc.remove(); f.elements.name.focus(); } });
    card.append(fc); ui.shake(fc);
  }
}

function blocked(e) {
  const el = S.el;
  el.innerHTML = '<div class="m-body"></div><div class="m-bottom"></div>';
  const fc = failCard(plain(e, { next: { label: '知道了', action: 'close' } }), { tone: 'err', sub: '', onNext: () => app().replace('#/scan') });
  ui.$('.m-body', el).append(fc); ui.shake(fc); ui.vibrate();
  ui.$('.m-body', el).append(ui.el('<p class="m-hint" style="text-align:center">满 16 周岁后可重新核验，核验通过即可骑行。</p>'));
}

async function doDeposit(btn, me) {
  if (S.busy) return;
  const my = S;
  if (my.pick === 'credit') {
    busy(btn, '开通中');
    try {
      const r = await api.user.openCreditFree();
      if (S !== my) return;
      ui.toast(`已开通免押，信用分 ${r.score}`, { type: 'ok' }); ui.vibrate();
      finish();
    } catch (e) {
      if (S !== my) return;
      idle(btn);
      if (e.code === 'CREDIT_SCORE_LOW') { my.pick = 'deposit'; await draw(e); return; }
      ui.toast(e.message, { type: 'err', hint: e.next ? e.next.label : '' });
    }
  } else {
    const amt = my.rule ? my.rule.depositAmount : 199;
    const res = await paySheet({ amount: amt, subject: '缴纳押金', desc: '押金存平台专户，可随时申请退还', run: () => api.user.payDeposit() });
    if (S !== my || !res) return;
    finish();
  }
}

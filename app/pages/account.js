// 账户：实名信息、冻结与自助解冻指引、注销账户（有欠费、骑行中、未退押金先提示处理）（R-14、R-48）
import * as ui from '../../shared/ui.js';
import { api } from '../../shared/api.js';
import { paySheet as nativePay } from '../kit.js';

const T = s => (s ? String(s).replace(' ', 'T') : s);
const go = (route, params = {}) => {
  const q = new URLSearchParams(Object.entries(params).filter(([, v]) => v != null && v !== '')).toString();
  location.hash = route + (q ? '?' + q : '');
};
function pageRoot(el) {
  if (el.classList.contains('m-page')) return el;
  const p = document.createElement('div'); p.className = 'm-page'; el.append(p); return p;
}
function style(id, css) {
  if (document.getElementById(id)) return;
  const s = document.createElement('style'); s.id = id; s.textContent = css; document.head.append(s);
}

style('p-account', `
.a-prof{display:flex;gap:14px;align-items:center}
.a-av{flex:none;width:56px;height:56px;border-radius:50%;background:var(--orange-100);color:var(--orange-700);display:grid;place-items:center;font-size:24px;font-weight:700}
.a-prof b{display:block;font-size:18px}
.a-prof span{display:block;font-size:13px;color:var(--ink-3)}
.a-frz{background:var(--surface);border:1.5px solid #FCA5A5;border-radius:var(--r-lg);padding:16px;display:flex;flex-direction:column;gap:8px;box-shadow:var(--sh-1)}
.a-frz.is-shake{animation:aShake 280ms var(--ease)}
@keyframes aShake{0%,100%{transform:none}25%{transform:translateX(-3px)}75%{transform:translateX(3px)}}
.a-frz h4{font-size:16px;display:flex;justify-content:space-between;align-items:center;gap:8px}
.a-frz p{font-size:14px;color:var(--ink-2)}
.a-frz .acts{display:grid;gap:8px;margin-top:4px}
.a-frz .acts .m-btn{height:46px;font-size:16px}
.a-sec{font-size:15px;font-weight:600;margin-bottom:4px}
.a-hist{display:flex;gap:10px;padding:8px 0;border-bottom:1px dashed var(--line);font-size:14px}
.a-hist:last-child{border:0}
.a-hist small{display:block;font-size:12px;color:var(--ink-3)}
.a-bl{display:flex;gap:10px;align-items:flex-start;padding:8px 0;border-bottom:1px dashed var(--line);font-size:14px}
.a-bl:last-child{border:0}
.a-bl i{flex:none;width:22px;height:22px;border-radius:50%;background:var(--err-bg);color:var(--err);display:grid;place-items:center;font-style:normal;font-size:12px;font-weight:700;margin-top:1px}
.a-bl .t{flex:1;min-width:0}
.a-bl .m-btn{width:auto;height:44px;padding:0 14px;font-size:14px;flex:none}
.a-gone{display:flex;flex-direction:column;align-items:center;text-align:center;gap:10px;padding:48px 24px}
.a-gone .ic{width:72px;height:72px;border-radius:50%;background:var(--disabled-bg);display:grid;place-items:center;font-size:32px}
.a-gone p{font-size:14px;color:var(--ink-2);max-width:280px}
.a-cancel{margin-top:4px;color:var(--err)}
.a-cancel:hover{background:var(--err-bg);color:var(--err)}
@media (prefers-reduced-motion:reduce){.a-frz.is-shake{animation:none}}
`);

let alive = false, offs = [];

const NEXT_ROUTE = { goRide: '#/ride', payArrears: '#/debt', viewArrears: '#/debt', payOrder: '#/bill', viewDeposit: '#/deposit', refundDeposit: '#/deposit', contactCS: '#/chat', viewOrders: '#/orders', unfreezeGuide: '#/account', onboarding: '#/guide', verifyRealName: '#/guide' };
const routeOf = n => {
  const base = n && NEXT_ROUTE[n.action];
  return base ? { route: base, params: { ...(n.params || {}), ...(n.orderId ? { orderId: n.orderId } : {}) } } : null;
};
const REAL_CHIP = { 已核验: 'ok', 待核验: 'warn', 未通过: 'err', 未实名: 'mute' };
const dt = s => (s ? ui.datetime(T(s)) : '—');

async function mount(el) {
  unmount();
  alive = true;
  const root = pageRoot(el);
  root.innerHTML = '<div class="m-body" id="a-body"></div>';
  const body = root.querySelector('#a-body');
  let A = null, cancelled = false;

  async function load(silent) {
    if (cancelled) return;
    if (!silent) body.innerHTML = ui.skeleton('card', 3);
    try {
      const a = await api.user.account.get();
      if (!alive) return;
      A = a; draw();
    } catch (e) {
      if (!alive) return;
      body.innerHTML = '';
      body.append(e.code === 'NOT_LOGGED_IN'
        ? ui.empty({ icon: '🔑', title: '请先授权登录', text: '登录后即可查看账户信息', action: '去授权登录', onAction: () => go('#/guide') })
        : e.code === 'ACCOUNT_CANCELLED' ? gone()
          : ui.retry({ text: '账户信息没有加载出来，请检查网络后重试', onRetry: () => load() }));
    }
  }

  function gone() {
    const n = document.createElement('div'); n.className = 'a-gone';
    n.innerHTML = '<div class="ic" aria-hidden="true">👋</div><h3>账户已注销</h3><p>已退出登录。注销前的订单与交易记录按法规保留；如需再次使用，重新授权登录即可注册新账户。</p><button type="button" class="m-btn m-btn-primary" style="width:auto;padding:0 28px;height:48px">知道了</button>';
    n.querySelector('button').addEventListener('click', () => go('#/scan'));
    return n;
  }

  function draw() {
    const { user: u, freeze, selfService, cancelBlockers } = A;
    const rn = u.realName;
    let html = `<section class="m-card"><div class="a-prof"><div class="a-av" aria-hidden="true">${ui.esc((u.name || '骑').slice(0, 1))}</div>
      <div><b>${ui.esc(u.name || '未实名用户')}</b><span class="num">${ui.esc(u.phoneMasked)}</span><span>${ui.esc(u.channel)}账号 · ${ui.date(T(u.registeredAt))} 注册</span></div></div></section>`;

    if (u.account.status === '冻结' && freeze) {
      html += `<section class="a-frz" id="a-frz" role="alert"><h4><span>账户已冻结</span><span class="chip err">冻结中</span></h4>
        <p>${ui.esc(freeze.reason)}</p>${freeze.at ? `<p class="m-hint">冻结时间 ${dt(freeze.at)}</p>` : ''}
        <p><b>解冻方法：</b>${ui.esc(freeze.unfreezeHow || '联系客服解冻')}</p>
        <div class="acts">${selfService.map((s, i) => `<button type="button" class="m-btn ${i === 0 ? 'm-btn-primary' : 'm-btn-secondary'}" data-ss="${ui.esc(s.action)}">${ui.esc(s.label)}</button>`).join('')}</div></section>`;
    }

    html += `<section class="m-card"><div class="a-sec">实名信息</div>
      <div class="m-kv"><span>状态</span><span class="chip ${REAL_CHIP[rn.status] || 'mute'}">${ui.esc(rn.status)}</span></div>
      ${rn.idMasked ? `<div class="m-kv"><span>证件号码</span><span class="num">${ui.esc(rn.idMasked)}</span></div>` : ''}
      ${rn.verifiedAt ? `<div class="m-kv"><span>核验时间</span><span class="num">${dt(rn.verifiedAt)}</span></div>` : ''}
      ${rn.method ? `<div class="m-kv"><span>核验方式</span><span>${ui.esc(rn.method)}</span></div>` : ''}
      ${rn.status === '已核验' ? `<div class="m-kv"><span>年龄</span><span>${rn.adult ? '已满 16 周岁' : '未满 16 周岁'}</span></div>` : ''}
      ${rn.status === '待核验' ? `<div class="m-notice" style="margin-top:6px"><span class="m-ic">ⓘ</span><span>请在 ${dt(rn.deadline)} 前完成实名核验，逾期未核验将冻结账户。<button type="button" class="m-link" data-ss="verifyRealName">去核验</button></span></div>` : ''}
      ${rn.status === '未通过' ? `<div class="m-notice is-err" style="margin-top:6px"><span class="m-ic">!</span><span>${ui.esc(rn.failReason || '实名核验没有通过')}。<button type="button" class="m-link" data-ss="verifyRealName">重新核验</button></span></div>` : ''}
      ${rn.status === '未实名' ? `<div class="m-notice" style="margin-top:6px"><span class="m-ic">ⓘ</span><span>完成实名核验后即可骑行。<button type="button" class="m-link" data-ss="verifyRealName">去实名</button></span></div>` : ''}</section>`;

    html += `<div class="m-list">
      <button type="button" class="m-cell has-arrow" data-to="#/orders"><span class="ic">🧾</span><span class="main"><b>我的订单</b><span>骑行记录与扣费明细</span></span></button>
      <button type="button" class="m-cell has-arrow" data-to="#/cards"><span class="ic">🎫</span><span class="main"><b>月卡与优惠券</b><span>${u.monthCard ? '月卡生效中' : '未开通月卡'} · ${u.usableCouponCount} 张可用券</span></span></button>
      <button type="button" class="m-cell has-arrow" data-to="#/deposit"><span class="ic">💰</span><span class="main"><b>押金</b><span>${u.deposit.mode === '免押' ? '信用免押' : u.deposit.mode === '押金' ? `押金 ${ui.money(u.deposit.balance)} · ${u.deposit.status}` : '未开通免押或押金'}</span></span></button>
      <button type="button" class="m-cell has-arrow" data-to="#/chat?entry=账户"><span class="ic">💬</span><span class="main"><b>联系客服</b><span>账户、费用、押金方面的问题</span></span></button></div>`;

    const hist = (u.account.history || []).slice().sort((a, b) => (a.at < b.at ? 1 : -1)).slice(0, 5);
    if (hist.length) html += `<section class="m-card"><div class="a-sec">账户记录</div>${hist.map(h => `<div class="a-hist"><div><b>${ui.esc(h.action)}</b><small>${dt(h.at)}${h.reason ? ' · ' + ui.esc(h.reason) : ''}</small></div></div>`).join('')}</section>`;

    html += '<button type="button" class="m-btn m-btn-ghost a-cancel" id="a-cancel">注销账户</button>';
    body.innerHTML = html;
    body.querySelectorAll('[data-ss]').forEach(b => b.addEventListener('click', () => runSelf(b.dataset.ss)));
    body.querySelectorAll('[data-to]').forEach(b => b.addEventListener('click', () => { const [r, q] = b.dataset.to.split('?'); go(r, Object.fromEntries(new URLSearchParams(q || ''))); }));
    body.querySelector('#a-cancel').addEventListener('click', e => cancelFlow(e.currentTarget));
  }

  /* ---------- 自助解冻 ---------- */
  function runSelf(action) {
    if (action === 'payArrears') return arrearsSheet();
    if (action === 'verifyRealName') return go('#/guide');
    if (action === 'contactCS') return go('#/chat', { entry: '账户' });
  }
  /** 补付欠费：与账单页同一个支付半屏；结清后满足条件自动解冻 */
  async function arrearsSheet() {
    const res = await nativePay({ amount: A.user.arrears, subject: '补付骑行欠费', desc: '结清后账户自动解冻', run: () => api.user.payArrears() });
    if (!res) return;
    const still = res.user && res.user.account && res.user.account.status === '冻结';
    ui.toast('欠费已补付', { type: 'ok', hint: still ? '还需完成上面的其他解冻条件' : '账户已自动解冻' });
    if (alive) await load(true);
  }

  /* ---------- 注销 ---------- */
  function blockersHtml(bl) {
    return `<p style="font-size:14px;color:var(--ink-2)">注销前还需处理下面的事项，处理完即可注销。</p>${bl.map((b, i) => `<div class="a-bl"><i>!</i><span class="t">${ui.esc(b.message)}</span>${routeOf(b.next) ? `<button type="button" class="m-btn m-btn-secondary" data-bl="${i}">${ui.esc(b.next.label)}</button>` : ''}</div>`).join('')}`;
  }
  function blockedSheet(bl) {
    const sh = ui.bottomSheet({
      title: '暂时不能注销', body: blockersHtml(bl),
      footer: [{ label: '确认注销', kind: 'danger', keepOpen: true, onClick() {} }, { label: '返回', kind: 'ghost' }],
    });
    const dbtn = sh.el.querySelector('.m-sheet-foot .m-btn-danger');
    ui.setDisabled(dbtn, bl[0].message, bl[0].next ? bl[0].next.label : '先处理上面的事项');
    sh.body.querySelectorAll('[data-bl]').forEach(b => b.addEventListener('click', () => { const r = routeOf(bl[+b.dataset.bl].next); sh.close('go'); r && go(r.route, r.params); }));
  }
  async function cancelFlow(trigger) {
    if (trigger.getAttribute('aria-busy')) return;
    trigger.setAttribute('aria-busy', 'true');
    let chk;
    try { chk = await api.user.account.cancelCheck(); }
    catch (e) { trigger.removeAttribute('aria-busy'); return ui.toast(e.message || '没有检查成功', { type: 'err', hint: '没有任何变更，请稍后重试' }); }
    trigger.removeAttribute('aria-busy');
    if (!alive) return;
    if (!chk.canCancel) return blockedSheet(chk.blockers);
    const sh = ui.bottomSheet({
      title: '注销账户',
      body: `<div class="m-notice is-err"><span class="m-ic">!</span><span>注销后账户不可恢复，将自动退出登录，不能再用这个账户骑行。</span></div>
        <p style="font-size:14px;color:var(--ink-2)">你的欠费、骑行中订单、押金都已处理完毕。</p>
        <p style="font-size:14px;color:var(--ink-2)">历史订单与交易记录按法规保留，不再对你展示。</p>
        <label class="m-check"><input type="checkbox" id="a-ok"><span>我已了解，确认注销这个账户</span></label><div id="a-err"></div>`,
      footer: [
        { label: '确认注销', kind: 'danger', keepOpen: true, async onClick(L, b) {
          const err = sh.body.querySelector('#a-err'); err.innerHTML = '';
          if (!sh.body.querySelector('#a-ok').checked) { err.innerHTML = '<div class="m-notice"><span class="m-ic">ⓘ</span><span>请先勾选确认</span></div>'; ui.shake(err.firstElementChild); return; }
          const old = b.innerHTML; b.setAttribute('aria-busy', 'true'); b.innerHTML = '<span class="m-spin"></span>注销中';
          cancelled = true;
          try { await api.user.account.cancel({ reason: '用户申请注销' }); L.close('ok'); if (alive) { body.innerHTML = ''; body.append(gone()); } }
          catch (e) {
            cancelled = false; b.removeAttribute('aria-busy'); b.innerHTML = old;
            if (e.code === 'CANCEL_BLOCKED' && e.blockers) { L.close('blocked'); setTimeout(() => blockedSheet(e.blockers), 260); }
            else { err.innerHTML = `<div class="m-notice is-err"><span class="m-ic">!</span><span>${ui.esc(e.message || '没有注销成功')}，账户没有任何变化。</span></div>`; ui.shake(err.firstElementChild); }
          }
        } },
        { label: '再想想', kind: 'ghost' },
      ],
    });
  }

  offs.push(api.on('session.changed', () => { if (alive && A) load(true); }));
  offs.push(api.on('data.reset', () => { if (alive) location.reload(); }));
  await load();
}

function unmount() {
  alive = false;
  offs.forEach(f => { try { f(); } catch { /* 已取消 */ } });
  offs = [];
}

export const route = '#/account';
export const title = '账户';
export { mount, unmount };
export default { route, title, mount, unmount };

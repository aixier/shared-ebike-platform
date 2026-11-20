// 用户端小程序外壳：启动页、微信/支付宝外观、授权、路由、手势返回、"我的"入口、推送横幅。
// 页面模块放在 ./pages/<名称>.js，默认导出 { route, title, root?, mount(el, params), unmount() }。
// 页面可用 window.__app：go(hash) replace(hash) back() refresh() setTitle(t) openMine() authorize()
//   getMe() refreshMe() skin channel。支付半屏、欠费浮层等公共件在 ./kit.js。
import * as ui from '../shared/ui.js';
import { api } from '../shared/api.js';

const qs = new URLSearchParams(location.search);
const SKIN = qs.get('p') === 'alipay' ? 'alipay' : 'wechat';
const CHANNEL = SKIN === 'alipay' ? '支付宝' : '微信';
const DEFAULT_PHONE = SKIN === 'alipay' ? '13060167604' : '13612097326';
const DEFAULT_CODE = 'SF86020226';
const PAGES = ['scan', 'guide', 'ride', 'bill', 'debt', 'orders', 'order-detail', 'cards', 'deposit', 'account', 'chat', 'refund-account'];
const sleep = ms => new Promise(r => setTimeout(r, ms));
const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches;
const $ = id => document.getElementById(id);
const esc = ui.esc;

const root = $('app'), view = $('view');
root.dataset.skin = SKIN;
$('splashFoot').textContent = SKIN === 'alipay' ? '支付宝小程序' : '微信小程序';
document.title = '共享骑行';

/* ---------------- 胶囊 ---------------- */
const ICON_MORE = '<svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="4" cy="10" r="1.7" fill="currentColor"/><circle cx="10" cy="10" r="1.7" fill="currentColor"/><circle cx="16" cy="10" r="1.7" fill="currentColor"/></svg>';
const ICON_WX_CLOSE = '<svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="10" cy="10" r="6.6" fill="none" stroke="currentColor" stroke-width="1.8"/><circle cx="10" cy="10" r="2.4" fill="currentColor"/></svg>';
const ICON_ALI_CLOSE = '<svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="10" cy="10" r="6.6" fill="none" stroke="currentColor" stroke-width="1.8"/><path d="M7.4 7.4l5.2 5.2M12.6 7.4l-5.2 5.2" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>';
$('capMore').innerHTML = ICON_MORE;
$('capClose').innerHTML = SKIN === 'alipay' ? ICON_ALI_CLOSE : ICON_WX_CLOSE;
$('capsule').classList.toggle('is-alipay', SKIN === 'alipay');

/* ---------------- 状态 ---------------- */
const S = { me: null, mods: {}, cur: null, stack: [], idx: -1, replace: false, noAnim: false, seq: 0 };

async function refreshMe() {
  try { S.me = await api.user.me(); } catch { /* 保持原状 */ }
  return S.me;
}
async function tickClock() {
  try { const t = await api.system.time(); $('clockText').textContent = String(t.now).slice(11, 16); } catch { /* 忽略 */ }
}

/* ---------------- 路由 ---------------- */
function parseHash() {
  const raw = location.hash.slice(1) || '';
  const [path, q] = raw.split('?');
  return { path, query: Object.fromEntries(new URLSearchParams(q || '')) };
}
const go = h => { location.hash = h.replace(/^#?/, '#'); };
const replace = h => {
  const nh = '#' + h.replace(/^#?/, '');
  if (nh === location.hash) return refresh();
  S.replace = true; location.replace(location.pathname + location.search + nh);
};
function back() { S.idx > 0 ? history.back() : replace('#/scan'); }
const canBack = () => S.idx > 0 && S.cur && !S.cur.root;
const setTitle = t => { $('navTitle').textContent = t; };

async function loadPages() {
  await Promise.all(PAGES.map(async n => {
    const url = new URL(`./pages/${n}.js`, import.meta.url);
    try {
      // 页面名单固定，文件不存在时跳过；网络抖动时再试一次
      const r = await fetch(url, { method: 'HEAD' }).catch(() => new Promise(res => setTimeout(res, 300)).then(() => fetch(url, { method: 'HEAD' })));
      if (!r.ok) return;
      const m = (await import(url.href)).default;
      if (m && m.route) S.mods[m.route.replace(/^#/, '')] = m;
    } catch (e) { console.error('页面模块加载失败', n, e); }
  }));
}

function notFoundPage() {
  return { title: '共享骑行', root: true, mount(el) {
    const b = ui.el('<div class="m-body"></div>');
    b.append(ui.empty({ icon: '🧭', title: '这个页面不存在', text: '链接可能已失效，回到首页扫码用车。', action: '回到扫码用车', onAction: () => replace('#/scan') }));
    el.append(b);
  } };
}

async function render() {
  const full = location.hash || '#/';
  const { path, query } = parseHash();
  let dir = 'fwd';
  if (S.replace) { S.replace = false; S.stack[S.idx] = full; }
  else if (S.stack[S.idx - 1] === full) { S.idx--; dir = 'back'; }
  else if (S.stack[S.idx + 1] === full) { S.idx++; }
  else { S.stack.length = S.idx + 1; S.stack.push(full); S.idx++; }
  const my = ++S.seq;
  closeStaleSheets();
  const mod = S.mods[path] || notFoundPage();
  const old = S.cur;
  if (old) { try { old.mod.unmount && old.mod.unmount(); } catch (e) { console.error(e); } }
  const el = document.createElement('div');
  el.className = 'm-page';
  const animate = old && !reduced() && !S.noAnim;
  if (animate) { el.classList.add(dir === 'back' ? 'is-enter-back' : 'is-enter-fwd'); old.el.classList.add(dir === 'back' ? 'is-leave-back' : 'is-leave-fwd'); }
  const oldEl = old && old.el;
  S.noAnim = false;
  view.append(el);
  const isRoot = typeof mod.root === 'function' ? mod.root(query) : !!mod.root;
  S.cur = { mod, el, hash: full, root: isRoot };
  if (oldEl) { if (animate) setTimeout(() => oldEl.remove(), 230); else oldEl.remove(); }
  setTitle(mod.title || '共享骑行');
  $('navBack').hidden = !canBack();
  root.dataset.route = path;
  try { await mod.mount(el, { ...query }); }
  catch (e) {
    console.error('页面渲染失败', path, e);
    if (my === S.seq) { el.replaceChildren(ui.retry({ text: '页面没有加载出来，请检查网络后重试', onRetry: () => render() })); }
  }
}
const refresh = () => { S.replace = true; return render(); };
/** 换页时收起上一页留下的半屏与对话框（如手势返回、系统返回时）；支付、授权这类不可关闭的浮层不动 */
function closeStaleSheets() {
  for (let i = 0; i < 4; i++) {
    const open = root.querySelector('.m-sheet:not(.is-out) .m-sheet-x, .m-dialog:not(.is-out)');
    if (!open) return;
    const before = root.querySelectorAll('.m-sheet:not(.is-out), .m-dialog:not(.is-out)').length;
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    if (root.querySelectorAll('.m-sheet:not(.is-out), .m-dialog:not(.is-out)').length >= before) return;
  }
}

addEventListener('hashchange', render);
addEventListener('keydown', e => { if (e.key === 'Tab') document.body.classList.add('kbd'); }, true);
addEventListener('pointerdown', () => document.body.classList.remove('kbd'), true);
$('navBack').addEventListener('click', back);

/* 手势返回：从左边缘向右拖，超过 90px 返回 */
let drag = null;
view.addEventListener('pointerdown', e => {
  if (!canBack() || e.pointerType === 'mouse' && e.button !== 0) return;
  const r = view.getBoundingClientRect();
  if (e.clientX - r.left > 26) return;
  drag = { x: e.clientX, y: e.clientY, moved: false, el: S.cur.el };
});
addEventListener('pointermove', e => {
  if (!drag) return;
  const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
  if (!drag.moved) {
    if (Math.abs(dy) > Math.abs(dx) + 8) { drag = null; return; }
    if (dx > 8) { drag.moved = true; drag.el.classList.add('is-dragging'); }
  }
  if (drag && drag.moved) { drag.el.style.transform = `translateX(${Math.max(0, dx)}px)`; }
});
const endDrag = e => {
  if (!drag) return;
  const d = drag; drag = null;
  if (!d.moved) return;
  const dx = e.clientX - d.x;
  d.el.classList.remove('is-dragging');
  if (dx > 90) { S.noAnim = true; d.el.style.transform = ''; back(); }
  else { d.el.classList.add('is-springback'); d.el.style.transform = ''; setTimeout(() => d.el.classList.remove('is-springback'), 240); }
};
addEventListener('pointerup', endDrag);
addEventListener('pointercancel', endDrag);
view.style.touchAction = 'pan-y';

/* ---------------- 授权 ---------------- */
function authPhone() {
  let p = localStorage.getItem('bike.app.phone');
  if (p === null) p = DEFAULT_PHONE;
  if (!p || p === 'new') { p = '1' + ['35', '36', '37', '38', '39', '50', '51', '58', '59', '86', '87', '88', '89'][Math.floor(Math.random() * 13)] + String(Math.floor(10000000 + Math.random() * 89999999)); localStorage.setItem('bike.app.phone', p); }
  return p;
}
const maskPhone = p => `${p.slice(0, 3)}****${p.slice(7)}`;
let authBusy = null;
/** 弹出授权手机号半屏（微信）或对话框（支付宝），允许后登录；返回用户或 null */
function authorize() {
  if (authBusy) return authBusy;
  authBusy = new Promise(resolve => {
    const phone = authPhone();
    let settled = false;
    const finish = v => { if (!settled) { settled = true; authBusy = null; resolve(v); } };
    const doAllow = async () => {
      try {
        const r = await api.user.authorize({ channel: CHANNEL, phone });
        S.me = r.user; ui.vibrate();
        finish(r.user); return true;
      } catch (e) { ui.toast(e.message, { type: 'err', hint: e.code === 'AUTH_UNAVAILABLE' ? '稍后再试，已登录的账户不受影响' : '' }); return false; }
    };
    if (SKIN === 'wechat') {
      const body = ui.el(`<div style="display:contents"><div class="sh-auth-head"><div class="sh-auth-icon" aria-hidden="true"><svg viewBox="0 0 48 48" width="26" height="26"><circle cx="12" cy="32" r="7" fill="none" stroke="#2A1405" stroke-width="3.4"/><circle cx="36" cy="32" r="7" fill="none" stroke="#2A1405" stroke-width="3.4"/><path d="M12 32l7-14h10l7 14M19 18l5 14M27 18h5" fill="none" stroke="#2A1405" stroke-width="3.4" stroke-linecap="round" stroke-linejoin="round"/></svg></div><div><b>共享骑行 申请获取你的手机号</b><span>用于登录骑行账户，不会用于其他用途</span></div></div>
        <div class="sh-auth-row"><span class="dot">✓</span><div><b class="num">${maskPhone(phone)}</b><span>微信绑定的手机号</span></div></div>
        <button type="button" class="sh-btn" data-allow>允许</button><button type="button" class="sh-btn is-ghost" data-deny>拒绝</button></div>`);
      const sh = ui.bottomSheet({ title: '手机号快速验证', body, closable: false });
      sh.el.classList.add('sh-native');
      const allow = ui.$('[data-allow]', body);
      allow.addEventListener('click', async () => { allow.setAttribute('aria-busy', 'true'); if (await doAllow()) sh.close('ok'); else allow.removeAttribute('aria-busy'); });
      ui.$('[data-deny]', body).addEventListener('click', () => { sh.close('deny'); finish(null); });
    } else {
      const d = ui.mDialog({ title: '共享骑行 申请获取你的手机号', text: `用于登录骑行账户（${maskPhone(phone)}），不会用于其他用途`, actions: [
        { label: '拒绝', kind: 'secondary', onClick: () => finish(null) },
        { label: '允许', kind: 'primary', onClick: () => doAllow().then(ok => ok ? true : false) },
      ] });
      d.el.classList.add('sh-native-ali');
    }
  });
  return authBusy;
}

/* ---------------- "我的" 与胶囊菜单 ---------------- */
async function openMine() {
  await refreshMe();
  const me = S.me;
  if (!me) { await authorize(); await refreshMe(); if (!S.me) return; return openMine(); }
  const dep = me.deposit.mode === '免押' ? `<span class="chip ok">已免押</span>` : me.deposit.mode === '押金' ? `<span class="chip brand">押金 ${ui.money(me.deposit.balance)}</span>` : '<span class="chip warn">未开通免押</span>';
  const mc = me.monthCard && me.monthCard.status === '生效中' ? '<span class="chip brand">月卡生效中</span>' : '';
  const cell = (ic, t, end, hash, extra = '') => `<button type="button" class="m-cell has-arrow" data-go="${hash}"><span class="ic">${ic}</span><span class="main"><b>${t}</b>${extra ? `<span>${extra}</span>` : ''}</span><span class="end">${end || ''}</span></button>`;
  const body = ui.el(`<div style="display:contents">
    <div class="sh-mine-head"><div class="sh-avatar" aria-hidden="true">${esc((me.name || '骑')[0])}</div><div><b>${esc(me.name || '骑行用户')}</b><span>${esc(me.phoneMasked)} · ${esc(me.channel)}账户</span><div class="sh-chips">${dep}${mc}</div></div></div>
    ${me.arrears > 0 ? `<button type="button" class="m-notice is-err" style="border:0;text-align:left;width:100%" data-go="#/debt"><span class="m-ic">!</span><span>有 <b>${ui.money(me.arrears)}</b> 欠费待补付，补付后才能开锁。<u>去补付</u></span></button>` : ''}
    <div class="m-list">
      ${cell('🧾', '我的订单', me.unpaidOrderIds && me.unpaidOrderIds.length ? `<span class="chip warn">${me.unpaidOrderIds.length} 单待付</span>` : '', '#/orders')}
      ${cell('🎫', '月卡与优惠券', me.usableCouponCount ? `${me.usableCouponCount} 张可用` : '', '#/cards')}
      ${cell('💰', '押金', me.deposit.mode === '免押' ? '信用免押' : me.deposit.mode === '押金' ? ui.money(me.deposit.balance) : '未开通', '#/deposit')}
      ${cell('👤', '账户与实名', me.realName.status, '#/account')}
      ${cell('🔔', '消息通知', me.unreadNotices ? `<span class="chip err">${me.unreadNotices} 条未读</span>` : '', 'notices')}
      ${cell('💬', '联系客服', '', '#/chat?entry=' + encodeURIComponent('我的'))}
    </div></div>`);
  const sh = ui.bottomSheet({ title: '我的', body });
  ui.$$('[data-go]', body).forEach(b => b.addEventListener('click', () => { sh.close(); setTimeout(() => (b.dataset.go === 'notices' ? openNotices() : go(b.dataset.go)), 120); }));
}

/** 消息通知：账单、扣款失败、押金抵扣、退押、冻结解冻、超限提醒；打开即标为已读 */
async function openNotices() {
  const body = ui.el(`<div style="display:contents">${ui.skeleton('lines', 4)}</div>`);
  const sh = ui.bottomSheet({ title: '消息通知', body });
  let list;
  try { list = await api.user.notices(); }
  catch { body.replaceChildren(ui.retry({ text: '通知没有加载出来，请检查网络后重试', onRetry: () => { sh.close(); openNotices(); } })); return; }
  if (!list.length) { body.replaceChildren(ui.empty({ icon: '🔔', title: '暂时没有通知', text: '还车账单、扣款结果、押金变动会在这里提醒你' })); return; }
  const at = s => `${+String(s).slice(5, 7)}月${+String(s).slice(8, 10)}日 ${String(s).slice(11, 16)}`;
  body.replaceChildren(ui.el(`<div class="m-list">${list.slice(0, 30).map((n, i) => `<button type="button" class="m-cell${n.orderId ? ' has-arrow' : ''}" data-i="${i}"><span class="main sh-note"><b>${n.read ? '' : '<i class="sh-unread" aria-label="未读"></i>'}${esc(n.title)}</b><span>${esc(n.text || '')}</span><span>${esc(at(n.at))}</span></span></button>`).join('')}</div>`));
  ui.$$('[data-i]', body).forEach(b => b.addEventListener('click', () => {
    const n = list[+b.dataset.i];
    if (n.orderId) { sh.close(); setTimeout(() => go('#/order-detail?orderId=' + encodeURIComponent(n.orderId)), 120); }
  }));
  if (list.some(n => !n.read)) api.user.markNoticesRead().then(refreshMe).catch(() => { /* 下次打开再标记 */ });
}

function openMenu() {
  const body = ui.el(`<div class="m-list" style="display:contents"><div class="m-list">
    <button type="button" class="m-cell" data-k="mine"><span class="ic">👤</span><span class="main"><b>我的</b></span></button>
    <button type="button" class="m-cell" data-k="share"><span class="ic">↗</span><span class="main"><b>转发给朋友</b></span></button>
    <button type="button" class="m-cell" data-k="cs"><span class="ic">💬</span><span class="main"><b>反馈与客服</b></span></button>
    <button type="button" class="m-cell" data-k="reload"><span class="ic">⟳</span><span class="main"><b>重新进入小程序</b></span></button></div></div>`);
  const sh = ui.bottomSheet({ title: '共享骑行', body });
  sh.el.classList.add('sh-native');
  ui.$$('[data-k]', body).forEach(b => b.addEventListener('click', async () => {
    const k = b.dataset.k; sh.close();
    if (k === 'mine') setTimeout(openMine, 120);
    else if (k === 'cs') setTimeout(() => go('#/chat?entry=' + encodeURIComponent('我的')), 120);
    else if (k === 'reload') setTimeout(() => { location.hash = ''; location.reload(); }, 120);
    else if (k === 'share') {
      const url = location.origin + location.pathname;
      try { if (navigator.share) await navigator.share({ title: '共享骑行', text: '扫码即骑，还车自动结算', url }); else { await navigator.clipboard.writeText(url); ui.toast('链接已复制，可以粘贴给朋友', { type: 'ok' }); } }
      catch { /* 用户取消分享 */ }
    }
  }));
}
$('capMore').addEventListener('click', openMenu);
$('capClose').addEventListener('click', () => {
  const ex = ui.el(`<div class="sh-exit" role="dialog" aria-label="小程序已关闭"><p>共享骑行 已退出</p><button type="button" class="m-btn m-btn-secondary">重新打开</button></div>`);
  root.append(ex);
  ui.$('button', ex).addEventListener('click', () => { ex.remove(); location.reload(); });
});

/* ---------------- 推送横幅 ---------------- */
let bannerTimer = 0;
function showBanner(n) {
  const old = root.querySelector('.sh-banner'); old && old.remove();
  clearTimeout(bannerTimer);
  const b = ui.el(`<div class="sh-banner" role="status"><span class="ic" aria-hidden="true">🔔</span><div><b>${esc(n.title)}</b><span>${esc(n.text || '')}</span></div></div>`);
  root.append(b);
  const close = () => { b.classList.add('is-out'); setTimeout(() => b.remove(), 230); };
  b.addEventListener('click', () => { close(); if (n.orderId) go('#/bill?orderId=' + n.orderId); });
  bannerTimer = setTimeout(close, 6000);
}
api.on('notice', async ev => {
  if (!S.me || ev.userId !== S.me.userId || ev.kind === '账单') return;
  try { const list = await api.user.notices({ unreadOnly: true }); const n = list.find(x => x.noticeId === ev.noticeId); if (n) showBanner(n); } catch { /* 忽略 */ }
});
api.on('data.reset', () => { location.hash = ''; location.reload(); });
api.on('session.changed', async () => { await refreshMe(); });

/* ---------------- 调试面板（仅 ?debug=1） ---------------- */
async function initDebug() {
  if (!ui.isDebug) return;
  const dbg = api.system.debug;
  const panel = ui.el('<aside class="dbg-panel" aria-label="调试面板"><h4>调试</h4></aside>');
  const add = (label, fn) => { const b = ui.el(`<button type="button">${esc(label)}</button>`); b.addEventListener('click', async () => { try { await fn(); ui.toast(label + ' 已执行', { type: 'ok' }); } catch (e) { ui.toast(e.message, { type: 'err' }); } }); panel.append(b); };
  const sec = t => panel.append(ui.el(`<h5>${esc(t)}</h5>`));
  const users = await dbg.demoUsers();
  sec('用户');
  const sel = ui.el(`<select aria-label="切换用户">${users.map(u => `<option value="${u.userId}">${esc(u.label)}</option>`).join('')}</select>`);
  panel.append(sel);
  add('切换到该用户并重进', async () => { await dbg.switchUser(sel.value); await refreshMe(); S.stack = []; S.idx = -1; location.hash = S.me && S.me.ridingOrderId ? '#/ride' : '#/scan?code=' + DEFAULT_CODE; location.reload(); });
  add('以新手机号注册', async () => { localStorage.setItem('bike.app.phone', 'new'); await api.user.logout(); location.hash = '#/scan?code=' + DEFAULT_CODE; location.reload(); });
  sec('扫哪辆车');
  const codes = [['可租 · 青禾', 'SF86020226'], ['暂停可租 · 木棉', 'SF54838002'], ['码已失效 · 简途', 'SF71009215'], ['码未启用 · 市民骑行', 'SF27239795'], ['低电量', 'SF59764025'], ['故障', 'SF21960305']];
  const cs = ui.el(`<select aria-label="选择车辆">${codes.map(c => `<option value="${c[1]}">${c[0]}</option>`).join('')}</select>`);
  panel.append(cs);
  add('扫这辆车', () => replace('#/scan?code=' + cs.value));
  sec('平台状态');
  add('进入降级 · 支付通道故障', () => dbg.forceDegrade('支付通道故障'));
  add('进入降级 · 实名核验不可用', () => dbg.forceDegrade('实名核验不可用'));
  add('进入降级 · 押金与免押查询不可用', () => dbg.forceDegrade('押金与免押查询不可用'));
  add('退出降级', () => dbg.forceDegrade(false));
  sec('下一次失败');
  add('开锁：车辆无响应', () => dbg.failNext('unlock.fail', { reason: '车辆无响应' }));
  add('开锁：电量不足', () => dbg.failNext('unlock.fail', { reason: '电量不足' }));
  add('开锁：车辆故障', () => dbg.failNext('unlock.fail', { reason: '车辆故障' }));
  add('开锁：车辆被占用', () => dbg.failNext('unlock.fail', { reason: '车辆被占用' }));
  add('开锁：超时后才成功（0 元）', () => dbg.failNext('unlock.lateSuccess'));
  add('扣款失败（余额不足）', () => dbg.failNext('payment.decline'));
  add('扣款结果未知', () => dbg.failNext('payment.unknown'));
  add('信用分不足', () => dbg.failNext('credit.low'));
  add('实名姓名与证件不一致', () => dbg.failNext('realname.mismatch'));
  add('还车接口网络失败', () => dbg.failNext('user.returnBike'));
  add('清除全部注入', () => dbg.clearInjections());
  sec('还车情形');
  const sim = ui.el('<label><input type="checkbox"> 下次入桩不上锁</label>'); ui.$('input', sim).checked = localStorage.getItem('bike.app.noLock') === '1';
  ui.$('input', sim).addEventListener('change', e => localStorage.setItem('bike.app.noLock', e.target.checked ? '1' : '0')); panel.append(sim);
  const cf = ui.el('<label><input type="checkbox"> 下次入桩与车端判定冲突</label>'); ui.$('input', cf).checked = localStorage.getItem('bike.app.conflict') === '1';
  ui.$('input', cf).addEventListener('change', e => localStorage.setItem('bike.app.conflict', e.target.checked ? '1' : '0')); panel.append(cf);
  sec('时间与数据');
  add('时钟快进 1 小时', () => dbg.advanceClock(60));
  add('时钟快进 24 小时', () => dbg.advanceClock(24 * 60));
  add('重置数据', async () => { await api.system.reset(); location.hash = ''; location.reload(); });
  document.body.append(panel);
}

/* ---------------- 启动 ---------------- */
async function boot() {
  const t0 = performance.now();
  await Promise.all([loadPages(), refreshMe(), tickClock()]);
  setInterval(tickClock, 20000);
  if (!location.hash || location.hash === '#' || location.hash === '#/') {
    history.replaceState(null, '', location.pathname + location.search + (S.me && S.me.ridingOrderId ? '#/ride' : '#/scan?code=' + DEFAULT_CODE));
  }
  S.stack = []; S.idx = -1;
  await initDebug();
  await render();
  await sleep(Math.max(0, (reduced() ? 200 : 1000) - (performance.now() - t0)));
  const sp = $('splash'); sp.classList.add('is-out'); setTimeout(() => sp.remove(), 380);
  if (!S.me) {
    const u = await authorize();
    if (u) { S.me = u; if (u.ridingOrderId && !parseHash().path.startsWith('/ride')) replace('#/ride'); else await refresh(); }
  }
}

window.__app = { skin: SKIN, channel: CHANNEL, go, replace, back, refresh, setTitle, openMine, authorize, getMe: () => S.me, refreshMe, toast: ui.toast };
boot().catch(e => { console.error('启动失败', e); $('splash').replaceChildren(ui.retry({ text: '小程序没有打开，请检查网络后重试', onRetry: () => location.reload() })); });

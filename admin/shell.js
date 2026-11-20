/* 管理端外壳：登录、岗位菜单、路由、顶栏、会话。页面约定与公共工具见 admin/README.md */
import { api, ApiError } from '../shared/api.js';
import { backendEnabled } from '../shared/real/index.js';
const REAL = backendEnabled();
import * as ui from '../shared/ui.js';
import { _bind, kit } from './kit.js';
import { permLabel } from './pages/common/labels.js';

export * from './kit.js';
export { kit };

const PLATFORM = '共享电单车监管平台';
const ROLE_DIR = { 主管部门: 'gov', 运营商: 'op', 平台公司财务: 'fin', 平台公司客服: 'cs', 平台公司运维: 'ops' };
const DIR_LABEL = { gov: '主管部门', op: '运营商', fin: '平台公司财务', cs: '平台公司客服', ops: '平台公司运维', common: '通用' };
const IDLE_MS = 30 * 60 * 1000;
const FAIL_KEY = 'bike.admin.loginFail', REMEMBER_KEY = 'bike.admin.remember';
const MAX_TRY = 5, LOCK_MS = 5 * 60 * 1000;

const $ = ui.$, $$ = ui.$$, esc = ui.esc;
const app = document.getElementById('app');
const S = { me: null, pages: [], byPath: new Map(), home: '', cleanup: null, seq: 0, timers: [], offs: [], fresh: false, expiring: false, debugInit: false };

/* ============================== 接口包装：登录失效统一处理 ============================== */
function wrapAdminApi(obj, top = true) {
  for (const k of Object.keys(obj)) {
    const v = obj[k];
    if (typeof v === 'function') {
      if (top && (k === 'login' || k === 'logout')) continue;
      obj[k] = function (...a) {
        const r = v.apply(this, a);
        return r && typeof r.then === 'function' ? r.catch(e => {
          if (e && S.me) {
            if (e.code === 'ADMIN_NOT_LOGGED_IN') expire('登录已过期，请重新登录。登录后会回到刚才的页面。');
            else if (e.code === 'ACCOUNT_DISABLED') expire('该账号已被停用，请联系平台公司运维账号管理员。');
          }
          throw e;
        }) : r;
      };
    } else if (v && typeof v === 'object' && !Array.isArray(v)) wrapAdminApi(v, false);
  }
}

// 令牌续期失败（过期、停用、会话作废）：回登录页
window.addEventListener('bike:session-lost', e => { if (e.detail && e.detail.who === 'admin') expire('登录已过期，请重新登录。登录后会回到刚才的页面。'); });

/* ============================== 登录页 ============================== */
const readFails = () => { try { return JSON.parse(localStorage.getItem(FAIL_KEY) || '{}'); } catch { return {}; } };
const writeFails = o => localStorage.setItem(FAIL_KEY, JSON.stringify(o));
const lockLeft = u => { const r = readFails()[u]; return r && r.lockedUntil && r.lockedUntil > Date.now() ? r.lockedUntil - Date.now() : 0; };
function recordFail(u) {
  const all = readFails(), r = all[u] && !(all[u].lockedUntil && all[u].lockedUntil <= Date.now()) ? all[u] : { n: 0 };
  r.n += 1;
  if (r.n >= MAX_TRY) { r.lockedUntil = Date.now() + LOCK_MS; r.n = 0; }
  all[u] = r; writeFails(all);
  return r;
}
const clearFail = u => { const all = readFails(); delete all[u]; writeFails(all); };
const fmtLeft = ms => { const s = Math.ceil(ms / 1000); return s >= 60 ? `${Math.floor(s / 60)} 分 ${String(s % 60).padStart(2, '0')} 秒` : `${s} 秒`; };

/* 岗位入口：点岗位直接以该岗位的账号进入；账号清单见 mockdata/accounts.json */
const ENTRY_PWD = ['Shifan', '2026'].join('@');
const LAST_KEY = 'bike.admin.lastEntry';
const ROLE_CARDS = [
  { key: 'gov', title: '主管部门', desc: '价格与规则、站点与围栏、桩、接入状态总览、执法证据', options: [{ u: 'zgbm.zhou', label: '进入主管部门工作台' }] },
  { key: 'opb', title: '运营商 · 业务', desc: '车辆清单、每日订单明细、月度结算单与异议', pick: '选择所属运营商', options: [{ u: 'qhcx.he', label: '青禾出行', note: '青禾、木棉' }, { u: 'clzx.shen', label: '驰路智行', note: '驰路' }, { u: 'gjcd.gong', label: '公交集团车队', note: '市民骑行 · 联调中' }, { u: 'jtgx.wei', label: '简途共享', note: '已退出 · 只读' }] },
  { key: 'opt', title: '运营商 · 技术对接', desc: '接入文档、联调凭证、符合性测试、验收与上线进度', pick: '选择所属运营商', options: [{ u: 'qhcx.deng', label: '青禾出行', note: '青禾、木棉' }, { u: 'lt.yan', label: '驰路智行', note: '驰路 · 服务商代接' }, { u: 'gjcd.li', label: '公交集团车队', note: '市民骑行 · 联调中' }] },
  { key: 'fin', title: '平台公司 · 财务', desc: '结算单生成与发送、异议核实、收单对账、押金专户', options: [{ u: 'cw.lv', label: '进入财务工作台' }] },
  { key: 'cs', title: '平台公司 · 客服', desc: '用户与订单查询、退款确认、强制结束、人工解冻', pick: '选择岗位', options: [{ u: 'kf.su', label: '客服', note: '退款 ¥20.0 以内' }, { u: 'kf.tan', label: '客服主管', note: '可确认超额退款' }] },
  { key: 'ops', title: '平台公司 · 运维', desc: '运行监控、降级与恢复闭合、接入验收、暂停可租', pick: '选择岗位', options: [{ u: 'yw.he', label: '运维值班', note: '降级进退与闭合' }, { u: 'yw.mao', label: '接入负责人', note: '验收确认' }, { u: 'yw.hao', label: '账号管理员', note: '开通与停用账号' }] },
];

function loginErrText(err) {
  if (!err) return '没有进入成功，请再试一次。';
  if (err.code === 'ACCOUNT_DISABLED') return '该岗位的账号已停用，不能进入。请换一个岗位，或联系平台公司运维账号管理员启用。';
  if (err.code === 'NETWORK' || err.code === 'SERVER_BUSY') return '网络不稳定，这次没有进入成功，没有产生任何变更。请再点一次。';
  return err.message || '没有进入成功，请再试一次。';
}

function showLogin({ notice, other = false } = {}) {
  S.me = null; _bind(null, null);
  document.title = `选择岗位 · ${PLATFORM}`;
  const remembered = localStorage.getItem(REMEMBER_KEY) || '';
  const last = localStorage.getItem(LAST_KEY) || '';
  app.className = ''; app.removeAttribute('role');
  const opt = (c, o) => `<button type="button" class="rc-opt${o.u === last ? ' is-last' : ''}" data-u="${esc(o.u)}"><span class="l"><b>${esc(o.label)}</b>${o.note ? `<small>${esc(o.note)}</small>` : ''}</span>${o.u === last ? '<span class="chip brand">上次使用</span>' : ''}<span class="go" aria-hidden="true">›</span></button>`;
  app.innerHTML = `<div class="a-login2">
    <section class="a-login2-brand" aria-hidden="true">
      <div class="logo"><i></i>${esc(PLATFORM)}</div>
      <div><h2>一码接入，统一监管<br>订单、结算、押金全程留痕</h2>
        <p>面向主管部门、运营商与平台公司的业务管理后台。每一次配置、退款、结算调整都有据可查。</p>
        <ul><li>价格规则与站点围栏统一配置，立即对用户生效</li><li>订单、结算与收单对账按同一口径核算</li><li>降级开单与恢复闭合，故障期间仍能骑行</li></ul></div>
      <small>建议使用 Chrome、Edge 最新版浏览器</small>
    </section>
    <section class="a-login2-form"><div class="a-login2-card is-wide" id="lg-card">
      <div><h1>${REAL ? '账号登录' : '选择岗位进入'}</h1></div>
      <div class="lead">${REAL ? '请输入平台开通的账号与密码，菜单与数据范围按账号岗位自动裁剪。' : '点你的岗位即可进入工作台，菜单与数据范围按岗位自动裁剪；进入后可随时在顶栏「切换岗位」。'}</div>
      <div class="a-alert is-info" id="lg-notice" role="status" ${notice ? '' : 'hidden'}>${notice ? esc(notice.text) : ''}</div>
      <div class="a-alert is-err" id="lg-err" role="alert" hidden></div>
      <div class="rc-grid" id="rc-grid" ${REAL ? 'hidden' : ''}>${(REAL ? [] : ROLE_CARDS).map(c => `<section class="rc-card${c.options.length === 1 ? ' is-single' : ''}" aria-labelledby="rc-${c.key}">
        <div class="rc-head"><h2 id="rc-${c.key}">${esc(c.title)}</h2><p>${esc(c.desc)}</p></div>
        ${c.pick ? `<div class="rc-pick">${esc(c.pick)}</div>` : ''}
        <div class="rc-opts">${c.options.map(o => opt(c, o)).join('')}</div></section>`).join('')}</div>
      <details class="a-login-other" id="lg-other" ${other || REAL ? 'open' : ''}><summary ${REAL ? 'hidden' : ''}>使用其他账号登录</summary>
        <form id="lg-form" class="a-form" novalidate autocomplete="on">
          <div class="a-item"><label class="req" for="lg-u">账号</label><input id="lg-u" name="username" class="a-input" autocomplete="username" placeholder="请输入账号" value="${esc(remembered)}" spellcheck="false" autocapitalize="off"></div>
          <div class="a-item"><label class="req" for="lg-p">密码</label><div class="a-pwd"><input id="lg-p" name="password" type="password" class="a-input" autocomplete="current-password" placeholder="请输入密码"><button type="button" id="lg-eye" aria-label="显示密码" aria-pressed="false">显示</button></div></div>
          <div class="row"><label><input type="checkbox" name="remember" ${remembered ? 'checked' : ''}>记住账号</label><button type="button" class="a-link" id="lg-forgot">忘记密码</button></div>
          <button type="submit" class="a-btn a-btn-primary" id="lg-go">登录</button>
        </form></details>
      <div class="a-login2-foot">${esc(PLATFORM)} · 账号由平台公司运维统一开通</div>
    </div></section></div>`;
  const form = $('#lg-form'), errBox = $('#lg-err'), go = $('#lg-go'), card = $('#lg-card'), grid = $('#rc-grid');
  let busy = false, tick = null;
  const showErr = t => { errBox.hidden = !t; errBox.textContent = t || ''; if (t) { card.classList.remove('is-shake'); void card.offsetWidth; card.classList.add('is-shake'); } };
  const enter = me => { clearInterval(tick); S.fresh = true; history.replaceState(null, '', location.pathname + location.search); startShell(me); };

  /* 岗位卡片：点一下直接进入 */
  grid.addEventListener('click', async e => {
    const b = e.target.closest('.rc-opt'); if (!b || busy) return;
    busy = true; showErr('');
    const opts = $$('.rc-opt', grid);
    opts.forEach(x => { if (x !== b) x.setAttribute('aria-disabled', 'true'); });
    b.setAttribute('aria-busy', 'true'); const old = b.innerHTML;
    b.innerHTML = `<span class="l"><b>${esc($('b', b).textContent)}</b><small>正在进入…</small></span><span class="a-spin" aria-hidden="true"></span>`;
    try {
      await api.admin.login(b.dataset.u, ENTRY_PWD);
      let me; try { me = await api.admin.me(); } catch { me = await api.admin.me(); }
      localStorage.setItem(LAST_KEY, b.dataset.u);
      enter(me);
    } catch (err) {
      b.innerHTML = old; b.removeAttribute('aria-busy');
      opts.forEach(x => x.removeAttribute('aria-disabled'));
      showErr(loginErrText(err)); b.focus();
    } finally { busy = false; }
  });

  /* 其他账号：账号密码 */
  $('#lg-eye').addEventListener('click', e => { const p = form.elements.password, show = p.type === 'password'; p.type = show ? 'text' : 'password'; e.currentTarget.textContent = show ? '隐藏' : '显示'; e.currentTarget.setAttribute('aria-pressed', String(show)); e.currentTarget.setAttribute('aria-label', show ? '隐藏密码' : '显示密码'); });
  $('#lg-forgot').addEventListener('click', () => ui.modal({ title: '忘记密码', body: '为了账号安全，密码不能在登录页自助找回。<br>请联系平台公司运维账号管理员重置密码；重置后首次登录请立即在「我的账号」里修改。', actions: [{ label: '知道了', kind: 'primary' }] }));
  $('#lg-other').addEventListener('toggle', e => { if (e.currentTarget.open) setTimeout(() => (form.elements.username.value ? form.elements.password : form.elements.username).focus(), 30); });

  /* 锁定倒计时：同一账号连续输错 5 次锁定 5 分钟 */
  const syncLock = () => {
    clearInterval(tick);
    const u = form.elements.username.value.trim().toLowerCase(); const left = u ? lockLeft(u) : 0;
    if (!left) { ui.setEnabled(go); if (errBox.dataset.lock) { showErr(''); delete errBox.dataset.lock; } return; }
    const paint = () => {
      const l = lockLeft(u);
      if (!l) { clearInterval(tick); ui.setEnabled(go); showErr(''); delete errBox.dataset.lock; return; }
      errBox.dataset.lock = '1'; errBox.hidden = false; errBox.textContent = `该账号因连续输错密码已锁定，请 ${fmtLeft(l)} 后再试，或联系平台公司运维账号管理员解锁。`;
      ui.setDisabled(go, '该账号因连续输错密码已暂时锁定', `${fmtLeft(l)} 后可再次登录，或联系运维账号管理员`);
    };
    paint(); tick = setInterval(paint, 1000);
  };
  form.elements.username.addEventListener('input', syncLock);
  syncLock();
  setTimeout(() => { const f = other ? form.elements.username : $('.rc-opt.is-last', grid); f && f.focus({ preventScroll: true }); }, 30);

  form.addEventListener('submit', async e => {
    e.preventDefault();
    if (busy) return;
    const r = ui.validate(form, { username: { label: '账号', required: true }, password: { label: '密码', required: true } });
    if (!r.ok) { showErr(''); return; }
    const u = r.values.username.toLowerCase();
    if (lockLeft(u)) { syncLock(); return; }
    busy = true; go.setAttribute('aria-busy', 'true'); go.innerHTML = '<span class="a-spin"></span>登录中'; showErr('');
    try {
      await api.admin.login(r.values.username, r.values.password);
      let me; try { me = await api.admin.me(); } catch { me = await api.admin.me(); }
      clearFail(u);
      r.values.remember ? localStorage.setItem(REMEMBER_KEY, r.values.username) : localStorage.removeItem(REMEMBER_KEY);
      enter(me);
    } catch (err) {
      let msg;
      if (err.code === 'LOGIN_FAILED') {
        const rec = recordFail(u);
        msg = rec.lockedUntil ? `密码连续输错 ${MAX_TRY} 次，该账号已锁定 5 分钟。请稍后再试，或联系平台公司运维账号管理员。` : `账号或密码不正确，还可以尝试 ${MAX_TRY - rec.n} 次；连续输错 ${MAX_TRY} 次将锁定 5 分钟。`;
      } else if (err.code === 'ACCOUNT_DISABLED') msg = '该账号已停用，不能登录。请联系平台公司运维账号管理员启用。';
      else msg = loginErrText(err);
      form.elements.password.value = '';
      showErr(msg); syncLock();
      form.elements.password.focus();
    } finally { busy = false; go.removeAttribute('aria-busy'); go.textContent = '登录'; }
  });
}

/* ============================== 页面注册与权限 ============================== */
async function loadPages(me) {
  const dir = ROLE_DIR[me.account.post];
  const dirs = [dir, 'common'].filter(Boolean);
  const list = [];
  for (const d of dirs) {
    let arr = [];
    try { const m = await import(`./pages/${d}/index.js`); arr = m.default || m.pages || []; } catch (e) { arr = []; }
    for (const p of arr) {
      if (!p || !p.path || typeof p.mount !== 'function') continue;
      list.push({ ...p, dir: d, path: p.path.startsWith('#') ? p.path : '#' + p.path });
    }
  }
  S.pages = list; S.byPath = new Map(list.map(p => [p.path, p]));
}
const allowed = p => (!p.perm || kit.can(p.perm)) && (!p.roles || p.roles.includes(S.me.account.role));
const visiblePages = () => S.pages.filter(p => p.menu && allowed(p));
function homePath() {
  const role = S.pages.find(p => p.dir !== 'common' && p.menu && allowed(p));
  const any = role || S.pages.find(p => p.menu && allowed(p));
  return any ? any.path : '#/common/me';
}
function parseHash() {
  const raw = location.hash || '';
  const [p, qs] = raw.split('?');
  return { path: (p || '').replace(/\/+$/, ''), query: Object.fromEntries(new URLSearchParams(qs || '')) };
}
/** 解析路径：ok / denied / notfound */
function resolve(path) {
  const page = S.byPath.get(path);
  if (page) return allowed(page) ? { kind: 'ok', page } : { kind: 'denied', why: 'perm', page };
  const seg = (path.split('/')[1] || '');
  const mine = ROLE_DIR[S.me.account.post];
  if (DIR_LABEL[seg] && seg !== mine && seg !== 'common') return { kind: 'denied', why: 'role', dir: seg };
  return { kind: 'notfound' };
}

/* ============================== 外壳 ============================== */
function startShell(me) {
  S.me = me; _bind(me, kit.go);
  app.className = ''; app.innerHTML = '<div class="a-boot" role="status"><span class="a-spin-lg"></span><span>正在加载你的工作台…</span></div>';
  loadPages(me).then(() => {
    if (S.me !== me) return;
    S.home = homePath();
    buildShell();
    const { path } = parseHash();
    const need = !path || path === '#' || path === '#/' || (S.fresh && resolve(path).kind !== 'ok');
    S.fresh = false;
    if (need && location.hash !== S.home) location.replace(S.home); else navigate();
  });
}

function buildShell() {
  const a = S.me.account, op = S.me.operator;
  app.innerHTML = `<div class="a-shell" id="shell">
    <aside class="a-side" id="side"><div class="a-brand"><i></i><span>监管平台</span></div><nav class="a-menu" id="menu" aria-label="主菜单"></nav></aside>
    <div class="a-side-mask" id="side-mask"></div>
    <header class="a-top">
      <button type="button" class="a-nav-toggle" id="nav-toggle" aria-label="打开菜单" aria-expanded="false">☰</button>
      <div class="a-scope" id="scope"></div><span class="a-grow"></span>
      <button type="button" class="a-btn a-btn-default is-sm a-switch-role" id="switch-role" title="回到岗位选择，换一个岗位进入">切换岗位</button>
      <div class="a-pop-wrap"><button type="button" class="a-todo" id="todo-btn" aria-haspopup="true" aria-expanded="false">待办<b id="todo-n" hidden>0</b></button><div class="a-pop" id="todo-pop" hidden></div></div>
      <div class="a-pop-wrap"><button type="button" class="a-user" id="user-btn" aria-haspopup="true" aria-expanded="false"><span class="av">${esc(String(a.name || '?')[0])}</span><span class="nm"><span>${esc(a.name)}</span><small>${esc(a.role)}</small></span></button><div class="a-pop" id="user-pop" hidden></div></div>
    </header>
    <main class="a-main" id="view">
      ${S.me.readonly ? '<div class="a-alert is-info a-ro-bar" role="status"><span class="grow">运营商已退出，当前账号为只读：可以查看历史订单、结算单和凭证，不能提交或修改。</span></div>' : ''}
      <div class="a-crumb" id="crumb" aria-label="当前位置"></div>
      <div class="a-head"><div><h1 id="h1"></h1><div class="sub" id="sub" hidden></div></div><div class="a-actions" id="acts"></div></div>
      <div id="slot"></div>
    </main></div>`;
  $('#scope').innerHTML = op ? `<span class="chip brand">${esc(op.shortName || op.name)}</span><span>${esc(op.name || '')}</span><span class="a-mute">只显示本运营商的数据</span>` : `<span>${esc(a.org || '')}</span>`;
  $('#user-pop').innerHTML = `<div class="who"><div class="n">${esc(a.name)}</div><div class="m">${esc(a.role)}${op ? ' · ' + esc(op.shortName || op.name) : ''}</div><div class="m">账号 ${esc(a.username)} · ${esc(a.org || '')}</div></div>
    <button type="button" class="it" data-go="#/common/me">我的账号</button><button type="button" class="it" data-switch>切换岗位</button><button type="button" class="it" id="logout">退出登录</button>`;
  paintMenu(); paintTodos();

  const shell = $('#shell');
  const closeNav = () => { shell.classList.remove('is-nav-open'); $('#nav-toggle').setAttribute('aria-expanded', 'false'); };
  $('#nav-toggle').addEventListener('click', () => { const o = shell.classList.toggle('is-nav-open'); $('#nav-toggle').setAttribute('aria-expanded', String(o)); });
  $('#side-mask').addEventListener('click', closeNav);
  $('#menu').addEventListener('click', closeNav);

  const pops = [['#todo-btn', '#todo-pop'], ['#user-btn', '#user-pop']];
  const closePops = () => pops.forEach(([b, p]) => { $(p).hidden = true; $(b).setAttribute('aria-expanded', 'false'); });
  pops.forEach(([b, p]) => $(b).addEventListener('click', e => { e.stopPropagation(); const open = $(p).hidden; closePops(); $(p).hidden = !open; $(b).setAttribute('aria-expanded', String(open)); }));
  const onDoc = e => { if (!e.target.closest('.a-pop-wrap')) closePops(); };
  const onKey = e => { if (e.key === 'Escape') closePops(); };
  document.addEventListener('click', onDoc); document.addEventListener('keydown', onKey);
  S.offs.push(() => { document.removeEventListener('click', onDoc); document.removeEventListener('keydown', onKey); });
  $('#user-pop').addEventListener('click', e => {
    const g = e.target.closest('[data-go]'); if (g) { closePops(); location.hash = g.dataset.go; }
    if (e.target.closest('#logout')) logout();
    if (e.target.closest('[data-switch]')) switchRole();
  });
  $('#switch-role').addEventListener('click', switchRole);
  $('#todo-pop').addEventListener('click', e => {
    const it = e.target.closest('[data-menu]'); if (!it) return; closePops();
    const p = visiblePages().find(x => x.menu === it.dataset.menu) || visiblePages().find(x => x.menu.includes(it.dataset.menu) || it.dataset.menu.includes(x.menu));
    p ? (location.hash = p.path) : ui.toast('没有找到对应的页面', { type: 'warn', hint: '请从左侧菜单进入相关功能' });
  });

  addEventListener('hashchange', navigate);
  S.offs.push(() => removeEventListener('hashchange', navigate));

  /* 待办与会话保活 */
  S.timers.push(setInterval(() => refreshMe(), 45000));
  let deb = null;
  S.offs.push(api.on('*', ev => {
    if (ev.name === 'data.reset') { location.reload(); return; }
    if (ev.name === 'session.changed' && ev.remote) { refreshMe(true); return; }
    if (/^audit\.|^session/.test(ev.name)) return;
    clearTimeout(deb); deb = setTimeout(() => refreshMe(), 1200);
  }));
  /* 长时间无操作自动退出 */
  let last = Date.now(), th = 0;
  const touch = () => { const n = Date.now(); if (n - th > 4000) { th = n; last = n; } };
  ['pointerdown', 'keydown', 'wheel'].forEach(ev => addEventListener(ev, touch, { passive: true }));
  S.offs.push(() => ['pointerdown', 'keydown', 'wheel'].forEach(ev => removeEventListener(ev, touch)));
  S.idleCheck = async () => { try { await api.admin.logout(); } catch { /* 已退出 */ } expire('长时间没有操作，为了账号安全已退出登录，请重新登录。'); };
  S.timers.push(setInterval(() => { if (Date.now() - last > IDLE_MS) S.idleCheck(); }, 30000));
}

function paintMenu() {
  const menu = $('#menu'); if (!menu) return;
  const todoOf = label => (S.me.todos || []).filter(t => t.menu === label).reduce((s, t) => s + t.count, 0);
  const groups = [];
  const role = visiblePages().filter(p => p.dir !== 'common'), common = visiblePages().filter(p => p.dir === 'common');
  const dir = ROLE_DIR[S.me.account.post];
  if (role.length) {
    const sub = new Map();
    role.forEach(p => { const k = p.group || DIR_LABEL[dir]; (sub.get(k) || sub.set(k, []).get(k)).push(p); });
    sub.forEach((ps, k) => groups.push([k, ps]));
  }
  if (common.length) groups.push(['通用', common]);
  const cur = parseHash().path;
  menu.innerHTML = groups.length ? groups.map(([k, ps]) => `<div class="a-menu-group">${esc(k)}</div>${ps.map(p => { const n = todoOf(p.menu); return `<a href="${esc(p.path)}" data-path="${esc(p.path)}"${p.path === cur ? ' class="is-on" aria-current="page"' : ''}><span>${esc(p.menu)}</span>${n ? `<span class="a-badge" aria-label="${n} 项待办">${n}</span>` : ''}</a>`; }).join('')}`).join('') : '<div class="a-menu-empty">暂无可用的功能页面</div>';
}
function markMenu(path) {
  $$('#menu a').forEach(a => { const on = a.dataset.path === path; a.classList.toggle('is-on', on); on ? a.setAttribute('aria-current', 'page') : a.removeAttribute('aria-current'); });
}
function paintTodos() {
  const todos = S.me.todos || [], total = todos.reduce((s, t) => s + t.count, 0), n = $('#todo-n');
  if (!n) return;
  n.hidden = !total; n.textContent = total > 99 ? '99+' : total;
  $('#todo-pop').innerHTML = todos.length ? `<h4>需要你处理</h4>${todos.map(t => `<button type="button" class="it" data-menu="${esc(t.menu)}"><span>${esc(t.label)}</span><b>${t.count}</b></button>`).join('')}` : '<div class="none">暂时没有待办事项</div>';
}
async function refreshMe(checkAccount) {
  if (!S.me) return;
  try {
    const me = await api.admin.me();
    if (!S.me) return;
    if (checkAccount && me.account.accountId !== S.me.account.accountId) { location.reload(); return; }
    S.me = me; _bind(me, kit.go); paintTodos(); paintMenu(); markMenu(parseHash().path);
  } catch { /* 网络抖动时保留现有待办；登录失效由接口包装处理 */ }
}

/* ============================== 路由与页面挂载 ============================== */
function closeOverlays() {
  for (let i = 0; i < 4 && $('.a-drawer:not(.is-out), .a-modal:not(.is-out)'); i++) document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
}
function setHead({ title, sub = '', crumb }) {
  $('#h1').textContent = title;
  const s = $('#sub'); s.textContent = sub; s.hidden = !sub;
  $('#acts').innerHTML = '';
  $('#crumb').innerHTML = crumb.map((c, i) => (i ? '<span class="sep">/</span>' : '') + (c.href ? `<a href="${esc(c.href)}">${esc(c.label)}</a>` : `<span${i === crumb.length - 1 ? ' class="cur" aria-current="page"' : ''}>${esc(c.label)}</span>`)).join('');
  document.title = `${title} · ${PLATFORM}`;
}
function renderPanel(node) { const slot = $('#slot'); slot.innerHTML = ''; node.classList.add('a-view'); slot.append(node); return node; }

async function navigate() {
  if (!S.me || !$('#slot')) return;
  const my = ++S.seq;
  const { path, query } = parseHash();
  closeOverlays();
  const sh = $('#shell'); if (sh && sh.classList.contains('is-nav-open')) { sh.classList.remove('is-nav-open'); $('#nav-toggle').setAttribute('aria-expanded', 'false'); }
  if (S.cleanup) { try { S.cleanup(); } catch (e) { console.error(e); } S.cleanup = null; }
  if (!path || path === '#' || path === '#/') { location.replace(S.home); return; }
  markMenu(path);
  $('#view').scrollTop = 0;
  const res = resolve(path);
  const homeCrumb = { label: '首页', href: S.home };
  if (res.kind !== 'ok') {
    setHead({ title: res.kind === 'denied' ? '没有访问权限' : '页面不存在', crumb: [homeCrumb, { label: res.kind === 'denied' ? '没有访问权限' : '页面不存在' }] });
    renderPanel(deniedPanel(res)); return;
  }
  const page = res.page, offs = [];
  const area = page.dir !== 'common' ? (page.group || DIR_LABEL[page.dir]) : '通用';
  setHead({ title: page.title, sub: page.sub || '', crumb: page.path === S.home ? [{ label: area }, { label: page.title }] : [homeCrumb, { label: area }, { label: page.title }] });
  const content = renderPanel(document.createElement('div'));
  const pctx = {
    account: S.me.account, me: S.me, operator: S.me.operator, readonly: S.me.readonly, can: kit.can, go: kit.go, query,
    setSub(t) { const s = $('#sub'); if (s) { s.textContent = t || ''; s.hidden = !t; } },
    setActions(x) { const box = $('#acts'); if (!box) return; box.innerHTML = ''; [].concat(x || []).forEach(n => typeof n === 'string' ? box.insertAdjacentHTML('beforeend', n) : box.append(n)); },
    setCrumb(tail) { const c = $('#crumb'); if (!c) return; $$('.tail', c).forEach(n => n.remove()); const curSpan = $('.cur', c); if (curSpan && tail) { curSpan.classList.remove('cur'); curSpan.removeAttribute('aria-current'); curSpan.outerHTML = `<a href="${esc(path)}">${esc(page.title)}</a>`; c.insertAdjacentHTML('beforeend', `<span class="sep tail">/</span><span class="cur tail" aria-current="page">${esc(tail)}</span>`); } },
    refreshTodos: () => refreshMe(),
    onUnmount: fn => offs.push(fn),
  };
  let skel = null;
  const st = setTimeout(() => { if (my === S.seq && !content.childElementCount) { skel = ui.el('<div class="a-panel">' + ui.skeleton('lines', 7) + '</div>'); content.append(skel); } }, 90);
  let ret;
  try { ret = await page.mount(content, query, pctx); }
  catch (e) {
    clearTimeout(st); skel && skel.remove();
    if (my !== S.seq) return;
    if (e && (e.code === 'FORBIDDEN' || e.code === 'SCOPE_DENIED')) { content.innerHTML = ''; content.append(deniedPanel({ kind: 'denied', why: 'api', message: e.message })); }
    else if (e && e.code === 'ADMIN_NOT_LOGGED_IN') { /* 已跳转登录 */ }
    else { console.error(e); content.innerHTML = ''; content.append(ui.retry({ text: `页面没有加载出来，没有产生任何变更。${e && e.message ? e.message : ''}`, onRetry: navigate })); }
    return;
  }
  clearTimeout(st); skel && skel.remove();
  const cleanup = () => { offs.splice(0).forEach(f => { try { f(); } catch (e) { console.error(e); } }); typeof ret === 'function' ? ret() : ret && typeof ret.unmount === 'function' ? ret.unmount() : page.unmount && page.unmount(); };
  if (my !== S.seq) { cleanup(); return; }
  S.cleanup = cleanup;
}

function deniedPanel(res) {
  const a = S.me.account;
  let title = '你的账号没有权限访问这个页面', text;
  if (res.kind === 'notfound') { title = '没有找到这个页面'; text = '链接可能已失效或输入有误，请从左侧菜单进入。'; }
  else if (res.why === 'role') text = `当前账号「${a.name}」是${a.role}，这个页面属于「${DIR_LABEL[res.dir]}」岗位。如确需使用，请联系平台公司运维账号管理员开通授权。`;
  else if (res.why === 'perm') { const need = [].concat(res.page.perm || []).map(permLabel).join('、') || '相应的功能权限'; text = `这个页面需要「${need}」权限，你的账号（${a.role}）没有开通。如确需使用，请联系平台公司运维账号管理员开通授权。`; }
  else text = res.message || '该操作超出了你的账号权限。';
  const n = ui.el(`<div class="a-denied" role="alert"><div class="ic" aria-hidden="true">${res.kind === 'notfound' ? '🧭' : '🔒'}</div><h2>${esc(title)}</h2><p>${esc(text)}</p><div class="a-actions"><a class="a-btn a-btn-primary" href="${esc(S.home)}">回到我的首页</a><a class="a-btn a-btn-default" href="#/common/me">查看我的权限</a></div></div>`);
  return n;
}

/* ============================== 退出与会话过期 ============================== */
function teardown() {
  S.timers.splice(0).forEach(clearInterval);
  S.offs.splice(0).forEach(f => { try { f(); } catch { /* 忽略 */ } });
  if (S.cleanup) { try { S.cleanup(); } catch { /* 忽略 */ } S.cleanup = null; }
  closeOverlays();
  S.seq++;
}
async function logout() {
  try { await api.admin.logout(); } catch { /* 即使失败也回到登录页 */ }
  teardown(); history.replaceState(null, '', location.pathname + location.search);
  showLogin({ notice: { text: '你已安全退出。' } });
}
/** 切换岗位：退出当前账号，回到岗位卡片页 */
async function switchRole() {
  try { await api.admin.logout(); } catch { /* 即使失败也回到岗位选择 */ }
  teardown(); history.replaceState(null, '', location.pathname + location.search);
  showLogin();
}
function expire(text) {
  if (S.expiring || !S.me) return;
  S.expiring = true; teardown();
  showLogin({ notice: { text } });
  S.expiring = false;
}

/* ============================== 启动与调试面板 ============================== */
function initDebug() {
  ui.initDebug({
    title: '调试',
    actions: [
      { label: '重置数据', onClick: async () => { await api.system.reset(); location.href = location.pathname + '?debug=1'; } },
      { label: '模拟会话过期', onClick: async () => { await api.admin.logout(); refreshMe(); } },
      { label: '模拟长时间无操作', onClick: () => S.idleCheck && S.idleCheck() },
      { label: '下一次操作失败', onClick: () => api.system.debug.failNext('admin.me') },
    ],
    fields: [{ label: '切换账号（输入编号，如 AC0201）', type: 'text', get: () => '', set: async v => { try { await api.admin.switchAccount(v.trim()); location.reload(); } catch (e) { ui.toast(e.message, { type: 'err' }); } } }],
  });
}

(async function boot() {
  wrapAdminApi(api.admin);
  initDebug();
  try {
    const me = await api.admin.me();
    startShell(me);
  } catch (e) {
    if (e && (e.code === 'ADMIN_NOT_LOGGED_IN' || e.code === 'ACCOUNT_DISABLED')) showLogin(e.code === 'ACCOUNT_DISABLED' ? { notice: { text: '该账号已被停用，请联系平台公司运维账号管理员。' } } : {});
    else {
      app.className = ''; app.innerHTML = '';
      app.append(ui.retry({ text: '管理后台没有打开，网络不稳定，没有产生任何变更。', onRetry: () => location.reload() }));
    }
  }
})();

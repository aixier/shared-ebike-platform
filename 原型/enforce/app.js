// 执法人员手机页：登录、扫码核查、拍照取证、我的取证记录（R-44、R-45，S-09）。看不到用户、订单与资金。
import * as ui from '../shared/ui.js';
import { api } from '../shared/api.js';
import { backendEnabled } from '../shared/real/index.js';
const REAL = backendEnabled();

const T = s => (s ? String(s).replace(' ', 'T') : s);
const MOCK_BASE = new URL('../mockdata/', import.meta.url);
const photoUrl = p => (/^(data:|blob:|https?:)/.test(p) ? p : new URL(p, MOCK_BASE).href);
const dt = s => (s ? ui.datetime(T(s)) : '—');
const RESULT_CLS = { 有效政府码: 'ok', 码未启用: 'warn', 码已失效: 'err', 无政府码: 'err' };
const ACCESS_CLS = { 已接入: 'ok', 联调中: 'warn', 暂停可租: 'warn', 未接入: 'err', 已退出: 'err' };
const RECENT_KEY = 'bike.enforce.recent';
const MAX_PHOTOS = 6;

let R = null;            // 路由器
let officer = null;      // 当前执法人员
let alive = [];          // 当前页面的清理函数

const $ = (s, r = document) => r.querySelector(s);
function page(inner, bottom) {
  const p = document.createElement('div'); p.className = 'm-page';
  p.innerHTML = `<div class="m-body">${inner}</div>${bottom ? `<div class="m-bottom">${bottom}</div>` : ''}`;
  return p;
}
const cleanup = () => { alive.forEach(f => { try { f(); } catch { /* 已清理 */ } }); alive = []; };
const sessionLost = e => e && (e.code === 'ADMIN_NOT_LOGGED_IN' || e.code === 'NOT_LOGGED_IN');
const recents = () => { try { return JSON.parse(localStorage.getItem(RECENT_KEY) || '[]').filter(x => !officer || x.by === officer.accountId); } catch { return []; } };
function pushRecent(code, scanResult, at) {
  let all = []; try { all = JSON.parse(localStorage.getItem(RECENT_KEY) || '[]'); } catch { /* 重新开始 */ }
  all = [{ code, scanResult, at, by: officer && officer.accountId }, ...all.filter(x => !(x.code === code && x.by === (officer && officer.accountId)))].slice(0, 30);
  localStorage.setItem(RECENT_KEY, JSON.stringify(all));
}
function failView(e, onRetry, what = '内容') {
  if (sessionLost(e)) { setTimeout(() => R.go('/login', true), 0); return ui.empty({ icon: '🔑', title: '登录已失效', text: '请重新登录', action: '重新登录', onAction: () => R.go('/login', true) }); }
  return ui.retry({ text: `${what}没有加载出来，请检查网络后重试`, onRetry });
}
function kv(k, v) { return `<div class="f-kv"><span>${k}</span><span>${v}</span></div>`; }
function button(cls, text, onClick, attrs = '') {
  const b = document.createElement('button'); b.type = 'button'; b.className = `m-btn m-btn-${cls}`; b.textContent = text; if (attrs) b.setAttribute('data-x', attrs); b.addEventListener('click', onClick); return b;
}

/* ================= 登录 ================= */
/* 执法终端随警务通下发时已绑定执法账号：默认一键登录，不用输密码；换人使用时再用账号密码登录 */
const DEVICE_KEY = 'bike.enforce.device';
const DEVICE_DEFAULT = { username: 'zf031207', secret: 'Shifan@2026' };
const deviceAccount = () => { try { return { ...DEVICE_DEFAULT, ...JSON.parse(localStorage.getItem(DEVICE_KEY) || '{}') }; } catch { return DEVICE_DEFAULT; } };

function loginPage() {
  const dev = deviceAccount();
  const p = page(`<div class="f-brand"><div class="logo" aria-hidden="true">🛵</div><h1>执法核查</h1><p>共享电单车监管平台 · 执法人员专用</p></div>
    <section class="m-card f-device" id="lg-dev"><div class="av" aria-hidden="true">警</div><div class="info"><b>本机已绑定执法账号</b><span>工号 ${ui.esc(dev.username.replace(/^zf/, ''))} · 终端由所在支队统一下发</span></div></section>
    <form id="lg" class="m-fields" novalidate autocomplete="off" hidden>
      <div class="m-field"><label for="lg-u">执法账号</label><input id="lg-u" name="username" autocomplete="username" autocapitalize="off" placeholder="请输入执法账号"></div>
      <div class="m-field"><label for="lg-p">密码</label><input id="lg-p" name="password" type="password" autocomplete="current-password" placeholder="请输入密码"></div>
    </form>
    <p class="m-hint" style="padding:0 4px" id="lg-hint">登录即记录本次执法人员与终端；账号由平台公司账号管理员开通。</p>`,
  '<button type="button" class="m-btn m-btn-primary" id="lg-one">一键登录</button><button type="submit" form="lg" class="m-btn m-btn-primary" id="lg-go" hidden>登录</button><button type="button" class="m-btn m-btn-ghost" id="lg-switch">使用其他执法账号登录</button>');
  const f = $('#lg', p), btn = $('#lg-go', p), one = $('#lg-one', p), sw = $('#lg-switch', p);
  if (REAL) { // 真实后端：只有账号密码表单，没有本机绑定账号的一键入口
    $('#lg-dev', p).hidden = true; one.hidden = true; sw.hidden = true; f.hidden = false; btn.hidden = false;
    $('#lg-hint', p).textContent = '账号由平台公司账号管理员开通；忘记密码请联系管理员重置。';
  }
  one.addEventListener('click', async () => {
    if (one.getAttribute('aria-busy')) return;
    one.setAttribute('aria-busy', 'true'); one.innerHTML = '<span class="m-spin"></span>登录中';
    try { officer = await api.enforcement.login(dev.username, dev.secret); ui.vibrate(); R.go('/', true); }
    catch (err) {
      one.removeAttribute('aria-busy'); one.textContent = '一键登录';
      ui.toast(err.code === 'ACCOUNT_DISABLED' ? '本机绑定的账号已停用' : '本机账号暂时无法登录', { type: 'err', hint: '请改用账号密码登录' });
      sw.click();
    }
  });
  sw.addEventListener('click', () => {
    const toForm = f.hidden;
    f.hidden = !toForm; $('#lg-dev', p).hidden = toForm; btn.hidden = !toForm; one.hidden = toForm;
    sw.textContent = toForm ? '使用本机绑定账号一键登录' : '使用其他执法账号登录';
    $('#lg-hint', p).textContent = toForm ? '账号由平台公司账号管理员开通；忘记密码请联系管理员重置。' : '登录即记录本次执法人员与终端；账号由平台公司账号管理员开通。';
    if (toForm) setTimeout(() => $('#lg-u', p).focus({ preventScroll: true }), 60);
  });
  f.addEventListener('submit', async e => {
    e.preventDefault();
    if (btn.getAttribute('aria-busy')) return;
    const v = ui.validate(f, { username: { label: '执法账号', required: true }, password: { label: '密码', required: true } });
    if (!v.ok) { ui.shake(f); return; }
    btn.setAttribute('aria-busy', 'true'); btn.innerHTML = '<span class="m-spin"></span>登录中';
    try {
      officer = await api.enforcement.login(v.values.username, v.values.password);
      ui.vibrate();
      R.go('/', true);
    } catch (err) {
      btn.removeAttribute('aria-busy'); btn.textContent = '登录';
      const item = f.elements.password.closest('.m-field'); item.classList.add('is-error');
      const m = document.createElement('div'); m.className = 'm-field-msg'; m.setAttribute('role', 'alert');
      m.textContent = err.code === 'LOGIN_FAILED' ? '账号或密码不正确，请核对后重试；忘记密码请联系账号管理员' : (err.message || '登录没有成功，请稍后重试');
      item.after(m); f.elements.password.addEventListener('input', () => { item.classList.remove('is-error'); m.remove(); }, { once: true });
      f.elements.password.select(); ui.shake(item);
    }
  });
  return p;
}

/* ================= 核查首页 ================= */
function homePage() {
  const p = page(`<section class="m-card f-me" id="me"><div class="av" aria-hidden="true">${ui.esc((officer.name || '执').slice(0, 1))}</div>
      <div class="info"><b>${ui.esc(officer.name)}</b><span>${ui.esc(officer.org || '')} · 工号 ${ui.esc(officer.badgeNo)}</span></div>
      <button type="button" class="m-btn m-btn-ghost" id="out">退出</button></section>
    <div class="m-section-title">扫码核查</div>
    <div class="f-code"><label class="m-field"><span class="m-lab">政府码</span><input id="code" inputmode="text" autocapitalize="characters" autocomplete="off" placeholder="输入车把上的政府码，如 SF12345678" aria-label="政府码"></label>
      <button type="button" class="f-scan" id="scan" aria-label="扫码"><span aria-hidden="true">📷</span>扫码</button></div>
    <p class="m-hint" style="padding:0 4px">车上没有码、码损坏或看不清？直接取证，系统会记录照片、位置和时间。</p>
    <div id="recent"></div>`,
  '<button type="button" class="m-btn m-btn-primary" id="go">核查</button><button type="button" class="m-btn m-btn-ghost" id="nocode">没有政府码，直接取证</button>');
  const code = $('#code', p), go = $('#go', p);
  const sync = () => (code.value.trim() ? ui.setEnabled(go) : ui.setDisabled(go, '还没有输入政府码', '输入车把上的编号，或点“扫码”'));
  sync();
  code.addEventListener('input', sync);
  const check = c => { c = String(c || '').trim().toUpperCase(); if (c) R.go(`/result?code=${encodeURIComponent(c)}`); };
  go.addEventListener('click', () => check(code.value));
  code.addEventListener('keydown', e => { if (e.key === 'Enter') check(code.value); });
  $('#nocode', p).addEventListener('click', () => R.go('/capture?scanResult=' + encodeURIComponent('无政府码')));
  $('#out', p).addEventListener('click', async () => { await api.enforcement.logout().catch(() => {}); officer = null; R.go('/login', true); });
  $('#scan', p).addEventListener('click', () => scanner(c => { code.value = c; sync(); check(c); }));
  const rec = recents().slice(0, 5);
  if (rec.length) {
    $('#recent', p).innerHTML = `<div class="m-section-title">最近核查</div><div class="m-list f-recent">${rec.map(r => `<button type="button" class="m-cell has-arrow" data-c="${ui.esc(r.code)}"><span class="main"><b class="mono">${ui.esc(r.code)}</b><span>${dt(r.at)}</span></span><span class="end"><span class="chip ${RESULT_CLS[r.scanResult] || 'mute'}">${ui.esc(r.scanResult)}</span></span></button>`).join('')}</div>`;
    $('#recent', p).querySelectorAll('[data-c]').forEach(b => b.addEventListener('click', () => check(b.dataset.c)));
  }
  // 已取证条数：以最新为准
  api.enforcement.me().then(m => { officer = { ...officer, ...m }; }).catch(() => {});
  return p;
}

/** 相机扫码：设备支持 BarcodeDetector 时识别二维码，否则提示手动输入 */
async function scanner(onCode) {
  if (!('BarcodeDetector' in window) || !navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    ui.toast('这台设备暂不支持相机扫码', { hint: '请直接输入车把上的政府码' }); return;
  }
  let stream, timer, closed = false;
  const stop = () => { closed = true; clearInterval(timer); stream && stream.getTracks().forEach(t => t.stop()); };
  try {
    stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' }, audio: false });
  } catch {
    ui.toast('没有拿到相机权限', { hint: '请在系统设置允许相机，或直接输入政府码' }); return;
  }
  const detector = new window.BarcodeDetector({ formats: ['qr_code'] });
  const sh = ui.bottomSheet({ title: '扫描车把上的二维码', body: '<video class="f-video" playsinline muted autoplay></video><p class="m-hint">对准二维码，识别后自动核查</p>', onClose: stop });
  const v = $('video', sh.el); v.srcObject = stream;
  timer = setInterval(async () => {
    if (closed || v.readyState < 2) return;
    try { const r = await detector.detect(v); if (r.length) { const raw = r[0].rawValue; stop(); sh.close('scanned'); onCode(raw); } } catch { /* 下一帧再试 */ }
  }, 300);
}

/* ================= 核查结果 ================= */
function resultPage(ctx) {
  const codeIn = ctx.query.code || '';
  const p = page(ui.skeleton('card', 3));
  const body = $('.m-body', p);
  (async function run() {
    body.innerHTML = ui.skeleton('card', 3);
    try {
      const [r, t] = await Promise.all([api.enforcement.scan(codeIn), api.system.time()]);
      if (!p.isConnected && !p.parentNode) { /* 页面已离开，仍写入以便返回 */ }
      pushRecent(r.vehicle ? r.vehicle.govCode : codeIn, r.scanResult, t.now);
      const v = r.vehicle;
      const tone = r.scanResult === '有效政府码' ? 'ok' : r.scanResult === '码未启用' ? 'warn' : 'err';
      body.innerHTML = `<section class="f-banner is-${tone}" role="status"><span class="k">${r.isGovCode ? '核查结果' : '未识别到政府码'}</span><div class="v">${ui.esc(r.scanResult)}</div><p>${ui.esc(r.text)}</p></section>`
        + (v ? `<section class="m-card"><div class="f-sec">车辆与运营商</div>
            ${kv('政府码', `<span class="mono">${ui.esc(v.govCode)}</span>`)}${kv('车牌号', ui.esc(v.plateNo || '—'))}${kv('品牌 · 车型', `${ui.esc(v.brandName)} · ${ui.esc(v.model || '—')}`)}
            ${kv('所属运营商', ui.esc(v.operatorName))}${kv('运营商状态', ui.esc(v.operatorStatus))}
            ${kv('接入状态', `<span class="chip ${ACCESS_CLS[v.accessStatus] || 'mute'}">${ui.esc(v.accessStatus)}</span>`)}
            ${kv('政府码状态', ui.esc(v.codeStatus))}${v.codeIssuedAt ? kv('发码时间', dt(v.codeIssuedAt)) : ''}${v.codeEnableAt ? kv('启用时间', dt(v.codeEnableAt)) : ''}${v.codeInvalidAt ? kv('失效时间', dt(v.codeInvalidAt)) : ''}</section>`
          : `<section class="m-card"><div class="f-sec">核查内容</div>${kv('输入的编号', `<span class="mono">${ui.esc(codeIn || '—')}</span>`)}${kv('核查时间', dt(t.now))}<p class="m-hint" style="margin-top:6px">这辆车没有有效的政府码，不在平台登记范围内。请拍照取证，证据提交后不可修改。</p></section>`);
      const bottom = document.createElement('div'); bottom.className = 'm-bottom';
      if (r.needCapture) {
        bottom.append(button('primary', '拍照取证', () => R.go(`/capture?scanResult=${encodeURIComponent(r.scanResult)}${v ? '&code=' + encodeURIComponent(v.govCode) : ''}`)), button('ghost', '继续核查下一辆', () => R.go('/', true)));
      } else bottom.append(button('primary', '继续核查下一辆', () => R.go('/', true)));
      $('.m-bottom', p)?.remove(); p.append(bottom);
    } catch (e) { body.innerHTML = ''; body.append(failView(e, run, '核查结果')); }
  })();
  return p;
}

/* ================= 拍照取证 ================= */
function readImage(file) {
  return new Promise((res, rej) => {
    const fr = new FileReader();
    fr.onerror = () => rej(new Error('这张照片无法读取'));
    fr.onload = () => {
      const img = new Image();
      img.onerror = () => rej(new Error('这张照片无法读取，请换一张'));
      img.onload = () => {
        const k = Math.min(1, 1280 / Math.max(img.width, img.height));
        const c = document.createElement('canvas'); c.width = Math.round(img.width * k); c.height = Math.round(img.height * k);
        c.getContext('2d').drawImage(img, 0, 0, c.width, c.height);
        res(c.toDataURL('image/jpeg', 0.78));
      };
      img.src = fr.result;
    };
    fr.readAsDataURL(file);
  });
}

function capturePage(ctx) {
  const code = ctx.query.code || '';
  let scanResult = ctx.query.scanResult || '无政府码';
  const p = page(`<div id="cx-ctx"></div>
    <section class="m-card"><div class="f-sec">现场照片 <span class="m-hint" id="cx-cnt"></span></div><div class="f-photos" id="cx-ph"></div>
      <input type="file" id="cx-file" accept="image/*" capture="environment" multiple hidden>
      <p class="m-hint" style="margin-top:8px">至少 1 张，拍清车身、车牌或品牌字样。提交后不能修改或删除。</p></section>
    <section class="m-card"><div class="f-sec">位置与时间</div><div class="f-loc"><div class="t" id="cx-loc"></div><button type="button" class="m-btn m-btn-secondary" id="cx-relocate">重新定位</button></div>
      <div class="f-kv" style="margin-top:6px"><span>取证时间</span><span class="num" id="cx-time">—</span></div></section>
    <div class="m-fields"><label class="m-field"><span class="m-lab">车辆描述（必填）</span><textarea id="cx-desc" maxlength="200" placeholder="品牌字样、车身编号、颜色、停放情况等"></textarea></label>
      <label class="m-field"><span class="m-lab">备注（选填）</span><textarea id="cx-note" maxlength="200" placeholder="补充说明"></textarea></label></div>`,
  '<button type="button" class="m-btn m-btn-primary" id="cx-go" aria-disabled="true" data-why="还不能提交" data-next="补全照片、位置和车辆描述">提交取证</button><p class="m-why" id="cx-why"></p>');
  const photos = []; let loc = null, locState = 'loading', clock = null, busy = false;
  const S = { code, scanResult };
  const sync = () => {
    const miss = [];
    if (!photos.length) miss.push('拍一张照片');
    if (!loc) miss.push(locState === 'loading' ? '等待定位完成' : '重新定位');
    if (!$('#cx-desc', p).value.trim()) miss.push('填写车辆描述');
    const go = $('#cx-go', p), why = $('#cx-why', p);
    if (miss.length) { ui.setDisabled(go, `还不能提交：${miss[0]}`, `${miss.join('、')}后即可提交`); why.textContent = `还差：${miss.join('、')}`; }
    else { ui.setEnabled(go); why.textContent = ''; }
  };
  const drawPhotos = () => {
    const box = $('#cx-ph', p);
    box.innerHTML = photos.map((s, i) => `<div class="f-photo"><button type="button" class="view" data-v="${i}" aria-label="查看第 ${i + 1} 张照片"><img src="${s}" alt="现场照片 ${i + 1}"></button><button type="button" class="x" data-r="${i}" aria-label="删除第 ${i + 1} 张照片"><span aria-hidden="true">✕</span></button></div>`).join('')
      + (photos.length < MAX_PHOTOS ? '<button type="button" class="f-add" id="cx-add"><span aria-hidden="true">＋</span>拍照 / 选择</button>' : '');
    $('#cx-cnt', p).textContent = `${photos.length}/${MAX_PHOTOS}`;
    $('#cx-add', p)?.addEventListener('click', () => $('#cx-file', p).click());
    box.querySelectorAll('[data-r]').forEach(b => b.addEventListener('click', () => { photos.splice(+b.dataset.r, 1); drawPhotos(); sync(); }));
    box.querySelectorAll('[data-v]').forEach(b => b.addEventListener('click', () => ui.bottomSheet({ title: '照片预览', body: `<img class="f-big" src="${photos[+b.dataset.v]}" alt="照片预览">` })));
    sync();
  };
  $('#cx-file', p).addEventListener('change', async e => {
    const files = [...e.target.files].slice(0, MAX_PHOTOS - photos.length); e.target.value = '';
    for (const f of files) {
      try { photos.push(await readImage(f)); } catch (err) { ui.toast(err.message, { type: 'err', hint: '换一张照片再试' }); }
    }
    drawPhotos();
  });
  const drawLoc = () => {
    const el = $('#cx-loc', p);
    if (locState === 'loading') el.innerHTML = '<span class="m-hint">正在定位…</span>';
    else if (locState === 'fail') el.innerHTML = '<span style="color:var(--err)">没有取得位置</span><small>请打开手机定位后点“重新定位”</small>';
    else el.innerHTML = `${ui.esc(loc.address)}<small class="num">${loc.location[0].toFixed(6)}, ${loc.location[1].toFixed(6)} · 精度约 ${loc.accuracyM} 米</small>`;
    sync();
  };
  const locate = async () => {
    locState = 'loading'; loc = null; drawLoc();
    try { loc = await api.enforcement.locate(); locState = 'ok'; } catch (e) { if (sessionLost(e)) return R.go('/login', true); locState = 'fail'; }
    drawLoc();
  };
  $('#cx-relocate', p).addEventListener('click', locate);
  ['#cx-desc', '#cx-note'].forEach(s => $(s, p).addEventListener('input', sync));
  // 时间：以系统时间为准，每秒走字，提交时再取一次
  const tick = async () => { try { const t = await api.system.time(); S.t0 = Date.parse(T(t.now)); S.p0 = performance.now(); } catch { /* 沿用上次 */ } };
  const show = () => { if (S.t0 == null) return; const n = new Date(S.t0 + performance.now() - S.p0); $('#cx-time', p).textContent = ui.datetime(n, true); };
  tick().then(show); clock = setInterval(show, 1000); alive.push(() => clearInterval(clock));

  // 核查情况
  (async () => {
    const box = $('#cx-ctx', p);
    let inner = `<section class="m-card"><div class="f-sec">核查情况</div>${kv('核查结果', `<span class="chip ${RESULT_CLS[scanResult] || 'mute'}">${ui.esc(scanResult)}</span>`)}`;
    if (code) {
      try { const r = await api.enforcement.scan(code); if (r.vehicle) { scanResult = S.scanResult = r.scanResult === '有效政府码' ? scanResult : r.scanResult; inner = `<section class="m-card"><div class="f-sec">核查情况</div>${kv('核查结果', `<span class="chip ${RESULT_CLS[scanResult] || 'mute'}">${ui.esc(scanResult)}</span>`)}${kv('政府码', `<span class="mono">${ui.esc(r.vehicle.govCode)}</span>`)}${kv('品牌 · 运营商', `${ui.esc(r.vehicle.brandName)} · ${ui.esc(r.vehicle.operatorShortName)}`)}`; } } catch (e) { if (sessionLost(e)) return R.go('/login', true); }
    } else inner += kv('说明', '未识别到政府码，按无码车取证');
    box.innerHTML = inner + '</section>';
  })();

  $('#cx-go', p).addEventListener('click', () => {
    if (busy) return;
    const sh = ui.bottomSheet({
      title: '确认提交取证',
      body: `<div class="m-notice"><span class="m-ic">ⓘ</span><span>提交后证据即被锁定，不能修改或删除。请确认照片、位置和描述无误。</span></div>
        ${kv('照片', `${photos.length} 张`)}${kv('位置', ui.esc(loc ? loc.address : ''))}${kv('核查结果', ui.esc(S.scanResult))}<div id="cx-err"></div>`,
      footer: [{ label: '确认提交', kind: 'primary', keepOpen: true, async onClick(L, b) {
        const err = $('#cx-err', sh.el); err.innerHTML = '';
        busy = true; const old = b.innerHTML; b.setAttribute('aria-busy', 'true'); b.innerHTML = '<span class="m-spin"></span>提交中';
        try {
          const t = await api.system.time();
          const ev = await api.enforcement.capture({ scanResult: S.scanResult, govCode: code || undefined, location: loc.location, address: loc.address, accuracyM: loc.accuracyM, vehicleDescription: $('#cx-desc', p).value.trim(), photos, note: $('#cx-note', p).value.trim() || undefined, capturedAt: t.now });
          L.close('ok'); ui.vibrate(); R.go('/done?id=' + encodeURIComponent(ev.evidenceId), true);
        } catch (e) {
          busy = false; b.removeAttribute('aria-busy'); b.innerHTML = old;
          if (sessionLost(e)) { L.close('x'); return R.go('/login', true); }
          err.innerHTML = `<div class="m-notice is-err"><span class="m-ic">!</span><span>${ui.esc(e.message || '没有提交成功')}，证据没有保存，内容还在，可以再提交一次。</span></div>`; ui.shake(err.firstElementChild); b.textContent = '重新提交';
        }
      } }, { label: '再检查一下', kind: 'ghost' }],
    });
  });
  drawPhotos(); drawLoc(); locate();
  return p;
}

/* ================= 提交完成 ================= */
function donePage(ctx) {
  const p = page(ui.skeleton('card', 2));
  const body = $('.m-body', p);
  (async function run() {
    try {
      const e = await api.enforcement.get(ctx.query.id);
      body.innerHTML = `<div class="f-done"><div class="ok" aria-hidden="true">✓</div><h2>证据已提交</h2><p>已锁定，不能修改或删除</p></div>
        <section class="m-card">${kv('证据编号', `<span class="mono">${ui.esc(e.evidenceId)}</span>`)}${kv('取证时间', dt(e.capturedAt))}${kv('工号', ui.esc(e.badgeNo))}${kv('核查结果', `<span class="chip ${RESULT_CLS[e.scanResult] || 'mute'}">${ui.esc(e.scanResult)}</span>`)}
          <div class="f-kv" style="flex-direction:column;gap:2px"><span>防篡改摘要</span><span class="f-hash" style="text-align:left">${ui.esc(e.sha256)}</span></div></section>
        <p class="m-hint" style="padding:0 4px">主管部门可按车辆、运营商、时间查询并导出，交线下处置。</p>`;
      const b = document.createElement('div'); b.className = 'm-bottom';
      b.append(button('primary', '继续核查', () => R.go('/', true)), button('ghost', '查看我的取证记录', () => R.go('/records', true)));
      p.append(b);
    } catch (e) { body.innerHTML = ''; body.append(failView(e, run, '取证结果')); }
  })();
  return p;
}

/* ================= 我的取证记录 ================= */
function recordsPage() {
  const p = page('<div id="rl" style="display:flex;flex-direction:column;gap:12px"></div><div id="rm"></div>');
  const body = $('.m-body', p), list = $('#rl', p), more = $('#rm', p);
  let page_ = 0, total = 0, loaded = 0, busy = false, io = null;
  alive.push(() => io && io.disconnect());
  const item = e => `<button type="button" class="m-cell has-arrow" data-id="${ui.esc(e.evidenceId)}"><span class="f-rec" style="flex:1;min-width:0"><span class="th">${e.photos && e.photos[0] ? `<img src="${ui.esc(photoUrl(e.photos[0]))}" alt="" loading="lazy">` : ''}</span><span class="main"><b>${ui.esc(e.plateNo || e.vehicleDescription || '无码车')}</b><span>${dt(e.capturedAt)} · ${ui.esc(e.address || '')}</span></span></span><span class="end"><span class="chip ${RESULT_CLS[e.scanResult] || 'mute'}">${ui.esc(e.scanResult)}</span></span></button>`;
  async function load(reset) {
    if (busy) return; busy = true;
    if (reset) { page_ = 0; loaded = 0; list.innerHTML = ui.skeleton('card', 4); more.innerHTML = ''; } else { more.innerHTML = ''; more.append(ui.loading('正在加载更多…')); }
    try {
      const r = await api.enforcement.mine({ page: page_ + 1, pageSize: 15 });
      if (reset) list.innerHTML = '';
      page_ = r.page; total = r.total; loaded += r.rows.length; more.innerHTML = '';
      if (!total) { list.append(ui.empty({ icon: '🗂️', title: '还没有取证记录', text: '核查无码车或失效码的车辆后，取证记录会出现在这里', action: '去核查', onAction: () => R.go('/') })); return; }
      const g = list.querySelector('.m-list') || Object.assign(document.createElement('div'), { className: 'm-list' });
      if (!g.parentNode) list.append(g);
      g.insertAdjacentHTML('beforeend', r.rows.map(item).join(''));
      g.querySelectorAll('[data-id]').forEach(b => { if (!b.dataset.bound) { b.dataset.bound = 1; b.addEventListener('click', () => R.go('/record/' + encodeURIComponent(b.dataset.id))); } });
      if (loaded < total) {
        const w = document.createElement('div'); w.className = 'f-more'; w.append(button('secondary', '加载更多', () => load(false))); w.firstChild.classList.add('is-sm'); more.append(w);
        if ('IntersectionObserver' in window) { io && io.disconnect(); io = new IntersectionObserver(es => { if (es.some(x => x.isIntersecting)) load(false); }, { root: body, rootMargin: '0px 0px 100px 0px' }); io.observe(w); }
      } else if (total > 15) more.innerHTML = '<div class="f-end">没有更多记录了</div>';
    } catch (e) {
      if (reset) { list.innerHTML = ''; list.append(failView(e, () => load(true), '取证记录')); } else { more.innerHTML = ''; more.append(ui.retry({ text: '更多记录没有加载出来', action: '重试', onRetry: () => load(false) })); }
    } finally { busy = false; }
  }
  load(true);
  const off = api.on('evidence.created', () => { if (!busy) load(true); }); alive.push(off);
  return p;
}

/* ================= 取证详情 ================= */
function recordPage(ctx) {
  const p = page(ui.skeleton('card', 3));
  const body = $('.m-body', p);
  (async function run() {
    body.innerHTML = ui.skeleton('card', 3);
    try {
      const e = await api.enforcement.get(ctx.params.id);
      const urls = (e.photos || []).map(photoUrl);
      body.innerHTML = `<section class="f-banner is-${RESULT_CLS[e.scanResult] === 'ok' ? 'ok' : RESULT_CLS[e.scanResult] || 'err'}"><span class="k">核查结果</span><div class="v">${ui.esc(e.scanResult)}</div><p>${ui.esc(dt(e.capturedAt))} · 工号 ${ui.esc(e.badgeNo)}</p></section>
        <section class="m-card"><div class="f-sec">现场照片（${urls.length}）</div><div class="f-gallery">${urls.map((u, i) => `<button type="button" data-p="${i}" aria-label="查看第 ${i + 1} 张照片"><img src="${ui.esc(u)}" alt="现场照片 ${i + 1}" loading="lazy"></button>`).join('')}</div></section>
        <section class="m-card"><div class="f-sec">取证信息</div>
          ${kv('证据编号', `<span class="mono">${ui.esc(e.evidenceId)}</span>`)}${kv('取证时间', dt(e.capturedAt))}${kv('提交时间', dt(e.submittedAt))}
          ${kv('位置', ui.esc(e.address || '—'))}${kv('坐标', `<span class="num">${e.location[0].toFixed(6)}, ${e.location[1].toFixed(6)}${e.accuracyM != null ? ` · 精度约 ${e.accuracyM} 米` : ''}</span>`)}
          ${e.vehicleId ? kv('政府码 · 车牌', `<span class="mono">${ui.esc(e.plateNo || '')}</span>`) : ''}${e.brandName ? kv('品牌', ui.esc(e.brandName)) : ''}${e.operatorName ? kv('所属运营商', ui.esc(e.operatorName)) : ''}
          ${kv('车辆描述', ui.esc(e.vehicleDescription))}${e.note ? kv('备注', ui.esc(e.note)) : ''}</section>
        <section class="m-card"><div class="f-sec">防篡改 <span class="chip ok" style="margin-left:6px">已锁定</span></div><p class="m-hint">提交后不能修改或删除；摘要用于核对证据没有被改动。</p><div class="f-hash" style="margin-top:6px">${ui.esc(e.sha256)}</div></section>`;
      body.querySelectorAll('[data-p]').forEach(b => b.addEventListener('click', () => ui.bottomSheet({ title: `照片 ${+b.dataset.p + 1}/${urls.length}`, body: `<img class="f-big" src="${ui.esc(urls[+b.dataset.p])}" alt="现场照片">` })));
      const bt = document.createElement('div'); bt.className = 'm-bottom';
      const edit = button('secondary', '修改记录', () => {}); ui.setDisabled(edit, '证据提交后不能修改或删除', '如需补充，请重新核查并新增一条取证');
      bt.append(edit); p.querySelector('.m-bottom')?.remove(); p.append(bt);
    } catch (e) {
      body.innerHTML = '';
      body.append(e && e.code === 'FORBIDDEN' ? ui.empty({ icon: '🔒', title: '只能查看本人的取证记录', text: e.message, action: '返回记录', onAction: () => R.go('/records', true) })
        : e && e.code === 'NOT_FOUND' ? ui.empty({ icon: '🗂️', title: '没有找到这条记录', action: '返回记录', onAction: () => R.go('/records', true) }) : failView(e, run, '取证详情'));
    }
  })();
  return p;
}

/* ================= 启动 ================= */
const TITLES = { '/login': '', '/': '执法核查', '/result': '核查结果', '/capture': '拍照取证', '/done': '取证完成', '/records': '我的取证记录' };
function chrome(ctx) {
  const path = ctx.path.startsWith('/record/') ? '/record' : ctx.path;
  const isTab = path === '/' || path === '/records';
  $('#nav').hidden = path === '/login';
  $('#back').style.visibility = isTab || path === '/done' ? 'hidden' : 'visible';
  $('#title').textContent = path === '/record' ? '取证详情' : (TITLES[path] ?? '执法核查');
  $('#tabs').hidden = !isTab;
  document.querySelectorAll('#tabs a').forEach(a => a.classList.toggle('is-on', a.dataset.tab === path));
}

async function boot() {
  try { officer = await api.enforcement.me(); } catch { officer = null; }
  const h = location.hash.slice(1) || '/';
  if (!officer && !h.startsWith('/login')) location.replace('#/login');
  else if (officer && h.startsWith('/login')) location.replace('#/');
  const guard = fn => ctx => { cleanup(); if (!officer) { setTimeout(() => R.go('/login', true), 0); return loginPage(); } return fn(ctx); };
  R = ui.router({
    outlet: '#view', mode: 'm',
    routes: {
      '/login': ctx => { cleanup(); return loginPage(ctx); },
      '/': guard(homePage), '/result': guard(resultPage), '/capture': guard(capturePage), '/done': guard(donePage), '/records': guard(recordsPage), '/record/:id': guard(recordPage),
    },
    notFound: () => ui.empty({ icon: '🧭', title: '没有找到这个页面', action: '回到核查', onAction: () => R.go('/', true) }),
    onChange: chrome,
  });
  $('#back').addEventListener('click', () => R.back());
  ui.initDebug({ actions: [{ label: '重置数据', onClick: () => api.system.reset().then(() => location.reload()) }] });
  api.on('data.reset', () => location.reload());
  window.addEventListener('bike:session-lost', e => { if (e.detail && e.detail.who === 'officer') { officer = null; R.go('/login', true); } });
}
boot();

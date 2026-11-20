/* 共用界面脚本：原生 ES 模块，无框架、无构建、无外网依赖。
   用法：import * as ui from '../shared/ui.js';  详见 README.md */

/* ================= 基础 ================= */
const doc = document;
export const isDebug = new URLSearchParams(location.search).get('debug') === '1';

/** 当前样式前缀：管理端 body 带 a-body 时为 a，否则为 m（小程序） */
export function mode() { return doc.body.classList.contains('a-body') ? 'a' : 'm'; }

export function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

/** 由 HTML 字符串创建单个元素 */
export function el(html) {
  const t = doc.createElement('template');
  t.innerHTML = html.trim();
  return t.content.firstElementChild;
}

export const $ = (sel, root = doc) => root.querySelector(sel);
export const $$ = (sel, root = doc) => [...root.querySelectorAll(sel)];
const sleep = ms => new Promise(r => setTimeout(r, ms));
const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches;
const ANIM = 220;

/* ================= 格式化 ================= */
const p2 = n => String(n).padStart(2, '0');
function toDate(v) { return v instanceof Date ? v : new Date(v); }

/** 金额（元）：¥ 保留一位小数，千位分隔，负数写作 −¥3.0；opts.sign 为 true 时正数带 + */
export function money(v, opts = {}) {
  const n = Number(v) || 0;
  const s = Math.abs(n).toLocaleString('zh-CN', { minimumFractionDigits: 1, maximumFractionDigits: 1 });
  if (n < 0) return `−¥${s}`;
  return `${opts.sign && n > 0 ? '+' : ''}¥${s}`;
}
/** 金额（分）转元后格式化 */
export const moneyCents = (c, opts) => money((Number(c) || 0) / 100, opts);
/** 不带符号的千位分隔数字 */
export const number = (v, digits = 0) => (Number(v) || 0).toLocaleString('zh-CN', { minimumFractionDigits: digits, maximumFractionDigits: digits });
export const percent = (v, digits = 1) => `${((Number(v) || 0) * 100).toFixed(digits)}%`;
export const km = v => `${(Number(v) || 0).toFixed(1)} 公里`;

/** 中文友好时间：今天 14:30 / 昨天 14:30 / 11月16日 14:30 / 2025年11月16日 14:30 */
export function time(v, now = new Date()) {
  const d = toDate(v);
  if (isNaN(d)) return '—';
  const hm = `${p2(d.getHours())}:${p2(d.getMinutes())}`;
  const day0 = x => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((day0(now) - day0(d)) / 864e5);
  if (diff === 0) return `今天 ${hm}`;
  if (diff === 1) return `昨天 ${hm}`;
  if (diff === -1) return `明天 ${hm}`;
  if (d.getFullYear() === now.getFullYear()) return `${d.getMonth() + 1}月${d.getDate()}日 ${hm}`;
  return `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日 ${hm}`;
}
/** 表格用精确时间：2026-11-17 14:30（seconds 为 true 时带秒） */
export function datetime(v, seconds = false) {
  const d = toDate(v);
  if (isNaN(d)) return '—';
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}${seconds ? ':' + p2(d.getSeconds()) : ''}`;
}
export function date(v) {
  const d = toDate(v);
  return isNaN(d) ? '—' : `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())}`;
}
/** 相对时间：刚刚 / 5 分钟前 / 3 小时前 / 其余走 time() */
export function ago(v, now = new Date()) {
  const s = Math.round((now - toDate(v)) / 1000);
  if (s < 0) return time(v, now);
  if (s < 60) return '刚刚';
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`;
  if (s < 86400 && toDate(v).getDate() === now.getDate()) return `${Math.floor(s / 3600)} 小时前`;
  return time(v, now);
}
/** 时长（秒）：18 分钟 / 1 小时 5 分钟 / 不足 1 分钟 */
export function duration(sec) {
  const m = Math.floor((Number(sec) || 0) / 60);
  if (m < 1) return '不足 1 分钟';
  if (m < 60) return `${m} 分钟`;
  const h = Math.floor(m / 60), r = m % 60;
  return r ? `${h} 小时 ${r} 分钟` : `${h} 小时`;
}
/** 计时器显示（秒）：03:25 或 1:03:25 */
export function clock(sec) {
  const s = Math.max(0, Math.floor(Number(sec) || 0));
  const h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60), r = s % 60;
  return h ? `${h}:${p2(m)}:${p2(r)}` : `${p2(m)}:${p2(r)}`;
}
/** 数字滚动到位（账单实付等），reduced-motion 下直接显示 */
export function countTo(node, to, ms = 400, fmt = money) {
  if (reduced()) { node.textContent = fmt(to); return; }
  const t0 = performance.now();
  (function step(n) {
    const k = Math.min(1, (n - t0) / ms);
    node.textContent = fmt(to * (1 - Math.pow(1 - k, 3)));
    if (k < 1) requestAnimationFrame(step);
  })(t0);
}
export const vibrate = () => { try { navigator.vibrate && navigator.vibrate(15); } catch { /* 静音或不支持时忽略 */ } };

/* ================= 浮层栈：焦点、Esc、遮罩 ================= */
const stack = [];
const FOCUSABLE = 'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

doc.addEventListener('keydown', e => {
  const top = stack[stack.length - 1];
  if (!top) return;
  if (e.key === 'Escape' && top.closable !== false) { e.preventDefault(); top.close('esc'); return; }
  if (e.key === 'Tab') {
    const f = $$(FOCUSABLE, top.root).filter(x => x.offsetParent !== null || x === doc.activeElement);
    if (!f.length) { e.preventDefault(); return; }
    const first = f[0], last = f[f.length - 1];
    if (e.shiftKey && (doc.activeElement === first || !top.root.contains(doc.activeElement))) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && (doc.activeElement === last || !top.root.contains(doc.activeElement))) { e.preventDefault(); first.focus(); }
  }
});

function host() { return mode() === 'm' ? ($('.m-app') || doc.body) : doc.body; }

/** 注册一个浮层：处理焦点进入、返还、关闭动画、栈顺序 */
function layer({ nodes, root, focusEl, closable = true, onClose, outClass = 'is-out' }) {
  const prev = doc.activeElement;
  let closed = false;
  const api = {
    root, closable,
    close(reason) {
      if (closed) return;
      closed = true;
      const i = stack.indexOf(api); if (i >= 0) stack.splice(i, 1);
      nodes.forEach(n => n.classList.add(outClass));
      const done = () => { nodes.forEach(n => n.remove()); prev && prev.focus && prev.isConnected && prev.focus(); onClose && onClose(reason); };
      reduced() ? done() : setTimeout(done, ANIM);
    },
  };
  stack.push(api);
  setTimeout(() => (focusEl || $(FOCUSABLE, root) || root).focus({ preventScroll: true }), 30);
  return api;
}

/* ================= Toast ================= */
let toastWrap;
/** toast('已保存', {type:'ok'|'warn'|'err', hint:'下一步说明', duration}) */
export function toast(msg, opts = {}) {
  const p = mode();
  if (!toastWrap || !toastWrap.isConnected || toastWrap.dataset.p !== p) {
    toastWrap?.remove();
    toastWrap = el(`<div class="${p}-toast-wrap" data-p="${p}" role="status" aria-live="polite"></div>`);
    doc.body.append(toastWrap);
  }
  const t = el(`<div class="${p}-toast${opts.type ? ' is-' + opts.type : ''}">${p === 'a' ? `<div>${esc(msg)}${opts.hint ? `<small>${esc(opts.hint)}</small>` : ''}</div>` : `${esc(msg)}${opts.hint ? `<small>${esc(opts.hint)}</small>` : ''}`}</div>`);
  toastWrap.append(t);
  const ms = opts.duration ?? (opts.hint ? 3600 : 2200);
  const rm = () => { t.classList.add('is-out'); setTimeout(() => t.remove(), reduced() ? 0 : ANIM); };
  const timer = setTimeout(rm, ms);
  t.addEventListener('click', () => { clearTimeout(timer); rm(); });
  return { close: rm };
}

/* ================= 置灰按钮说明 =================
   置灰用 aria-disabled="true"（不用 disabled 属性），点击时说明原因和下一步。
   <button class="m-btn m-btn-primary" aria-disabled="true" data-why="您有一笔欠费" data-next="补付后即可骑行">开锁</button> */
export function setDisabled(btn, why, next) {
  btn.setAttribute('aria-disabled', 'true');
  btn.dataset.why = why || '';
  btn.dataset.next = next || '';
}
export function setEnabled(btn) {
  btn.removeAttribute('aria-disabled');
  delete btn.dataset.why; delete btn.dataset.next;
}
doc.addEventListener('click', e => {
  const b = e.target.closest('[aria-disabled="true"]');
  if (!b) return;
  e.preventDefault(); e.stopImmediatePropagation();
  toast(b.dataset.why || '当前操作暂不可用', { hint: b.dataset.next || '', type: 'warn' });
}, true);

/* ================= 骨架屏、空状态、加载、重试 ================= */
/** skeleton('card', 3) 手机卡片 / skeleton('lines', 4) 文本行 / skeleton('rows', 6, 5) 表格行（列数） */
export function skeleton(kind = 'lines', n = 3, cols = 5) {
  const p = mode();
  if (kind === 'card') return Array.from({ length: n }, () => `<div class="${p}-skel-card" aria-hidden="true"><span class="skel is-ic"></span><div class="col"><span class="skel ${p}-skel-line is-mid"></span><span class="skel ${p}-skel-line is-short"></span></div></div>`).join('');
  if (kind === 'rows') return Array.from({ length: n }, () => `<tr class="a-skel-row" aria-hidden="true">${Array.from({ length: cols }, (_, i) => `<td><span class="skel" style="width:${50 + (i * 17) % 45}%"></span></td>`).join('')}</tr>`).join('');
  return `<div class="${p === 'a' ? 'a-skel-block' : ''}" aria-hidden="true">${Array.from({ length: n }, (_, i) => `<span class="skel ${p}-skel-line${i % 3 === 1 ? ' is-mid' : i % 3 === 2 ? ' is-short' : ''}"></span>`).join('')}</div>`;
}
/** empty({icon:'🧾', title:'还没有骑行记录', text:'扫码骑一次，这里就会有记录', action:'去扫码', onAction}) 返回元素 */
export function empty({ icon = '🗂️', title = '暂无内容', text = '', action, onAction } = {}) {
  const p = mode();
  const n = el(`<div class="${p}-empty"><div class="ic" aria-hidden="true">${icon}</div><h4>${esc(title)}</h4>${text ? `<p>${esc(text)}</p>` : ''}${action ? `<button type="button" class="${p}-btn ${p === 'a' ? 'a-btn-primary' : 'm-btn-primary'}">${esc(action)}</button>` : ''}</div>`);
  if (action) $('button', n).addEventListener('click', () => onAction && onAction());
  return n;
}
export function loading(text = '加载中…') { return el(`<div class="m-loading" role="status">${esc(text)}</div>`); }
/** retry({text:'订单加载失败，没有任何扣费', action:'重新加载', onRetry}) */
export function retry({ text = '加载失败，请稍后重试', action = '重新加载', onRetry } = {}) {
  const p = mode();
  const n = el(`<div class="${p}-retry" role="alert"><p>${esc(text)}</p><button type="button" class="${p}-btn ${p === 'a' ? 'a-btn-default' : 'm-btn-secondary'}">${esc(action)}</button></div>`);
  $('button', n).addEventListener('click', () => onRetry && onRetry());
  return n;
}

/* ================= 弹窗 modal / confirm（管理端居中） ================= */
/** modal({title, body(html|Element), actions:[{label, kind:'primary'|'default'|'danger', onClick, keepOpen}], icon, danger}) */
export function modal({ title, body = '', actions, icon, danger, closable = true, onClose } = {}) {
  const p = mode();
  const acts = actions || [{ label: '知道了', kind: 'primary' }];
  const wrap = el(`<div class="a-modal-wrap"><div class="a-mask"></div>
    <div class="a-modal" role="dialog" aria-modal="true" aria-label="${esc(title)}">
      <div class="a-modal-head">${icon !== false ? `<span class="ic${danger ? ' is-danger' : ''}" aria-hidden="true">${danger ? '!' : (icon || 'i')}</span>` : ''}<h3>${esc(title)}</h3></div>
      <div class="a-modal-body"></div>
      <div class="a-modal-foot"></div></div></div>`);
  const dlg = $('.a-modal', wrap), bodyEl = $('.a-modal-body', wrap), foot = $('.a-modal-foot', wrap);
  if (icon === false) bodyEl.style.paddingLeft = '20px';
  typeof body === 'string' ? bodyEl.innerHTML = body : bodyEl.append(body);
  host(); doc.body.append(wrap);
  const L = layer({ nodes: [wrap], root: dlg, closable, onClose, focusEl: $('textarea,input', bodyEl) || foot.lastElementChild });
  $('.a-mask', wrap).addEventListener('click', () => closable && L.close('mask'));
  acts.map(a => {
    const b = el(`<button type="button" class="a-btn a-btn-${a.kind || 'default'}">${esc(a.label)}</button>`);
    b.addEventListener('click', async () => { const r = a.onClick && await a.onClick(L, b); if (!a.keepOpen && r !== false) L.close(a.label); });
    foot.append(b); return b;
  });
  return { close: L.close, el: dlg, body: bodyEl };
}

/** 危险操作二次确认。必填事由，写入操作留痕。
    confirm({title, message, impact:'将退还 ¥9.0 至用户原支付账户', danger:true, confirmText:'确认退款', reasons:['用户误扣','重复扣款'], minLen:4,
             requireReason:true, onConfirm: async (reason)=>{ ... 抛错则留在弹窗内显示 } })
    返回 Promise：确认后 resolve({confirmed:true, reason})，取消 resolve(null) */
export function confirm(opts = {}) {
  const { title = '确认操作', message = '', impact = '', danger = false, confirmText = '确认', cancelText = '取消', reasons = [], minLen = 4, requireReason = true, onConfirm } = opts;
  return new Promise(resolve => {
    const body = el(`<div style="display:contents">
      ${message ? `<div>${message}</div>` : ''}
      ${impact ? `<div class="a-impact">影响范围：<b>${esc(impact)}</b></div>` : ''}
      ${requireReason ? `<div class="a-item"><label class="req" for="cf-reason">事由（必填，会写入操作留痕）</label>
        ${reasons.length ? `<div class="a-actions" style="flex-wrap:wrap;gap:6px">${reasons.map(r => `<button type="button" class="a-btn a-btn-default is-sm" data-r="${esc(r)}">${esc(r)}</button>`).join('')}</div>` : ''}
        <textarea id="cf-reason" class="a-textarea" placeholder="请写明为什么要这样做，至少 ${minLen} 个字"></textarea>
        <span class="err-msg" hidden></span></div>` : ''}
      <div class="a-alert is-err" hidden role="alert"></div></div>`);
    let settled = false;
    const finish = v => { if (!settled) { settled = true; resolve(v); } };
    const m = modal({
      title, body, danger, onClose: () => finish(null),
      actions: [
        { label: cancelText, kind: 'default' },
        { label: confirmText, kind: danger ? 'danger' : 'primary', keepOpen: true, async onClick(L, btn) {
          const ta = $('textarea', body), item = ta && ta.closest('.a-item'), msg = item && $('.err-msg', item);
          const reason = ta ? ta.value.trim() : '';
          if (requireReason && reason.length < minLen) {
            item.classList.add('is-error'); msg.hidden = false;
            msg.textContent = reason ? `事由太短，请至少写 ${minLen} 个字，说明为什么这样做` : `请先填写事由，再${confirmText}`;
            const d = m.el; d.classList.remove('is-shake'); void d.offsetWidth; d.classList.add('is-shake'); ta.focus(); return;
          }
          if (onConfirm) {
            const old = btn.innerHTML; btn.disabled = true; btn.innerHTML = '<span class="a-spin"></span>处理中';
            try { await onConfirm(reason); }
            catch (err) { btn.disabled = false; btn.innerHTML = old; const a = $('.a-alert', body); a.hidden = false; a.textContent = (err && err.message) || '操作没有成功，没有产生任何变更，请重试'; return; }
          }
          finish({ confirmed: true, reason }); L.close('ok');
        } },
      ],
    });
    $$('[data-r]', body).forEach(b => b.addEventListener('click', () => { const ta = $('textarea', body); ta.value = b.dataset.r; ta.dispatchEvent(new Event('input')); ta.focus(); }));
    const ta = $('textarea', body);
    ta && ta.addEventListener('input', () => { const it = ta.closest('.a-item'); it.classList.remove('is-error'); $('.err-msg', it).hidden = true; });
  });
}

/* ================= 右侧详情抽屉（管理端） ================= */
/** drawer({title, sub, body, footer:[{label,kind,onClick}], wide, onClose}) 返回 {close, body, setBody} */
export function drawer({ title = '', sub = '', body = '', footer, wide = false, onClose } = {}) {
  const wrap = el(`<div><div class="a-mask"></div>
    <aside class="a-drawer${wide ? ' is-wide' : ''}" role="dialog" aria-modal="true" aria-label="${esc(title)}">
      <header class="a-drawer-head"><div><h2>${esc(title)}</h2>${sub ? `<div class="sub">${esc(sub)}</div>` : ''}</div><button type="button" class="a-x" aria-label="关闭">✕</button></header>
      <div class="a-drawer-body"></div>${footer ? '<footer class="a-drawer-foot"></footer>' : ''}
    </aside></div>`);
  const mask = $('.a-mask', wrap), dr = $('.a-drawer', wrap), bodyEl = $('.a-drawer-body', wrap);
  const setBody = b => { bodyEl.innerHTML = ''; typeof b === 'string' ? bodyEl.innerHTML = b : bodyEl.append(b); };
  setBody(body);
  doc.body.append(mask, dr);
  const L = layer({ nodes: [mask, dr], root: dr, focusEl: $('.a-x', dr), onClose });
  mask.addEventListener('click', () => L.close('mask'));
  $('.a-x', dr).addEventListener('click', () => L.close('x'));
  if (footer) {
    const f = $('.a-drawer-foot', dr);
    footer.forEach(a => { const b = el(`<button type="button" class="a-btn a-btn-${a.kind || 'default'}">${esc(a.label)}</button>`); b.addEventListener('click', async () => { const r = a.onClick && await a.onClick(L, b); if (!a.keepOpen && r !== false) L.close(a.label); }); f.append(b); });
  }
  return { close: L.close, body: bodyEl, setBody, el: dr };
}

/* ================= 底部抽屉（小程序） ================= */
/** bottomSheet({title, body, footer:[{label,kind:'primary'|'secondary'|'ghost',onClick}], closable, onClose}) */
export function bottomSheet({ title = '', body = '', footer, closable = true, onClose } = {}) {
  const root = host();
  const mask = el('<div class="m-mask"></div>');
  const sh = el(`<section class="m-sheet" role="dialog" aria-modal="true" aria-label="${esc(title)}"><div class="m-sheet-grip"></div>
    <header class="m-sheet-head"><h3>${esc(title)}</h3>${closable ? '<button type="button" class="m-sheet-x" aria-label="关闭">✕</button>' : ''}</header>
    <div class="m-sheet-body"></div>${footer ? '<footer class="m-sheet-foot"></footer>' : ''}</section>`);
  const bodyEl = $('.m-sheet-body', sh);
  const setBody = b => { bodyEl.innerHTML = ''; typeof b === 'string' ? bodyEl.innerHTML = b : bodyEl.append(b); };
  setBody(body);
  root.append(mask, sh);
  const L = layer({ nodes: [mask, sh], root: sh, closable, onClose });
  closable && mask.addEventListener('click', () => L.close('mask'));
  closable && $('.m-sheet-x', sh).addEventListener('click', () => L.close('x'));
  if (footer) {
    const f = $('.m-sheet-foot', sh);
    footer.forEach(a => { const b = el(`<button type="button" class="m-btn m-btn-${a.kind || 'primary'}">${esc(a.label)}</button>`); b.addEventListener('click', async () => { const r = a.onClick ? await a.onClick(L, b) : undefined; if (!a.keepOpen && r !== false) L.close(a.label); }); f.append(b); });
  }
  return { close: L.close, body: bodyEl, setBody, el: sh };
}

/** 小程序居中对话框（授权、确认）：mDialog({title, text, actions:[{label,kind,onClick}]}) */
export function mDialog({ title = '', text = '', actions = [{ label: '知道了', kind: 'primary' }], closable = false } = {}) {
  const root = host();
  const mask = el('<div class="m-mask"></div>');
  const d = el(`<div class="m-dialog" role="alertdialog" aria-modal="true" aria-label="${esc(title)}"><h3>${esc(title)}</h3><p>${esc(text)}</p><div class="m-dialog-actions${actions.length === 1 ? ' is-one' : ''}"></div></div>`);
  root.append(mask, d);
  const L = layer({ nodes: [mask, d], root: d, closable });
  closable && mask.addEventListener('click', () => L.close('mask'));
  actions.forEach(a => { const b = el(`<button type="button" class="m-btn m-btn-${a.kind || 'secondary'}">${esc(a.label)}</button>`); b.addEventListener('click', async () => { const r = a.onClick && await a.onClick(L, b); if (r !== false) L.close(a.label); }); $('.m-dialog-actions', d).append(b); });
  return { close: L.close, el: d };
}

/* ================= 开锁充能按钮 =================
   unlockButton(btn, run, {onDone}) —— btn 结构：<button class="m-btn m-btn-primary m-unlock"><span class="fill"></span><span class="lab">开锁</span></button>
   run 是返回 Promise 的真实开锁调用；5 秒未返回提示较慢，10 秒转失败（reject 的 Error.message 由调用方展示） */
export async function unlockButton(btn, run, { label = '开锁', runningText = '正在开锁…', slowText = '运营商响应较慢，再等几秒', timeoutMs = 10000, slowMs = 5000 } = {}) {
  const lab = $('.lab', btn);
  btn.classList.remove('is-run', 'is-done', 'is-pop', 'is-finish'); void btn.offsetWidth;
  btn.classList.add('is-run'); lab.textContent = runningText;
  btn.setAttribute('aria-busy', 'true');
  const slow = setTimeout(() => { lab.textContent = slowText; }, slowMs);
  try {
    const res = await Promise.race([run(), new Promise((_, rej) => setTimeout(() => rej(Object.assign(new Error('开锁超时'), { code: 'TIMEOUT' })), timeoutMs))]);
    clearTimeout(slow);
    btn.classList.remove('is-run'); btn.classList.add('is-finish'); await sleep(reduced() ? 0 : 140);
    btn.classList.add('is-done', 'is-pop'); lab.textContent = '✓ 已开锁'; vibrate();
    await sleep(reduced() ? 0 : 400);
    return res;
  } catch (e) {
    clearTimeout(slow);
    btn.classList.remove('is-run'); lab.textContent = label;
    throw e;
  } finally { btn.removeAttribute('aria-busy'); }
}
/** 让元素播放一次抖动 */
export function shake(node) { const c = node.classList.contains('m-fail') ? 'is-shake' : 'm-shake'; node.classList.remove(c); void node.offsetWidth; node.classList.add(c); }

/* ================= 表单校验 =================
   const r = validate(formEl, { name:{label:'规则名称', required:true, min:2, max:20}, price:{label:'单价', required:true, type:'number', gt:0, max:10},
                                phone:{pattern:/^1\d{10}$/, message:'请输入 11 位手机号'}, end:{custom:(v,all)=> v<all.start && '结束时间不能早于开始时间'} })
   r = {ok, values, errors}；第一个错误项自动聚焦；输入时自动清除该项错误。
   控件通过 name 属性匹配；错误显示在最近的 .a-item（管理端）或 .m-field（小程序）。 */
export function validate(form, rules) {
  const values = {}, errors = {};
  const fd = new FormData(form);
  for (const [k, v] of fd.entries()) values[k] = typeof v === 'string' ? v.trim() : v;
  $$('input[type=checkbox]', form).forEach(c => { if (c.name) values[c.name] = c.checked; });
  for (const [name, r] of Object.entries(rules)) {
    const v = values[name] ?? '', label = r.label || name;
    let msg = '';
    if (r.required && (v === '' || v === false)) msg = r.requiredMessage || (typeof v === 'boolean' ? `请先勾选${label}` : `请填写${label}`);
    else if (v !== '') {
      if (r.type === 'number') {
        const n = Number(v);
        if (Number.isNaN(n)) msg = `${label}需要填数字`;
        else if (r.gt !== undefined && !(n > r.gt)) msg = `${label}需要大于 ${r.gt}`;
        else if (r.minValue !== undefined && n < r.minValue) msg = `${label}不能小于 ${r.minValue}`;
        else if (r.maxValue !== undefined && n > r.maxValue) msg = `${label}不能大于 ${r.maxValue}`;
        else values[name] = n;
      } else {
        if (r.min !== undefined && String(v).length < r.min) msg = `${label}至少 ${r.min} 个字`;
        else if (r.max !== undefined && String(v).length > r.max) msg = `${label}不能超过 ${r.max} 个字`;
      }
      if (!msg && r.pattern && !r.pattern.test(v)) msg = r.message || `${label}格式不对`;
    }
    if (!msg && r.custom) msg = r.custom(values[name], values) || '';
    if (msg) errors[name] = msg;
  }
  const p = mode();
  $$(`.${p === 'a' ? 'a-item' : 'm-field'}.is-error`, form).forEach(i => i.classList.remove('is-error'));
  $$('.a-item .err-msg,.m-field-msg', form).forEach(n => n.remove());
  let first;
  for (const [name, msg] of Object.entries(errors)) {
    const ctl = form.elements[name]; const c = ctl && (ctl.length && !ctl.tagName ? ctl[0] : ctl);
    const item = c && c.closest(p === 'a' ? '.a-item' : '.m-field'); if (!item) continue;
    item.classList.add('is-error');
    const m = el(`<${p === 'a' ? 'span class="err-msg"' : 'div class="m-field-msg"'} role="alert">${esc(msg)}</${p === 'a' ? 'span' : 'div'}>`);
    p === 'a' ? item.append(m) : item.after(m);
    c.addEventListener('input', () => { item.classList.remove('is-error'); m.remove(); }, { once: true });
    first = first || c;
  }
  if (first) first.focus();
  return { ok: !Object.keys(errors).length, values, errors };
}

/* ================= 表格渲染与分页（管理端） =================
   const t = table(container, {
     columns:[{key:'no', title:'订单号'}, {key:'amount', title:'实付', align:'right', sortable:true, render:r=>ui.money(r.amount)}, {key:'state', title:'状态', render:r=>`<span class="chip ok">…</span>`}],
     rows: [...]            // 本地数据（本地排序、分页）
     // 或 load: async ({page,pageSize,sort}) => ({rows,total})  // 远程（经 mock 接口层，自带加载骨架与失败重试）
     rowKey:'id', pageSize:20, selectable:true, bulkActions:[{label:'导出所选', onClick(rows){}}],
     onRowClick:(row)=>{}, empty:{title,text}, summary:'共 {total} 条' })
   render 返回 HTML 字符串，用户数据务必用 ui.esc 转义。返回 {reload,setRows,getSelected,clearSelection,goto} */
export function table(container, opts) {
  const { columns, rowKey = 'id', pageSizes = [10, 20, 50], selectable = false, bulkActions = [], onRowClick, onSelect } = opts;
  const st = { page: 1, pageSize: opts.pageSize || 20, sort: opts.sort || null, rows: opts.rows || [], total: 0, selected: new Map(), loading: false, error: false, active: null };
  const remote = typeof opts.load === 'function';
  container.classList.add('a-table-wrap');
  container.innerHTML = '';
  const bar = el('<div class="a-table-bar" hidden></div>');
  const scroller = el('<div class="a-table-scroll"></div>');
  const pager = el('<div class="a-pager"></div>');
  container.append(bar, scroller, pager);

  const cmp = (a, b) => {
    const c = columns.find(x => x.key === st.sort.key); const f = c.sortValue || (r => r[c.key]);
    const x = f(a), y = f(b);
    const r = typeof x === 'number' && typeof y === 'number' ? x - y : String(x ?? '').localeCompare(String(y ?? ''), 'zh-CN');
    return st.sort.dir === 'asc' ? r : -r;
  };
  const view = () => {
    if (remote) return st.rows;
    const all = st.sort ? [...st.rows].sort(cmp) : st.rows;
    st.total = all.length;
    return all.slice((st.page - 1) * st.pageSize, st.page * st.pageSize);
  };

  function draw() {
    const rows = st.loading ? [] : view();
    const ths = columns.map(c => {
      const r = c.align === 'right' ? ' r' : '', w = c.width ? ` style="width:${c.width}"` : '';
      const as = st.sort && st.sort.key === c.key ? ` aria-sort="${st.sort.dir === 'asc' ? 'ascending' : 'descending'}"` : '';
      return `<th class="${r.trim()}"${w}${as}>${c.sortable ? `<button type="button" class="a-sort" data-sort="${esc(c.key)}">${esc(c.title)}</button>` : esc(c.title)}</th>`;
    }).join('');
    const allOn = rows.length && rows.every(r => st.selected.has(r[rowKey]));
    let tb = '';
    if (st.loading) tb = skeleton('rows', Math.min(st.pageSize, 8), columns.length + (selectable ? 1 : 0));
    else rows.forEach((r, i) => {
      const k = r[rowKey], sel = st.selected.has(k);
      tb += `<tr data-i="${i}" class="${onRowClick ? 'is-click ' : ''}${sel ? 'is-selected ' : ''}${st.active === k ? 'is-active' : ''}"${onRowClick ? ' tabindex="0"' : ''}>${selectable ? `<td class="c-chk"><input type="checkbox" aria-label="选择此行" ${sel ? 'checked' : ''}></td>` : ''}${columns.map(c => {
        const v = c.render ? c.render(r) : esc(r[c.key] ?? '—');
        return `<td class="${c.align === 'right' ? 'r num' : ''}${c.ellipsis ? ' ell' : ''}">${v}</td>`;
      }).join('')}</tr>`;
    });
    scroller.innerHTML = `<table class="a-table"><thead><tr>${selectable ? `<th class="c-chk"><input type="checkbox" aria-label="全选本页" ${allOn ? 'checked' : ''} ${rows.length ? '' : 'disabled'}></th>` : ''}${ths}</tr></thead><tbody>${tb}</tbody></table>`;
    scroller.querySelectorAll('th.r').forEach(th => th.classList.add('r'));
    if (st.error) { scroller.append(retry({ text: opts.errorText || '数据没有加载出来，请检查网络后重试', onRetry: reload })); }
    else if (!st.loading && !rows.length) scroller.append(empty({ icon: '🔍', title: (opts.empty && opts.empty.title) || '没有符合条件的数据', text: (opts.empty && opts.empty.text) || '换个筛选条件试试，或清空筛选查看全部' , action: opts.empty && opts.empty.action, onAction: opts.empty && opts.empty.onAction }));
    // 绑定
    $$('[data-sort]', scroller).forEach(b => b.addEventListener('click', () => {
      const k = b.dataset.sort;
      st.sort = !st.sort || st.sort.key !== k ? { key: k, dir: 'asc' } : st.sort.dir === 'asc' ? { key: k, dir: 'desc' } : null;
      st.page = 1; remote ? reload() : draw();
    }));
    const hdr = $('thead input[type=checkbox]', scroller);
    hdr && hdr.addEventListener('change', () => { rows.forEach(r => hdr.checked ? st.selected.set(r[rowKey], r) : st.selected.delete(r[rowKey])); draw(); onSelect && onSelect([...st.selected.values()]); });
    $$('tbody tr[data-i]', scroller).forEach(tr => {
      const r = rows[+tr.dataset.i], chk = $('input[type=checkbox]', tr);
      chk && chk.addEventListener('change', () => { chk.checked ? st.selected.set(r[rowKey], r) : st.selected.delete(r[rowKey]); draw(); onSelect && onSelect([...st.selected.values()]); });
      chk && chk.addEventListener('click', e => e.stopPropagation());
      if (onRowClick) {
        const go = () => { st.active = r[rowKey]; $$('tbody tr', scroller).forEach(x => x.classList.remove('is-active')); tr.classList.add('is-active'); onRowClick(r, tr); };
        tr.addEventListener('click', go);
        tr.addEventListener('keydown', e => { if (e.key === 'Enter' && e.target === tr) go(); });
      }
      $$('[data-act]', tr).forEach(b => b.addEventListener('click', e => { e.stopPropagation(); opts.onAction && opts.onAction(b.dataset.act, r, b); }));
    });
    // 批量条
    const n = st.selected.size;
    bar.hidden = !n;
    if (n) {
      bar.innerHTML = `<span>已选 <b class="num">${n}</b> 条</span><span class="a-actions"><button type="button" class="a-btn a-btn-text is-sm" data-clear>取消选择</button></span>`;
      bulkActions.forEach(a => { const b = el(`<button type="button" class="a-btn a-btn-default is-sm">${esc(a.label)}</button>`); b.addEventListener('click', () => a.onClick([...st.selected.values()])); $('.a-actions', bar).prepend(b); });
      $('[data-clear]', bar).addEventListener('click', () => { st.selected.clear(); draw(); onSelect && onSelect([]); });
    }
    drawPager();
  }

  function drawPager() {
    const pages = Math.max(1, Math.ceil(st.total / st.pageSize));
    st.page = Math.min(st.page, pages);
    const a = st.total ? (st.page - 1) * st.pageSize + 1 : 0, b = Math.min(st.total, st.page * st.pageSize);
    const nums = [];
    const add = n => nums.push(n);
    for (let i = 1; i <= pages; i++) if (i === 1 || i === pages || Math.abs(i - st.page) <= 1) add(i); else if (nums[nums.length - 1] !== '…') add('…');
    pager.innerHTML = `<span>${opts.summary ? esc(opts.summary).replace('{total}', number(st.total)) : `共 <b class="num">${number(st.total)}</b> 条`}${st.total ? `，当前第 <span class="num">${a}–${b}</span> 条` : ''}</span>
      <span class="pages"><select aria-label="每页条数">${pageSizes.map(s => `<option value="${s}" ${s === st.pageSize ? 'selected' : ''}>${s} 条/页</option>`).join('')}</select>
      <button type="button" data-p="${st.page - 1}" ${st.page <= 1 ? 'disabled' : ''} aria-label="上一页">‹</button>
      ${nums.map(n => n === '…' ? '<span class="gap">…</span>' : `<button type="button" data-p="${n}" class="${n === st.page ? 'is-on' : ''}" ${n === st.page ? 'aria-current="page"' : ''}>${n}</button>`).join('')}
      <button type="button" data-p="${st.page + 1}" ${st.page >= pages ? 'disabled' : ''} aria-label="下一页">›</button></span>`;
    $$('button[data-p]', pager).forEach(x => x.addEventListener('click', () => { st.page = +x.dataset.p; goto(st.page); }));
    $('select', pager).addEventListener('change', e => { st.pageSize = +e.target.value; st.page = 1; remote ? reload() : draw(); });
  }

  let token = 0;
  async function reload() {
    if (!remote) { draw(); return; }
    const my = ++token;
    st.loading = true; st.error = false; draw();
    try {
      const res = await opts.load({ page: st.page, pageSize: st.pageSize, sort: st.sort });
      if (my !== token) return;
      st.rows = res.rows; st.total = res.total;
    } catch (e) { if (my !== token) return; st.rows = []; st.error = true; }
    st.loading = false; draw();
  }
  function goto(p) { st.page = p; remote ? reload() : draw(); }

  reload();
  return {
    reload,
    setRows(rows) { st.rows = rows; st.page = 1; st.selected.clear(); draw(); },
    getSelected: () => [...st.selected.values()],
    clearSelection() { st.selected.clear(); draw(); },
    goto,
    getState: () => ({ page: st.page, pageSize: st.pageSize, sort: st.sort, total: st.total }),
  };
}

/* ================= Tab（管理端） =================
   tabs(container, [{key,label,count}], activeKey, onChange) */
export function tabs(container, items, active, onChange) {
  container.classList.add('a-tabs'); container.setAttribute('role', 'tablist');
  const draw = () => {
    container.innerHTML = items.map(t => `<button type="button" role="tab" data-k="${esc(t.key)}" aria-selected="${t.key === active}">${esc(t.label)}${t.count != null ? `<span class="cnt">${t.count}</span>` : ''}</button>`).join('');
    $$('button', container).forEach(b => b.addEventListener('click', () => { active = b.dataset.k; draw(); onChange && onChange(active); }));
  };
  draw();
  container.addEventListener('keydown', e => {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    const i = items.findIndex(t => t.key === active), n = (i + (e.key === 'ArrowRight' ? 1 : -1) + items.length) % items.length;
    active = items[n].key; draw(); $(`[data-k="${active}"]`, container).focus(); onChange && onChange(active);
  });
  return { set(k) { active = k; draw(); } };
}

/* ================= 简单哈希路由与页面转场 =================
   const r = router({ outlet:'#view', routes:{'/':home, '/order/:id':order}, notFound, mode:'m'|'a', onChange })
   页面函数 (ctx)=>Element|string|Promise，ctx={params,query,path,go(path),back()}；小程序下自动包成 .m-page 并做前进左滑、返回右滑 */
export function router({ outlet, routes, notFound, onChange, mode: md } = {}) {
  const root = typeof outlet === 'string' ? $(outlet) : outlet;
  const compiled = Object.entries(routes).map(([p, fn]) => {
    const keys = []; const re = new RegExp('^' + p.replace(/:([a-zA-Z_]+)/g, (_, k) => { keys.push(k); return '([^/]+)'; }) + '/?$');
    return { re, keys, fn };
  });
  const m = md || mode();
  const hist = []; let idx = -1; let seq = 0;
  function parse() {
    const raw = location.hash.slice(1) || '/'; const [path, qs] = raw.split('?');
    return { path, query: Object.fromEntries(new URLSearchParams(qs || '')) };
  }
  async function render() {
    const { path, query } = parse();
    const my = ++seq;
    let dir = 'fwd';
    const prev = hist[idx - 1], next = hist[idx + 1];
    if (prev === path) { idx--; dir = 'back'; } else if (next === path && hist.length - 1 > idx) { idx++; dir = 'fwd'; } else { hist.length = idx + 1; hist.push(path); idx++; }
    let hit = null, params = {};
    for (const c of compiled) { const mm = c.re.exec(path); if (mm) { hit = c; c.keys.forEach((k, i) => params[k] = decodeURIComponent(mm[i + 1])); break; } }
    const ctx = { params, query, path, go, back, dir };
    let out;
    try { out = hit ? await hit.fn(ctx) : notFound ? await notFound(ctx) : empty({ icon: '🧭', title: '没有找到这个页面', text: '链接可能已失效，回到首页看看', action: '回到首页', onAction: () => go('/') }); }
    catch (e) { out = retry({ text: '页面没有加载出来，没有产生任何扣费或变更', onRetry: render }); }
    if (my !== seq) return;
    let node = typeof out === 'string' ? el(`<div>${out}</div>`) : out;
    const old = [...root.children];
    if (m === 'm') {
      if (!node.classList.contains('m-page')) { const w = el('<div class="m-page"></div>'); w.append(...(typeof out === 'string' ? node.childNodes : [node])); node = w; }
      if (!reduced() && old.length) { node.classList.add(dir === 'back' ? 'is-enter-back' : 'is-enter-fwd'); old.forEach(o => o.classList.add(dir === 'back' ? 'is-leave-back' : 'is-leave-fwd')); }
      root.append(node);
      setTimeout(() => old.forEach(o => o.remove()), reduced() ? 0 : ANIM);
    } else {
      node.classList.add('a-view'); root.innerHTML = ''; root.append(node);
    }
    onChange && onChange(ctx);
  }
  function go(path, replace = false) { replace ? location.replace('#' + path) : (location.hash = path); }
  function back() { idx > 0 ? history.back() : go('/', true); }
  addEventListener('hashchange', render);
  render();
  return { go, back, refresh: render, current: parse };
}

/* ================= 调试面板（仅 ?debug=1 出现） =================
   initDebug({ title:'调试', actions:[{label:'重置数据', onClick(){}}], fields:[{label:'失败注入', type:'checkbox', get, set}] })
   没有 debug=1 时什么都不渲染。 */
export function initDebug({ title = '调试', actions = [], fields = [] } = {}) {
  if (!isDebug) return null;
  const p = el(`<aside class="dbg-panel" aria-label="调试面板"><h4>${esc(title)}</h4></aside>`);
  fields.forEach(f => {
    const l = el(`<label><input type="${f.type || 'checkbox'}"> ${esc(f.label)}</label>`); const i = $('input', l);
    if (i.type === 'checkbox') { i.checked = !!(f.get && f.get()); i.addEventListener('change', () => f.set && f.set(i.checked)); }
    else { i.value = f.get ? f.get() : ''; i.addEventListener('change', () => f.set && f.set(i.value)); }
    p.append(l);
  });
  actions.forEach(a => { const b = el(`<button type="button">${esc(a.label)}</button>`); b.addEventListener('click', () => a.onClick && a.onClick()); p.append(b); });
  doc.body.append(p);
  return p;
}

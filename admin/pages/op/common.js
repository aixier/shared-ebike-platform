// 运营商页面共用：账号与品牌缓存、状态标签、错误提示、文件下载、页面样式。
import { api } from '../../../shared/api.js';
import * as ui from '../../../shared/ui.js';

export { api, ui };
export const esc = ui.esc;
export const op = () => api.admin.operator;

/* ---------------- 页面样式（只作用于 op- 前缀） ---------------- */
const CSS = `
.op-stack{display:flex;flex-direction:column;gap:16px}
.op-grid2{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}
.op-grid3{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px}
@media (max-width:1180px){.op-grid2,.op-grid3{grid-template-columns:1fr}}
.op-row{display:flex;gap:12px;align-items:center;flex-wrap:wrap}
.op-note{font-size:12px;color:var(--ink-3)}
.op-mono{font-family:var(--font-mono);font-size:12px}
.op-strong{font-weight:600}
.op-link{background:none;border:0;padding:0;color:var(--orange-700);font-size:inherit;text-decoration:underline;cursor:pointer}
.op-link:hover{color:var(--orange-600)}
.op-panel-body-tight{padding:0}
.op-xl{width:min(1080px,96vw)!important}
.op-big{font-size:28px;font-weight:700;font-variant-numeric:tabular-nums;line-height:1.2}
.op-todo{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:10px 0;border-bottom:1px solid var(--line-2);font-size:13px}
.op-todo:last-child{border-bottom:0}
.op-todo .t{display:flex;flex-direction:column;gap:2px}
.op-bars{display:flex;align-items:flex-end;gap:6px;height:104px;padding-top:8px}
.op-bars .b{flex:1;min-width:8px;background:var(--orange-200);border-radius:4px 4px 0 0;position:relative;transition:background-color var(--d-fast) var(--ease)}
.op-bars .b:hover{background:var(--orange-500)}
.op-bars .b.is-today{background:var(--orange-500)}
.op-bars-x{display:flex;gap:6px;font-size:11px;color:var(--ink-3);margin-top:4px}
.op-bars-x span{flex:1;min-width:8px;text-align:center;white-space:nowrap;overflow:hidden}
.op-steps{display:grid;grid-auto-flow:column;grid-auto-columns:minmax(0,1fr);gap:0}
.op-step{position:relative;padding-right:10px;font-size:13px}
.op-step .dot{width:26px;height:26px;border-radius:50%;display:grid;place-items:center;font-size:12px;font-weight:700;background:var(--disabled-bg);color:var(--ink-2);margin-bottom:8px;position:relative;z-index:1}
.op-step::before{content:"";position:absolute;top:12px;left:26px;right:0;height:2px;background:var(--line)}
.op-step:last-child::before{display:none}
.op-step.is-done .dot{background:var(--ok);color:#fff}
.op-step.is-done::before{background:var(--ok)}
.op-step.is-now .dot{background:var(--orange-500);color:var(--on-orange);box-shadow:0 0 0 4px var(--orange-100)}
.op-step .t{font-weight:600;color:var(--ink)}
.op-step .s{font-size:12px;color:var(--ink-3);margin-top:2px;line-height:1.4}
@media (max-width:1100px){.op-steps{grid-auto-flow:row;gap:12px}.op-step::before{display:none}.op-step{display:grid;grid-template-columns:34px 1fr}.op-step .dot{margin:0}}
.op-drop{border:1.5px dashed var(--orange-200);border-radius:var(--r-md);background:var(--orange-50);padding:24px;text-align:center;display:flex;flex-direction:column;align-items:center;gap:8px;transition:border-color var(--d-fast) var(--ease),background-color var(--d-fast) var(--ease)}
.op-drop.is-over{border-color:var(--orange-500);background:var(--orange-100)}
.op-drop b{font-size:14px}
.op-errs{margin:0;padding-left:16px;list-style:disc;font-size:12px;color:var(--err)}
.op-code{position:relative;background:var(--surface-2);border:1px solid var(--line);border-radius:var(--r-sm);padding:12px 14px;font-family:var(--font-mono);font-size:12px;line-height:1.6;overflow:auto;white-space:pre;color:var(--ink)}
.op-code-wrap{position:relative}
.op-code-wrap>.a-btn{position:absolute;top:6px;right:6px;background:var(--surface)}
.op-doc{display:grid;grid-template-columns:188px minmax(0,1fr);gap:28px;align-items:start}
@media (max-width:980px){.op-doc{grid-template-columns:1fr}}
.op-toc{position:sticky;top:0;display:flex;flex-direction:column;gap:2px;font-size:13px}
.op-toc button{text-align:left;border:0;background:none;padding:6px 10px;border-radius:var(--r-sm);color:var(--ink-2);min-height:32px;transition:background-color var(--d-fast) var(--ease)}
.op-toc button:hover{background:var(--orange-50);color:var(--ink)}
.op-toc button.is-on{background:var(--orange-100);color:var(--orange-700);font-weight:600}
.op-article{display:flex;flex-direction:column;gap:28px;min-width:0}
.op-article section{scroll-margin-top:8px}
.op-article h2{font-size:17px;margin-bottom:8px}
.op-article h3{font-size:14px;margin:16px 0 6px}
.op-article p{font-size:13px;color:var(--ink-2);margin:6px 0}
.op-article ul.op-list{list-style:disc;padding-left:20px;font-size:13px;color:var(--ink-2);display:flex;flex-direction:column;gap:4px;margin:6px 0}
.op-mini{width:100%;border-collapse:collapse;font-size:13px;margin:8px 0;background:var(--surface);border:1px solid var(--line)}
.op-mini th,.op-mini td{padding:8px 12px;text-align:left;border-bottom:1px solid var(--line-2);vertical-align:top}
.op-mini th{background:var(--orange-50);font-size:12px;color:var(--ink-2);font-weight:600;white-space:nowrap}
.op-mini tr:last-child td{border-bottom:0}
.op-case{display:grid;grid-template-columns:90px minmax(0,1fr) 80px 72px;gap:8px 12px;align-items:start;padding:10px 16px;border-bottom:1px solid var(--line-2);font-size:13px}
.op-case:last-child{border-bottom:0}
.op-case .id{font-family:var(--font-mono);font-size:12px;color:var(--ink-3)}
.op-case .dur{text-align:right;color:var(--ink-3);font-variant-numeric:tabular-nums}
.op-case-group{padding:8px 16px;background:var(--orange-50);font-size:12px;font-weight:600;color:var(--ink-2);border-bottom:1px solid var(--line-2)}
.op-case .why{grid-column:2/-1;background:var(--err-bg);color:var(--err-ink);border-radius:var(--r-sm);padding:8px 12px;display:flex;flex-direction:column;gap:4px}
.op-case .why b{font-weight:600}
.op-case .why .fix{color:var(--ink);background:var(--surface);border-radius:6px;padding:6px 10px;border:1px solid var(--line)}
.op-agent{display:inline-flex;align-items:center;font-size:11px;font-weight:600;padding:0 6px;border-radius:4px;background:var(--orange-100);color:var(--orange-700);margin-right:6px;line-height:18px}
.op-pending{display:flex;align-items:center;gap:10px;padding:20px 16px;font-size:13px;color:var(--ink-2)}
.op-pending .a-spin{width:16px;height:16px;border:2px solid var(--orange-500);border-right-color:transparent;border-radius:50%;animation:spin .8s linear infinite;display:inline-block}
.op-banner{display:flex;gap:12px;align-items:flex-start;border-radius:var(--r-md);padding:12px 16px;font-size:13px;border:1px solid var(--line);background:var(--surface)}
.op-banner.is-warn{background:var(--warn-bg);border-color:#F6D58A;color:var(--warn-ink)}
.op-banner.is-ok{background:var(--ok-bg);border-color:#A7E3B8;color:var(--ok-ink)}
.op-banner.is-info{background:var(--orange-50);border-color:var(--orange-200);color:var(--orange-700)}
.op-banner .grow{flex:1}
.op-tl-dot{display:inline-block}
.op-kvgrid{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px}
.op-kvgrid>div{background:var(--surface-2);border-radius:var(--r-sm);padding:10px 12px}
.op-kvgrid .k{font-size:12px;color:var(--ink-3)}
.op-kvgrid .v{font-size:16px;font-weight:700;margin-top:2px;font-variant-numeric:tabular-nums}
.op-radio-card{display:flex;gap:8px;align-items:center}
.op-scroll{max-height:320px;overflow:auto;border:1px solid var(--line);border-radius:var(--r-sm)}
.op-tag-list{display:flex;flex-wrap:wrap;gap:6px}
.op-tag{display:inline-flex;align-items:center;gap:4px;background:var(--orange-50);border:1px solid var(--orange-200);border-radius:var(--r-full);padding:0 4px 0 10px;font-size:12px;line-height:24px}
.op-tag button{border:0;background:none;width:20px;height:20px;border-radius:50%;color:var(--ink-2)}
.op-tag button:hover{background:var(--orange-100)}
`;
let styled = false;
export function injectStyle() {
  if (styled || document.getElementById('op-style')) { styled = true; return; }
  const s = document.createElement('style'); s.id = 'op-style'; s.textContent = CSS; document.head.append(s); styled = true;
}

/* ---------------- 账号、品牌 ---------------- */
let meCache = null;
export async function getMe(force = false) {
  if (!force && meCache && Date.now() - meCache.at < 15000) return meCache.v;
  const v = await api.admin.me();
  meCache = { at: Date.now(), v };
  return v;
}
export const can = (me, perm) => !!(me && me.permissions && me.permissions.includes(perm));

let brandCache = null;
/** 本运营商的品牌（名称、接入状态） */
export async function getBrands(force = false) {
  if (!force && brandCache && Date.now() - brandCache.at < 10000) return brandCache.v;
  const o = await api.admin.access.overview();
  brandCache = { at: Date.now(), v: o.brands };
  return o.brands;
}
export async function nowStr() { return (await api.system.time()).now; }

/* ---------------- 格式化 ---------------- */
export const dt = v => (v ? ui.datetime(v) : '—');
export const dts = v => (v ? ui.datetime(v, true) : '—');
export const fee2 = v => { const n = Number(v) || 0; const s = ui.number(Math.abs(n), 2); return n < 0 ? `−¥${s}` : `¥${s}`; };
export const loc = l => (Array.isArray(l) ? `${l[0].toFixed(5)}，${l[1].toFixed(5)}` : '—');
export const pct = v => ui.percent(v, 0);
export const addDays = (dateStr, n) => { const [y, m, d] = dateStr.slice(0, 10).split('-').map(Number); return ui.date(new Date(y, m - 1, d + n)); };
export const monthLabel = m => { const [y, mm] = String(m).split('-'); return `${y} 年 ${Number(mm)} 月`; };

/* ---------------- 标签 ---------------- */
const KIND = {
  codeStatus: { 已启用: 'ok', 待启用: 'warn', 已失效: 'mute' },
  vehicle: { 可用: 'ok', 骑行中: 'brand', 低电量: 'warn', 故障: 'err', 离线: 'mute', 码未启用: 'warn', 码已失效: 'mute' },
  order: { 已结束: 'ok', 骑行中: 'brand', 开锁失败: 'err', 待人工处理: 'warn' },
  access: { 已接入: 'ok', 联调中: 'brand', 暂停可租: 'warn', 未接入: 'mute', 已退出: 'mute' },
  settlement: { 待审核: 'warn', 已审核: 'brand', 已发送: 'brand', 已到账: 'ok', 未出单: 'mute' },
  dispute: { 待核实: 'warn', 已采纳: 'ok', 已驳回: 'err', 无需调整: 'mute' },
  result: { 通过: 'ok', 失败: 'err', 待确认: 'warn', 未开始: 'mute', 有效: 'ok', 停用: 'mute' },
};
export function chip(text, group) { const k = (KIND[group] || {})[text] || 'mute'; return `<span class="chip ${k}">${esc(text)}</span>`; }
export const chipRaw = (text, kind = 'mute') => `<span class="chip ${kind}">${esc(text)}</span>`;

/* ---------------- 提示与错误 ---------------- */
const NEXT = {
  NETWORK: '网络不稳定，请稍后重试，未产生任何变更',
  SERVER_BUSY: '服务繁忙，请稍后重试，未产生任何变更',
  READONLY: '如需恢复，请联系主管部门',
  SCOPE_DENIED: '请回到本运营商的数据继续操作',
  FORBIDDEN: '请联系账号管理员调整授权',
  ADMIN_NOT_LOGGED_IN: '请重新登录后再试',
};
export const errText = e => (e && e.message) || '没有完成，请稍后重试';
export function fail(e, prefix) {
  ui.toast(prefix ? `${prefix}：${errText(e)}` : errText(e), { type: 'err', hint: NEXT[e && e.code] || '请检查后重试，没有产生任何变更', duration: 4200 });
}
export function readonlyGuard(me, btn) {
  if (me && me.readonly) { ui.setDisabled(btn, '运营商已退出，账号为只读', '如有疑问，请联系主管部门'); return true; }
  return false;
}
export function lacks(btn, why, next) { ui.setDisabled(btn, why, next); return btn; }

/* ---------------- 构件 ---------------- */
export function pageHead(title, sub, actions) {
  const n = ui.el(`<div class="a-head"><div><h1>${esc(title)}</h1>${sub ? `<div class="sub">${esc(sub)}</div>` : ''}</div><div class="a-actions"></div></div>`);
  (actions || []).forEach(a => n.querySelector('.a-actions').append(a));
  return n;
}
/** 页头：外壳已提供标题与面包屑，这里只设副标题与右上角操作；脱离外壳时自己画一个 */
export function setHead(el, pctx, title, sub, actions) {
  if (pctx && typeof pctx.setActions === 'function') { pctx.setSub && pctx.setSub(sub || ''); pctx.setActions(actions || []); return; }
  el.append(pageHead(title, sub, actions));
}
export function btn(label, kind = 'default', onClick, extra = '') {
  const b = ui.el(`<button type="button" class="a-btn a-btn-${kind}" ${extra}>${esc(label)}</button>`);
  if (onClick) b.addEventListener('click', () => onClick(b));
  return b;
}
/** 点击后显示处理中，完成后恢复 */
export async function busy(b, fn) {
  if (b.dataset.busy) return;
  const old = b.innerHTML; b.dataset.busy = '1'; b.setAttribute('aria-busy', 'true'); b.innerHTML = `<span class="a-spin"></span>${old}`;
  try { return await fn(); } finally { b.innerHTML = old; delete b.dataset.busy; b.removeAttribute('aria-busy'); }
}
export function panel(title, bodyHtml, headRight = '') {
  const p = ui.el(`<section class="a-panel"><div class="a-panel-head"><h3>${esc(title)}</h3><div>${headRight}</div></div><div class="a-panel-body"></div></section>`);
  const body = p.querySelector('.a-panel-body');
  if (typeof bodyHtml === 'string') body.innerHTML = bodyHtml; else if (bodyHtml) body.append(bodyHtml);
  return p;
}
export const kv = pairs => `<dl class="a-dl">${pairs.map(([k, v]) => `<dt>${esc(k)}</dt><dd>${v == null || v === '' ? '—' : v}</dd>`).join('')}</dl>`;
export const stat = (k, v, d = '', extra = '') => `<div class="a-stat"${extra}><div class="k">${esc(k)}</div><div class="v">${v}</div>${d ? `<div class="d">${d}</div>` : ''}</div>`;
export const item = (label, control, { req = false, tip = '', id } = {}) => `<div class="a-item"><label${req ? ' class="req"' : ''}${id ? ` for="${id}"` : ''}>${esc(label)}</label>${control}${tip ? `<span class="tip">${esc(tip)}</span>` : ''}</div>`;

/** 载入块：骨架屏 → 内容；失败给重试 */
export async function loadInto(host, fn, render, { rows = 5 } = {}) {
  const run = async () => {
    host.innerHTML = ui.skeleton('lines', rows);
    try {
      const d = await fn();
      if (!host.isConnected) return;
      host.innerHTML = '';
      await render(d, host);
    } catch (e) {
      if (!host.isConnected) return;
      host.innerHTML = '';
      host.append(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: run }));
    }
  };
  await run();
}

/** 订阅跨端事件；页面离开后自动退订 */
export function watch(host, names, cb, wait = 500) {
  let t; const offs = [];
  const off = () => { clearTimeout(t); offs.forEach(o => o()); };
  names.forEach(n => offs.push(api.on(n, () => { if (!host.isConnected) { off(); return; } clearTimeout(t); t = setTimeout(cb, wait); })));
  return off;
}

/** 复制文本 */
export async function copyText(text, what = '内容') {
  try { await navigator.clipboard.writeText(text); }
  catch { const ta = document.createElement('textarea'); ta.value = text; document.body.append(ta); ta.select(); try { document.execCommand('copy'); } catch { /* 浏览器不允许时提示手动复制 */ } ta.remove(); }
  ui.toast(`${what}已复制`, { type: 'ok', duration: 1400 });
}

/* ---------------- 文件 ---------------- */
export function saveFile(name, text, type = 'text/csv;charset=utf-8') {
  const blob = new Blob([text], { type });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob); a.download = name; document.body.append(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 5000);
}
export function toCsv(cols, rows) {
  const q = v => { const s = v == null ? '' : String(v); return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s; };
  return '﻿' + [cols.map(c => q(c.title)).join(','), ...rows.map(r => cols.map(c => q(c.value(r))).join(','))].join('\r\n');
}
/** 解析 CSV（支持引号、转义、换行） */
export function parseCsv(text) {
  const rows = []; let row = [], cur = '', inQ = false;
  const s = text.replace(/^﻿/, '');
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (inQ) { if (c === '"') { if (s[i + 1] === '"') { cur += '"'; i++; } else inQ = false; } else cur += c; }
    else if (c === '"') inQ = true;
    else if (c === ',' || c === '\t') { row.push(cur); cur = ''; }
    else if (c === '\n' || c === '\r') { if (c === '\r' && s[i + 1] === '\n') i++; row.push(cur); cur = ''; if (row.some(x => x.trim() !== '')) rows.push(row); row = []; }
    else cur += c;
  }
  row.push(cur); if (row.some(x => x.trim() !== '')) rows.push(row);
  return rows.map(r => r.map(x => x.trim()));
}
/** 读取用户选的文件，先按 UTF-8，不行再按 GBK（电子表格另存的 CSV 常见） */
export async function readTextFile(file) {
  const buf = await file.arrayBuffer();
  try { return new TextDecoder('utf-8', { fatal: true }).decode(buf); }
  catch { return new TextDecoder('gbk').decode(buf); }
}

export function go(hash) { location.hash = hash; }

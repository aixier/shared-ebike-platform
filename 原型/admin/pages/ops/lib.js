// 运维页面共用：样式加载、业务时间、错误提示、监听、取数渲染、角标。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';

export const { esc, $, $$, el } = ui;

export function loadCss() {
  if (document.getElementById('ops-css')) return;
  const l = document.createElement('link');
  l.id = 'ops-css'; l.rel = 'stylesheet'; l.href = new URL('./ops.css', import.meta.url).href;
  document.head.append(l);
}

export function query(params) {
  const raw = location.hash.split('?')[1] || '';
  return { ...Object.fromEntries(new URLSearchParams(raw)), ...(params || {}) };
}

/* ---------- 业务时间（以接口层时钟为准） ---------- */
let delta = 0;
export async function syncClock() {
  try {
    const t = await api.system.time();
    delta = new Date(t.now.replace(' ', 'T')).getTime() - Date.now();
  } catch { /* 取不到时退回浏览器时间 */ }
}
export const bizNow = () => new Date(Date.now() + delta);
export const toMs = s => new Date(String(s).replace(' ', 'T')).getTime();
export const minutesSince = s => Math.max(0, Math.floor((bizNow().getTime() - toMs(s)) / 60000));
export function durText(mins) {
  mins = Math.max(0, Math.round(mins));
  if (mins < 1) return '不足 1 分钟';
  if (mins < 60) return `${mins} 分钟`;
  const h = Math.floor(mins / 60), m = mins % 60;
  if (h < 24) return m ? `${h} 小时 ${m} 分钟` : `${h} 小时`;
  return `${Math.floor(h / 24)} 天 ${h % 24} 小时`;
}
export const nowInput = () => { const d = bizNow(), p = n => String(n).padStart(2, '0'); return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`; };
export const localInput = s => (s ? String(s).slice(0, 16).replace(' ', 'T') : '');
export const fromInput = v => (v ? `${v.replace('T', ' ')}:00` : '');
export const fmtDt = v => (v ? ui.datetime(v) : '—');
export const dash = v => (v == null || v === '' ? '—' : esc(v));
export const pct = v => (v == null ? '—' : ui.percent(v, 1));

/* ---------- 提示 ---------- */
export function errText(e) { return (e && e.message) || '服务繁忙，请稍后再试'; }
export function failToast(e, hint = '没有产生任何变更，可以重试') {
  ui.toast(errText(e), { type: 'err', hint: e && e.code === 'FORBIDDEN' ? '请换有权限的岗位处理' : hint });
}
export const chip = (text, kind = 'mute') => `<span class="chip ${kind}">${esc(text)}</span>`;
export const ACCESS_CHIP = { 已接入: 'ok', 联调中: 'brand', 暂停可租: 'warn', 未接入: 'mute', 已退出: 'mute' };
export const accessChip = s => chip(s, ACCESS_CHIP[s] || 'mute');
export const LEVEL_CHIP = { 严重: 'err', 警告: 'warn', 提示: 'brand' };
export const byTag = kind => `<span class="ops-by ${kind === '智能体' ? 'agent' : kind === '人' ? 'human' : 'sys'}">${esc(kind === '人' ? '人工' : kind)}</span>`;

export function watch(root, names, fn) {
  let timer;
  const offs = names.map(n => api.on(n, () => {
    if (!root.isConnected) { offs.forEach(o => o()); return; }
    clearTimeout(timer); timer = setTimeout(fn, 400);
  }));
  return () => { clearTimeout(timer); offs.forEach(o => o()); };
}

export async function fetchInto(box, loader, render, skel = ['lines', 4]) {
  box.innerHTML = ui.skeleton(skel[0], skel[1]);
  try {
    const data = await loader();
    if (!box.isConnected) return;
    box.innerHTML = '';
    await render(data, box);
  } catch (e) {
    if (!box.isConnected) return;
    box.innerHTML = '';
    box.append(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: () => fetchInto(box, loader, render, skel) }));
  }
}

export async function myPerms() {
  const me = await api.admin.me();
  return { me, has: p => me.permissions.includes(p), account: me.account };
}
export function goto(path) { location.hash = path; }

/** 订单抽屉里用到的小块：键值 */
export const kvRows = pairs => `<div class="ops-kv">${pairs.map(([k, v, full]) => `<div${full ? ' class="full"' : ''}><div class="k">${esc(k)}</div><div class="v">${v == null || v === '' ? '—' : v}</div></div>`).join('')}</div>`;

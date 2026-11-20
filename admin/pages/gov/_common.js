/* 主管部门页面共用：接口、界面库、生命周期、错误文案、样式注入 */
import { api } from '../../../shared/api.js';
import * as ui from '../../../shared/ui.js';

export { api, ui };
export const A = api.admin;
export const esc = ui.esc;
export const ROOT = new URL('../../../', import.meta.url);

/* 样式只注入一次 */
if (!document.getElementById('gov-css')) {
  const l = document.createElement('link');
  l.id = 'gov-css'; l.rel = 'stylesheet'; l.href = new URL('./gov.css', import.meta.url).href;
  document.head.append(l);
}

export const chip = (text, kind = 'mute') => `<span class="chip ${kind}">${esc(text)}</span>`;
export const ACCESS_KIND = { 未接入: 'mute', 联调中: 'warn', 已接入: 'ok', 暂停可租: 'warn', 已退出: 'err' };
export const PILE_KIND = { 在线: 'ok', 离线: 'mute', 故障: 'err' };
export const accessChip = s => chip(s, ACCESS_KIND[s] || 'mute');

/** 写操作失败文案：一句原因 + 没有产生变更 */
export function failText(e) {
  if (!e) return '操作没有成功，没有产生任何变更，请重试';
  if (e.code === 'NETWORK' || e.code === 'SERVER_BUSY') return '网络不稳定，这次操作没有保存，没有产生任何变更，请重试';
  if (e.code === 'ADMIN_NOT_LOGGED_IN') return '登录已失效，请重新登录后再操作';
  return e.message || '操作没有成功，没有产生任何变更，请重试';
}
/** 读操作失败（页面级重试） */
export function loadFailText(e) {
  if (e && (e.code === 'FORBIDDEN' || e.code === 'ADMIN_NOT_LOGGED_IN')) return e.message;
  return '数据没有加载出来，没有产生任何变更，请检查网络后重试';
}

export const bizNow = async () => (await api.system.time()).now;
export const toLocalInput = s => String(s).slice(0, 16).replace(' ', 'T');
export const fromLocalInput = s => `${String(s).replace('T', ' ')}:00`;

/** 页面生命周期：订阅事件，页面被替换后自动退订 */
export function lifecycle(root) {
  const offs = [];
  let dead = false;
  const life = {
    get alive() { return !dead && root.isConnected; },
    on(name, cb) {
      const off = api.on(name, ev => { if (!life.alive) { off(); return; } cb(ev); });
      offs.push(off);
    },
    dispose() { dead = true; offs.splice(0).forEach(f => f()); },
  };
  return life;
}

/** 在容器里加载数据：骨架屏、失败重试 */
export async function loadInto(box, fetcher, render, skeletonRows = 4) {
  box.innerHTML = `<div class="a-panel">${ui.skeleton('lines', skeletonRows)}</div>`;
  try {
    const data = await fetcher();
    if (!box.isConnected) return;
    box.innerHTML = '';
    await render(data);
  } catch (e) {
    if (!box.isConnected) return;
    box.innerHTML = '';
    const p = document.createElement('div'); p.className = 'a-panel';
    p.append(ui.retry({ text: loadFailText(e), onRetry: () => loadInto(box, fetcher, render, skeletonRows) }));
    box.append(p);
  }
}

/** 页头由外壳负责（标题、面包屑）；页面只设置副标题和右侧操作按钮。返回 {按钮id: 元素} */
export function setHead(pctx, sub, actions = []) {
  const btns = {};
  actions.forEach(a => {
    const b = ui.el(`<button type="button" class="a-btn a-btn-${a.kind || 'default'}">${esc(a.label)}</button>`);
    btns[a.id] = b;
  });
  if (pctx) { pctx.setSub(sub || ''); pctx.setActions(Object.values(btns)); }
  return btns;
}

export function kv(rows) {
  return `<dl class="a-dl">${rows.map(([k, v]) => `<dt>${esc(k)}</dt><dd>${v == null || v === '' ? '—' : v}</dd>`).join('')}</dl>`;
}

export const pct = (r, d = 2) => `${(Number(r) * 100).toFixed(d).replace(/\.?0+$/, '')}%`;
export const yuan = v => ui.money(v);

/** 下载文本文件（带 BOM，Excel 直接打开不乱码） */
export function download(name, text, type = 'text/csv;charset=utf-8') {
  const blob = new Blob(['\ufeff', text], { type });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob); a.download = name;
  document.body.append(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 4000);
}

/** 取证照片地址（数据目录下的相对路径） */
export const photoUrl = p => new URL(`mockdata/${p}`, ROOT).href;

/** 操作留痕时间线（按对象编号），gov 账号有 audit.view */
export async function auditTimeline(targetId, limit = 6) {
  try {
    const r = await A.audit.list({ targetId, pageSize: limit });
    if (!r.rows.length) return '<div class="a-muted">还没有调整记录</div>';
    return `<ul class="a-timeline">${r.rows.map(x => `<li><div>${esc(x.summary)}</div><div class="t">${ui.datetime(x.at)} · ${esc(x.actorName || '')}${x.reason ? ` · 事由：${esc(x.reason)}` : ''}</div></li>`).join('')}</ul>`;
  } catch (e) {
    return '<div class="a-muted">调整记录暂时没有加载出来</div>';
  }
}

/* 管理端公共工具：列表加详情抽屉、危险操作确认、筛选条、下载等。
   各岗位页面 import { kit } from '../../kit.js'（shell.js 同样再导出）。说明见 admin/README.md。 */
import * as ui from '../shared/ui.js';
import { ApiError } from '../shared/api.js';

const state = { me: null, go: null };

/** 由 shell 在登录后注入当前账号信息与跳转函数 */
export function _bind(me, go) { state.me = me; state.go = go; }

/** 当前登录信息：{account, menus, permissions, readonly, operator, todos}；未登录为 null */
export const session = () => state.me;
/** 是否拥有某权限点（字符串或数组，满足其一即可） */
export const can = p => !!state.me && [].concat(p).some(x => state.me.permissions.includes(x));
/** 路由跳转，例如 go('#/common/audit?targetId=OD2026102613011') */
export const go = h => { location.hash = h.startsWith('#') ? h : '#' + h; };

/* ---------- 错误文案 ---------- */
/** 把接口错误变成「一句原因 + 下一步」 */
export function errText(e) {
  if (!e) return '操作没有成功，请重试';
  const base = e.message || '操作没有成功，请重试';
  if (e.code === 'NETWORK' || e.code === 'SERVER_BUSY') return `${base}，没有产生任何变更，可以直接再试一次`;
  const next = e.next && e.next.label ? `（${e.next.label}）` : '';
  return base + next;
}
/** 用 toast 展示接口错误；登录失效交给 shell 统一处理，这里不重复提示 */
export function fail(e, title) {
  if (e instanceof ApiError && (e.code === 'ADMIN_NOT_LOGGED_IN')) return;
  const msg = errText(e);
  if (title) ui.toast(title, { type: 'err', hint: msg });
  else ui.toast(msg, { type: 'err' });
}

/* ---------- 小件 ---------- */
/** 状态标签：kind 取 ok / warn / err / mute / brand */
export const chip = (text, kind = 'mute') => `<span class="chip ${kind}">${ui.esc(text)}</span>`;
/** 键值列表，pairs: [[键, HTML 或文字(已转义)], …]；值为 null 时显示「—」 */
export const kv = pairs => `<dl class="a-dl">${pairs.map(([k, v]) => `<dt>${ui.esc(k)}</dt><dd>${v == null || v === '' ? '<span class="a-mute">—</span>' : v}</dd>`).join('')}</dl>`;
/** 时间线，items: [{title, time, note}]，title/note 需已转义 */
export const timeline = items => `<ul class="a-timeline">${items.map(i => `<li><div>${i.title}</div>${i.note ? `<div class="a-mute">${i.note}</div>` : ''}${i.time ? `<div class="t">${ui.esc(i.time)}</div>` : ''}</li>`).join('')}</ul>`;
/** 抽屉内的小节：标题 + 内容 */
export const section = (title, html) => `<div><div class="a-sec-title">${ui.esc(title)}</div>${html}</div>`;
/** 本地下载文本文件（CSV 自动带 BOM，Excel 打开不乱码） */
export function download(fileName, text, mime = 'text/csv;charset=utf-8') {
  const bom = /csv$/i.test(fileName) ? '﻿' : '';
  const url = URL.createObjectURL(new Blob([bom + text], { type: mime }));
  const a = Object.assign(document.createElement('a'), { href: url, download: fileName });
  document.body.append(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 2000);
}

/* ---------- 筛选条 ---------- */
/**
 * filterBar(container, fields, {onSearch, initial})
 * fields: [{key, label, type:'text'|'select'|'date', options:[{value,label}|'文字'], placeholder, width, grow}]
 * 文本框回车或点「查询」触发，下拉与日期改变即触发。返回 {values(), set(obj, silent), reset()}；values() 不含空值。
 */
export function filterBar(container, fields, { onSearch, initial = {} } = {}) {
  const uid = 'fb' + Math.random().toString(36).slice(2, 7);
  const form = ui.el(`<form class="a-filter" novalidate>${fields.map(f => {
    const id = `${uid}-${f.key}`;
    let ctl;
    if (f.type === 'select') ctl = `<select id="${id}" name="${ui.esc(f.key)}" class="a-select"${f.width ? ` style="min-width:${f.width}"` : ''}>${[{ value: '', label: f.allLabel || '全部' }, ...(f.options || [])].map(o => { const v = typeof o === 'string' ? { value: o, label: o } : o; return `<option value="${ui.esc(v.value)}">${ui.esc(v.label)}</option>`; }).join('')}</select>`;
    else ctl = `<input id="${id}" name="${ui.esc(f.key)}" class="a-input${f.grow ? ' a-search' : ''}" type="${f.type === 'date' ? 'date' : 'text'}" autocomplete="off" placeholder="${ui.esc(f.placeholder || '')}"${f.width ? ` style="min-width:${f.width}"` : ''}>`;
    return `<div class="f${f.grow ? ' grow' : ''}"><label for="${id}">${ui.esc(f.label)}</label>${ctl}</div>`;
  }).join('')}<div class="f"><label>&nbsp;</label><div class="a-actions"><button type="submit" class="a-btn a-btn-primary">查询</button><button type="button" class="a-btn a-btn-default" data-reset>重置</button></div></div></form>`);
  container.innerHTML = ''; container.append(form);
  const read = () => { const o = {}; fields.forEach(f => { const v = form.elements[f.key].value.trim(); if (v !== '') o[f.key] = v; }); return o; };
  const set = (o = {}, silent = true) => { fields.forEach(f => { const c = form.elements[f.key]; if (c) c.value = o[f.key] ?? ''; }); if (!silent) onSearch && onSearch(read()); };
  set(initial);
  form.addEventListener('submit', e => { e.preventDefault(); onSearch && onSearch(read()); });
  ui.$$('select,input[type=date]', form).forEach(c => c.addEventListener('change', () => onSearch && onSearch(read())));
  ui.$('[data-reset]', form).addEventListener('click', () => { set({}); onSearch && onSearch(read()); });
  return { values: read, set, reset: () => { set({}); onSearch && onSearch(read()); }, el: form };
}

/* ---------- 列表 + 详情抽屉 ---------- */
/**
 * listDetail(container, {columns, load, rowKey, detail, pageSize, sort, empty, wide, titleOf})
 *   load(({page,pageSize,sort})) -> {rows,total}（直接返回 api 分页结果；筛选条件由你在 load 里读取）
 *   detail(row, handle) -> {title, sub, body(html|Element), footer:[{label, kind, onClick(handle), why, next, closeAfter}]}，可返回 Promise
 *       点击行时先打开带骨架屏的抽屉，详情取到后替换；取不到显示重试。footer 里带 why 的按钮置灰并说明原因与下一步。
 *   返回 {table, reload(), open(row), close()}；写操作成功后调 reload() 刷新列表、handle.refresh() 刷新抽屉。
 */
export function listDetail(container, opts) {
  const { detail, titleOf, wide = false, ...tableOpts } = opts;
  let cur = null;
  const api = {
    table: null,
    reload: () => api.table.reload(),
    close: () => cur && cur.close(),
    open: async row => {
      cur && cur.close();
      const dr = ui.drawer({ title: titleOf ? titleOf(row) : String(row[opts.rowKey || 'id'] ?? ''), body: ui.skeleton('lines', 7), footer: [], wide, onClose: () => { if (cur === handle) cur = null; } });
      const foot = ui.$('.a-drawer-foot', dr.el);
      foot.hidden = true;
      const handle = { row, close: dr.close, el: dr.el, body: dr.body, reload: api.reload, refresh: () => fill() };
      cur = handle;
      async function fill() {
        dr.setBody(ui.skeleton('lines', 7));
        try {
          const d = await detail(row, handle);
          if (cur !== handle) return;
          if (d.title) ui.$('h2', dr.el).textContent = d.title;
          const head = ui.$('.a-drawer-head > div', dr.el);
          let sub = ui.$('.sub', head); if (d.sub != null) { if (!sub) { sub = ui.el('<div class="sub"></div>'); head.append(sub); } sub.textContent = d.sub; }
          dr.setBody(d.body || '');
          foot.innerHTML = '';
          (d.footer || []).forEach(a => {
            const b = ui.el(`<button type="button" class="a-btn a-btn-${a.kind || 'default'}">${ui.esc(a.label)}</button>`);
            if (a.why) ui.setDisabled(b, a.why, a.next);
            else b.addEventListener('click', async () => { const r = await a.onClick(handle); if (a.closeAfter && r !== false) dr.close(); });
            foot.append(b);
          });
          foot.hidden = !(d.footer && d.footer.length);
        } catch (e) {
          if (cur !== handle) return;
          dr.setBody(ui.retry({ text: errText(e), onRetry: fill }));
        }
      }
      fill();
    },
  };
  api.table = ui.table(container, { ...tableOpts, onRowClick: row => api.open(row) });
  return api;
}

/* ---------- 危险操作：二次确认 + 必填事由 ---------- */
/**
 * dangerAction({title, message(HTML), impact, confirmText, reasons:[常用事由], danger:true, run(reason)->Promise, success, hint, onDone(result,reason)})
 * 弹出确认框（事由至少 4 个字），确认后在弹窗内执行 run(reason)；失败留在弹窗内说明「没有产生任何变更」，成功后 toast 并返回 {reason, result}，取消返回 null。
 * run 里把 reason 原样传给带 reason 参数的接口，接口会自动写入操作留痕。
 */
export async function dangerAction({ title, message, impact, confirmText = '确认', reasons, danger = true, run, success = '已完成', hint = '已写入操作留痕', onDone, minLen, requireReason = true }) {
  let result;
  const r = await ui.confirm({
    title, message, impact, danger, confirmText, reasons, minLen, requireReason,
    onConfirm: async reason => { try { result = await run(reason); } catch (e) { throw new Error(errText(e)); } },
  });
  if (!r) return null;
  ui.toast(success, { type: 'ok', hint: requireReason ? hint : '' });
  onDone && onDone(result, r.reason);
  return { reason: r.reason, result };
}

/** 同上，但不要求事由（非危险的普通确认）。 */
export const simpleConfirm = o => dangerAction({ ...o, danger: false, requireReason: false });

/** 给按钮加上「置灰并说明」：无权限、状态不允许时用，点击会提示原因与下一步 */
export const lock = (btn, why, next) => ui.setDisabled(btn, why, next);

export const kit = { session, can, go, errText, fail, chip, kv, timeline, section, download, filterBar, listDetail, dangerAction, simpleConfirm, lock, ui };

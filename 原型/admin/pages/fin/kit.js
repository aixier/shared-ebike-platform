// 财务页面共用的小工具：页面生命周期、区块加载、表单加二次确认、金额与下载。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';

export { ui, api };
export const esc = ui.esc;

/* ---------- 金额与格式 ---------- */
/** 结算服务费：两位小数 */
export const fee2 = (v, o = {}) => {
  const n = Number(v) || 0;
  const s = ui.number(Math.abs(n), 2);
  return n < 0 ? `−¥${s}` : `${o.sign && n > 0 ? '+' : ''}¥${s}`;
};
/** 用户侧金额带符号 */
export const money = (v, o) => ui.money(v, o);
export const chip = (text, kind = 'mute') => `<span class="chip ${kind}">${esc(text)}</span>`;
export const pct = r => `${+((Number(r) || 0) * 100).toFixed(2)}%`;
export const dt = (v, sec = false) => (v ? ui.datetime(v, sec) : '—');
export const mono = v => `<span class="mono">${esc(v ?? '—')}</span>`;
/** 按分计算的四舍五入（half up），与结算口径一致：服务费 = round(基数 × 比例) */
export const feeOf = (base, ratio) => Math.floor(Math.round(Number(base) * 100) * Number(ratio) + 0.5 + 1e-7) / 100;
export const sum2 = list => Math.round(list.reduce((s, x) => s + Math.round(x * 100), 0)) / 100;

/** 账号编号转姓名：当前登录人显示姓名，其余显示账号编号 */
export async function whoName(id) {
  if (!id) return '—';
  const me = await api.admin.me().catch(() => null);
  return me && me.account.accountId === id ? me.account.name : `账号 ${id}`;
}

/** 业务当前时间（不用浏览器时间） */
export async function nowInfo() {
  const t = await api.system.time();
  return { now: t.now, label: t.label, today: String(t.now).slice(0, 10), labelMonth: String(t.label).slice(0, 7) };
}
export function monthAdd(m, k) {
  const [y, mo] = m.split('-').map(Number);
  const d = new Date(y, mo - 1 + k, 1);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
}

/** 从路由参数或网址里取查询参数 */
export function qp(params) {
  const out = { ...(params && typeof params === 'object' && !(params instanceof Event) ? params : {}) };
  const h = location.hash.split('?')[1];
  if (h) for (const [k, v] of new URLSearchParams(h)) if (out[k] == null) out[k] = v;
  return out;
}

export function download(name, text) {
  const b = new Blob(['﻿' + text], { type: 'text/csv;charset=utf-8' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(b); a.download = name;
  document.body.append(a); a.click();
  setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 0);
}
export const csvCell = v => { const s = String(v ?? ''); return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s; };
export const toCsv = (head, rows) => [head, ...rows].map(r => r.map(csvCell).join(',')).join('\r\n');

/* ---------- 页面骨架 ---------- */
/** 页面对象：mount 返回清理函数，离开页面时关闭抽屉、取消订阅 */
export function page({ path, title, menu, perm, build }) {
  let cleanups = [];
  const p = {
    path, title, menu, perm,
    mount(el, params, pctx) {
      p.unmount();
      el.innerHTML = '';
      const ctx = {
        el, params: qp(params), alive: () => el.isConnected,
        head(sub, actions = []) { if (pctx) { pctx.setSub(sub || ''); pctx.setActions(actions); } },
        onCleanup: f => cleanups.push(f),
        track(d) { cleanups.push(() => d && d.close && d.close()); return d; },
        watch(events, fn) {
          let t = null;
          const offs = events.map(e => api.on(e, () => { clearTimeout(t); t = setTimeout(() => el.isConnected && fn(), 350); }));
          cleanups.push(() => { clearTimeout(t); offs.forEach(o => o && o()); });
        },
      };
      build(ctx);
      return p.unmount;
    },
    unmount() { const c = cleanups; cleanups = []; c.forEach(f => { try { f(); } catch { /* 忽略清理失败 */ } }); },
  };
  return p;
}

/** 区块加载：骨架屏、失败重试；返回重新加载函数 */
export function section(box, load, render, { rows = 4 } = {}) {
  let seq = 0;
  async function run(quiet) {
    const my = ++seq;
    if (!quiet) box.innerHTML = ui.skeleton('lines', rows);
    try {
      const data = await load();
      if (my !== seq || !box.isConnected) return;
      box.innerHTML = '';
      render(data, box, run);
    } catch (e) {
      if (my !== seq || !box.isConnected) return;
      box.innerHTML = '';
      box.append(ui.retry({ text: `${(e && e.message) || '数据没有加载出来'}，页面上的数据没有任何变更`, onRetry: () => run() }));
    }
  }
  run();
  return run;
}

/** 按钮处理中状态 */
export async function busy(btn, fn) {
  if (btn.dataset.busy) return;
  btn.dataset.busy = '1';
  const old = btn.innerHTML;
  btn.setAttribute('aria-busy', 'true'); btn.style.pointerEvents = 'none';
  btn.innerHTML = '<span class="a-spin"></span>处理中';
  try { return await fn(); }
  finally { btn.innerHTML = old; btn.removeAttribute('aria-busy'); btn.style.pointerEvents = ''; delete btn.dataset.busy; }
}

export function failToast(e, hint) {
  ui.toast((e && e.message) || '操作没有成功', { type: 'err', hint: hint || '没有产生任何变更，可以重试' });
}

/* ---------- 先填写、再二次确认 ---------- */
const fieldHtml = f => {
  const id = `f-${f.name}`;
  let ctl;
  if (f.type === 'select') ctl = `<select class="a-select" id="${id}" name="${f.name}">${f.options.map(o => `<option value="${esc(o.v)}"${String(o.v) === String(f.value ?? '') ? ' selected' : ''}>${esc(o.t)}</option>`).join('')}</select>`;
  else if (f.type === 'textarea') ctl = `<textarea class="a-textarea" id="${id}" name="${f.name}" placeholder="${esc(f.placeholder || '')}">${esc(f.value || '')}</textarea>`;
  else if (f.type === 'readonly') ctl = `<div class="a-readonly" style="min-height:34px;display:flex;align-items:center;font-size:13px;color:var(--ink)">${f.html || esc(f.value)}</div>`;
  else ctl = `<input class="a-input" id="${id}" name="${f.name}" type="${f.type || 'text'}" value="${esc(f.value ?? '')}" placeholder="${esc(f.placeholder || '')}" autocomplete="off" ${f.step ? `step="${f.step}"` : ''} ${f.max ? `max="${f.max}"` : ''}>`;
  return `<div class="a-item"><label for="${id}"${f.required ? ' class="req"' : ''}>${esc(f.label)}</label>${ctl}${f.tip ? `<span class="tip">${esc(f.tip)}</span>` : ''}</div>`;
};

/** 弹出表单，校验通过后返回取值；取消返回 null。表单不负责落库，落库由随后的二次确认完成。 */
export function askForm({ title, intro = '', fields, rules, nextText = '下一步' }) {
  return new Promise(resolve => {
    const body = ui.el(`<div style="display:contents">${intro ? `<div>${intro}</div>` : ''}<form class="a-form" novalidate>${fields.map(fieldHtml).join('')}</form></div>`);
    const form = ui.$('form', body);
    form.addEventListener('submit', e => e.preventDefault());
    const m = ui.modal({
      title, body, icon: 'i',
      onClose: () => resolve(null),
      actions: [
        { label: '取消', kind: 'default' },
        { label: nextText, kind: 'primary', keepOpen: true, onClick() {
          const r = ui.validate(form, rules);
          if (!r.ok) { m.el.classList.remove('is-shake'); void m.el.offsetWidth; m.el.classList.add('is-shake'); return; }
          resolve(r.values); m.close();
        } },
      ],
    });
  });
}

export const REASON_RULE = { label: '事由', required: true, min: 4, max: 200 };

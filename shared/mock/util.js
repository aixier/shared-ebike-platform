// 通用小工具：时间、金额（分）、几何、编号、摘要。纯函数，无状态。

export const p2 = n => String(n).padStart(2, '0');

/** Date -> 'YYYY-MM-DD HH:MM:SS'（本地时间，即北京时间） */
export function fmt(d) {
  d = d instanceof Date ? d : new Date(d);
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
}
export const fmtDate = d => fmt(d).slice(0, 10);
export const fmtMonth = d => fmt(d).slice(0, 7);

/** 'YYYY-MM-DD HH:MM:SS' 或 'YYYY-MM-DD' -> Date（本地） */
export function parse(s) {
  if (s instanceof Date) return new Date(s.getTime());
  if (typeof s === 'number') return new Date(s);
  const m = /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2})(?::(\d{2}))?)?/.exec(String(s || ''));
  if (!m) return new Date(NaN);
  return new Date(+m[1], +m[2] - 1, +m[3], +(m[4] || 0), +(m[5] || 0), +(m[6] || 0));
}
export const ms = s => (s == null ? NaN : parse(s).getTime());
export const TIME_RE = /^\d{4}-\d{2}-\d{2}( \d{2}:\d{2}:\d{2})?$/;

/** 平移天数，保留时分秒 */
export function shiftStr(s, days) {
  const d = parse(s);
  d.setDate(d.getDate() + days);
  return s.length === 10 ? fmtDate(d) : fmt(d);
}
/** 深度平移：值匹配 TIME_RE 的字符串都加 days 天（SCHEMA 第 3 节） */
export function shiftDeep(x, days) {
  if (!days) return x;
  if (Array.isArray(x)) { for (let i = 0; i < x.length; i++) x[i] = shiftAny(x[i], days); return x; }
  if (x && typeof x === 'object') { for (const k of Object.keys(x)) x[k] = shiftAny(x[k], days); return x; }
  return x;
}
function shiftAny(v, days) {
  if (typeof v === 'string') return TIME_RE.test(v) ? shiftStr(v, days) : v;
  if (v && typeof v === 'object') return shiftDeep(v, days);
  return v;
}
export function addSec(s, sec) { return fmt(ms(s) + sec * 1000); }
export function addDays(s, n) { const d = parse(s); d.setDate(d.getDate() + n); return fmt(d); }
/** 加 n 个工作日（周一至周五，不含节假日），到账时刻 18:00 */
export function addWorkdays(s, n) {
  const d = parse(s);
  let left = n;
  while (left > 0) { d.setDate(d.getDate() + 1); const w = d.getDay(); if (w !== 0 && w !== 6) left--; }
  d.setHours(18, 0, 0, 0);
  return fmt(d);
}
export function monthAdd(m, k) { const [y, mo] = m.split('-').map(Number); const d = new Date(y, mo - 1 + k, 1); return `${d.getFullYear()}-${p2(d.getMonth() + 1)}`; }

/* ---------- 金额：一律整数分计算 ---------- */
export const C = y => Math.round((Number(y) || 0) * 100);
export const Y = c => Math.round(c) / 100;

/* ---------- 几何 ---------- */
const R = 6371008.8;
export function dist(a, b) {
  const rad = Math.PI / 180;
  const dLat = (b[1] - a[1]) * rad, dLng = (b[0] - a[0]) * rad;
  const h = Math.sin(dLat / 2) ** 2 + Math.cos(a[1] * rad) * Math.cos(b[1] * rad) * Math.sin(dLng / 2) ** 2;
  return 2 * R * Math.asin(Math.sqrt(h));
}
export function inPolygon(pt, poly) {
  let inside = false;
  for (let i = 0, j = poly.length - 1; i < poly.length; j = i++) {
    const [xi, yi] = poly[i], [xj, yj] = poly[j];
    if ((yi > pt[1]) !== (yj > pt[1]) && pt[0] < (xj - xi) * (pt[1] - yi) / (yj - yi) + xi) inside = !inside;
  }
  return inside;
}
/** 点到多边形的距离（米），在内为 0 */
export function distToPolygon(pt, poly) {
  if (inPolygon(pt, poly)) return 0;
  const kx = 111320 * Math.cos(pt[1] * Math.PI / 180), ky = 110540;
  const P = q => [(q[0] - pt[0]) * kx, (q[1] - pt[1]) * ky];
  let best = Infinity;
  for (let i = 0; i < poly.length - 1; i++) {
    const a = P(poly[i]), b = P(poly[i + 1]);
    const dx = b[0] - a[0], dy = b[1] - a[1];
    const t = Math.max(0, Math.min(1, -(a[0] * dx + a[1] * dy) / (dx * dx + dy * dy || 1)));
    best = Math.min(best, Math.hypot(a[0] + t * dx, a[1] + t * dy));
  }
  return best;
}
export function polygonArea(poly) { // 平方米，近似
  const lat = poly[0][1], kx = 111320 * Math.cos(lat * Math.PI / 180), ky = 110540;
  let s = 0;
  for (let i = 0; i < poly.length - 1; i++) s += poly[i][0] * kx * poly[i + 1][1] * ky - poly[i + 1][0] * kx * poly[i][1] * ky;
  return Math.abs(s / 2);
}
export function centroid(poly) {
  const pts = poly.slice(0, -1);
  return [pts.reduce((s, p) => s + p[0], 0) / pts.length, pts.reduce((s, p) => s + p[1], 0) / pts.length].map(v => +v.toFixed(6));
}

/* ---------- 随机（可注入，自测时固定种子） ---------- */
export function seeded(seed = 20261201) {
  let s = seed >>> 0;
  return () => { s = (s + 0x6D2B79F5) >>> 0; let t = s; t = Math.imul(t ^ (t >>> 15), t | 1); t ^= t + Math.imul(t ^ (t >>> 7), t | 61); return ((t ^ (t >>> 14)) >>> 0) / 4294967296; };
}
export function hash32(str) { let h = 2166136261; for (const ch of String(str)) { h ^= ch.codePointAt(0); h = Math.imul(h, 16777619); } return h >>> 0; }

/* ---------- 身份证：年龄 ---------- */
const ID_W = [7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2];
const ID_C = '10X98765432';
export function idValid(id) {
  id = String(id || '').toUpperCase();
  if (!/^\d{17}[\dX]$/.test(id)) return false;
  const sum = ID_W.reduce((s, w, i) => s + w * +id[i], 0);
  if (ID_C[sum % 11] !== id[17]) return false;
  const b = parse(`${id.slice(6, 10)}-${id.slice(10, 12)}-${id.slice(12, 14)}`);
  return !isNaN(b) && fmtDate(b) === `${id.slice(6, 10)}-${id.slice(10, 12)}-${id.slice(12, 14)}`;
}
export function idCheckChar(first17) { return ID_C[ID_W.reduce((s, w, i) => s + w * +first17[i], 0) % 11]; }
export function ageAt(id, at) {
  const y = +id.slice(6, 10), m = +id.slice(10, 12), d = +id.slice(12, 14);
  const t = parse(at);
  let age = t.getFullYear() - y;
  if (t.getMonth() + 1 < m || (t.getMonth() + 1 === m && t.getDate() < d)) age--;
  return age;
}
export const maskId = id => `${id.slice(0, 3)}***********${id.slice(-4)}`;
export const maskPhone = p => `${p.slice(0, 3)}****${p.slice(-4)}`;
export const maskName = n => n ? n[0] + '＊'.repeat(Math.max(1, [...n].length - 1)) : n;
export const maskAccount = a => { const s = String(a).replace(/\s/g, ''); return `${s.slice(0, 4)} **** **** ${s.slice(-4)}`; };

/* ---------- 摘要 ---------- */
export async function sha256hex(text) {
  const buf = await globalThis.crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
  return [...new Uint8Array(buf)].map(b => b.toString(16).padStart(2, '0')).join('');
}

export const clone = v => (v === undefined ? v : JSON.parse(JSON.stringify(v)));

/** 列表：筛选后排序分页。sort={key,dir} */
export function paginate(rows, { page = 1, pageSize = 20, sort } = {}) {
  let all = rows;
  if (sort && sort.key) {
    const k = sort.key, dir = sort.dir === 'desc' ? -1 : 1;
    all = [...rows].sort((a, b) => {
      const x = a[k], y = b[k];
      const r = typeof x === 'number' && typeof y === 'number' ? x - y : String(x ?? '').localeCompare(String(y ?? ''), 'zh-CN');
      return r * dir;
    });
  }
  const total = all.length;
  page = Math.max(1, +page || 1); pageSize = Math.max(1, Math.min(500, +pageSize || 20));
  return { rows: all.slice((page - 1) * pageSize, page * pageSize), total, page, pageSize };
}
export function csv(columns, rows) {
  const q = v => { const s = v == null ? '' : String(v); return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s; };
  return '﻿' + [columns.map(c => q(c.title)).join(','), ...rows.map(r => columns.map(c => q(typeof c.value === 'function' ? c.value(r) : r[c.key])).join(','))].join('\n');
}

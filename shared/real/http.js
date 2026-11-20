// 真实接口的 HTTP 层：同源 /api/<模块>/...，令牌放 Authorization 头，写接口带 Idempotency-Key，错误体转 ApiError。
import { ApiError } from '../mock/errors.js';
import { fromWire } from './convert.js';

let seq = 0;
const newKey = () => `fe-${Date.now().toString(36)}-${(++seq).toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

/**
 * 创建 HTTP 客户端。opts：
 *   base         前缀，浏览器默认 ''（同源），Node 自测传 'http://127.0.0.1:18300'
 *   fetch        默认全局 fetch
 *   store        {get(k), set(k,v), remove(k)}，存令牌；默认 localStorage（键 bike.real.<名字>Token），失败退回内存
 */
export function createHttp(opts = {}) {
  const base = opts.base || '';
  const doFetch = opts.fetch || ((...a) => fetch(...a));
  const mem = new Map();
  const store = opts.store || {
    get: k => { try { return localStorage.getItem(k); } catch { return mem.get(k) ?? null; } },
    set: (k, v) => { try { localStorage.setItem(k, v); } catch { mem.set(k, v); } },
    remove: k => { try { localStorage.removeItem(k); } catch { mem.delete(k); } },
  };
  const tkey = who => `bike.real.${who}Token`;
  const ekey = who => `bike.real.${who}Exp`;
  // 管理令牌与执法令牌有效期 30 分钟：记下到期时刻（毫秒），到期前用 /api/iam/token/refresh 续期
  const parseExp = s => { const m = typeof s === 'string' && /^(\d{4}-\d\d-\d\d)[ T](\d\d:\d\d:\d\d)$/.exec(s); const t = m ? Date.parse(`${m[1]}T${m[2]}+08:00`) : Date.parse(s); return Number.isNaN(t) ? null : t; };
  const tokens = {
    get: who => store.get(tkey(who)) || null,
    set: (who, t, expiresAt) => { store.set(tkey(who), t); const e = expiresAt ? parseExp(expiresAt) : null; if (e) store.set(ekey(who), String(e)); else store.remove(ekey(who)); },
    clear: who => { store.remove(tkey(who)); store.remove(ekey(who)); },
    expiresAt: who => { const v = Number(store.get(ekey(who))); return v > 0 ? v : null; },
  };
  const REFRESH_AHEAD_MS = opts.refreshAheadMs ?? 5 * 60 * 1000;
  const now = opts.now || (() => Date.now());
  const sessionLost = who => {
    tokens.clear(who);
    if (opts.onSessionLost) { try { opts.onSessionLost(who); } catch { /* 忽略 */ } }
    else if (typeof window !== 'undefined' && window.dispatchEvent && typeof CustomEvent !== 'undefined') window.dispatchEvent(new CustomEvent('bike:session-lost', { detail: { who } }));
  };
  const refreshing = {};
  /** 到期前续期（同一端同时只发一次）。续期被拒（过期、停用、会话作废）清令牌并通知页面回登录页；网络错误不清，下次再试 */
  function refresh(who) {
    if (refreshing[who]) return refreshing[who];
    refreshing[who] = (async () => {
      let res, data = null;
      try { res = await doFetch(base ? new URL(base + '/api/iam/token/refresh').toString() : '/api/iam/token/refresh', { method: 'POST', headers: { Accept: 'application/json', Authorization: `Bearer ${tokens.get(who)}`, 'Content-Type': 'application/json', 'Idempotency-Key': newKey() }, body: '{}' }); }
      catch { return false; }
      try { data = await res.json(); } catch { /* 非 JSON */ }
      if (res.ok && data && data.token) { tokens.set(who, data.token, data.expiresAt); return true; }
      if (res.status === 401 || res.status === 403) { sessionLost(who); return false; }
      return false;
    })().finally(() => { refreshing[who] = null; });
    return refreshing[who];
  }
  /** 令牌离到期不足 REFRESH_AHEAD_MS 就续期。返回是否仍持有令牌 */
  async function ensureFresh(who) {
    if (who !== 'admin' && who !== 'officer') return true;
    if (!tokens.get(who)) return false;
    const exp = tokens.expiresAt(who);
    if (exp && exp - now() < REFRESH_AHEAD_MS && exp > now()) await refresh(who);
    return !!tokens.get(who);
  }
  // 浏览器里每 30 秒查一次（页面没有请求时也续期，避免停留在页面上令牌过期）
  if (opts.autoRefresh ?? typeof window !== 'undefined') {
    const t = setInterval(() => { ensureFresh('admin'); ensureFresh('officer'); }, 30000);
    if (t && t.unref) t.unref();
  }

  /**
   * 调一次接口。who：'user' | 'admin' | null（不带令牌）。
   * 返回已适配的 JSON（金额转元、时间转本地格式）；204 返回 null。失败抛 ApiError。
   * raw:true 时不做金额时间转换。
   */
  async function call(method, path, { who = null, query, body, idem, raw = false } = {}) {
    if (who === 'admin' || who === 'officer') await ensureFresh(who);
    const url = new URL(base + path, typeof location !== 'undefined' ? location.href : 'http://127.0.0.1/');
    if (query) for (const [k, v] of Object.entries(query)) if (v != null && v !== '') url.searchParams.set(k, v);
    const headers = { Accept: 'application/json' };
    const token = who ? tokens.get(who) : null;
    if (token) headers.Authorization = `Bearer ${token}`;
    const init = { method, headers };
    if (body !== undefined) {
      if (typeof FormData !== 'undefined' && body instanceof FormData) init.body = body; // 上传：浏览器自带 multipart 边界
      else { headers['Content-Type'] = 'application/json'; init.body = JSON.stringify(body); }
    }
    if (method !== 'GET' && method !== 'HEAD') headers['Idempotency-Key'] = idem || newKey();
    let res;
    try { res = await doFetch(base ? url.toString() : url.pathname + url.search, init); }
    catch { throw new ApiError('NETWORK'); }
    if (res.status === 204) return null;
    let data = null;
    try { data = await res.json(); } catch { /* 非 JSON */ }
    if (!res.ok) {
      const d = data && typeof data === 'object' ? fromWire(data) : {};
      const { code, message, charged, next, ...rest } = d;
      // 401 且没有业务错误码、或错误码为 UNAUTHORIZED（内部鉴权码）一律当作未登录
      if (res.status === 401 && (!code || code === 'UNAUTHORIZED')) throw new ApiError(who === 'admin' || who === 'officer' ? 'ADMIN_NOT_LOGGED_IN' : 'NOT_LOGGED_IN');
      const extra = { status: res.status, ...rest };
      if (charged !== undefined) extra.charged = charged;
      if (next !== undefined) extra.next = next;
      throw new ApiError(code || (res.status >= 500 ? 'SERVER_BUSY' : 'INVALID'), message, extra);
    }
    return raw ? data : fromWire(data);
  }
  return { call, tokens, ensureFresh, refresh, get: (p, o) => call('GET', p, o), post: (p, o) => call('POST', p, o), put: (p, o) => call('PUT', p, o), patch: (p, o) => call('PATCH', p, o), del: (p, o) => call('DELETE', p, o) };
}

// mock 接口层统一入口。页面一律 `import { api } from '../shared/api.js'`，按域调用：
//   api.user.*         骑行用户（小程序）
//   api.enforcement.*  执法人员手机页
//   api.admin.*        管理端（主管部门、运营商、财务、客服、运维）
//   api.system.*       时间、降级信号、重置与调试（仅调试面板使用）
//   api.on(事件名, 回调)  跨端实时联动
// 地址带 ?backend=1 时，能对应上的方法改走真实后端（/api/<模块>/...），其余仍走 mock，见 real/README.md。
// 说明见同目录 API.md。
import { createCore, DATA_VERSION } from './mock/core.js';
import { createDomain } from './mock/domain.js';
import { createViews } from './mock/views.js';
import { createAgents } from './mock/agents.js';
import { createUserApi } from './mock/user.js';
import { createEnforcementApi } from './mock/enforcement.js';
import { createAdminApi } from './mock/admin.js';
import { createSystemApi } from './mock/system.js';
import { ApiError, CATALOG } from './mock/errors.js';
import { backendEnabled, createRealApi } from './real/index.js';

export { ApiError, CATALOG as ERROR_CODES, DATA_VERSION };

/**
 * 创建一个接口实例。env：
 *   loadJson(name) -> Promise<object>     读 mockdata/<name>.json
 *   kv: {getMany(keys), setMany(obj), clear()}  持久化（浏览器为 IndexedDB）
 *   local: {get(k), set(k,v), remove(k)}   小键值（浏览器为 localStorage，键名以 bike. 开头）
 *   now() -> 毫秒；random() -> [0,1)；latency: [最小, 最大] 毫秒；debug: 是否调试
 *   channel: BroadcastChannel|null；onStorage(cb)：订阅其他标签页的 localStorage 变化
 */
export function createApi(env) {
  const ctx = createCore(env);
  const D = createDomain(ctx);
  const V = createViews(ctx, D);
  const A = createAgents(ctx, D);
  const user = createUserApi(ctx, D, V, A);
  const admin = createAdminApi(ctx, D, V, A);
  const enforcement = createEnforcementApi(ctx, D, V);
  const system = createSystemApi(ctx, D, V, user);
  ctx.tickers.push(D.tick);
  return {
    ready: () => ctx.ready(),
    on: (name, cb) => ctx.on(name, cb),
    user, enforcement, admin, system,
    ApiError,
    /** 仅自测使用：直接读内存数据 */
    _ctx: ctx,
  };
}

/* ---------------- 浏览器环境 ---------------- */
function idbKv(name = 'bike.mock') {
  let dbp = null;
  const open = () => dbp || (dbp = new Promise((res, rej) => {
    const r = indexedDB.open(name, 1);
    r.onupgradeneeded = () => r.result.createObjectStore('kv');
    r.onsuccess = () => res(r.result);
    r.onerror = () => rej(r.error);
  }));
  const run = async (mode, fn) => {
    const db = await open();
    return new Promise((res, rej) => {
      const t = db.transaction('kv', mode);
      const out = fn(t.objectStore('kv'));
      t.oncomplete = () => res(out);
      t.onerror = () => rej(t.error);
      t.onabort = () => rej(t.error);
    });
  };
  return {
    getMany: keys => run('readonly', st => { const out = {}; keys.forEach(k => { const r = st.get(k); r.onsuccess = () => { out[k] = r.result; }; }); return out; }),
    setMany: obj => run('readwrite', st => { for (const [k, v] of Object.entries(obj)) st.put(v, k); return true; }),
    clear: () => run('readwrite', st => { st.clear(); return true; }),
  };
}
function memoryKv() {
  const m = new Map();
  return { getMany: async keys => Object.fromEntries(keys.map(k => [k, m.has(k) ? JSON.parse(JSON.stringify(m.get(k))) : undefined])), setMany: async obj => { for (const [k, v] of Object.entries(obj)) m.set(k, JSON.parse(JSON.stringify(v))); }, clear: async () => m.clear() };
}
function browserEnv() {
  const base = new URL('../mockdata/', import.meta.url);
  const debug = new URLSearchParams(location.search).get('debug') === '1';
  let kv;
  try { kv = typeof indexedDB !== 'undefined' ? idbKv() : memoryKv(); } catch { kv = memoryKv(); }
  return {
    loadJson: async n => { const r = await fetch(new URL(`${n}.json`, base)); if (!r.ok) throw new ApiError('NETWORK', '数据没有加载出来，请刷新重试'); return r.json(); },
    kv,
    local: { get: k => localStorage.getItem(k), set: (k, v) => localStorage.setItem(k, v), remove: k => localStorage.removeItem(k) },
    now: () => Date.now(),
    random: Math.random,
    latency: [150, 600],
    debug,
    channel: typeof BroadcastChannel !== 'undefined' ? new BroadcastChannel('bike.mock') : null,
    onStorage: cb => addEventListener('storage', e => cb(e.key, e.newValue)),
  };
}

/**
 * 浏览器里的默认实例（Node 中为 null，自测用 createApi）。
 * 地址带 ?backend=1（或 localStorage 键 bike.backend=1）时改走真实后端接口，见 real/README.md；默认仍是 mock。
 */
const isBrowser = typeof window !== 'undefined' && typeof location !== 'undefined';
export const api = isBrowser ? (backendEnabled() ? createRealApi(createApi(browserEnv())) : createApi(browserEnv())) : null;
export default api;

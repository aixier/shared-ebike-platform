// 真实接口模式：?backend=1 或 localStorage 键 bike.backend=1 开启（?backend=0 关闭并清除）。默认仍走 mock。
// 开启后 api.user / api.admin / api.enforcement 中能对应上后端接口的方法改走 fetch /api/<模块>/...，
// 其余方法保持 mock，并在控制台标注一次（console.info，前缀 [bike real]）。
import { createHttp } from './http.js';
import { createRealUser } from './user.js';
import { createRealAdmin } from './admin.js';
import { createRealEnforce } from './enforce.js';

/** 是否开启真实接口模式 */
export function backendEnabled() {
  try {
    const qs = new URLSearchParams(location.search).get('backend');
    if (qs === '1') { localStorage.setItem('bike.backend', '1'); return true; }
    if (qs === '0') { localStorage.removeItem('bike.backend'); return false; }
    return localStorage.getItem('bike.backend') === '1';
  } catch { return false; }
}

const isObj = v => v && typeof v === 'object' && !Array.isArray(v);

/** 以 overrides 覆盖 base：同名函数换成真实实现，同名命名空间递归合并，base 里没被覆盖的函数包一层"仍走 mock"提示 */
export function overlay(base, overrides, path, said = new Set()) {
  const out = {};
  for (const k of Object.keys(base)) {
    const p = `${path}.${k}`;
    const b = base[k];
    const o = overrides ? overrides[k] : undefined;
    if (typeof b === 'function') {
      if (typeof o === 'function') out[k] = o;
      else out[k] = (...a) => { if (!said.has(p)) { said.add(p); console.info(`[bike real] ${p} 没有对应的后端接口，仍走 mock`); } return b(...a); };
    } else if (isObj(b)) out[k] = overlay(b, isObj(o) ? o : undefined, p, said);
    else out[k] = b;
  }
  return out;
}

/**
 * 在 mock 接口实例上叠加真实接口。opts：base（接口前缀，Node 自测用）、fetch、store、mirror（默认 true：登录真实账号时静默在 mock 里也登录同名账号，
 * 让仍走 mock 的页面部分有登录态）。
 */
export function createRealApi(mock, opts = {}) {
  const http = createHttp(opts);
  const mirror = opts.mirror !== false;
  const quiet = fn => { try { Promise.resolve(fn()).catch(() => {}); } catch { /* 忽略 */ } };
  const userApi = createRealUser(http, mock.user, {
    onUserLogin: ({ channel, phone }) => mirror && quiet(() => mock.user.authorize({ channel, phone })),
  });
  const adminApi = createRealAdmin(http, {
    onAdminLogin: ({ username, password }) => mirror && quiet(() => mock.admin.login(username, password)),
  });
  const enforceApi = createRealEnforce(http);
  const said = new Set();
  const beijingNow = () => new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 19).replace('T', ' ');
  const system = overlay(mock.system, {
    time: async () => ({ now: beijingNow(), label: beijingNow(), shiftDays: 0, anchorMode: false, clockOffsetMs: 0 }),
  }, 'api.system', said);
  return {
    ...mock,
    user: overlay(mock.user, userApi, 'api.user', said),
    admin: overlay(mock.admin, adminApi, 'api.admin', said),
    enforcement: overlay(mock.enforcement, enforceApi, 'api.enforcement', said),
    system,
    backend: true,
    _http: http,
  };
}

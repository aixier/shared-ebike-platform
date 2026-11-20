// 执法端真实接口适配：api.enforcement.* -> /api/iam（client=执法端）、/api/enforce
// 执法端与管理端共用 iam 的令牌，但各存各的（bike.real.officerToken），互不顶掉。
import { ApiError } from '../mock/errors.js';

const O = { who: 'officer' };
const ignore = async (fn, dflt) => { try { return await fn(); } catch { return dflt; } };

async function dataUrlToBlob(u) { const r = await fetch(u); return r.blob(); }

export function createRealEnforce(http) {
  const { get, post, put, del, tokens } = http;
  const view = a => ({ accountId: a.accountId, name: a.name, username: a.username, org: a.org, badgeNo: a.badgeNo });

  async function me() {
    if (!tokens.get('officer')) throw new ApiError('ADMIN_NOT_LOGGED_IN', '请先登录执法账号');
    const r = await get('/api/iam/me', O);
    const mine = await ignore(() => get('/api/enforce/my/evidence', { ...O, query: { pageSize: 1 } }), null);
    return { ...view(r.account), evidenceCount: mine ? mine.total : undefined };
  }
  async function uploadPhotos(photos) {
    const ids = [];
    for (const p of photos) {
      if (!/[/.:]/.test(p)) { ids.push(p); continue; } // 没有路径或扩展名的当作已上传的照片编号
      if (!String(p).startsWith('data:image/jpeg') && !String(p).startsWith('data:image/png')) throw new ApiError('INVALID', '真实接口只收 JPEG/PNG 照片，请用拍照或相册里的图片');
      const fd = new FormData();
      fd.append('file', await dataUrlToBlob(p), 'photo.jpg');
      const r = await post('/api/enforce/photos', { ...O, body: fd });
      ids.push(r.photoId);
    }
    return ids;
  }
  return {
    async login(username, password) {
      const r = await post('/api/iam/login', { body: { username, password, client: '执法端' } });
      tokens.set('officer', r.token, r.expiresAt);
      return view(r.account);
    },
    async logout() { await ignore(() => post('/api/iam/logout', { ...O, body: {} })); tokens.clear('officer'); return { ok: true }; },
    me,
    scan: code => post('/api/enforce/scan', { ...O, body: { code } }),
    async capture(p = {}) { return post('/api/enforce/my/evidence', { ...O, body: { ...p, photos: await uploadPhotos(p.photos || []) } }); },
    mine: ({ page = 1, pageSize = 20 } = {}) => get('/api/enforce/my/evidence', { ...O, query: { page, pageSize } }),
    get: id => get(`/api/enforce/my/evidence/${id}`, O),
    update: id => put(`/api/enforce/my/evidence/${id}`, { ...O, body: {} }),
    remove: id => del(`/api/enforce/my/evidence/${id}`, O),
  };
}

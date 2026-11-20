// 管理端真实接口适配：api.admin.* -> /api/iam、/api/ride、/api/bill、/api/pay、/api/fleet、/api/site、/api/rule、/api/ops、/api/onboard 等
// 令牌由 iam 签发（管理端与执法端共用），存 localStorage 的 bike.real.adminToken。
import { ApiError } from '../mock/errors.js';
import { listQuery, moneyToWire } from './convert.js';

const A = { who: 'admin' };
const ignore = async (fn, dflt) => { try { return await fn(); } catch { return dflt; } };
const PRICE_KEYS = ['startFee', 'perMinute', 'dispatchFee', 'price', 'perOrderCap', 'amount', 'maxAmount', 'csRefundLimit'];

export function createRealAdmin(http, hooks = {}) {
  const { get, post, put, patch, tokens } = http;
  const q = p => ({ ...A, query: listQuery(p) });

  const TODO_PATHS = ['/api/bill/admin/todos', '/api/cs/admin/todos', '/api/onboard/todos', '/api/ops/todos', '/api/pay/admin/todos', '/api/settle/todos'];

  async function me() {
    if (!tokens.get('admin')) throw new ApiError('ADMIN_NOT_LOGGED_IN');
    const r = await get('/api/iam/me', A);
    const lists = await Promise.all(TODO_PATHS.map(p => ignore(() => get(p, A), [])));
    const todos = lists.flat().filter(t => t && t.count > 0);
    return { ...r, todos };
  }

  async function billsOf(ids) {
    if (!ids.length) return {};
    const res = await ignore(() => get('/api/bill/admin/orders', { ...A, query: { orderIds: ids.join(',') } }), { rows: [] });
    return Object.fromEntries((res.rows || []).map(b => [b.orderId, b]));
  }
  const rideText = o => (o.status === '开锁中' ? '开锁中' : o.status === '开锁失败' ? '开锁失败 · 未扣费' : o.status === '骑行中' ? '骑行中' : o.status === '待人工处理' ? '处理中' : '已结束');
  function adminOrder(r, b) {
    const o = { ...r, ticketIds: r.ticketIds || [], userName: r.userName ?? null, userPhoneMasked: r.userPhoneMasked ?? null };
    if (b) Object.assign(o, { fee: b.fee, bill: b.bill, payStatus: b.payStatus, payMethod: b.payMethod, arrearsPaidAt: b.arrearsPaidAt, refundStatus: b.refundStatus, refundFailedPaymentId: b.refundFailedPaymentId, billedMinutes: b.billedMinutes, billPushedAt: b.billPushedAt, statusText: b.statusText || rideText(r) });
    else Object.assign(o, { fee: null, bill: null, payStatus: null, payMethod: null, statusText: rideText(r) });
    return o;
  }

  const real = {
    async login(username, password) {
      const r = await post('/api/iam/login', { body: { username, password, client: '管理端' } });
      tokens.set('admin', r.token, r.expiresAt);
      if (hooks.onAdminLogin) hooks.onAdminLogin({ username, password });
      return r.account;
    },
    async logout() { await ignore(() => post('/api/iam/logout', { ...A, body: {} })); tokens.clear('admin'); return { ok: true }; },
    async changePassword(oldPassword, newPassword) { await post('/api/iam/password', { ...A, body: { oldPassword, newPassword } }); return { ok: true }; },
    me,

    orders: {
      async list(f = {}) {
        const { payStatus, ...rest } = f;
        if (payStatus) console.info('[bike real] orders.list 的 payStatus 筛选后端按订单号回查，本机适配暂未支持，已忽略');
        const res = await get('/api/ride/admin/orders', q(rest));
        const bills = await billsOf(res.rows.map(r => r.orderId));
        return { ...res, rows: res.rows.map(r => adminOrder(r, bills[r.orderId])) };
      },
      async get(id) {
        const [r, b, payments, tickets, settlement, audit] = await Promise.all([
          get(`/api/ride/admin/orders/${id}`, A),
          ignore(() => get(`/api/bill/admin/orders/${id}`, A), null),
          ignore(() => get(`/api/pay/admin/orders/${id}/payments`, A), []),
          ignore(() => get(`/api/cs/admin/orders/${id}/tickets`, A), []),
          ignore(() => get(`/api/settle/orders/${id}/settlement`, A), null),
          ignore(async () => (await get('/api/iam/audit', { ...A, query: { targetType: '订单', targetId: id, pageSize: 50 } })).rows, []),
        ]);
        const o = adminOrder(r, b);
        if (settlement) Object.assign(o, settlement);
        return { ...o, payments, tickets, audit };
      },
    },

    audit: { list: f => get('/api/iam/audit', q(f)), actions: () => get('/api/iam/audit/actions', A) },
    accounts: {
      list: f => get('/api/iam/accounts', q(f)),
      create: (spec, reason) => post('/api/iam/accounts', { ...A, body: { ...spec, reason } }),
      setStatus: (accountId, status, reason) => put(`/api/iam/accounts/${accountId}/status`, { ...A, body: { status, reason } }),
    },

    stations: {
      list: f => get('/api/site/admin/stations', q(f)),
      all: () => get('/api/site/admin/stations/all', A),
      get: id => get(`/api/site/admin/stations/${id}`, A),
      create: (spec, reason) => post('/api/site/admin/stations', { ...A, body: { ...spec, reason } }),
      update: (id, patchBody, reason) => patch(`/api/site/admin/stations/${id}`, { ...A, body: { ...patchBody, reason } }),
      remove: (id, reason) => post(`/api/site/admin/stations/${id}/remove`, { ...A, body: { reason } }),
    },
    piles: {
      list: f => get('/api/site/admin/piles', q(f)),
      create: (spec, reason) => post('/api/site/admin/piles', { ...A, body: { ...spec, reason } }),
      setStatus: (pileId, status, reason, faultNote) => post(`/api/site/admin/piles/${pileId}/status`, { ...A, body: { status, reason, ...(faultNote ? { faultNote } : {}) } }),
    },

    pricing: {
      list: () => get('/api/rule/pricing/versions', A),
      current: () => get('/api/rule/pricing/current', A),
      get: id => get(`/api/rule/pricing/versions/${id}`, A),
      create: (payload = {}, reason) => post('/api/rule/pricing/versions', { ...A, body: moneyToWire({ ...payload, reason }, PRICE_KEYS) }),
      revoke: (versionId, reason) => post(`/api/rule/pricing/versions/${versionId}/revoke`, { ...A, body: { reason } }),
      params: () => get('/api/rule/platform-params', A),
    },

    access: {
      overview: () => get('/api/fleet/access/overview', A),
      setExit: (operatorId, reason) => post(`/api/fleet/operators/${operatorId}/exit`, { ...A, body: { reason } }),
    },

    operator: {
      vehicles: {
        list: f => get('/api/fleet/operator/vehicles', q(f)),
        upload: rows => post('/api/fleet/operator/vehicles/upload', { ...A, body: { rows } }),
        update: (vehicleId, p) => patch(`/api/fleet/operator/vehicles/${vehicleId}`, { ...A, body: p }),
        remove: (vehicleId, reason) => post(`/api/fleet/operator/vehicles/${vehicleId}/remove`, { ...A, body: { reason } }),
      },
    },

    ops: {
      monitor: f => get('/api/ops/monitor', q(f)),
      degrade: {
        status: () => get('/api/ops/degrade/status', A),
        events: f => get('/api/ops/degrade/events', q(f)),
        event: id => get(`/api/ops/degrade/events/${id}`, A),
      },
      alerts: { list: f => get('/api/ops/alerts', q(f)) },
      pauses: f => get('/api/ops/pauses', q(f)),
      suspendSuggestions: f => get('/api/ops/suspend-suggestions', q(f)),
      closure: { list: f => get('/api/ops/closures', q(f)) },
      acceptance: { list: f => get('/api/onboard/acceptance', q(f)) },
    },
  };
  return real;
}

// 用户端真实接口适配：api.user.* -> /api/acct、/api/ride、/api/bill、/api/deposit、/api/pay、/api/site、/api/cs
// 页面看到的形态与 mock 一致（金额元、时间 'YYYY-MM-DD HH:MM:SS'），失败一律 ApiError。
import { ApiError } from '../mock/errors.js';

const U = { who: 'user' };
const sleep = ms => new Promise(r => setTimeout(r, ms));
// 后端各服务靠事件同步状态（ride 的准入快照约 1 秒刷新一次）：实名、免押、缴押金、付款之后稍等再返回，页面紧接着扫码开锁才不会读到旧状态
const SETTLE_MS = 1300;
const PAY_WAIT_MS = 20000, PAY_POLL_MS = 400;
const ignore = async (fn, dflt) => { try { return await fn(); } catch { return dflt; } };

export function createRealUser(http, mockUser, hooks = {}) {
  const { get, post } = http;
  const tokens = http.tokens;

  /* ---------- 视图拼装 ---------- */
  function rideStatusText(o) {
    if (o.status === '开锁中') return '开锁中';
    if (o.status === '开锁失败') return '开锁失败 · 未扣费';
    if (o.status === '骑行中') return '骑行中';
    if (o.status === '待人工处理') return '处理中';
    if (o.unlockResult === '超时后成功') return '超时未开锁 · 0 元';
    if (o.degraded) return '费用将稍后收取';
    return '已结束';
  }
  /** 行程（ride）＋账单（bill）-> UserOrder */
  function mergeOrder(r, b) {
    if (!r) return r;
    const o = { ...r, ticketIds: r.ticketIds || [] };
    if (b) {
      o.fee = b.fee; o.bill = b.bill; o.payStatus = b.payStatus; o.payMethod = b.payMethod;
      o.arrearsPaidAt = b.arrearsPaidAt; o.refundStatus = b.refundStatus; o.refundFailedPaymentId = b.refundFailedPaymentId;
      o.billedMinutes = b.billedMinutes; o.billPushedAt = b.billPushedAt; o.statusText = b.statusText || rideStatusText(r);
    } else {
      o.fee = null; o.bill = null; o.payStatus = null; o.payMethod = null; o.statusText = rideStatusText(r);
    }
    return o;
  }
  const billOf = id => ignore(() => get(`/api/bill/me/orders/${id}`, U), null);
  async function billsOf(ids) {
    if (!ids.length) return {};
    const res = await ignore(() => get('/api/bill/me/orders', { ...U, query: { orderIds: ids.join(',') } }), { rows: [] });
    return Object.fromEntries((res.rows || []).map(b => [b.orderId, b]));
  }

  /* ---------- me ---------- */
  async function me() {
    if (!tokens.get('user')) return null;
    let a;
    try { a = await get('/api/acct/me', U); }
    catch (e) { if (e.code === 'NOT_LOGGED_IN' || e.status === 401) { tokens.clear('user'); return null; } throw e; }
    const [sum, dep, cur] = await Promise.all([
      ignore(() => get('/api/bill/me/summary', U), {}),
      ignore(() => get('/api/deposit/me/state', U), { mode: '未开通' }),
      ignore(() => get('/api/ride/current', U), null),
    ]);
    return {
      ...a, deposit: dep, arrears: sum.arrears || 0, unpaidOrderIds: [...(sum.unpaidOrderIds || [])],
      monthCard: sum.monthCard || null, usableCouponCount: sum.usableCouponCount || 0,
      ridingOrderId: cur ? cur.orderId : null,
    };
  }

  /* ---------- 位置与电量：只用后端给的值，没有就留空（页面显示「—」或不画），不造假 ---------- */
  const pt = l => (l && typeof l === 'object' && !Array.isArray(l) && l.lng != null ? [l.lng, l.lat] : Array.isArray(l) ? l : null);
  function fillRide(r) {
    if (!r) return r;
    const unlockLocation = pt(r.unlockLocation);
    const startLocation = pt(r.startLocation) || unlockLocation;
    return { ...r, unlockLocation, startLocation, currentLocation: pt(r.currentLocation) || startLocation, battery: r.battery ?? null };
  }
  const fillScan = r => r;

  /** 订单为「开锁中」时等运营商开锁结果回来（开锁报告时限内），最多等 12 秒；返回最新订单 */
  async function settleUnlock(r) {
    for (let i = 0; r && r.status === '开锁中' && i < 24; i++) {
      await sleep(500);
      const n = await ignore(() => get(`/api/ride/orders/${r.orderId}`, U), null);
      if (n) r = { ...r, ...n };
    }
    return r;
  }

  /** 轮询账单到 done(bill) 为真；超时抛 PAY_PENDING。payParams 的 mode=real 时原型没有收银台，直接说明 */
  async function pollBill(orderId, done, payParams, waitMs = PAY_WAIT_MS) {
    if (payParams && payParams.mode === 'real') throw new ApiError('PAY_PENDING', '需要调起微信或支付宝收银台，原型没有收银台；支付完成后再刷新订单', { charged: false, payParams });
    const t0 = Date.now();
    for (;;) {
      const b = await billOf(orderId);
      if (b && done(b)) return b;
      if (Date.now() - t0 > waitMs) throw new ApiError('PAY_PENDING', '支付结果还在确认中，请稍后在订单里查看，不要重复支付', { charged: false });
      await sleep(PAY_POLL_MS);
    }
  }

  /* ---------- 骑行 ---------- */
  const zeroLive = r => ({ ...r, liveBilledMinutes: 0, liveRideFee: 0, liveMonthCardDeduct: 0, liveEstimate: 0 });
  async function withLive(r) {
    if (!r) return r;
    if (!r.startAt || !r.pricingVersionId) return zeroLive(r);
    const est = await ignore(() => get(`/api/bill/me/orders/${r.orderId}/estimate`, { ...U, query: { startAt: toWire(r.startAt), pricingVersionId: r.pricingVersionId } }), null);
    return est ? { ...r, liveBilledMinutes: est.liveBilledMinutes, liveRideFee: est.liveRideFee, liveMonthCardDeduct: est.liveMonthCardDeduct, liveEstimate: est.liveEstimate } : zeroLive(r);
  }
  const toWire = s => (/^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d$/.test(s) ? s.replace(' ', 'T') + '+08:00' : s);
  const loc = l => (l ? { lng: l[0], lat: l[1] } : {});

  async function collectOf(orderId) {
    for (let i = 0; i < 4; i++) {
      const b = await billOf(orderId);
      if (b) {
        if (b.payStatus === '已支付' && b.payMethod === '免押自动扣款') return ['已收款', b];
        if (b.payStatus === '待支付') return ['已推送一键支付', b];
        if (b.payStatus === '欠费') return ['已转欠费', b];
        return ['无需支付', b];
      }
      await sleep(350);
    }
    return ['无需支付', null];
  }

  /* ---------- 方法表 ---------- */
  const real = {
    me,
    async authorize({ channel, phone } = {}) {
      const r = await post('/api/acct/authorize', { body: { channel, phone } });
      tokens.set('user', r.token);
      if (hooks.onUserLogin) hooks.onUserLogin({ channel, phone });
      return { user: await me(), isNew: !!r.isNew };
    },
    async logout() { await ignore(() => post('/api/acct/logout', { ...U, body: {} })); tokens.clear('user'); return { ok: true }; },
    async verifyRealName({ name, idNo } = {}) {
      const r = await post('/api/acct/real-name', { ...U, body: { name, idNo } });
      await sleep(SETTLE_MS);
      return { ...r, user: await me() };
    },
    async openCreditFree({ provider } = {}) {
      const r = await post('/api/deposit/me/credit-free', { ...U, body: provider ? { provider } : {} });
      await sleep(SETTLE_MS);
      return { user: await me(), score: r.score, threshold: r.threshold };
    },
    async payDeposit() { const r = await post('/api/deposit/me/payment', { ...U, body: {} }); await sleep(SETTLE_MS); return { user: await me(), paymentId: r.paymentId, amount: r.amount }; },
    async topupDeposit() { const r = await post('/api/deposit/me/topup', { ...U, body: {} }); await sleep(SETTLE_MS); return { user: await me(), paymentId: r.paymentId, amount: r.amount }; },

    stations: () => get('/api/site/map'),
    async scan(code) { return fillScan(await post('/api/ride/scan', { ...U, body: { code } })); },
    nearby: ({ vehicleId, location, radiusM } = {}) => get('/api/ride/nearby', { ...U, query: { vehicleId, ...loc(location), radiusM } }),
    async unlock(code) {
      let r = await post('/api/ride/unlock', { ...U, body: { code } });
      if (r.status === '开锁中') {
        r = await settleUnlock(r);
        // 正常情况下接口保持到开锁结果回来才返回；这里只兜底「返回时仍是开锁中」
        const CODE = { 车辆无响应: 'UNLOCK_NO_RESPONSE', 电量不足: 'UNLOCK_LOW_BATTERY', 车辆故障: 'UNLOCK_FAULT', 车辆被占用: 'UNLOCK_OCCUPIED' };
        const next = { label: '查看附近可用车', action: 'viewNearby' };
        if (r.status === '开锁失败') throw new ApiError(CODE[r.unlockFailReason] || 'UNLOCK_NO_RESPONSE', `开锁失败：${r.unlockFailReason || '车辆无响应'}，没有扣费`, { charged: false, orderId: r.orderId, next });
        if (r.status === '开锁中') throw new ApiError('UNLOCK_NO_RESPONSE', '车辆没有响应，未扣费', { charged: false, orderId: r.orderId, next });
        r = { ...(await ignore(() => get('/api/ride/current', U), null) || r) };
      }
      return fillRide(await withLive(r));
    },
    async ride() {
      let r = await get('/api/ride/current', U);
      if (r && r.status === '开锁中') { r = await settleUnlock(r); if (r.status !== '骑行中') return null; r = await ignore(() => get('/api/ride/current', U), r) || r; }
      return r ? fillRide(await withLive(r)) : null;
    },
    returnPreview: ({ stationId, location } = {}) => get('/api/ride/return-preview', { ...U, query: { stationId, ...loc(location) } }),
    async returnBike({ method, stationId, pileId, location, confirmDispatch } = {}) {
      if (method && method !== 'app') console.info(`[bike real] returnBike(method=${method})：后端的入桩/锁车由桩在位事件与运营商锁车报告触发，页面这里按"页面还车"提交`);
      const body = {};
      if (stationId) body.stationId = stationId;
      if (pileId) body.pileId = pileId;
      if (location) body.location = location;
      if (confirmDispatch) body.confirmDispatch = true;
      const r = await post('/api/ride/return', { ...U, body });
      if (!r.ended) return { order: mergeOrder(r.order, null), deferred: !!r.deferred, collect: '无需支付', message: r.message };
      const [collect, b] = await collectOf(r.order.orderId);
      return { order: mergeOrder(r.order, b), deferred: !!r.deferred, collect, message: r.message };
    },

    async orders({ page = 1, pageSize = 20, status } = {}) {
      let res;
      if (status === '待付款') {
        const s = await ignore(() => get('/api/bill/me/summary', U), {});
        const ids = [...(s.arrearsOrderIds || []), ...(s.unpaidOrderIds || [])];
        res = ids.length ? await get('/api/ride/orders', { ...U, query: { orderIds: ids.join(','), page, pageSize } }) : { rows: [], total: 0, page, pageSize };
      } else {
        res = await get('/api/ride/orders', { ...U, query: { page, pageSize, status } });
      }
      const bills = await billsOf(res.rows.map(r => r.orderId));
      return { ...res, rows: res.rows.map(r => mergeOrder(r, bills[r.orderId])) };
    },
    async order(orderId) {
      const [r, b, payments, tickets] = await Promise.all([
        get(`/api/ride/orders/${orderId}`, U), billOf(orderId),
        ignore(() => get('/api/pay/me/payments', { ...U, query: { orderId } }), []),
        ignore(() => get(`/api/cs/orders/${orderId}/tickets`, U), []),
      ]);
      return { ...mergeOrder(r, b), payments, tickets };
    },
    // 一键支付两步：pay-intent 返回调起参数 payParams，结果以支付事件为准；这里回查账单 payStatus 到已支付（模拟渠道由 pay 延迟自动回调）。
    // 真实渠道需要前端按 payParams 调起收银台，原型没有收银台，轮询超时按「结果确认中」报错。
    async pay(orderId) {
      const r = await post(`/api/bill/me/orders/${orderId}/pay-intent`, { ...U, body: {} });
      let bill = r.bill;
      if (!r.paid) bill = await pollBill(orderId, b => b.payStatus === '已支付' || b.payStatus === '欠费已补付', r.payParams);
      const ride = await get(`/api/ride/orders/${orderId}`, U);
      return { order: mergeOrder(ride, bill), paymentId: r.paymentId, amount: r.amount };
    },
    async payArrears() {
      const r = await post('/api/bill/me/arrears/pay-intent', { ...U, body: {} });
      if (r.intents && r.intents.length) {
        const t0 = Date.now();
        for (const it of r.intents) await pollBill(it.orderId, b => b.payStatus === '欠费已补付' || b.payStatus === '已支付', it.payParams, Math.max(3000, PAY_WAIT_MS - (Date.now() - t0)));
      }
      await sleep(SETTLE_MS);
      return { ...r, user: await me() };
    },

    deposit: {
      get: () => get('/api/deposit/me', U),
      refund: () => post('/api/deposit/me/refund', { ...U, body: {} }),
    },
    submitPayee: ({ paymentId, name, bank, account } = {}) => post(`/api/pay/me/payments/${paymentId}/payee`, { ...U, body: { name, bank, account } }),

    monthCard: {
      get: () => get('/api/bill/me/month-cards', U),
      async buy() {
        const c = await post('/api/bill/me/month-cards', { ...U, body: {} });
        if (c.payState !== '待支付') return c;
        const t0 = Date.now(); // 款到后才生效：轮询我的月卡到已支付
        while (Date.now() - t0 < PAY_WAIT_MS) {
          await sleep(PAY_POLL_MS);
          const m = await ignore(() => get('/api/bill/me/month-cards', U), null);
          const k = m && (m.cards || []).find(x => x.cardId === c.cardId);
          if (k && k.payState !== '待支付') return k;
        }
        throw new ApiError('PAY_PENDING', '月卡购买款还在确认中，请稍后在「我的月卡」查看，不要重复购买', { charged: false });
      },
    },
    coupons: {
      list: ({ status } = {}) => get('/api/bill/me/coupons', { ...U, query: { status } }),
      templates: () => get('/api/bill/coupon-templates', U),
      claim: templateId => post('/api/bill/me/coupons', { ...U, body: { templateId } }),
    },

    account: {
      get: () => get('/api/acct/account', U),
      cancelCheck: () => get('/api/acct/account/cancel-check', U),
      async cancel({ reason } = {}) { const r = await post('/api/acct/account/cancel', { ...U, body: reason ? { reason } : {} }); tokens.clear('user'); return r; },
    },
    notices: ({ unreadOnly } = {}) => get('/api/acct/notices', { ...U, query: { unreadOnly: unreadOnly ? 'true' : undefined } }),
    markNoticesRead: ids => post('/api/acct/notices/read', { ...U, body: ids ? { ids } : {} }),

    cs: {
      tickets: () => get('/api/cs/tickets', U),
      ticket: id => get(`/api/cs/tickets/${id}`, U),
      open: ({ orderId, entry, text, category } = {}) => post('/api/cs/tickets', { ...U, body: { orderId, entry, text, category } }),
      send: (ticketId, text) => post(`/api/cs/tickets/${ticketId}/messages`, { ...U, body: { text } }),
    },
  };
  return real;
}

export { ApiError };

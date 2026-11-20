// mock 接口层内核：载入与时间平移、持久化、跨端同步、延迟与失败注入、留痕、编号、事件。
// 业务判断不在这里；这里只做"确定性代码合法范围"内的事：事实采集、版本不可变、审计留痕、状态持久与适配。
import { ApiError } from './errors.js';
import { fmt, ms, parse, shiftDeep, clone, p2, Y, C } from './util.js';
import { refreshVersionChain, correctionsMap } from './rules.js';

export const DATA_VERSION = 'mock-20261201-v2';
export const ANCHOR = '2026-12-01 12:00:00';
export const FILES = ['operators', 'vehicles', 'stations', 'users', 'orders', 'payments', 'pricing', 'settlements', 'tickets', 'evidence', 'integration', 'ops', 'audit', 'accounts'];
export const COLLECTIONS = [...FILES, 'notices', 'runtime'];
const K = { meta: 'bike.meta', rev: 'bike.rev', debug: 'bike.debug', sUser: 'bike.session.user', sAdmin: 'bike.session.admin', sOfficer: 'bike.session.officer' };

/* 集合变化时自动发出的事件名 */
const COL_EVENTS = { orders: 'orders.changed', users: 'users.changed', pricing: 'pricing.changed', ops: 'ops.changed', payments: 'payments.changed', settlements: 'settlements.changed', tickets: 'tickets.changed', vehicles: 'vehicles.changed', stations: 'stations.changed', operators: 'operators.changed', evidence: 'evidence.changed', integration: 'integration.changed', audit: 'audit.changed', accounts: 'accounts.changed', notices: 'notices.changed' };

export function createCore(env) {
  const tabId = Math.random().toString(36).slice(2);
  const listeners = new Map();
  const ctx = {
    env, db: null, meta: null, revs: {}, dirtySet: new Set(), events: [], loading: null,
    get debugOn() { return !!env.debug; },
  };

  /* ---------------- 时间 ---------------- */
  ctx.nowMs = () => env.now() + ((ctx.meta && ctx.meta.clockOffsetMs) || 0);
  ctx.now = () => fmt(ctx.nowMs());
  ctx.shiftDays = () => (ctx.meta ? ctx.meta.shiftDays : 0);
  /** 标签时间 = 真实时间 − 平移天数。编号里的日期与结算月份用标签时间（SCHEMA 第 3 节） */
  ctx.label = t => { const d = parse(t || ctx.now()); d.setDate(d.getDate() - ctx.shiftDays()); return fmt(d); };
  ctx.labelMonth = t => ctx.label(t).slice(0, 7);
  ctx.labelYMD = t => ctx.label(t).slice(0, 10).replace(/-/g, '');
  /** 标签时间 -> 真实时间 */
  ctx.realOf = labelT => { const d = parse(labelT); d.setDate(d.getDate() + ctx.shiftDays()); return fmt(d); };
  ctx.rnd = () => env.random();
  ctx.randInt = (a, b) => a + Math.floor(env.random() * (b - a + 1));

  /* ---------------- 载入、平移、持久 ---------------- */
  function computeShift(anchorMode) {
    if (anchorMode) return { shiftDays: 0, clockOffsetMs: ms(ANCHOR) - env.now() };
    // 平移整天数，使锚点"现在"不晚于真实现在：避免出现未来时刻
    return { shiftDays: Math.floor((env.now() - ms(ANCHOR)) / 864e5), clockOffsetMs: 0 };
  }
  async function seed(anchorMode = false) {
    const raw = {};
    await Promise.all(FILES.map(async f => { raw[f] = await env.loadJson(f); }));
    const { shiftDays, clockOffsetMs } = computeShift(anchorMode);
    for (const f of FILES) shiftDeep(raw[f], shiftDays);
    raw.notices = [];
    raw.runtime = { seededAt: fmt(env.now() + clockOffsetMs) };
    ctx.meta = { dataVersion: DATA_VERSION, shiftDays, clockOffsetMs, anchorMode: !!anchorMode, seededAt: raw.runtime.seededAt };
    ctx.db = raw;
    normalize();
    await env.kv.clear();
    await env.kv.setMany(Object.fromEntries(COLLECTIONS.map(c => [c, ctx.db[c]])));
    env.local.set(K.meta, JSON.stringify(ctx.meta));
    ctx.revs = Object.fromEntries(COLLECTIONS.map(c => [c, 1]));
    env.local.set(K.rev, JSON.stringify({ ...ctx.revs, _epoch: ctx.meta.seededAt + tabId }));
  }
  /** 补齐运行时需要、原始数据里没有的字段（不改原有值） */
  function normalize() {
    const db = ctx.db, now = ctx.now();
    refreshVersionChain(db.pricing.versions, now);
    const corr = correctionsMap(db.settlements.settlements, db.settlements.disputes);
    for (const d of db.settlements.disputes) if (d.status === '已采纳' && !d.corrections) {
      d.corrections = Object.entries(corr).flatMap(([orderId, arr]) => arr.filter(x => x.disputeId === d.disputeId).map(x => ({ orderId, baseDelta: x.baseDelta })));
    }
    db.ops.current = { eventId: null, tightened: false, stopped: false, components: [], cause: null, ...db.ops.current };
    for (const r of db.payments.reconciliation) r.frozen = true;
  }
  async function loadFromKv() {
    const got = await env.kv.getMany(COLLECTIONS);
    if (COLLECTIONS.some(c => got[c] == null)) return false;
    ctx.db = got;
    return true;
  }
  ctx.ready = () => {
    if (ctx.loading) return ctx.loading;
    ctx.loading = (async () => {
      let meta = null;
      try { meta = JSON.parse(env.local.get(K.meta) || 'null'); } catch { meta = null; }
      if (meta && meta.dataVersion === DATA_VERSION) {
        ctx.meta = meta;
        if (await loadFromKv()) { ctx.revs = readRevs(); return; }
      }
      await seed(false);
    })();
    ctx.loading.catch(() => { ctx.loading = null; });
    return ctx.loading;
  };
  function readRevs() { try { return JSON.parse(env.local.get(K.rev) || '{}'); } catch { return {}; } }

  /** 另一端写过的集合重新读入（每次调用前） */
  async function syncFromOthers() {
    try { const m = JSON.parse(env.local.get(K.meta) || 'null'); if (m && m.dataVersion === DATA_VERSION) ctx.meta = m; } catch { /* 保持当前 */ }
    const r = readRevs();
    if (r._epoch && ctx.revs._epoch && r._epoch !== ctx.revs._epoch) { // 被重置过
      ctx.meta = JSON.parse(env.local.get(K.meta) || 'null') || ctx.meta;
      await loadFromKv(); ctx.revs = r; return COLLECTIONS;
    }
    const stale = COLLECTIONS.filter(c => (r[c] || 0) > (ctx.revs[c] || 0));
    if (stale.length) {
      const got = await env.kv.getMany(stale);
      for (const c of stale) if (got[c] != null) ctx.db[c] = got[c];
      for (const c of stale) ctx.revs[c] = r[c];
    }
    return stale;
  }
  ctx.dirty = (...cols) => cols.forEach(c => ctx.dirtySet.add(c));
  ctx.emit = (name, payload = {}) => ctx.events.push({ name, payload });
  async function flush() {
    const cols = [...ctx.dirtySet];
    const events = ctx.events.splice(0);
    ctx.dirtySet.clear();
    if (cols.length) {
      await env.kv.setMany(Object.fromEntries(cols.map(c => [c, ctx.db[c]])));
      const r = readRevs();
      for (const c of cols) { r[c] = (r[c] || 0) + 1; ctx.revs[c] = r[c]; }
      if (!r._epoch) r._epoch = ctx.revs._epoch;
      ctx.revs._epoch = r._epoch;
      env.local.set(K.rev, JSON.stringify(r));
    }
    const all = [...events, ...cols.filter(c => COL_EVENTS[c]).map(c => ({ name: COL_EVENTS[c], payload: {} }))];
    if (all.length && env.channel) { try { env.channel.postMessage({ from: tabId, cols, events: all }); } catch { /* 关闭中 */ } }
    all.forEach(e => fire(e.name, e.payload, false));
  }
  function fire(name, payload, remote) {
    for (const k of [name, '*']) (listeners.get(k) || []).forEach(cb => { try { cb({ name, ...payload, remote }); } catch (e) { console.error(e); } });
  }
  ctx.on = (name, cb) => {
    if (!listeners.has(name)) listeners.set(name, new Set());
    listeners.get(name).add(cb);
    return () => listeners.get(name).delete(cb);
  };
  /* 跨端：BroadcastChannel 为主，storage 事件兜底（例如 BroadcastChannel 不可用时） */
  let lastRemote = 0;
  async function onRemote(msg) {
    if (!ctx.db) return;
    lastRemote = Date.now();
    if (msg.reset) { ctx.meta = JSON.parse(env.local.get(K.meta) || 'null') || ctx.meta; await loadFromKv(); ctx.revs = readRevs(); fire('data.reset', {}, true); return; }
    await syncFromOthers();
    (msg.events || []).forEach(e => fire(e.name, e.payload, true));
  }
  if (env.channel) env.channel.onmessage = e => { if (e.data && e.data.from !== tabId) onRemote(e.data); };
  if (env.onStorage) env.onStorage(async (key) => {
    if (key === K.rev && !env.channel && ctx.db) {
      const stale = await syncFromOthers();
      if (Date.now() - lastRemote > 200) stale.forEach(c => COL_EVENTS[c] && fire(COL_EVENTS[c], {}, true));
    }
    if (key && key.startsWith('bike.session.')) fire('session.changed', { key }, true);
  });

  /* ---------------- 调用包装：延迟、失败注入、到点任务、持久 ---------------- */
  const sleep = t => new Promise(r => setTimeout(r, t));
  ctx.sleep = t => (env.latency && env.latency[1] === 0 ? Promise.resolve() : sleep(t));
  ctx.tickers = [];
  ctx.call = async (name, fn, opts = {}) => {
    await ctx.ready();
    await syncFromOthers();
    const [a, b] = opts.latency || env.latency || [150, 600];
    const dbg = ctx.debugState();
    const scale = dbg.latencyScale || 1;
    if (b > 0) await sleep((a + env.random() * (b - a)) * scale);
    const inj = ctx.takeInjection(name);
    if (inj) throw new ApiError(inj.code || 'NETWORK', inj.message);
    if (ctx.debugOn && dbg.randomFailRate && !opts.noRandomFail && env.random() < dbg.randomFailRate) throw new ApiError('NETWORK');
    try {
      ctx._actor = null;
      for (const t of ctx.tickers) t();
      const r = await fn();
      return clone(r);
    } catch (e) {
      if (e instanceof ApiError) throw e;
      console.error(`[mock] ${name}`, e); // 程序缺陷：控制台留底，页面只看到一句人话
      throw new ApiError('SERVER_BUSY');
    } finally {
      await flush();
    }
  };

  /* ---------------- 调试状态（仅 ?debug=1） ---------------- */
  ctx.debugState = () => { if (!ctx.debugOn) return {}; try { return JSON.parse(env.local.get(K.debug) || '{}'); } catch { return {}; } };
  ctx.setDebugState = s => env.local.set(K.debug, JSON.stringify(s));
  /** 取出一次注入：key 可为接口名（如 'user.pay'）或业务注入点（如 'payment.decline'） */
  ctx.takeInjection = key => {
    if (!ctx.debugOn) return null;
    const s = ctx.debugState();
    const f = (s.failNext || {})[key];
    if (!f) return null;
    f.times = (f.times || 1) - 1;
    if (f.times <= 0) delete s.failNext[key];
    ctx.setDebugState(s);
    return f;
  };
  ctx.peekInjection = key => (ctx.debugOn ? ((ctx.debugState().failNext || {})[key] || null) : null);

  /* ---------------- 会话 ---------------- */
  ctx.session = {
    get: kind => { try { return JSON.parse(env.local.get(K['s' + kind]) || 'null'); } catch { return null; } },
    set: (kind, v) => { v == null ? env.local.remove(K['s' + kind]) : env.local.set(K['s' + kind], JSON.stringify(v)); ctx.emit('session.changed', { kind }); },
  };

  /* ---------------- 重置 ---------------- */
  ctx.reset = async ({ anchorMode } = {}) => {
    const keepAnchor = anchorMode == null ? !!(ctx.meta && ctx.meta.anchorMode) : anchorMode;
    await seed(keepAnchor);
    for (const k of ['User', 'Admin', 'Officer']) env.local.remove(K['s' + k]);
    if (env.channel) env.channel.postMessage({ from: tabId, reset: true });
    fire('data.reset', {}, false);
  };

  /* ---------------- 查找与编号 ---------------- */
  const IDX = new WeakMap();
  ctx.find = (arr, key, val) => {
    let e = IDX.get(arr);
    if (!e || e.len !== arr.length || e.key !== key) { e = { len: arr.length, key, map: new Map(arr.map(x => [x[key], x])) }; IDX.set(arr, e); }
    return e.map.get(val) || null;
  };
  ctx.get = (arr, key, val, what = '记录') => { const x = ctx.find(arr, key, val); if (!x) throw new ApiError('NOT_FOUND', `没有找到该${what}`); return x; };
  function nextSeq(list, prefix, width, start, step = [1, 1]) {
    let max = start;
    for (const id of list) if (id && id.startsWith(prefix)) { const n = parseInt(id.slice(prefix.length), 10); if (n > max) max = n; }
    return prefix + String(max + ctx.randInt(step[0], step[1])).padStart(width, '0');
  }
  /** 取某集合的编号列（按字段名，不猜：桩、车辆记录里也带 stationId） */
  const ids = (arr, key) => arr.map(x => x && x[key]);
  ctx.ids = {
    order: t => nextSeq(ids(ctx.db.orders, 'orderId'), 'OD' + ctx.labelYMD(t), 5, 10000, [3, 29]),
    payment: t => { let id; do { id = 'PY' + ctx.label(t).replace(/\D/g, '') + String(ctx.randInt(100, 999)); } while (ctx.find(ctx.db.payments.payments, 'paymentId', id)); return id; },
    ticket: t => nextSeq(ids(ctx.db.tickets, 'ticketId'), 'TK' + ctx.labelYMD(t), 4, 30),
    audit: t => nextSeq(ids(ctx.db.audit, 'auditId'), 'AU' + ctx.labelYMD(t), 4, 0),
    notice: t => nextSeq(ids(ctx.db.notices, 'noticeId'), 'NT' + ctx.labelYMD(t), 4, 0),
    evidence: t => nextSeq(ids(ctx.db.evidence.evidence, 'evidenceId'), 'EV' + ctx.labelYMD(t), 3, 0),
    exportId: t => nextSeq(ids(ctx.db.evidence.exports, 'exportId'), 'EV-EXP-' + ctx.labelYMD(t) + '-', 2, 0),
    card: t => nextSeq(ids(ctx.db.users.monthCards, 'cardId'), 'MC' + ctx.labelYMD(t), 4, 1000),
    coupon: () => { let id; do { id = 'CP' + String(ctx.randInt(30100000, 39999999)); } while (ctx.find(ctx.db.users.coupons, 'couponId', id)); return id; },
    user: () => nextSeq(ids(ctx.db.users.users, 'userId'), 'U', 6, 100000, [3, 17]),
    version: () => nextSeq(ids(ctx.db.pricing.versions, 'versionId'), 'PR', 4, 0),
    dispute: t => nextSeq(ids(ctx.db.settlements.disputes, 'disputeId'), 'DS' + ctx.labelYMD(t).slice(0, 6), 3, 0),
    degrade: t => nextSeq(ids(ctx.db.ops.degradeEvents, 'eventId'), 'OPS-DG-' + ctx.labelYMD(t) + '-', 2, 0),
    alert: t => nextSeq(ids(ctx.db.ops.alerts, 'alertId'), 'OPS-AL-' + ctx.labelYMD(t) + '-', 2, 0),
    pause: t => nextSeq(ids(ctx.db.ops.pauses, 'id'), 'OPS-PS-' + ctx.labelYMD(t) + '-', 2, 0),
    suggestion: t => nextSeq(ids(ctx.db.ops.suspendSuggestions || [], 'id'), 'OPS-SG-' + ctx.labelYMD(t) + '-', 2, 0),
    station: () => nextSeq(ids(ctx.db.stations.stations, 'stationId'), 'ST', 4, 0),
    pile: () => nextSeq(ids(ctx.db.stations.piles, 'pileId'), 'PL', 5, 0),
    vehicle: () => nextSeq(ids(ctx.db.vehicles, 'vehicleId'), 'VH', 6, 0),
    account: prefix => nextSeq(ids(ctx.db.accounts.accounts, 'accountId'), 'AC' + prefix, 2, 0),
    govCode: () => { let c; do { c = 'SF' + String(ctx.randInt(10000000, 99999999)); } while (ctx.find(ctx.db.vehicles, 'govCode', c)); return c; },
    tradeNo: (channel, t, refund = false) => {
      const d = ctx.label(t).slice(0, 10).replace(/-/g, '');
      const digits = n => Array.from({ length: n }, () => ctx.randInt(0, 9)).join('');
      if (channel === '微信支付') return (refund ? '5030' : '4200') + digits(6) + d + digits(10);
      if (channel === '支付宝') return d + '22001' + digits(15);
      if (channel === '银行转账') return 'HX' + d + digits(10);
      return 'ZH' + d + digits(8);
    },
  };

  /* ---------------- 留痕（R-47） ---------------- */
  ctx.actor = () => ctx._actor || 'AC0900';
  ctx.audit = ({ action, targetType, targetId, summary, reason = null, before = null, after = null, accountId, at }) => {
    const t = at || ctx.now();
    const rec = { auditId: ctx.ids.audit(t), at: t, accountId: accountId || ctx.actor(), action, targetType, targetId, summary, reason, before, after };
    ctx.db.audit.push(rec);
    ctx.dirty('audit');
    ctx.emit('audit.created', { auditId: rec.auditId, action });
    return rec;
  };

  /* ---------------- 便捷 ---------------- */
  ctx.fail = (code, message, extra) => { throw new ApiError(code, message, extra); };
  ctx.reason = r => { if (!r || String(r).trim().length < 4) throw new ApiError('REASON_REQUIRED'); return String(r).trim(); };
  ctx.Y = Y; ctx.C = C; ctx.p2 = p2;
  return ctx;
}

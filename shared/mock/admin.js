// 管理端接口：登录与岗位、主管部门（价格规则、站点与桩、接入状态、证据）、操作留痕、账号管理。
// 运营商、财务、客服、运维的接口分别在 admin-operator.js / admin-finance.js / admin-cs.js / admin-ops.js。
import { ApiError } from './errors.js';
import { C, Y, ms, fmt, inPolygon, polygonArea, centroid, paginate, csv, sha256hex, clone } from './util.js';
import * as R from './rules.js';
import { createOperatorApi } from './admin-operator.js';
import { createFinanceApi } from './admin-finance.js';
import { createCsApi } from './admin-cs.js';
import { createOpsApi } from './admin-ops.js';

const PUBLIC_ACCOUNT = a => { const x = clone(a); delete x.password; return x; };

export function createGuard(ctx, D) {
  const g = {};
  g.account = () => {
    const s = ctx.session.get('Admin');
    const a = s && D.account(s.accountId);
    if (!a) throw new ApiError('ADMIN_NOT_LOGGED_IN');
    if (a.status === '停用') throw new ApiError('ACCOUNT_DISABLED');
    return a;
  };
  /** perms：权限点（字符串或数组，满足其一）；null 表示登录即可。opts.write 为写操作（只读账号拒绝） */
  g.call = (name, perms, fn, opts = {}) => ctx.call('admin.' + name, () => {
    const a = g.account();
    if (perms) { const list = [].concat(perms); if (!list.some(p => a.permissions.includes(p))) throw new ApiError('FORBIDDEN', `你的账号（${a.role}）没有该操作的权限`); }
    if (opts.write && /^只读/.test(a.status)) throw new ApiError('READONLY');
    ctx._actor = a.accountId;
    return fn(a);
  }, opts);
  g.can = (a, p) => a.permissions.includes(p);
  /** 运营商账号只能访问本运营商；平台账号返回传入的 operatorId（可为空=全部） */
  g.scope = (a, operatorId) => {
    if (!a.operatorScope) return operatorId || null;
    if (operatorId && operatorId !== a.operatorScope) throw new ApiError('SCOPE_DENIED', `只能查看和操作${D.operator(a.operatorScope).shortName}的数据`);
    return a.operatorScope;
  };
  g.inRange = (t, from, to) => (!from || (t && t.slice(0, from.length) >= from)) && (!to || (t && t.slice(0, to.length) <= to));
  return g;
}

export function createAdminApi(ctx, D, V, A) {
  const db = () => ctx.db;
  const g = createGuard(ctx, D);
  const api = {};

  /* ================= 登录与岗位 ================= */
  api.login = (username, password) => ctx.call('admin.login', () => {
    const a = db().accounts.accounts.find(x => x.username && x.username === String(username || '').trim());
    if (!a || a.password !== password || a.role === '执法人员') throw new ApiError('LOGIN_FAILED');
    if (a.status === '停用') throw new ApiError('ACCOUNT_DISABLED');
    a.lastLoginAt = ctx.now(); ctx.dirty('accounts');
    ctx.session.set('Admin', { accountId: a.accountId });
    return PUBLIC_ACCOUNT(a);
  });
  api.logout = () => ctx.call('admin.logout', () => { ctx.session.set('Admin', null); return { ok: true }; });
  /** 修改本人密码：旧密码须正确；新密码至少 8 位、同时含字母和数字、不能与旧密码相同 */
  api.changePassword = (oldPassword, newPassword) => g.call('changePassword', null, a => {
    if (a.password !== oldPassword) throw new ApiError('LOGIN_FAILED', '当前密码不正确');
    const p = String(newPassword || '');
    if (p.length < 8 || !/[a-z]/i.test(p) || !/\d/.test(p)) throw new ApiError('INVALID', '新密码至少 8 位，且同时包含字母和数字');
    if (p === oldPassword) throw new ApiError('INVALID', '新密码不能与当前密码相同');
    a.password = p;
    ctx.audit({ action: '修改密码', targetType: 'account', targetId: a.accountId, summary: `账号 ${a.username} 修改了登录密码`, reason: '本人修改密码' });
    ctx.dirty('accounts');
    return { ok: true };
  }, { write: true, latency: [200, 450] });
  /** 当前账号、菜单、权限与待办 */
  api.me = () => g.call('me', null, a => ({ account: PUBLIC_ACCOUNT(a), menus: a.menus, permissions: a.permissions, readonly: /^只读/.test(a.status), operator: a.operatorScope ? D.operator(a.operatorScope) : null, todos: todos(a) }), { latency: [80, 200] });
  function todos(a) {
    const T = [];
    const has = p => a.permissions.includes(p);
    const d = db();
    if (has('refund.confirm')) T.push({ key: 'tickets', label: '待处理工单', count: d.tickets.filter(t => t.status === '待处理' || t.status === '处理中').length, menu: '退款处理' });
    if (has('refund.confirm.overLimit')) T.push({ key: 'overLimit', label: '待主管确认的退款', count: d.tickets.filter(t => t.status === '待主管确认').length, menu: '退款处理' });
    if (has('order.forceEnd')) T.push({ key: 'forceEnd', label: '待强制结束的订单', count: d.tickets.filter(t => t.category === '订单无法结束' && t.status !== '已解决').length, menu: '强制结束订单' });
    if (has('settlement.manage')) T.push({ key: 'settlements', label: '待审核结算单', count: d.settlements.settlements.filter(s => s.status === '待审核').length, menu: '结算单管理' });
    if (has('dispute.decide')) T.push({ key: 'disputes', label: '待核实异议', count: d.settlements.disputes.filter(x => x.status === '待核实').length, menu: '异议处理' });
    if (has('recon.manage')) T.push({ key: 'recon', label: '待处理对账差异', count: d.payments.reconciliation.reduce((s, r) => s + r.diffItems.filter(i => i.status === '待处理').length, 0), menu: '收单对账' });
    if (has('refund.manualTransfer')) T.push({ key: 'refundFail', label: '退款失败待转账', count: refundFailures().length, menu: '欠费与退款失败' });
    if (has('degrade.control') || has('ops.monitor')) T.push({ key: 'alerts', label: '未确认告警', count: d.ops.alerts.filter(x => !x.ackBy && (x.notified || []).includes(a.accountId)).length, menu: '运行监控' });
    if (has('degrade.closure.handle') || has('degrade.closure.view')) T.push({ key: 'closure', label: '降级单待人工', count: d.orders.filter(o => o.degraded && o.degraded.closure && o.degraded.closure.status === '待人工').length, menu: '恢复闭合' });
    if (has('access.pause')) T.push({ key: 'suspend', label: '待确认的暂停可租建议', count: (d.ops.suspendSuggestions || []).filter(x => x.status === '待确认').length, menu: '接入验收' });
    if (has('acceptance.confirm')) T.push({ key: 'acceptance', label: '待确认的验收报告', count: d.integration.integrations.filter(i => i.acceptance && i.acceptance.result === '待确认').length, menu: '接入验收' });
    if (has('dispute.submit.own')) T.push({ key: 'myDisputes', label: '异议待平台核实', count: d.settlements.disputes.filter(x => x.operatorId === a.operatorScope && x.status === '待核实').length, menu: '结算单' });
    return T.filter(t => t.count > 0);
  }
  function refundFailures() {
    const pays = db().payments.payments;
    return pays.filter(p => (p.type === '退款' || p.type === '退押') && p.status === '失败' && !pays.some(x => x.relatedPaymentId === p.paymentId && x.status === '成功' && /人工转账/.test(x.type)));
  }
  /** 调试：直接切换到某个账号（仅 ?debug=1） */
  api.switchAccount = accountId => ctx.call('admin.switchAccount', () => {
    if (!ctx.debugOn) throw new ApiError('DEBUG_ONLY');
    const a = D.account(accountId);
    if (!a || !a.username || a.role === '执法人员') throw new ApiError('NOT_FOUND', '没有找到可登录的管理端账号');
    ctx.session.set('Admin', { accountId });
    return PUBLIC_ACCOUNT(a);
  });

  /* ================= 主管部门：价格与规则配置（R-19～R-22，BR-14、BR-15） ================= */
  const versionView = v => ({ ...clone(v) });
  api.pricing = {
    list: () => g.call('pricing.list', ['pricing.view', 'pricing.edit'], () => [...db().pricing.versions].sort((a, b) => ms(b.effectiveAt) - ms(a.effectiveAt)).map(versionView)),
    current: () => g.call('pricing.current', null, () => versionView(D.currentVersion())),
    get: id => g.call('pricing.get', ['pricing.view', 'pricing.edit'], () => versionView(ctx.get(db().pricing.versions, 'versionId', id, '计价规则版本'))),
    /** 平台公司参数（只读展示） */
    params: () => g.call('pricing.params', null, () => clone(db().pricing.platformParams)),
    /**
     * 新增一个版本（不覆盖旧版本）。payload 为要改的部分，未给出的项沿用"生效时刻前一版"。
     * payload: { name, effectiveAt?（缺省=立即）, note?, ride?, dispatchFee?, monthCard?, coupons?, deposit?, creditFree?, serviceFee?: {ratios}, riskLimits? }
     */
    create: (payload = {}, reason) => g.call('pricing.create', 'pricing.edit', a => {
      const why = ctx.reason(reason);
      const now = ctx.now();
      const eff = payload.effectiveAt ? fmt(new Date(String(payload.effectiveAt).replace(' ', 'T'))) : now;
      if (isNaN(ms(eff))) throw new ApiError('INVALID', '生效时间格式不正确');
      if (ms(eff) < ctx.nowMs() - 60e3) throw new ApiError('INVALID', '生效时间不能早于现在，改动只影响生效之后开锁的订单');
      if (!String(payload.name || '').trim()) throw new ApiError('INVALID', '请填写版本名称');
      const base = D.versionAt(eff) || D.currentVersion();
      const v = clone(base);
      const errs = [];
      const num = (x, k, lo, hi, dec = 1) => {
        if (x == null) return;
        const n = Number(x);
        if (!isFinite(n) || n < lo || n > hi) errs.push(`${k} 应在 ${lo}～${hi} 之间`);
        else if (Math.abs(n * 10 ** dec - Math.round(n * 10 ** dec)) > 1e-6) errs.push(dec ? `${k} 最多 ${dec} 位小数` : `${k} 须为整数`);
      };
      if (payload.ride) { num(payload.ride.startFee, '起步价', 0, 20); num(payload.ride.perMinute, '分钟价', 0, 5); num(payload.ride.startMinutes, '起步含分钟', 0, 60, 0); Object.assign(v.ride, payload.ride); }
      if (payload.dispatchFee != null) { num(payload.dispatchFee, '调度费', 0, 100); v.dispatchFee = Number(payload.dispatchFee); }
      if (payload.monthCard) { num(payload.monthCard.price, '月卡价格', 0, 500); num(payload.monthCard.perOrderCap, '月卡每单抵扣上限', 0, 50); num(payload.monthCard.days, '月卡天数', 1, 365, 0); Object.assign(v.monthCard, payload.monthCard); }
      if (payload.coupons) { for (const c of payload.coupons) { num(c.amount, `券「${c.name}」面额`, 0, 50); num(c.validDays, `券「${c.name}」有效天数`, 1, 90, 0); } v.coupons = clone(payload.coupons); }
      if (payload.deposit) { num(payload.deposit.amount, '押金金额', 0, 1000); Object.assign(v.deposit, payload.deposit); }
      if (payload.creditFree) { num(payload.creditFree.threshold, '免押门槛', 350, 950, 0); Object.assign(v.creditFree, payload.creditFree); }
      if (payload.serviceFee && payload.serviceFee.ratios) { for (const [b, r] of Object.entries(payload.serviceFee.ratios)) { if (!D.brand(b)) errs.push(`品牌 ${b} 不存在`); num(r, `${b} 服务费比例`, 0, 1, 4); } Object.assign(v.serviceFee.ratios, payload.serviceFee.ratios); }
      if (payload.riskLimits) {
        const rl = payload.riskLimits;
        num(rl.perUserOpenDegradedOrders, '每用户未结降级单数', 1, 5, 0); num(rl.hourlyDegradedOrders, '每小时降级单总量', 1, 100000, 0); num(rl.degradeDurationAlertMinutes, '降级持续告警分钟', 1, 1440, 0);
        for (const k of ['normal', 'tight']) if (rl[k]) { num(rl[k].maxMinutes, '单笔最长分钟', 1, 1440, 0); num(rl[k].maxAmount, '单笔最高金额', 0, 1000); }
        v.riskLimits = { ...v.riskLimits, ...rl, normal: { ...v.riskLimits.normal, ...(rl.normal || {}) }, tight: { ...v.riskLimits.tight, ...(rl.tight || {}) } };
      }
      if (errs.length) throw new ApiError('INVALID', errs.join('；'), { errors: errs });
      Object.assign(v, { versionId: ctx.ids.version(), name: String(payload.name).trim(), effectiveAt: eff, createdAt: now, createdBy: a.accountId, note: payload.note || why });
      delete v.revokedAt;
      db().pricing.versions.push(v);
      db().pricing.versions.sort((x, y) => ms(x.effectiveAt) - ms(y.effectiveAt) || (x.versionId < y.versionId ? -1 : 1));
      R.refreshVersionChain(db().pricing.versions, ctx.now());
      const cv = D.currentVersion();
      for (const b of db().operators.brands) b.currentServiceFeeRatio = cv.serviceFee.ratios[b.brandId] ?? null;
      ctx.audit({ action: '新增规则版本', targetType: 'pricing', targetId: v.versionId, summary: `录入计价规则版本 ${v.versionId}「${v.name}」，${eff.slice(0, 16)}生效`, reason: why, before: { versionId: base.versionId }, after: diffVersion(base, v) });
      ctx.dirty('pricing', 'operators');
      ctx.emit('pricing.changed', { versionId: v.versionId, effectiveAt: eff, immediate: v.status === '生效中' });
      return versionView(v);
    }, { write: true }),
    /** 撤销一个尚未生效的版本（保留记录，状态为已撤销） */
    revoke: (versionId, reason) => g.call('pricing.revoke', 'pricing.edit', a => {
      const why = ctx.reason(reason);
      const v = ctx.get(db().pricing.versions, 'versionId', versionId, '计价规则版本');
      if (v.status !== '待生效') throw new ApiError('CONFLICT', '只能撤销尚未生效的版本，已生效的版本留存可查');
      v.revokedAt = ctx.now(); v.revokedBy = a.accountId;
      R.refreshVersionChain(db().pricing.versions, ctx.now());
      ctx.audit({ action: '撤销规则版本', targetType: 'pricing', targetId: versionId, summary: `撤销待生效版本 ${versionId}「${v.name}」`, reason: why });
      ctx.dirty('pricing');
      ctx.emit('pricing.changed', { versionId, revoked: true });
      return versionView(v);
    }, { write: true }),
  };
  function diffVersion(a, b) {
    const out = {};
    const walk = (x, y, p) => {
      if (x && y && typeof x === 'object' && !Array.isArray(x)) { for (const k of new Set([...Object.keys(x), ...Object.keys(y)])) walk(x[k], y[k], p ? `${p}.${k}` : k); return; }
      if (JSON.stringify(x) !== JSON.stringify(y)) out[p] = { from: x ?? null, to: y ?? null };
    };
    for (const k of ['ride', 'dispatchFee', 'monthCard', 'coupons', 'deposit', 'creditFree', 'serviceFee', 'riskLimits']) walk(a[k], b[k], k);
    return out;
  }

  /* ================= 主管部门：站点与围栏、桩（R-26、R-27） ================= */
  const stationView = s => {
    const ps = db().stations.piles.filter(p => p.stationId === s.stationId);
    return { ...clone(s), pileCount: ps.length, onlinePiles: ps.filter(p => p.status === '在线').length, freePiles: ps.filter(p => p.status === '在线' && !p.occupiedVehicleId).length, faultPiles: ps.filter(p => p.status !== '在线').length, parkedVehicles: db().vehicles.filter(v => v.stationId === s.stationId).length, fullNow: s.type === '有桩' && ps.length > 0 && !ps.some(p => p.status === '在线' && !p.occupiedVehicleId), updatedByName: D.actorName(s.updatedBy) };
  };
  function checkFence(fence) {
    if (!Array.isArray(fence) || fence.length < 4) throw new ApiError('INVALID', '围栏至少需要 3 个顶点');
    const f = fence.map(p => [+(+p[0]).toFixed(6), +(+p[1]).toFixed(6)]);
    if (f[0][0] !== f[f.length - 1][0] || f[0][1] !== f[f.length - 1][1]) f.push(f[0]);
    const [[w, s], [e, n]] = db().stations.region.bounds;
    if (f.some(p => p[0] < w || p[0] > e || p[1] < s || p[1] > n)) throw new ApiError('INVALID', '围栏超出本市范围');
    if (polygonArea(f) < 20) throw new ApiError('INVALID', '围栏面积过小，请重新绘制');
    return f;
  }
  api.stations = {
    list: ({ district, type, q, includeRemoved = false, page = 1, pageSize = 20, sort } = {}) => g.call('stations.list', ['station.edit', 'pile.edit', 'ops.monitor'], () => {
      let list = db().stations.stations.filter(s => (includeRemoved || s.status !== '已停用') && (!district || s.district === district) && (!type || s.type === type) && (!q || s.name.includes(q) || s.stationId.includes(q)));
      return { ...paginate(list.map(stationView), { page, pageSize, sort }), region: db().stations.region };
    }),
    /** 地图用：全部站点（不分页） */
    all: () => g.call('stations.all', null, () => ({ region: db().stations.region, stations: db().stations.stations.filter(s => s.status !== '已停用').map(stationView) })),
    get: id => g.call('stations.get', null, () => { const s = ctx.get(db().stations.stations, 'stationId', id, '站点'); return { ...stationView(s), piles: db().stations.piles.filter(p => p.stationId === id) }; }),
    create: ({ name, district, type = '无桩', fence, capacity } = {}, reason) => g.call('stations.create', 'station.edit', a => {
      const why = ctx.reason(reason);
      if (!String(name || '').trim()) throw new ApiError('INVALID', '请填写站点名称');
      if (!['东城区', '西城区', '滨河区', '高新区'].includes(district)) throw new ApiError('INVALID', '请选择所在区');
      if (!['有桩', '无桩'].includes(type)) throw new ApiError('INVALID', '站点类型只能是有桩或无桩');
      const f = checkFence(fence);
      const t = ctx.now();
      const s = { stationId: ctx.ids.station(), name: String(name).trim(), district, type, center: centroid(f), fence: f, capacity: Math.max(1, +capacity || 10), pileIds: [], createdAt: t, updatedAt: t, updatedBy: a.accountId, fullNow: false };
      db().stations.stations.push(s);
      ctx.audit({ action: '新建站点', targetType: 'station', targetId: s.stationId, summary: `新建站点 ${s.stationId}「${s.name}」（${type}）`, reason: why });
      ctx.dirty('stations'); ctx.emit('station.changed', { stationId: s.stationId });
      return stationView(s);
    }, { write: true }),
    update: (id, patch = {}, reason) => g.call('stations.update', 'station.edit', a => {
      const why = ctx.reason(reason);
      const s = ctx.get(db().stations.stations, 'stationId', id, '站点');
      const before = { name: s.name, fence: s.fence, capacity: s.capacity, type: s.type };
      if (patch.fence) { s.fence = checkFence(patch.fence); s.center = centroid(s.fence); }
      if (patch.name != null) { if (!String(patch.name).trim()) throw new ApiError('INVALID', '请填写站点名称'); s.name = String(patch.name).trim(); }
      if (patch.capacity != null) s.capacity = Math.max(1, +patch.capacity || 1);
      if (patch.type && ['有桩', '无桩'].includes(patch.type)) s.type = patch.type;
      s.updatedAt = ctx.now(); s.updatedBy = a.accountId;
      ctx.audit({ action: '调整站点', targetType: 'station', targetId: id, summary: `调整站点 ${id}「${s.name}」${patch.fence ? '电子围栏' : ''}${patch.name ? ' 名称' : ''}${patch.capacity != null ? ' 容量' : ''}`.trim(), reason: why, before, after: { name: s.name, fence: s.fence, capacity: s.capacity, type: s.type } });
      ctx.dirty('stations'); ctx.emit('station.changed', { stationId: id });
      return stationView(s);
    }, { write: true }),
  };
  /** 删除站点：站点下还有桩、停着车或有进行中订单时拒绝；删除后保留记录（状态"已停用"），不再参与还车判定 */
  api.stations.remove = (id, reason) => g.call('stations.remove', 'station.edit', a => {
    const why = ctx.reason(reason);
    const s = ctx.get(db().stations.stations, 'stationId', id, '站点');
    if (s.status === '已停用') throw new ApiError('ALREADY_DONE', '该站点已删除');
    const piles = db().stations.piles.filter(p => p.stationId === id).length;
    if (piles) throw new ApiError('STATION_IN_USE', `站点下还有 ${piles} 个桩，桩撤除并在桩管理中处理后才能删除站点`, { piles });
    const riding = db().orders.filter(o => o.status === '骑行中' && (o.startStationId === id || o.claimedStationId === id)).length;
    if (riding) throw new ApiError('STATION_IN_USE', `该站点有 ${riding} 单进行中的骑行，结束后才能删除`, { riding });
    const parked = db().vehicles.filter(v => v.stationId === id).length;
    if (parked) throw new ApiError('STATION_IN_USE', `站点内还停着 ${parked} 辆车，运营商调走后才能删除`, { parked });
    Object.assign(s, { status: '已停用', removedAt: ctx.now(), removedBy: a.accountId, updatedAt: ctx.now(), updatedBy: a.accountId });
    ctx.audit({ action: '删除站点', targetType: 'station', targetId: id, summary: `删除站点 ${id}「${s.name}」`, reason: why });
    ctx.dirty('stations'); ctx.emit('station.changed', { stationId: id, removed: true });
    return { ok: true, stationId: id };
  }, { write: true });
  api.piles = {
    list: ({ stationId, status, page = 1, pageSize = 50, sort } = {}) => g.call('piles.list', ['pile.edit', 'station.edit', 'ops.monitor'], () => paginate(db().stations.piles.filter(p => (!stationId || p.stationId === stationId) && (!status || p.status === status)).map(p => ({ ...clone(p), stationName: V.stationName(p.stationId), occupiedPlateNo: p.occupiedVehicleId ? (D.vehicle(p.occupiedVehicleId) || {}).plateNo : null })), { page, pageSize, sort })),
    create: ({ stationId, location } = {}, reason) => g.call('piles.create', 'pile.edit', a => {
      const why = ctx.reason(reason);
      const s = ctx.get(db().stations.stations, 'stationId', stationId, '站点');
      if (!Array.isArray(location) || !inPolygon(location, s.fence)) throw new ApiError('INVALID', '桩的位置须在站点围栏内');
      const t = ctx.now();
      const no = Math.max(0, ...db().stations.piles.filter(p => p.stationId === stationId).map(p => p.no)) + 1;
      const p = { pileId: ctx.ids.pile(), stationId, no, location: location.map(x => +(+x).toFixed(6)), status: '在线', occupiedVehicleId: null, lastHeartbeatAt: t, installedAt: t, faultNote: null };
      db().stations.piles.push(p); s.pileIds.push(p.pileId);
      if (s.type === '无桩') s.type = '有桩';
      ctx.audit({ action: '登记桩', targetType: 'pile', targetId: p.pileId, summary: `站点 ${stationId}「${s.name}」登记 ${no} 号桩 ${p.pileId}`, reason: why });
      ctx.dirty('stations'); ctx.emit('station.changed', { stationId });
      return p;
    }, { write: true }),
    setStatus: (pileId, status, reason, faultNote) => g.call('piles.setStatus', 'pile.edit', a => {
      const why = ctx.reason(reason);
      const p = ctx.get(db().stations.piles, 'pileId', pileId, '桩');
      if (!['在线', '离线', '故障'].includes(status)) throw new ApiError('INVALID', '桩状态只能是在线、离线或故障');
      const before = p.status;
      p.status = status; p.faultNote = status === '在线' ? null : (faultNote || why);
      ctx.audit({ action: '设置桩状态', targetType: 'pile', targetId: pileId, summary: `桩 ${pileId} 状态 ${before} → ${status}`, reason: why, before: { status: before }, after: { status } });
      ctx.dirty('stations'); ctx.emit('station.changed', { stationId: p.stationId });
      return p;
    }, { write: true }),
  };

  /* ================= 主管部门：接入状态总览与运营商退出（R-42） ================= */
  api.access = {
    overview: () => g.call('access.overview', ['access.view', 'access.view.own', 'ops.monitor'], a => {
      const scope = a.operatorScope;
      const brands = db().operators.brands.filter(b => !scope || b.operatorId === scope);
      const statuses = ['未接入', '联调中', '已接入', '暂停可租', '已退出'];
      return {
        summary: statuses.map(s => ({ status: s, brandCount: brands.filter(b => b.accessStatus === s).length, vehicleCount: db().vehicles.filter(v => brands.some(b => b.brandId === v.brandId && b.accessStatus === s)).length })),
        brands: brands.map(b => {
          const vs = db().vehicles.filter(v => v.brandId === b.brandId);
          const cnt = k => vs.reduce((m, v) => (m[v[k]] = (m[v[k]] || 0) + 1, m), {});
          const integ = db().integration.integrations.find(i => i.brandId === b.brandId);
          const pause = db().ops.pauses.filter(p => p.brandId === b.brandId && !p.resumedAt).pop() || null;
          const lastM = [...db().ops.metricsHourly].filter(m => m.brandId === b.brandId && m.unlockSuccessRate != null).sort((x, y) => ms(y.at) - ms(x.at))[0] || null;
          return { ...V.brand(b), unlockSuccessRate: lastM ? lastM.unlockSuccessRate : null, unlockRateAt: lastM ? lastM.at : null, operator: clone(D.operator(b.operatorId)), byStatus: cnt('status'), byCodeStatus: cnt('codeStatus'), rentable: vs.filter(D.rentable).length, statusHistory: integ ? integ.statusHistory : [], pause };
        }),
      };
    }),
    /** 按线下结果设置运营商退出：其品牌已退出、车辆码失效、凭证停用；在途订单照常结束 */
    setExit: (operatorId, reason) => g.call('access.setExit', 'access.setExit', a => {
      const why = ctx.reason(reason);
      const op = ctx.get(db().operators.operators, 'operatorId', operatorId, '运营商');
      if (op.status === '已退出') throw new ApiError('ALREADY_DONE', '该运营商已是退出状态');
      const t = ctx.now();
      op.status = '已退出';
      let n = 0;
      for (const b of db().operators.brands.filter(x => x.operatorId === operatorId)) {
        b.accessStatus = '已退出'; b.statusSince = t;
        const integ = db().integration.integrations.find(i => i.brandId === b.brandId);
        if (integ) { integ.status = '已退出'; integ.statusSince = t; integ.statusHistory.push({ status: '已退出', at: t, by: a.accountId, note: why }); integ.credentials.status = '停用'; integ.credentials.disabledAt = t; }
        for (const v of db().vehicles.filter(x => x.brandId === b.brandId)) {
          if (v.codeStatus !== '已失效') { v.codeStatus = '已失效'; v.codeInvalidAt = t; n++; }
          if (v.status !== '骑行中') v.status = '码已失效';
        }
      }
      for (const acc of db().accounts.accounts.filter(x => x.operatorScope === operatorId && x.status === '正常')) acc.status = '只读（运营商已退出）';
      ctx.audit({ action: '设置运营商退出', targetType: 'operator', targetId: operatorId, summary: `按线下结果设置 ${op.shortName} 已退出，${n} 辆车政府码失效`, reason: why });
      ctx.dirty('operators', 'integration', 'vehicles', 'accounts');
      ctx.emit('access.changed', { operatorId });
      return { operator: clone(op), invalidatedCodes: n };
    }, { write: true }),
  };

  /* ================= 主管部门：证据查询与导出（R-45） ================= */
  const evView = e => ({ ...clone(e), officerName: D.actorName(e.officerId), brandName: e.brandId ? D.brand(e.brandId).name : null, operatorName: e.operatorId ? D.operator(e.operatorId).shortName : null, plateNo: e.vehicleId ? (D.vehicle(e.vehicleId) || {}).plateNo : null });
  const evFilter = (f = {}) => db().evidence.evidence.filter(e => (!f.scanResult || e.scanResult === f.scanResult) && (!f.brandId || e.brandId === f.brandId) && (!f.operatorId || e.operatorId === f.operatorId) && (!f.officerId || e.officerId === f.officerId) && g.inRange(e.capturedAt, f.from, f.to) && (!f.q || [e.evidenceId, e.vehicleDescription, e.address, e.vehicleId && (D.vehicle(e.vehicleId) || {}).plateNo].some(x => x && String(x).includes(f.q))));
  api.evidence = {
    list: (f = {}) => g.call('evidence.list', 'evidence.view', () => paginate(evFilter(f).sort((a, b) => ms(b.capturedAt) - ms(a.capturedAt)).map(evView), f)),
    get: id => g.call('evidence.get', 'evidence.view', () => evView(ctx.get(db().evidence.evidence, 'evidenceId', id, '证据'))),
    /** 重算摘要，核对证据未被篡改 */
    verify: id => g.call('evidence.verify', 'evidence.view', async () => {
      const e = ctx.get(db().evidence.evidence, 'evidenceId', id, '证据');
      const actual = await evidenceHash(e, ctx.label);
      return { evidenceId: id, ok: actual === e.sha256, stored: e.sha256, actual };
    }),
    update: id => g.call('evidence.update', 'evidence.view', () => { ctx.get(db().evidence.evidence, 'evidenceId', id, '证据'); throw new ApiError('EVIDENCE_LOCKED'); }),
    export: (f = {}, reason) => g.call('evidence.export', 'evidence.export', a => {
      const list = evFilter(f).sort((x, y) => ms(x.capturedAt) - ms(y.capturedAt));
      if (!list.length) throw new ApiError('INVALID', '没有符合条件的证据，换个筛选条件试试');
      const t = ctx.now();
      const ex = { exportId: ctx.ids.exportId(t), at: t, by: a.accountId, filter: { ...f }, evidenceIds: list.map(e => e.evidenceId), count: list.length, fileName: `执法证据_${ctx.label(t).replace(/\D/g, '').slice(0, 12)}.zip` };
      db().evidence.exports.push(ex);
      ctx.audit({ action: '导出证据', targetType: 'evidence', targetId: ex.exportId, summary: `导出执法证据 ${list.length} 条`, reason: reason || null });
      ctx.dirty('evidence');
      const cols = [{ title: '证据编号', key: 'evidenceId' }, { title: '取证时间', key: 'capturedAt' }, { title: '执法人员工号', key: 'badgeNo' }, { title: '核查结果', key: 'scanResult' }, { title: '品牌', value: e => (e.brandId ? D.brand(e.brandId).name : '') }, { title: '运营商', value: e => (e.operatorId ? D.operator(e.operatorId).shortName : '') }, { title: '车辆描述', key: 'vehicleDescription' }, { title: '地址', key: 'address' }, { title: '经度', value: e => e.location[0] }, { title: '纬度', value: e => e.location[1] }, { title: '照片数', value: e => e.photos.length }, { title: 'SHA-256', key: 'sha256' }];
      return { export: ex, csv: csv(cols, list), csvFileName: ex.fileName.replace('.zip', '.csv') };
    }),
    exports: () => g.call('evidence.exports', 'evidence.view', () => [...db().evidence.exports].reverse().map(x => ({ ...x, byName: D.actorName(x.by) }))),
  };

  /* ================= 操作留痕（R-47） ================= */
  api.audit = {
    list: (f = {}) => g.call('audit.list', 'audit.view', () => {
      const list = db().audit.filter(x => (!f.accountId || x.accountId === f.accountId) && (!f.action || [].concat(f.action).includes(x.action)) && (!f.targetType || x.targetType === f.targetType) && (!f.targetId || x.targetId === f.targetId) && g.inRange(x.at, f.from, f.to) && (!f.actorKind || D.actorKind(x.accountId) === f.actorKind) && (!f.q || [x.summary, x.reason, x.targetId].some(s => s && s.includes(f.q))));
      const pg = paginate([...list].sort((a, b) => ms(b.at) - ms(a.at) || (a.auditId < b.auditId ? 1 : -1)), f);
      return { ...pg, rows: pg.rows.map(x => ({ ...x, actorName: D.actorName(x.accountId), actorKind: D.actorKind(x.accountId) })) };
    }),
    actions: () => g.call('audit.actions', 'audit.view', () => [...new Set(db().audit.map(x => x.action))].sort()),
  };

  /* ================= 账号与授权（R-46，运维账号管理员） ================= */
  api.accounts = {
    list: (f = {}) => g.call('accounts.list', 'account.manage', () => paginate(db().accounts.accounts.filter(a => (!f.role || a.role === f.role) && (!f.q || [a.name, a.username, a.org].some(s => s && s.includes(f.q)))).map(PUBLIC_ACCOUNT), f)),
    create: ({ username, name, role, org, operatorScope = null } = {}, reason) => g.call('accounts.create', 'account.manage', a => {
      const why = ctx.reason(reason);
      if (!/^[a-z0-9.]{3,30}$/.test(String(username || ''))) throw new ApiError('INVALID', '账号名只能用小写字母、数字和点，3～30 位');
      if (db().accounts.accounts.some(x => x.username === username)) throw new ApiError('INVALID', '账号名已存在');
      const tpl = db().accounts.accounts.find(x => x.role === role && x.username);
      if (!tpl) throw new ApiError('INVALID', '岗位不存在');
      if (/运营商/.test(role) && !D.operator(operatorScope)) throw new ApiError('INVALID', '运营商账号须指定所属运营商');
      const prefix = { 主管部门管理员: '00', 运营商业务: '01', 运营商技术对接: '01', 财务: '02', 客服: '03', 客服主管: '03', 运维值班: '04', 运维接入负责人: '04', 运维账号管理员: '04', 执法人员: '05' }[role] || '06';
      const acc = { ...clone(tpl), accountId: ctx.ids.account(prefix), username, name: String(name || '').trim() || tpl.name, org: org || tpl.org, operatorScope: /运营商/.test(role) ? operatorScope : null, password: db().accounts.loginInfo.password, status: '正常', createdAt: ctx.now(), createdBy: a.accountId, lastLoginAt: null };
      db().accounts.accounts.push(acc);
      ctx.audit({ action: '开通账号', targetType: 'account', targetId: acc.accountId, summary: `开通${role}账号 ${username}`, reason: why });
      ctx.dirty('accounts');
      return PUBLIC_ACCOUNT(acc);
    }, { write: true }),
    setStatus: (accountId, status, reason) => g.call('accounts.setStatus', 'account.manage', () => {
      const why = ctx.reason(reason);
      const acc = ctx.get(db().accounts.accounts, 'accountId', accountId, '账号');
      if (!['正常', '停用'].includes(status)) throw new ApiError('INVALID', '状态只能是正常或停用');
      if (!acc.username) throw new ApiError('INVALID', '智能体与系统账号不能修改');
      const before = acc.status; acc.status = status;
      ctx.audit({ action: '调整权限', targetType: 'account', targetId: accountId, summary: `账号 ${acc.username} 状态 ${before} → ${status}`, reason: why, before: { status: before }, after: { status } });
      ctx.dirty('accounts');
      return PUBLIC_ACCOUNT(acc);
    }, { write: true }),
  };

  /* ================= 通用订单查询（平台公司岗位；运营商走 operator.orders） ================= */
  api.orders = {
    list: (f = {}) => g.call('orders.list', ['order.view', 'settlement.manage', 'ops.monitor', 'degrade.closure.view', 'arrears.view'], () => {
      const q = f.q && String(f.q).trim();
      const users = q && /^1\d{10}$/.test(q) ? new Set(db().users.users.filter(u => u.phone === q).map(u => u.userId)) : null;
      const list = db().orders.filter(o => (!f.userId || o.userId === f.userId) && (!f.status || o.status === f.status) && (!f.payStatus || o.payStatus === f.payStatus) && (!f.brandId || o.brandId === f.brandId) && (!f.operatorId || o.operatorId === f.operatorId) && (f.degraded == null || !!o.degraded === !!f.degraded) && g.inRange(o.requestAt, f.from, f.to) && (!q || o.orderId.includes(q) || o.userId === q || (users && users.has(o.userId)) || (D.vehicle(o.vehicleId) || {}).plateNo === q));
      const pg = paginate([...list].sort((a, b) => ms(b.requestAt) - ms(a.requestAt)), f);
      return { ...pg, rows: pg.rows.map(V.adminOrder) };
    }),
    get: id => g.call('orders.get', ['order.view', 'settlement.manage', 'ops.monitor', 'degrade.closure.view', 'arrears.view'], () => {
      const o = ctx.get(db().orders, 'orderId', id, '订单');
      return { ...V.adminOrder(o), payments: db().payments.payments.filter(p => p.orderId === id), tickets: db().tickets.filter(t => t.orderId === id), audit: db().audit.filter(x => x.targetId === id) };
    }),
  };

  Object.assign(api, createOperatorApi(ctx, D, V, A, g));
  api.finance = createFinanceApi(ctx, D, V, A, g, refundFailures);
  api.cs = createCsApi(ctx, D, V, A, g);
  api.ops = createOpsApi(ctx, D, V, A, g);
  return api;
}

/** 证据摘要（SCHEMA 5.10）。时间取标签时间（未平移的原始时刻），平移后仍可复核 */
export async function evidenceHash(e, label) {
  return sha256hex([e.evidenceId, e.officerId, label(e.capturedAt), `${e.location[0].toFixed(6)},${e.location[1].toFixed(6)}`, e.scanResult, ...e.photos].join('|'));
}

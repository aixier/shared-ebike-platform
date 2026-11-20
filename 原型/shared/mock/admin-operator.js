// 运营商端（业务人员与技术对接人员）：只看本运营商的数据（R-46、NFR-11）。
import { ApiError } from './errors.js';
import { C, Y, ms, fmt, paginate, csv, clone, addDays } from './util.js';
import * as R from './rules.js';

export function createOperatorApi(ctx, D, V, A, g) {
  const db = () => ctx.db;
  const own = (a, opId) => g.scope(a, opId);
  const ownBrandIds = opId => db().operators.brands.filter(b => b.operatorId === opId).map(b => b.brandId);

  /* ---------------- 车辆清单与发码（M-04、R-01、R-41） ---------------- */
  const vehicleRow = v => ({ ...clone(v), brandName: D.brand(v.brandId).name, stationName: V.stationName(v.stationId), rentable: D.rentable(v) });
  const vehicles = {
    list: (f = {}) => g.call('operator.vehicles.list', ['vehicle.view.own', 'access.view', 'ops.monitor'], a => {
      const op = own(a, f.operatorId);
      const list = db().vehicles.filter(v => (!op || v.operatorId === op) && (!f.brandId || v.brandId === f.brandId) && (!f.codeStatus || v.codeStatus === f.codeStatus) && (!f.status || v.status === f.status) && (!f.q || [v.plateNo, v.govCode, v.vehicleId, v.pileTag].some(s => s && s.includes(String(f.q).toUpperCase()))));
      const pg = paginate(list, f);
      const scopeList = db().vehicles.filter(v => !op || v.operatorId === op);
      return { ...pg, rows: pg.rows.map(vehicleRow), stats: { total: scopeList.length, enabled: scopeList.filter(v => v.codeStatus === '已启用').length, pending: scopeList.filter(v => v.codeStatus === '待启用').length, invalid: scopeList.filter(v => v.codeStatus === '已失效').length } };
    }),
    /**
     * 上传车辆清单（整批校验，全部正确才入库），入库即发政府码。
     * rows: [{brandId, plateNo, model, pileTag}]；返回 {added, vehicles}；有错误时以 VEHICLE_LIST_INVALID 拒绝，error.errors=[{row, field, message}]
     */
    upload: (rows = []) => g.call('operator.vehicles.upload', 'vehicle.edit.own', a => {
      const op = own(a);
      const errors = [];
      const seenPlate = new Set(), seenTag = new Set();
      const brandIds = ownBrandIds(op);
      if (!rows.length) throw new ApiError('INVALID', '清单为空');
      rows.forEach((r, i) => {
        const row = i + 1;
        const b = D.brand(r.brandId);
        if (!b || !brandIds.includes(r.brandId)) errors.push({ row, field: 'brandId', message: '品牌不属于本运营商' });
        else if (['已退出', '未接入'].includes(b.accessStatus)) errors.push({ row, field: 'brandId', message: `品牌${b.accessStatus}，不能提交车辆` });
        const plate = String(r.plateNo || '').trim().toUpperCase();
        const prefix = b ? (db().vehicles.find(v => v.brandId === b.brandId) || {}).plateNo?.slice(0, 2) : null;
        if (!/^[A-Z]{2}\d{6}$/.test(plate)) errors.push({ row, field: 'plateNo', message: '车号应为 2 位字母加 6 位数字' });
        else if (prefix && plate.slice(0, 2) !== prefix) errors.push({ row, field: 'plateNo', message: `该品牌车号以 ${prefix} 开头` });
        else if (seenPlate.has(plate) || db().vehicles.some(v => v.plateNo === plate)) errors.push({ row, field: 'plateNo', message: '车号重复' });
        seenPlate.add(plate);
        const tag = String(r.pileTag || '').trim().toUpperCase();
        if (!/^[0-9A-F]{24}$/.test(tag)) errors.push({ row, field: 'pileTag', message: '桩识别用车辆标识应为 24 位十六进制' });
        else if (seenTag.has(tag) || db().vehicles.some(v => v.pileTag === tag)) errors.push({ row, field: 'pileTag', message: '车辆标识重复' });
        seenTag.add(tag);
        if (!String(r.model || '').trim()) errors.push({ row, field: 'model', message: '请填写车型' });
      });
      if (errors.length) throw new ApiError('VEHICLE_LIST_INVALID', `车辆清单有 ${new Set(errors.map(e => e.row)).size} 行错误，请修改后重新上传`, { errors });
      const t = ctx.now();
      const stations = db().stations.stations;
      const added = rows.map((r, i) => {
        const b = D.brand(r.brandId);
        const live = b.accessStatus === '已接入' || b.accessStatus === '暂停可租';
        const integ = db().integration.integrations.find(x => x.brandId === b.brandId);
        const plan = !live ? (db().vehicles.find(v => v.brandId === b.brandId && v.codeEnableAt) || {}).codeEnableAt || null : null;
        const st = stations[(ctx.randInt(0, stations.length - 1) + i) % stations.length];
        const v = {
          vehicleId: ctx.ids.vehicle(), brandId: b.brandId, operatorId: op, plateNo: String(r.plateNo).trim().toUpperCase(), govCode: ctx.ids.govCode(),
          pileTag: String(r.pileTag).trim().toUpperCase(), vehicleType: b.vehicleType, model: String(r.model).trim(), listedAt: t, codeIssuedAt: t,
          codeStatus: live ? '已启用' : '待启用', codeEnableAt: live ? null : plan, codeInvalidAt: null, battery: 100, status: live ? '可用' : '码未启用',
          location: st.center, stationId: st.stationId, pileId: null, lastReportAt: t,
        };
        db().vehicles.push(v);
        if (integ) { integ.vehicleList = { ...(integ.vehicleList || {}), submittedAt: t, vehicleCount: db().vehicles.filter(x => x.brandId === b.brandId).length, codesIssued: db().vehicles.filter(x => x.brandId === b.brandId).length, submittedBy: a.accountId }; }
        return v;
      });
      for (const b of new Set(added.map(v => v.brandId))) D.brand(b).vehicleCount = db().vehicles.filter(v => v.brandId === b).length;
      ctx.audit({ action: '提交车辆清单', targetType: 'brand', targetId: added[0].brandId, summary: `${D.operator(op).shortName} 上传车辆清单 ${added.length} 辆`, reason: null });
      ctx.audit({ action: '发放政府码', targetType: 'brand', targetId: added[0].brandId, accountId: 'AC0900', summary: `按车辆清单发放政府码 ${added.length} 个（${added[0].govCode} 等）`, reason: null });
      ctx.dirty('vehicles', 'operators', 'integration');
      ctx.emit('vehicle.changed', { operatorId: op, added: added.length });
      return { added: added.length, vehicles: added.map(vehicleRow) };
    }, { write: true }),
    /** 在线编辑：车型、桩识别用车辆标识 */
    update: (vehicleId, patch = {}) => g.call('operator.vehicles.update', 'vehicle.edit.own', a => {
      const v = ctx.get(db().vehicles, 'vehicleId', vehicleId, '车辆');
      own(a, v.operatorId);
      const before = { model: v.model, pileTag: v.pileTag };
      if (patch.model != null) { if (!String(patch.model).trim()) throw new ApiError('INVALID', '请填写车型'); v.model = String(patch.model).trim(); }
      if (patch.pileTag != null) {
        const tag = String(patch.pileTag).trim().toUpperCase();
        if (!/^[0-9A-F]{24}$/.test(tag)) throw new ApiError('INVALID', '桩识别用车辆标识应为 24 位十六进制');
        if (db().vehicles.some(x => x.pileTag === tag && x.vehicleId !== vehicleId)) throw new ApiError('INVALID', '车辆标识重复');
        v.pileTag = tag;
      }
      ctx.audit({ action: '编辑车辆', targetType: 'brand', targetId: v.brandId, summary: `编辑车辆 ${v.plateNo}`, before, after: { model: v.model, pileTag: v.pileTag } });
      ctx.dirty('vehicles');
      return vehicleRow(v);
    }, { write: true }),
    /** 从清单移除：政府码随之失效（R-01）；骑行中的车不能移除 */
    remove: (vehicleId, reason) => g.call('operator.vehicles.remove', 'vehicle.edit.own', a => {
      const why = ctx.reason(reason);
      const v = ctx.get(db().vehicles, 'vehicleId', vehicleId, '车辆');
      own(a, v.operatorId);
      if (v.status === '骑行中') throw new ApiError('CONFLICT', '车辆正在骑行中，还车后再移除');
      if (v.codeStatus === '已失效') throw new ApiError('ALREADY_DONE', '该车的码已失效');
      const t = ctx.now();
      Object.assign(v, { codeStatus: '已失效', codeInvalidAt: t, status: '码已失效' });
      if (v.pileId) { const p = D.pile(v.pileId); if (p) p.occupiedVehicleId = null; v.pileId = null; ctx.dirty('stations'); }
      ctx.audit({ action: '移出车辆', targetType: 'brand', targetId: v.brandId, summary: `从清单移除车辆 ${v.plateNo}，政府码 ${v.govCode} 失效`, reason: why });
      ctx.dirty('vehicles');
      ctx.emit('vehicle.changed', { vehicleId });
      return vehicleRow(v);
    }, { write: true }),
  };

  /* ---------------- 每日订单明细（R-24） ---------------- */
  const opOrders = (op, f) => db().orders.filter(o => o.operatorId === op && (!f.brandId || o.brandId === f.brandId) && (!f.status || o.status === f.status) && (f.degraded == null || !!o.degraded === !!f.degraded) && (!f.date || (o.requestAt || '').startsWith(f.date)) && g.inRange(o.requestAt, f.from, f.to) && (!f.q || o.orderId.includes(f.q) || (D.vehicle(o.vehicleId) || {}).plateNo === String(f.q).toUpperCase()));
  const ORDER_COLS = [
    { title: '订单号', key: 'orderId' }, { title: '车号', key: 'plateNo' }, { title: '品牌', key: 'brandName' }, { title: '状态', key: 'status' },
    { title: '开锁请求时刻', key: 'requestAt' }, { title: '开锁结果', key: 'unlockResult' }, { title: '计费起点', key: 'startAt' }, { title: '计费终点', key: 'endAt' },
    { title: '锁车报告时刻', key: 'lockReportAt' }, { title: '桩在位时刻', key: 'pileInAt' }, { title: '结束方式', key: 'endMethod' }, { title: '在点依据', key: 'inStationBasis' },
    { title: '时长（秒）', key: 'durationSec' }, { title: '计费分钟', key: 'billedMinutes' }, { title: '里程（公里）', key: 'distanceKm' },
    { title: '订单应收（元）', key: 'receivable' }, { title: '降级单', value: r => (r.degraded ? '是' : '否') }, { title: '补报', value: r => (r.lateReport ? '是' : '否') }, { title: '结算月', key: 'settleMonth' },
  ];
  const orders = {
    list: (f = {}) => g.call('operator.orders.list', 'order.view.own', a => {
      const op = own(a, f.operatorId);
      const list = opOrders(op, f).sort((x, y) => ms(y.requestAt) - ms(x.requestAt));
      const pg = paginate(list, f);
      const fin = list.filter(o => o.fee);
      return { ...pg, rows: pg.rows.map(V.operatorOrder), summary: { count: list.length, finished: fin.length, receivableSum: Y(fin.reduce((s, o) => s + C(o.fee.receivable), 0)), degraded: list.filter(o => o.degraded).length } };
    }),
    get: id => g.call('operator.orders.get', 'order.view.own', a => { const o = ctx.get(db().orders, 'orderId', id, '订单'); own(a, o.operatorId); return V.operatorOrder(o); }),
    /** 下载某日明细（含降级单）：返回 CSV 文本 */
    export: ({ date, from, to, brandId } = {}) => g.call('operator.orders.export', 'order.export.own', a => {
      const op = own(a);
      const list = opOrders(op, { date, from, to, brandId }).sort((x, y) => ms(x.requestAt) - ms(y.requestAt)).map(V.operatorOrder);
      const name = `${D.operator(op).shortName}_订单明细_${date || `${from || ''}_${to || ''}`}.csv`;
      return { fileName: name, count: list.length, csv: csv(ORDER_COLS, list) };
    }),
  };

  /* ---------------- 结算单与异议（R-23、R-24、BR-16） ---------------- */
  const sView = (s, withDetails = false) => {
    const x = clone(s);
    x.operatorName = D.operator(s.operatorId).shortName;
    x.brands = (x.brands || []).map(b => ({ ...b, brandName: D.brand(b.brandId).name }));
    x.overdue = !s.paidAt && s.dueDate && ctx.now().slice(0, 10) > s.dueDate;
    if (!withDetails) { x.detailCount = s.details.length; delete x.details; }
    return x;
  };
  const settlements = {
    list: (f = {}) => g.call('operator.settlements.list', 'settlement.view.own', a => {
      const op = own(a, f.operatorId);
      return db().settlements.settlements.filter(s => s.operatorId === op).sort((x, y) => (x.month < y.month ? 1 : -1)).map(s => sView(s));
    }),
    get: id => g.call('operator.settlements.get', 'settlement.view.own', a => {
      const s = ctx.get(db().settlements.settlements, 'settlementId', id, '结算单');
      own(a, s.operatorId);
      return sView(s, true);
    }),
    /** 本期（尚未出单）的实时预览 */
    current: () => g.call('operator.settlements.current', 'settlement.view.own', a => sView(D.previewSettlement(own(a)), true)),
    export: id => g.call('operator.settlements.export', 'settlement.export.own', a => {
      const s = ctx.get(db().settlements.settlements, 'settlementId', id, '结算单');
      own(a, s.operatorId);
      const cols = [{ title: '订单号', key: 'orderId' }, { title: '品牌', value: d => D.brand(d.brandId).name }, { title: '开始', key: 'startAt' }, { title: '结束', key: 'endAt' }, { title: '计费分钟', key: 'billedMinutes' }, { title: '里程', key: 'distanceKm' }, { title: '降级单', value: d => (d.degraded ? '是' : '否') }, { title: '价格版本', key: 'pricingVersionId' }, { title: '订单应收', key: 'receivable' }, { title: '出单时已退款', key: 'refundedAtGeneration' }, { title: '服务费基数', key: 'base' }, { title: '比例', key: 'ratio' }, { title: '服务费', key: 'fee' }, { title: '备注', key: 'remark' }];
      const adj = s.adjustments.flatMap(x => x.items.map(it => ({ orderId: it.orderId, brandId: x.brandId, startAt: '', endAt: it.endAt, billedMinutes: '', distanceKm: '', degraded: false, pricingVersionId: '', receivable: '', refundedAtGeneration: '', base: it.newBase, ratio: it.ratio, fee: it.feeDelta, remark: `调整项 ${x.adjustmentId}（${x.type}）` })));
      return { fileName: `${s.settlementId}.csv`, csv: csv(cols, [...s.details, ...adj]) };
    }),
  };
  const disputes = {
    list: (f = {}) => g.call('operator.disputes.list', ['dispute.submit.own', 'settlement.view.own'], a => {
      const op = own(a, f.operatorId);
      return db().settlements.disputes.filter(d => d.operatorId === op).sort((x, y) => ms(y.submittedAt) - ms(x.submittedAt)).map(d => ({ ...clone(d), submittedByName: D.actorName(d.submittedBy), decidedByName: D.actorName(d.decidedBy) }));
    }),
    /** 对时长、里程提异议；价格不受理（BR-16）。payload: {settlementId, orderIds, field, claim} */
    submit: ({ settlementId, orderIds = [], field, claim } = {}) => g.call('operator.disputes.submit', 'dispute.submit.own', a => {
      const op = own(a);
      if (!['时长', '里程'].includes(field)) throw new ApiError('PRICE_NOT_DISPUTABLE');
      const s = ctx.get(db().settlements.settlements, 'settlementId', settlementId, '结算单');
      own(a, s.operatorId);
      if (!orderIds.length) throw new ApiError('INVALID', '请选择有异议的订单');
      const inS = new Set([...s.details.map(d => d.orderId), ...s.adjustments.flatMap(x => x.items.map(i => i.orderId))]);
      for (const id of orderIds) { const o = D.order(id); if (!o) throw new ApiError('NOT_FOUND', `没有找到订单 ${id}`); own(a, o.operatorId); if (!inS.has(id)) throw new ApiError('INVALID', `订单 ${id} 不在该结算单中`); }
      if (String(claim || '').trim().length < 6) throw new ApiError('INVALID', '请写明你方的记录与理由（至少 6 个字）');
      const t = ctx.now();
      const cmp = A.compareDispute(field, orderIds, claim);
      const d = { disputeId: ctx.ids.dispute(t), operatorId: op, settlementId, orderIds: [...orderIds], field, operatorClaim: String(claim).trim(), platformFact: orderIds.map(id => { const o = D.order(id); return `${id} 平台记录 ${o.billedMinutes} 分钟、${o.distanceKm} 公里`; }).join('；'), agentComparison: cmp.text, agentSuggestion: cmp.suggestion, status: '待核实', submittedAt: t, submittedBy: a.accountId, decidedAt: null, decidedBy: null, adjustmentId: null };
      db().settlements.disputes.push(d);
      for (const id of orderIds) D.order(id).disputeIds.push(d.disputeId);
      ctx.dirty('settlements', 'orders');
      ctx.emit('dispute.changed', { disputeId: d.disputeId, operatorId: op });
      return clone(d);
    }, { write: true }),
  };

  /* ---------------- 接入：文档、联调、符合性测试、验收（R-36～R-41） ---------------- */
  const integ = {
    /** 本运营商各品牌的接入记录 */
    get: () => g.call('operator.integration.get', ['acceptance.view.own', 'access.view.own', 'integration.sandbox.own'], a => {
      const op = own(a);
      return db().integration.integrations.filter(i => i.operatorId === op).map(i => ({ ...clone(i), brandName: D.brand(i.brandId).name, latestRun: latestRun(i) }));
    }),
    spec: () => g.call('operator.integration.spec', ['integration.doc', 'acceptance.view.own', 'access.view.own'], () => clone(db().integration.spec)),
    sandbox: () => g.call('operator.integration.sandbox', 'integration.sandbox.own', a => {
      const op = own(a);
      return { ...clone(db().integration.sandbox), paymentMode: mockPayMode(op), credentials: db().integration.integrations.filter(i => i.operatorId === op).map(i => ({ brandId: i.brandId, brandName: D.brand(i.brandId).name, ...clone(i.credentials) })) };
    }),
    runs: ({ brandId } = {}) => g.call('operator.integration.runs', ['integration.conformance.own', 'acceptance.view.own', 'ops.monitor'], a => {
      const op = own(a);
      return db().integration.conformanceRuns.filter(r => { const i = db().integration.integrations.find(x => x.integrationId === r.integrationId); return (!op || i.operatorId === op) && (!brandId || r.brandId === brandId); }).sort((x, y) => ms(y.at) - ms(x.at)).map(clone);
    }),
    /** 发起一轮符合性测试（18 项）。上一轮失败的用例按修改后重跑 */
    startRun: brandId => g.call('operator.integration.startRun', 'integration.conformance.own', a => {
      const op = own(a);
      const i = db().integration.integrations.find(x => x.brandId === brandId);
      if (!i) throw new ApiError('NOT_FOUND', '没有找到该品牌的接入记录');
      own(a, i.operatorId);
      if (['已退出', '未接入'].includes(i.status)) throw new ApiError('CONFLICT', `品牌${i.status}，不能发起测试`);
      if (i.credentials.status !== '有效') throw new ApiError('CREDENTIAL_DISABLED', '联调凭证已吊销，不能发起符合性测试');
      const prev = latestRun(i);
      const t = ctx.now();
      const forceFail = ctx.takeInjection('conformance.fail');
      const cases = clone(prev ? prev.cases : db().integration.conformanceRuns[0].cases).map((c, k) => {
        const fail = forceFail && k === 8;
        if (fail) return { ...c, result: '失败', failReason: '开锁结果在请求后 41 秒才到达，规范要求 30 秒内', suggestion: '规范允许先回"已受理"，车辆确认后再报结果；见接入规范第 3.2 节参考报文。', suggestedBy: 'AC0903', durationMs: 3000 + ctx.randInt(0, 1500) };
        const x = { caseId: c.caseId, group: c.group, name: c.name, result: '通过', durationMs: 900 + ctx.randInt(0, 3500) };
        return x;
      });
      const n = db().integration.conformanceRuns.filter(r => r.integrationId === i.integrationId).length + 1;
      const run = { runId: `${i.integrationId}-R${String(n).padStart(3, '0')}`, integrationId: i.integrationId, brandId, at: t, env: '联调环境', summary: { total: cases.length, passed: cases.filter(c => c.result === '通过').length, failed: cases.filter(c => c.result === '失败').length }, cases };
      db().integration.conformanceRuns.push(run);
      i.runIds.push(run.runId);
      if (i.acceptance) { i.acceptance.conformanceRunId = run.runId; if (i.acceptance.result !== '通过') i.acceptance.summary = run.summary.failed ? `符合性测试 ${run.summary.total} 项中 ${run.summary.passed} 项通过，${run.summary.failed} 项未通过；接入智能体已给出修改建议。` : `符合性测试 ${run.summary.total} 项全部通过，可进行真车联调（${D.params().acceptance.realVehicles} 辆、${D.params().acceptance.realOrders} 单）。`; }
      ctx.dirty('integration');
      ctx.emit('integration.changed', { integrationId: i.integrationId });
      return clone(run);
    }, { write: true }),
    /** 提交真车联调结果，接入智能体生成验收报告，交接入负责人确认（BR-37） */
    submitRealRides: brandId => g.call('operator.integration.submitRealRides', 'integration.conformance.own', a => {
      const i = db().integration.integrations.find(x => x.brandId === brandId);
      if (!i) throw new ApiError('NOT_FOUND', '没有找到该品牌的接入记录');
      own(a, i.operatorId);
      const run = latestRun(i);
      if (!run || run.summary.failed) throw new ApiError('ACCEPTANCE_NOT_READY', '符合性测试全部通过后才能提交真车联调结果');
      if (i.status !== '联调中') throw new ApiError('CONFLICT', `当前接入状态为${i.status}`);
      const P = D.params().acceptance;
      const t = ctx.now();
      const n = (i.acceptance && i.acceptance.reportId ? +i.acceptance.reportId.slice(-2) : 0) + 1;
      i.acceptance = { ...(i.acceptance || {}), reportId: `${i.integrationId}-AR${String(n).padStart(2, '0')}`, realVehiclesRequired: P.realVehicles, realVehiclesRidden: P.realVehicles, realOrders: P.realOrders, matchedOrders: P.realOrders, lockReportsMatched: P.realOrders, timeLimitOk: true, conformanceRunId: run.runId, submittedAt: t, submittedBy: 'AC0903', result: '待确认', confirmedBy: null, confirmedAt: null, summary: `符合性测试 ${run.summary.total} 项全部通过；真车联调 ${P.realVehicles} 辆、实骑 ${P.realOrders} 单，订单与锁车报告全部匹配，开锁结果时限达标。验收报告已提交接入负责人。` };
      ctx.dirty('integration');
      ctx.emit('integration.changed', { integrationId: i.integrationId });
      return clone(i);
    }, { write: true }),
  };
  /** 轮换联调密钥：旧密钥立即失效，新密钥只在返回值里出现一次（secret） */
  integ.rotateCredential = (brandId, reason) => g.call('operator.integration.rotateCredential', 'integration.sandbox.own', a => {
    const why = ctx.reason(reason);
    const i = ownInteg(a, brandId);
    if (i.credentials.status !== '有效') throw new ApiError('CREDENTIAL_DISABLED');
    const hex = n => Array.from({ length: n }, () => '0123456789abcdef'[ctx.randInt(0, 15)]).join('');
    const secret = `sk_${i.brandId.toLowerCase()}_${hex(28)}`;
    const t = ctx.now();
    Object.assign(i.credentials, { sandboxKeyMasked: `sk_****${secret.slice(-4)}`, issuedAt: t, rotatedAt: t, rotatedBy: a.accountId });
    ctx.audit({ action: '轮换联调凭证', targetType: 'integration', targetId: i.integrationId, summary: `${D.brand(i.brandId).name} 联调密钥轮换，旧密钥失效`, reason: why });
    ctx.dirty('integration'); ctx.emit('integration.changed', { integrationId: i.integrationId });
    return { secret, credentials: clone(i.credentials) };
  }, { write: true });
  /** 吊销联调凭证：立即停用，联调请求与符合性测试均被拒绝；恢复须平台公司接入负责人重新签发 */
  integ.revokeCredential = (brandId, reason) => g.call('operator.integration.revokeCredential', 'integration.sandbox.own', a => {
    const why = ctx.reason(reason);
    const i = ownInteg(a, brandId);
    if (i.credentials.status !== '有效') throw new ApiError('ALREADY_DONE', '该凭证已停用');
    Object.assign(i.credentials, { status: '停用', disabledAt: ctx.now(), disabledBy: a.accountId });
    ctx.audit({ action: '吊销联调凭证', targetType: 'integration', targetId: i.integrationId, summary: `${D.brand(i.brandId).name} 联调凭证 ${i.credentials.sandboxAppId} 已吊销`, reason: why });
    ctx.dirty('integration'); ctx.emit('integration.changed', { integrationId: i.integrationId });
    return { credentials: clone(i.credentials) };
  }, { write: true });
  /** 联调环境模拟支付：mode 为 '正常'（全部成功）或 '余额不足'（扣款返回失败，测欠费路径）；只影响本运营商的联调环境 */
  integ.setMockPayment = (mode, reason) => g.call('operator.integration.setMockPayment', 'integration.sandbox.own', a => {
    const op = own(a);
    if (!['正常', '余额不足'].includes(mode)) throw new ApiError('INVALID', '模拟支付只能是"正常"或"余额不足"');
    const sb = db().integration.sandbox;
    sb.paymentModes = { ...(sb.paymentModes || {}), [op]: mode };
    ctx.audit({ action: '设置联调环境', targetType: 'integration', targetId: op, summary: `${D.operator(op).shortName} 联调环境模拟支付切换为"${mode}"`, reason: reason || null });
    ctx.dirty('integration');
    return { paymentMode: mode };
  }, { write: true });
  function mockPayMode(op) { return ((db().integration.sandbox.paymentModes || {})[op]) || '正常'; }
  function ownInteg(a, brandId) {
    const i = db().integration.integrations.find(x => x.brandId === brandId);
    if (!i) throw new ApiError('NOT_FOUND', '没有找到该品牌的接入记录');
    own(a, i.operatorId);
    return i;
  }
  function latestRun(i) { return db().integration.conformanceRuns.filter(r => r.integrationId === i.integrationId).sort((x, y) => ms(y.at) - ms(x.at))[0] || null; }

  return { operator: { vehicles, orders, settlements, disputes, integration: integ } };
}

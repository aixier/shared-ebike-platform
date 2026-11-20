// 平台公司运维：运行监控、降级与限额、恢复闭合、暂停可租、接入验收、告警（R-31～R-35、R-40～R-43）。
import { ApiError } from './errors.js';
import { C, Y, ms, addSec, paginate, clone, inPolygon, centroid } from './util.js';
import * as R from './rules.js';

export function createOpsApi(ctx, D, V, A, g) {
  const db = () => ctx.db;
  const O = () => db().ops;

  const eventView = ev => ({ ...clone(ev), enteredByName: D.actorName(ev.enteredBy), timeline: ev.timeline.map(t => ({ ...t, byName: D.actorName(t.by), byKind: D.actorKind(t.by) })) });
  const closureRow = o => ({ orderId: o.orderId, userId: o.userId, userPhoneMasked: D.user(o.userId).phoneMasked, plateNo: (D.vehicle(o.vehicleId) || {}).plateNo, brandName: D.brand(o.brandId).name, status: o.status, requestAt: o.requestAt, startAt: o.startAt, endAt: o.endAt, billedMinutes: o.billedMinutes, receivable: o.fee ? o.fee.receivable : null, payStatus: o.payStatus, admission: o.degraded.admission, limitMinutes: o.degraded.limitMinutes, limitAmount: o.degraded.limitAmount, overLimitRemindedAt: o.degraded.overLimitRemindedAt, closure: o.degraded.closure, eventId: o.degraded.eventId, userClaimedEndAt: o.userClaimedEndAt, anomalyNote: o.anomalyNote });

  /** 运营商不可用时暂停可租（R-43）；by 为运行智能体或人工 */
  function pauseBrand(brandId, { by, metric = '人工', value = null, note }) {
    const b = ctx.get(db().operators.brands, 'brandId', brandId, '品牌');
    if (b.accessStatus !== '已接入') throw new ApiError('CONFLICT', `品牌当前为${b.accessStatus}，不能暂停可租`);
    const t = ctx.now(), P = D.params().pauseThreshold;
    b.accessStatus = '暂停可租'; b.statusSince = t;
    const ps = { id: ctx.ids.pause(t), brandId, pausedAt: t, resumedAt: null, metric, value, threshold: metric === '开锁成功率' ? P.unlockSuccessRate : metric === '可用率' ? P.availability : null, sustainMinutes: metric === '人工' ? null : P.sustainMinutes, by, note };
    O().pauses.push(ps);
    O().current.pausedBrandIds = [...new Set([...(O().current.pausedBrandIds || []), brandId])];
    const integ = db().integration.integrations.find(i => i.brandId === brandId);
    if (integ) { integ.status = '暂停可租'; integ.statusSince = t; integ.statusHistory.push({ status: '暂停可租', at: t, by, note }); }
    ctx.audit({ action: '暂停可租', targetType: 'brand', targetId: brandId, accountId: by, summary: `${b.name} 暂停可租${value != null ? `（${metric} ${(value * 100).toFixed(1)}%）` : ''}`, reason: note });
    D.alert({ level: '警告', type: '暂停可租', text: `${b.name} 已暂停可租：${note}`, brandId });
    ctx.dirty('operators', 'ops', 'integration');
    ctx.emit('access.changed', { brandId, accessStatus: '暂停可租' });
    return ps;
  }
  function resumeBrand(brandId, { by, note }) {
    const b = ctx.get(db().operators.brands, 'brandId', brandId, '品牌');
    if (b.accessStatus !== '暂停可租') throw new ApiError('CONFLICT', '该品牌未处于暂停可租');
    const t = ctx.now();
    b.accessStatus = '已接入'; b.statusSince = t;
    const ps = O().pauses.filter(p => p.brandId === brandId && !p.resumedAt).pop();
    if (ps) ps.resumedAt = t;
    O().current.pausedBrandIds = (O().current.pausedBrandIds || []).filter(x => x !== brandId);
    const integ = db().integration.integrations.find(i => i.brandId === brandId);
    if (integ) { integ.status = '已接入'; integ.statusSince = t; integ.statusHistory.push({ status: '已接入', at: t, by, note }); }
    ctx.audit({ action: '恢复可租', targetType: 'brand', targetId: brandId, accountId: by, summary: `${b.name} 恢复可租，回到已接入`, reason: note });
    ctx.dirty('operators', 'ops', 'integration');
    ctx.emit('access.changed', { brandId, accessStatus: '已接入' });
    return ps;
  }
  D.pauseBrand = pauseBrand; D.resumeBrand = resumeBrand;
  const sgView = x => ({ ...clone(x), brandName: D.brand(x.brandId).name, byName: D.actorName(x.by), decidedByName: D.actorName(x.decidedBy) });

  const degradeStatus = () => {
    const cur = O().current, ev = D.activeEvent();
    const v = D.currentVersion();
    const open = ev ? ev.orderIds.map(D.order).filter(o => o && o.status === '骑行中') : [];
    const amount = ev ? ev.orderIds.map(D.order).filter(Boolean).reduce((s, o) => s + (o.fee ? C(o.fee.receivable) : o.status === '骑行中' ? R.rideFeeC(D.versionsById()[o.pricingVersionId], R.billedMinutes((ctx.nowMs() - ms(o.startAt)) / 1000)) : 0), 0) : 0;
    return {
      degraded: !!cur.degraded, tightened: !!cur.tightened, stopped: !!cur.stopped, cause: cur.cause, components: cur.components || [], eventId: cur.eventId, lastDegradeEventId: cur.lastDegradeEventId,
      event: ev ? eventView(ev) : null, minutes: ev ? Math.round((ctx.nowMs() - ms(ev.enteredAt)) / 60000) : 0,
      usage: { lastHourOrders: D.hourCount(), hourlyLimit: v.riskLimits.hourlyDegradedOrders, orders: ev ? ev.orderIds.length : 0, riding: open.length, amountSoFar: Y(amount), overLimitRiding: open.filter(o => o.degraded.overLimitRemindedAt).length },
      limits: clone(v.riskLimits), limitsVersionId: v.versionId,
    };
  };

  return {
    /** 运行监控：开锁成功率、各品牌可用率、平台指标、降级状态（指标为运行智能体上报的序列，见假设 A6） */
    monitor: ({ hours = 72, days = 30 } = {}) => g.call('ops.monitor', ['ops.monitor', 'metrics.unlockRate.view'], a => {
      const n = ctx.nowMs();
      if (!g.can(a, 'ops.monitor')) { // 只读开锁成功率（主管部门）：不返回降级、告警、可用率等其他运维信息
        return { asOf: ctx.now(), limited: true, brands: db().operators.brands.filter(b => ['已接入', '暂停可租'].includes(b.accessStatus)).map(b => {
          const last = [...O().metricsHourly].filter(m => m.brandId === b.brandId && m.unlockSuccessRate != null).sort((x, y) => ms(y.at) - ms(x.at))[0] || null;
          return { brandId: b.brandId, name: b.name, displayName: b.displayName, accessStatus: b.accessStatus, latest: last ? { at: last.at, unlockSuccessRate: last.unlockSuccessRate } : null };
        }), hourly: O().metricsHourly.filter(m => ms(m.at) >= n - hours * 3600e3).map(m => ({ at: m.at, brandId: m.brandId, unlockSuccessRate: m.unlockSuccessRate })) };
      }
      const hourly = O().metricsHourly.filter(m => ms(m.at) >= n - hours * 3600e3);
      const daily = O().metricsDaily.filter(m => ms(m.date + ' 00:00:00') >= n - days * 864e5);
      const today = ctx.now().slice(0, 10);
      const todays = db().orders.filter(o => o.requestAt.slice(0, 10) === today);
      const brands = db().operators.brands.filter(b => ['已接入', '暂停可租'].includes(b.accessStatus)).map(b => {
        const last = [...O().metricsHourly].filter(m => m.brandId === b.brandId).sort((x, y) => ms(y.at) - ms(x.at))[0] || null;
        const vs = db().vehicles.filter(v => v.brandId === b.brandId);
        return { ...V.brand(b), latest: last, rentableNow: vs.filter(D.rentable).length, vehicles: vs.length, pause: O().pauses.filter(p => p.brandId === b.brandId && !p.resumedAt).pop() || null };
      });
      return {
        asOf: ctx.now(), degrade: degradeStatus(), brands, hourly, daily, platformDaily: O().platformDaily.filter(m => ms(m.date + ' 00:00:00') >= n - days * 864e5),
        today: { requests: todays.length, unlockFailed: todays.filter(o => o.status === '开锁失败').length, unlockSuccessRate: todays.length ? Y(Math.round(10000 * todays.filter(o => o.status !== '开锁失败').length / todays.length)) / 100 : null, riding: db().orders.filter(o => o.status === '骑行中').length },
        thresholds: { pause: D.params().pauseThreshold, availabilityTarget: D.params().availabilityTarget, unlockP90TargetSec: D.params().unlockP90TargetSec, billPushTargetSec: D.params().billPushTargetSec },
        alerts: O().alerts.filter(x => !x.ackBy).sort((a, b) => ms(b.at) - ms(a.at)).slice(0, 20),
        suspendSuggestions: (O().suspendSuggestions || []).filter(x => x.status === '待确认').map(sgView),
        autoPauseByAgent: !!D.params().autoPauseByAgent,
      };
    }),
    degrade: {
      status: () => g.call('ops.degrade.status', ['ops.monitor', 'degrade.closure.view'], () => degradeStatus()),
      events: () => g.call('ops.degrade.events', ['ops.monitor', 'degrade.closure.view'], () => [...O().degradeEvents].sort((a, b) => ms(b.enteredAt) - ms(a.enteredAt)).map(eventView)),
      event: id => g.call('ops.degrade.event', ['ops.monitor', 'degrade.closure.view'], () => { const ev = ctx.get(O().degradeEvents, 'eventId', id, '降级事件'); return { ...eventView(ev), orders: ev.orderIds.map(D.order).filter(Boolean).map(closureRow) }; }),
      /** 收紧：暂停先骑后验新用户与押金状态未知老用户的降级开单（值班人） */
      tighten: reason => g.call('ops.degrade.tighten', 'degrade.control', a => control(a, 'tighten', reason), { write: true }),
      /** 停止开新单（值班人） */
      stop: reason => g.call('ops.degrade.stop', 'degrade.control', a => control(a, 'stop', reason), { write: true }),
      /** 取消收紧或停止，恢复正常降级开单（值班人） */
      relax: reason => g.call('ops.degrade.relax', 'degrade.control', a => control(a, 'relax', reason), { write: true }),
    },
    closure: {
      /** 降级单逐单闭合情况；eventId 缺省为全部事件中待人工的单 */
      list: ({ eventId, status } = {}) => g.call('ops.closure.list', ['degrade.closure.view', 'degrade.closure.handle'], () => {
        const os = db().orders.filter(o => o.degraded && (!eventId || o.degraded.eventId === eventId) && (!status || (o.degraded.closure && o.degraded.closure.status === status)) && (eventId || status || (o.degraded.closure && o.degraded.closure.status !== '已闭合')));
        return os.sort((a, b) => ms(b.requestAt) - ms(a.requestAt)).map(closureRow);
      }),
      /**
       * 人工处理缺项的降级单（R-34、BR-32）：
       * action='补录锁车事实'：{endAt, stationId?} 按补录时刻计价并收款；
       * action='确认收款结果'：{result:'已收款'|'未收款'} 对渠道结果未知的扣款落定
       */
      handle: (orderId, { action, endAt, stationId, result } = {}, reason) => g.call('ops.closure.handle', 'degrade.closure.handle', a => {
        const why = ctx.reason(reason);
        const o = ctx.get(db().orders, 'orderId', orderId, '订单');
        if (!o.degraded || !o.degraded.closure || o.degraded.closure.status === '已闭合') throw new ApiError('CONFLICT', '该订单不需要人工闭合');
        const t = ctx.now();
        if (action === '补录锁车事实') {
          if (!['待人工处理', '骑行中'].includes(o.status)) throw new ApiError('CONFLICT', '该订单已有锁车事实');
          const end = endAt || o.userClaimedEndAt;
          if (!end || ms(end) < ms(o.startAt) || ms(end) > ctx.nowMs()) throw new ApiError('INVALID', '锁车时刻须在开锁之后、现在之前');
          const st = stationId ? D.station(stationId) : null;
          const v = D.vehicle(o.vehicleId);
          const loc = st ? (inPolygon(st.center, st.fence) ? st.center : centroid(st.fence)) : v.location;
          const r = R.decideReturn({ stations: db().stations.stations, piles: db().stations.piles, location: loc, method: 'lock' });
          o.forceEnd = { at: t, billedEndAt: end, by: a.accountId, reason: why, ticketId: null };
          o.status = '骑行中';
          D.finishRide(o, { endAt: end, lockReportAt: t, reportReceivedAt: t, endMethod: '客服强制结束', location: loc, stationId: r.station ? r.station.stationId : null, inStation: r.inStation, basis: r.basis, distanceToStationM: r.distanceToStationM, lateReport: true });
        } else if (action === '确认收款结果') {
          const p = db().payments.payments.find(x => x.orderId === orderId && x.status === '处理中');
          if (!p) throw new ApiError('CONFLICT', '该订单没有结果未知的扣款');
          if (result === '已收款') {
            Object.assign(p, { status: '成功', completedAt: t, channelSettledDate: t.slice(0, 10), channelTradeNo: ctx.ids.tradeNo(p.channel, t), note: `渠道核实已扣款（${why}）` });
            o.fee.channelPaid = Y(C(o.fee.channelPaid) + C(p.amount)); R.settleFee(o.fee); o.payStatus = '已支付';
            Object.assign(o.degraded.closure, { payment: '已收款', status: '已闭合', missing: [], closedAt: t, note: `人工核实：${why}` });
          } else if (result === '未收款') {
            Object.assign(p, { status: '失败', failReason: '渠道核实未扣款', note: why });
            o.payStatus = '欠费'; D.recomputeArrears(D.user(o.userId));
            Object.assign(o.degraded.closure, { payment: '已转欠费', status: '已闭合', missing: [], closedAt: t, note: `人工核实：${why}` });
          } else throw new ApiError('INVALID', '请选择收款结果：已收款或未收款');
          ctx.dirty('payments', 'orders', 'users');
        } else throw new ApiError('INVALID', '处理方式只能是补录锁车事实或确认收款结果');
        const ev = D.event(o.degraded.eventId);
        if (ev) {
          D.refreshEventClosure(ev);
          if (!ev.closure.pendingOrderIds.length && ev.status === '已退出') ev.timeline.push({ at: t, by: a.accountId, action: '全部闭合', text: `人工处理后，降级单 ${ev.orderCount} 笔全部闭合` });
        }
        ctx.audit({ action: '闭合订单', targetType: 'order', targetId: orderId, summary: `降级单 ${orderId} 人工${action}${result ? `：${result}` : ''}`, reason: why });
        ctx.dirty('ops', 'orders');
        return closureRow(o);
      }, { write: true }),
    },
    alerts: {
      list: (f = {}) => g.call('ops.alerts.list', ['ops.monitor', 'degrade.control'], () => paginate([...O().alerts].filter(x => (!f.level || x.level === f.level) && (f.acked == null || !!x.ackBy === !!f.acked)).sort((a, b) => ms(b.at) - ms(a.at)).map(x => ({ ...x, ackByName: D.actorName(x.ackBy) })), f)),
      ack: alertId => g.call('ops.alerts.ack', ['ops.monitor', 'degrade.control'], a => {
        const x = ctx.get(O().alerts, 'alertId', alertId, '告警');
        if (x.ackBy) throw new ApiError('ALREADY_DONE', '该告警已确认');
        x.ackBy = a.accountId; x.ackAt = ctx.now();
        ctx.dirty('ops');
        return clone(x);
      }, { write: true }),
    },
    pauses: () => g.call('ops.pauses', ['ops.monitor', 'access.view', 'access.view.own'], a => [...O().pauses].filter(p => !a.operatorScope || D.brand(p.brandId).operatorId === a.operatorScope).sort((x, y) => ms(y.pausedAt) - ms(x.pausedAt)).map(p => ({ ...p, brandName: D.brand(p.brandId).name, byName: D.actorName(p.by) }))),
    /** 运行智能体的暂停可租建议（R-43）：status 待确认 | 已确认 | 已驳回 | 已失效（指标已恢复） */
    suspendSuggestions: ({ status } = {}) => g.call('ops.suspendSuggestions', ['ops.monitor', 'access.pause'], () => [...(O().suspendSuggestions || [])].filter(x => !status || x.status === status).sort((a, b) => ms(b.at) - ms(a.at)).map(sgView)),
    /** 运维确认建议：品牌暂停可租 */
    confirmSuspend: (suggestionId, reason) => g.call('ops.confirmSuspend', 'access.pause', a => {
      const why = ctx.reason(reason);
      const sg = ctx.get(O().suspendSuggestions, 'id', suggestionId, '暂停建议');
      if (sg.status !== '待确认') throw new ApiError('ALREADY_DONE', `该建议已${sg.status}`);
      const ps = pauseBrand(sg.brandId, { by: a.accountId, metric: sg.metric, value: sg.value, note: `确认运行智能体建议：${sg.note}（${why}）` });
      Object.assign(sg, { status: '已确认', decidedBy: a.accountId, decidedAt: ctx.now(), decisionReason: why, pauseId: ps.id });
      ctx.dirty('ops');
      return sgView(sg);
    }, { write: true }),
    /** 运维驳回建议：品牌保持已接入 */
    dismissSuspend: (suggestionId, reason) => g.call('ops.dismissSuspend', 'access.pause', a => {
      const why = ctx.reason(reason);
      const sg = ctx.get(O().suspendSuggestions, 'id', suggestionId, '暂停建议');
      if (sg.status !== '待确认') throw new ApiError('ALREADY_DONE', `该建议已${sg.status}`);
      Object.assign(sg, { status: '已驳回', decidedBy: a.accountId, decidedAt: ctx.now(), decisionReason: why });
      ctx.audit({ action: '驳回暂停建议', targetType: 'brand', targetId: sg.brandId, summary: `驳回运行智能体对 ${D.brand(sg.brandId).name} 的暂停可租建议`, reason: why });
      ctx.dirty('ops');
      return sgView(sg);
    }, { write: true }),
    /** 自动暂停开关（平台公司参数 autoPauseByAgent，默认关闭）：开启后指标低于门槛由运行智能体直接暂停 */
    setAutoSuspend: (on, reason) => g.call('ops.setAutoSuspend', 'access.pause', () => {
      const why = ctx.reason(reason);
      const it = db().pricing.platformParams.items;
      const before = !!it.autoPauseByAgent;
      it.autoPauseByAgent = !!on;
      db().pricing.platformParams.updatedAt = ctx.now(); db().pricing.platformParams.updatedBy = ctx.actor();
      ctx.audit({ action: '设置自动暂停', targetType: 'pricing', targetId: 'platformParams', summary: `运行智能体自动暂停可租：${before ? '开启' : '关闭'} → ${on ? '开启' : '关闭'}`, reason: why, before: { autoPauseByAgent: before }, after: { autoPauseByAgent: !!on } });
      ctx.dirty('pricing');
      return { autoPauseByAgent: !!on };
    }, { write: true }),
    pause: (brandId, reason) => g.call('ops.pause', 'access.pause', a => pauseBrand(brandId, { by: a.accountId, note: ctx.reason(reason) }), { write: true }),
    resume: (brandId, reason) => g.call('ops.resume', 'access.pause', a => resumeBrand(brandId, { by: a.accountId, note: ctx.reason(reason) }), { write: true }),
    acceptance: {
      list: () => g.call('ops.acceptance.list', ['ops.monitor', 'acceptance.confirm'], () => db().integration.integrations.map(i => ({ ...clone(i), brandName: D.brand(i.brandId).name, operatorName: D.operator(i.operatorId).shortName, latestRun: db().integration.conformanceRuns.filter(r => r.integrationId === i.integrationId).sort((x, y) => ms(y.at) - ms(x.at))[0] || null, elapsedDays: i.startedAt ? Math.floor((ctx.nowMs() - ms(i.startedAt)) / 864e5) : null, suspendSuggestion: (() => { const x = (O().suspendSuggestions || []).find(y => y.brandId === i.brandId && y.status === '待确认'); return x ? sgView(x) : null; })() }))),
      /** 接入负责人确认验收通过：品牌上线、码启用、车辆可租（R-40、R-41、BR-37） */
      confirm: (integrationId, reason) => g.call('ops.acceptance.confirm', 'acceptance.confirm', a => {
        const why = ctx.reason(reason);
        const i = ctx.get(db().integration.integrations, 'integrationId', integrationId, '接入记录');
        const ac = i.acceptance;
        if (!ac || ac.result !== '待确认') throw new ApiError('ACCEPTANCE_NOT_READY', '验收报告尚未提交，或已确认');
        const t = ctx.now();
        Object.assign(ac, { result: '通过', confirmedBy: a.accountId, confirmedAt: t });
        i.status = '已接入'; i.statusSince = t;
        i.statusHistory.push({ status: '已接入', at: t, by: a.accountId, note: `验收通过，上线（${ac.reportId}）` });
        i.credentials.prodAppId = i.credentials.prodAppId || `prod_${i.brandId.toLowerCase()}_${String(ctx.randInt(100000, 999999))}`;
        const b = D.brand(i.brandId);
        b.accessStatus = '已接入'; b.statusSince = t;
        let n = 0;
        for (const v of db().vehicles.filter(x => x.brandId === i.brandId && x.codeStatus === '待启用')) { Object.assign(v, { codeStatus: '已启用', codeEnableAt: null, status: v.status === '码未启用' ? '可用' : v.status }); n++; }
        ctx.audit({ action: '确认验收', targetType: 'integration', targetId: integrationId, summary: `${b.name} 验收通过并上线，${n} 辆车政府码启用`, reason: why });
        ctx.dirty('integration', 'operators', 'vehicles');
        ctx.emit('access.changed', { brandId: i.brandId, accessStatus: '已接入' });
        return clone(i);
      }, { write: true }),
    },
  };

  function control(a, kind, reason) {
    const why = ctx.reason(reason);
    const ev = D.activeEvent();
    if (!ev) throw new ApiError('DEGRADE_NOT_ACTIVE');
    const cur = O().current, t = ctx.now();
    const map = {
      tighten: ['降级收紧', '暂停押金状态未知用户与先骑后验新用户的降级开单', { tightened: true }],
      stop: ['降级停止', '停止开新单，已开锁的订单不受影响', { stopped: true }],
      relax: ['降级放宽', '恢复正常降级开单', { tightened: false, stopped: false }],
    };
    const [action, text, patch] = map[kind];
    Object.assign(cur, patch);
    ev.timeline.push({ at: t, by: a.accountId, action, text: `${text}（${why}）` });
    ctx.audit({ action: ({ tighten: '收紧降级', stop: '停止降级', relax: '放宽降级' })[kind], targetType: 'degrade', targetId: ev.eventId, summary: `值班人${text}`, reason: why });
    ctx.dirty('ops');
    ctx.emit('degrade.changed', { degraded: true, eventId: ev.eventId, ...patch });
    return degradeStatus();
  }
}

// 系统接口：时间、降级开关（运行智能体信号）、品牌运行指标（运行智能体信号）、重置与调试。
// 页面上不得出现这些操作的入口；只有 ?debug=1 的调试面板调用。reset 与 debug.* 在非调试模式下拒绝。
import { ApiError } from './errors.js';
import { fmt, ms, clone } from './util.js';

export const DEGRADE_PRESETS = {
  支付通道故障: { cause: '支付通道故障（微信支付回调持续超时）', components: ['微信支付收款', '支付宝收款'] },
  实名核验不可用: { cause: '实名核验服务不可用（第三方核验接口连续超时）', components: ['实名核验'] },
  押金与免押查询不可用: { cause: '押金与免押查询服务不可用', components: ['押金与免押查询'] },
  授权不可用: { cause: '微信与支付宝授权接口不可用', components: ['微信授权', '支付宝授权'] },
};
export const INJECTION_KEYS = {
  'payment.decline': '下一笔渠道扣款/支付失败（余额不足）',
  'payment.unknown': '下一笔渠道扣款结果未知（处理中）',
  'channel.missing': '下一笔成功收款渠道未到账（对账差异）',
  'refund.fail': '下一笔原路退款失败（原支付账户已注销）',
  'deposit.refund.fail': '下一笔退押原路失败',
  'unlock.fail': '下一次开锁由运营商报告失败（params.reason：车辆无响应｜电量不足｜车辆故障｜车辆被占用）',
  'unlock.lateSuccess': '下一次开锁超时后才报告成功（BR-03，0 元）',
  'credit.low': '下一次开通免押信用分不足',
  'realname.mismatch': '下一次实名核验姓名与证件不一致',
  'conformance.fail': '下一轮符合性测试"开锁结果时限"失败',
};

export function createSystemApi(ctx, D, V, users) {
  const db = () => ctx.db;
  const dbg = () => { if (!ctx.debugOn) throw new ApiError('DEBUG_ONLY'); };
  const call = (name, fn, opts) => ctx.call('system.' + name, fn, { noRandomFail: true, latency: [0, 0], ...opts });
  const timeInfo = () => ({ now: ctx.now(), label: ctx.label(ctx.now()), shiftDays: ctx.shiftDays(), anchorMode: !!(ctx.meta && ctx.meta.anchorMode), clockOffsetMs: (ctx.meta && ctx.meta.clockOffsetMs) || 0, seededAt: ctx.meta && ctx.meta.seededAt });
  /** 降级操作人：显式 by；否则当前登录的管理端账号（须有降级权限）；都没有时为运行智能体 */
  const resolveActor = by => {
    if (by) { const a = D.account(by); if (!a) throw new ApiError('NOT_FOUND', '没有找到该操作人'); if (a.role !== '智能体' && !a.permissions.includes('degrade.control')) throw new ApiError('FORBIDDEN', '该账号没有降级操作权限'); return a.accountId; }
    const s = ctx.session.get('Admin');
    const a = s && D.account(s.accountId);
    return a && a.permissions.includes('degrade.control') ? a.accountId : 'AC0901';
  };
  const saveMeta = () => ctx.env.local.set('bike.meta', JSON.stringify(ctx.meta));

  const sys = {
    /** 当前时间（平移后的真实时间）与标签时间 */
    /** 业务时间心跳：到点（标签时间次月 1 日 00:30 起）且财务尚未手动生成时，自动出上月结算单 */
    time: () => call('time', () => { D.autoSettle(); return timeInfo(); }),
    /** 当前降级状态（公开信息，用户端可用来决定是否显示"费用将稍后收取"） */
    degrade: () => call('degrade', () => { const c = db().ops.current; return { active: !!c.degraded, eventId: c.eventId, cause: c.cause, components: c.components || [], tightened: !!c.tightened, stopped: !!c.stopped }; }),
    /**
     * 降级进入/退出（模拟运行智能体判断，D-27 自动进入、通知值班人）。
     * on=true 时需 cause 与 components（或 preset 名称）；on=false 时故障恢复、退出并逐单闭合。
     */
    setDegrade: ({ on, preset, cause, components, reason, by } = {}) => call('setDegrade', () => {
      const actor = resolveActor(by);
      if (on) {
        const p = preset ? DEGRADE_PRESETS[preset] : null;
        if (preset && !p) throw new ApiError('INVALID', `没有该故障预设：${preset}`);
        const ev = D.enterDegrade({ cause: cause || (p && p.cause) || '支付通道故障', components: components || (p && p.components) || ['微信支付收款', '支付宝收款'], by: actor, reason });
        return clone(ev);
      }
      return clone(D.exitDegrade({ by: actor, reason }));
    }),
    /** 品牌运行指标（模拟运行智能体上报）：低于门槛即暂停可租，恢复后自动回到已接入（R-43） */
    setBrandHealth: (brandId, { unlockSuccessRate, availability } = {}) => call('setBrandHealth', () => {
      const b = D.brand(brandId);
      if (!b) throw new ApiError('NOT_FOUND', '没有找到该品牌');
      const th = D.params().pauseThreshold;
      const low = (unlockSuccessRate != null && unlockSuccessRate < th.unlockSuccessRate) ? ['开锁成功率', unlockSuccessRate] : (availability != null && availability < th.availability) ? ['可用率', availability] : null;
      const t = ctx.now();
      db().ops.metricsHourly.push({ at: t, brandId, paused: !!low, unlockSuccessRate: unlockSuccessRate ?? null, availability: availability ?? null, rentableVehicles: db().vehicles.filter(D.rentable).filter(v => v.brandId === brandId).length });
      ctx.dirty('ops');
      const sgs = db().ops.suspendSuggestions = db().ops.suspendSuggestions || [];
      const pending = sgs.find(x => x.brandId === brandId && x.status === '待确认');
      if (low && b.accessStatus === '已接入') {
        const note = `${low[0]}持续低于门槛（${(low[1] * 100).toFixed(1)}%）`;
        if (D.params().autoPauseByAgent) return { action: '暂停可租', pause: D.pauseBrand(brandId, { by: 'AC0901', metric: low[0], value: low[1], note: `${note}，运行智能体判定暂停可租` }) };
        if (pending) { Object.assign(pending, { value: low[1], metric: low[0], updatedAt: t }); ctx.dirty('ops'); return { action: '已有待确认建议', suggestion: clone(pending) }; }
        const sg = { id: ctx.ids.suggestion(t), brandId, at: t, metric: low[0], value: low[1], threshold: low[0] === '开锁成功率' ? th.unlockSuccessRate : th.availability, sustainMinutes: th.sustainMinutes, by: 'AC0901', note: `${note}，建议暂停可租`, status: '待确认', decidedBy: null, decidedAt: null, decisionReason: null, pauseId: null };
        sgs.push(sg);
        D.alert({ level: '警告', type: '暂停可租建议', text: `运行智能体建议暂停 ${b.name}：${note}，请在接入验收页确认`, brandId });
        ctx.dirty('ops');
        ctx.emit('access.changed', { brandId, suggestion: sg.id });
        return { action: '建议暂停可租', suggestion: clone(sg) };
      }
      if (!low && pending) { Object.assign(pending, { status: '已失效', decidedBy: 'AC0901', decidedAt: t, decisionReason: '指标已恢复到门槛以上' }); ctx.dirty('ops'); }
      if (!low && b.accessStatus === '暂停可租') return { action: '恢复可租', pause: D.resumeBrand(brandId, { by: 'AC0901', note: '指标恢复到门槛以上，自动回到已接入' }) };
      return { action: '无变化', accessStatus: b.accessStatus };
    }),
    /** 重置全部数据（仅调试）。anchorMode=true 时固定在锚点日期 2026-12-01 12:00 */
    reset: ({ anchorMode } = {}) => ctx.call('system.reset', async () => { dbg(); await ctx.reset({ anchorMode }); return timeInfo(); }, { latency: [0, 0], noRandomFail: true }),
  };

  /* ---------------- 调试（仅 ?debug=1） ---------------- */
  sys.debug = {
    enabled: async () => ctx.debugOn,
    state: () => call('debug.state', () => { dbg(); return { ...ctx.debugState(), time: timeInfo(), keys: INJECTION_KEYS, presets: Object.keys(DEGRADE_PRESETS) }; }),
    /** 指定下一次某接口（如 'user.pay'，网络失败）或某业务注入点（见 INJECTION_KEYS）失败 */
    failNext: (key, opts = {}) => call('debug.failNext', () => {
      dbg();
      const s = ctx.debugState();
      s.failNext = s.failNext || {};
      s.failNext[key] = { times: opts.times || 1, code: opts.code || (INJECTION_KEYS[key] ? null : 'NETWORK'), message: opts.message || null, reason: opts.reason || null };
      ctx.setDebugState(s);
      return s.failNext;
    }),
    clearInjections: () => call('debug.clearInjections', () => { dbg(); const s = ctx.debugState(); s.failNext = {}; ctx.setDebugState(s); return {}; }),
    setRandomFailRate: rate => call('debug.setRandomFailRate', () => { dbg(); const s = ctx.debugState(); s.randomFailRate = Math.max(0, Math.min(0.5, +rate || 0)); ctx.setDebugState(s); return s; }),
    setLatencyScale: k => call('debug.setLatencyScale', () => { dbg(); const s = ctx.debugState(); s.latencyScale = Math.max(0, Math.min(10, +k || 1)); ctx.setDebugState(s); return s; }),
    /** 固定在锚点日期（重新载入数据，days=0；时钟从 2026-12-01 12:00:00 走起） */
    setAnchorMode: on => ctx.call('system.debug.setAnchorMode', async () => { dbg(); await ctx.reset({ anchorMode: !!on }); return timeInfo(); }, { latency: [0, 0], noRandomFail: true }),
    /** 时钟快进（分钟），用于演示一键支付期限、退押到账、降级超时提醒等 */
    advanceClock: minutes => call('debug.advanceClock', () => { dbg(); ctx.meta.clockOffsetMs = (ctx.meta.clockOffsetMs || 0) + Math.round(+minutes * 60e3); saveMeta(); return timeInfo(); }),
    /** 切换用户端当前用户 */
    switchUser: userId => call('debug.switchUser', () => {
      dbg();
      const u = D.user(userId);
      if (!u) throw new ApiError('NOT_FOUND', '没有找到该用户');
      ctx.session.set('User', { userId, channel: u.channel });
      return V.user(u);
    }),
    /** 强制降级：preset 为预设名，false 退出 */
    forceDegrade: preset => call('debug.forceDegrade', () => {
      dbg();
      if (preset === false) return clone(D.exitDegrade({ by: 'AC0901' }));
      const p = DEGRADE_PRESETS[preset || '支付通道故障'];
      if (!p) throw new ApiError('INVALID', '没有该故障预设');
      return clone(D.enterDegrade({ ...p, by: 'AC0901' }));
    }),
    /** 用户端推荐的演示用户 */
    demoUsers: () => call('debug.demoUsers', () => { dbg(); return DEMO_USERS.map(d => ({ ...d, phone: (D.user(d.userId) || {}).phone })); }),
  };
  return sys;
}

export const DEMO_USERS = [
  { userId: 'U100341', label: '老用户 · 信用免押 · 有月卡' },
  { userId: 'U100004', label: '老用户 · 缴押金 ¥99' },
  { userId: 'U100087', label: '新用户 · 已实名，未开通免押或押金' },
  { userId: 'U100127', label: '新用户 · 未实名' },
  { userId: 'U100036', label: '有欠费 ¥11.0' },
  { userId: 'U100712', label: '冻结 · 欠费追缴未结' },
  { userId: 'U101538', label: '冻结 · 先骑后验未通过（未满 16 周岁）' },
  { userId: 'U100026', label: '先骑后验 · 待核验' },
  { userId: 'U100054', label: '押金不足（被抵扣后未补缴）' },
  { userId: 'U101097', label: '退押中' },
];

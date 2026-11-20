// 岗位智能体在原型里的替身：客服智能体、对账智能体。
// 真实系统中这些是 LLM 驱动的智能体（读订单事实、给出提议，由人确认）；原型里按订单事实套用固定话术，
// 只用于让页面能走通"智能体提议 → 人确认"的流程，不代表规则本身。
import { C, Y, ms } from './util.js';

const hm = t => (t ? t.slice(11, 16) : '—');
const hms = t => (t ? t.slice(11, 19) : '—');

export function createAgents(ctx, D) {
  const A = {};
  const P = () => D.params();

  /** 客服智能体：根据用户留言与订单事实回复；必要时给出退款或强制结束提议 */
  A.csReply = (ticket, text) => {
    const o = ticket.orderId ? D.order(ticket.orderId) : null;
    const u = D.user(ticket.userId);
    const s = String(text || '') + ' ' + (ticket.category || '');
    const out = { text: '', proposal: null, forceEndProposal: null, category: null };
    if (/旧押金|原品牌|美团|哈啰|原来.*押金/.test(s)) {
      out.category = '旧押金咨询';
      out.text = `原品牌押金由原品牌退还，不转到这里。${u.deposit.mode === '免押' ? '你已开通免押，不用再交。' : '推荐开通信用免押，不用再交押金。'}`;
      return out;
    }
    if (o && o.status === '骑行中' && /锁|还车|结束|计时|还在/.test(s)) {
      out.category = '订单无法结束';
      const v = D.vehicle(o.vehicleId);
      const end = o.userClaimedEndAt || ctx.now();
      out.forceEndProposal = { billedEndAt: end, stationId: o.claimedStationId || v.stationId || null };
      const mins = Math.max(1, Math.ceil((ms(end) - ms(o.startAt)) / 60000));
      out.text = `订单 ${o.orderId} 还没有收到锁车报告。建议强制结束并按 ${hm(end)} 还车计费，约 ${mins} 分钟。待客服确认后会通知你。`;
      return out;
    }
    if (o && o.status === '开锁失败') {
      out.category = '开锁失败';
      out.text = `订单 ${o.orderId} 开锁失败（${o.unlockFailReason || '车辆没有响应'}），没有扣费。附近还有可用车辆，可以重新扫码。`;
      return out;
    }
    if (o && o.fee && C(o.fee.dispatchFee) > 0 && /调度|站点|站外|停在/.test(s)) {
      out.category = '调度费异议';
      const d = o.distanceToStationM;
      if (d != null && d <= 50) {
        out.proposal = { amount: o.fee.dispatchFee, kind: '部分退款', basis: `锁车位置距站点围栏仅 ${d} 米，可能为定位漂移，建议退还调度费` };
        out.text = `订单 ${o.orderId} 锁车位置距最近站点围栏 ${d} 米，车端判定在站外。距离很近，可能是定位漂移。建议退还调度费 ¥${o.fee.dispatchFee.toFixed(1)}，待客服确认。`;
      } else {
        out.proposal = { amount: 0, kind: '不退款', basis: `锁车位置在站点外 ${d ?? '—'} 米，按规则收取调度费` };
        out.text = `订单 ${o.orderId} 锁车位置在站点外${d != null ? ` ${d} 米` : ''}，按规则收取调度费 ¥${o.fee.dispatchFee.toFixed(1)}，下单前页面已提示。如有其他情况，客服会继续跟进。`;
      }
      return out;
    }
    if (o && o.fee && /费|贵|多|扣|退款|钱/.test(s)) {
      out.category = '费用异议';
      const f = o.fee;
      const parts = [`骑行 ${o.billedMinutes} 分钟，骑行费 ¥${f.rideFee.toFixed(1)}`];
      if (C(f.dispatchFee)) parts.push(`调度费 ¥${f.dispatchFee.toFixed(1)}`);
      if (C(f.monthCardDeduct)) parts.push(`月卡抵扣 ¥${f.monthCardDeduct.toFixed(1)}`);
      if (C(f.couponDeduct)) parts.push(`优惠券抵扣 ¥${f.couponDeduct.toFixed(1)}`);
      const late = o.lockReportAt && o.pileInAt && ms(o.lockReportAt) - ms(o.pileInAt) > 60000;
      if (late) {
        out.proposal = { amount: 0, kind: '不退款', basis: '按桩在位时刻结束计费，已是较早时刻' };
      } else if (o.inStationBasis === '桩满·围栏内' || o.inStationBasis === '无桩站点·车端判定') {
        out.proposal = { amount: 0, kind: '不退款', basis: '按运营商锁车报告时刻计费，需客服核实车辆是否提前停放' };
      } else {
        out.proposal = { amount: 0, kind: '不退款', basis: '计费与规则一致' };
      }
      out.text = `订单 ${o.orderId}：${parts.join('，')}，应付 ¥${f.payable.toFixed(1)}。按开锁时的价格规则计算。${out.proposal.basis}；如果你确实更早还车，客服会核实后处理。`;
      return out;
    }
    if (/退押|押金/.test(s)) {
      out.category = '退押';
      const d = u.deposit;
      out.text = d.mode === '押金'
        ? (d.status === '退押中' ? `押金 ¥${Number(d.refund.amount).toFixed(1)} 正在原路退回，预计 ${d.refund.expectedAt.slice(5, 16)} 前到账。` : `你的押金余额 ¥${Number(d.balance).toFixed(1)}，在"押金"页一键申请即可原路退回，预计 ${P().depositRefundWorkdays} 个工作日到账。有骑行中订单或欠费时需先处理。`)
        : '你当前是信用免押，没有在平台缴纳押金。';
      return out;
    }
    if (/冻结|解冻/.test(s)) {
      out.category = '账户冻结';
      out.text = u.account.status === '冻结' ? `账户冻结原因：${u.account.freeze.reason}。解冻方法：${u.account.freeze.unfreezeHow}。` : '你的账户状态正常。';
      return out;
    }
    if (/注销/.test(s)) { out.category = '注销'; out.text = '在"账户"页可申请注销；有欠费、骑行中订单或未退押金时，需先处理。'; return out; }
    if (/发票/.test(s)) { out.category = '开具发票'; out.text = '骑行发票由客服人工开具，请留下发票抬头与接收邮箱，客服会在 1 个工作日内处理。'; return out; }
    out.category = ticket.category || '费用咨询';
    out.text = '收到，已转给人工客服，稍后在这里回复你。';
    return out;
  };

  /** 对账智能体：比对运营商异议订单的事实，给出建议（由财务确认） */
  A.compareDispute = (field, orderIds, claim) => {
    const lines = [], verdicts = [];
    for (const id of orderIds) {
      const o = D.order(id);
      if (!o) continue;
      if (field === '里程') { lines.push(`${id} 平台记录 ${o.distanceKm ?? '—'} 公里`); verdicts.push('无需调整'); continue; }
      if (o.pileInAt && o.lockReportAt) { lines.push(`${id} 桩在位 ${hms(o.pileInAt)}、锁车报告 ${hms(o.lockReportAt)}`); verdicts.push('驳回'); }
      else { lines.push(`${id} 平台记录 ${o.billedMinutes} 分钟，结束时刻以运营商锁车报告 ${hms(o.lockReportAt)} 为准`); verdicts.push('待核实'); }
    }
    let suggestion = '待核实', tail = '运营商主张需结合车辆轨迹人工核实，待财务确认。';
    if (verdicts.length && verdicts.every(v => v === '无需调整')) { suggestion = '无需调整'; tail = '计价规则只按时长计费，里程差异不影响服务费基数，建议无需调整。'; }
    else if (verdicts.length && verdicts.every(v => v === '驳回')) { suggestion = '驳回'; tail = '均有桩在位记录且早于锁车报告，平台计时与规则一致，建议驳回。'; }
    return { text: `对账智能体比对：${lines.join('；')}。${tail}`, suggestion };
  };
  return A;
}

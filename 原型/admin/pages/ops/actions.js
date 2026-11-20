// 运维动作：暂停与恢复可租、降级进入与退出、收紧与停止。全部二次确认并必填事由。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $ } from './lib.js';

export const PRESETS = [
  ['支付通道故障', '微信或支付宝收款不可用'],
  ['实名核验不可用', '实名核验服务不可用，新用户先骑后验'],
  ['押金与免押查询不可用', '查不到用户押金或免押状态'],
  ['授权不可用', '微信或支付宝授权不可用，新登录受限'],
];

/** 暂停某品牌可租。返回 true 表示已执行 */
export async function pauseBrand(brand, { note = '' } = {}) {
  const ans = await ui.confirm({
    title: `暂停「${brand.name}」可租`, danger: true, confirmText: '确认暂停可租',
    message: `<p>暂停后，${esc(brand.name)}的车辆不能再被租用；用户扫码会看到说明和附近其他品牌的车，已在骑行的订单照常结束。</p>${note ? `<div class="ops-script">${note}</div>` : ''}<div class="ops-script">用户将看到：<b>【${esc(brand.name)}】电单车暂停可租，附近其他品牌的车可以骑。</b></div>`,
    impact: `${brand.name} 全部 ${brand.vehicles ?? brand.vehicleCount ?? ''} 辆车立即不可租；恢复后自动回到已接入`,
    reasons: ['开锁成功率持续低于门槛，按运行智能体建议暂停', '运营商通知车载通信故障，暂停可租等待修复'],
    onConfirm: async reason => { await api.admin.ops.pause(brand.brandId, reason); },
  });
  if (!ans) return false;
  ui.toast(`已暂停「${brand.name}」可租`, { type: 'ok', hint: '用户端扫码立即显示附近其他品牌的车' });
  return true;
}

export async function resumeBrand(brand, { note = '' } = {}) {
  const ans = await ui.confirm({
    title: `恢复「${brand.name}」可租`, danger: false, confirmText: '确认恢复可租',
    message: `<p>恢复后品牌回到“已接入”，车辆立即可租。</p>${note ? `<div class="ops-script">${note}</div>` : ''}`,
    impact: `${brand.name} 回到已接入，可租车辆立即在用户端出现`,
    reasons: ['指标已恢复到门槛以上，按运行智能体建议恢复', '运营商确认故障已修复，核实后恢复'],
    onConfirm: async reason => { await api.admin.ops.resume(brand.brandId, reason); },
  });
  if (!ans) return false;
  ui.toast(`已恢复「${brand.name}」可租`, { type: 'ok', hint: '用户端扫码立即可以骑该品牌的车' });
  return true;
}

/** 运行智能体的暂停可租建议：一句话结论与依据 */
export function suggestionText(sg) {
  const v = sg.value == null ? '—' : ui.percent(sg.value, 1), th = sg.threshold == null ? '—' : ui.percent(sg.threshold, 0);
  return {
    say: `建议暂停「${sg.brandName}」可租`,
    basis: [`${sg.metric} ${v}，低于门槛 ${th}`, `判定要求持续 ${sg.sustainMinutes} 分钟；首次上报 ${ui.datetime(sg.at)}${sg.updatedAt ? `，最近更新 ${ui.datetime(sg.updatedAt)}` : ''}`, '指标回到门槛以上时，这条建议自动失效'].filter(Boolean),
  };
}

/** 确认运行智能体的暂停建议：品牌暂停可租。返回 true 表示已执行 */
export async function confirmSuggestion(sg, { vehicles } = {}) {
  const t = suggestionText(sg);
  const ans = await ui.confirm({
    title: `确认暂停「${sg.brandName}」可租`, danger: true, confirmText: '确认暂停可租',
    message: `<div class="ops-script"><b>运行智能体：</b>${esc(t.say)}。依据：${t.basis.slice(0, 2).map(esc).join('；')}。</div>
      <p>确认后，${esc(sg.brandName)}的车辆立即不能再被租用；已在骑行的订单照常结束。用户扫码看到：<b>【${esc(sg.brandName)}】电单车暂停可租，附近其他品牌的车可以骑。</b></p>`,
    impact: `${sg.brandName}${vehicles ? ` 全部 ${vehicles} 辆车` : ' 的车辆'}立即不可租；确认人记入操作留痕，指标恢复后可在这里恢复可租`,
    reasons: ['核对指标属实，按运行智能体建议暂停可租', '运营商确认车辆通信故障，暂停可租等待修复'],
    onConfirm: async reason => { await api.admin.ops.confirmSuspend(sg.id, reason); },
  });
  if (!ans) return false;
  ui.toast(`已暂停「${sg.brandName}」可租`, { type: 'ok', hint: '用户端扫码立即显示附近其他品牌的车；确认已留痕' });
  return true;
}

/** 驳回运行智能体的暂停建议：品牌保持已接入。返回 true 表示已执行 */
export async function dismissSuggestion(sg) {
  const ans = await ui.confirm({
    title: `驳回暂停「${sg.brandName}」的建议`, danger: false, confirmText: '驳回，保持可租',
    message: `<p>驳回后${esc(sg.brandName)}保持已接入，车辆照常可租。指标再次持续低于门槛时，运行智能体会重新提出建议。</p>`,
    impact: `${sg.brandName} 保持已接入；驳回人与事由记入操作留痕`,
    reasons: ['指标短时波动，已与运营商核实车辆正常', '上报数据有误，运营商已说明原因'],
    onConfirm: async reason => { await api.admin.ops.dismissSuspend(sg.id, reason); },
  });
  if (!ans) return false;
  ui.toast(`已驳回，「${sg.brandName}」保持可租`, { type: 'ok', hint: '驳回已写入操作留痕' });
  return true;
}

/** 进入降级开单：值班人采纳运行智能体建议，或自行判断后进入 */
export async function enterDegrade({ preset = '', why = '' } = {}) {
  const ans = await ui.confirm({
    title: '进入降级开单', danger: true, confirmText: '确认进入降级开单',
    message: `${why ? `<div class="ops-script">${why}</div>` : ''}
      <div class="a-item"><label for="dg-preset">故障类型</label><select id="dg-preset" class="a-select">${PRESETS.map(([k, d]) => `<option value="${esc(k)}" ${k === preset ? 'selected' : ''}>${esc(k)}：${esc(d)}</option>`).join('')}</select></div>
      <p>进入后用户端立即按降级准入：老用户照常开锁，只多一行“费用将稍后收取”；先骑后验新用户与押金状态未知的老用户额度更紧；已知欠费或退押中的用户不能开单。已开锁的订单不受影响。</p>`,
    impact: '全部用户的扫码开锁立即按降级准入；值班人收到告警，后续可收紧或停止',
    reasons: ['支付通道持续超时，判断仍可请求运营商开锁，进入降级开单', '实名核验服务不可用，进入降级开单'],
    onConfirm: async () => { await api.system.setDegrade({ on: true, preset: $('#dg-preset').value }); },
  });
  if (!ans) return false;
  ui.toast('已进入降级开单', { type: 'ok', hint: '用户端已按降级准入工作，进入已留痕' });
  return true;
}

export async function exitDegrade({ pending = 0 } = {}) {
  let res = null;
  const ans = await ui.confirm({
    title: '退出降级开单', danger: true, confirmText: '确认退出降级开单',
    message: `<p>退出后用户端恢复正常开单，降级单进入恢复闭合：逐单匹配运营商报告、计价收款，并为先骑后验的新用户补做实名核验。</p>${pending ? `<div class="a-alert">还有 ${pending} 笔降级单未闭合，退出后会自动逐单处理，缺项的转人工。</div>` : ''}`,
    impact: '用户端立即恢复正常开单；降级单开始恢复闭合，收款与补验实名会通知相关用户',
    reasons: ['故障已恢复，核实支付与实名服务正常，退出降级', '指标恢复正常，退出降级开单'],
    onConfirm: async () => { res = await api.system.setDegrade({ on: false }); },
  });
  if (!ans) return null;
  const c = res && res.closure;
  ui.toast('已退出降级开单', { type: 'ok', hint: c && c.pendingOrderIds && c.pendingOrderIds.length ? `${c.closed} 笔已闭合，${c.pendingOrderIds.length} 笔缺项，已进入恢复闭合的人工处理列表` : '降级单已逐单闭合' });
  return res;
}

const CTRL = {
  tighten: { title: '收紧降级开单', btn: '确认收紧', fn: r => api.admin.ops.degrade.tighten(r), danger: true, text: '收紧后，先骑后验新用户和押金状态未知的老用户暂时不能再开降级单；老用户照常。', done: '已收紧降级开单', hint: '紧档用户在用户端看到“平台临时限制开新单”', impact: '先骑后验新用户与押金状态未知的老用户立即不能开新单', reasons: ['每小时降级单接近上限，收紧控制坏账风险', '降级持续时间过长，先收紧观察'] },
  stop: { title: '停止开新单', btn: '确认停止开新单', fn: r => api.admin.ops.degrade.stop(r), danger: true, text: '停止后，所有用户都不能开新单；已开锁的订单不受影响，仍可正常还车。', done: '已停止开新单', hint: '用户端看到“平台暂时无法开新单，正在恢复”', impact: '所有用户立即不能开新单；骑行中的订单不受影响', reasons: ['已达每小时降级单上限，停止开新单控制风险', '故障扩大，停止开新单等待恢复'] },
  relax: { title: '放宽降级开单', btn: '确认放宽', fn: r => api.admin.ops.degrade.relax(r), danger: false, text: '放宽后恢复正常的降级开单，不再收紧或停止。', done: '已放宽降级开单', hint: '用户端恢复按降级准入开单', impact: '取消收紧与停止，恢复正常降级开单', reasons: ['风险下降，放宽恢复降级开单', '故障部分恢复，放宽开单'] },
};
export async function controlDegrade(kind, { why = '' } = {}) {
  const c = CTRL[kind];
  const ans = await ui.confirm({
    title: c.title, danger: c.danger, confirmText: c.btn,
    message: `<p>${c.text}</p>${why ? `<div class="ops-script">${why}</div>` : ''}`,
    impact: c.impact, reasons: c.reasons,
    onConfirm: async reason => { await c.fn(reason); },
  });
  if (!ans) return false;
  ui.toast(c.done, { type: 'ok', hint: c.hint });
  return true;
}

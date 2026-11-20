// 订单详情：扣费明细（骑行费、调度费、月卡、优惠券、押金抵扣、退款）、支付与退款记录、联系客服（R-12）
import * as ui from '../../shared/ui.js';
import { api } from '../../shared/api.js';
import { paySheet as nativePay } from '../kit.js';

const T = s => (s ? String(s).replace(' ', 'T') : s);
const go = (route, params = {}) => {
  const q = new URLSearchParams(Object.entries(params).filter(([, v]) => v != null && v !== '')).toString();
  location.hash = route + (q ? '?' + q : '');
};
const hashQuery = () => { const i = location.hash.indexOf('?'); return Object.fromEntries(new URLSearchParams(i < 0 ? '' : location.hash.slice(i + 1))); };
function pageRoot(el) {
  if (el.classList.contains('m-page')) return el;
  const p = document.createElement('div'); p.className = 'm-page'; el.append(p); return p;
}
function style(id, css) {
  if (document.getElementById(id)) return;
  const s = document.createElement('style'); s.id = id; s.textContent = css; document.head.append(s);
}

style('p-order-detail', `
.d-hero{display:flex;flex-direction:column;gap:6px}
.d-hero .top{display:flex;justify-content:space-between;align-items:center;gap:8px}
.d-hero .brand{font-size:15px;font-weight:600}
.d-hero .brand small{font-weight:400;color:var(--ink-3);margin-left:6px}
.d-hero .m-big{font-size:30px}
.d-sec{font-size:15px;font-weight:600;margin-bottom:4px}
.d-pay{display:flex;justify-content:space-between;gap:12px;padding:8px 0;border-bottom:1px dashed var(--line);font-size:14px}
.d-pay:last-child{border:0}
.d-pay .l b{display:block;font-weight:600}
.d-pay .l span{display:block;font-size:12px;color:var(--ink-3)}
.d-pay .r{text-align:right}
.d-pay .r .chip{margin-top:2px}
.d-tk{display:flex;align-items:center;gap:8px;justify-content:space-between;padding:10px 0;border-bottom:1px dashed var(--line);font-size:14px;background:none;border-left:0;border-right:0;border-top:0;width:100%;text-align:left;min-height:48px}
.d-tk:last-child{border-bottom:0}
.d-tk:hover{color:var(--orange-700)}
.d-tk small{display:block;font-size:12px;color:var(--ink-3)}
.d-sheet-total{display:flex;justify-content:space-between;align-items:baseline;padding:4px 0}
.d-sheet-total b{font-size:30px}
`);

let alive = false, offs = [];

const chipCls = t => (/欠费/.test(t) ? 'err' : /待支付|稍后|处理中/.test(t) ? 'warn' : /0 元|无需支付|未扣费/.test(t) ? 'mute' : /退款/.test(t) ? 'brand' : 'ok');
const payChip = s => (s === '成功' ? 'ok' : s === '失败' ? 'err' : 'warn');
const dt = s => (s ? ui.datetime(T(s)) : '—');

function failCard(e, root, onRetry) {
  const n = ui.retry({ text: '订单没有加载出来，请检查网络后重试', onRetry });
  if (e && e.code === 'NOT_FOUND') {
    return ui.empty({ icon: '🧾', title: '没有找到这笔订单', text: '订单可能不属于当前账号', action: '回到我的订单', onAction: () => go('#/orders') });
  }
  if (e && e.code === 'NOT_LOGGED_IN') return ui.empty({ icon: '🔑', title: '请先授权登录', text: '登录后即可查看订单', action: '去授权登录', onAction: () => go('#/guide') });
  return n;
}

async function mount(el, params = {}) {
  unmount();
  alive = true;
  const orderId = params.orderId || hashQuery().orderId;
  const root = pageRoot(el);
  root.innerHTML = '<div class="m-body" id="d-body"></div><div class="m-bottom" id="d-bottom" hidden></div>';
  const body = root.querySelector('#d-body'), bottom = root.querySelector('#d-bottom');
  let o = null;

  async function load() {
    if (!o) { body.innerHTML = ui.skeleton('card', 3); bottom.hidden = true; }
    if (!orderId) { body.innerHTML = ''; body.append(ui.empty({ icon: '🧾', title: '没有指定订单', text: '请从我的订单里选择一笔', action: '去我的订单', onAction: () => go('#/orders') })); return; }
    try {
      o = await api.user.order(orderId);
      if (o.refundStatus === '原路失败·待人工转账') {
        // 已经填过收款账户的，不再让用户重复填写
        const ts = await api.user.cs.tickets().catch(() => null);
        o.awaitingPayee = ts ? ts.some(t => t.orderId === o.orderId && t.awaitingPayee) : true;
      }
      if (!alive) return;
      render();
    } catch (e) { if (!alive) return; body.innerHTML = ''; body.append(failCard(e, root, load)); }
  }

  function billCard() {
    const b = o.bill;
    if (!b) {
      const msg = o.status === '开锁失败' ? `开锁失败：${o.unlockFailReason || '车辆没有响应'}，没有扣费`
        : o.status === '骑行中' ? '骑行进行中，还车后出账单'
          : o.degraded && o.degraded.feeLater ? '费用将稍后收取，平台恢复后会通知你'
            : '本单没有产生费用';
      return `<section class="m-card"><div class="d-sec">费用明细</div><p class="m-hint" style="font-size:14px;color:var(--ink-2)">${ui.esc(msg)}</p></section>`;
    }
    let seenTotal = false, html = '';
    for (const l of b.lines) {
      if (l.total) {
        seenTotal = true;
        html += `<div class="m-bill-total"><span>${ui.esc(l.label)}</span><b class="num">${ui.money(l.amount)}</b></div>`;
      } else {
        html += `<div class="m-row"><span class="m-k">${ui.esc(l.label)}${l.note ? `<br><small class="m-hint">${ui.esc(l.note)}</small>` : ''}</span><span class="num${l.amount < 0 ? ' minus' : ''}">${ui.money(l.amount)}</span></div>`;
      }
    }
    if (!seenTotal) html += `<div class="m-bill-total"><span>应付</span><b class="num">${ui.money(b.payable)}</b></div>`;
    return `<section class="m-bill"><div class="m-bill-head"><b>费用明细</b><span class="chip ${chipCls(o.statusText)}">${ui.esc(o.statusText)}</span></div>${html}
      <div class="m-kv" style="margin-top:6px"><span>已付款</span><span class="num">${ui.money(b.paid)}</span></div>
      ${b.unpaid > 0 ? `<div class="m-kv"><span>待付款</span><span class="num">${ui.money(b.unpaid)}</span></div>` : ''}
      ${b.refunded > 0 ? `<div class="m-kv"><span>已退款</span><span class="num">${ui.money(b.refunded)}</span></div>` : ''}</section>`;
  }

  function render() {
    const b = o.bill;
    const when = o.startAt || o.requestAt;
    const head = o.status === '开锁失败' ? '未扣费' : o.status === '骑行中' ? '骑行中' : b ? b.headline : (o.degraded ? '费用将稍后收取' : o.statusText);
    const ends = o.endStationName ? `${o.endStationName}${o.inStation === false ? '（站点外）' : ''}` : (o.endAt ? '站点外' : '—');
    const kv = (k, v) => `<div class="m-kv"><span>${k}</span><span class="num">${v}</span></div>`;
    let html = `<section class="m-card d-hero"><div class="top"><span class="brand">${ui.esc(o.brandName)}<small class="mono">${ui.esc(o.plateNo || '')}</small></span><span class="chip ${chipCls(o.statusText)}">${ui.esc(o.statusText)}</span></div>
      <div class="m-big num">${ui.esc(head)}</div><div class="m-hint mono">订单号 ${ui.esc(o.orderId)}</div></section>`;
    const notes = [...((b && b.notes) || [])];
    if ((o.inStationBasis === '桩与车端冲突·以桩为准' || (o.inStationBasis === '桩识别' && o.vehicleSideInStation === false))) notes.push('车辆定位在站点边缘外，桩已识别到车辆入桩，以桩为准按站内还车，不收调度费');
    if (notes.length) html += notes.map(n => `<div class="m-notice is-info"><span class="m-ic">ⓘ</span><span>${ui.esc(n)}</span></div>`).join('');
    if (o.degraded && o.degraded.feeLater) html += `<div class="m-notice"><span class="m-ic">ⓘ</span><span>${ui.esc(o.degraded.line)}</span></div>`;
    if (b && b.canPay) html += `<div class="m-notice ${o.payStatus === '欠费' ? 'is-err' : ''}"><span class="m-ic">${o.payStatus === '欠费' ? '!' : 'ⓘ'}</span><span>${o.payStatus === '欠费' ? `这单有 ${ui.money(b.unpaid)} 未付，补付后才能开新单。` : `请在 ${ui.datetime(T(b.payDeadline))} 前支付，逾期未付将按规则从押金抵扣或记为欠费。`}</span></div>`;
    html += billCard();
    html += `<section class="m-card"><div class="d-sec">骑行信息</div>
      ${kv(o.status === '开锁失败' ? '申请开锁时间' : '开锁时间', dt(when))}${o.endAt ? kv('还车时间', dt(o.endAt)) : ''}
      ${o.status === '已结束' || o.endAt ? kv('骑行时长', ui.duration(o.durationSec)) + kv('骑行里程', ui.km(o.distanceKm)) : ''}
      ${o.startStationName ? kv('起点站点', ui.esc(o.startStationName)) : ''}${o.endAt ? kv('还车位置', ui.esc(ends)) : ''}
      ${o.endMethod ? kv('结束方式', ui.esc(o.endMethod)) : ''}${o.payMethod ? kv('付款方式', ui.esc(o.payMethod)) : ''}</section>`;
    if (o.payments && o.payments.length) {
      html += `<section class="m-card"><div class="d-sec">支付与退款记录</div>${o.payments.map(p => `<div class="d-pay"><div class="l"><b>${ui.esc(p.type)}</b><span>${ui.esc(p.channel)} · ${dt(p.completedAt || p.createdAt)}</span>${p.failReason ? `<span style="color:var(--err)">${ui.esc(p.failReason)}</span>` : ''}</div>
        <div class="r"><div class="num">${p.type === '退款' || p.type === '退押' ? '−' : ''}${ui.money(p.amount).replace('−', '')}</div><span class="chip ${payChip(p.status)}">${ui.esc(p.status)}</span></div></div>`).join('')}</section>`;
    }
    const failedRefund = (o.payments || []).find(p => p.type === '退款' && p.status === '失败');
    if (failedRefund && o.refundStatus === '原路失败·待人工转账' && !o.awaitingPayee) {
      html += `<div class="m-notice is-info"><span class="m-ic">ⓘ</span><span>退款 ${ui.money(failedRefund.amount)} 原路退回没有成功，已收到你的收款账户，平台公司财务会人工转账，到账后通知你。</span></div>`;
    } else if (failedRefund && o.refundStatus === '原路失败·待人工转账') {
      html += `<div class="m-notice is-err"><span class="m-ic">!</span><span>退款 ${ui.money(failedRefund.amount)} 原路退回没有成功。<button type="button" class="m-link" data-payee="${ui.esc(failedRefund.paymentId)}">填写本人收款账户</button>，平台将人工转账。</span></div>`;
    }
    if (o.tickets && o.tickets.length) {
      html += `<section class="m-card"><div class="d-sec">客服对话</div>${o.tickets.map(t => `<button type="button" class="d-tk" data-tk="${ui.esc(t.ticketId)}"><span>${ui.esc(t.category)}<small>${dt(t.createdAt)}</small></span><span class="chip ${t.status === '已解决' ? 'ok' : 'warn'}">${ui.esc(t.status)}</span></button>`).join('')}</section>`;
    }
    body.innerHTML = html;
    body.scrollTop = 0;

    bottom.hidden = false; bottom.innerHTML = '';
    const open = (o.tickets || []).filter(t => t.status !== '已解决').sort((a, c) => (a.createdAt < c.createdAt ? 1 : -1))[0];
    const csBtn = kind => { const x = document.createElement('button'); x.type = 'button'; x.className = `m-btn m-btn-${kind}`; x.textContent = open ? '继续客服对话' : '联系客服'; x.addEventListener('click', () => (open ? go('#/chat', { ticketId: open.ticketId }) : go('#/chat', { orderId: o.orderId, entry: '我的订单' }))); return x; };
    if (b && b.canPay) {
      const pay = document.createElement('button'); pay.type = 'button'; pay.className = 'm-btn m-btn-primary';
      pay.textContent = `${o.payStatus === '欠费' ? '补付' : '一键支付'} ${ui.money(b.unpaid)}`;
      pay.addEventListener('click', () => paySheet(pay));
      bottom.append(pay, csBtn('ghost'));
    } else bottom.append(csBtn('secondary'));
  }

  /** 与账单页同一个支付半屏（随小程序平台显示微信或支付宝样式） */
  async function paySheet() {
    const b = o.bill, arrears = o.payStatus === '欠费';
    const res = await nativePay({ amount: b.unpaid, subject: arrears ? '补付骑行欠费' : '骑行费用', desc: `${o.plateNo || o.brandName} · ${o.billedMinutes || 1} 分钟`, run: () => api.user.pay(o.orderId) });
    if (!res) return;
    ui.toast(arrears ? '欠费已补付' : '支付成功', { type: 'ok' });
    if (alive) await load();
  }

  body.addEventListener('click', e => {
    const tk = e.target.closest('[data-tk]'), pe = e.target.closest('[data-payee]');
    if (tk) go('#/chat', { ticketId: tk.dataset.tk });
    else if (pe) go('#/refund-account', { paymentId: pe.dataset.payee, orderId });
  });
  offs.push(api.on('ticket.changed', () => { if (alive && o) load(); }));
  offs.push(api.on('order.updated', ev => { if (alive && ev.orderId === orderId) load(); }));

  await load();
}

function unmount() {
  alive = false;
  offs.forEach(f => { try { f(); } catch { /* 已取消 */ } });
  offs = [];
}

export const route = '#/order-detail';
export const title = '订单详情';
export { mount, unmount };
export default { route, title, mount, unmount };

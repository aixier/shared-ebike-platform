// 结束账单与付款（R-09；BR-07 BR-22）、降级单账单（R-32；S-07）。
// 免押自动扣款、缴押金一键支付、月卡与优惠券自动抵扣明细、欠费补付、降级单"费用将稍后收取"。
import { ui, api, esc, app, paySheet, mdhm, toD, nowDate } from '../kit.js';

let S = null;
export default {
  route: '#/bill',
  title: '行程结束',
  root: q => q.fresh === '1',
  async mount(el, p) {
    S = { el, id: p.orderId || '', fresh: p.fresh === '1', alive: true };
    el.innerHTML = `<div class="m-body">${ui.skeleton('card', 1)}${ui.skeleton('lines', 5)}</div><div class="m-bottom"></div>`;
    await load();
  },
  unmount() { S = null; },
};

async function load() {
  const my = S;
  try {
    let o;
    if (my.id) o = await api.user.order(my.id);
    else {
      const l = await api.user.orders({ pageSize: 1 });
      if (!l.rows.length) { my.el.innerHTML = '<div class="m-body"></div>'; ui.$('.m-body', my.el).append(ui.empty({ icon: '🧾', title: '还没有骑行记录', text: '扫码骑一次，账单会出现在这里。', action: '扫码用车', onAction: () => app().replace('#/scan') })); return; }
      o = await api.user.order(l.rows[0].orderId); my.id = o.orderId;
    }
    if (S !== my) return;
    if (o.status === '骑行中') { app().replace('#/ride'); return; }
    my.o = o;
    await draw(o, my.fresh);
  } catch (e) {
    if (S !== my) return;
    my.el.innerHTML = '';
    my.el.append(ui.retry({ text: e.code === 'NOT_FOUND' ? '没有找到这笔订单' : '账单没有加载出来，请检查网络后重试', onRetry: () => { my.el.innerHTML = ''; load(); } }));
  }
}

const CHIP = {
  已支付: o => o.payMethod === '免押自动扣款' ? ['ok', '已自动扣款'] : ['ok', '已支付'],
  待支付: () => ['warn', '待支付'], 欠费: () => ['err', '欠费'], 押金抵扣: () => ['brand', '已从押金抵扣'], 无需支付: () => ['ok', '无需支付'], 欠费已补付: () => ['ok', '已补付'],
};

async function draw(o, fresh, popped) {
  const my = S, el = my.el, b = o.bill, me = app().getMe();
  const later = !!(o.degraded && !o.fee);
  const failed = o.status === '开锁失败';
  const [tone, text] = later ? ['warn', '稍后收取'] : failed ? ['ok', '未扣费'] : (CHIP[o.payStatus] || (() => ['ok', o.statusText]))(o);
  const mins = o.billedMinutes || Math.max(1, Math.ceil((o.durationSec || 0) / 60));
  const route = `${o.startStationName || '路边'} → ${o.endStationName || (o.inStation === false ? '站点外' : '路边')}`;
  const unpaid = b && (o.payStatus === '待支付' || o.payStatus === '欠费');
  const total = b ? (unpaid ? b.unpaid : b.payable) : 0;
  const totalLabel = unpaid ? '应付' : '实付';
  const rows = b ? b.lines.filter(l => !l.total && l.key !== 'depositDeducted' && l.key !== 'refunded').map(l => `<div class="m-row"><span class="m-k">${esc(l.label)}${l.note ? `<small>${esc(l.note)}</small>` : ''}</span><span class="num ${l.amount < 0 ? 'minus' : ''}">${ui.money(l.amount)}</span></div>`).join('') : '';
  const after = b ? b.lines.filter(l => l.key === 'depositDeducted' || l.key === 'refunded').map(l => `<div class="m-row"><span class="m-k">${esc(l.label)}</span><span class="num">${ui.money(l.amount)}</span></div>`).join('') : '';
  const notes = [...(b ? b.notes : [])];
  if (o.endMethod === '入桩识别' && !notes.length) notes.push('车辆推入桩位后自动结束，按入桩时间计费');
  if ((o.inStationBasis === '桩与车端冲突·以桩为准' || (o.inStationBasis === '桩识别' && o.vehicleSideInStation === false))) notes.push('车辆定位在站点边缘外，桩已识别到车辆入桩，以桩为准按站内还车，不收调度费');
  if (later) {
    const dep = me && me.deposit.mode === '押金';
    if (o.inStation === false) notes.push('站点外还车，平台恢复后计价时一并收取调度费');
    notes.push(dep ? '平台恢复后会推送一键支付，你无需任何操作' : '平台恢复后会自动扣费并通知你，你无需任何操作');
    if (o.degraded.admission === '先骑后验新用户') notes.push('实名核验稍后完成，核验未通过将冻结账户并追缴本次费用');
  }
  if (b && o.payStatus === '待支付' && b.payDeadline) notes.push(`请在 ${mdhm(b.payDeadline)} 前支付，逾期将从押金自动抵扣并通知你`);
  if (o.unlockResult === '超时后成功' && !b) notes.push('车辆在超时后才开锁，本单没有产生费用');
  const lead = later ? '已还车，费用将稍后收取' : failed ? '开锁失败，没有收费' : b ? `已还车，${b.headline}` : '本单没有产生费用';
  const canPay = b && b.canPay;
  const pending = b && unpaid && !b.canPay;
  const msg = o.payStatus === '欠费' ? `<div class="m-notice is-err"><span class="m-ic">!</span><span>自动扣款没有成功，已记为欠费。补付后才能再次开锁。</span></div>` : pending ? '<div class="m-notice"><span class="m-ic">ⓘ</span><span>上一笔扣款结果还在确认中，请勿重复支付，确认后会通知你。</span></div>' : '';
  el.innerHTML = `<div class="m-body"><div class="sh-bill-wrap" style="display:grid;gap:12px">
      ${fresh || popped ? `<div class="sh-success"><span class="tick" aria-hidden="true">✓</span><span>${esc(lead)}</span></div>` : ''}
      ${msg}
      <div class="m-bill sh-bill${fresh ? ' is-slide' : ''}${popped ? ' is-pop' : ''}" id="billCard">
        <div class="m-bill-head"><b>${failed ? '开锁失败' : '本次骑行'}</b><span class="chip ${tone}">${esc(text)}</span></div>
        <div class="sub">${failed ? esc(`${o.displayName} · ${o.plateNo}`) : `${esc(route)} · ${mins} 分钟${o.distanceKm ? ` · ${o.distanceKm} 公里` : ''}`}</div>
        ${b ? `${rows}<div class="m-bill-total"><span>${totalLabel}</span><b class="num" id="totalAmt">${ui.money(total)}</b></div>${after}`
          : later ? `<div class="m-row"><span class="m-k">骑行 ${mins} 分钟</span><span class="num">—</span></div><div class="m-bill-total"><span>费用</span><b class="later">将稍后收取</b></div>`
            : `<div class="m-bill-total"><span>费用</span><b class="num">${ui.money(0)}</b></div>`}
        ${notes.length ? `<div class="sh-notes">${notes.map(n => `<p>${esc(n)}</p>`).join('')}</div>` : ''}
      </div></div></div>
    <div class="m-bottom" id="billBot"></div>`;
  if (b && fresh && !popped) ui.countTo(ui.$('#totalAmt', el), total);
  if (fresh && !popped) { ui.vibrate(); }
  const bot = ui.$('#billBot', el);
  const done = ui.el(`<button type="button" class="m-btn m-btn-primary">完成</button>`);
  done.addEventListener('click', () => (my.fresh ? app().replace('#/scan') : app().back()));
  if (canPay) {
    const pay = ui.el(`<button type="button" class="m-btn m-btn-primary">${o.payStatus === '欠费' ? '立即补付' : '一键支付'} ${ui.money(b.unpaid)}</button>`);
    pay.addEventListener('click', async () => {
      const res = await paySheet({ amount: b.unpaid, subject: o.payStatus === '欠费' ? '补付骑行欠费' : '骑行费用', desc: `${o.plateNo} · ${mins} 分钟`, run: () => api.user.pay(o.orderId) });
      if (S !== my || !res) return;
      my.o = { ...o, ...res.order, payments: o.payments };
      my.fresh = false;
      await draw(my.o, false, true);
    });
    bot.append(pay);
  } else if (pending) {
    const r = ui.el('<button type="button" class="m-btn m-btn-primary">刷新状态</button>');
    r.addEventListener('click', () => { my.el.innerHTML = ''; load(); });
    bot.append(r);
  } else bot.append(done);
  const cs = ui.el('<button type="button" class="m-btn m-btn-ghost">有疑问？联系客服</button>');
  cs.addEventListener('click', () => app().go('#/chat?orderId=' + encodeURIComponent(o.orderId) + '&entry=' + encodeURIComponent('我的订单')));
  bot.append(cs);
}

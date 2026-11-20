// 押金：显示押金或免押状态；一键退押并显示预计到账时间与不能退的原因；信用免押开通（R-14～R-17，BR-23、BR-24）
import * as ui from '../../shared/ui.js';
import { api } from '../../shared/api.js';
import { paySheet as nativePay } from '../kit.js';

const T = s => (s ? String(s).replace(' ', 'T') : s);
const go = (route, params = {}) => {
  const q = new URLSearchParams(Object.entries(params).filter(([, v]) => v != null && v !== '')).toString();
  location.hash = route + (q ? '?' + q : '');
};
function pageRoot(el) {
  if (el.classList.contains('m-page')) return el;
  const p = document.createElement('div'); p.className = 'm-page'; el.append(p); return p;
}
function style(id, css) {
  if (document.getElementById(id)) return;
  const s = document.createElement('style'); s.id = id; s.textContent = css; document.head.append(s);
}

style('p-deposit', `
.e-hero{border-radius:var(--r-lg);padding:16px;box-shadow:var(--sh-1);display:flex;flex-direction:column;gap:6px;background:var(--surface)}
.e-hero.is-ok{background:linear-gradient(135deg,var(--ok-bg),var(--surface) 70%)}
.e-hero.is-warn{background:linear-gradient(135deg,var(--warn-bg),var(--surface) 70%)}
.e-hero.is-slide{animation:ePop var(--d-slow) var(--ease) both}
@keyframes ePop{from{transform:translateY(16px) scale(.98);opacity:0}to{transform:none;opacity:1}}
.e-hero .top{display:flex;justify-content:space-between;align-items:center}
.e-hero .top b{font-size:15px}
.e-hero .big{font-size:32px;font-weight:700;line-height:1.2}
.e-hero p{font-size:13px;color:var(--ink-2)}
.e-sec{font-size:15px;font-weight:600;margin-bottom:6px}
.e-chk{display:flex;gap:10px;align-items:flex-start;padding:6px 0;font-size:14px}
.e-chk i{flex:none;width:22px;height:22px;border-radius:50%;display:grid;place-items:center;font-style:normal;font-size:12px;font-weight:700;margin-top:1px}
.e-chk.ok i{background:var(--ok-bg);color:var(--ok)}
.e-chk.no i{background:var(--err-bg);color:var(--err)}
.e-chk .t{flex:1;min-width:0}
.e-chk .t small{display:block;font-size:12px;color:var(--ink-3)}
.e-chk .m-btn{width:auto;height:44px;padding:0 14px;font-size:14px;flex:none}
.e-tl{display:flex;flex-direction:column}
.e-tl .s{display:flex;gap:12px;position:relative;padding-bottom:14px}
.e-tl .s:last-child{padding-bottom:0}
.e-tl .s::before{content:"";position:absolute;left:9px;top:22px;bottom:0;width:2px;background:var(--disabled-bg)}
.e-tl .s:last-child::before{display:none}
.e-tl .s.done::before{background:var(--ok)}
.e-tl .s i{flex:none;width:20px;height:20px;border-radius:50%;background:var(--disabled-bg);display:grid;place-items:center;font-style:normal;font-size:11px;font-weight:700;color:var(--ink-2);position:relative;z-index:1}
.e-tl .s.done i{background:var(--ok);color:#fff}
.e-tl .s.cur i{background:var(--orange-500);color:var(--on-orange)}
.e-tl .s.bad i{background:var(--err);color:#fff}
.e-tl .s b{display:block;font-size:14px}
.e-tl .s span{display:block;font-size:12px;color:var(--ink-3)}
.e-sheet-total{display:flex;justify-content:space-between;align-items:baseline}
.e-sheet-total b{font-size:30px}
@media (prefers-reduced-motion:reduce){.e-hero.is-slide{animation:none}}
`);

let alive = false, offs = [];

const dt = s => (s ? ui.datetime(T(s)) : '—');
const mdhm = s => { const d = new Date(T(s)); return `${d.getMonth() + 1}月${d.getDate()}日 ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`; };
const NEXT_ROUTE = { goRide: '#/ride', payArrears: '#/debt', viewArrears: '#/debt', payOrder: '#/bill', viewDeposit: '#/deposit', contactCS: '#/chat', viewOrders: '#/orders', unfreezeGuide: '#/account', onboarding: '#/guide', verifyRealName: '#/guide', login: '#/guide' };
const routeOf = n => {
  if (!n) return null;
  const base = NEXT_ROUTE[n.action];
  if (!base) return null;
  return { route: base, params: { ...(n.params || {}), ...(n.orderId ? { orderId: n.orderId } : {}) } };
};
const provider = ch => (ch === '支付宝' ? '芝麻信用' : '微信支付分');

async function mount(el) {
  unmount();
  alive = true;
  const root = pageRoot(el);
  root.innerHTML = '<div class="m-body" id="e-body"></div><div class="m-bottom" id="e-bottom" hidden></div>';
  const body = root.querySelector('#e-body'), bottom = root.querySelector('#e-bottom');
  let S = null, fresh = false;

  async function load(silent) {
    if (!silent) { body.innerHTML = ui.skeleton('card', 3); bottom.hidden = true; }
    try {
      const [dep, me] = await Promise.all([api.user.deposit.get(), api.user.me()]);
      if (!alive) return;
      if (!me) throw Object.assign(new Error('请先授权登录'), { code: 'NOT_LOGGED_IN' });
      S = { dep, me };
      draw();
    } catch (e) {
      if (!alive) return;
      body.innerHTML = ''; bottom.hidden = true;
      body.append(e.code === 'NOT_LOGGED_IN'
        ? ui.empty({ icon: '🔑', title: '请先授权登录', text: '登录后即可查看押金与免押状态', action: '去授权登录', onAction: () => go('#/guide') })
        : ui.retry({ text: '押金信息没有加载出来，请检查网络后重试', onRetry: () => load() }));
    }
  }

  const btn = (cls, text, onClick, id) => { const b = document.createElement('button'); b.type = 'button'; b.className = `m-btn m-btn-${cls}`; b.textContent = text; if (id) b.id = id; b.addEventListener('click', onClick); return b; };
  const csLink = () => `<button type="button" class="m-btn m-btn-ghost" data-cs>对押金有疑问？联系客服</button>`;

  function draw() {
    const { dep, me } = S;
    const rule = dep.rule;
    bottom.innerHTML = ''; bottom.hidden = true;
    let html = '';
    const slide = fresh ? ' is-slide' : ''; fresh = false;
    const actions = [];

    if (dep.mode === '免押') {
      html += `<section class="e-hero is-ok${slide}"><div class="top"><b>押金</b><span class="chip ok">已免押</span></div><div class="big">信用免押已开通</div>
        <p>${ui.esc(dep.creditFree.provider)} · ${ui.datetime(T(dep.creditFree.openedAt))} 开通</p></section>
        <section class="m-card"><div class="e-sec">免押说明</div>
          <div class="e-chk ok"><i>✓</i><span class="t">骑行不用缴押金</span></div>
          <div class="e-chk ok"><i>✓</i><span class="t">每单还车后自动扣款，月卡与优惠券自动抵扣</span></div>
          <div class="e-chk ok"><i>✓</i><span class="t">平台不会冻结你的资金</span></div></section>`;
    } else if (dep.mode === '未开通') {
      html += `<section class="e-hero is-warn${slide}"><div class="top"><b>押金</b><span class="chip warn">未开通</span></div><div class="big">还没有免押或押金</div>
        <p>骑行前需要开通信用免押，或缴纳押金 ${ui.money(rule.depositAmount)}。推荐免押，不用先付钱。</p></section>`;
      actions.push(btn('primary', '开通信用免押', () => creditSheet(), 'e-open'), btn('secondary', `缴纳押金 ${ui.money(rule.depositAmount)}`, () => paySheet()));
    } else {
      const d = dep.deposit;
      if (d.status === '退押中') html += refundingHtml(d, dep, slide);
      else if (d.status === '已退') {
        html += `<section class="e-hero${slide}"><div class="top"><b>押金</b><span class="chip mute">已退还</span></div><div class="big">押金已退还</div><p>原押金 ${ui.money(d.amountPaid)} 已原路退回。再骑行需要重新开通免押或缴纳押金 ${ui.money(rule.depositAmount)}。</p></section>`;
        actions.push(btn('primary', '开通信用免押', () => creditSheet()), btn('secondary', `缴纳押金 ${ui.money(rule.depositAmount)}`, () => paySheet()));
      } else {
        const low = d.status === '押金不足';
        html += `<section class="e-hero ${low ? 'is-warn' : 'is-ok'}${slide}"><div class="top"><b>押金</b><span class="chip ${low ? 'warn' : 'ok'}">${ui.esc(d.status)}</span></div>
          <div class="big num">${ui.money(d.balance)}</div><p>已缴 ${ui.money(d.amountPaid)} · ${dt(d.paidAt)} 缴纳。按缴纳时的金额退还，之后规则调整不补差也不退差。</p></section>`;
        if (low) html += `<div class="m-notice"><span class="m-ic">ⓘ</span><span>押金被抵扣了 ${ui.money(d.amountPaid - d.balance)}，补缴 ${ui.money(d.amountPaid - d.balance)} 后才能继续骑行，也可以直接退押。</span></div>`;
        html += refundRulesHtml(dep);
        if (low) actions.push(btn('primary', `补缴押金 ${ui.money(d.amountPaid - d.balance)}`, () => topupSheet(d)));
        const bl = dep.blockers || [];
        const rb = btn(low ? 'secondary' : 'primary', '申请退押', () => refundSheet(), 'e-refund');
        if (!dep.canRefund) {
          const first = bl[0];
          ui.setDisabled(rb, first ? first.message : '当前不能退押', first && first.next ? first.next.label : '先处理上面的事项');
        }
        actions.push(rb);
      }
    }
    if (dep.previousDeposit && dep.previousDeposit.refund) {
      const pr = dep.previousDeposit;
      html += `<section class="m-card is-flat"><div class="e-sec">上一笔押金</div><div class="m-kv"><span>金额</span><span class="num">${ui.money(pr.amountPaid)}</span></div><div class="m-kv"><span>状态</span><span>${ui.esc(pr.status)}</span></div></section>`;
    }
    html += `<p class="m-hint" style="padding:0 4px">押金金额 ${ui.money(rule.depositAmount)} · 信用免押需 ${rule.creditFreeThreshold} 分以上 · 退押 ${rule.refundWorkdays} 个工作日内到账。原品牌的旧押金不能转到这里，由原品牌退还。</p>${csLink()}`;
    body.innerHTML = html;
    body.scrollTop = 0;
    body.querySelector('[data-cs]').addEventListener('click', () => go('#/chat', { entry: '押金' }));
    body.querySelectorAll('[data-next]').forEach(b => b.addEventListener('click', () => { const r = routeOf(JSON.parse(b.dataset.next)); r && go(r.route, r.params); }));
    const pe = body.querySelector('[data-payee]'); pe && pe.addEventListener('click', () => go('#/refund-account', { paymentId: S.dep.refundFailedPaymentId }));
    if (actions.length) {
      bottom.hidden = false;
      actions.forEach(a => bottom.append(a));
      const rb = bottom.querySelector('#e-refund');
      if (rb && rb.getAttribute('aria-disabled') === 'true') {
        const first = (dep.blockers || [])[0];
        const w = document.createElement('p'); w.className = 'm-why';
        w.innerHTML = `${ui.esc(first ? first.message : '当前不能退押')}${first && first.next && routeOf(first.next) ? `，<button type="button">${ui.esc(first.next.label)}</button>` : ''}`;
        const wb = w.querySelector('button'); wb && wb.addEventListener('click', () => { const r = routeOf({ ...first.next, orderId: first.next.orderId || first.orderId }); r && go(r.route, r.params); });
        rb.after(w);
      }
    }
  }

  function refundRulesHtml(dep) {
    const bl = dep.blockers || [];
    const has = c => bl.find(b => b.code === c);
    const row = (ok, text, b) => `<div class="e-chk ${ok ? 'ok' : 'no'}"><i>${ok ? '✓' : '!'}</i><span class="t">${ui.esc(b ? b.message : text)}</span>${b && routeOf(b.next) ? `<button type="button" class="m-btn m-btn-secondary" data-next='${ui.esc(JSON.stringify({ ...b.next, orderId: b.next.orderId || b.orderId }))}'>${ui.esc(b.next.label)}</button>` : ''}</div>`;
    const riding = has('HAS_RIDING'), debt = has('HAS_ARREARS') || has('UNPAID_ORDER');
    return `<section class="m-card"><div class="e-sec">退押条件</div>
      ${row(!riding, '没有骑行中的订单', riding)}${row(!debt, '没有未结清的骑行费用', debt)}
      <p class="m-hint" style="margin-top:6px">申请后立即停止开新单，押金 ${ui.money(dep.refundAmount)} 原路退回，预计 ${mdhm(dep.expectedAtIfApplyNow)} 前到账（${dep.rule.refundWorkdays} 个工作日内）。</p></section>`;
  }

  function refundingHtml(d, dep, slide) {
    const r = d.refund || {};
    const bad = dep.awaitingPayee;
    const step = (cls, t, s) => `<div class="s ${cls}"><i>${cls === 'done' ? '✓' : cls === 'bad' ? '!' : ''}</i><div><b>${t}</b><span>${s}</span></div></div>`;
    return `<section class="e-hero is-warn${slide}"><div class="top"><b>押金</b><span class="chip ${bad ? 'err' : 'warn'}">${bad ? '原路退回失败' : '退押中'}</span></div><div class="big num">${ui.money(r.amount ?? d.balance)}</div>
        <p>${bad ? '原路退回没有成功，这笔钱仍会退给你。请填写本人实名的收款账户，平台公司会人工转账。' : `预计 ${mdhm(r.expectedAt)} 前原路退回到你的支付账户。`}</p></section>
      <section class="m-card"><div class="e-sec">退押进度</div><div class="e-tl">
        ${step('done', '已提交退押申请', dt(r.appliedAt))}
        ${step(r.stoppedAt ? 'done' : 'cur', '已停止开新单', r.stoppedAt ? dt(r.stoppedAt) : '正在确认')}
        ${step(bad ? 'bad' : 'cur', bad ? '原路退回失败' : '原路退回中', bad ? '等待你填写收款账户' : `预计 ${mdhm(r.expectedAt)} 前到账`)}</div></section>
      ${bad ? '<button type="button" class="m-btn m-btn-primary" data-payee>填写收款账户</button>' : '<div class="m-notice is-info"><span class="m-ic">ⓘ</span><span>退押期间不能开新单；到账后可重新开通免押或缴押。</span></div>'}`;
  }

  /* ---------- 操作浮层 ---------- */
  function runSheet({ title, bodyHtml, label, run, onDone, errTitleRetry }) {
    const sh = ui.bottomSheet({
      title, body: bodyHtml + '<div id="e-err"></div>',
      footer: [
        { label, kind: 'primary', keepOpen: true, async onClick(L, b) {
          const err = sh.body.querySelector('#e-err'); err.innerHTML = '';
          const old = b.innerHTML; b.setAttribute('aria-busy', 'true'); b.innerHTML = '<span class="m-spin"></span>处理中';
          try {
            const res = await run();
            L.close('ok'); if (alive) await onDone(res);
          } catch (e) {
            b.removeAttribute('aria-busy'); b.innerHTML = old;
            const n = e.next;
            err.innerHTML = `<div class="m-notice is-err"><span class="m-ic">!</span><span>${ui.esc(e.message || '没有成功')}${e.charged === false ? '，没有扣款。' : ''}</span></div>`;
            ui.shake(err.firstElementChild);
            if (e.code === 'CREDIT_SCORE_LOW') { b.textContent = '改缴押金'; b.onclick = null; b.addEventListener('click', ev => { ev.stopImmediatePropagation(); L.close('alt'); setTimeout(paySheet, 260); }, { capture: true, once: true }); }
            else if (n && routeOf(n)) { b.textContent = n.label; b.addEventListener('click', ev => { ev.stopImmediatePropagation(); L.close('next'); const r = routeOf(n); go(r.route, r.params); }, { capture: true, once: true }); }
            else b.textContent = errTitleRetry || '重试';
          }
        } },
        { label: '再想想', kind: 'ghost' },
      ],
    });
    return sh;
  }
  const refreshWith = text => async () => { ui.toast(text, { type: 'ok' }); ui.vibrate(); fresh = true; await load(true); };

  function creditSheet() {
    const th = S.dep.rule.creditFreeThreshold, pv = provider(S.me.channel);
    runSheet({
      title: '开通信用免押',
      bodyHtml: `<div class="e-sheet-total"><span class="m-k">${pv}</span><b class="num">免押</b></div>
        <p style="font-size:14px;color:var(--ink-2)">将查询你的${pv}，达到 ${th} 分即可免押。开通后骑行不用缴押金，每单还车后自动扣款。</p>`,
      label: '同意并开通', run: () => api.user.openCreditFree({ provider: pv }), onDone: refreshWith('已开通信用免押'),
    });
  }
  /* 缴押与补缴走与账单页同一个支付半屏 */
  async function paySheet() {
    const amt = S.dep.rule.depositAmount;
    const res = await nativePay({ amount: amt, subject: '缴纳押金', desc: '存平台专户，满足条件随时可退', run: () => api.user.payDeposit() });
    if (res && alive) await refreshWith('押金已缴纳')();
  }
  async function topupSheet(d) {
    const amt = Math.round((d.amountPaid - d.balance) * 10) / 10;
    const res = await nativePay({ amount: amt, subject: '补缴押金', desc: `补足到缴纳时的 ${ui.money(d.amountPaid)}`, run: () => api.user.topupDeposit() });
    if (res && alive) await refreshWith('押金已补缴')();
  }
  function refundSheet() {
    const dep = S.dep;
    runSheet({
      title: '确认退押',
      bodyHtml: `<div class="e-sheet-total"><span class="m-k">退还金额</span><b class="num">${ui.money(dep.refundAmount)}</b></div>
        <div class="m-kv"><span>退回方式</span><span>原路退回</span></div>
        <div class="m-kv"><span>预计到账</span><span class="num">${mdhm(dep.expectedAtIfApplyNow)} 前</span></div>
        <div class="m-notice"><span class="m-ic">ⓘ</span><span>申请后立即不能开新单；退押后要重新开通免押或缴押才能骑行。</span></div>`,
      label: '确认退押', run: () => api.user.deposit.refund(),
      onDone: async res => { ui.toast('已申请退押', { type: 'ok', hint: res.text }); ui.vibrate(); fresh = true; await load(true); },
    });
  }

  offs.push(api.on('order.updated', () => { if (alive && S) load(true); }));
  offs.push(api.on('pricing.changed', () => { if (alive && S) load(true); }));
  offs.push(api.on('data.reset', () => { if (alive) location.reload(); }));
  await load();
}

function unmount() {
  alive = false;
  offs.forEach(f => { try { f(); } catch { /* 已取消 */ } });
  offs = [];
}

export const route = '#/deposit';
export const title = '押金';
export { mount, unmount };
export default { route, title, mount, unmount };

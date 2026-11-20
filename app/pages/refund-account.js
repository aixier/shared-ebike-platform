// 填写收款账户：退款或退押金原路退回失败后，填写本人实名的银行账户，由平台公司人工转账（R-11）
import * as ui from '../../shared/ui.js';
import { api } from '../../shared/api.js';

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

style('p-refund-account', `
.r-amt{display:flex;flex-direction:column;gap:2px}
.r-done{display:flex;flex-direction:column;align-items:center;text-align:center;gap:8px;padding:12px 8px}
.r-done .ok{width:64px;height:64px;border-radius:50%;background:var(--ok);color:#fff;display:grid;place-items:center;font-size:32px;animation:rPop var(--d-slow) var(--ease) both}
@keyframes rPop{0%{transform:scale(.6);opacity:0}60%{transform:scale(1.08);opacity:1}100%{transform:none}}
.r-done h3{font-size:18px}
.r-done p{font-size:14px;color:var(--ink-2);max-width:280px}
@media (prefers-reduced-motion:reduce){.r-done .ok{animation:none}}
`);

const BANKS = ['中国工商银行', '中国农业银行', '中国银行', '中国建设银行', '交通银行', '招商银行', '中国邮政储蓄银行', '中信银行', '兴业银行', '平安银行'];

let alive = false;

async function mount(el, params = {}) {
  unmount();
  alive = true;
  const q = { ...hashQuery(), ...params };
  const root = pageRoot(el);
  root.innerHTML = '<div class="m-body" id="r-body"></div><div class="m-bottom" id="r-bottom" hidden></div>';
  const body = root.querySelector('#r-body'), bottom = root.querySelector('#r-bottom');

  const back = () => (q.ticketId ? go('#/chat', { ticketId: q.ticketId }) : q.orderId ? go('#/order-detail', { orderId: q.orderId }) : go('#/orders'));

  /** 找出需要填写收款账户的失败流水与金额 */
  async function resolve() {
    const me = await api.user.me();
    if (!me) { const e = new Error('请先授权登录'); e.code = 'NOT_LOGGED_IN'; throw e; }
    let paymentId = q.paymentId, amount = null, kind = '退款', orderId = q.orderId;
    if (orderId) {
      try {
        const o = await api.user.order(orderId);
        const p = o.payments.find(x => (paymentId ? x.paymentId === paymentId : x.type === '退款' && x.status === '失败'));
        if (p) { paymentId = p.paymentId; amount = p.amount; }
      } catch { /* 按其他途径查找 */ }
    }
    if (!paymentId || amount == null) {
      const d = await api.user.deposit.get();
      if (d.awaitingPayee && (!paymentId || paymentId === d.refundFailedPaymentId)) {
        paymentId = d.refundFailedPaymentId; kind = '退押金';
        const r = (d.deposit && d.deposit.refund) || (d.previousDeposit && d.previousDeposit.refund);
        amount = r ? r.amount : null;
      }
    }
    if (!paymentId) {
      const ts = await api.user.cs.tickets();
      const t = ts.find(x => x.awaitingPayee && x.refundFailedPaymentId);
      if (t) { paymentId = t.refundFailedPaymentId; orderId = orderId || t.orderId; }
    }
    return { me, paymentId, amount, kind, orderId };
  }

  async function start() {
    body.innerHTML = ui.skeleton('card', 2); bottom.hidden = true;
    let r;
    try { r = await resolve(); }
    catch (e) {
      if (!alive) return;
      body.innerHTML = '';
      body.append(e.code === 'NOT_LOGGED_IN'
        ? ui.empty({ icon: '🔑', title: '请先授权登录', text: '登录后才能填写收款账户', action: '去授权登录', onAction: () => go('#/guide') })
        : ui.retry({ text: '收款信息没有加载出来，请检查网络后重试', onRetry: start }));
      return;
    }
    if (!alive) return;
    if (!r.paymentId) {
      body.innerHTML = '';
      body.append(ui.empty({ icon: '✅', title: '没有需要填写的收款账户', text: '你的退款都已正常处理，无需再操作', action: '返回', onAction: back }));
      return;
    }
    form(r);
  }

  function form(r) {
    const masked = r.me.name || '';
    body.innerHTML = `
      <section class="m-card r-amt"><span class="m-hint">${ui.esc(r.kind)}原路退回没有成功</span>
        ${r.amount != null ? `<div class="m-big num">${ui.money(r.amount)}</div>` : ''}
        <span class="m-hint">填写本人实名的银行账户，平台公司将人工转账，不会再扣你任何费用。</span></section>
      <form id="r-form" class="m-fields" novalidate autocomplete="off">
        <div class="m-field"><label for="r-name">收款人姓名</label><input id="r-name" name="name" placeholder="须与实名信息一致${masked ? '（' + ui.esc(masked) + '）' : ''}" autocomplete="off"></div>
        <div class="m-field"><label for="r-bank">开户银行</label><input id="r-bank" name="bank" list="r-banks" placeholder="选择或输入银行名称" autocomplete="off"><datalist id="r-banks">${BANKS.map(b => `<option value="${b}">`).join('')}</datalist></div>
        <div class="m-field"><label for="r-acc">银行卡号</label><input id="r-acc" name="account" inputmode="numeric" placeholder="12 到 19 位数字" autocomplete="off"></div>
        <label class="m-check"><input type="checkbox" name="mine"><span>我确认这是本人名下的银行账户，信息无误后不能自行修改</span></label>
      </form>
      <div class="m-notice is-info"><span class="m-ic">ⓘ</span><span>户名与实名不一致时无法提交；转账到账后会通知你。</span></div>`;
    bottom.hidden = false;
    bottom.innerHTML = '<button type="submit" form="r-form" class="m-btn m-btn-primary" id="r-submit">提交收款账户</button>';
    const f = body.querySelector('#r-form'), btn = bottom.querySelector('#r-submit');
    const acc = f.elements.account;
    acc.addEventListener('input', () => { const d = acc.value.replace(/\D/g, '').slice(0, 19); acc.value = d.replace(/(.{4})/g, '$1 ').trim(); });
    f.addEventListener('submit', async e => {
      e.preventDefault();
      if (btn.getAttribute('aria-busy')) return;
      const v = ui.validate(f, {
        name: { label: '收款人姓名', required: true, min: 2, max: 20 },
        bank: { label: '开户银行', required: true, min: 2 },
        account: { label: '银行卡号', required: true, custom: x => !/^\d{12,19}$/.test(String(x).replace(/\s/g, '')) && '银行卡号应为 12 到 19 位数字' },
        mine: { label: '本人账户确认', required: true, requiredMessage: '请先确认这是本人账户' },
      });
      if (!v.ok) { if (v.errors.mine) ui.toast(v.errors.mine, { type: 'warn' }); ui.shake(f); return; }
      btn.setAttribute('aria-busy', 'true'); const old = btn.textContent; btn.innerHTML = '<span class="m-spin"></span>提交中';
      try {
        const res = await api.user.submitPayee({ paymentId: r.paymentId, name: v.values.name, bank: v.values.bank, account: v.values.account.replace(/\s/g, '') });
        if (!alive) return;
        done(res);
      } catch (err) {
        if (!alive) return;
        btn.removeAttribute('aria-busy'); btn.textContent = old;
        const field = err.code === 'PAYEE_NAME_MISMATCH' ? 'name' : err.code === 'INVALID' && /卡号/.test(err.message) ? 'account' : err.code === 'INVALID' && /银行/.test(err.message) ? 'bank' : null;
        if (field) {
          const item = f.elements[field].closest('.m-field'); item.classList.add('is-error');
          const m = document.createElement('div'); m.className = 'm-field-msg'; m.setAttribute('role', 'alert'); m.textContent = err.message; item.after(m);
          f.elements[field].addEventListener('input', () => { item.classList.remove('is-error'); m.remove(); }, { once: true });
          f.elements[field].focus(); ui.shake(item);
        } else ui.toast(err.message || '没有提交成功', { type: 'err', hint: '没有产生任何费用，请稍后重试' });
      }
    });
  }

  function done(res) {
    const p = res.payee || {};
    body.innerHTML = `<section class="m-card r-done"><div class="ok" aria-hidden="true">✓</div><h3>已提交</h3><p>${ui.esc(res.text || '平台公司财务将人工转账，到账后通知你')}</p>
      <div class="m-kv" style="width:100%"><span>收款人</span><span>${ui.esc(p.name || '')}</span></div>
      <div class="m-kv" style="width:100%"><span>开户银行</span><span>${ui.esc(p.bank || '')}</span></div>
      <div class="m-kv" style="width:100%"><span>银行卡号</span><span class="num">${ui.esc(p.account || '')}</span></div></section>`;
    bottom.hidden = false;
    bottom.innerHTML = '<button type="button" class="m-btn m-btn-primary">返回</button>';
    bottom.querySelector('button').addEventListener('click', back);
    ui.vibrate();
  }

  await start();
}

function unmount() { alive = false; }

export const route = '#/refund-account';
export const title = '填写收款账户';
export { mount, unmount };
export default { route, title, mount, unmount };

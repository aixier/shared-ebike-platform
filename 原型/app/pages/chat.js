// 客服对话：每单带订单号进入；客服智能体依订单事实提议退款，用户看到进度；原路退回失败时去填写收款账户（R-10、R-11、R-12）
import * as ui from '../../shared/ui.js';
import { api } from '../../shared/api.js';

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

style('p-chat', `
.k-body{gap:10px}
.k-time{align-self:center;font-size:12px;color:var(--ink-3);margin-top:4px}
.k-msg{display:flex;flex-direction:column;gap:2px;max-width:84%;animation:kIn var(--d-base) var(--ease) both}
.k-msg .who{font-size:12px;color:var(--ink-3);padding:0 4px}
.k-msg .bub{padding:10px 14px;border-radius:16px;font-size:15px;line-height:1.55;white-space:pre-wrap;word-break:break-word;background:var(--surface);box-shadow:var(--sh-1);border-top-left-radius:6px}
.k-msg.me{align-self:flex-end;align-items:flex-end}
.k-msg.me .bub{background:var(--orange-100);box-shadow:none;border-top-left-radius:16px;border-top-right-radius:6px}
.k-msg.pending .bub{opacity:.6}
.k-msg.failed .bub{box-shadow:inset 0 0 0 1.5px var(--err)}
.k-msg .st{font-size:12px;color:var(--ink-3);padding:0 4px}
.k-msg .st button{background:none;border:0;color:var(--err);text-decoration:underline;font-size:12px;padding:0 0 0 4px;min-height:32px}
.k-typing{align-self:flex-start;display:flex;gap:4px;padding:12px 14px;background:var(--surface);border-radius:16px;border-top-left-radius:6px;box-shadow:var(--sh-1)}
.k-typing i{width:6px;height:6px;border-radius:50%;background:var(--ink-3);animation:kDot 1s ease-in-out infinite}
.k-typing i:nth-child(2){animation-delay:.15s}.k-typing i:nth-child(3){animation-delay:.3s}
@keyframes kDot{0%,100%{opacity:.25;transform:translateY(0)}50%{opacity:1;transform:translateY(-2px)}}
@keyframes kIn{from{transform:translateY(8px);opacity:0}to{transform:none;opacity:1}}
.k-order{display:flex;justify-content:space-between;gap:10px;align-items:center;background:var(--surface);border-radius:var(--r-md);box-shadow:var(--sh-1);padding:10px 14px;text-align:left;border:0;width:100%;min-height:56px}
.k-order:hover{background:var(--orange-50)}
.k-order b{display:block;font-size:14px}.k-order span{font-size:12px;color:var(--ink-3)}
.k-order .end{font-size:15px;font-variant-numeric:tabular-nums;text-align:right}
.k-prog{background:var(--surface);border-radius:var(--r-lg);box-shadow:var(--sh-2);padding:14px;display:flex;flex-direction:column;gap:10px}
.k-prog h4{font-size:15px;display:flex;justify-content:space-between;align-items:center;gap:8px}
.k-steps{display:flex;align-items:flex-start}
.k-step{flex:1;display:flex;flex-direction:column;align-items:center;gap:4px;font-size:12px;color:var(--ink-3);position:relative;text-align:center}
.k-step i{width:24px;height:24px;border-radius:50%;background:var(--disabled-bg);color:var(--ink-2);display:grid;place-items:center;font-style:normal;font-size:13px;font-weight:700;position:relative;z-index:1}
.k-step::before{content:"";position:absolute;top:11px;left:-50%;width:100%;height:2px;background:var(--disabled-bg)}
.k-step:first-child::before{display:none}
.k-step.done i{background:var(--ok);color:#fff}.k-step.done::before{background:var(--ok)}
.k-step.cur i{background:var(--orange-500);color:var(--on-orange)}.k-step.cur::before{background:var(--ok)}
.k-step.bad i{background:var(--err);color:#fff}.k-step.bad::before{background:var(--ok)}
.k-step.done,.k-step.cur,.k-step.bad{color:var(--ink)}
.k-prog p{font-size:13px;color:var(--ink-2)}
.k-prog .m-btn{height:46px;font-size:16px}
.k-bottom{padding-top:8px}
.k-bar{display:flex;gap:8px;align-items:flex-end}
.k-input{flex:1;min-height:48px;max-height:112px;border:0;border-radius:var(--r-md);background:var(--surface);box-shadow:inset 0 0 0 1px var(--line);padding:12px 14px;font-size:16px;resize:none;line-height:1.4;transition:box-shadow var(--d-fast) var(--ease)}
.k-input:hover{box-shadow:inset 0 0 0 1px var(--orange-200)}
.k-input:focus{box-shadow:inset 0 0 0 2px var(--orange-500)}
.k-bar .m-btn{width:72px;height:48px;font-size:16px;flex:none}
.k-chips{display:flex;flex-wrap:wrap;gap:8px}
.k-chips button{min-height:44px;padding:0 14px;border-radius:var(--r-full);border:1px solid var(--orange-200);background:var(--orange-50);color:var(--orange-700);font-size:14px;transition:background-color var(--d-fast) var(--ease)}
.k-chips button:hover{background:var(--orange-100)}
.k-tk{display:flex;flex-direction:column;gap:2px}
.m-cell .main.k-tk .row1{display:flex;justify-content:space-between;gap:8px;align-items:center}
.m-cell .main.k-tk .row1 b{color:var(--ink)}
.m-cell .main.k-tk .row1 .chip{display:inline-flex;flex:none;font-size:12px}
.m-cell .main.k-tk .last{font-size:13px;color:var(--ink-2);display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
@media (prefers-reduced-motion:reduce){.k-msg,.k-typing i{animation:none}}
`);

const QUICK = ['这单费用不对，想核实', '调度费收得不合理，想退款', '车锁不上，订单一直没结束', '想开发票', '押金和退押的问题'];
const STATUS_CLS = { 待处理: 'warn', 处理中: 'warn', 待主管确认: 'warn', 已解决: 'ok' };

let alive = false, offs = [];

const addDays = (s, n) => { const d = new Date(T(s)); d.setDate(d.getDate() + n); return d; };
const md = d => `${d.getMonth() + 1}月${d.getDate()}日`;

function who(m) { return m.from === '客服智能体' ? '客服智能体' : m.from === '客服' ? '人工客服' : ''; }

async function mount(el, params = {}) {
  unmount();
  alive = true;
  const q = { ...hashQuery(), ...params };
  const root = pageRoot(el);
  root.innerHTML = '<div class="m-body k-body" id="k-body"></div><div class="m-bottom k-bottom" id="k-bottom"></div>';
  const body = root.querySelector('#k-body'), bottom = root.querySelector('#k-bottom');
  let me = null, tk = null, order = null, sending = false, draft = '';
  const entry = q.entry || (q.orderId ? '我的订单' : '我的');

  const needLogin = () => { body.innerHTML = ''; bottom.innerHTML = ''; body.append(ui.empty({ icon: '🔑', title: '请先授权登录', text: '登录后即可联系客服', action: '去授权登录', onAction: () => go('#/guide') })); };
  const loadFail = (e, again) => { body.innerHTML = ''; bottom.innerHTML = ''; body.append(ui.retry({ text: '对话没有加载出来，请检查网络后重试', onRetry: again })); };

  /* ---------- 入口分流 ---------- */
  async function start() {
    body.innerHTML = ui.skeleton('lines', 5); bottom.innerHTML = '';
    try {
      me = await api.user.me();
      if (!alive) return;
      if (!me) return needLogin();
      if (q.ticketId) { tk = await api.user.cs.ticket(q.ticketId); }
      else if (q.orderId) {
        const list = await api.user.cs.tickets();
        tk = list.filter(t => t.orderId === q.orderId && t.status !== '已解决')[0] || null;
      } else if (!q.new) { return await showList(); }
      if (!alive) return;
      const oid = (tk && tk.orderId) || q.orderId;
      order = null;
      if (oid) order = await api.user.order(oid).catch(() => null);
      if (!alive) return;
      draft = '';
      render();
    } catch (e) { if (alive) loadFail(e, start); }
  }

  /* ---------- 对话列表 ---------- */
  async function showList() {
    const list = await api.user.cs.tickets();
    if (!alive) return;
    bottom.innerHTML = '<button type="button" class="m-btn m-btn-primary" id="k-new">发起新咨询</button>';
    bottom.querySelector('#k-new').addEventListener('click', () => go('#/chat', { new: 1, entry: '我的' }));
    body.innerHTML = '';
    if (!list.length) { body.append(ui.empty({ icon: '💬', title: '还没有咨询记录', text: '有费用、押金、账户方面的问题，可以随时联系客服' })); return; }
    const now = new Date(T((await api.system.time()).now));
    body.innerHTML = `<div class="m-list">${list.map(t => {
      const last = t.messages[t.messages.length - 1];
      return `<button type="button" class="m-cell has-arrow" data-tk="${ui.esc(t.ticketId)}"><span class="main k-tk"><span class="row1"><b>${ui.esc(t.category)}</b><span class="chip ${STATUS_CLS[t.status] || 'mute'}">${ui.esc(t.status)}</span></span><span class="last">${ui.esc(last ? last.text : '')}</span><span>${ui.time(T(last ? last.at : t.createdAt), now)}${t.orderId ? ' · 订单 ' + ui.esc(t.orderId) : ''}</span></span></button>`;
    }).join('')}</div>`;
    body.querySelectorAll('[data-tk]').forEach(b => b.addEventListener('click', () => go('#/chat', { ticketId: b.dataset.tk })));
  }

  /* ---------- 退款进度卡 ---------- */
  function refundCard(now) {
    const p = tk.refundProposal; if (!p) return '';
    const msgs = tk.messages.map(m => m.text);
    const manualFail = msgs.some(t => /原路退回没有成功/.test(t));
    const transferred = msgs.some(t => /已通过银行转账退回/.test(t));
    const payeeSent = msgs.some(t => /^已填写收款账户/.test(t));
    const money = ui.money(p.amount);
    if (!(p.amount > 0) && p.decision !== '已驳回') return '';
    let steps, cap = '', cta = '', title = `退款 ${money}`, chip = '';
    const S = (a, b, c) => `<div class="k-steps"><div class="k-step ${a}"><i>${a === 'done' ? '✓' : '1'}</i>智能体提议</div><div class="k-step ${b}"><i>${b === 'done' ? '✓' : '2'}</i>${p.decision === '待主管确认' ? '主管确认' : '客服确认'}</div><div class="k-step ${c}"><i>${c === 'done' ? '✓' : c === 'bad' ? '!' : '3'}</i>${transferred || manualFail ? '转账到账' : '原路退回'}</div></div>`;
    if (p.decision === '已驳回') {
      steps = S('done', 'bad', 'todo'); chip = '<span class="chip mute">未通过</span>'; cap = '这笔退款没有通过，原因见上方客服的回复。如有不同意见，可以继续留言。';
    } else if (p.decision === '待确认' || p.decision === '待主管确认') {
      steps = S('done', 'cur', 'todo'); chip = '<span class="chip warn">处理中</span>';
      cap = p.decision === '待主管确认' ? `${money} 超过客服授权额度，已转客服主管确认，确认后会在这里通知你。` : `客服智能体依订单记录建议退还 ${money}，客服确认后原路退回，你不需要再操作。`;
    } else if (transferred) {
      steps = S('done', 'done', 'done'); chip = '<span class="chip ok">已转账</span>'; cap = `已通过银行转账退回 ${money}，请留意你填写的收款账户。`;
    } else if (payeeSent || (manualFail && !tk.awaitingPayee)) {
      // 原路失败后已填收款账户：等财务人工转账（此时 awaitingPayee 已变为 false，不能当成已原路退回）
      steps = S('done', 'done', 'cur'); chip = '<span class="chip warn">待转账</span>'; cap = '已收到你的收款账户，平台公司财务会人工转账，到账后通知你。';
    } else if (tk.awaitingPayee) {
      steps = S('done', 'done', 'bad'); chip = '<span class="chip err">原路退回失败</span>'; cap = `${money} 原路退回没有成功，这笔钱仍会退给你。请填写本人实名的收款账户，平台公司会人工转账。`;
      cta = '<button type="button" class="m-btn m-btn-primary" data-payee>填写收款账户</button>';
    } else {
      const done = tk.closedAt || (tk.messages[tk.messages.length - 1] || {}).at || now;
      steps = S('done', 'done', 'done'); chip = '<span class="chip ok">已原路退回</span>';
      cap = `${money} 已原路退回你的${order && order.channel === '支付宝' ? '支付宝' : '微信支付'}账户，预计 ${md(addDays(done, 1))}～${md(addDays(done, 3))} 到账。`;
    }
    return `<section class="k-prog" aria-label="退款进度"><h4><span>${title}</span>${chip}</h4>${steps}<p>${ui.esc(cap)}</p>${cta}</section>`;
  }

  /* ---------- 渲染 ---------- */
  function msgHtml(m, extra = '') {
    const me_ = m.from === '用户';
    return `<div class="k-msg${me_ ? ' me' : ''}${extra}">${me_ ? '' : `<span class="who">${who(m)}</span>`}<div class="bub">${ui.esc(m.text)}</div></div>`;
  }
  const tail = opts => (opts.pendingText ? msgHtml({ from: '用户', text: opts.pendingText }, ' pending') + '<div class="k-typing" aria-label="客服智能体正在回复"><i></i><i></i><i></i></div>' : '')
    + (opts.failedText ? msgHtml({ from: '用户', text: opts.failedText }, ' failed') + '<div class="k-msg me"><span class="st">没有发送成功，没有产生任何费用。<button type="button" data-retry>重新发送</button></span></div>' : '');
  async function render(opts = {}) {
    const now = new Date(T((await api.system.time()).now));
    if (!alive) return;
    let html = '';
    if (order) {
      const amt = order.bill ? ui.money(order.bill.payable) : (order.status === '开锁失败' ? '未扣费' : '—');
      html += `<button type="button" class="k-order" data-order="${ui.esc(order.orderId)}"><span><b>咨询订单 · ${ui.esc(order.brandName)} ${ui.time(T(order.startAt || order.requestAt), now)}</b><span class="mono">${ui.esc(order.orderId)}</span></span><span class="end">${ui.esc(amt)}<br><small style="color:var(--ink-3);font-size:12px">${ui.esc(order.statusText)}</small></span></button>`;
    }
    if (!tk) {
      html += `<div class="k-msg"><span class="who">客服智能体</span><div class="bub">你好，我是客服智能体。说说遇到的问题，我会先查订单记录再答复；需要退款时我先给出建议，由客服确认后原路退回。</div></div>
        ${opts.pendingText || opts.failedText ? '' : `<div class="k-chips" id="k-chips">${QUICK.map(t => `<button type="button">${ui.esc(t)}</button>`).join('')}</div>`}`;
    } else {
      let prev = null;
      for (const m of tk.messages) {
        const t = new Date(T(m.at));
        if (!prev || t - prev > 5 * 60000) html += `<div class="k-time">${ui.time(t, now)}</div>`;
        prev = t;
        html += msgHtml(m);
      }
      html += tail(opts) + refundCard(now);
      if (tk.status === '已解决' && !tk.refundProposal) html += '<div class="k-time">本次咨询已解决，再发消息会重新打开</div>';
    }
    if (!tk) html += tail(opts);
    body.innerHTML = html;
    body.scrollTop = body.scrollHeight;
    bar();
  }

  function bar() {
    const awaiting = tk && tk.awaitingPayee;
    bottom.innerHTML = `<div class="k-bar"><textarea class="k-input" id="k-in" rows="1" maxlength="300" placeholder="${tk ? '继续留言' : '说说遇到的问题'}" aria-label="消息内容"></textarea><button type="button" class="m-btn ${awaiting ? 'm-btn-secondary' : 'm-btn-primary'}" id="k-send" aria-disabled="true" data-why="还没有输入内容" data-next="写下你的问题再发送">发送</button></div>`;
    const inp = bottom.querySelector('#k-in'), send = bottom.querySelector('#k-send');
    inp.value = draft;
    const sync = () => { draft = inp.value; inp.style.height = 'auto'; inp.style.height = Math.min(112, inp.scrollHeight) + 'px'; draft.trim() ? ui.setEnabled(send) : ui.setDisabled(send, '还没有输入内容', '写下你的问题再发送'); };
    sync();
    inp.addEventListener('input', sync);
    inp.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); if (draft.trim()) submit(); } });
    send.addEventListener('click', () => { if (draft.trim()) submit(); });
    if (sending) inp.disabled = true;
  }

  async function submit(textOverride) {
    const text = (textOverride || draft).trim();
    if (!text || sending) return;
    sending = true; draft = '';
    await render({ pendingText: text });
    try {
      tk = tk ? await api.user.cs.send(tk.ticketId, text) : await api.user.cs.open({ orderId: q.orderId || null, entry, text });
      if (!alive) return;
      sending = false;
      await render();
      if (q.orderId && !q.ticketId) { /* 保持在同一对话，不改变地址栏 */ }
    } catch (e) {
      if (!alive) return;
      sending = false; draft = text;
      await render({ failedText: text });
      ui.toast(e.message || '没有发送成功', { type: 'err', hint: '没有产生任何费用，点“重新发送”再试' });
    }
  }

  body.addEventListener('click', e => {
    const chip = e.target.closest('#k-chips button'), ord = e.target.closest('[data-order]'), pe = e.target.closest('[data-payee]'), rt = e.target.closest('[data-retry]');
    if (chip) { draft = chip.textContent; bar(); const i = bottom.querySelector('#k-in'); i.focus(); }
    else if (ord) go('#/order-detail', { orderId: ord.dataset.order });
    else if (pe) {
      const pid = tk && tk.refundFailedPaymentId;
      go('#/refund-account', { paymentId: pid, orderId: tk.orderId, ticketId: tk.ticketId });
    } else if (rt) submit(draft);
  });

  offs.push(api.on('ticket.changed', async ev => {
    if (!alive || sending || !tk || ev.ticketId !== tk.ticketId) return;
    try { tk = await api.user.cs.ticket(tk.ticketId); if (alive) { if (order) order = await api.user.order(tk.orderId).catch(() => order); render(); } } catch { /* 下次事件再刷新 */ }
  }));
  offs.push(api.on('data.reset', () => { if (alive) location.reload(); }));

  await start();
}

function unmount() {
  alive = false;
  offs.forEach(f => { try { f(); } catch { /* 已取消 */ } });
  offs = [];
}

export const route = '#/chat';
export const title = '客服';
export { mount, unmount };
export default { route, title, mount, unmount };

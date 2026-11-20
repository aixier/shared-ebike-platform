// 我的订单：历史订单列表（分页加载）、状态筛选、每单一键联系客服（R-12）
import * as ui from '../../shared/ui.js';
import { api } from '../../shared/api.js';

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

const FILTERS = [['', '全部'], ['待付款', '待付款'], ['已结束', '已结束'], ['开锁失败', '开锁失败']];
const PAGE_SIZE = 20;

style('p-orders', `
.o-list{display:flex;flex-direction:column;gap:12px}
.o-item{background:var(--surface);border-radius:var(--r-lg);box-shadow:var(--sh-1);overflow:hidden;animation:oIn var(--d-base) var(--ease) both}
@keyframes oIn{from{transform:translateY(8px);opacity:0}to{transform:none;opacity:1}}
.o-item .m-cell{border-bottom:0;min-height:64px}
.o-item .m-cell .end{max-width:42%}
.o-item .m-cell .end b{display:block;font-size:17px}
.o-foot{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:0 8px 4px 14px;border-top:1px dashed var(--line);min-height:48px}
.o-foot .no{font-size:12px;color:var(--ink-3);min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.o-foot .m-btn{width:auto;height:44px;padding:0 14px;font-size:14px;flex:none}
.o-more{padding:8px 0 4px;text-align:center}
.o-more .m-btn{width:auto;margin:0 auto;padding:0 24px;height:44px;font-size:14px}
.o-end{text-align:center;font-size:12px;color:var(--ink-3);padding:8px 0 4px}
@media (prefers-reduced-motion:reduce){.o-item{animation:none}}
`);

let alive = false, offs = [], io = null;

function chipOf(o) {
  const t = o.statusText;
  if (o.status === '骑行中') return 'brand';
  if (o.status === '开锁失败' || /0 元|无需支付/.test(t)) return 'mute';
  if (/欠费/.test(t)) return 'err';
  if (/待支付|稍后|处理中|开锁中/.test(t)) return 'warn';
  if (/退款/.test(t)) return 'brand';
  return 'ok';
}
function summary(o, now) {
  const when = ui.time(T(o.startAt || o.requestAt), now);
  let sub, amt;
  if (o.status === '开锁失败') { sub = `${o.unlockFailReason || '车辆无响应'}，没有扣费`; amt = '¥0.0'; }
  else if (o.status === '骑行中') { sub = '正在骑行'; amt = '计费中'; }
  else {
    sub = `骑行 ${ui.duration(o.durationSec)} · ${ui.km(o.distanceKm)}`;
    amt = o.bill ? ui.money(o.bill.payable) : (o.degraded ? '稍后收取' : '—');
  }
  return { title: `${when} · ${o.brandName}`, sub, amt };
}
function itemHtml(o, now) {
  const s = summary(o, now);
  return `<article class="o-item" data-id="${ui.esc(o.orderId)}">
    <button type="button" class="m-cell has-arrow" data-open="${ui.esc(o.orderId)}" aria-label="查看订单 ${ui.esc(o.orderId)}">
      <span class="main"><b>${ui.esc(s.title)}</b><span>${ui.esc(s.sub)}</span></span>
      <span class="end"><b class="num">${ui.esc(s.amt)}</b><small><span class="chip ${chipOf(o)}">${ui.esc(o.statusText)}</span></small></span>
    </button>
    <div class="o-foot"><span class="no mono">${ui.esc(o.orderId)}</span>
      <button type="button" class="m-btn m-btn-ghost" data-cs="${ui.esc(o.orderId)}">联系客服</button></div>
  </article>`;
}

async function mount(el, params = {}) {
  unmount();
  alive = true;
  const root = pageRoot(el);
  root.innerHTML = `<div class="m-body" id="o-body">
      <div id="o-alert"></div>
      <div class="m-seg" role="tablist" aria-label="订单筛选" id="o-seg">${FILTERS.map(([k, l]) => `<button type="button" role="tab" data-k="${k}" aria-selected="${k === (params.status || '')}">${l}</button>`).join('')}</div>
      <div id="o-list" class="o-list" aria-live="polite"></div>
      <div id="o-more"></div>
    </div>`;
  const $ = s => root.querySelector(s);
  const body = $('#o-body'), listEl = $('#o-list'), moreEl = $('#o-more'), alertEl = $('#o-alert');
  let status = params.status || '', page = 0, total = 0, loaded = 0, busy = false, token = 0, now = new Date();

  const showSkeleton = () => { listEl.innerHTML = ui.skeleton('card', 4); moreEl.innerHTML = ''; };
  const sentinel = () => {
    moreEl.innerHTML = '';
    if (io) { io.disconnect(); io = null; }
    if (loaded >= total) { if (total > PAGE_SIZE) moreEl.innerHTML = '<div class="o-end">没有更多订单了</div>'; return; }
    const w = document.createElement('div'); w.className = 'o-more';
    w.innerHTML = '<button type="button" class="m-btn m-btn-secondary is-sm">加载更多</button>';
    w.querySelector('button').addEventListener('click', () => load(false));
    moreEl.append(w);
    if ('IntersectionObserver' in window) {
      io = new IntersectionObserver(es => { if (es.some(e => e.isIntersecting) && !busy) load(false); }, { root: body, rootMargin: '0px 0px 120px 0px' });
      io.observe(w);
    }
  };
  const fail = (e, again) => {
    if (e && e.code === 'NOT_LOGGED_IN') return ui.empty({ icon: '🔑', title: '请先授权登录', text: '登录后即可查看你的骑行订单', action: '去授权登录', onAction: () => go('#/guide') });
    return ui.retry({ text: '订单没有加载出来，请检查网络后重试', onRetry: again });
  };

  async function load(reset) {
    if (busy && !reset) return;
    const my = ++token;
    busy = true;
    if (reset) { page = 0; loaded = 0; showSkeleton(); }
    else { moreEl.innerHTML = ''; moreEl.append(ui.loading('正在加载更多…')); }
    try {
      const [r, t] = await Promise.all([api.user.orders({ page: page + 1, pageSize: PAGE_SIZE, status: status || undefined }), api.system.time()]);
      if (!alive || my !== token) return;
      now = new Date(T(t.now));
      if (reset) listEl.innerHTML = '';
      page = r.page; total = r.total; loaded += r.rows.length;
      if (!total) {
        listEl.innerHTML = ''; moreEl.innerHTML = '';
        const emptyText = { '': ['还没有骑行记录', '扫码骑一次，这里就会有记录', '去扫码'], '待付款': ['没有待付款的订单', '骑行费用都已结清', null], '已结束': ['还没有已结束的订单', '还车后这里会出现账单', null], '开锁失败': ['没有开锁失败的订单', '开锁失败的订单不会扣费，会记在这里', null] }[status];
        listEl.append(ui.empty({ icon: '🧾', title: emptyText[0], text: emptyText[1], action: emptyText[2], onAction: () => go('#/scan') }));
        return;
      }
      listEl.insertAdjacentHTML('beforeend', r.rows.map(o => itemHtml(o, now)).join(''));
      sentinel();
    } catch (e) {
      if (!alive || my !== token) return;
      if (reset) { listEl.innerHTML = ''; moreEl.innerHTML = ''; listEl.append(fail(e, () => load(true))); }
      else { moreEl.innerHTML = ''; moreEl.append(ui.retry({ text: '更多订单没有加载出来', action: '重试', onRetry: () => load(false) })); }
    } finally { if (my === token) busy = false; }
  }

  async function loadAlert() {
    try {
      const me = await api.user.me();
      if (!alive || !me) return;
      if (me.arrears > 0) {
        alertEl.innerHTML = `<div class="m-notice is-err"><span class="m-ic">!</span><span>你有 ${ui.money(me.arrears)} 未付的骑行费用，补付后即可开锁。<button type="button" class="m-link" data-debt>去补付</button></span></div>`;
        alertEl.querySelector('[data-debt]').addEventListener('click', () => go('#/debt'));
      } else if (me.unpaidOrderIds && me.unpaidOrderIds.length) {
        alertEl.innerHTML = `<div class="m-notice"><span class="m-ic">ⓘ</span><span>有 ${me.unpaidOrderIds.length} 单待支付，超过期限未付会从押金自动抵扣。<button type="button" class="m-link" data-pay>查看待付款</button></span></div>`;
        alertEl.querySelector('[data-pay]').addEventListener('click', () => setFilter('待付款'));
      } else alertEl.innerHTML = '';
    } catch { alertEl.innerHTML = ''; }
  }

  function setFilter(k) {
    status = k;
    root.querySelectorAll('#o-seg button').forEach(b => b.setAttribute('aria-selected', b.dataset.k === k));
    load(true);
  }

  $('#o-seg').addEventListener('click', e => { const b = e.target.closest('button[data-k]'); if (b && b.dataset.k !== status) setFilter(b.dataset.k); });
  root.addEventListener('click', e => {
    const open = e.target.closest('[data-open]'), cs = e.target.closest('[data-cs]');
    if (open) go('#/order-detail', { orderId: open.dataset.open });
    else if (cs) go('#/chat', { orderId: cs.dataset.cs, entry: '我的订单' });
  });

  offs.push(api.on('order.updated', async ev => {
    if (!alive) return;
    const me = await api.user.me().catch(() => null);
    if (alive && me && ev.userId === me.userId && !busy) { loadAlert(); load(true); }
  }));
  // 平台恢复后降级单逐单计价收款，列表要跟着刷新
  offs.push(api.on('degrade.changed', ev => { if (alive && ev && ev.degraded === false) { loadAlert(); load(true); } }));
  offs.push(api.on('data.reset', () => { if (alive) location.reload(); }));

  loadAlert();
  await load(true);
}

function unmount() {
  alive = false;
  offs.forEach(f => { try { f(); } catch { /* 已取消 */ } });
  offs = [];
  if (io) { io.disconnect(); io = null; }
}

export const route = '#/orders';
export const title = '我的订单';
export { mount, unmount };
export default { route, title, mount, unmount };

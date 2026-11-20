// 月卡与优惠券：购买月卡、权益与有效期、领取与查看优惠券、结账自动使用说明（R-02、R-19，BR-07）
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

style('p-cards', `
.c-hero{background:linear-gradient(135deg,var(--orange-100),var(--orange-50));border-radius:var(--r-lg);padding:16px;box-shadow:var(--sh-1);display:flex;flex-direction:column;gap:6px}
.c-hero.is-slide{animation:cPop var(--d-slow) var(--ease) both}
@keyframes cPop{from{transform:translateY(16px) scale(.98);opacity:0}to{transform:none;opacity:1}}
.c-hero .top{display:flex;justify-content:space-between;align-items:center}
.c-hero .top b{font-size:15px}
.c-hero .big{font-size:30px;font-weight:700;line-height:1.2}
.c-hero .big small{font-size:14px;font-weight:500;color:var(--ink-2)}
.c-hero p{font-size:13px;color:var(--ink-2)}
.c-sec{font-size:15px;font-weight:600;margin-bottom:6px}
.c-pt{display:flex;gap:10px;font-size:14px;color:var(--ink-2);padding:5px 0}
.c-pt i{flex:none;width:20px;height:20px;border-radius:50%;background:var(--ok-bg);color:var(--ok);display:grid;place-items:center;font-style:normal;font-size:12px;font-weight:700;margin-top:2px}
.c-hist{display:flex;justify-content:space-between;gap:8px;padding:9px 0;border-bottom:1px dashed var(--line);font-size:14px}
.c-hist:last-child{border:0}
.c-hist small{display:block;font-size:12px;color:var(--ink-3)}
.c-cp{display:flex;background:var(--surface);border-radius:var(--r-lg);box-shadow:var(--sh-1);overflow:hidden;min-height:84px}
.c-cp .amt{flex:none;width:96px;background:var(--orange-100);display:flex;flex-direction:column;align-items:center;justify-content:center;color:var(--orange-700);border-right:2px dotted var(--orange-200)}
.c-cp .amt b{font-size:26px;font-variant-numeric:tabular-nums;line-height:1.1}
.c-cp .amt span{font-size:12px}
.c-cp .info{flex:1;min-width:0;padding:12px 14px;display:flex;flex-direction:column;justify-content:center;gap:2px}
.c-cp .info b{font-size:15px}
.c-cp .info span{font-size:12px;color:var(--ink-3)}
.c-cp .act{flex:none;display:flex;align-items:center;padding-right:12px}
.c-cp .act .m-btn{width:auto;height:44px;padding:0 16px;font-size:14px}
.c-cp.is-off{opacity:.6}
.c-cp.is-off .amt{background:var(--disabled-bg);color:var(--ink-2)}
.c-cp.is-new{animation:cPop var(--d-slow) var(--ease) both}
.c-more{align-self:center;background:none;border:0;color:var(--orange-700);font-size:14px;min-height:44px;padding:0 16px;border-radius:var(--r-md)}
.c-more:hover{background:var(--orange-50)}
.c-sheet-total{display:flex;justify-content:space-between;align-items:baseline}
.c-sheet-total b{font-size:30px}
@media (prefers-reduced-motion:reduce){.c-hero.is-slide,.c-cp.is-new{animation:none}}
`);

let alive = false, offs = [];

const md = s => { const d = new Date(T(s)); return `${d.getMonth() + 1}月${d.getDate()}日`; };
const ymd = s => { const d = new Date(T(s)); return `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日`; };
const daysLeft = (end, now) => Math.max(0, Math.ceil((new Date(T(end)) - now) / 864e5));
const CARD_CHIP = { 生效中: 'ok', 未生效: 'brand', 已过期: 'mute' };

async function mount(el, params = {}) {
  unmount();
  alive = true;
  const q = { ...hashQuery(), ...params };
  const root = pageRoot(el);
  root.innerHTML = `<div class="m-body" id="c-body">
      <div class="m-seg" role="tablist" aria-label="月卡与优惠券" id="c-seg"><button type="button" role="tab" data-k="card">月卡</button><button type="button" role="tab" data-k="coupon">优惠券</button></div>
      <div id="c-main" style="display:flex;flex-direction:column;gap:12px"></div></div>
    <div class="m-bottom" id="c-bottom" hidden></div>`;
  const main = root.querySelector('#c-main'), bottom = root.querySelector('#c-bottom'), seg = root.querySelector('#c-seg');
  let tab = q.tab === 'coupon' ? 'coupon' : 'card';
  let D = null, justBought = null, justClaimed = null, showHist = false;

  const setSeg = () => seg.querySelectorAll('button').forEach(b => b.setAttribute('aria-selected', b.dataset.k === tab));

  async function load(silent) {
    if (!silent) { main.innerHTML = ui.skeleton('card', 3); bottom.hidden = true; }
    try {
      const [me, mc, coupons, tpls, t] = await Promise.all([api.user.me(), api.user.monthCard.get(), api.user.coupons.list(), api.user.coupons.templates(), api.system.time()]);
      if (!alive) return;
      if (!me) return needLogin();
      D = { me, mc, coupons, tpls, now: new Date(T(t.now)) };
      draw();
    } catch (e) {
      if (!alive) return;
      if (e.code === 'NOT_LOGGED_IN') return needLogin();
      main.innerHTML = ''; bottom.hidden = true;
      main.append(ui.retry({ text: '月卡与优惠券没有加载出来，请检查网络后重试', onRetry: () => load() }));
    }
  }
  function needLogin() {
    main.innerHTML = ''; bottom.hidden = true;
    main.append(ui.empty({ icon: '🔑', title: '请先授权登录', text: '登录后即可购买月卡、领取优惠券', action: '去授权登录', onAction: () => go('#/guide') }));
  }

  function draw() { setSeg(); tab === 'card' ? drawCard() : drawCoupon(); }

  /* ---------- 月卡 ---------- */
  function buyState() {
    const { me } = D;
    if (me.account.status !== '正常') return { why: '账户已冻结，暂时不能购买月卡', next: '先完成解冻', go: '#/account', label: '去解冻' };
    if (me.realName.status !== '已核验') return { why: '完成实名核验后才能购买月卡', next: '先去实名核验', go: '#/guide', label: '去实名核验' };
    return null;
  }
  function drawCard() {
    const { mc, me, now } = D;
    const cards = mc.cards;
    const active = cards.find(c => c.status === '生效中' && new Date(T(c.endAt)) > now);
    const queued = cards.filter(c => c.status === '未生效');
    const rule = mc.rule;
    let hero;
    if (active) {
      const n = daysLeft(active.endAt, now);
      hero = `<section class="c-hero${justBought ? ' is-slide' : ''}"><div class="top"><b>骑行月卡</b><span class="chip ok">生效中</span></div>
        <div class="big num">${md(active.endAt)}<small> 到期 · 还剩 ${n} 天</small></div>
        <p>每单骑行费最多抵 ${ui.money(active.perOrderCap)}，调度费不抵。${active.usedOrderIds && active.usedOrderIds.length ? `已为你抵扣 ${active.usedOrderIds.length} 单。` : ''}</p>
        ${queued.length ? `<p>已购续期：${md(queued[0].startAt)} 起生效，到 ${md(queued[queued.length - 1].endAt)}</p>` : ''}</section>`;
    } else {
      hero = `<section class="c-hero"><div class="top"><b>骑行月卡</b><span class="chip mute">未开通</span></div>
        <div class="big num">${ui.money(rule.price)}<small> / ${rule.days} 天</small></div><p>${ui.esc(rule.text)}</p></section>`;
    }
    justBought = null;
    const pts = [
      `${rule.days} 天内每单骑行费最多抵 ${ui.money(rule.perOrderCap)}，用得越多越划算`,
      '只抵骑行费，站点外还车的调度费不在抵扣范围',
      '结账时自动使用，不用手动选择；可与优惠券叠加',
      '到期不会自动续费；有效期内再买会顺延',
    ];
    const hist = cards.filter(c => c !== active).sort((a, b) => (a.startAt < b.startAt ? 1 : -1));
    main.innerHTML = hero + `<section class="m-card"><div class="c-sec">月卡权益</div>${pts.map(p => `<div class="c-pt"><i>✓</i><span>${ui.esc(p)}</span></div>`).join('')}</section>`
      + (hist.length ? `<section class="m-card"><div class="c-sec">月卡记录</div>${hist.map(c => `<div class="c-hist"><span>${md(c.startAt)} – ${md(c.endAt)}<small>${ui.money(c.price)} · 每单最多抵 ${ui.money(c.perOrderCap)}</small></span><span class="chip ${CARD_CHIP[c.status] || 'mute'}">${ui.esc(c.status)}</span></div>`).join('')}</section>` : '');
    const bs = buyState();
    bottom.hidden = false;
    bottom.innerHTML = `<button type="button" class="m-btn m-btn-primary" id="c-buy">${active ? `续购月卡 ${ui.money(rule.price)}` : `购买月卡 ${ui.money(rule.price)}`}</button>${bs ? `<p class="m-why">${ui.esc(bs.why)}，<button type="button" data-go="${bs.go}">${bs.label}</button></p>` : ''}`;
    const btn = bottom.querySelector('#c-buy');
    if (bs) ui.setDisabled(btn, bs.why, bs.next);
    btn.addEventListener('click', () => { if (!bs) buySheet(active); });
    const g = bottom.querySelector('[data-go]'); g && g.addEventListener('click', () => go(g.dataset.go));
  }

  function buySheet(active) {
    const { mc, me, now } = D, rule = mc.rule;
    const start = active ? new Date(T(active.endAt)) : now;
    const end = new Date(start.getTime() + rule.days * 864e5);
    const fmt = d => `${d.getMonth() + 1}月${d.getDate()}日`;
    // 与账单页同一个支付半屏；有效期写在说明里，付款前就能看到
    nativePay({ amount: rule.price, subject: active ? '续购骑行月卡' : '购买骑行月卡', desc: `${fmt(start)}–${fmt(end)} 有效${active ? '（到期后顺延）' : ''}`, run: () => api.user.monthCard.buy() })
      .then(async res => {
        if (!res) return;
        justBought = true;
        ui.toast(active ? '已续购，到期后自动接上' : '月卡已开通', { type: 'ok', hint: `有效期至 ${fmt(end)}` });
        if (alive) await load(true);
      });
  }

  /* ---------- 优惠券 ---------- */
  function cpState(c) { return c.status === '未使用' && new Date(T(c.expireAt)) < D.now ? '已过期' : c.status; }
  function drawCoupon() {
    const { coupons, tpls, now } = D;
    bottom.hidden = true;
    const usable = coupons.filter(c => cpState(c) === '未使用').sort((a, b) => (a.expireAt < b.expireAt ? -1 : 1));
    const past = coupons.filter(c => cpState(c) !== '未使用');
    const claimables = tpls.filter(t => /领取/.test(t.issue));
    const cpHtml = (c, off) => {
      const n = daysLeft(c.expireAt, now);
      const st = cpState(c);
      return `<article class="c-cp${off ? ' is-off' : ''}${justClaimed === c.couponId ? ' is-new' : ''}"><div class="amt"><b>${ui.money(c.amount).replace('¥', '')}</b><span>元</span></div>
        <div class="info"><b>${ui.esc(c.name)}</b><span>${off ? (st === '已使用' ? `已于 ${md(c.usedAt)} 使用` : `${md(c.expireAt)} 已过期`) : `${md(c.expireAt)} 到期${n <= 3 ? '，还剩 ' + n + ' 天' : ''}`}</span></div>
        ${off ? `<div class="act"><span class="chip mute">${st}</span></div>` : n <= 3 ? '<div class="act"><span class="chip warn">即将到期</span></div>' : ''}</article>`;
    };
    justClaimed = null;
    main.innerHTML = `<section class="m-card"><div class="c-sec">结账时自动使用</div>
        <div class="c-pt"><i>1</i><span>先用月卡，抵骑行费</span></div>
        <div class="c-pt"><i>2</i><span>再自动选一张券：抵扣额最大的优先，金额相同先用快到期的</span></div>
        <div class="c-pt"><i>3</i><span>券可以抵调度费，每单最多用一张，不用你手动选</span></div></section>`
      + (claimables.length ? `<div class="m-section-title">可领取</div>${claimables.map(t => `<article class="c-cp"><div class="amt"><b>${ui.money(t.amount).replace('¥', '')}</b><span>元</span></div><div class="info"><b>${ui.esc(t.name)}</b><span>领取后 ${t.validDays} 天内有效 · 每人每期 ${t.perUser || 1} 张</span></div><div class="act"><button type="button" class="m-btn m-btn-primary is-sm" data-claim="${ui.esc(t.templateId)}" ${t.claimable ? '' : `aria-disabled="true" data-why="${ui.esc(t.why || '当前不能领取')}" data-next="${t.why === '本期已领取' ? '下一期开放时再来领取' : '开放时间到了再来领取'}"`}>${t.claimable ? '领取' : (t.why === '本期已领取' ? '已领取' : '未开放')}</button></div></article>`).join('')}${claimables.some(t => !t.claimable && t.why && t.why !== '本期已领取') ? `<p class="m-hint" style="padding:0 4px">${ui.esc(claimables.find(t => !t.claimable && t.why !== '本期已领取').why)}</p>` : ''}` : '')
      + `<div class="m-section-title">我的优惠券（${usable.length}）</div>`
      + (usable.length ? usable.map(c => cpHtml(c, false)).join('') : '<div class="m-card is-flat" style="text-align:center;color:var(--ink-3);font-size:14px">暂时没有可用的优惠券。关注开放领取的券，或骑行后会收到关怀券。</div>')
      + (past.length ? `<button type="button" class="c-more" id="c-hist">${showHist ? '收起' : '查看'}已使用与已过期（${past.length}）</button>${showHist ? past.map(c => cpHtml(c, true)).join('') : ''}` : '');
    main.querySelectorAll('[data-claim]').forEach(b => b.addEventListener('click', () => claim(b)));
    const h = main.querySelector('#c-hist'); h && h.addEventListener('click', () => { showHist = !showHist; drawCoupon(); });
  }
  async function claim(btn) {
    if (btn.getAttribute('aria-busy')) return;
    btn.setAttribute('aria-busy', 'true'); const old = btn.textContent; btn.innerHTML = '<span class="m-spin"></span>';
    try {
      const c = await api.user.coupons.claim(btn.dataset.claim);
      justClaimed = c && c.couponId;
      ui.toast('已领取 1 张优惠券', { type: 'ok', hint: '结账时自动使用' }); ui.vibrate();
      if (alive) await load(true);
    } catch (e) {
      btn.removeAttribute('aria-busy'); btn.textContent = old;
      ui.toast(e.message || '没有领取成功', { type: 'err', hint: e.code === 'COUPON_NOT_OPEN' ? '开放时间到了再来领取' : e.code === 'COUPON_LIMIT' ? '本期已领过，不能重复领取' : '请稍后重试' });
      if (alive && (e.code === 'COUPON_LIMIT' || e.code === 'COUPON_NOT_OPEN')) load(true);
    }
  }

  seg.addEventListener('click', e => { const b = e.target.closest('button[data-k]'); if (b && b.dataset.k !== tab && D) { tab = b.dataset.k; draw(); } else if (b) { tab = b.dataset.k; setSeg(); } });
  offs.push(api.on('pricing.changed', () => { if (alive && D) load(true); }));
  offs.push(api.on('data.reset', () => { if (alive) location.reload(); }));

  setSeg();
  await load();
}

function unmount() {
  alive = false;
  offs.forEach(f => { try { f(); } catch { /* 已取消 */ } });
  offs = [];
}

export const route = '#/cards';
export const title = '月卡与优惠券';
export { mount, unmount };
export default { route, title, mount, unmount };

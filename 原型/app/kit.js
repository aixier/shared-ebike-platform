// 用户端内部辅助：地图、支付半屏、欠费与解冻浮层、失败卡、附近车列表。
// 主路径页面（scan、guide、ride、bill、debt）共用；页面之间不互相引用，只引用这个文件。
import * as ui from '../shared/ui.js';
import { api } from '../shared/api.js';

export { ui, api };
export const esc = ui.esc;
export const app = () => window.__app;
export const skin = () => document.getElementById('app').dataset.skin;
const sleep = ms => new Promise(r => setTimeout(r, ms));
export const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

/** 业务"现在"（调试时钟可能快进），返回 Date */
export async function nowDate() {
  const t = await api.system.time();
  return new Date(String(t.now).replace(' ', 'T'));
}
const toD = s => new Date(String(s).replace(' ', 'T'));
export const hm = s => (s ? String(s).slice(11, 16) : '');
export const mdhm = s => (s ? `${+String(s).slice(5, 7)}月${+String(s).slice(8, 10)}日 ${String(s).slice(11, 16)}` : '');
export { toD };

/* ---------------- 几何 ---------------- */
export const MLAT = 110540;
export const mLon = lat => 111320 * Math.cos(lat * Math.PI / 180);
export function dist(a, b) {
  const dx = (a[0] - b[0]) * mLon((a[1] + b[1]) / 2), dy = (a[1] - b[1]) * MLAT;
  return Math.hypot(dx, dy);
}
export function inPoly(pt, poly) {
  let c = false;
  for (let i = 0, j = poly.length - 1; i < poly.length; j = i++) {
    const [xi, yi] = poly[i], [xj, yj] = poly[j];
    if ((yi > pt[1]) !== (yj > pt[1]) && pt[0] < (xj - xi) * (pt[1] - yi) / (yj - yi) + xi) c = !c;
  }
  return c;
}
export function centroid(poly) {
  const n = poly.length - 1 || 1;
  let x = 0, y = 0;
  for (let i = 0; i < poly.length - 1; i++) { x += poly[i][0]; y += poly[i][1]; }
  return [x / n, y / n];
}
export const insidePoint = st => (inPoly(st.center, st.fence) ? st.center : centroid(st.fence));

/** 站点与桩（地图底图数据，取自 api.user.stations()；取不到时地图只画道路与车辆） */
export async function getGeo() {
  try {
    const r = await api.user.stations();
    return { stations: r.stations || [], piles: r.piles || [] };
  } catch { return { stations: [], piles: [] }; }
}
/** 手机当前定位（扫码首页用；骑行中以订单里的车辆位置为准） */
export const HOME_LOC = [104.0667, 30.5999];

/* ---------------- 小部件 ---------------- */
export const BIKE_SVG = '<svg viewBox="0 0 32 32" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="8" cy="22" r="4.5"/><circle cx="24" cy="22" r="4.5"/><path d="M8 22l5-10h6l5 10M13 12l4 10M19 12h4"/></svg>';

export function batteryBar(b) {
  if (b == null) return '<span class="sh-bat-none" style="color:var(--ink-3,#8a7c70);font-size:12px">电量 —</span>'; // 车辆没有上报电量时不造数
  const cls = b < 20 ? 'is-low' : b < 40 ? 'is-mid' : 'is-ok';
  return `<span class="sh-bat-bar ${cls}" style="--lv:${b}%"><i></i>${b}%</span>`;
}

export function bikeCard(v, me, extra = '') {
  const chips = [];
  if (me) {
    if (me.deposit.mode === '免押') chips.push('<span class="chip ok">已免押</span>');
    else if (me.deposit.mode === '押金' && me.deposit.status === '已缴') chips.push('<span class="chip brand">已缴押金</span>');
    if (me.monthCard && me.monthCard.status === '生效中') chips.push('<span class="chip brand">月卡生效中</span>');
  }
  return `<div class="m-bike"><div class="sh-brandic" style="background:${esc(v.color || '#F97316')}">${BIKE_SVG}</div>
    <div class="sh-grow"><b>${esc(v.displayName)}</b><span>${esc(v.plateNo)} · ${esc(v.model || '电单车')}</span><div style="margin-top:2px">${batteryBar(v.battery)}</div></div>
    <div style="display:grid;gap:4px;justify-items:end">${chips.join('')}${extra}</div></div>`;
}

export function priceCard(price, me) {
  const mc = me && me.monthCard && me.monthCard.status === '生效中' ? `<span class="chip brand">月卡每单最多抵 ${ui.money(me.monthCard.perOrderCap)}</span>` : '';
  return `<div class="m-card sh-price"><div class="m-big num">${ui.money(price.startFee)}<em>起步，含 ${price.startMinutes} 分钟 · 之后每分钟 ${ui.money(price.perMinute)}</em></div>
    <p class="m-hint">${esc(price.dispatchText)}。月卡与优惠券结账时自动使用。</p>${mc ? `<div style="margin-top:8px">${mc}</div>` : ''}</div>`;
}

const FAIL_TITLE = {
  CODE_NOT_ENABLED: '这辆车还不能骑', BRAND_PAUSED: '这个品牌暂停可租', CODE_INVALID: '这辆车的码已失效', BRAND_NOT_AVAILABLE: '这个品牌暂未开放',
  HAS_ARREARS: '有一笔欠费待补付', UNPAID_ORDER: '上一单还没付款', DEPOSIT_REQUIRED: '开通免押或缴押金后即可骑行', DEPOSIT_INSUFFICIENT: '押金需要补缴',
  DEPOSIT_REFUNDING: '押金退还中', ACCOUNT_FROZEN: '账户已冻结', NEED_ONBOARDING: '首次使用，先完成开通', NOT_LOGGED_IN: '请先授权登录',
  HAS_RIDING: '你有一单正在骑行', UNLOCK_NO_RESPONSE: '这辆车暂时打不开', UNLOCK_LOW_BATTERY: '这辆车暂时打不开', UNLOCK_FAULT: '这辆车暂时打不开',
  UNLOCK_OCCUPIED: '这辆车暂时打不开', TIMEOUT: '这辆车暂时打不开', CODE_NOT_FOUND: '没有识别到政府码', NETWORK: '请求没有完成', SERVER_BUSY: '服务有点忙',
  DEGRADE_REJECT_ARREARS: '有欠费，暂不能开锁', DEGRADE_REJECT_REFUNDING: '押金退还中', DEGRADE_USER_LIMIT: '上一单还在处理', DEGRADE_PAUSED_TIGHT: '暂时不能开新单',
  DEGRADE_STOPPED: '暂时不能开新单', DEGRADE_NEW_USER_UNAVAILABLE: '暂时不能开新单', AUTH_UNAVAILABLE: '授权暂时不可用',
  UNDER_AGE: '暂时不能骑行', REALNAME_NAME_MISMATCH: '核验没有通过', CREDIT_SCORE_LOW: '暂未达到免押条件',
};
/** ApiError 的 message 不可枚举，展开前先取出 */
export const plain = (e, over = {}) => ({ ...e, code: e.code, message: e.message, ...over });
export const failTitle = e => FAIL_TITLE[e.code] || '暂时无法完成';
/** 是否收费的一句话：已在 message 里说过则不重复 */
export function chargeLine(e) {
  if (e.charged === false) return /未扣费|没有收费|不收费|未收费|没有扣费/.test(e.message) ? '' : '本次没有扣费';
  if (e.charged === true) return '';
  return '';
}

/** 失败卡：一句原因 + 是否收费 + 一个下一步。tone: warn|err */
export function failCard(e, { tone, onNext, nextLabel, id, sub } = {}) {
  const isErr = (tone || (/FROZEN|CANCELLED|UNDER_AGE/.test(e.code) ? 'err' : 'warn')) === 'err';
  const next = e.next && (nextLabel || e.next.label);
  if (e.code === 'NETWORK') e = { ...e, code: e.code, message: '网络不稳定，请检查网络后重试', next: e.next };
  const cl = sub !== undefined ? sub : chargeLine(e);
  const n = ui.el(`<div class="m-fail sh-fail${isErr ? ' is-err' : ''}"${id ? ` id="${id}"` : ''} role="alert">
    <div class="sh-fail-title"><span class="ic" aria-hidden="true">${isErr ? '!' : 'i'}</span><h4>${esc(failTitle(e))}</h4></div>
    <p>${esc(e.message)}</p>${cl ? `<span class="sh-fail-sub">${esc(cl)}</span>` : ''}
    ${next ? `<button type="button" class="m-btn m-btn-primary" data-next>${esc(next)}</button>` : ''}</div>`);
  const b = ui.$('[data-next]', n);
  if (b) b.addEventListener('click', () => onNext && onNext(e, b));
  return n;
}

/** 附近可用车列表 */
export function nearbyList(list, onPick) {
  const n = ui.el(`<div class="m-list sh-near" id="nearList" role="list">${list.map((v, i) => `<button type="button" data-i="${i}" role="listitem">
    <span class="dot" style="background:${esc(v.color || '#F97316')}"></span>
    <span class="sh-grow"><b>${esc(v.displayName)} · ${esc(v.plateNo)}</b><span class="s">${esc(v.stationName || '路边')} · ${batteryBar(v.battery)}</span></span>
    <span class="d">${v.distanceM} 米</span><span class="go">去这辆</span></button>`).join('')}</div>`);
  ui.$$('button', n).forEach(b => b.addEventListener('click', () => onPick(list[+b.dataset.i])));
  return n;
}

/* ---------------- 地图 ---------------- */
const hash = n => { let h = 2166136261 ^ n; h = Math.imul(h, 16777619); h ^= h >>> 13; h = Math.imul(h, 2246822519); return ((h ^ (h >>> 16)) >>> 0) / 4294967295; };
const ORIGIN = [104.06, 30.60];

/**
 * 简化地图（SVG）：道路底纹、站点围栏、桩位、车辆、轨迹。
 * 选项：vehicles[{loc,color,main,label,id}] route[[lng,lat]…] linkTo{from,to,label}
 *       geo{stations,piles} spanM focus(station id) onPile(pile,station) onStation(station) onVehicle(v)
 */
export function mapView(host, opts = {}) {
  const st = { vehicles: [], route: [], linkTo: null, geo: { stations: [], piles: [] }, spanM: 360, center: HOME_LOC, nearId: null, ...opts };
  host.classList.add('sh-map');
  let W = 0, H = 0, raf = 0;
  const proj = p => { const k = W / st.spanM; return [W / 2 + (p[0] - st.center[0]) * mLon(st.center[1]) * k, H / 2 - (p[1] - st.center[1]) * MLAT * k]; };
  const lonOf = (x) => st.center[0] + (x - W / 2) / (W / st.spanM) / mLon(st.center[1]);
  const latOf = (y) => st.center[1] - (y - H / 2) / (W / st.spanM) / MLAT;

  function roads() {
    const k = W / st.spanM, out = [];
    const x0 = (lonOf(0) - ORIGIN[0]) * mLon(ORIGIN[1]), x1 = (lonOf(W) - ORIGIN[0]) * mLon(ORIGIN[1]);
    const y0 = (latOf(H) - ORIGIN[1]) * MLAT, y1 = (latOf(0) - ORIGIN[1]) * MLAT;
    const step = 150;
    const px = xm => W / 2 + ((ORIGIN[0] + xm / mLon(ORIGIN[1])) - st.center[0]) * mLon(st.center[1]) * k;
    const py = ym => H / 2 - ((ORIGIN[1] + ym / MLAT) - st.center[1]) * MLAT * k;
    for (let i = Math.floor(x0 / step) - 1; i <= Math.ceil(x1 / step) + 1; i++) {
      const xm = i * step + (hash(i * 7 + 3) - .5) * 60, w = (i % 3 === 0 ? 9 : 5) * Math.max(.7, Math.min(1.4, k / 1.1));
      const sk = (hash(i * 11 + 1) - .5) * 40, a = px(xm - sk), b = px(xm + sk);
      out.push(`<line class="road-b" x1="${a}" y1="-10" x2="${b}" y2="${H + 10}" stroke-width="${w + 2}"/><line class="road" x1="${a}" y1="-10" x2="${b}" y2="${H + 10}" stroke-width="${w}"/>`);
    }
    for (let j = Math.floor(y0 / step) - 1; j <= Math.ceil(y1 / step) + 1; j++) {
      const ym = j * step + (hash(j * 13 + 5) - .5) * 60, w = (j % 3 === 1 ? 9 : 5) * Math.max(.7, Math.min(1.4, k / 1.1));
      const sk = (hash(j * 17 + 2) - .5) * 40, a = py(ym - sk), b = py(ym + sk);
      out.push(`<line class="road-b" x1="-10" y1="${a}" x2="${W + 10}" y2="${b}" stroke-width="${w + 2}"/><line class="road" x1="-10" y1="${a}" x2="${W + 10}" y2="${b}" stroke-width="${w}"/>`);
    }
    return out.join('');
  }

  function draw() {
    raf = 0;
    if (!W || !H) return;
    const k = W / st.spanM;
    const reach = st.spanM * 1.2;
    const parts = [roads()];
    const near = st.geo.stations.filter(s => dist(s.center, st.center) < reach);
    for (const s of near) {
      const pts = s.fence.map(p => proj(p).map(n => n.toFixed(1)).join(',')).join(' ');
      parts.push(`<polygon class="st-fence${st.nearId === s.stationId ? ' is-near' : ''}${s.type === '无桩' ? ' is-none' : ''}" points="${pts}" data-st="${s.stationId}"/>`);
      const c = proj(centroid(s.fence));
      parts.push(`<g data-st="${s.stationId}" style="cursor:pointer"><circle cx="${c[0]}" cy="${c[1] - 16}" r="9" fill="${s.type === '无桩' ? '#8A7C70' : '#F97316'}" stroke="#fff" stroke-width="2"/><text x="${c[0]}" y="${c[1] - 12}" text-anchor="middle" font-size="11" font-weight="700" fill="#fff" font-family="sans-serif">P</text><circle class="hit" cx="${c[0]}" cy="${c[1] - 8}" r="20"/></g>`);
      // 车停在站点里时，站名挪到 P 标上方，避免和车辆图钉、车牌重叠
      const busy = st.vehicles.some(v => { const q = proj(v.loc); return Math.abs(q[0] - c[0]) < 70 && q[1] - c[1] > -6 && q[1] - c[1] < 46; });
      parts.push(`<text class="st-label" x="${c[0]}" y="${busy ? c[1] - 32 : c[1] + 24}" text-anchor="middle">${esc(s.name)}</text>`);
    }
    if (k > 0.9) {
      for (const p of st.geo.piles) {
        if (dist(p.location, st.center) > reach) continue;
        const [x, y] = proj(p.location);
        const cls = p.status !== '在线' ? 'is-fault' : p.occupied ? 'is-busy' : 'is-free';
        parts.push(`<circle class="pile ${cls}" cx="${x}" cy="${y}" r="${Math.min(7, Math.max(3, 1.4 * k))}"/>`);
        if (st.onPile) parts.push(`<circle class="hit" cx="${x}" cy="${y}" r="14" data-pile="${p.pileId}"><title>${p.no}号桩</title></circle>`);
      }
    }
    if (st.route.length > 1) parts.push(`<polyline class="route" points="${st.route.map(p => proj(p).join(',')).join(' ')}"/>`);
    if (st.linkTo) {
      const a = proj(st.linkTo.from), b = proj(st.linkTo.to);
      parts.push(`<line class="link" x1="${a[0]}" y1="${a[1]}" x2="${b[0]}" y2="${b[1]}"/>`);
    }
    const sorted = [...st.vehicles].sort((a, b) => (a.main ? 1 : 0) - (b.main ? 1 : 0));
    for (const v of sorted) {
      const [x, y] = proj(v.loc);
      if (x < -20 || y < -20 || x > W + 20 || y > H + 20) continue;
      if (v.me) {
        parts.push(`<g style="pointer-events:none"><circle cx="${x}" cy="${y}" r="17" fill="#2563EB" opacity=".16"/><circle cx="${x}" cy="${y}" r="7" fill="#2563EB" stroke="#fff" stroke-width="3"/></g>`);
      } else if (v.main) {
        parts.push(`<g style="pointer-events:none"><circle class="pin-ring" cx="${x}" cy="${y}" r="14" fill="${v.color || '#F97316'}" opacity=".4"/><circle cx="${x}" cy="${y}" r="14" fill="${v.color || '#F97316'}" stroke="#fff" stroke-width="2.5" opacity="${k > 3 ? .55 : 1}"/>
          <g transform="translate(${x - 9},${y - 9}) scale(.56)" color="#fff">${BIKE_SVG.replace('<svg viewBox="0 0 32 32"', '<svg x="0" y="0" width="32" height="32" viewBox="0 0 32 32"')}</g></g>`);
        if (v.label) parts.push(`<text class="lbl" x="${x}" y="${y + 28}" text-anchor="middle">${esc(v.label)}</text>`);
      } else {
        parts.push(`<g class="pin-other" data-veh="${esc(v.id || '')}"><circle cx="${x}" cy="${y}" r="9" fill="${v.color || '#8A7C70'}" stroke="#fff" stroke-width="2"/><circle cx="${x}" cy="${y}" r="3" fill="#fff"/><circle class="hit" cx="${x}" cy="${y}" r="16"/></g>`);
      }
    }
    host.querySelector('svg').innerHTML = parts.join('');
  }
  const schedule = () => { if (!raf) raf = requestAnimationFrame(draw); };

  host.innerHTML = '<svg role="img" aria-label="地图"></svg>';
  const ro = new ResizeObserver(() => { const r = host.getBoundingClientRect(); W = r.width; H = r.height; schedule(); });
  ro.observe(host);
  host.addEventListener('click', e => {
    const pl = e.target.closest('[data-pile]');
    if (pl && st.onPile) { const p = st.geo.piles.find(x => x.pileId === pl.dataset.pile); st.onPile(p, st.geo.stations.find(s => s.stationId === p.stationId)); return; }
    const vh = e.target.closest('[data-veh]');
    if (vh && st.onVehicle) { const v = st.vehicles.find(x => String(x.id) === vh.dataset.veh); v && st.onVehicle(v); return; }
    const sl = e.target.closest('[data-st]');
    if (sl && st.onStation) st.onStation(st.geo.stations.find(s => s.stationId === sl.dataset.st));
  });
  return {
    set(p) { Object.assign(st, p); schedule(); },
    /** 让一组点都进入视野 */
    fit(points, minSpan = 280) {
      const lons = points.map(p => p[0]), lats = points.map(p => p[1]);
      const c = [(Math.min(...lons) + Math.max(...lons)) / 2, (Math.min(...lats) + Math.max(...lats)) / 2];
      const dx = (Math.max(...lons) - Math.min(...lons)) * mLon(c[1]), dy = (Math.max(...lats) - Math.min(...lats)) * MLAT;
      const ratio = (H || 240) / (W || 320);
      st.center = c; st.spanM = Math.max(minSpan, dx * 1.5 + 90, (dy * 1.5 + 90) / ratio);
      schedule();
    },
    destroy() { ro.disconnect(); cancelAnimationFrame(raf); host.innerHTML = ''; },
  };
}

/* ---------------- 支付半屏（随小程序所在平台显示微信或支付宝风格） ---------------- */
/** paySheet({amount, desc, run}) -> Promise<run 的结果 | null（取消）> */
export function paySheet({ amount, desc, subject = '共享骑行服务', run, okLabel }) {
  return new Promise(resolve => {
    const wx = skin() === 'wechat';
    let busy = false, finished = false;
    const body = ui.el(`<div class="sh-native" style="display:contents">
      <div class="sh-pay-amount"><div class="t">${esc(subject)}</div><div class="v num">${ui.money(amount)}</div></div>
      <div class="sh-pay-rows"><div><span>收款方</span><span>共享骑行服务平台</span></div>${desc ? `<div><span>说明</span><span>${esc(desc)}</span></div>` : ''}<div><span>付款方式</span><span>${wx ? '零钱' : '账户余额'}</span></div></div>
      <div class="m-notice is-err" hidden role="alert"></div>
      <button type="button" class="sh-btn" data-pay>${esc(okLabel || (wx ? '立即支付' : '确认付款'))}</button>
      <button type="button" class="m-btn m-btn-ghost" data-cancel>取消</button></div>`);
    const sh = ui.bottomSheet({ title: wx ? '微信支付' : '支付宝收银台', body, closable: false, onClose: () => { if (!finished) resolve(null); } });
    sh.el.classList.add('sh-native');
    const btn = ui.$('[data-pay]', body), err = ui.$('.m-notice', body);
    ui.$('[data-cancel]', body).addEventListener('click', () => { if (!busy) sh.close('cancel'); });
    btn.addEventListener('click', async () => {
      if (busy) return;
      busy = true; err.hidden = true;
      const label = btn.textContent;
      btn.setAttribute('aria-busy', 'true'); btn.innerHTML = '<span class="m-spin"></span>支付中';
      try {
        const res = await run();
        btn.removeAttribute('aria-busy'); btn.classList.add('is-ok'); btn.textContent = '✓ 支付成功'; ui.vibrate();
        finished = true;
        await sleep(reduced() ? 0 : 650);
        sh.close('ok'); resolve(res);
      } catch (e) {
        busy = false;
        btn.removeAttribute('aria-busy'); btn.textContent = e.code === 'PAY_DECLINED' || e.code === 'NETWORK' ? '重新支付' : label;
        err.hidden = false; err.textContent = `${e.message}${e.charged === false ? '，没有扣款' : ''}`;
        err.classList.remove('sh-sheet-err'); void err.offsetWidth; err.classList.add('sh-sheet-err');
        ui.shake(sh.el.querySelector('.sh-pay-rows'));
      }
    });
  });
}

/* ---------------- 欠费补付（扫码遇到欠费直接弹出，也可作为整页内容） ---------------- */
export async function loadArrears() {
  const [list, me] = await Promise.all([api.user.orders({ status: '待付款', pageSize: 50 }), api.user.me()]);
  return { rows: list.rows.filter(o => o.payStatus === '欠费'), total: me ? me.arrears : 0, me };
}

export function arrearsContent({ rows, total }, now) {
  const dayDiff = o => Math.max(0, Math.floor((now - toD(o.endAt || o.startAt)) / 864e5));
  return ui.el(`<div style="display:contents">
    <div class="sh-debt-total"><div class="t">待补付合计</div><div class="v num">${ui.money(total)}</div></div>
    <div class="m-card is-flat" style="padding:4px 14px">${rows.map(o => `<div class="sh-debt-item"><span>${esc(mdhm(o.endAt || o.startAt))} · ${esc(o.plateNo || '')}<small>骑行 ${o.billedMinutes} 分钟${o.inStation === false ? '，含调度费' : ''} · 已欠 ${dayDiff(o)} 天</small></span><b class="num">${ui.money(o.fee.unpaid)}</b></div>`).join('') || '<div class="sh-debt-item"><span>暂无明细</span></div>'}</div>
    <p class="m-hint" style="text-align:center">补付后立即可以开锁。欠费时间过长会冻结账户。</p></div>`);
}

/** 一键补付：弹出支付半屏，成功后回调 */
export async function payArrearsFlow(total) {
  return paySheet({ amount: total, subject: '补付骑行欠费', desc: '骑行费用补付', run: () => api.user.payArrears(), okLabel: undefined });
}

export async function arrearsSheet({ onPaid } = {}) {
  const data = await loadArrears();
  if (!data.rows.length) { ui.toast('没有需要补付的欠费', { type: 'ok' }); onPaid && onPaid(); return; }
  const now = await nowDate();
  const sh = ui.bottomSheet({ title: '有一笔欠费待补付', body: arrearsContent(data, now), footer: [{ label: `一键补付 ${ui.money(data.total)}`, kind: 'primary', keepOpen: true, async onClick() {
    const res = await payArrearsFlow(data.total);
    if (res) { sh.close('paid'); ui.toast(`已补付 ${ui.money(res.paid)}，可以开锁了`, { type: 'ok' }); onPaid && onPaid(res); }
  } }] });
}

/* ---------------- 账户冻结：解冻方法 ---------------- */
export async function unfreezeSheet(e, { onDone } = {}) {
  let info = null;
  try { info = await api.user.account.get(); } catch { /* 取不到时只显示解冻方法 */ }
  const how = (info && info.freeze && info.freeze.unfreezeHow) || e.unfreezeHow || '联系客服处理';
  const body = ui.el(`<div style="display:contents"><div class="m-notice is-err"><span class="m-ic">!</span><span>${esc(e.message)}</span></div>
    <div class="m-card is-flat"><div class="m-hint">解冻方法</div><p style="margin-top:4px">${esc(how)}</p></div></div>`);
  const acts = (info && info.selfService) || [{ label: '联系客服解冻', action: 'contactCS' }];
  const sh = ui.bottomSheet({ title: '怎么解冻', body, footer: acts.map((a, i) => ({ label: a.label, kind: i === 0 ? 'primary' : 'secondary', keepOpen: true, async onClick() {
    if (a.action === 'payArrears') { sh.close(); await arrearsSheet({ onPaid: onDone }); }
    else if (a.action === 'verifyRealName') { sh.close(); app().go('#/guide'); }
    else { sh.close(); app().go('#/chat?entry=' + encodeURIComponent('账户')); }
  } })) });
}

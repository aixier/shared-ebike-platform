// 骑行中与还车（R-07 R-28 R-29；BR-08～BR-13；S-01 S-06）。
// 地图里的站点、桩用真实坐标；轻点站点或空桩，车会骑过去：站点内还车、推入空桩自动结束、站外还车先提示调度费。
import { ui, api, esc, app, mapView, getGeo, HOME_LOC, insidePoint, inPoly, dist, mLon, MLAT, batteryBar, failCard, plain } from '../kit.js';

let S = null;
const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches;
const pad = n => String(n).padStart(2, '0');
const clockText = s => { const h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60), r = Math.floor(s % 60); return h ? `${h}:${pad(m)}:${pad(r)}` : `${pad(m)}:${pad(r)}`; };

function cleanup() {
  if (!S) return;
  S.alive = false; clearInterval(S.t1); clearInterval(S.t2); cancelAnimationFrame(S.raf);
  S.offs.forEach(f => f()); S.map && S.map.destroy(); S = null;
}

export default {
  route: '#/ride',
  title: '骑行中',
  root: true,
  async mount(el) {
    cleanup();
    S = { el, alive: true, offs: [], r: null, pv: null, geo: null, map: null, busy: false, moving: false, t1: 0, t2: 0, raf: 0, base: null, pos: null, path: [] };
    el.innerHTML = `<div class="m-body">${ui.skeleton('card', 1)}<div class="sh-map skel" style="min-height:200px"></div></div><div class="m-bottom"></div>`;
    await start();
  },
  unmount: cleanup,
};

async function start() {
  const my = S;
  let r;
  try { r = await api.user.ride(); }
  catch (e) {
    if (S !== my) return;
    my.el.innerHTML = '';
    if (e.code === 'NOT_LOGGED_IN') { my.el.append(ui.empty({ icon: '🛵', title: '请先授权登录', text: '登录后可以查看正在骑行的订单。', action: '一键授权', onAction: async () => { if (await app().authorize()) app().refresh(); } })); return; }
    my.el.append(ui.retry({ text: '骑行信息没有加载出来，车辆没有受影响', onRetry: () => app().refresh() })); return;
  }
  if (S !== my) return;
  if (!r) {
    my.el.innerHTML = '<div class="m-body"></div>';
    ui.$('.m-body', my.el).append(ui.empty({ icon: '🛵', title: '当前没有骑行中的订单', text: '扫车把上的码就能开始骑行。', action: '扫码用车', onAction: () => app().replace('#/scan') }));
    return;
  }
  my.r = r; my.geo = await getGeo();
  if (S !== my) return;
  my.base = { sec: r.elapsedSec, at: performance.now() };
  // 车辆位置只用后端给的；没有任何上报时，用起点站点中心，再不行用手机位置，只是为了让地图有个中心，不画成车辆真实位置
  const stC = r.startStationId && my.geo.stations.find(s => s.stationId === r.startStationId);
  my.pos = (r.currentLocation || r.startLocation || (stC && stC.center) || HOME_LOC).slice();
  my.path = [(r.startLocation || my.pos).slice(), my.pos.slice()];
  my.me = app().getMe();
  try { const sc = await api.user.scan(r.govCode); my.r.color = sc.vehicle.color; } catch { /* 取不到品牌色时用默认橙色 */ }
  if (S !== my) return;
  build();
  await refreshPreview();
  my.t1 = setInterval(tick, 1000);
  my.t2 = setInterval(poll, 3000);
  my.offs.push(api.on('order.updated', ev => { if (S && ev.orderId === S.r.orderId && ev.status !== '骑行中') poll(); }));
}

/* ---------------- 页面 ---------------- */
function build() {
  const r = S.r, el = S.el;
  el.innerHTML = `<div class="m-body">
    <div class="sh-ride-top"><div class="l"><b>${esc(r.displayName)}</b> · ${esc(r.plateNo)}</div><div style="display:flex;gap:6px;align-items:center">${batteryBar(r.battery)}<span class="chip ok">骑行中</span></div></div>
    <div class="sh-timer m-timer"><div class="t num" id="clock">${clockText(r.elapsedSec)}</div><div class="l"><span id="mins"></span><span>费用</span><b id="fee"></b><span id="mcChip"></span></div></div>
    <div id="notices" style="display:grid;gap:8px"></div>
    <div id="mapHost"></div></div>
    <div class="m-bottom"><button type="button" class="m-btn m-btn-secondary" id="retBtn"><span class="lab">还车</span></button>
      <div class="sh-bottom-links"><button type="button" class="m-btn m-btn-ghost" id="csBtn">遇到问题</button><button type="button" class="m-btn m-btn-ghost" id="lockBtn">我已手动锁车</button></div></div>`;
  const host = ui.$('#mapHost', el);
  host.style.cssText = 'flex:1;display:flex;min-height:200px';
  const mh = ui.el('<div style="flex:1"></div>'); host.append(mh);
  S.map = mapView(mh, { geo: S.geo, vehicles: [], route: S.path, onPile, onStation });
  mh.append(ui.el('<div class="sh-map-tip">轻点站点或空桩，骑过去还车</div>'), ui.el('<div class="sh-map-chip" id="mapChip" hidden></div>'));
  S.mh = mh;
  mapUpdate(true);
  tick(true);
  ui.$('#retBtn', el).addEventListener('click', onReturnTap);
  ui.$('#lockBtn', el).addEventListener('click', onLockTap);
  ui.$('#csBtn', el).addEventListener('click', helpSheet);
}

function elapsed() { return S.base.sec + (performance.now() - S.base.at) / 1000; }
function feeOf(sec) {
  const p = S.r.price, m = Math.max(1, Math.ceil(sec / 60));
  const gross = Math.round((p.startFee + Math.max(0, m - p.startMinutes) * p.perMinute) * 10) / 10;
  const card = S.r.liveMonthCardDeduct > 0 && S.me && S.me.monthCard ? S.me.monthCard.perOrderCap : 0;
  const mc = card ? Math.min(card, gross) : 0;
  return { m, gross, mc, pay: Math.round((gross - mc) * 10) / 10 };
}
function tick(first) {
  if (!S) return;
  const sec = elapsed(), f = feeOf(sec);
  const c = ui.$('#clock', S.el); if (c) c.textContent = clockText(sec);
  const m = ui.$('#mins', S.el); if (m) m.textContent = `已计费 ${f.m} 分钟 ·`;
  const fe = ui.$('#fee', S.el); if (fe) fe.textContent = ui.money(f.pay);
  const mc = ui.$('#mcChip', S.el); if (mc) mc.innerHTML = f.mc ? `<span class="chip brand">月卡已抵 ${ui.money(f.mc)}</span>` : '';
  if (first) notices();
}

async function poll() {
  const my = S; if (!my || my.busy) return;
  try {
    const r = await api.user.ride();
    if (S !== my) return;
    if (!r) { // 订单已在别处结束（如客服远程结束）
      cleanup();
      try { const l = await api.user.orders({ pageSize: 1 }); if (l.rows[0]) app().replace('#/bill?orderId=' + l.rows[0].orderId); else app().replace('#/scan'); } catch { app().replace('#/scan'); }
      return;
    }
    my.r = { ...my.r, ...r }; my.base = { sec: r.elapsedSec, at: performance.now() };
    notices();
  } catch { /* 轮询失败时保持当前显示，下一轮再试 */ }
}

function notices() {
  const box = ui.$('#notices', S.el); if (!box) return;
  const r = S.r, pv = S.pv, out = [];
  if (r.degraded) out.push('<div class="m-notice"><span class="m-ic">ⓘ</span><span>费用将稍后收取</span></div>');
  if (r.overLimit && r.overLimit.reached) out.push('<div class="m-notice"><span class="m-ic">!</span><span>已超过本次上限，请尽快就近还车。骑行不会被中断，仍在计费。</span></div>');
  box.innerHTML = out.join('');
  chip();
}

/* ---------------- 地图与站点 ---------------- */
async function refreshPreview() {
  const my = S; if (!my) return;
  try { const pv = await api.user.returnPreview({ location: my.pos }); if (S === my) { my.pv = pv; chip(); mapUpdate(); } } catch { /* 预判失败不影响还车，点还车时服务端会再判一次 */ }
}
function mapUpdate(fit) {
  if (!S || !S.map) return;
  const nid = S.pv ? (S.pv.station ? S.pv.station.stationId : S.pv.nearestStation && S.pv.nearestStation.stationId) : null;
  S.map.set({ vehicles: [{ loc: S.pos, color: S.r.color || '#F97316', main: true, label: S.r.plateNo }], route: S.path, nearId: nid });
  if (fit) {
    const pts = [S.path[0], S.pos];
    if (S.r.nearestStation) pts.push(S.r.nearestStation.center);
    S.map.fit(pts, 300);
  }
}
function chip() {
  const c = ui.$('#mapChip', S.el), pv = S.pv; if (!c || !pv) return;
  const retBtn = ui.$('#retBtn', S.el);
  c.hidden = false;
  const fee = ui.money(pv.dispatchFee || S.r.price.dispatchFee);
  if (pv.inStation) {
    const full = pv.station && pv.station.type === '有桩' && pv.station.freePiles === 0;
    c.innerHTML = `<span class="t"><b>已在「${esc(pv.station.name)}」站点内</b><span>${full ? '桩位已满，在桩旁锁车即可，不收调度费' : pv.station.type === '有桩' ? `空桩 ${pv.station.freePiles} 个 · 推入空桩自动结束，不收调度费` : '无桩站点，点还车即可，不收调度费'}</span></span>`;
  } else if (pv.nearestStation) {
    c.innerHTML = `<span class="t"><b>最近站点「${esc(pv.nearestStation.name)}」${pv.nearestStation.distanceM} 米</b><span>站外还车收调度费 ${fee}</span></span><button type="button" class="m-btn m-btn-secondary" id="goSt">骑过去</button>`;
    ui.$('#goSt', c).addEventListener('click', () => goStation(pv.nearestStation.stationId));
  }
  if (retBtn && !S.busy) {
    retBtn.dataset.in = pv.inStation ? '1' : '0';
    // 在站点内时还车是唯一主行动；站外时降为次按钮，引导先骑到站点
    retBtn.classList.toggle('m-btn-primary', !!pv.inStation);
    retBtn.classList.toggle('m-btn-secondary', !pv.inStation);
  }
}
function goStation(id) {
  const st = S.geo.stations.find(s => s.stationId === id);
  if (st) moveTo(insidePoint(st));
}
function onStation(st) {
  if (S.moving || S.busy) return;
  if (S.pv && S.pv.station && S.pv.station.stationId === st.stationId) { ui.toast(`已在「${st.name}」站点内`); return; }
  moveTo(insidePoint(st));
}
async function onPile(pile, st) {
  if (S.moving || S.busy) return;
  if (pile.occupied) { ui.toast(`${pile.no} 号桩已有车`, { hint: '换一个空桩' }); return; }
  await moveTo(pile.location);
  if (!S) return;
  // 车端定位有几米漂移：桩靠近围栏边缘时，车端可能报在站外，此时以桩的识别为准（桩与车端冲突）
  let conflict = !inPoly(gpsOf(pile), st.fence);
  let noLock = false;
  if (ui.isDebug) {
    if (localStorage.getItem('bike.app.noLock') === '1') noLock = true;
    if (localStorage.getItem('bike.app.conflict') === '1') conflict = true;
  }
  doReturn({ method: noLock ? 'pileNoLock' : 'pile', stationId: st.stationId, pileId: pile.pileId, conflict }, { pileNo: pile.no, noLock });
}
/** 车辆入桩后车端上报的位置：桩位 + 约 6 米定位漂移（方向随桩固定） */
function gpsOf(pile) {
  let h = 2166136261;
  for (const ch of String(pile.pileId)) { h ^= ch.charCodeAt(0); h = Math.imul(h, 16777619); }
  const a = (h >>> 0) / 4294967295 * Math.PI * 2, d = 6;
  return [pile.location[0] + d * Math.cos(a) / mLon(pile.location[1]), pile.location[1] + d * Math.sin(a) / MLAT];
}

/* ---------------- 遇到问题 ---------------- */
/** 当前所在或 30 米内的有桩站点，以及其中离车最近的空桩 */
function pileSpot() {
  const pos = S.pos;
  let best = null;
  for (const st of S.geo.stations) {
    if (st.type !== '有桩') continue;
    const d = inPoly(pos, st.fence) ? 0 : Math.min(...st.fence.map(p => dist(p, pos)));
    if (!best || d < best.d) best = { st, d };
  }
  if (!best || best.d > 30) return null;
  const free = S.geo.piles.filter(p => p.stationId === best.st.stationId && p.status === '在线' && !p.occupied).sort((a, b) => dist(a.location, pos) - dist(b.location, pos));
  return free.length ? { st: best.st, pile: free[0] } : null;
}
function helpSheet() {
  if (S.busy || S.moving) return;
  const spot = pileSpot();
  const items = [
    { k: 'nolock', t: '车已推入桩位，但没有锁上', s: '平台请求运营商远程锁车，按入桩时间结束计费' },
    { k: 'conflict', t: '车已入桩，页面却显示在站点外', s: '以桩的识别为准，按站内还车，不收调度费' },
    { k: 'cs', t: '其他问题，联系客服', s: '车辆故障、费用疑问等，客服智能体会先查这单的记录' },
  ];
  const body = ui.el(`<div class="m-list">${items.map(i => `<button type="button" class="m-cell has-arrow" data-k="${i.k}"><span class="main"><b>${esc(i.t)}</b><span>${esc(i.s)}</span></span></button>`).join('')}</div>`);
  const sh = ui.bottomSheet({ title: '骑行中遇到问题', body });
  ui.$$('[data-k]', body).forEach(b => {
    const k = b.dataset.k;
    if ((k === 'nolock' || k === 'conflict') && !spot) ui.setDisabled(b, '附近没有带桩位的站点', '把车推入站点内的空桩后再选这一项');
    b.addEventListener('click', () => {
      if (b.getAttribute('aria-disabled') === 'true') return;
      sh.close();
      if (k === 'cs') { app().go('#/chat?entry=' + encodeURIComponent('骑行中页') + '&orderId=' + encodeURIComponent(S.r.orderId)); return; }
      S.pos = spot.pile.location.slice(); S.path.push(S.pos.slice()); mapUpdate();
      doReturn({ method: k === 'nolock' ? 'pileNoLock' : 'pile', stationId: spot.st.stationId, pileId: spot.pile.pileId, conflict: k === 'conflict' }, { pileNo: spot.pile.no, noLock: k === 'nolock' });
    });
  });
}

/** 车沿直线骑到目标点，到达后刷新还车预判 */
function moveTo(target) {
  const my = S;
  return new Promise(res => {
    const from = my.pos.slice(), d = dist(from, target);
    if (d < 3) { res(); return; }
    my.moving = true;
    my.map.fit([from, target], 260);
    ui.$('#retBtn', my.el).setAttribute('aria-disabled', 'true'); ui.$('#retBtn', my.el).dataset.why = '车还在行驶中'; ui.$('#retBtn', my.el).dataset.next = '到达后再点还车';
    my.path.push(from.slice());
    const dur = reduced() ? 0 : Math.max(900, Math.min(3800, d / 70 * 1000));
    const t0 = performance.now();
    const step = now => {
      if (S !== my) { res(); return; }
      const k = dur ? Math.min(1, (now - t0) / dur) : 1, e = 1 - Math.pow(1 - k, 2);
      my.pos = [from[0] + (target[0] - from[0]) * e, from[1] + (target[1] - from[1]) * e];
      my.path[my.path.length - 1] = my.pos.slice();
      my.map.set({ vehicles: [{ loc: my.pos, color: my.r.color || '#F97316', main: true, label: my.r.plateNo }], route: my.path });
      if (k < 1) my.raf = requestAnimationFrame(step);
      else {
        my.moving = false;
        const b = ui.$('#retBtn', my.el); if (b) { b.removeAttribute('aria-disabled'); delete b.dataset.why; delete b.dataset.next; }
        refreshPreview().then(() => {
          if (S === my && my.pv && my.pv.inStation) {
            const st = my.geo.stations.find(x => x.stationId === my.pv.station.stationId);
            my.map.fit(st ? st.fence.concat([my.pos]) : [my.pos], st && st.type === '有桩' ? 90 : 140);
          }
          res();
        });
      }
    };
    my.raf = requestAnimationFrame(step);
  });
}

/* ---------------- 还车 ---------------- */
/** 手动锁车：站外锁车会直接收调度费，锁之前先提醒一次（防错优于纠错） */
function onLockTap() {
  if (S.busy || S.moving) return;
  const pv = S.pv;
  if (pv && !pv.inStation && pv.nearestStation) {
    ui.mDialog({ title: '这里不在站点内', text: `锁车后订单立即结束，并收取调度费 ${ui.money(pv.dispatchFee || S.r.price.dispatchFee)}。最近站点「${pv.nearestStation.name}」约 ${pv.nearestStation.distanceM} 米，骑过去还车不收调度费。`, closable: true, actions: [
      { label: '去最近站点', kind: 'secondary', onClick: () => goStation(pv.nearestStation.stationId) },
      { label: '已锁车，结束订单', kind: 'primary', onClick: () => doReturn({ method: 'lock', location: S.pos }) },
    ] });
    return;
  }
  doReturn({ method: 'lock', location: S.pos });
}

function onReturnTap() {
  if (S.busy || S.moving) return;
  doReturn({ method: 'app', location: S.pos });
}

async function doReturn(args, { pileNo, noLock } = {}) {
  const my = S;
  if (my.busy) return;
  my.busy = true;
  const btn = ui.$('#retBtn', my.el);
  const busyText = noLock ? `已入 ${pileNo} 号桩，正在请求远程锁车…` : pileNo ? `已入 ${pileNo} 号桩，正在确认锁车…` : '正在锁车…';
  btn.setAttribute('aria-busy', 'true'); btn.classList.add('m-btn-primary'); btn.classList.remove('m-btn-secondary');
  btn.innerHTML = `<span class="m-spin"></span><span class="lab">${esc(busyText)}</span>`;
  try {
    const res = await api.user.returnBike(args);
    if (S !== my) return;
    ui.vibrate();
    const id = res.order.orderId;
    cleanup();
    app().replace('#/bill?orderId=' + encodeURIComponent(id) + '&fresh=1');
  } catch (e) {
    if (S !== my) return;
    my.busy = false;
    btn.removeAttribute('aria-busy'); btn.innerHTML = '<span class="lab">还车</span>';
    chip();
    onReturnError(e, args);
  }
}

function onReturnError(e, args) {
  if (e.code === 'DISPATCH_CONFIRM_REQUIRED') { dispatchSheet(e, args); return; }
  if (e.code === 'PILE_FULL' || e.code === 'PILE_UNAVAILABLE') {
    const body = ui.el(`<div style="display:contents"></div>`);
    body.append(failCard(plain(e, { next: null }), { tone: 'warn', sub: '' }));
    const sh = ui.bottomSheet({ title: e.code === 'PILE_FULL' ? '桩位已满' : '这个桩暂时不可用', body, footer: [{ label: e.next ? e.next.label : '站内锁车', kind: 'primary', keepOpen: true, onClick: () => { sh.close(); doReturn({ method: 'lock', stationId: e.stationId || args.stationId, location: undefined }); } }] });
    ui.shake(body.firstElementChild);
    return;
  }
  const body = ui.el('<div style="display:contents"></div>');
  body.append(failCard(plain(e, { next: null }), { tone: 'warn', sub: '订单仍在计时，重试还车即可' }));
  const sh = ui.bottomSheet({ title: '还车没有成功', body, footer: [{ label: '重试', kind: 'primary', keepOpen: true, onClick: () => { sh.close(); doReturn(args); } }] });
  ui.shake(body.firstElementChild);
}

/** 站外还车：先提示调度费与最近站点，再二次确认 */
function dispatchSheet(e, args) {
  const ns = e.nearestStation;
  const body = ui.el(`<div style="display:contents"><div class="m-notice"><span class="m-ic">!</span><span>当前不在站点内，还车将收取调度费 <b>${ui.money(e.dispatchFee)}</b>。</span></div>
    <div class="m-card is-flat"><div class="m-kv"><span>最近站点</span><b>${esc(ns.name)}</b></div><div class="m-kv"><span>距离</span><b class="num">${ns.distanceM} 米</b></div><div class="m-kv"><span>站点内还车</span><b>不收调度费</b></div></div></div>`);
  const sh = ui.bottomSheet({ title: '这里不在站点内', body, footer: [
    { label: '去最近站点还车', kind: 'primary', onClick: () => { goStation(ns.stationId); } },
    { label: `仍在此处还车，付调度费 ${ui.money(e.dispatchFee)}`, kind: 'ghost', keepOpen: true, onClick() {
      ui.mDialog({ title: '确认在站外还车？', text: `本次将收取调度费 ${ui.money(e.dispatchFee)}，计入本单账单。`, closable: true, actions: [
        { label: '再想想', kind: 'secondary' },
        { label: '确认还车', kind: 'primary', onClick: () => { sh.close(); doReturn({ ...args, method: 'app', confirmDispatch: true }); } },
      ] });
      return false;
    } },
  ] });
}

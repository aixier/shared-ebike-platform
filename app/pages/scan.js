// 扫码开锁页（R-02 R-04 R-05 R-06；S-01 S-03）。带 code 参数时为扫到某辆车后的开锁页，不带时为扫码首页。
import { ui, api, esc, app, bikeCard, priceCard, failCard, nearbyList, mapView, getGeo, HOME_LOC, arrearsSheet, unfreezeSheet, paySheet, BIKE_SVG } from '../kit.js';

let S = null;
const cleanup = () => { if (S) { S.alive = false; S.offs.forEach(f => f()); S.map && S.map.destroy(); S = null; } };

export default {
  route: '#/scan',
  title: '扫码用车',
  root: true,
  async mount(el, p) {
    cleanup();
    S = { el, code: p.code || '', auto: p.autounlock === '1', alive: true, offs: [], map: null, popped: false, last: null };
    if (!S.code) return home();
    el.innerHTML = `<div class="m-body">${ui.skeleton('card', 2)}<div class="sh-map skel" style="min-height:180px"></div></div><div class="m-bottom"><button class="m-btn m-btn-primary" aria-disabled="true" data-why="正在确认车辆信息" data-next="稍等一下，马上好">开锁</button></div>`;
    await load();
    S.offs.push(api.on('pricing.changed', () => S && load(true)), api.on('degrade.changed', () => S && load(true)));
  },
  unmount: cleanup,
};

/* ---------------- 取数与渲染 ---------------- */
async function load(silent) {
  const my = S;
  try {
    const [r, me] = await Promise.all([api.user.scan(my.code), api.user.me()]);
    if (S !== my) return;
    my.last = { r, me };
    await render(r, me);
  } catch (e) {
    if (S !== my) return;
    if (e.code === 'CODE_NOT_FOUND') {
      my.el.innerHTML = '<div class="m-body"></div><div class="m-bottom"></div>';
      ui.$('.m-body', my.el).append(failCard(e, { onNext: () => app().replace('#/scan') }));
      const tb = ui.el('<button type="button" class="m-btn m-btn-ghost">输入车辆编号</button>');
      tb.addEventListener('click', () => typeCode());
      ui.$('.m-bottom', my.el).append(tb);
      return;
    }
    if (silent) return;
    my.el.innerHTML = '';
    my.el.append(ui.retry({ text: '车辆信息没有加载出来，请检查网络后重试', onRetry: () => { my.el.innerHTML = ''; load(); } }));
  }
}

async function render(r, me, failErr) {
  const el = S.el, v = r.vehicle, check = failErr ? { ok: false, ...failErr, message: failErr.message } : r.check;
  const geo = await getGeo();
  if (!S) return;
  el.innerHTML = '<div class="m-body" id="scanBody"></div><div class="m-bottom" id="scanBot"></div>';
  const body = ui.$('#scanBody', el), bot = ui.$('#scanBot', el);
  body.append(ui.el(bikeCard(v, me)));
  body.append(ui.el(priceCard(r.price, me)));

  let near = failErr ? (failErr.nearby || []) : (r.nearby && r.nearby.length ? r.nearby : (check.nearby || []));
  let failNode = null;
  const adm = r.degrade && r.degrade.admission;

  const needAuth = !check.ok && check.code === 'NOT_LOGGED_IN';
  if (check.ok || needAuth) {
    if (r.degrade.active) {
      body.append(ui.el(`<div class="m-notice"><span class="m-ic">ⓘ</span><span>${esc(adm && adm.notice ? adm.notice : (adm ? adm.line : r.degrade.line))}</span></div>`));
    }
  } else {
    const e = check;
    const label = e.code === 'NEED_ONBOARDING' ? (me ? '去完成开通' : '授权并开始开通') : e.code === 'DEPOSIT_REQUIRED' ? '开通免押' : undefined;
    S.nextLabel = label || (e.next && e.next.label);
    const e2 = near.length || !e.next || e.next.action !== 'viewNearby' ? e : { ...e, next: { label: '重新扫码', action: 'rescan' } };
    failNode = failCard(e2, { nextLabel: label, onNext: ex => onNext(ex, failErr ? 'unlock' : 'scan'), id: 'failCard' });
    body.append(failNode);
    if (failErr) { ui.shake(failNode); ui.vibrate(); }
    if (e.careCoupon) body.append(ui.el(`<div class="m-notice is-ok"><span class="m-ic">✓</span><span>已送你一张 ${ui.money(e.careCoupon.amount)} 关怀券，下次骑行自动使用</span></div>`));
    if (near.length) {
      body.append(ui.el('<div class="m-section-title">附近可用的车</div>'));
      body.append(nearbyList(near, pick));
    }
  }
  const mapHost = ui.el('<div></div>');
  body.append(mapHost);
  // 车辆没有上报位置时不画车点（不造假位置）：地图以所在站点或默认中心为准
  const others = near.filter(n => n.location).map(n => ({ loc: n.location, color: n.color, id: n.govCode }));
  const st0 = !v.location && v.stationId ? geo.stations.find(s => s.stationId === v.stationId) : null;
  const vc = v.location || (st0 && st0.center) || HOME_LOC;
  S.map = mapView(mapHost, { geo, vehicles: [...(v.location ? [{ loc: v.location, color: v.color, main: true, label: v.plateNo }] : []), ...others], onVehicle: o => pick({ govCode: o.id }), spanM: 360, center: vc });
  if (others.length) S.map.fit([...(v.location ? [v.location] : []), ...others.map(n => n.loc)], 320);

  if (needAuth) {
    const b = ui.el('<button type="button" class="m-btn m-btn-primary">一键授权并开锁</button>');
    b.addEventListener('click', async () => { const u = await app().authorize(); if (u && S) { await app().refreshMe(); S.auto = true; load(); } });
    bot.append(b);
  } else if (check.ok) {
    const btn = ui.el(`<button type="button" class="m-btn m-btn-primary m-unlock" id="unlockBtn"><span class="fill"></span><span class="lab">${adm && adm.notice ? '知道了，开锁' : '开锁'}</span></button>`);
    bot.append(btn);
    btn.addEventListener('click', () => unlock(btn, r, me));
    if (S.auto) { S.auto = false; setTimeout(() => S && btn.isConnected && btn.click(), 400); }
  } else {
    const e = check;
    const b = ui.el('<button type="button" class="m-btn m-btn-primary">开锁</button>');
    ui.setDisabled(b, e.message, S.nextLabel ? `请点上方「${S.nextLabel}」` : '');
    bot.append(b, ui.el(`<div class="m-why">${esc(e.next && e.next.action === 'viewNearby' && near.length ? '换一辆附近的车就能骑' : e.next && e.next.action === 'retry' ? '稍后重试即可' : S.nextLabel ? `暂时不能开锁，请先点上方「${S.nextLabel}」` : '暂时不能开锁')}</div>`));
    if (!failErr && e.code === 'HAS_ARREARS' && !S.popped) { S.popped = true; setTimeout(() => S && arrearsSheet({ onPaid: () => load() }), 260); }
  }
}

function pick(n) { app().replace('#/scan?code=' + encodeURIComponent(n.govCode)); }

/* ---------------- 开锁 ---------------- */
async function unlock(btn, r, me) {
  const my = S;
  try {
    await ui.unlockButton(btn, () => api.user.unlock(my.code));
    if (S === my) app().replace('#/ride');
  } catch (e0) {
    if (S !== my) return;
    let e = e0;
    if (e.code === 'TIMEOUT') {
      let nearby = [];
      try { nearby = await api.user.nearby({ vehicleId: r.vehicle.vehicleId }); } catch { /* 取不到附近车时只给原因 */ }
      e = { code: 'TIMEOUT', message: `车辆没有响应，未扣费。${nearby.length ? `附近还有 ${nearby.length} 辆可用车` : '附近暂时没有可用车'}`, charged: false, next: { label: '查看附近可用车', action: 'viewNearby' }, nearby };
    }
    if (!e.code) { console.error(e); e = { code: 'SERVER_BUSY', message: '服务繁忙，请稍后再试', charged: false, next: { label: '重试', action: 'retry' } }; }
    e.next = e.next || { label: '重试', action: 'retry' };
    if (e.charged === undefined) e.charged = false;
    await render(r, me, e);
  }
}

/* ---------------- 失败后的下一步 ---------------- */
async function onNext(e, ctx) {
  const a = e.next && e.next.action;
  const A = app();
  switch (a) {
    case 'viewNearby': {
      const n = ui.$('#nearList');
      if (n) { n.scrollIntoView({ behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth', block: 'center' }); n.classList.remove('sh-flash'); void n.offsetWidth; n.classList.add('sh-flash'); }
      else A.go('#/scan');
      break;
    }
    case 'rescan': A.replace('#/scan'); break;
    case 'login': { const u = await A.authorize(); if (u) { await A.refreshMe(); load(); } break; }
    case 'onboarding': case 'verifyRealName': case 'openCreditFree': case 'payDeposit': A.go('#/guide?code=' + encodeURIComponent(S.code)); break;
    case 'topupDeposit': {
      const res = await paySheet({ amount: e.topupAmount || 0, subject: '补缴押金', desc: '补足押金至缴纳时金额', run: () => api.user.topupDeposit() });
      if (res) { ui.toast('押金已补缴，可以开锁了', { type: 'ok' }); load(); }
      break;
    }
    case 'payArrears': arrearsSheet({ onPaid: () => load() }); break;
    case 'payOrder': A.go('#/bill?orderId=' + encodeURIComponent(e.orderId)); break;
    case 'unfreezeGuide': unfreezeSheet(e, { onDone: () => load() }); break;
    case 'goRide': A.replace('#/ride'); break;
    case 'viewArrears': A.go('#/debt'); break;
    case 'viewDeposit': case 'refundDeposit': A.go('#/deposit'); break;
    case 'viewOrders': A.go('#/orders'); break;
    case 'contactCS': A.go('#/chat?entry=' + encodeURIComponent('失败提示页')); break;
    case 'retry': {
      if (ctx === 'unlock' && S.last) { await render(S.last.r, S.last.me); const b = ui.$('#unlockBtn'); b && b.click(); }
      else load();
      break;
    }
    default: break;
  }
}

/* ---------------- 扫码首页（未带 code） ---------------- */
async function home() {
  const my = S, el = S.el;
  el.innerHTML = `<div class="m-body"><div class="m-card sh-price"><div class="m-big">找车骑行</div><p class="m-hint">对准车把上的政府码扫一扫，点一下就开锁。</p></div><div id="homeMap"></div><div class="m-section-title" id="nearTitle">附近可用的车</div><div id="homeNear">${ui.skeleton('card', 2)}</div></div>
    <div class="m-bottom"><button type="button" class="m-btn m-btn-primary" id="scanBtn">${BIKE_SVG.replace('<svg', '<svg width="22" height="22"')}扫码用车</button><button type="button" class="m-btn m-btn-ghost" id="typeBtn">输入车辆编号</button></div>`;
  const geo = await getGeo();
  if (S !== my) return;
  const mapHost = ui.$('#homeMap', el);
  my.map = mapView(mapHost, { geo, vehicles: [{ loc: HOME_LOC, color: '#2563EB', id: 'me', me: true }], spanM: 700, center: HOME_LOC, onVehicle: o => o.id !== 'me' && pick({ govCode: o.id }) });
  mapHost.style.cssText = 'display:block;flex:none;height:200px';
  mapHost.classList.add('sh-map');
  let list = [];
  try { list = await api.user.nearby({ location: HOME_LOC, radiusM: 500 }); }
  catch (e) { if (S !== my) return; ui.$('#homeNear', el).replaceChildren(ui.retry({ text: '附近的车没有加载出来', onRetry: () => app().refresh() })); }
  if (S !== my) return;
  const host = ui.$('#homeNear', el);
  if (list.length) {
    host.replaceChildren(nearbyList(list, pick));
    const placed = list.filter(n => n.location); // 没有位置的车只在列表里出现
    my.map.set({ vehicles: [{ loc: HOME_LOC, color: '#2563EB', id: 'me', me: true }, ...placed.map(n => ({ loc: n.location, color: n.color, id: n.govCode }))] });
    my.map.fit([HOME_LOC, ...placed.map(n => n.location)], 380);
  } else if (host.children.length && !host.querySelector('.m-retry')) host.replaceChildren(ui.empty({ icon: '🛵', title: '附近暂时没有可用车', text: '换个地方，或扫车把上的码试试' }));
  ui.$('#scanBtn', el).addEventListener('click', () => viewfinder(list));
  ui.$('#typeBtn', el).addEventListener('click', () => typeCode());
}

function viewfinder(list) {
  const el = S.el;
  const v = ui.el(`<div class="sh-scan" role="dialog" aria-label="扫一扫"><div class="sh-scan-top"><button type="button" aria-label="关闭" data-x>✕</button><span></span><span style="width:48px"></span></div>
    <div class="sh-scan-mid"><div class="sh-frame"><i></i><i></i><i></i><i></i><u></u></div><div class="sh-scan-tip" id="scanTip">将车把上的二维码放入框内</div></div>
    <div class="sh-scan-bot"><button type="button" data-type>输入车辆编号</button></div></div>`);
  el.append(v);
  let closed = false;
  const close = () => { closed = true; v.remove(); };
  ui.$('[data-x]', v).addEventListener('click', close);
  ui.$('[data-type]', v).addEventListener('click', () => { close(); typeCode(); });
  setTimeout(() => {
    if (closed || !S) return;
    const code = list.length ? list[0].govCode : 'SF86020226';
    v.classList.add('sh-scan-ok'); ui.$('#scanTip', v).textContent = '已识别，正在打开'; ui.vibrate();
    setTimeout(() => { if (!closed && S) pick({ govCode: code }); }, 450);
  }, 1500);
}

function typeCode() {
  const body = ui.el(`<form style="display:contents" novalidate><label class="m-field"><span class="m-lab">车辆编号（车把二维码下方）</span><input name="code" inputmode="text" autocomplete="off" placeholder="例如 SF86020226" maxlength="24"></label></form>`);
  const sh = ui.bottomSheet({ title: '输入车辆编号', body, footer: [{ label: '确定', kind: 'primary', keepOpen: true, onClick() {
    const r = ui.validate(body, { code: { label: '车辆编号', required: true, pattern: /^(SF\d{8}|VH\d{6})$/i, message: '编号格式不对，应为 SF 加 8 位数字' } });
    if (!r.ok) { ui.shake(body); return false; }
    sh.close(); pick({ govCode: r.values.code.toUpperCase() });
  } }] });
  body.addEventListener('submit', e => { e.preventDefault(); ui.$('.m-sheet-foot button', sh.el).click(); });
}

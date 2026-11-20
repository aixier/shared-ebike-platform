/* 自绘矢量地图：不用外网瓦片。坐标 [经度, 纬度]，内部换算成以区域中心为原点的米制平面。
   模式：view 浏览 / draw 绘制新围栏 / edit 拖动顶点 / pick 点选位置 */
import { esc } from './_common.js';

const KY = 110574, KX = 111320 * Math.cos(30.6 * Math.PI / 180);

export const areaM2 = ring => {
  const r = open(ring);
  let s = 0;
  for (let i = 0; i < r.length; i++) {
    const a = r[i], b = r[(i + 1) % r.length];
    s += a[0] * KX * b[1] * KY - b[0] * KX * a[1] * KY;
  }
  return Math.abs(s) / 2;
};
export const inRing = (pt, ring) => {
  let c = false;
  for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
    const [xi, yi] = ring[i], [xj, yj] = ring[j];
    if ((yi > pt[1]) !== (yj > pt[1]) && pt[0] < (xj - xi) * (pt[1] - yi) / (yj - yi) + xi) c = !c;
  }
  return c;
};
function open(f) { return f.length > 1 && f[0][0] === f[f.length - 1][0] && f[0][1] === f[f.length - 1][1] ? f.slice(0, -1) : f.slice(); }

export function createMap(box, { region, onSelect, onDraw, onEdit, onPick } = {}) {
  const O = region.center;
  const xy = p => [(p[0] - O[0]) * KX, (p[1] - O[1]) * KY];
  const ll = (x, y) => [O[0] + x / KX, O[1] + y / KY];
  const [b0, b1] = region.bounds.map(xy);
  const S = { cx: 0, cy: 0, mpp: 10, W: 800, H: 600, stations: [], piles: [], selected: null, mode: 'view', draw: [], edit: null, pick: null, cursor: null };

  box.classList.add('g-map');
  box.innerHTML = `<svg role="img" aria-label="站点电子围栏地图"></svg>
    <div class="g-map-tools"><button type="button" data-z="in" aria-label="放大">+</button><button type="button" data-z="out" aria-label="缩小">−</button><button type="button" data-z="all" aria-label="显示全部" title="显示全部">⌂</button></div>
    <div class="g-map-hint" hidden></div>
    <div class="g-map-legend"><span><i style="background:rgba(249,115,22,.5)"></i>有桩站点</span><span><i style="background:rgba(107,91,78,.35)"></i>无桩站点</span><span><i style="background:var(--ok);border-radius:50%"></i>桩在线</span><span><i style="background:#8A7C70;border-radius:50%"></i>桩离线</span><span><i style="background:var(--err);border-radius:50%"></i>桩故障</span></div>
    <div class="g-map-scale"></div>`;
  const svg = box.querySelector('svg'), hint = box.querySelector('.g-map-hint'), scale = box.querySelector('.g-map-scale');

  const sx = x => S.W / 2 + (x - S.cx) / S.mpp, sy = y => S.H / 2 - (y - S.cy) / S.mpp;
  const scr = p => { const [x, y] = xy(p); return [sx(x), sy(y)]; };
  const unscr = (px, py) => ll(S.cx + (px - S.W / 2) * S.mpp, S.cy - (py - S.H / 2) * S.mpp);
  const pts = ring => ring.map(p => scr(p).map(v => v.toFixed(1)).join(',')).join(' ');
  const clampLL = p => { const [w, s] = region.bounds[0], [e, n] = region.bounds[1]; return [Math.min(e, Math.max(w, p[0])), Math.min(n, Math.max(s, p[1]))]; };

  function size() {
    const r = box.getBoundingClientRect();
    S.W = Math.max(300, r.width); S.H = Math.max(300, r.height);
    svg.setAttribute('viewBox', `0 0 ${S.W} ${S.H}`);
  }
  function fitXY(minx, miny, maxx, maxy, pad, minMpp) {
    S.cx = (minx + maxx) / 2; S.cy = (miny + maxy) / 2;
    S.mpp = Math.max(minMpp, Math.max((maxx - minx) * pad / S.W, (maxy - miny) * pad / S.H));
  }
  const fitAll = () => { fitXY(b0[0], b0[1], b1[0], b1[1], 1.02, 0.08); render(); };
  function focus(id) {
    const st = S.stations.find(s => s.stationId === id);
    if (!st) return;
    const ps = open(st.fence).map(xy), xs = ps.map(p => p[0]), ys = ps.map(p => p[1]);
    fitXY(Math.min(...xs), Math.min(...ys), Math.max(...xs), Math.max(...ys), 3, 0.08);
    render();
  }
  function setHint(html) { hint.hidden = !html; hint.innerHTML = html || ''; }

  function render() {
    const parts = [];
    parts.push(`<rect width="${S.W}" height="${S.H}" fill="#F6EFE5"/>`);
    const steps = [25, 50, 100, 200, 500, 1000, 2000], step = steps.find(v => v / S.mpp >= 70) || 2000;
    const x0 = Math.floor((S.cx - S.W / 2 * S.mpp) / step) * step, x1 = S.cx + S.W / 2 * S.mpp;
    const y0 = Math.floor((S.cy - S.H / 2 * S.mpp) / step) * step, y1 = S.cy + S.H / 2 * S.mpp;
    let g = '';
    for (let x = x0; x <= x1; x += step) g += `<line class="grid" x1="${sx(x).toFixed(1)}" y1="0" x2="${sx(x).toFixed(1)}" y2="${S.H}"/>`;
    for (let y = y0; y <= y1; y += step) g += `<line class="grid" x1="0" y1="${sy(y).toFixed(1)}" x2="${S.W}" y2="${sy(y).toFixed(1)}"/>`;
    parts.push(g);
    parts.push(`<rect x="${sx(b0[0])}" y="${sy(b1[1])}" width="${(b1[0] - b0[0]) / S.mpp}" height="${(b1[1] - b0[1]) / S.mpp}" fill="none" stroke="rgba(107,91,78,.35)" stroke-dasharray="6 4"/>`);
    if (S.mpp > 4) {
      const byD = {};
      S.stations.forEach(s => { (byD[s.district] = byD[s.district] || []).push(xy(s.center)); });
      Object.entries(byD).forEach(([d, a]) => {
        const mx = a.reduce((m, p) => m + p[0], 0) / a.length, my = a.reduce((m, p) => m + p[1], 0) / a.length;
        parts.push(`<text class="dlbl" x="${sx(mx).toFixed(1)}" y="${sy(my).toFixed(1)}" text-anchor="middle">${esc(d)}</text>`);
      });
    }
    const editing = S.mode === 'edit' && S.edit;
    S.stations.forEach(s => {
      const sel = s.stationId === S.selected;
      if (editing && sel) return;
      const dim = (editing || S.mode === 'pick' || S.mode === 'draw') && !sel ? ' is-dim' : '';
      const cls = `st${s.type === '无桩' ? ' is-free' : ''}${sel ? ' is-sel' : ''}${s.fullNow ? ' is-full' : ''}${dim}`;
      parts.push(`<polygon class="${cls}" data-sid="${s.stationId}" points="${pts(open(s.fence))}"><title>${esc(s.name)}</title></polygon>`);
      const [cx, cy] = scr(s.center);
      if (S.mpp > 2.2) parts.push(`<circle class="mk${s.type === '无桩' ? ' is-free' : ''}${sel ? ' is-sel' : ''}${dim}" data-sid="${s.stationId}" cx="${cx.toFixed(1)}" cy="${cy.toFixed(1)}" r="${sel ? 7 : 5}"><title>${esc(s.name)}</title></circle>`);
      else if ((S.mpp < 1.4 || sel) && !dim) parts.push(`<text class="lbl" x="${cx.toFixed(1)}" y="${(cy - 18).toFixed(1)}" text-anchor="middle">${esc(s.name)}</text>`);
    });
    if (S.mpp < 1.4) {
      S.piles.forEach(p => {
        const [px, py] = scr(p.location);
        if (px < -10 || py < -10 || px > S.W + 10 || py > S.H + 10) return;
        const k = p.status === '在线' ? 'ok' : p.status === '故障' ? 'err' : 'mute';
        parts.push(`<circle class="pl pl-${k}" cx="${px.toFixed(1)}" cy="${py.toFixed(1)}" r="${S.mpp < 0.4 ? 6 : 4.5}"><title>${p.no} 号桩 · ${esc(p.status)}</title></circle>`);
      });
    }
    if (editing) {
      const r = S.edit.ring;
      parts.push(`<polygon class="ed" points="${pts(r)}"/>`);
      r.forEach((p, i) => {
        const q = r[(i + 1) % r.length], [mx, my] = scr([(p[0] + q[0]) / 2, (p[1] + q[1]) / 2]);
        parts.push(`<circle class="mh" data-m="${i}" cx="${mx.toFixed(1)}" cy="${my.toFixed(1)}" r="5"><title>拖动以增加顶点</title></circle>`);
      });
      r.forEach((p, i) => {
        const [vx, vy] = scr(p);
        parts.push(`<circle class="vh" data-v="${i}" cx="${vx.toFixed(1)}" cy="${vy.toFixed(1)}" r="7"><title>拖动调整，双击删除</title></circle>`);
      });
    }
    if (S.mode === 'draw' && S.draw.length) {
      const d = S.draw, line = d.map(p => scr(p).map(v => v.toFixed(1)).join(',')).join(' ');
      parts.push(`<polygon class="ed" points="${line}"/><polyline class="edge" points="${line}"/>`);
      if (S.cursor) { const [lx, ly] = scr(d[d.length - 1]); parts.push(`<line class="rb" x1="${lx}" y1="${ly}" x2="${S.cursor[0]}" y2="${S.cursor[1]}"/>`); }
      d.forEach((p, i) => { const [vx, vy] = scr(p); parts.push(`<circle class="vh${i === 0 ? ' is-first' : ''}" cx="${vx.toFixed(1)}" cy="${vy.toFixed(1)}" r="${i === 0 ? 8 : 6}"/>`); });
    }
    if (S.mode === 'pick' && S.pick) { const [px, py] = scr(S.pick); parts.push(`<circle class="pick" cx="${px.toFixed(1)}" cy="${py.toFixed(1)}" r="8"/>`); }
    svg.innerHTML = parts.join('');
    const m = [10, 20, 50, 100, 200, 500, 1000, 2000].find(v => v / S.mpp >= 60) || 2000;
    scale.textContent = `${m >= 1000 ? m / 1000 + ' 公里' : m + ' 米'}`;
  }

  /* ---------- 交互 ---------- */
  let drag = null, raf = 0;
  const redraw = () => { if (!raf) raf = requestAnimationFrame(() => { raf = 0; render(); }); };
  const local = e => { const r = svg.getBoundingClientRect(); return [e.clientX - r.left, e.clientY - r.top]; };

  svg.addEventListener('pointerdown', e => {
    if (e.button !== 0) return;
    const t = e.target, [px, py] = local(e);
    svg.setPointerCapture(e.pointerId);
    if (t.dataset && t.dataset.v != null && S.edit) drag = { kind: 'v', i: +t.dataset.v, moved: false };
    else if (t.dataset && t.dataset.m != null && S.edit) {
      const i = +t.dataset.m, r = S.edit.ring, p = r[i], q = r[(i + 1) % r.length];
      r.splice(i + 1, 0, [(p[0] + q[0]) / 2, (p[1] + q[1]) / 2]);
      drag = { kind: 'v', i: i + 1, moved: true };
      onEdit && onEdit(r.map(x => x.slice()));
      render();
    } else drag = { kind: 'pan', px, py, cx: S.cx, cy: S.cy, moved: false, sid: t.dataset && t.dataset.sid };
  });
  svg.addEventListener('pointermove', e => {
    const [px, py] = local(e);
    if (S.mode === 'draw') { S.cursor = [px, py]; if (!drag) redraw(); }
    if (!drag) return;
    if (drag.kind === 'v') {
      S.edit.ring[drag.i] = clampLL(unscr(px, py));
      drag.moved = true;
      onEdit && onEdit(S.edit.ring.map(x => x.slice()));
      redraw();
      return;
    }
    if (Math.hypot(px - drag.px, py - drag.py) > 4) drag.moved = true;
    if (drag.moved) {
      box.classList.add('is-pan');
      S.cx = drag.cx - (px - drag.px) * S.mpp; S.cy = drag.cy + (py - drag.py) * S.mpp;
      redraw();
    }
  });
  const end = e => {
    if (!drag) return;
    const d = drag; drag = null; box.classList.remove('is-pan');
    if (d.kind !== 'pan' || d.moved) return;
    const [px, py] = local(e), p = unscr(px, py);
    if (S.mode === 'draw') {
      const last = S.draw[S.draw.length - 1];
      if (last) { const [lx, ly] = scr(last); if (Math.hypot(lx - px, ly - py) < 5) return; }
      let close = false;
      if (S.draw.length >= 3) { const [fx, fy] = scr(S.draw[0]); close = Math.hypot(fx - px, fy - py) < 10; }
      if (!close) S.draw.push(clampLL(p));
      onDraw && onDraw(S.draw.map(x => x.slice()), close);
      render();
    } else if (S.mode === 'pick') {
      S.pick = clampLL(p);
      onPick && onPick(S.pick);
      render();
    } else if (S.mode === 'view') onSelect && onSelect(d.sid || null);
  };
  svg.addEventListener('pointerup', end);
  svg.addEventListener('pointercancel', () => { drag = null; box.classList.remove('is-pan'); });
  svg.addEventListener('dblclick', e => {
    const t = e.target;
    if (S.mode === 'edit' && S.edit && t.dataset && t.dataset.v != null) {
      if (S.edit.ring.length <= 3) return;
      S.edit.ring.splice(+t.dataset.v, 1);
      onEdit && onEdit(S.edit.ring.map(x => x.slice()));
      render();
    } else if (S.mode === 'draw' && S.draw.length >= 3) onDraw && onDraw(S.draw.map(x => x.slice()), true);
  });
  svg.addEventListener('wheel', e => {
    e.preventDefault();
    const [px, py] = local(e), f = e.deltaY < 0 ? 1 / 1.25 : 1.25;
    const bx = S.cx + (px - S.W / 2) * S.mpp, by = S.cy - (py - S.H / 2) * S.mpp;
    S.mpp = Math.min(30, Math.max(0.08, S.mpp * f));
    S.cx = bx - (px - S.W / 2) * S.mpp; S.cy = by + (py - S.H / 2) * S.mpp;
    render();
  }, { passive: false });
  box.querySelector('.g-map-tools').addEventListener('click', e => {
    const z = e.target.closest('[data-z]');
    if (!z) return;
    if (z.dataset.z === 'all') { fitAll(); return; }
    S.mpp = Math.min(30, Math.max(0.08, S.mpp * (z.dataset.z === 'in' ? 0.7 : 1.4)));
    render();
  });
  const ro = new ResizeObserver(() => { size(); render(); });
  ro.observe(box);
  size(); fitAll();

  const setMode = m => { S.mode = m; box.classList.toggle('is-draw', m === 'draw'); box.classList.toggle('is-pick', m === 'pick'); };
  return {
    setData({ stations, piles }) { if (stations) S.stations = stations; if (piles) S.piles = piles; render(); },
    select(id) { S.selected = id; render(); },
    focus, fitAll, setHint, render,
    startDraw() { setMode('draw'); S.draw = []; S.cursor = null; render(); },
    drawPoints: () => S.draw.map(x => x.slice()),
    undoDraw() { S.draw.pop(); onDraw && onDraw(S.draw.map(x => x.slice()), false); render(); },
    stopDraw() { setMode('view'); S.draw = []; S.cursor = null; render(); },
    startEdit(id) {
      const s = S.stations.find(x => x.stationId === id);
      S.selected = id; S.edit = { id, ring: open(s.fence).map(p => p.slice()) };
      setMode('edit'); focus(id);
    },
    editRing: () => (S.edit ? S.edit.ring.map(p => p.slice()) : null),
    stopEdit() { S.edit = null; setMode('view'); render(); },
    startPick(id, point) { S.selected = id; S.pick = point || null; setMode('pick'); focus(id); },
    pickPoint: () => S.pick,
    stopPick() { S.pick = null; setMode('view'); render(); },
    destroy() { ro.disconnect(); },
  };
}

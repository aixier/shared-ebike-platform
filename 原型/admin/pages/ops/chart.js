// 折线图：纯 SVG 自绘，不依赖外部库。支持门槛线、悬停读数、缺失值断线、随容器宽度重绘。
import * as ui from '../../../shared/ui.js';

const NS = 'http://www.w3.org/2000/svg';
const p2 = n => String(n).padStart(2, '0');
export const fmtHour = ms => { const d = new Date(ms); return `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:00`; };
export const fmtDay = ms => { const d = new Date(ms); return `${p2(d.getMonth() + 1)}-${p2(d.getDate())}`; };

/**
 * lineChart(box, {series:[{key,name,color,points:[{x:毫秒,y:0~1|null}]}], thresholds:[{v,label}], xFmt, yFmt, height, label})
 * 返回 {update(opts), destroy()}
 */
export function lineChart(box, opts0) {
  let opts = opts0, W = 0, ro = null, hoverIdx = -1;
  box.classList.add('ops-chart');
  const draw = () => {
    if (!box.isConnected) { destroy(); return; }
    const { series, thresholds = [], xFmt = fmtHour, yFmt = v => ui.percent(v, (Math.round(v * 1000) % 10 === 0 ? 0 : 1)), height = 220, label = '折线图' } = opts;
    W = Math.max(320, Math.round(box.clientWidth || 720));
    const H = height, L = 46, R = 44, T = 12, B = 26, iw = W - L - R, ih = H - T - B;
    const all = series.flatMap(s => s.points).filter(p => p.y != null);
    box.innerHTML = '';
    if (!all.length) { box.append(ui.empty({ icon: '📈', title: '这个时间段没有指标', text: '运行智能体上报后会显示在这里' })); return; }
    const xs = series.flatMap(s => s.points.map(p => p.x));
    const x0 = Math.min(...xs), x1 = Math.max(...xs);
    let lo = Math.min(...all.map(p => p.y), ...thresholds.map(t => t.v)), hi = Math.max(...all.map(p => p.y), ...thresholds.map(t => t.v));
    const span0 = Math.max(hi - lo, 0.04) + 0.02;
    const step = [0.005, 0.01, 0.02, 0.025, 0.05, 0.1, 0.2].find(k => span0 / k <= 5) || 0.2;
    lo = Math.max(0, Math.floor((lo - 0.005) / step + 1e-9) * step); hi = Math.min(1, Math.ceil((hi + 0.002) / step - 1e-9) * step);
    if (hi - lo < step) lo = Math.max(0, hi - step);
    const nt = Math.max(1, Math.round((hi - lo) / step));
    const X = x => L + (x1 === x0 ? iw / 2 : (x - x0) / (x1 - x0) * iw);
    const Y = y => T + (1 - (y - lo) / (hi - lo)) * ih;
    const svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('viewBox', `0 0 ${W} ${H}`); svg.setAttribute('role', 'img'); svg.setAttribute('aria-label', label);
    const add = (tag, attrs, text) => { const n = document.createElementNS(NS, tag); for (const k in attrs) n.setAttribute(k, attrs[k]); if (text != null) n.textContent = text; svg.append(n); return n; };
    // 网格与 Y 轴
    for (let i = 0; i <= nt; i++) {
      const v = lo + (hi - lo) * i / nt, y = Y(v);
      add('line', { x1: L, x2: W - R, y1: y, y2: y, class: 'grid' });
      add('text', { x: L - 6, y: y + 4, 'text-anchor': 'end', class: 'axis' }, yFmt(v));
    }
    // X 轴
    const n = Math.min(6, Math.max(2, Math.floor(iw / 110)));
    for (let i = 0; i < n; i++) {
      const t = x0 + (x1 - x0) * i / (n - 1);
      add('text', { x: X(t), y: H - 6, 'text-anchor': i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle', class: 'axis' }, xFmt(t));
    }
    // 门槛线
    thresholds.forEach(t => {
      add('line', { x1: L, x2: W - R, y1: Y(t.v), y2: Y(t.v), class: 'th' });
      add('text', { x: W - R + 4, y: Y(t.v) + 4, class: 'thl' }, t.label);
    });
    // 折线
    series.forEach(s => {
      let d = '', pen = false;
      s.points.forEach(p => { if (p.y == null) { pen = false; return; } d += `${pen ? 'L' : 'M'}${X(p.x).toFixed(1)} ${Y(p.y).toFixed(1)}`; pen = true; });
      if (d) add('path', { d, fill: 'none', stroke: s.color, 'stroke-width': 2, 'stroke-linejoin': 'round', 'stroke-linecap': 'round' });
      if (s.points.filter(p => p.y != null).length === 1) { const p = s.points.find(q => q.y != null); add('circle', { cx: X(p.x), cy: Y(p.y), r: 3, fill: s.color }); }
    });
    const guide = add('line', { y1: T, y2: T + ih, class: 'guide', visibility: 'hidden' });
    const dots = series.map(s => add('circle', { r: 4, fill: s.color, stroke: '#fff', 'stroke-width': 2, visibility: 'hidden' }));
    const cap = add('rect', { x: L, y: T, width: iw, height: ih, fill: 'transparent', style: 'cursor:crosshair' });
    box.append(svg);
    const tip = ui.el('<div class="ops-tip" hidden></div>');
    box.append(tip);
    // 读数摘要，供读屏
    const sr = ui.el(`<div class="ops-sr">${series.map(s => { const last = [...s.points].reverse().find(p => p.y != null); return `${ui.esc(s.name)} 最新 ${last ? yFmt(last.y) : '无数据'}`; }).join('；')}</div>`);
    box.append(sr);
    // 悬停
    const xsU = [...new Set(xs)].sort((a, b) => a - b);
    const show = clientX => {
      const r = svg.getBoundingClientRect(), px = (clientX - r.left) * (W / r.width);
      const target = x0 + (px - L) / iw * (x1 - x0);
      let best = xsU[0]; xsU.forEach(v => { if (Math.abs(v - target) < Math.abs(best - target)) best = v; });
      hoverIdx = best;
      guide.setAttribute('x1', X(best)); guide.setAttribute('x2', X(best)); guide.setAttribute('visibility', 'visible');
      const rows = [];
      series.forEach((s, i) => {
        const pt = s.points.find(p => p.x === best);
        if (pt && pt.y != null) { dots[i].setAttribute('cx', X(best)); dots[i].setAttribute('cy', Y(pt.y)); dots[i].setAttribute('visibility', 'visible'); rows.push(`<div class="r"><span class="sw" style="background:${s.color}"></span><span class="n">${ui.esc(s.name)}</span><b>${yFmt(pt.y)}</b></div>`); }
        else dots[i].setAttribute('visibility', 'hidden');
      });
      tip.innerHTML = `<div class="t">${xFmt(best)}</div>${rows.join('')}`;
      tip.hidden = false;
      const left = X(best) / W * r.width, flip = left > r.width * 0.6;
      tip.style.left = flip ? 'auto' : `${left + 12}px`; tip.style.right = flip ? `${r.width - left + 12}px` : 'auto';
      tip.style.top = `${T + ih / 2}px`;
    };
    const hide = () => { guide.setAttribute('visibility', 'hidden'); dots.forEach(d => d.setAttribute('visibility', 'hidden')); tip.hidden = true; };
    cap.addEventListener('pointermove', e => show(e.clientX));
    cap.addEventListener('pointerleave', hide);
  };
  const destroy = () => { ro && ro.disconnect(); ro = null; };
  draw();
  if (typeof ResizeObserver !== 'undefined') {
    let last = box.clientWidth;
    ro = new ResizeObserver(() => { if (Math.abs(box.clientWidth - last) > 8) { last = box.clientWidth; draw(); } });
    ro.observe(box);
  }
  return { update(o) { opts = o; draw(); }, destroy };
}

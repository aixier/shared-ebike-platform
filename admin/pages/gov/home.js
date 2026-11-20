/* 首页概览：只放最必要的数字卡片与接入状态 */
import { A, ui, esc, chip, lifecycle, loadInto, setHead, accessChip, ACCESS_KIND, bizNow } from './_common.js';

const ORDER = ['未接入', '联调中', '已接入', '暂停可租', '已退出'];
const go = h => { location.hash = h; };

function mount(el, query, pctx) {
  const life = lifecycle(el);
  el.innerHTML = '';
  const host = ui.el('<div class="g-stack"></div>');
  el.append(host);

  async function init() {
    host.innerHTML = '';
    setHead(pctx, '');
    const body = ui.el('<div class="g-stack"></div>');
    host.append(body);
    await loadInto(body, async () => {
      const now = await bizNow();
      const d = new Date(now.replace(' ', 'T')); d.setDate(d.getDate() - 30);
      const since = ui.date(d);
      const [versions, st, ov, ev, exps] = await Promise.all([A.pricing.list(), A.stations.all(), A.access.overview(), A.evidence.list({ from: since, pageSize: 1 }), A.evidence.exports()]);
      return { now, since, versions, st, ov, ev, exps };
    }, ({ now, versions, st, ov, ev, exps }) => {
      const cur = versions.find(v => v.status === '生效中'), pend = versions.filter(v => v.status === '待生效').sort((a, b) => (a.effectiveAt < b.effectiveAt ? -1 : 1));
      const piles = st.stations.reduce((m, s) => ({ t: m.t + s.pileCount, o: m.o + s.onlinePiles }), { t: 0, o: 0 });
      const paused = ov.brands.filter(b => b.accessStatus === '暂停可租');
      pctx && pctx.setSub(`数据截至 ${ui.datetime(now)}`);
      paused.forEach(b => body.append(ui.el(`<div class="a-alert is-info"><span class="grow">${esc(b.name)}（${esc(b.operatorName)}）当前暂停可租，${ui.number(b.vehicleCount)} 辆车暂不可骑；恢复由平台公司运维处理。</span><a href="#/gov/access">查看接入状态</a></div>`)));
      const card = (k, v, d, href, cls = '') => `<button type="button" class="a-stat" data-href="${href}"><div class="k">${k}</div><div class="v">${v}</div><div class="d ${cls}">${d}</div></button>`;
      const stats = ui.el(`<div class="a-stats">
        ${card('现行价格规则', cur ? esc(cur.versionId) : '—', cur ? `起步 ${ui.money(cur.ride.startFee)}，之后 ${ui.money(cur.ride.perMinute)} / 分钟` : '暂无', '#/gov/pricing')}
        ${card('待生效规则', `${pend.length}<small>个</small>`, pend.length ? `${esc(pend[0].versionId)} 于 ${ui.datetime(pend[0].effectiveAt)} 生效` : '没有待生效的版本', '#/gov/pricing')}
        ${card('站点', `${st.stations.length}<small>个</small>`, `有桩 ${st.stations.filter(s => s.type === '有桩').length} · 无桩 ${st.stations.filter(s => s.type === '无桩').length}`, '#/gov/stations')}
        ${card('桩在线率', ui.percent(piles.t ? piles.o / piles.t : 0), `${ui.number(piles.o)} / ${ui.number(piles.t)} 个桩在线`, '#/gov/piles', piles.t - piles.o ? 'down' : 'up')}
        ${card('近 30 天取证', `${ui.number(ev.total)}<small>条</small>`, exps.length ? `最近导出 ${ui.datetime(exps[0].at)}` : '尚未导出', '#/gov/evidence')}</div>`);
      body.append(stats);
      body.append(ui.el(`<div><div class="a-sec-title">接入状态</div><div class="g-acc">${ORDER.map(s => { const x = ov.summary.find(y => y.status === s) || { brandCount: 0, vehicleCount: 0 }; return `<button type="button" class="a-stat k-${ACCESS_KIND[s]}" data-href="#/gov/access"><div class="k">${s}</div><div class="v">${x.brandCount}<small>个品牌</small></div><div class="d">${ui.number(x.vehicleCount)} 辆车</div></button>`; }).join('')}</div></div>`));
      body.querySelectorAll('[data-href]').forEach(b => b.addEventListener('click', () => go(b.dataset.href)));
      const t = ui.el('<div></div>');
      body.append(t);
      ui.table(t, {
        rowKey: 'brandId', pageSize: 10, rows: ov.brands, onRowClick: () => go('#/gov/access'),
        columns: [
          { key: 'name', title: '品牌', render: r => `<b>${esc(r.name)}</b>` }, { key: 'operatorName', title: '运营商' },
          { key: 'accessStatus', title: '接入状态', render: r => accessChip(r.accessStatus) },
          { key: 'vehicleCount', title: '车辆数', align: 'right', render: r => ui.number(r.vehicleCount) }, { key: 'rentable', title: '当前可租', align: 'right', render: r => ui.number(r.rentable) },
        ],
      });
    });
  }

  life.on('pricing.changed', () => init());
  life.on('access.changed', () => init());
  init();
  return () => life.dispose();
}

export default { path: '#/gov/home', title: '首页概览', menu: '首页概览', mount };

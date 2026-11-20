/* 桩管理（R-27）：按站点登记桩的位置，查看在线状态，新增与停用 */
import { A, ui, esc, chip, kv, failText, lifecycle, loadInto, setHead, PILE_KIND, auditTimeline } from './_common.js';
import { createMap, inRing } from './_map.js';

const STATUS = ['在线', '离线', '故障'];
const pos = l => `${l[0].toFixed(6)}, ${l[1].toFixed(6)}`;

function mount(el, query, pctx) {
  const life = lifecycle(el);
  el.innerHTML = '';
  const host = ui.el('<div class="g-stack"></div>');
  el.append(host);
  let stations = [], region = null, table = null;
  const flt = { stationId: (query && query.station) || '', status: '' };

  async function init() {
    host.innerHTML = '';
    const hb = setHead(pctx, '按站点登记桩的位置，查看在线状态；停用后该桩不再参与入桩识别', [{ id: 'add', label: '登记新桩', kind: 'primary' }]);
    const body = ui.el('<div class="g-stack"></div>');
    host.append(body);
    hb.add.addEventListener('click', () => {
      if (!stations.length) { ui.toast('站点列表还没有加载出来', { type: 'warn', hint: '等页面加载完成后再登记' }); return; }
      openRegister();
    });
    await loadInto(body, async () => { const all = await A.stations.all(); return all; }, all => {
      stations = all.stations.sort((a, b) => (a.stationId < b.stationId ? -1 : 1)); region = all.region;
      const sum = stations.reduce((m, s) => ({ total: m.total + s.pileCount, online: m.online + s.onlinePiles, bad: m.bad + s.faultPiles }), { total: 0, online: 0, bad: 0 });
      body.append(ui.el(`<div class="a-stats">
        <div class="a-stat"><div class="k">已登记的桩</div><div class="v">${ui.number(sum.total)}<small>个</small></div><div class="d">分布在 ${stations.filter(s => s.pileCount).length} 个站点</div></div>
        <button type="button" class="a-stat" data-st="在线"><div class="k">在线</div><div class="v">${ui.number(sum.online)}<small>个</small></div><div class="d up">在线率 ${ui.percent(sum.total ? sum.online / sum.total : 0)}</div></button>
        <button type="button" class="a-stat" data-st="bad"><div class="k">离线或故障</div><div class="v">${ui.number(sum.bad)}<small>个</small></div><div class="d ${sum.bad ? 'down' : ''}">点击查看，离线与故障分别筛选</div></button>
        <div class="a-stat"><div class="k">无桩站点</div><div class="v">${stations.filter(s => s.type === '无桩').length}<small>个</small></div><div class="d">靠围栏判定在点，登记桩后自动变为有桩站点</div></div></div>`));
      body.append(ui.el(`<div class="a-filter"><div class="f"><label for="fs">站点</label><select id="fs" class="a-select" style="min-width:260px"><option value="">全部站点</option>${stations.map(s => `<option value="${s.stationId}" ${s.stationId === flt.stationId ? 'selected' : ''}>${s.stationId} · ${esc(s.name)}（${s.pileCount} 个桩）</option>`).join('')}</select></div>
        <div class="f"><label for="fst">桩状态</label><select id="fst" class="a-select"><option value="">全部</option>${STATUS.map(s => `<option ${s === flt.status ? 'selected' : ''}>${s}</option>`).join('')}</select></div><span class="grow"></span><button type="button" class="a-btn a-btn-text" id="clr">清空筛选</button></div>`));
      const tbl = ui.el('<div id="tbl"></div>');
      body.append(tbl);
      const apply = () => { flt.stationId = body.querySelector('#fs').value; flt.status = body.querySelector('#fst').value; table.goto(1); };
      body.querySelector('#fs').addEventListener('change', apply);
      body.querySelector('#fst').addEventListener('change', apply);
      body.querySelector('#clr').addEventListener('click', () => { body.querySelector('#fs').value = ''; body.querySelector('#fst').value = ''; apply(); });
      body.querySelectorAll('[data-st]').forEach(b => b.addEventListener('click', () => { body.querySelector('#fst').value = b.dataset.st === 'bad' ? '故障' : b.dataset.st; apply(); }));
      table = ui.table(tbl, {
        rowKey: 'pileId', pageSize: 20, sort: { key: 'pileId', dir: 'asc' },
        load: async ({ page, pageSize, sort }) => A.piles.list({ stationId: flt.stationId || undefined, status: flt.status || undefined, page, pageSize, sort }),
        empty: { title: '没有符合条件的桩', text: flt.stationId ? '这个站点还没有登记桩，点右上角「登记新桩」' : '换个站点或状态试试', action: '登记新桩', onAction: openRegister },
        onRowClick: openDetail, onAction: (act, r) => rowAction(act, r),
        columns: [
          { key: 'pileId', title: '桩编号', sortable: true, render: r => `<span class="mono">${r.pileId}</span>` },
          { key: 'stationName', title: '所在站点', render: r => `${esc(r.stationName || '')}<div class="g-note mono">${r.stationId}</div>` },
          { key: 'no', title: '桩位号', align: 'right', sortable: true },
          { key: 'location', title: '位置（经度, 纬度）', render: r => `<span class="mono num">${pos(r.location)}</span>` },
          { key: 'status', title: '在线状态', sortable: true, render: r => chip(r.status, PILE_KIND[r.status]) },
          { key: 'occupiedPlateNo', title: '停放车辆', render: r => esc(r.occupiedPlateNo || '空闲') },
          { key: 'lastHeartbeatAt', title: '最近心跳', sortable: true, render: r => `<span class="num">${ui.datetime(r.lastHeartbeatAt, true)}</span>` },
          { key: 'act', title: '', render: r => `<span class="a-row-act">${r.status === '在线' ? '<button type="button" class="a-btn a-btn-text is-sm" data-act="off">停用</button><button type="button" class="a-btn a-btn-text is-sm" data-act="fault">报故障</button>' : `<button type="button" class="a-btn a-btn-text is-sm" data-act="on">${r.status === '故障' ? '恢复在线' : '启用'}</button>`}</span>` },
        ],
      });
    });
  }

  /* ---------- 状态变更 ---------- */
  async function rowAction(act, r) {
    const cfg = {
      off: { to: '离线', title: `停用 ${r.pileId}`, msg: `停用后该桩状态为离线，不再识别入桩；${r.occupiedPlateNo ? `当前停放的 ${esc(r.occupiedPlateNo)} 将无法入桩自动结束，需用户手动锁车。` : '用户在站点内锁车仍按围栏判定在点。'}`, reasons: ['桩体维修停用', '站点撤桩', '位置调整，暂停使用'], danger: true, ok: '确认停用' },
      fault: { to: '故障', title: `标记 ${r.pileId} 故障`, msg: '标记故障后用户端不再推荐该桩位，用户在站点内锁车仍按围栏判定在点。', reasons: ['巡检发现桩体无响应', '心跳中断超过 1 小时'], danger: true, ok: '确认标记' },
      on: { to: '在线', title: `${r.status === '故障' ? '恢复' : '启用'} ${r.pileId}`, msg: '恢复后该桩重新参与入桩识别，下一次心跳正常即可使用。', reasons: ['维修完成，恢复使用', '重新启用'], danger: false, ok: r.status === '故障' ? '确认恢复' : '确认启用' },
    }[act];
    const res = await ui.confirm({
      title: cfg.title, danger: cfg.danger, confirmText: cfg.ok, message: cfg.msg, impact: `${r.stationName} ${r.no} 号桩：${r.status} → ${cfg.to}`, reasons: cfg.reasons,
      onConfirm: async reason => { try { await A.piles.setStatus(r.pileId, cfg.to, reason); } catch (e) { throw new Error(failText(e)); } },
    });
    if (res) { ui.toast(`${r.pileId} 已${cfg.to === '在线' ? '启用' : cfg.to === '离线' ? '停用' : '标记故障'}`, { type: 'ok' }); table.reload(); }
  }

  /* ---------- 详情 ---------- */
  async function openDetail(r) {
    const dr = ui.drawer({ title: `${r.no} 号桩 · ${r.pileId}`, sub: r.stationName, body: ui.skeleton('lines', 6) });
    const trail = await auditTimeline(r.pileId);
    dr.setBody(`${kv([['在线状态', chip(r.status, PILE_KIND[r.status])], ['所在站点', `${esc(r.stationName)} <span class="mono g-note">${r.stationId}</span>`], ['位置', `<span class="mono">${pos(r.location)}</span>`], ['停放车辆', esc(r.occupiedPlateNo || '空闲')], ['最近心跳', ui.datetime(r.lastHeartbeatAt, true)], ['安装时间', ui.datetime(r.installedAt)], ['故障说明', esc(r.faultNote || '—')]])}
      <div><div class="a-sec-title">调整记录</div>${trail}</div>`);
    const foot = ui.el('<footer class="a-drawer-foot"></footer>');
    const mk = (label, kind, act) => { const b = ui.el(`<button type="button" class="a-btn a-btn-${kind}">${label}</button>`); b.addEventListener('click', () => { dr.close(); setTimeout(() => rowAction(act, r), 240); }); foot.append(b); };
    if (r.status === '在线') { mk('标记故障', 'default', 'fault'); mk('停用此桩', 'danger', 'off'); } else mk(r.status === '故障' ? '恢复在线' : '启用此桩', 'primary', 'on');
    dr.el.append(foot);
  }

  /* ---------- 登记新桩 ---------- */
  function openRegister() {
    let map = null, point = null;
    const dr = ui.drawer({
      title: '登记新桩', sub: '选择站点，在围栏内点选桩的位置', wide: true, onClose: () => map && map.destroy(),
      body: `<form class="a-form" novalidate id="rf">
        <div class="a-item"><label class="req" for="rf-st">所在站点</label><select id="rf-st" name="stationId" class="a-select"><option value="">请选择站点</option>${stations.map(s => `<option value="${s.stationId}" ${s.stationId === flt.stationId ? 'selected' : ''}>${s.stationId} · ${esc(s.name)}（${s.type}，${s.pileCount} 个桩）</option>`).join('')}</select></div>
        <div class="a-item"><label class="req">桩的位置</label><div id="rmap" class="g-mini"></div><span class="tip" id="rtip">先选择站点，再在地图上点击桩的位置；位置须在围栏内</span>
          <div class="a-input-group"><input class="a-input" name="loc" id="rf-loc" readonly placeholder="尚未选择位置" aria-label="已选位置"></div></div>
        <div class="a-item"><label class="req" for="rf-reason">事由（会写入操作留痕）</label><textarea id="rf-reason" name="reason" class="a-textarea" placeholder="如：站点新装 2 个桩位，至少 4 个字"></textarea></div>
        <div class="a-alert is-err" id="rf-err" hidden role="alert"></div></form>`,
      footer: [{ label: '取消', kind: 'default' }, { label: '登记', kind: 'primary', keepOpen: true, onClick: (L, btn) => save(L, btn) }],
    });
    const form = dr.el.querySelector('#rf'), mapBox = dr.el.querySelector('#rmap'), loc = form.elements.loc, tip = dr.el.querySelector('#rtip');
    map = createMap(mapBox, { region, onPick: p => { point = p; loc.value = pos(p); const st = stations.find(s => s.stationId === form.elements.stationId.value); const okIn = st && inRing(p, st.fence); tip.textContent = okIn ? '位置在围栏内' : '位置不在围栏内，请重新点选'; tip.style.color = okIn ? 'var(--ok)' : 'var(--err)'; } });
    const chooseStation = () => {
      const id = form.elements.stationId.value; point = null; loc.value = '';
      if (!id) { map.setData({ stations: [], piles: [] }); map.stopPick(); return; }
      const st = stations.find(s => s.stationId === id);
      A.piles.list({ stationId: id, pageSize: 100 }).then(r => { map.setData({ stations: [st], piles: r.rows }); map.startPick(id, null); tip.textContent = '在地图上点击桩的位置（已有的桩以圆点显示）'; tip.style.color = ''; }).catch(() => { tip.textContent = '该站点的桩没有加载出来，仍可点选位置'; map.setData({ stations: [st], piles: [] }); map.startPick(id, null); });
    };
    form.elements.stationId.addEventListener('change', chooseStation);
    map.setData({ stations: [], piles: [] });
    if (form.elements.stationId.value) chooseStation();
    async function save(L, btn) {
      const r = ui.validate(form, { stationId: { label: '所在站点', required: true, requiredMessage: '请选择所在站点' }, loc: { label: '桩的位置', required: true, requiredMessage: '请在地图上点选桩的位置' }, reason: { label: '事由', required: true, min: 4 } });
      if (!r.ok) return;
      const st = stations.find(s => s.stationId === r.values.stationId), err = dr.el.querySelector('#rf-err');
      err.hidden = true;
      if (!point || !inRing(point, st.fence)) { err.hidden = false; err.textContent = '桩的位置须在站点围栏内，请在地图上重新点选'; return; }
      const old = btn.innerHTML; btn.disabled = true; btn.innerHTML = '<span class="a-spin"></span>登记中';
      try {
        const p = await A.piles.create({ stationId: st.stationId, location: point }, r.values.reason);
        L.close('ok');
        ui.toast(`已登记 ${st.name} ${p.no} 号桩`, { type: 'ok', hint: `桩编号 ${p.pileId}，默认在线` });
        flt.stationId = st.stationId; init();
      } catch (e) { btn.disabled = false; btn.innerHTML = old; err.hidden = false; err.textContent = failText(e); }
    }
  }

  life.on('station.changed', ev => { if (ev.remote && table) table.reload(); });
  init();
  return () => life.dispose();
}

export default { path: '#/gov/piles', title: '桩管理', menu: '桩管理', mount };

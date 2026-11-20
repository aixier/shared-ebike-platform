/* 站点与围栏（R-26）：地图上绘制、拖动调整、保存、删除；点击站点看详情 */
import { A, ui, esc, chip, kv, failText, lifecycle, loadInto, setHead, auditTimeline } from './_common.js';
import { createMap, areaM2 } from './_map.js';

const DISTRICTS = ['东城区', '西城区', '滨河区', '高新区'];
const fmtArea = m => `${ui.number(Math.round(m))} ㎡`;
const closeRing = r => [...r, r[0]];

function mount(el, query, pctx) {
  const life = lifecycle(el);
  el.innerHTML = '';
  const host = ui.el('<div class="g-stack"></div>');
  el.append(host);
  let data = { region: null, stations: [], piles: [] }, map = null, table = null, drawer = null;
  const flt = { q: '', district: '', type: '' };
  let mode = 'view', drawBtn = null;

  async function fetchAll() {
    const [all, piles] = await Promise.all([A.stations.all(), A.piles.list({ pageSize: 500 })]);
    return { region: all.region, stations: all.stations, piles: piles.rows };
  }
  const filtered = () => data.stations.filter(s => (!flt.q || s.name.includes(flt.q) || s.stationId.includes(flt.q.toUpperCase())) && (!flt.district || s.district === flt.district) && (!flt.type || s.type === flt.type));

  function frame() {
    host.innerHTML = '';
    drawBtn = setHead(pctx, '在地图上绘制、调整站点电子围栏；用户在围栏内还车即判为在点', [{ id: 'draw', label: '绘制新站点', kind: 'primary' }]).draw;
    host.append(ui.el(`<div class="a-filter"><div class="f"><label for="q">站点名称或编号</label><input id="q" class="a-input a-search" placeholder="如：ST0012、望江路" value="${esc(flt.q)}"></div>
      <div class="f"><label for="d">所在区</label><select id="d" class="a-select"><option value="">全部</option>${DISTRICTS.map(d => `<option ${d === flt.district ? 'selected' : ''}>${d}</option>`).join('')}</select></div>
      <div class="f"><label for="t">站点类型</label><select id="t" class="a-select"><option value="">全部</option><option ${flt.type === '有桩' ? 'selected' : ''}>有桩</option><option ${flt.type === '无桩' ? 'selected' : ''}>无桩</option></select></div>
      <span class="grow"></span><button type="button" class="a-btn a-btn-text" id="clr">清空筛选</button></div>`));
    const wrap = ui.el('<div class="g-mapwrap"><div id="map"></div><div id="list"></div></div>');
    host.append(wrap);
    return wrap;
  }

  async function init() {
    const wrap = frame();
    const mapBox = wrap.querySelector('#map'), listBox = wrap.querySelector('#list');
    mapBox.classList.add('g-map');
    drawBtn.addEventListener('click', startDraw);
    const refilter = () => { flt.q = host.querySelector('#q').value.trim(); flt.district = host.querySelector('#d').value; flt.type = host.querySelector('#t').value; table && table.setRows(filtered()); map && map.setData({ stations: data.stations }); };
    host.querySelector('#q').addEventListener('input', refilter);
    host.querySelector('#d').addEventListener('change', refilter);
    host.querySelector('#t').addEventListener('change', refilter);
    host.querySelector('#clr').addEventListener('click', () => { flt.q = flt.district = flt.type = ''; host.querySelector('#q').value = ''; host.querySelector('#d').value = ''; host.querySelector('#t').value = ''; refilter(); });
    mapBox.innerHTML = `<div class="a-skel-block">${ui.skeleton('lines', 6)}</div>`;
    listBox.innerHTML = '';
    try {
      data = await fetchAll();
    } catch (e) {
      if (!el.isConnected) return;
      wrap.innerHTML = '';
      const p = ui.el('<div class="a-panel" style="grid-column:1/-1"></div>');
      p.append(ui.retry({ text: '站点数据没有加载出来，没有产生任何变更，请检查网络后重试', onRetry: init }));
      wrap.append(p);
      return;
    }
    if (!el.isConnected) return;
    map = createMap(mapBox, {
      region: data.region,
      onSelect: id => { if (id) selectStation(id, false); else { map.select(null); table && table.clearSelection(); } },
      onDraw: (pts, close) => drawChanged(pts, close),
      onEdit: ring => editChanged(ring),
    });
    map.setData({ stations: data.stations, piles: data.piles });
    table = ui.table(listBox, {
      rowKey: 'stationId', pageSize: 10, rows: filtered(), onRowClick: r => selectStation(r.stationId, true),
      empty: { title: '没有符合条件的站点', text: '换个关键字，或清空筛选查看全部' },
      columns: [
        { key: 'name', title: '站点', render: r => `<div>${esc(r.name)}</div><div class="g-note mono">${r.stationId}</div>`, sortable: true },
        { key: 'district', title: '区', width: '64px' },
        { key: 'type', title: '类型', render: r => chip(r.type, r.type === '有桩' ? 'brand' : 'mute'), width: '70px' },
        { key: 'onlinePiles', title: '在线桩', align: 'right', sortable: true, render: r => (r.type === '有桩' ? `${r.onlinePiles}/${r.pileCount}` : '—'), width: '70px' },
      ],
    });
  }

  async function reload() {
    try { data = await fetchAll(); } catch { ui.toast('站点数据刷新失败', { type: 'err', hint: '刷新页面后重试' }); return; }
    map.setData({ stations: data.stations, piles: data.piles });
    table.setRows(filtered());
  }

  /* ---------- 选中与详情 ---------- */
  function selectStation(id, fromList) {
    if (mode !== 'view') return;
    map.select(id);
    map.focus(id);
    openDetail(id);
  }
  async function openDetail(id) {
    drawer && drawer.close();
    const st = data.stations.find(s => s.stationId === id);
    drawer = ui.drawer({ title: st ? st.name : id, sub: `${id} · ${st ? st.district : ''}`, body: ui.skeleton('lines', 8), onClose: () => { if (mode === 'view') map && map.select(null); } });
    const dr = drawer;
    try {
      const [s, trail] = await Promise.all([A.stations.get(id), auditTimeline(id)]);
      if (dr !== drawer) return;
      const canDelete = typeof A.stations.remove === 'function';
      dr.setBody(`${s.fullNow ? '<div class="a-alert is-err">当前所有在线桩位都已占用，用户只能桩旁锁车还车。</div>' : ''}
        ${kv([['站点类型', chip(s.type, s.type === '有桩' ? 'brand' : 'mute')], ['所在区', esc(s.district)], ['容量', `${s.capacity} 个车位（含桩旁）`], ['围栏面积', fmtArea(areaM2(s.fence))], ['围栏顶点', `${s.fence.length - 1} 个`],
          ['桩', s.pileCount ? `共 ${s.pileCount}，在线 ${s.onlinePiles}，空闲 ${s.freePiles}，离线或故障 ${s.faultPiles}` : '尚未登记桩'], ['停放车辆', `${s.parkedVehicles} 辆`],
          ['最近调整', `${ui.datetime(s.updatedAt)} · ${esc(s.updatedByName || '')}`], ['创建时间', ui.datetime(s.createdAt)]])}
        ${s.pileCount ? `<div><a href="#/gov/piles?station=${s.stationId}">查看该站点的 ${s.pileCount} 个桩 ›</a></div>` : `<div><a href="#/gov/piles?station=${s.stationId}">去登记桩 ›</a></div>`}
        <div><div class="a-sec-title">调整记录</div>${trail}</div>`);
      const foot = dr.el.querySelector('.a-drawer-foot') || (() => { const f = ui.el('<footer class="a-drawer-foot"></footer>'); dr.el.append(f); return f; })();
      foot.innerHTML = '';
      const btn = (label, kind, fn, extra = '') => { const b = ui.el(`<button type="button" class="a-btn a-btn-${kind}" ${extra}>${label}</button>`); b.addEventListener('click', fn); foot.append(b); return b; };
      if (canDelete) {
        const del = btn('删除站点', 'danger', () => removeStation(s, dr));
        if (s.pileCount > 0) ui.setDisabled(del, `站点下还登记着 ${s.pileCount} 个桩，不能删除`, '只能删除没有登记桩的站点；有桩站点不再使用时，可调整围栏或在桩管理里把桩设为离线');
        else if (s.parkedVehicles > 0) ui.setDisabled(del, `站点内还停着 ${s.parkedVehicles} 辆车，不能删除`, '车辆驶离后再删除，或改为调整围栏');
      }
      btn('修改名称与容量', 'default', () => editInfo(s, dr));
      btn('调整围栏', 'primary', () => { dr.close(); setTimeout(() => startEdit(s.stationId), 240); });
    } catch (e) {
      if (dr !== drawer) return;
      dr.setBody('');
      dr.body.append(ui.retry({ text: '站点详情没有加载出来，请重试', onRetry: () => { dr.close(); openDetail(id); } }));
    }
  }

  /* ---------- 绘制新站点 ---------- */
  function hintBar(html) { map.setHint(html); }
  function startDraw() {
    if (mode !== 'view') return;
    drawer && drawer.close();
    mode = 'draw'; setBusy(true);
    map.select(null); map.startDraw();
    drawChanged([], false);
  }
  function drawChanged(pts, close) {
    const n = pts.length;
    hintBar(`<b>绘制新站点</b>：在地图上依次点击围栏的顶点（已点 ${n} 个，至少 3 个）。点回第一个点或双击即可闭合。${n >= 3 ? `<br>当前面积约 ${fmtArea(areaM2(closeRing(pts)))}` : ''}
      <div class="g-row"><button type="button" class="a-btn a-btn-default is-sm" data-a="undo" ${n ? '' : 'aria-disabled="true" data-why="还没有点任何顶点" data-next="先在地图上点第一个点"'}>撤销上一点</button><button type="button" class="a-btn a-btn-primary is-sm" data-a="done" ${n >= 3 ? '' : `aria-disabled="true" data-why="围栏至少需要 3 个顶点，目前 ${n} 个" data-next="继续在地图上点击添加顶点"`}>完成绘制</button><button type="button" class="a-btn a-btn-text is-sm" data-a="cancel">取消</button></div>`);
    if (close && n >= 3) finishDraw();
  }
  function finishDraw() {
    const pts = map.drawPoints();
    if (pts.length < 3) return;
    const fence = closeRing(pts), area = areaM2(fence);
    openCreateForm(fence, area);
  }
  let createDr = null;
  function openCreateForm(fence, area) {
    if (createDr && createDr.el.isConnected) return;
    const dr = createDr = ui.drawer({
      title: '新建站点', sub: `围栏 ${fence.length - 1} 个顶点，面积约 ${fmtArea(area)}`,
      body: `<form class="a-form" novalidate id="cf">
        ${area > 20000 ? `<div class="a-alert is-info" role="status">围栏面积约 ${fmtArea(area)}，远大于常见站点（几百平方米）。如果是在缩得很小的地图上绘制的，请返回放大后重新绘制。</div>` : ''}
        <div class="a-item"><label class="req" for="cf-name">站点名称</label><input id="cf-name" name="name" class="a-input" maxlength="30" placeholder="如：望江路地铁口站"></div>
        <div class="a-item"><label class="req" for="cf-d">所在区</label><select id="cf-d" name="district" class="a-select"><option value="">请选择</option>${DISTRICTS.map(d => `<option>${d}</option>`).join('')}</select></div>
        <div class="a-item"><label class="req">站点类型</label><div class="a-radio"><label><input type="radio" name="type" value="无桩" checked>无桩（靠围栏判定在点）</label><label><input type="radio" name="type" value="有桩">有桩（之后登记桩）</label></div></div>
        <div class="a-item"><label class="req" for="cf-cap">容量</label><div class="a-input-group"><input id="cf-cap" name="capacity" class="a-input" value="10" inputmode="numeric"><span class="unit">个车位</span></div></div>
        <div class="a-item"><label class="req" for="cf-reason">事由（会写入操作留痕）</label><textarea id="cf-reason" name="reason" class="a-textarea" placeholder="如：地铁口新增停放需求，至少 4 个字"></textarea></div>
        <div class="a-alert is-err" id="cf-err" hidden role="alert"></div></form>`,
      footer: [{ label: '返回继续绘制', kind: 'default', onClick: () => {} }, { label: '保存站点', kind: 'primary', keepOpen: true, onClick: async (L, btn) => save(L, btn) }],
      onClose: reason => { if (reason !== 'saved') { /* 回到绘制状态 */ } },
    });
    const form = dr.el.querySelector('#cf');
    async function save(L, btn) {
      const r = ui.validate(form, {
        name: { label: '站点名称', required: true, min: 2, max: 30 }, district: { label: '所在区', required: true, requiredMessage: '请选择所在区' },
        capacity: { label: '容量', required: true, type: 'number', minValue: 1, maxValue: 200, custom: v => (typeof v === 'number' && !Number.isInteger(v) ? '容量须为整数' : '') },
        reason: { label: '事由', required: true, min: 4 },
      });
      if (!r.ok) return;
      const v = r.values, err = form.querySelector('#cf-err');
      err.hidden = true;
      const old = btn.innerHTML; btn.disabled = true; btn.innerHTML = '<span class="a-spin"></span>保存中';
      try {
        const s = await A.stations.create({ name: v.name, district: v.district, type: v.type, fence, capacity: v.capacity }, v.reason);
        L.close('saved'); endDraw();
        ui.toast(`站点「${s.name}」已创建`, { type: 'ok', hint: '用户在围栏内还车即判为在点' });
        await reload(); map.select(s.stationId); map.focus(s.stationId);
      } catch (e) { btn.disabled = false; btn.innerHTML = old; err.hidden = false; err.textContent = failText(e); }
    }
  }
  function endDraw() { map.stopDraw(); map.setHint(''); mode = 'view'; setBusy(false); }

  /* ---------- 调整围栏 ---------- */
  let editId = null, editOrig = null;
  function startEdit(id) {
    mode = 'edit'; editId = id; setBusy(true);
    const s = data.stations.find(x => x.stationId === id);
    editOrig = s.fence;
    map.startEdit(id);
    editChanged(map.editRing());
  }
  function editChanged(ring) {
    const area = areaM2(closeRing(ring)), bad = area < 20;
    const s = data.stations.find(x => x.stationId === editId);
    map.setHint(`<b>调整围栏：${esc(s.name)}</b><br>拖动白色顶点移动位置；拖动边中间的小圆点增加顶点；双击顶点删除（至少 3 个）。<br>当前 ${ring.length} 个顶点，面积约 ${fmtArea(area)}${bad ? '，<span style="color:var(--err)">过小，至少 20 ㎡</span>' : ''}
      <div class="g-row"><button type="button" class="a-btn a-btn-primary is-sm" data-a="saveEdit" ${bad ? 'aria-disabled="true" data-why="围栏面积不足 20 ㎡" data-next="把顶点拉开一些再保存"' : ''}>保存围栏</button><button type="button" class="a-btn a-btn-default is-sm" data-a="resetEdit">还原</button><button type="button" class="a-btn a-btn-text is-sm" data-a="cancelEdit">取消</button></div>`);
  }
  async function saveEdit() {
    const ring = map.editRing(), s = data.stations.find(x => x.stationId === editId);
    const before = areaM2(editOrig), after = areaM2(closeRing(ring));
    const r = await ui.confirm({
      title: `保存「${s.name}」的新围栏`, confirmText: '确认保存',
      message: `围栏将由 ${editOrig.length - 1} 个顶点、约 ${esc(fmtArea(before))}，调整为 ${ring.length} 个顶点、约 ${esc(fmtArea(after))}。`,
      impact: '保存后用户在新围栏内还车判为在点；骑行中的订单按结束时刻的围栏判定',
      reasons: ['实地测量后修正围栏', '站点扩容', '道路改造后调整'],
      onConfirm: async reason => { try { await A.stations.update(editId, { fence: closeRing(ring) }, reason); } catch (e) { throw new Error(failText(e)); } },
    });
    if (r) { const id = editId; endEdit(); ui.toast('围栏已保存', { type: 'ok', hint: '用户端在点判定已按新围栏执行' }); await reload(); map.select(id); }
  }
  function endEdit() { map.stopEdit(); map.setHint(''); mode = 'view'; editId = null; setBusy(false); }

  /* ---------- 修改信息、删除 ---------- */
  function editInfo(s, dr) {
    const m = ui.modal({
      title: `修改「${s.name}」`, icon: false,
      body: `<form class="a-form" novalidate id="ef">
        <div class="a-item"><label class="req" for="ef-name">站点名称</label><input id="ef-name" name="name" class="a-input" value="${esc(s.name)}" maxlength="30"></div>
        <div class="a-item"><label class="req" for="ef-cap">容量</label><div class="a-input-group"><input id="ef-cap" name="capacity" class="a-input" value="${s.capacity}" inputmode="numeric"><span class="unit">个车位</span></div></div>
        <div class="a-item"><label class="req" for="ef-reason">事由（会写入操作留痕）</label><textarea id="ef-reason" name="reason" class="a-textarea"></textarea></div>
        <div class="a-alert is-err" id="ef-err" hidden role="alert"></div></form>`,
      actions: [{ label: '取消', kind: 'default' }, { label: '保存修改', kind: 'primary', keepOpen: true, async onClick(L, btn) {
        const form = m.el.querySelector('#ef');
        const r = ui.validate(form, { name: { label: '站点名称', required: true, min: 2, max: 30 }, capacity: { label: '容量', required: true, type: 'number', minValue: 1, maxValue: 200, custom: v => (typeof v === 'number' && !Number.isInteger(v) ? '容量须为整数' : '') }, reason: { label: '事由', required: true, min: 4 } });
        if (!r.ok) return;
        if (r.values.name === s.name && r.values.capacity === s.capacity) { const e = form.querySelector('#ef-err'); e.hidden = false; e.textContent = '名称和容量都没有变化，没有产生任何变更'; return; }
        const old = btn.innerHTML; btn.disabled = true; btn.innerHTML = '<span class="a-spin"></span>保存中';
        try { await A.stations.update(s.stationId, { name: r.values.name, capacity: r.values.capacity }, r.values.reason); L.close('ok'); dr.close(); ui.toast('站点信息已保存', { type: 'ok' }); await reload(); openDetail(s.stationId); }
        catch (e) { btn.disabled = false; btn.innerHTML = old; const er = form.querySelector('#ef-err'); er.hidden = false; er.textContent = failText(e); }
      } }],
    });
  }
  async function removeStation(s, dr) {
    const r = await ui.confirm({
      title: `删除站点「${s.name}」`, danger: true, confirmText: '确认删除',
      message: `删除后该站点停用：围栏不再参与还车在点判定，地图与用户端不再显示，此前订单的还车记录保留。`,
      impact: `站点「${s.name}」停用并从地图移除；删除人与事由写入操作留痕`,
      reasons: ['站点已撤销', '重复建设，合并到相邻站点'],
      onConfirm: async reason => { try { await A.stations.remove(s.stationId, reason); } catch (e) { throw new Error(failText(e)); } },
    });
    if (r) { dr.close(); ui.toast(`站点「${s.name}」已删除`, { type: 'ok' }); await reload(); }
  }

  function setBusy(on) {
    const b = drawBtn;
    if (!b) return;
    on ? ui.setDisabled(b, '正在绘制或调整围栏', '先完成或取消当前操作') : ui.setEnabled(b);
  }

  /* 地图提示条里的按钮 */
  host.addEventListener('click', e => {
    const a = e.target.closest('[data-a]');
    if (!a || a.getAttribute('aria-disabled') === 'true') return;
    ({
      undo: () => map.undoDraw(), done: finishDraw, cancel: endDraw,
      saveEdit, resetEdit: () => { map.startEdit(editId); editChanged(map.editRing()); }, cancelEdit: endEdit,
    })[a.dataset.a]?.();
  });
  const onKey = e => { if (e.key === 'Escape' && !document.querySelector('.a-drawer,.a-modal-wrap')) { if (mode === 'draw') endDraw(); else if (mode === 'edit') endEdit(); } };
  document.addEventListener('keydown', onKey);

  life.on('station.changed', ev => { if (ev.remote && mode === 'view' && map) reload(); });
  init();
  return () => { life.dispose(); document.removeEventListener('keydown', onKey); map && map.destroy(); drawer && drawer.close(); };
}

export default { path: '#/gov/stations', title: '站点与围栏', menu: '站点与围栏', mount };

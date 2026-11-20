/* 证据查询导出（R-45）：按车辆、运营商、时间筛选执法取证，预览照片，导出供线下处置 */
import { A, ui, esc, chip, kv, failText, lifecycle, loadInto, setHead, photoUrl, download } from './_common.js';

const RESULT_KIND = { 无政府码: 'err', 码已失效: 'warn', 码未启用: 'warn' };
const RESULTS = ['无政府码', '码已失效', '码未启用'];
const thumbs = r => `<span class="g-thumbs">${(r.photos || []).slice(0, 3).map(p => `<img src="${esc(photoUrl(p))}" alt="" loading="lazy">`).join('')}</span>`;

function mount(el, query, pctx) {
  const life = lifecycle(el);
  el.innerHTML = '';
  const host = ui.el('<div class="g-stack"></div>');
  el.append(host);
  const flt = { q: '', scanResult: '', brandId: '', operatorId: '', from: '', to: '' };
  let table = null, tab = 'list', brands = [], expBtn = null;

  const params = () => Object.fromEntries(Object.entries(flt).filter(([, v]) => v));
  const filterText = () => {
    const t = [];
    if (flt.q) t.push(`关键字「${flt.q}」`);
    if (flt.scanResult) t.push(flt.scanResult);
    const b = brands.find(x => x.brandId === flt.brandId); if (b) t.push(`品牌 ${b.name}`);
    const o = brands.find(x => x.operatorId === flt.operatorId); if (o) t.push(`运营商 ${o.operatorName}`);
    if (flt.from || flt.to) t.push(`取证日期 ${flt.from || '不限'} 至 ${flt.to || '不限'}`);
    return t.join('，') || '不限条件（全部证据）';
  };

  async function init() {
    host.innerHTML = '';
    const hb = setHead(pctx, '执法人员现场取证的记录，提交后不可修改；按车辆、运营商、时间筛选后导出，供线下处置使用', [{ id: 'exp', label: '导出当前筛选结果', kind: 'primary' }]);
    expBtn = hb.exp;
    const body = ui.el('<div class="g-stack"></div>');
    host.append(body);
    await loadInto(body, async () => (await A.access.overview()).brands, bs => {
      brands = bs;
      const ops = [...new Map(bs.map(b => [b.operatorId, b.operatorName])).entries()];
      const tabsBox = ui.el('<div id="tabs"></div>');
      const view = ui.el('<div class="g-stack" id="view"></div>');
      body.append(tabsBox, view);
      ui.tabs(tabsBox, [{ key: 'list', label: '取证记录' }, { key: 'exports', label: '导出记录' }], tab, k => { tab = k; draw(); });
      const exp = expBtn;
      exp.addEventListener('click', doExport);
      function draw() {
        view.innerHTML = '';
        tab === 'list' ? drawList() : drawExports();
        exp.hidden = tab !== 'list';
      }
      function drawList() {
        const f = ui.el(`<div class="a-filter">
          <div class="f"><label for="eq">车号、地址或证据编号</label><input id="eq" class="a-input a-search" placeholder="如：TM610769、望江路" value="${esc(flt.q)}"></div>
          <div class="f"><label for="er">核查结果</label><select id="er" class="a-select"><option value="">全部</option>${RESULTS.map(r => `<option ${r === flt.scanResult ? 'selected' : ''}>${r}</option>`).join('')}</select></div>
          <div class="f"><label for="eb">品牌</label><select id="eb" class="a-select"><option value="">全部</option>${bs.map(b => `<option value="${b.brandId}" ${b.brandId === flt.brandId ? 'selected' : ''}>${esc(b.name)}</option>`).join('')}</select></div>
          <div class="f"><label for="eo">运营商</label><select id="eo" class="a-select"><option value="">全部</option>${ops.map(([id, n]) => `<option value="${id}" ${id === flt.operatorId ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select></div>
          <div class="f"><label for="ef">取证日期自</label><input id="ef" type="date" class="a-input" value="${flt.from}"></div>
          <div class="f"><label for="et">至</label><input id="et" type="date" class="a-input" value="${flt.to}"></div>
          <span class="grow"></span><button type="button" class="a-btn a-btn-text" id="eclr">清空筛选</button></div>`);
        const tbl = ui.el('<div id="tbl"></div>');
        view.append(f, tbl);
        let timer;
        const apply = () => {
          flt.q = f.querySelector('#eq').value.trim(); flt.scanResult = f.querySelector('#er').value; flt.brandId = f.querySelector('#eb').value; flt.operatorId = f.querySelector('#eo').value; flt.from = f.querySelector('#ef').value; flt.to = f.querySelector('#et').value;
          if (flt.from && flt.to && flt.from > flt.to) { ui.toast('起始日期晚于结束日期', { type: 'warn', hint: '调整日期范围后再查询' }); return; }
          table.goto(1);
        };
        f.querySelector('#eq').addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(apply, 300); });
        f.querySelectorAll('select,input[type=date]').forEach(x => x.addEventListener('change', apply));
        f.querySelector('#eclr').addEventListener('click', () => { Object.keys(flt).forEach(k => (flt[k] = '')); drawList2(); });
        const drawList2 = () => { view.innerHTML = ''; drawList(); };
        table = ui.table(tbl, {
          rowKey: 'evidenceId', pageSize: 20, onRowClick: openDetail,
          load: ({ page, pageSize, sort }) => A.evidence.list({ ...params(), page, pageSize, sort: sort || { key: 'capturedAt', dir: 'desc' } }),
          empty: { title: '没有符合条件的取证记录', text: '放宽日期范围或清空筛选再试', action: '清空筛选', onAction: () => { Object.keys(flt).forEach(k => (flt[k] = '')); drawList2(); } },
          columns: [
            { key: 'evidenceId', title: '证据编号', render: r => `<span class="mono">${r.evidenceId}</span>` },
            { key: 'capturedAt', title: '取证时间', sortable: true, render: r => `<span class="num">${ui.datetime(r.capturedAt)}</span>` },
            { key: 'officerName', title: '执法人员', render: r => `${esc(r.officerName || '')}<div class="g-note">工号 ${esc(r.badgeNo)}</div>` },
            { key: 'scanResult', title: '核查结果', render: r => chip(r.scanResult, RESULT_KIND[r.scanResult] || 'mute') },
            { key: 'brandName', title: '品牌 / 运营商', render: r => (r.brandName ? `${esc(r.brandName)}<div class="g-note">${esc(r.operatorName || '')}</div>` : '—') },
            { key: 'vehicleDescription', title: '车辆', ellipsis: true, render: r => `<span title="${esc(r.vehicleDescription)}">${esc(r.plateNo || r.vehicleDescription)}</span>` },
            { key: 'address', title: '取证地点', ellipsis: true, render: r => `<span title="${esc(r.address)}">${esc(r.address)}</span>` },
            { key: 'photos', title: '照片', render: r => thumbs(r) },
          ],
        });
      }
      function drawExports() {
        const box = ui.el('<div id="xbl"></div>');
        view.append(box);
        loadInto(box, () => A.evidence.exports(), rows => {
          const t = ui.el('<div></div>');
          box.append(t);
          ui.table(t, {
            rowKey: 'exportId', pageSize: 10, rows,
            empty: { title: '还没有导出过', text: '在「取证记录」里筛选后点右上角导出，这里会留下记录' },
            columns: [
              { key: 'at', title: '导出时间', render: r => `<span class="num">${ui.datetime(r.at)}</span>` },
              { key: 'byName', title: '导出人' },
              { key: 'filter', title: '筛选条件', ellipsis: true, render: r => esc(Object.entries(r.filter || {}).map(([k, v]) => `${({ scanResult: '结果', brandId: '品牌', operatorId: '运营商', from: '自', to: '至', q: '关键字' })[k] || k} ${v}`).join('，') || '不限') },
              { key: 'count', title: '条数', align: 'right', render: r => ui.number(r.count) },
              { key: 'fileName', title: '文件', render: r => `<span class="mono">${esc(r.fileName.replace(/\.zip$/, '.csv'))}</span>` },
            ],
          });
        });
      }
      draw();
    });
  }

  /* ---------- 导出 ---------- */
  async function doExport() {
    const total = table ? table.getState().total : 0;
    if (!total) { ui.toast('当前筛选结果为空，没有可导出的证据', { type: 'warn', hint: '放宽筛选条件后再导出' }); return; }
    let out = null;
    const r = await ui.confirm({
      title: `导出 ${ui.number(total)} 条取证记录`, confirmText: '确认导出',
      message: `<div>筛选范围：${esc(filterText())}。</div><div>文件为 CSV，含证据编号、取证时间、执法人员工号、核查结果、品牌、运营商、车辆描述、地址、经纬度、照片数与 SHA-256 摘要，可用表格软件打开。</div>`,
      impact: `${ui.number(total)} 条证据；导出人与事由写入操作留痕`,
      reasons: ['线下处置：违规投放车辆核查', '线下约谈运营商取证材料'],
      onConfirm: async reason => { try { out = await A.evidence.export(params(), reason); } catch (e) { throw new Error(failText(e)); } },
    });
    if (r && out) {
      download(out.csvFileName, out.csv);
      ui.toast(`已导出 ${out.export.count} 条，文件 ${out.csvFileName}`, { type: 'ok', hint: '导出记录可在「导出记录」里查看' });
    }
  }

  /* ---------- 详情 ---------- */
  function openDetail(r) {
    const photos = r.photos || [];
    const dr = ui.drawer({
      title: `证据 ${r.evidenceId}`, sub: `${ui.datetime(r.capturedAt)} · ${r.officerName || ''}（工号 ${r.badgeNo}）`, wide: true,
      body: `<div><div class="a-sec-title">现场照片（${photos.length} 张，点击放大）</div>${photos.length ? `<div class="g-photos">${photos.map((p, i) => `<button type="button" data-i="${i}" aria-label="放大第 ${i + 1} 张照片"><img src="${esc(photoUrl(p))}" alt="取证照片 ${i + 1}"></button>`).join('')}</div>` : '<div class="a-muted">没有照片</div>'}</div>
        ${kv([['核查结果', chip(r.scanResult, RESULT_KIND[r.scanResult] || 'mute')], ['品牌', esc(r.brandName || '未能识别')], ['运营商', esc(r.operatorName || '—')], ['车号', esc(r.plateNo || '—')], ['车辆描述', esc(r.vehicleDescription)], ['现场备注', esc(r.note || '—')],
          ['取证地点', esc(r.address)], ['经纬度', `<span class="mono">${r.location[0].toFixed(6)}, ${r.location[1].toFixed(6)}</span>，定位精度 ${r.accuracyM} 米`], ['取证时间', ui.datetime(r.capturedAt, true)], ['提交时间', ui.datetime(r.submittedAt, true)]])}
        <div><div class="a-sec-title">防篡改</div><div class="a-alert is-info">证据提交后已锁定，不能修改或删除。摘要由证据编号、人员、时间、位置、核查结果与照片计算得出。</div>
          <div class="g-hash" style="margin-top:8px">${esc(r.sha256)}</div><div class="g-row" style="margin-top:8px"><button type="button" class="a-btn a-btn-default is-sm" id="vf">重新计算并校验摘要</button><span id="vr" class="g-note" role="status"></span></div></div>`,
    });
    dr.body.querySelectorAll('.g-photos button').forEach(b => b.addEventListener('click', () => {
      const i = +b.dataset.i;
      ui.modal({ title: `取证照片 ${i + 1} / ${photos.length}`, icon: false, body: `<img class="g-bigphoto" src="${esc(photoUrl(photos[i]))}" alt="取证照片 ${i + 1}">`, actions: [{ label: '关闭', kind: 'default' }] });
    }));
    const vf = dr.body.querySelector('#vf'), vr = dr.body.querySelector('#vr');
    vf.addEventListener('click', async () => {
      vf.disabled = true; vr.textContent = '计算中…';
      try { const v = await A.evidence.verify(r.evidenceId); vr.innerHTML = v.ok ? chip('摘要一致，证据未被改动', 'ok') : chip('摘要不一致，请核实证据来源', 'err'); }
      catch (e) { vr.textContent = failText(e).replace('这次操作没有保存，', ''); }
      vf.disabled = false;
    });
    const foot = ui.el('<footer class="a-drawer-foot"></footer>');
    const one = ui.el('<button type="button" class="a-btn a-btn-primary">导出此条</button>');
    one.addEventListener('click', async () => {
      let out = null;
      const res = await ui.confirm({ title: `导出证据 ${r.evidenceId}`, confirmText: '确认导出', message: '导出为 CSV 文件，含此条证据的全部文字信息与摘要。', impact: '1 条证据；导出人与事由写入操作留痕', reasons: ['线下处置取证材料'], onConfirm: async reason => { try { out = await A.evidence.export({ q: r.evidenceId }, reason); } catch (e) { throw new Error(failText(e)); } } });
      if (res && out) { download(out.csvFileName, out.csv); ui.toast(`已导出 ${out.csvFileName}`, { type: 'ok' }); }
    });
    foot.append(one); dr.el.append(foot);
  }

  life.on('evidence.created', () => { if (tab === 'list' && table) table.reload(); });
  init();
  return () => life.dispose();
}

export default { path: '#/gov/evidence', title: '证据查询导出', menu: '证据查询导出', mount };

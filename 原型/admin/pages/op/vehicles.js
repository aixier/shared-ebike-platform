// 车辆清单（R-01、R-41、M-04）：列表、上传清单、在线新增与编辑、移除、政府码发放状态与导出。
import { api, ui, esc, op, injectStyle, getMe, getBrands, can, dt, chip, chipRaw, setHead, btn, busy, fail, errText, kv, item, watch, copyText, saveFile, toCsv, parseCsv, readTextFile, nowStr, lacks, go } from './common.js';

const VEHICLE_STATUS = ['可用', '骑行中', '低电量', '故障', '离线', '码未启用', '码已失效'];
const PLATE_RE = /^[A-Z]{2}\d{6}$/;
const TAG_RE = /^[0-9A-F]{24}$/;
const shortTag = t => (t ? `${t.slice(0, 8)}…${t.slice(-4)}` : '—');

export default {
  path: '#/op/vehicles', title: '车辆清单', menu: '车辆清单', perm: 'vehicle.view.own',
  async mount(el, query, pctx) {
    injectStyle();
    el.innerHTML = '';
    let me, brands;
    try { [me, brands] = await Promise.all([getMe(), getBrands()]); }
    catch (e) { el.append(ui.retry({ text: `${errText(e)}，页面没有加载出来`, onRetry: () => this.mount(el, query, pctx) })); return; }
    const editable = can(me, 'vehicle.edit.own');
    const ro = me.readonly;
    const submitBrands = brands.filter(b => !['已退出', '未接入'].includes(b.accessStatus));
    const F = { q: '', brandId: '', status: '', codeStatus: '' };
    const V = op().vehicles;

    /* 页头与操作 */
    const bUpload = btn('上传清单', 'primary', () => openUpload());
    const bAdd = btn('新增车辆', 'default', () => openAdd());
    const bExport = btn('导出政府码清单', 'default', b => exportCodes(b));
    if (!editable) { lacks(bUpload, '当前账号没有提交车辆清单的权限', '请联系账号管理员调整授权'); lacks(bAdd, '当前账号没有编辑车辆的权限', '请联系账号管理员调整授权'); }
    else if (ro) { ui.setDisabled(bUpload, '运营商已退出，账号为只读', '如有疑问，请联系主管部门'); ui.setDisabled(bAdd, '运营商已退出，账号为只读', '如有疑问，请联系主管部门'); }
    else if (!submitBrands.length) { const why = '名下品牌均已退出或未接入，不能提交车辆'; ui.setDisabled(bUpload, why, '接入后即可提交清单'); ui.setDisabled(bAdd, why, '接入后即可提交清单'); }
    setHead(el, pctx, '车辆清单', '清单提交后系统自动发放政府码，一车一码；移出清单的车辆，政府码随即失效', [bExport, bAdd, bUpload]);

    /* 统计卡片 */
    const stats = ui.el('<div class="a-stats" aria-live="polite"></div>');
    el.append(stats);
    const drawStats = s => {
      const cards = [['全部车辆', s.total, '', ''], ['政府码已启用', s.enabled, '用户扫码可骑', '已启用'], ['政府码待启用', s.pending, '联调中品牌，验收上线后启用', '待启用'], ['政府码已失效', s.invalid, '已移出清单或品牌退出', '已失效']];
      stats.innerHTML = cards.map(([k, v, d, key]) => `<button type="button" class="a-stat${F.codeStatus === key ? ' is-on' : ''}" data-k="${key}" aria-pressed="${F.codeStatus === key}"><div class="k">${k}</div><div class="v">${ui.number(v)}<small>辆</small></div><div class="d">${d || '&nbsp;'}</div></button>`).join('');
      ui.$$('button', stats).forEach(b => b.addEventListener('click', () => { F.codeStatus = F.codeStatus === b.dataset.k ? '' : b.dataset.k; flt.elements.codeStatus.value = F.codeStatus; t.goto(1); }));
    };
    stats.innerHTML = [1, 2, 3, 4].map(() => '<div class="a-stat"><span class="skel" style="width:60%;height:12px"></span><span class="skel" style="width:40%;height:26px;margin-top:8px"></span></div>').join('');

    /* 筛选 */
    const flt = ui.el(`<form class="a-filter" autocomplete="off" role="search">
      <div class="f grow"><label for="vq">搜索</label><input id="vq" name="q" class="a-input a-search" style="width:100%" placeholder="车号、政府码、车辆编号、桩识别标识"></div>
      <div class="f"><label for="vb">品牌</label><select id="vb" name="brandId" class="a-select"><option value="">全部品牌</option>${brands.map(b => `<option value="${esc(b.brandId)}">${esc(b.name)}</option>`).join('')}</select></div>
      <div class="f"><label for="vs">车辆状态</label><select id="vs" name="status" class="a-select"><option value="">全部状态</option>${VEHICLE_STATUS.map(s => `<option>${s}</option>`).join('')}</select></div>
      <div class="f"><label for="vc">政府码状态</label><select id="vc" name="codeStatus" class="a-select"><option value="">全部</option><option>已启用</option><option>待启用</option><option>已失效</option></select></div>
      <div class="f"><button type="reset" class="a-btn a-btn-default">清空筛选</button></div></form>`);
    el.append(flt);
    let qt;
    flt.addEventListener('input', e => { if (e.target.name === 'q') { clearTimeout(qt); qt = setTimeout(() => { F.q = e.target.value.trim(); t.goto(1); }, 300); } });
    flt.addEventListener('change', e => { if (e.target.name && e.target.name !== 'q') { F[e.target.name] = e.target.value; t.goto(1); } });
    flt.addEventListener('reset', () => { setTimeout(() => { Object.keys(F).forEach(k => F[k] = ''); t.goto(1); }, 0); });

    /* 表格 */
    const box = ui.el('<div></div>');
    el.append(box);
    const actAttr = (r, kind) => {
      if (ro) return 'aria-disabled="true" data-why="运营商已退出，账号为只读" data-next="如有疑问，请联系主管部门"';
      if (!editable) return 'aria-disabled="true" data-why="当前账号没有编辑车辆的权限" data-next="请联系账号管理员调整授权"';
      if (r.codeStatus === '已失效') return 'aria-disabled="true" data-why="这辆车已移出清单，政府码已失效" data-next="如需继续使用，请通过上传清单重新提交"';
      if (kind === 'remove' && r.status === '骑行中') return 'aria-disabled="true" data-why="车辆正在骑行中，不能移除" data-next="还车后再移除"';
      return '';
    };
    const t = ui.table(box, {
      rowKey: 'vehicleId', pageSize: 20,
      columns: [
        { key: 'plateNo', title: '车号', sortable: true, render: r => `<span class="op-strong">${esc(r.plateNo)}</span>` },
        { key: 'govCode', title: '政府码', render: r => `<span class="op-mono">${esc(r.govCode)}</span>` },
        { key: 'brandName', title: '品牌', render: r => esc(r.brandName) },
        { key: 'model', title: '车型', render: r => esc(r.model) },
        { key: 'status', title: '车辆状态', render: r => chip(r.status, 'vehicle') },
        { key: 'battery', title: '电量', align: 'right', sortable: true, render: r => (r.codeStatus === '已失效' || r.battery == null ? '—' : `<span style="${r.battery < 20 ? 'color:var(--warn);font-weight:600' : ''}">${r.battery}%</span>`) },
        { key: 'codeStatus', title: '政府码状态', render: r => chip(r.codeStatus, 'codeStatus') },
        { key: 'pileTag', title: '桩识别标识', render: r => `<span class="op-mono" title="${esc(r.pileTag)}">${esc(shortTag(r.pileTag))}</span>` },
        { key: 'codeIssuedAt', title: '发码时间', sortable: true, render: r => dt(r.codeIssuedAt) },
        { key: 'act', title: '操作', align: 'right', width: '120px', render: r => `<span class="a-row-act"><button type="button" class="a-btn a-btn-text is-sm" data-act="edit" ${actAttr(r, 'edit')}>编辑</button><button type="button" class="a-btn a-btn-text is-sm" data-act="remove" style="color:var(--err)" ${actAttr(r, 'remove')}>移除</button></span>` },
      ],
      load: async ({ page, pageSize, sort }) => {
        const r = await V.list({ ...F, page, pageSize, sort: sort || undefined });
        drawStats(r.stats);
        return r;
      },
      onRowClick: r => openDetail(r),
      onAction: (act, r) => (act === 'edit' ? openEdit(r) : openRemove(r)),
      empty: { title: '没有符合条件的车辆', text: '换个筛选条件，或点右上角「上传清单」提交车辆' },
      summary: '共 {total} 辆',
    });
    const off = watch(el, ['vehicle.changed', 'access.changed'], () => t.reload());
    pctx && pctx.onUnmount && pctx.onUnmount(off);

    /* ---------- 详情抽屉 ---------- */
    function detailBody(r) {
      const rentWhy = r.rentable ? '可租' : r.codeStatus === '待启用' ? '政府码待启用' : r.codeStatus === '已失效' ? '政府码已失效' : r.status !== '可用' ? `车辆${r.status}` : '品牌当前不可租';
      return ui.el(`<div style="display:contents">
        <div><div class="a-sec-title">基本信息</div>${kv([['车号', `<b>${esc(r.plateNo)}</b>`], ['品牌', esc(r.brandName)], ['车型', esc(r.model)], ['车辆编号', `<span class="op-mono">${esc(r.vehicleId)}</span>`], ['清单提交', dt(r.listedAt)]])}</div>
        <div><div class="a-sec-title">政府码</div>${kv([
          ['政府码', `<span class="op-mono">${esc(r.govCode)}</span> <button type="button" class="a-btn a-btn-text is-sm" data-copy="${esc(r.govCode)}" data-what="政府码">复制</button>`],
          ['状态', chip(r.codeStatus, 'codeStatus')], ['发码时间', dt(r.codeIssuedAt)],
          ...(r.codeStatus === '待启用' ? [['计划启用', r.codeEnableAt ? dt(r.codeEnableAt) : '品牌验收上线后启用']] : []),
          ...(r.codeStatus === '已失效' ? [['失效时间', dt(r.codeInvalidAt)]] : []),
          ['桩识别标识', `<span class="op-mono">${esc(r.pileTag)}</span> <button type="button" class="a-btn a-btn-text is-sm" data-copy="${esc(r.pileTag)}" data-what="桩识别标识">复制</button>`]])}</div>
        <div><div class="a-sec-title">当前状态</div>${kv([['车辆状态', chip(r.status, 'vehicle')], ['电量', r.codeStatus === '已失效' || r.battery == null ? '—' : `${r.battery}%`], ['能否被租', `${r.rentable ? chipRaw('可租', 'ok') : chipRaw(`不可租 · ${rentWhy}`, 'mute')}`], ['所在站点', esc(r.stationName || '站点外')], ['最近上报', r.lastReportAt ? dt(r.lastReportAt) : '—']])}</div></div>`);
    }
    function openDetail(r) {
      const body = detailBody(r);
      const footer = [];
      const rm = { label: '移除车辆', kind: 'danger', keepOpen: true, onClick: () => openRemove(r, d), };
      const ed = { label: '编辑', kind: 'primary', keepOpen: true, onClick: () => openEdit(r, d) };
      footer.push(rm, ed);
      const d = ui.drawer({ title: r.plateNo, sub: `${r.brandName} · ${r.model}`, body, footer });
      const [bRm, bEd] = ui.$$('.a-drawer-foot button', d.el);
      [[bRm, 'remove'], [bEd, 'edit']].forEach(([b, k]) => { const a = actAttr(r, k); if (a) { const why = /data-why="([^"]*)"/.exec(a)[1], nx = /data-next="([^"]*)"/.exec(a)[1]; ui.setDisabled(b, why, nx); } });
      ui.$$('[data-copy]', body).forEach(b => b.addEventListener('click', () => copyText(b.dataset.copy, b.dataset.what)));
    }

    /* ---------- 编辑 ---------- */
    function openEdit(r, drawerRef) {
      const form = ui.el(`<form class="a-form" novalidate>
        ${item('车号', `<input class="a-input" value="${esc(r.plateNo)}" disabled>`, { tip: '车号提交后不能修改；如有误，请移除后重新提交' })}
        ${item('车型', `<input id="ev-model" name="model" class="a-input" value="${esc(r.model)}" maxlength="30">`, { req: true, id: 'ev-model' })}
        ${item('桩识别用车辆标识', `<input id="ev-tag" name="pileTag" class="a-input op-mono" value="${esc(r.pileTag)}" maxlength="24" spellcheck="false">`, { req: true, id: 'ev-tag', tip: '24 位十六进制，桩据此识别车辆在位；全部车辆内不能重复' })}
        <div class="a-alert is-err" hidden role="alert"></div></form>`);
      const tag = form.elements.pileTag; tag.addEventListener('input', () => { tag.value = tag.value.toUpperCase(); });
      ui.modal({
        title: `编辑车辆 ${r.plateNo}`, icon: false, body: form,
        actions: [{ label: '取消', kind: 'default' }, {
          label: '保存', kind: 'primary', keepOpen: true,
          async onClick(L, b) {
            const v = ui.validate(form, { model: { label: '车型', required: true, max: 30 }, pileTag: { label: '桩识别用车辆标识', required: true, pattern: TAG_RE, message: '应为 24 位十六进制（数字与 A–F）' } });
            if (!v.ok) return;
            if (v.values.model === r.model && v.values.pileTag === r.pileTag) { ui.toast('没有修改内容', { type: 'warn' }); return; }
            const al = ui.$('.a-alert', form); al.hidden = true;
            await busy(b, async () => {
              try { await V.update(r.vehicleId, { model: v.values.model, pileTag: v.values.pileTag }); }
              catch (e) { al.hidden = false; al.textContent = `${errText(e)}。没有产生任何变更。`; return; }
              ui.toast('已保存', { type: 'ok' }); L.close('ok'); drawerRef && drawerRef.close(); t.reload();
            });
          },
        }],
      });
    }

    /* ---------- 移除 ---------- */
    async function openRemove(r, drawerRef) {
      const res = await ui.confirm({
        title: `移除车辆 ${r.plateNo}`, danger: true, confirmText: '确认移除',
        message: `<p>移除后这辆车从清单中去掉，政府码 <b class="op-mono">${esc(r.govCode)}</b> 立即失效，用户扫码会提示"码已失效"。</p>`,
        impact: `1 辆车（${r.plateNo}，${r.brandName}），政府码失效后不可恢复，需重新提交清单才能再发码`,
        reasons: ['车辆报废', '车辆转让', '清单录入有误', '车辆退出运营'],
        onConfirm: reason => V.remove(r.vehicleId, reason),
      });
      if (res) { ui.toast(`已移除 ${r.plateNo}，政府码已失效`, { type: 'ok' }); drawerRef && drawerRef.close(); t.reload(); }
    }

    /* ---------- 在线新增 ---------- */
    function openAdd() {
      const form = ui.el(`<form class="a-form" novalidate>
        ${item('品牌', `<select id="ad-brand" name="brandId" class="a-select">${submitBrands.map(b => `<option value="${esc(b.brandId)}">${esc(b.name)}（${esc(b.accessStatus)}）</option>`).join('')}</select>`, { req: true, id: 'ad-brand' })}
        ${item('车号', `<input id="ad-plate" name="plateNo" class="a-input" maxlength="8" spellcheck="false" autocomplete="off">`, { req: true, id: 'ad-plate', tip: '2 位字母加 6 位数字' })}
        ${item('车型', `<input id="ad-model" name="model" class="a-input" maxlength="30">`, { req: true, id: 'ad-model' })}
        ${item('桩识别用车辆标识', `<input id="ad-tag" name="pileTag" class="a-input op-mono" maxlength="24" spellcheck="false" autocomplete="off">`, { req: true, id: 'ad-tag', tip: '24 位十六进制，桩据此识别车辆在位' })}
        <div class="a-alert is-info" id="ad-hint">提交后系统立即发放政府码。</div></form>`);
      const hint = ui.$('#ad-hint', form);
      const upper = n => form.elements[n].addEventListener('input', e => { e.target.value = e.target.value.toUpperCase(); });
      upper('plateNo'); upper('pileTag');
      const syncBrand = async () => {
        const b = submitBrands.find(x => x.brandId === form.elements.brandId.value);
        hint.textContent = b && b.accessStatus === '联调中' ? '该品牌正在联调，车辆入库后政府码为"待启用"，验收上线后统一启用。' : b && b.accessStatus === '暂停可租' ? '该品牌当前暂停可租，入库后政府码已启用，恢复可租后车辆即可被骑行。' : '提交后系统立即发放政府码，已接入品牌的码立即启用，用户即可扫码骑行。';
        try { const r = await V.list({ brandId: b.brandId, pageSize: 1 }); if (r.rows[0]) form.elements.plateNo.placeholder = `${r.rows[0].plateNo.slice(0, 2)} 开头，共 8 位`; } catch { /* 占位提示取不到不影响提交 */ }
      };
      form.elements.brandId.addEventListener('change', syncBrand); syncBrand();
      ui.modal({
        title: '新增车辆', icon: false, body: form,
        actions: [{ label: '取消', kind: 'default' }, {
          label: '提交并发码', kind: 'primary', keepOpen: true,
          async onClick(L, b) {
            const v = ui.validate(form, { plateNo: { label: '车号', required: true, pattern: PLATE_RE, message: '车号应为 2 位字母加 6 位数字' }, model: { label: '车型', required: true, max: 30 }, pileTag: { label: '桩识别用车辆标识', required: true, pattern: TAG_RE, message: '应为 24 位十六进制（数字与 A–F）' } });
            if (!v.ok) return;
            await busy(b, async () => {
              try {
                const r = await V.upload([{ brandId: v.values.brandId, plateNo: v.values.plateNo, model: v.values.model, pileTag: v.values.pileTag }]);
                ui.toast(`已入库，政府码 ${r.vehicles[0].govCode} 已发放`, { type: 'ok', hint: r.vehicles[0].codeStatus === '待启用' ? '该品牌验收上线后码自动启用' : '用户现在即可扫码骑行', duration: 4200 });
                L.close('ok'); t.reload();
              } catch (e) {
                if (e.code === 'VEHICLE_LIST_INVALID' && e.errors) showFieldErrors(form, e.errors); else fail(e, '提交失败');
              }
            });
          },
        }],
      });
    }
    function showFieldErrors(form, errors) {
      ui.$$('.a-item.is-error', form).forEach(i => i.classList.remove('is-error'));
      ui.$$('.err-msg', form).forEach(n => n.remove());
      let first;
      errors.forEach(er => {
        const c = form.elements[er.field]; const it = c && c.closest('.a-item'); if (!it) return;
        it.classList.add('is-error'); it.append(ui.el(`<span class="err-msg" role="alert">${esc(er.message)}</span>`));
        c.addEventListener('input', () => { it.classList.remove('is-error'); ui.$('.err-msg', it)?.remove(); }, { once: true });
        first = first || c;
      });
      first && first.focus();
    }

    /* ---------- 导出政府码清单 ---------- */
    async function exportCodes(b) {
      await busy(b, async () => {
        try {
          const all = []; let page = 1, total = 0;
          do { const r = await V.list({ ...F, page, pageSize: 500 }); all.push(...r.rows); total = r.total; page++; } while (all.length < total);
          if (!all.length) { ui.toast('当前筛选下没有车辆，没有生成文件', { type: 'warn' }); return; }
          const cols = [
            { title: '车号', value: r => r.plateNo }, { title: '政府码', value: r => r.govCode }, { title: '品牌', value: r => r.brandName }, { title: '车型', value: r => r.model },
            { title: '政府码状态', value: r => r.codeStatus }, { title: '发码时间', value: r => r.codeIssuedAt }, { title: '计划启用时间', value: r => r.codeEnableAt || '' }, { title: '失效时间', value: r => r.codeInvalidAt || '' },
            { title: '车辆状态', value: r => r.status }, { title: '桩识别标识', value: r => r.pileTag },
          ];
          const day = (await nowStr()).slice(0, 10);
          saveFile(`${me.operator.shortName}_政府码发放清单_${day}.csv`, toCsv(cols, all));
          ui.toast(`已导出 ${ui.number(all.length)} 辆车的政府码发放状态`, { type: 'ok' });
        } catch (e) { fail(e, '导出失败'); }
      });
    }

    /* ---------- 上传清单 ---------- */
    async function openUpload() {
      const state = { rows: [], fileName: '', existing: null, done: false };
      const root = ui.el('<div style="display:contents"></div>');
      const d = ui.drawer({ title: '上传车辆清单', sub: '选择 CSV 文件，逐行校验，全部通过后入库并自动发放政府码', body: root, wide: true, footer: [{ label: '取消', kind: 'default' }, { label: '确认入库并发码', kind: 'primary', keepOpen: true, onClick: () => confirmUpload() }] });
      d.el.classList.add('op-xl');
      const foot = ui.$$('.a-drawer-foot button', d.el);
      const [bCancel, bOk] = foot;
      ui.setDisabled(bOk, '还没有选择清单文件', '先选择 CSV 文件，校验通过后即可入库');
      const setOk = () => {
        const bad = state.rows.filter(r => r.errors.length).length;
        if (!state.rows.length) ui.setDisabled(bOk, '还没有选择清单文件', '先选择 CSV 文件，校验通过后即可入库');
        else if (bad) ui.setDisabled(bOk, `有 ${bad} 行未通过校验，不能入库`, '修改文件中的错误行后，重新选择文件');
        else { ui.setEnabled(bOk); bOk.textContent = `确认入库并发码（${state.rows.length} 辆）`; }
      };

      /* 先取现有车辆，用来核对重复 */
      const loadExisting = async () => {
        root.innerHTML = `<div class="op-pending"><span class="a-spin"></span>正在核对已有车辆，请稍候…</div>`;
        try {
          const plates = new Set(), tags = new Set(), prefix = {};
          let page = 1, total = 0, n = 0;
          do { const r = await V.list({ page, pageSize: 1000 }); r.rows.forEach(v => { plates.add(v.plateNo); tags.add(v.pileTag); prefix[v.brandId] = prefix[v.brandId] || v.plateNo.slice(0, 2); }); total = r.total; n += r.rows.length; page++; } while (n < total);
          state.existing = { plates, tags, prefix };
          drawPick();
        } catch (e) { root.innerHTML = ''; root.append(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: loadExisting })); }
      };

      const template = () => saveFile('车辆清单模板.csv', '﻿品牌,车号,车型,桩识别标识\r\n');
      function drawPick(msg) {
        state.rows = []; setOk();
        root.innerHTML = '';
        root.append(ui.el(`<div class="op-stack">
          <div class="a-alert is-info"><div class="grow">每行一辆车，四列：<b>品牌、车号、车型、桩识别标识</b>。品牌填名称（${submitBrands.map(b => esc(b.name)).join('、')}）；车号为 2 位字母加 6 位数字；桩识别标识为 24 位十六进制。整批校验，全部正确才会入库。</div><button type="button" class="a-btn a-btn-default is-sm" data-tpl>下载模板</button></div>
          ${msg ? `<div class="a-alert is-err" role="alert">${esc(msg)}</div>` : ''}
          <div class="op-drop" id="op-drop"><b>把 CSV 文件拖到这里</b><span class="op-note">或点击下方按钮选择；支持 UTF-8 与 GBK 编码，最多 5000 行，文件不超过 2 MB</span>
            <button type="button" class="a-btn a-btn-primary" data-pick>选择 CSV 文件</button><input type="file" accept=".csv,text/csv" hidden id="op-file"></div></div>`));
        const inp = ui.$('#op-file', root), drop = ui.$('#op-drop', root);
        ui.$('[data-tpl]', root).addEventListener('click', template);
        ui.$('[data-pick]', root).addEventListener('click', () => inp.click());
        inp.addEventListener('change', () => inp.files[0] && handleFile(inp.files[0]));
        drop.addEventListener('dragover', e => { e.preventDefault(); drop.classList.add('is-over'); });
        drop.addEventListener('dragleave', () => drop.classList.remove('is-over'));
        drop.addEventListener('drop', e => { e.preventDefault(); drop.classList.remove('is-over'); e.dataTransfer.files[0] && handleFile(e.dataTransfer.files[0]); });
      }

      async function handleFile(file) {
        if (!/\.csv$/i.test(file.name) && !/csv|text/.test(file.type)) { drawPick('只支持 CSV 文件。可在表格软件里"另存为 CSV"后重新选择。'); return; }
        if (file.size > 2 * 1024 * 1024) { drawPick('文件超过 2 MB，请拆成多个文件分批上传。'); return; }
        root.innerHTML = `<div class="op-pending"><span class="a-spin"></span>正在读取并逐行校验…</div>`;
        let text; try { text = await readTextFile(file); } catch { drawPick('文件读取失败，请重新选择。'); return; }
        await new Promise(r => setTimeout(r, 280));
        const table = parseCsv(text);
        if (!table.length) { drawPick('文件是空的，没有读到任何车辆。'); return; }
        const HEAD = { 品牌: 'brand', 车号: 'plateNo', 车型: 'model', 桩识别标识: 'pileTag', 车辆标识: 'pileTag', 桩识别用车辆标识: 'pileTag', brandid: 'brand', brand: 'brand', brandname: 'brand', plateno: 'plateNo', model: 'model', piletag: 'pileTag' };
        let cols = ['brand', 'plateNo', 'model', 'pileTag'], body = table;
        if (table[0].some(c => HEAD[c] || HEAD[String(c).toLowerCase()])) { cols = table[0].map(c => HEAD[c] || HEAD[String(c).toLowerCase()] || null); body = table.slice(1); }
        const need = ['brand', 'plateNo', 'model', 'pileTag'].filter(k => !cols.includes(k));
        if (need.length) { drawPick(`表头缺少列：${need.map(k => ({ brand: '品牌', plateNo: '车号', model: '车型', pileTag: '桩识别标识' }[k])).join('、')}。请使用模板。`); return; }
        if (!body.length) { drawPick('文件只有表头，没有车辆行。'); return; }
        if (body.length > 5000) { drawPick(`文件有 ${ui.number(body.length)} 行，超过 5000 行上限，请分批上传。`); return; }
        state.fileName = file.name;
        state.rows = validateRows(body.map((cells, i) => { const o = { line: i + 1 }; cols.forEach((k, j) => { if (k) o[k] = (cells[j] || '').trim(); }); return o; }));
        setOk(); drawPreview();
      }

      function validateRows(raw) {
        const seenP = new Map(), seenT = new Map();
        return raw.map(r => {
          const errors = [];
          const b = submitBrands.concat(brands).find(x => x.name === r.brand || x.brandId === (r.brand || '').toUpperCase() || x.displayName === r.brand);
          if (!r.brand) errors.push({ field: 'brandId', message: '请填写品牌' });
          else if (!b) errors.push({ field: 'brandId', message: `品牌「${r.brand}」不属于本运营商` });
          else if (['已退出', '未接入'].includes(b.accessStatus)) errors.push({ field: 'brandId', message: `品牌${b.accessStatus}，不能提交车辆` });
          const plate = (r.plateNo || '').toUpperCase();
          if (!PLATE_RE.test(plate)) errors.push({ field: 'plateNo', message: '车号应为 2 位字母加 6 位数字' });
          else {
            const pre = b && state.existing.prefix[b.brandId];
            if (pre && plate.slice(0, 2) !== pre) errors.push({ field: 'plateNo', message: `该品牌车号以 ${pre} 开头` });
            else if (state.existing.plates.has(plate)) errors.push({ field: 'plateNo', message: '车号已在清单中' });
            else if (seenP.has(plate)) errors.push({ field: 'plateNo', message: `车号与第 ${seenP.get(plate)} 行重复` });
            if (!seenP.has(plate)) seenP.set(plate, r.line);
          }
          const tag = (r.pileTag || '').toUpperCase();
          if (!TAG_RE.test(tag)) errors.push({ field: 'pileTag', message: '桩识别标识应为 24 位十六进制' });
          else {
            if (state.existing.tags.has(tag)) errors.push({ field: 'pileTag', message: '桩识别标识已被其他车辆使用' });
            else if (seenTag(tag)) errors.push({ field: 'pileTag', message: `桩识别标识与第 ${seenT.get(tag)} 行重复` });
            if (!seenT.has(tag)) seenT.set(tag, r.line);
          }
          function seenTag(x) { return seenT.has(x); }
          if (!r.model) errors.push({ field: 'model', message: '请填写车型' });
          return { line: r.line, brandId: b ? b.brandId : '', brandName: b ? b.name : r.brand, brandStatus: b ? b.accessStatus : '', plateNo: plate, model: r.model, pileTag: tag, errors };
        });
      }

      let view = 'all';
      function drawPreview() {
        const bad = state.rows.filter(r => r.errors.length), good = state.rows.length - bad.length;
        const pend = state.rows.some(r => !r.errors.length && r.brandStatus === '联调中');
        root.innerHTML = '';
        const top = ui.el(`<div class="op-stack">
          <div class="op-row"><b>${esc(state.fileName)}</b><span class="chip brand">共 ${ui.number(state.rows.length)} 行</span><span class="chip ok">通过 ${ui.number(good)}</span>${bad.length ? `<span class="chip err">错误 ${ui.number(bad.length)} 行</span>` : ''}<span class="a-grow" style="flex:1"></span>
            ${bad.length ? '<button type="button" class="a-btn a-btn-default is-sm" data-errcsv>下载错误明细</button>' : ''}<button type="button" class="a-btn a-btn-default is-sm" data-again>重新选择文件</button></div>
          ${bad.length ? `<div class="a-alert is-err" role="alert"><div class="grow">有 ${bad.length} 行没有通过校验，清单不会入库。请在文件里修改错误行后重新选择文件；也可以只看错误行。</div></div>`
            : `<div class="a-alert is-ok"><div class="grow">全部 ${ui.number(good)} 行校验通过。确认后车辆入库，系统自动发放 ${ui.number(good)} 个政府码。${pend ? '其中联调中品牌的车辆，政府码为"待启用"，验收上线后统一启用。' : '已接入品牌的政府码立即启用。'}</div></div>`}
          <div class="op-row"><div class="a-tabs" id="op-vt" style="margin:0;border:0"></div></div>
          <div id="op-pv"></div></div>`);
        root.append(top);
        ui.$('[data-again]', root).addEventListener('click', () => drawPick());
        ui.$('[data-errcsv]', root)?.addEventListener('click', () => saveFile('车辆清单_错误明细.csv', toCsv([{ title: '行号', value: r => r.line }, { title: '品牌', value: r => r.brandName }, { title: '车号', value: r => r.plateNo }, { title: '车型', value: r => r.model }, { title: '桩识别标识', value: r => r.pileTag }, { title: '错误原因', value: r => r.errors.map(e => e.message).join('；') }], bad)));
        ui.tabs(ui.$('#op-vt', root), [{ key: 'all', label: '全部', count: state.rows.length }, { key: 'bad', label: '仅错误行', count: bad.length }], bad.length && view === 'bad' ? 'bad' : (view = 'all'), k => { view = k; drawTable(); });
        if (bad.length && view === 'all') { /* 保持全部，用户可切换 */ }
        drawTable();
      }
      function drawTable() {
        const host = ui.$('#op-pv', root); if (!host) return;
        const rows = view === 'bad' ? state.rows.filter(r => r.errors.length) : state.rows;
        ui.table(host, {
          rowKey: 'line', rows, pageSize: 10, pageSizes: [10, 20, 50],
          columns: [
            { key: 'line', title: '行号', align: 'right', width: '64px' },
            { key: 'brandName', title: '品牌', render: r => esc(r.brandName || '—') },
            { key: 'plateNo', title: '车号', render: r => `<span class="op-strong">${esc(r.plateNo || '—')}</span>` },
            { key: 'model', title: '车型', render: r => esc(r.model || '—') },
            { key: 'pileTag', title: '桩识别标识', render: r => `<span class="op-mono">${esc(r.pileTag || '—')}</span>` },
            { key: 'res', title: '校验结果', render: r => (r.errors.length ? `<span class="chip err">未通过</span> <span style="color:var(--err);white-space:normal;display:inline-block">${esc(r.errors.map(e => e.message).join('；'))}</span>` : '<span class="chip ok">通过</span>') },
          ],
          empty: { title: '没有错误行', text: '所有行都通过了校验' },
        });
      }

      /* 确认入库 */
      async function confirmUpload() {
        const n = state.rows.length; let result;
        const pend = state.rows.some(r => r.brandStatus === '联调中');
        const res = await ui.confirm({
          title: '确认入库并发码', requireReason: false, confirmText: '确认入库',
          message: `<p>将把 <b>${ui.number(n)}</b> 辆车写入车辆清单，并立即发放政府码。</p>`,
          impact: `新增 ${ui.number(n)} 辆车，发放 ${ui.number(n)} 个政府码；${pend ? '联调中品牌的码为待启用，验收上线后启用' : '码立即启用，用户即可扫码骑行'}`,
          onConfirm: async () => {
            try { result = await V.upload(state.rows.map(r => ({ brandId: r.brandId, plateNo: r.plateNo, model: r.model, pileTag: r.pileTag }))); }
            catch (e) {
              if (e.code === 'VEHICLE_LIST_INVALID' && e.errors) {
                e.errors.forEach(er => { const r = state.rows[er.row - 1]; if (r) r.errors.push({ field: er.field, message: er.message }); });
                setOk(); drawPreview();
                throw new Error(`${e.message}。清单没有入库，请先处理预览中标出的错误行。`);
              }
              throw e;
            }
          },
        });
        if (!res) return;
        state.done = true; drawDone(result);
      }
      function drawDone(r) {
        root.innerHTML = '';
        const pending = r.vehicles.filter(v => v.codeStatus === '待启用').length;
        root.append(ui.el(`<div class="op-stack">
          <div class="a-alert is-ok" role="status"><div class="grow"><b>已入库 ${ui.number(r.added)} 辆，政府码已发放。</b>${pending ? `其中 ${pending} 辆属联调中品牌，码为"待启用"，验收上线后统一启用。` : '政府码已启用，用户现在即可扫码骑行。'}</div></div>
          <div id="op-done"></div></div>`));
        ui.table(ui.$('#op-done', root), {
          rowKey: 'vehicleId', rows: r.vehicles, pageSize: 10,
          columns: [
            { key: 'plateNo', title: '车号', render: x => `<span class="op-strong">${esc(x.plateNo)}</span>` }, { key: 'brandName', title: '品牌' }, { key: 'model', title: '车型' },
            { key: 'govCode', title: '政府码', render: x => `<span class="op-mono">${esc(x.govCode)}</span>` }, { key: 'codeStatus', title: '政府码状态', render: x => chip(x.codeStatus, 'codeStatus') }, { key: 'codeIssuedAt', title: '发码时间', render: x => dt(x.codeIssuedAt) },
          ],
        });
        bCancel.textContent = '完成';
        bOk.textContent = '下载本批政府码'; ui.setEnabled(bOk);
        bOk.onclick = null;
        const nb = bOk.cloneNode(true); bOk.replaceWith(nb);
        nb.addEventListener('click', () => saveFile(`新增车辆政府码_${r.vehicles[0].codeIssuedAt.slice(0, 10)}.csv`, toCsv([{ title: '车号', value: x => x.plateNo }, { title: '政府码', value: x => x.govCode }, { title: '品牌', value: x => x.brandName }, { title: '车型', value: x => x.model }, { title: '政府码状态', value: x => x.codeStatus }, { title: '发码时间', value: x => x.codeIssuedAt }], r.vehicles)));
        t.reload();
      }
      loadExisting();
    }
  },
};

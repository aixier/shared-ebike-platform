/* 价格与规则配置（R-19～R-22）：一个入口录入全部规则，每次保存新增一个版本，旧版本留存。 */
import { A, ui, esc, chip, kv, failText, lifecycle, loadInto, setHead, bizNow, toLocalInput, fromLocalInput, pct } from './_common.js';

const KIND = { 生效中: 'ok', 待生效: 'warn', 已失效: 'mute', 已撤销: 'mute' };
const money = v => ui.money(v);

/* ---------- 版本内容：展开成「分组 + 名称 + 文案」，用于展示与对比 ---------- */
function flat(v, brands) {
  const out = [];
  const add = (g, l, t) => out.push({ g, l, t: String(t) });
  add('骑行计费', '起步价', `${money(v.ride.startFee)}（含 ${v.ride.startMinutes} 分钟）`);
  add('骑行计费', '分钟价', `${money(v.ride.perMinute)} / 分钟`);
  add('骑行计费', '调度费', `${money(v.dispatchFee)}（站点外还车）`);
  add('月卡', '价格', `${money(v.monthCard.price)} / ${v.monthCard.days} 天`);
  add('月卡', '每单抵扣上限', `${money(v.monthCard.perOrderCap)}（${v.monthCard.appliesTo || '骑行费'}）`);
  add('月卡', '收入分配', v.monthCard.allocation || '—');
  (v.coupons || []).forEach(c => add('优惠券', `${c.name}（${c.templateId}）`, `面额 ${money(c.amount)}，有效 ${c.validDays} 天；对象：${c.audience}；发放：${c.issue}`));
  add('押金与免押', '押金金额', money(v.deposit.amount));
  add('押金与免押', '免押门槛', `${v.creditFree.threshold} 分（${(v.creditFree.providers || []).join('、')}）`);
  Object.entries(v.serviceFee.ratios).forEach(([b, r]) => add('服务费比例', brands[b] || b, pct(r)));
  const r = v.riskLimits;
  add('风控限额', '常规档单笔上限', `${r.normal.maxMinutes} 分钟 / ${money(r.normal.maxAmount)}`);
  add('风控限额', '紧档单笔上限', `${r.tight.maxMinutes} 分钟 / ${money(r.tight.maxAmount)}`);
  add('风控限额', '每用户未结降级单', `${r.perUserOpenDegradedOrders} 单`);
  add('风控限额', '每小时降级单总量', `${r.hourlyDegradedOrders} 单`);
  add('风控限额', '降级持续告警', `${r.degradeDurationAlertMinutes} 分钟`);
  return out;
}
function diff(a, b, brands) {
  const fa = new Map(flat(a, brands).map(x => [x.g + '|' + x.l, x])), fb = new Map(flat(b, brands).map(x => [x.g + '|' + x.l, x]));
  const rows = [];
  for (const [k, x] of fb) { const o = fa.get(k); if (!o) rows.push({ g: x.g, l: x.l, from: '无', to: x.t }); else if (o.t !== x.t) rows.push({ g: x.g, l: x.l, from: o.t, to: x.t }); }
  for (const [k, x] of fa) if (!fb.has(k)) rows.push({ g: x.g, l: x.l, from: x.t, to: '已删除' });
  return rows;
}
const diffTable = rows => `<div class="g-diff-wrap"><table class="g-diff"><thead><tr><th>项目</th><th>原值</th><th>新值</th></tr></thead><tbody>${rows.map(r => `<tr><td>${esc(r.g)} · ${esc(r.l)}</td><td>${esc(r.from)}</td><td class="to">${esc(r.to)}</td></tr>`).join('')}</tbody></table></div>`;
const section = (v, brands) => {
  const g = {};
  flat(v, brands).forEach(x => (g[x.g] = g[x.g] || []).push([x.l, esc(x.t)]));
  return Object.entries(g).map(([t, rows]) => `<div><div class="a-sec-title">${esc(t)}</div>${kv(rows)}</div>`).join('');
};

/** 与接口层一致的合并：只覆盖给出的项 */
function merge(base, p) {
  const v = JSON.parse(JSON.stringify(base));
  if (p.ride) Object.assign(v.ride, p.ride);
  if (p.dispatchFee != null) v.dispatchFee = p.dispatchFee;
  if (p.monthCard) Object.assign(v.monthCard, p.monthCard);
  if (p.coupons) v.coupons = JSON.parse(JSON.stringify(p.coupons));
  if (p.deposit) Object.assign(v.deposit, p.deposit);
  if (p.creditFree) Object.assign(v.creditFree, p.creditFree);
  if (p.serviceFee && p.serviceFee.ratios) Object.assign(v.serviceFee.ratios, p.serviceFee.ratios);
  if (p.riskLimits) { const r = p.riskLimits; v.riskLimits = { ...v.riskLimits, ...r, normal: { ...v.riskLimits.normal, ...(r.normal || {}) }, tight: { ...v.riskLimits.tight, ...(r.tight || {}) } }; }
  return v;
}
/** 生效时刻起适用的版本：未撤销、生效时间不晚于该时刻的最后一个 */
const baseAt = (versions, eff) => [...versions].filter(v => v.status !== '已撤销' && v.effectiveAt <= eff).sort((a, b) => (a.effectiveAt < b.effectiveAt ? -1 : 1)).pop() || versions.find(v => v.status === '生效中');

function mount(el, query, pctx) {
  const life = lifecycle(el);
  el.innerHTML = '';
  const host = ui.el('<div class="g-stack"></div>');
  el.append(host);
  let state = { versions: [], brands: {}, now: '' };
  let tableApi = null, filterKey = '全部';

  async function fetchAll() {
    const [versions, now, ov] = await Promise.all([A.pricing.list(), bizNow(), A.access.overview().catch(() => null)]);
    const brands = {};
    if (ov) ov.brands.forEach(b => (brands[b.brandId] = b.name));
    return { versions, now, brands };
  }

  /* ================= 列表视图 ================= */
  async function showList() {
    host.innerHTML = '';
    const hb = setHead(pctx, '单价、调度费、优惠券、月卡、押金与免押、服务费比例、风控限额都在这里设置；每次保存生成一个新版本，到点生效，旧版本留存可查', [{ id: 'new', label: '新建规则版本', kind: 'primary' }]);
    const body = ui.el('<div class="g-stack"></div>');
    host.append(body);
    hb.new.addEventListener('click', () => {
      if (!state.versions.length) { ui.toast('规则版本还没有加载出来', { type: 'warn', hint: '等页面加载完成，或点重新加载后再新建' }); return; }
      openEditor(state.versions.find(v => v.status === '生效中') || state.versions[0]);
    });
    await loadInto(body, fetchAll, data => {
      state = data;
      const cur = state.versions.find(v => v.status === '生效中');
      const pend = state.versions.filter(v => v.status === '待生效').sort((a, b) => (a.effectiveAt < b.effectiveAt ? -1 : 1));
      if (pend.length) {
        const first = pend[0], base = cur ? diff(cur, first, state.brands) : [];
        body.append(ui.el(`<div class="g-pending">待生效 ${pend.length} 个版本：${esc(first.versionId)}「${esc(first.name)}」将于 ${ui.datetime(first.effectiveAt)} 生效${base.length ? `，主要变化：${esc(base.slice(0, 2).map(r => (r.from === '无' ? `新增${r.l.replace(/（.*/, '')}` : `${r.l}：${r.from.replace(/（.*/, '')} → ${r.to.replace(/（.*/, '')}`)).join('；'))}${base.length > 2 ? ` 等 ${base.length} 项` : ''}` : ''}。在此之前现行规则继续适用。</div>`));
      }
      if (cur) {
        const sum = ui.el(`<div class="a-panel"><div class="a-panel-head"><h3>现行规则 · ${esc(cur.versionId)}「${esc(cur.name)}」</h3><span class="g-note">${ui.datetime(cur.effectiveAt)} 起生效</span></div>
          <div class="g-sum">
            ${[['起步价', `${money(cur.ride.startFee)}<small>含 ${cur.ride.startMinutes} 分钟</small>`], ['分钟价', `${money(cur.ride.perMinute)}<small>/ 分钟</small>`], ['调度费', money(cur.dispatchFee)],
              ['月卡', `${money(cur.monthCard.price)}<small>${cur.monthCard.days} 天 · 每单抵扣 ≤ ${money(cur.monthCard.perOrderCap)}</small>`], ['押金', money(cur.deposit.amount)], ['免押门槛', `${cur.creditFree.threshold}<small>分</small>`],
              ['在发优惠券', `${cur.coupons.length}<small>种</small>`], ['降级单笔上限', `${cur.riskLimits.normal.maxMinutes} 分钟 / ${money(cur.riskLimits.normal.maxAmount)}<small>常规档</small>`]]
              .map(([k, v]) => `<div class="it"><div class="k">${k}</div><div class="v">${v}</div></div>`).join('')}
          </div></div>`);
        body.append(sum);
      }
      const panel = ui.el('<div><div id="tabs"></div><div id="tbl"></div></div>');
      body.append(panel);
      const count = k => (k === '全部' ? state.versions.length : state.versions.filter(v => v.status === k).length);
      ui.tabs(panel.querySelector('#tabs'), ['全部', '生效中', '待生效', '已失效', '已撤销'].map(k => ({ key: k, label: k, count: count(k) })), filterKey, k => { filterKey = k; tableApi.setRows(rows()); });
      const rows = () => state.versions.filter(v => filterKey === '全部' || v.status === filterKey);
      tableApi = ui.table(panel.querySelector('#tbl'), {
        rowKey: 'versionId', pageSize: 10, rows: rows(), onRowClick: openDetail,
        empty: { title: '这个状态下没有版本', text: '切换上方标签查看其他版本' },
        columns: [
          { key: 'versionId', title: '版本号', render: r => `<span class="mono">${esc(r.versionId)}</span>`, width: '90px' },
          { key: 'name', title: '名称', render: r => esc(r.name) },
          { key: 'status', title: '状态', render: r => chip(r.status, KIND[r.status]), width: '90px' },
          { key: 'effectiveAt', title: '生效时间', sortable: true, render: r => `<span class="num">${ui.datetime(r.effectiveAt)}</span>` },
          { key: 'effectiveUntil', title: '失效时间', render: r => (r.effectiveUntil ? `<span class="num">${ui.datetime(r.effectiveUntil)}</span>` : '—') },
          { key: 'note', title: '变更说明', ellipsis: true, render: r => `<span title="${esc(r.note || '')}">${esc(r.note || '—')}</span>` },
        ],
      });
    });
  }

  /* ================= 版本详情抽屉 ================= */
  function openDetail(v) {
    const older = state.versions.filter(x => x.status !== '已撤销' && x.effectiveAt < v.effectiveAt).sort((a, b) => (a.effectiveAt < b.effectiveAt ? -1 : 1)).pop();
    const d = older ? diff(older, v, state.brands) : [];
    const footer = [{ label: '基于此版本新建', kind: 'primary', onClick: () => { setTimeout(() => openEditor(v), 260); } }];
    if (v.status === '待生效') footer.unshift({ label: '撤销该版本', kind: 'danger', keepOpen: true, onClick: () => revoke(v, dr) });
    const dr = ui.drawer({
      title: `${v.versionId}「${v.name}」`, sub: `${v.status} · ${ui.datetime(v.effectiveAt)} 起${v.effectiveUntil ? `，至 ${ui.datetime(v.effectiveUntil)}` : ''}`, wide: true, footer,
      body: `${v.status === '已撤销' ? `<div class="a-alert is-info">该版本已于 ${ui.datetime(v.revokedAt)} 撤销，未曾生效，记录保留。</div>` : ''}
        <div><div class="a-sec-title">变更说明</div><div>${esc(v.note || '—')}</div></div>
        ${older ? `<div><div class="a-sec-title">相对上一版 ${esc(older.versionId)} 的变化（${d.length} 项）</div>${d.length ? diffTable(d) : '<div class="a-muted">取值没有变化</div>'}</div>` : ''}
        ${section(v, state.brands)}
        <div class="g-note">订单按开锁请求时刻有效的版本计价，改价不影响此前已开锁的订单。</div>`,
    });
  }

  async function revoke(v, dr) {
    const r = await ui.confirm({
      title: `撤销版本 ${v.versionId}`, danger: true, confirmText: '确认撤销',
      message: `撤销后 ${esc(v.versionId)}「${esc(v.name)}」不会再生效，记录保留可查，不能恢复。`,
      impact: `${ui.datetime(v.effectiveAt)} 的生效计划取消，现行规则继续适用`,
      reasons: ['录入有误，重新录入', '政策调整，暂缓执行'],
      onConfirm: async reason => { try { await A.pricing.revoke(v.versionId, reason); } catch (e) { throw new Error(failText(e)); } },
    });
    if (r) { dr.close(); ui.toast(`已撤销 ${v.versionId}`, { type: 'ok', hint: '版本记录里仍可查到' }); showList(); }
  }

  /* ================= 编辑视图 ================= */
  function openEditor(base) {
    const cur = state.versions.find(v => v.status === '生效中') || base;
    base = base || cur;
    host.innerHTML = '';
    const hb = setHead(pctx, `新建规则版本：以 ${base.versionId}「${base.name}」的取值为起点，只提交改动的项，未改动的项沿用生效时刻的规则`, [{ id: 'back', label: '返回版本列表', kind: 'default' }]);
    const form = ui.el(`<form class="g-editor" novalidate autocomplete="off"></form>`);
    host.append(form);
    let dirty = false, couponSeq = 0;
    const input = (name, label, val, { unit = '', tip = '', type = 'text', req = true, attrs = '' } = {}) =>
      `<div class="a-item"><label class="${req ? 'req' : ''}" for="f-${name}">${label}</label><div class="a-input-group"><input id="f-${name}" name="${name}" class="a-input" value="${esc(val)}" ${type === 'number' ? 'inputmode="decimal"' : ''} ${attrs}>${unit ? `<span class="unit">${unit}</span>` : ''}</div>${tip ? `<span class="tip">${tip}</span>` : ''}</div>`;
    const couponRow = (c = {}) => {
      const k = couponSeq++;
      const iss = ['注册后自动发放', '系统一次性发放', '用户在券中心领取（每周五 17:00 开放）', '开锁失败后自动发放'];
      if (c.issue && !iss.includes(c.issue)) iss.push(c.issue);
      return `<div class="g-coupon" data-k="${k}" data-tid="${esc(c.templateId || '')}">
        <div class="a-item"><label for="f-cp${k}_name">券名称</label><input id="f-cp${k}_name" name="cp${k}_name" class="a-input" value="${esc(c.name || '')}" placeholder="如：周末骑行券"></div>
        <div class="a-item"><label for="f-cp${k}_amount">面额（元）</label><input id="f-cp${k}_amount" name="cp${k}_amount" class="a-input" value="${c.amount ?? ''}" inputmode="decimal"></div>
        <div class="a-item"><label for="f-cp${k}_audience">发放对象</label><input id="f-cp${k}_audience" name="cp${k}_audience" class="a-input" value="${esc(c.audience || '')}" placeholder="如：全部实名用户"></div>
        <div class="a-item"><label for="f-cp${k}_issue">发放方式</label><select id="f-cp${k}_issue" name="cp${k}_issue" class="a-select">${iss.map(x => `<option ${x === c.issue ? 'selected' : ''}>${esc(x)}</option>`).join('')}</select></div>
        <div class="a-item"><label for="f-cp${k}_days">有效天数</label><input id="f-cp${k}_days" name="cp${k}_days" class="a-input" value="${c.validDays ?? ''}" inputmode="numeric"></div>
        <button type="button" class="a-btn a-btn-text g-x" data-rm="${k}" aria-label="删除这张券">删除</button></div>`;
    };
    const v = base;
    form.innerHTML = `
      <div class="g-anchor">${['生效时间', '骑行计费', '月卡', '优惠券', '押金与免押', '服务费比例', '风控限额'].map((t, i) => `<button type="button" data-go="s${i}">${t}</button>`).join('')}</div>
      <section class="a-panel g-sec" id="s0"><div class="a-panel-head"><h3>版本与生效时间</h3></div><div class="a-panel-body">
        <div class="g-fields">${input('name', '版本名称', '', { attrs: 'maxlength="30" placeholder="如：2027年1月调价"', tip: '便于在版本记录里识别，不超过 30 字' })}
          <div class="a-item"><label class="req">生效时间</label><div class="a-radio"><label><input type="radio" name="effMode" value="now" checked>保存后立即生效</label><label><input type="radio" name="effMode" value="later">指定时间</label></div></div>
          <div class="a-item"><label for="f-effAt">指定生效时间</label><input id="f-effAt" name="effAt" type="datetime-local" class="a-input" disabled><span class="tip">只影响生效之后开锁的订单</span></div></div>
        <div class="g-pending" id="pendHint" hidden></div></div></section>
      <section class="a-panel g-sec" id="s1"><div class="a-panel-head"><h3>骑行计费</h3><span class="g-note">单价按车型设置，当前只有电单车一种车型</span></div><div class="a-panel-body"><div class="g-fields">
        ${input('startFee', '起步价', v.ride.startFee, { unit: '元', tip: '0～20，最多 1 位小数' })}${input('startMinutes', '起步含时长', v.ride.startMinutes, { unit: '分钟', tip: '0～60 的整数' })}
        ${input('perMinute', '分钟价', v.ride.perMinute, { unit: '元 / 分钟', tip: '0～5，最多 1 位小数；不足 1 分钟按 1 分钟计' })}${input('dispatchFee', '调度费', v.dispatchFee, { unit: '元', tip: '站点外还车加收，0～100' })}</div></div></section>
      <section class="a-panel g-sec" id="s2"><div class="a-panel-head"><h3>月卡</h3></div><div class="a-panel-body"><div class="g-fields">
        ${input('mcPrice', '月卡价格', v.monthCard.price, { unit: '元', tip: '0～500' })}${input('mcDays', '有效天数', v.monthCard.days, { unit: '天', tip: '1～365 的整数' })}
        ${input('mcCap', '每单抵扣上限', v.monthCard.perOrderCap, { unit: '元', tip: `只抵${esc(v.monthCard.appliesTo || '骑行费')}，0～50` })}
        <div class="a-item"><label class="req" for="f-mcAlloc">月卡收入分配</label><input id="f-mcAlloc" name="mcAlloc" class="a-input" list="allocs" value="${esc(v.monthCard.allocation || '')}" maxlength="30"><datalist id="allocs"><option value="按骑行单数"><option value="按骑行时长"><option value="按骑行里程"></datalist><span class="tip">月卡收入在运营商之间的归属方式，仅作收入归属展示</span></div></div></div></section>
      <section class="a-panel g-sec" id="s3"><div class="a-panel-head"><h3>优惠券规则</h3><button type="button" class="a-btn a-btn-default is-sm" id="addCp">新增一种券</button></div><div class="a-panel-body" id="cpBox">${(v.coupons || []).map(couponRow).join('')}</div></section>
      <section class="a-panel g-sec" id="s4"><div class="a-panel-head"><h3>押金与免押门槛</h3></div><div class="a-panel-body"><div class="g-fields">
        ${input('depAmount', '押金金额', v.deposit.amount, { unit: '元', tip: '新用户按新金额缴纳；已缴押金的用户不补差、不退差，0～1000' })}${input('creditTh', '免押信用分门槛', v.creditFree.threshold, { unit: '分', tip: `${esc((v.creditFree.providers || []).join('、'))}；350～950 的整数` })}</div></div></section>
      <section class="a-panel g-sec" id="s5"><div class="a-panel-head"><h3>服务费比例（按品牌）</h3><span class="g-note">月结按开锁时刻有效的比例计算，基数为订单应收</span></div><div class="a-panel-body"><div class="g-fields">
        ${Object.entries(v.serviceFee.ratios).map(([b, r]) => input('ratio_' + b, `${esc(state.brands[b] || b)}（${b}）`, +(r * 100).toFixed(2), { unit: '%', tip: '0～100，最多 2 位小数' })).join('')}</div></div></section>
      <section class="a-panel g-sec" id="s6"><div class="a-panel-head"><h3>风控限额（降级开单）</h3><span class="g-note">平台降级期间用户可开的单笔上限</span></div><div class="a-panel-body"><div class="g-fields">
        ${input('rlNormMin', '常规档单笔最长', v.riskLimits.normal.maxMinutes, { unit: '分钟', tip: '1～1440 的整数' })}${input('rlNormAmt', '常规档单笔最高', v.riskLimits.normal.maxAmount, { unit: '元', tip: '0～1000' })}
        ${input('rlTightMin', '紧档单笔最长', v.riskLimits.tight.maxMinutes, { unit: '分钟', tip: '先骑后验、押金状态未知用户；不高于常规档' })}${input('rlTightAmt', '紧档单笔最高', v.riskLimits.tight.maxAmount, { unit: '元', tip: '不高于常规档' })}
        ${input('rlPerUser', '每用户未结降级单', v.riskLimits.perUserOpenDegradedOrders, { unit: '单', tip: '1～5 的整数' })}${input('rlHourly', '每小时降级单总量', v.riskLimits.hourlyDegradedOrders, { unit: '单', tip: '1～100000 的整数' })}
        ${input('rlAlert', '降级持续告警', v.riskLimits.degradeDurationAlertMinutes, { unit: '分钟', tip: '1～1440 的整数' })}</div></div></section>
      <div class="g-bar"><span class="g-note" id="barNote">改动会在提交前列出，确认后才保存</span><span class="g-row"><button type="button" class="a-btn a-btn-default" id="cancel">取消</button><button type="submit" class="a-btn a-btn-primary" id="submit">检查并提交</button></span></div>`;

    const q = s => form.querySelector(s);
    const effAt = q('[name=effAt]');
    bizNow().then(now => { effAt.min = toLocalInput(now); const t = new Date(now.replace(' ', 'T')); t.setDate(t.getDate() + 1); t.setHours(0, 0, 0, 0); const p = n => String(n).padStart(2, '0'); effAt.value = `${t.getFullYear()}-${p(t.getMonth() + 1)}-${p(t.getDate())}T00:00`; state.now = now; });
    const syncEff = () => {
      const later = form.elements.effMode.value === 'later';
      effAt.disabled = !later;
      const eff = later && effAt.value ? fromLocalInput(effAt.value) : state.now;
      const after = state.versions.filter(x => x.status === '待生效' && eff && x.effectiveAt > eff);
      const h = q('#pendHint');
      h.hidden = !after.length;
      if (after.length) h.textContent = `此后还有待生效版本 ${after.map(x => `${x.versionId}（${ui.datetime(x.effectiveAt)}）`).join('、')}，它们各项取值是独立的完整快照，不会随这次修改变化；需要一并调整时，请撤销后重建或另建版本。`;
    };
    form.addEventListener('change', e => { if (e.target.name === 'effMode' || e.target.name === 'effAt') syncEff(); });
    form.addEventListener('input', () => { dirty = true; });
    form.addEventListener('click', e => {
      const go = e.target.closest('[data-go]'); if (go) { q('#' + go.dataset.go).scrollIntoView({ behavior: 'smooth', block: 'start' }); return; }
      const rm = e.target.closest('[data-rm]'); if (rm) { rm.closest('.g-coupon').remove(); dirty = true; return; }
      if (e.target.closest('#addCp')) { q('#cpBox').insertAdjacentHTML('beforeend', couponRow()); dirty = true; q('#cpBox').lastElementChild.querySelector('input').focus(); }
    });
    const leave = async () => {
      if (dirty) {
        const ok = await new Promise(res => ui.modal({ title: '放弃未提交的修改？', body: '关闭后填写的内容不会保存。', onClose: () => res(false), actions: [{ label: '继续填写', kind: 'default', onClick: () => { res(false); } }, { label: '放弃修改', kind: 'danger', onClick: () => { res(true); } }] }));
        if (!ok) return;
      }
      showList();
    };
    q('#cancel').addEventListener('click', leave);
    hb.back.addEventListener('click', leave);
    form.addEventListener('submit', e => { e.preventDefault(); submit(); });

    /* ---------- 校验 ---------- */
    const N = (label, lo, hi, dec) => ({ label, required: true, type: 'number', minValue: lo, maxValue: hi, custom: x => (typeof x === 'number' && Math.abs(x * 10 ** dec - Math.round(x * 10 ** dec)) > 1e-6 ? (dec ? `${label}最多 ${dec} 位小数` : `${label}须为整数`) : '') });
    async function submit() {
      const now = await bizNow(); state.now = now;
      const rules = {
        name: { label: '版本名称', required: true, max: 30 },
        effAt: { label: '生效时间', custom: (x, all) => (all.effMode === 'later' ? (!x ? '请选择生效时间' : fromLocalInput(x) <= now ? '生效时间必须晚于现在，改动只影响生效之后开锁的订单' : '') : '') },
        startFee: N('起步价', 0, 20, 1), startMinutes: N('起步含时长', 0, 60, 0), perMinute: N('分钟价', 0, 5, 1), dispatchFee: N('调度费', 0, 100, 1),
        mcPrice: N('月卡价格', 0, 500, 1), mcDays: N('月卡有效天数', 1, 365, 0), mcCap: N('每单抵扣上限', 0, 50, 1), mcAlloc: { label: '月卡收入分配', required: true, max: 30 },
        depAmount: N('押金金额', 0, 1000, 1), creditTh: N('免押门槛', 350, 950, 0),
        rlNormMin: N('常规档单笔最长', 1, 1440, 0), rlNormAmt: N('常规档单笔最高', 0, 1000, 1), rlPerUser: N('每用户未结降级单', 1, 5, 0), rlHourly: N('每小时降级单总量', 1, 100000, 0), rlAlert: N('降级持续告警', 1, 1440, 0),
        rlTightMin: { ...N('紧档单笔最长', 1, 1440, 0), custom: (x, all) => N('', 1, 1440, 0).custom(x) || (typeof x === 'number' && typeof all.rlNormMin === 'number' && x > all.rlNormMin ? '紧档不能高于常规档' : '') },
        rlTightAmt: { ...N('紧档单笔最高', 0, 1000, 1), custom: (x, all) => N('紧档单笔最高', 0, 1000, 1).custom(x) || (typeof x === 'number' && typeof all.rlNormAmt === 'number' && x > all.rlNormAmt ? '紧档不能高于常规档' : '') },
      };
      Object.keys(v.serviceFee.ratios).forEach(b => { rules['ratio_' + b] = N(`${state.brands[b] || b}服务费比例`, 0, 100, 2); });
      const rows = [...form.querySelectorAll('.g-coupon')];
      rows.forEach(r => {
        const k = r.dataset.k;
        rules[`cp${k}_name`] = { label: '券名称', required: true, max: 20 };
        rules[`cp${k}_amount`] = N('券面额', 0.1, 50, 1);
        rules[`cp${k}_audience`] = { label: '发放对象', required: true, max: 30 };
        rules[`cp${k}_days`] = N('券有效天数', 1, 90, 0);
      });
      const res = ui.validate(form, rules);
      if (res.ok) {
        const names = rows.map(r => res.values[`cp${r.dataset.k}_name`]);
        const dup = names.findIndex((n, i) => names.indexOf(n) !== i);
        if (dup >= 0) { const inp = form.elements[`cp${rows[dup].dataset.k}_name`]; const it = inp.closest('.a-item'); it.classList.add('is-error'); it.append(ui.el('<span class="err-msg" role="alert">券名称不能重复</span>')); inp.focus(); res.ok = false; }
      }
      if (!res.ok) { ui.toast('有 ' + Object.keys(res.errors).length + ' 项填写有误', { type: 'err', hint: '已定位到第一项，改正后再提交' }); return; }
      const x = res.values;
      const eff = x.effMode === 'later' ? fromLocalInput(x.effAt) : null;
      const rb = baseAt(state.versions, eff || now);
      // 只收集「相对表单起点 的取值」被用户改动的字段，其余沿用生效时刻的规则
      const same = (a, b) => (typeof a === 'number' && typeof b === 'number' ? Math.abs(a - b) < 1e-9 : JSON.stringify(a) === JSON.stringify(b));
      const pick = (a, b) => Object.fromEntries(Object.entries(b).filter(([k, val]) => !same(a[k], val)));
      const sent = { name: x.name };
      if (eff) sent.effectiveAt = eff;
      const ride = pick(v.ride, { startFee: x.startFee, startMinutes: x.startMinutes, perMinute: x.perMinute });
      if (Object.keys(ride).length) sent.ride = ride;
      if (!same(v.dispatchFee, x.dispatchFee)) sent.dispatchFee = x.dispatchFee;
      const mc = pick(v.monthCard, { price: x.mcPrice, days: x.mcDays, perOrderCap: x.mcCap, allocation: x.mcAlloc });
      if (Object.keys(mc).length) sent.monthCard = mc;
      const dep = pick(v.deposit, { amount: x.depAmount });
      if (Object.keys(dep).length) sent.deposit = dep;
      const cf = pick(v.creditFree, { threshold: x.creditTh });
      if (Object.keys(cf).length) sent.creditFree = cf;
      const ratios = pick(v.serviceFee.ratios, Object.fromEntries(Object.keys(v.serviceFee.ratios).map(b => [b, +(x['ratio_' + b] / 100).toFixed(4)])));
      if (Object.keys(ratios).length) sent.serviceFee = { ratios };
      const rl = v.riskLimits;
      const rlSent = pick(rl, { perUserOpenDegradedOrders: x.rlPerUser, hourlyDegradedOrders: x.rlHourly, degradeDurationAlertMinutes: x.rlAlert });
      const nm = pick(rl.normal, { maxMinutes: x.rlNormMin, maxAmount: x.rlNormAmt }), tg = pick(rl.tight, { maxMinutes: x.rlTightMin, maxAmount: x.rlTightAmt });
      if (Object.keys(nm).length) rlSent.normal = nm;
      if (Object.keys(tg).length) rlSent.tight = tg;
      if (Object.keys(rlSent).length) sent.riskLimits = rlSent;
      // 优惠券：按券编号比较，新增、修改、删除都落到生效时刻的券列表上
      let seq = Math.max(0, ...state.versions.flatMap(z => z.coupons.map(c => +String(c.templateId).replace(/\D/g, '') || 0)), ...rows.map(r => +String(r.dataset.tid).replace(/\D/g, '') || 0));
      const formCoupons = rows.map(r => {
        const k = r.dataset.k;
        return { templateId: r.dataset.tid || `CPT${String(++seq).padStart(2, '0')}`, name: x[`cp${k}_name`], amount: x[`cp${k}_amount`], audience: x[`cp${k}_audience`], validDays: x[`cp${k}_days`], issue: form.elements[`cp${k}_issue`].value, perUser: 1 };
      });
      const pre = new Map((v.coupons || []).map(c => [c.templateId, c])), now2 = new Map(formCoupons.map(c => [c.templateId, c]));
      const removed = [...pre.keys()].filter(id => !now2.has(id)), edited = formCoupons.filter(c => !pre.has(c.templateId) || !same(pre.get(c.templateId), c));
      if (removed.length || edited.length) {
        const list = (rb.coupons || []).filter(c => !removed.includes(c.templateId)).map(c => edited.find(e => e.templateId === c.templateId) || c);
        edited.forEach(e => { if (!list.some(c => c.templateId === e.templateId)) list.push(e); });
        sent.coupons = list;
      }
      const preview = merge(rb, sent);
      const changes = diff(rb, preview, state.brands);
      if (!changes.length) { ui.toast('没有任何改动', { type: 'warn', hint: `所有取值与 ${rb.versionId} 相同，修改后再提交` }); return; }
      const later = state.versions.filter(z => z.status === '待生效' && z.effectiveAt > (eff || now));
      const r = await ui.confirm({
        title: eff ? `确认在 ${ui.datetime(eff)} 生效` : '确认立即生效', confirmText: eff ? '确认保存' : '确认并立即生效',
        message: `<div>以下 ${changes.length} 项相对 ${esc(rb.versionId)} 有变化：</div>${diffTable(changes)}${later.length ? `<div class="g-pending">${esc(later.map(z => z.versionId).join('、'))} 生效时间更晚，取值不随本次修改变化。</div>` : ''}`,
        impact: eff ? `${ui.datetime(eff)} 之后开锁的订单按新规则计价，此前的订单不变` : '保存后用户端扫码页立即按新规则显示；此前已开锁的订单仍按旧规则计价',
        reasons: ['按主管部门通知调整', '节假日运营调整', '纠正此前录入的取值'],
        onConfirm: async reason => { try { await A.pricing.create(sent, reason); } catch (e) { throw new Error(failText(e) + (e.errors ? '' : '')); } },
      });
      if (r) { dirty = false; ui.toast(eff ? `规则已保存，${ui.datetime(eff)} 起生效` : '规则已保存并立即生效', { type: 'ok', hint: '用户端扫码页将按新规则显示' }); showList(); }
    }
    syncEff();
    host.scrollIntoView?.({ block: 'start' });
  }

  life.on('pricing.changed', ev => { if (ev.remote && host.querySelector('#tbl')) showList(); });
  showList();
  return () => life.dispose();
}

export default { path: '#/gov/pricing', title: '价格与规则配置', menu: '价格与规则配置', mount };

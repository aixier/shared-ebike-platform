/* 接入状态总览（R-42）：各品牌接入状态、车辆数、近期开锁成功率；按线下结果设置运营商已退出 */
import { A, ui, esc, chip, kv, failText, lifecycle, loadInto, setHead, accessChip, ACCESS_KIND, pct } from './_common.js';

const ORDER = ['未接入', '联调中', '已接入', '暂停可租', '已退出'];
const HINT = { 未接入: '尚未提交接入', 联调中: '码待启用，暂不可骑', 已接入: '可租用', 暂停可租: '车辆暂不可租', 已退出: '车辆码已失效' };

/** 近期开锁成功率来自运行监控；当前账号无权限时返回 null */
async function unlockRates() {
  try {
    const m = await A.ops.monitor({ hours: 24, days: 7 });
    const out = {};
    m.brands.forEach(b => { if (b.latest && b.latest.unlockSuccessRate != null) out[b.brandId] = b.latest.unlockSuccessRate; });
    return out;
  } catch { return null; }
}
const rateText = (rates, id) => (rates === null ? '—' : rates[id] != null ? pct(rates[id], 1) : '—');
/** 接入总览自带运行智能体最近一次上报的开锁成功率；缺失时再取运行监控 */
const ratesFrom = (d, r) => { const out = { ...(r || {}) }; let any = !!r; d.brands.forEach(b => { if (b.unlockSuccessRate != null) { out[b.brandId] = b.unlockSuccessRate; any = true; } }); return any ? out : null; };

function mount(el, query, pctx) {
  const life = lifecycle(el);
  el.innerHTML = '';
  const host = ui.el('<div class="g-stack"></div>');
  el.append(host);
  let data = null, rates = null, table = null, active = '';

  async function init() {
    host.innerHTML = '';
    setHead(pctx, '各品牌的接入状态、车辆数与近期开锁成功率；运营商退出按线下处理结果在这里设置');
    const body = ui.el('<div class="g-stack"></div>');
    host.append(body);
    await loadInto(body, async () => { const [d, r] = await Promise.all([A.access.overview(), unlockRates()]); return { d, r }; }, ({ d, r }) => {
      data = d; rates = ratesFrom(d, r);
      const bs = data.brands;
      body.append(ui.el(`<div class="g-acc">${ORDER.map(s => { const x = data.summary.find(y => y.status === s) || { brandCount: 0, vehicleCount: 0 }; return `<button type="button" class="a-stat k-${ACCESS_KIND[s]} ${active === s ? 'is-on' : ''}" data-s="${s}"><div class="k">${s}</div><div class="v">${x.brandCount}<small>个品牌</small></div><div class="d">${ui.number(x.vehicleCount)} 辆车 · ${HINT[s]}</div></button>`; }).join('')}</div>`));
      body.querySelectorAll('[data-s]').forEach(b => b.addEventListener('click', () => { active = active === b.dataset.s ? '' : b.dataset.s; body.querySelectorAll('[data-s]').forEach(x => x.classList.toggle('is-on', x.dataset.s === active)); table.setRows(rows()); }));
      const rows = () => bs.filter(b => !active || b.accessStatus === active);
      const tbl = ui.el('<div id="tbl"></div>');
      body.append(tbl);
      table = ui.table(tbl, {
        rowKey: 'brandId', pageSize: 10, rows: rows(), onRowClick: openDetail,
        empty: { title: '这个状态下没有品牌', text: '再点一次上方的状态卡片可取消筛选' },
        columns: [
          { key: 'name', title: '品牌', sortable: true, render: r => `<b>${esc(r.name)}</b>` },
          { key: 'operatorName', title: '运营商', render: r => esc(r.operatorName) },
          { key: 'accessStatus', title: '接入状态', sortable: true, render: r => accessChip(r.accessStatus), width: '100px' },
          { key: 'statusSince', title: '状态起始', sortable: true, render: r => `<span class="num">${ui.datetime(r.statusSince)}</span>` },
          { key: 'vehicleCount', title: '车辆数', align: 'right', sortable: true, render: r => ui.number(r.vehicleCount) },
          { key: 'rentable', title: '当前可租', align: 'right', render: r => ui.number(r.rentable) },
          { key: 'rate', title: '近期开锁成功率', align: 'right', render: r => `<span title="${rates === null ? '开锁成功率由运行监控提供，当前账号未开通查看' : ''}">${rateText(rates, r.brandId)}</span>` },
        ],
      });
      if (rates === null) body.append(ui.el('<div class="g-note">开锁成功率来自运维岗位的运行监控，当前账号未开通查看，显示为「—」。</div>'));
    });
  }

  function openDetail(b) {
    const op = b.operator, sibling = data.brands.filter(x => x.operatorId === b.operatorId);
    const codes = b.byCodeStatus || {}, st = b.byStatus || {};
    const exited = op.status === '已退出' || b.accessStatus === '已退出';
    const hist = [...(b.statusHistory || [])].reverse();
    const dr = ui.drawer({
      title: `${b.name} · ${op.shortName || b.operatorName}`, sub: b.displayName || '', wide: true,
      body: `${b.accessStatus === '暂停可租' && b.pause ? `<div class="a-alert is-info">自 ${ui.datetime(b.pause.pausedAt)} 起暂停可租（${esc(b.pause.metric || '运行指标')}低于门槛）；恢复由平台公司运维处理，用户扫码时推荐其他品牌的车。</div>` : ''}
        ${exited ? '<div class="a-alert is-err">该运营商已退出：车辆码已失效，凭证已停用，运营商账号为只读。</div>' : ''}
        ${kv([['接入状态', accessChip(b.accessStatus)], ['状态起始', ui.datetime(b.statusSince)], ['运营商', `${esc(op.name || op.shortName)}${op.accessVia === '车联网服务商代接' ? '（车联网服务商代接）' : ''}`], ['车辆数', `${ui.number(b.vehicleCount)} 辆，当前可租 ${ui.number(b.rentable)} 辆`],
          ['政府码', `已启用 ${ui.number(codes['已启用'] || 0)} · 待启用 ${ui.number(codes['待启用'] || 0)} · 已失效 ${ui.number(codes['已失效'] || 0)}`], ['车辆状态', Object.entries(st).map(([k, v]) => `${esc(k)} ${ui.number(v)}`).join(' · ') || '—'],
          ['近期开锁成功率', rateText(rates, b.brandId)], ['现行服务费比例', b.currentServiceFeeRatio != null ? pct(b.currentServiceFeeRatio) : '—'],
          ['联系人', (op.contacts || []).map(c => `${esc(c.name)}（${esc(c.role)}）${esc(c.phone)}`).join('<br>') || '—']])}
        <div><div class="a-sec-title">接入状态变更记录</div>${hist.length ? `<ul class="a-timeline">${hist.map(h => `<li><div>${accessChip(h.status)} ${esc(h.note || '')}</div><div class="t">${ui.datetime(h.at)}</div></li>`).join('')}</ul>` : '<div class="a-muted">暂无记录</div>'}</div>`,
    });
    const foot = ui.el('<footer class="a-drawer-foot"></footer>');
    const btn = ui.el('<button type="button" class="a-btn a-btn-danger">设为已退出</button>');
    if (exited) ui.setDisabled(btn, `${op.shortName || b.operatorName}已是退出状态`, '车辆码已失效，如需重新接入请联系平台公司运维');
    btn.addEventListener('click', () => { if (btn.getAttribute('aria-disabled') === 'true') return; exitOperator(b, sibling, dr); });
    foot.append(btn);
    dr.el.append(foot);
  }

  async function exitOperator(b, sibling, dr) {
    const op = b.operator, vehicles = sibling.reduce((n, x) => n + x.vehicleCount, 0);
    const r = await ui.confirm({
      title: `将 ${op.shortName || b.operatorName} 设为已退出`, danger: true, confirmText: '确认设为已退出',
      message: `退出按<b>运营商</b>设置，${esc(op.shortName || b.operatorName)}名下 ${sibling.length} 个品牌（${esc(sibling.map(x => x.name).join('、'))}）将一并退出。<br>此操作在本系统内不能撤回，请确认线下处理结果已经明确。`,
      impact: `${ui.number(vehicles)} 辆车的政府码立即失效、运营商凭证停用、其账号转为只读；骑行中的订单照常结束`,
      reasons: ['线下处置完成，运营商清退', '运营商主动申请退出', '许可到期未续'],
      onConfirm: async reason => { try { await A.access.setExit(b.operatorId, reason); } catch (e) { throw new Error(failText(e)); } },
    });
    if (r) { dr.close(); ui.toast(`${op.shortName || b.operatorName}已设为退出`, { type: 'ok', hint: '车辆码已失效，操作已写入留痕' }); init(); }
  }

  life.on('access.changed', ev => { if (ev.remote) init(); });
  init();
  return () => life.dispose();
}

export default { path: '#/gov/access', title: '接入状态总览', menu: '接入状态总览', mount };

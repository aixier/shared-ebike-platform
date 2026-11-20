/* 操作留痕：按人、时间、类型、对象编号查询，详情抽屉（R-47） */
import { api } from '../../../shared/api.js';
import * as ui from '../../../shared/ui.js';
import { kit } from '../../kit.js';
import { TARGET_TYPES, targetLabel, fieldLabel } from './labels.js';

const esc = ui.esc;
const KIND_CHIP = { 人: '', 智能体: 'brand', 系统: 'mute', 用户: 'mute' };

const fmtVal = v => {
  if (v == null) return '<span class="a-mute">—</span>';
  if (typeof v === 'boolean') return v ? '是' : '否';
  if (typeof v === 'number') return ui.number(v, Number.isInteger(v) ? 0 : 2);
  if (typeof v === 'string') return esc(v);
  if (Array.isArray(v) && v.length && Array.isArray(v[0])) return `${v.length} 个顶点`;
  const s = JSON.stringify(v);
  return `<span class="a-mono">${esc(s.length > 160 ? s.slice(0, 160) + '…' : s)}</span>`;
};

/** 变更前后：对象按字段并排；{字段:{from,to}} 形式逐项列出；其余按文字显示 */
function changeHtml(before, after) {
  const isObj = x => x && typeof x === 'object' && !Array.isArray(x);
  if (isObj(after) && Object.keys(after).length && Object.values(after).every(v => isObj(v) && ('from' in v || 'to' in v))) {
    return `<table class="a-chg"><thead><tr><th>项目</th><th>变更前</th><th>变更后</th></tr></thead><tbody>${Object.entries(after).map(([k, v]) => `<tr><td>${esc(fieldLabel(k))}</td><td>${fmtVal(v.from)}</td><td class="to">${fmtVal(v.to)}</td></tr>`).join('')}</tbody></table>`;
  }
  if (isObj(before) || isObj(after)) {
    const keys = [...new Set([...Object.keys(before || {}), ...Object.keys(after || {})])];
    return `<table class="a-chg"><thead><tr><th>项目</th><th>变更前</th><th>变更后</th></tr></thead><tbody>${keys.map(k => `<tr><td>${esc(fieldLabel(k))}</td><td>${fmtVal((before || {})[k])}</td><td class="to">${fmtVal((after || {})[k])}</td></tr>`).join('')}</tbody></table>`;
  }
  return `<table class="a-chg"><thead><tr><th>变更前</th><th>变更后</th></tr></thead><tbody><tr><td>${fmtVal(before)}</td><td class="to">${fmtVal(after)}</td></tr></tbody></table>`;
}

async function mount(el, query, ctx) {
  const dayStr = d => ui.date(d);
  const now = new Date((await api.system.time()).now.replace(' ', 'T'));
  const ago = n => dayStr(new Date(now.getTime() - n * 864e5));

  el.innerHTML = `<div class="a-lead-box">留痕只增不改，任何人都不能修改或删除记录。智能体与系统自动执行的动作单独标明，与人的操作区分开。</div>
    <div id="au-filter"></div><div id="au-quick" class="a-actions" style="margin:-4px 0 12px;flex-wrap:wrap"></div><div id="au-table"></div>`;

  const [actions, people] = await Promise.all([
    api.admin.audit.actions().catch(() => []),
    api.admin.audit.list({ pageSize: 500 }).then(r => {
      const m = new Map(); r.rows.forEach(x => m.set(x.accountId, { id: x.accountId, name: x.actorName, kind: x.actorKind }));
      return [...m.values()].sort((a, b) => (a.kind === '人' ? 0 : 1) - (b.kind === '人' ? 0 : 1) || a.name.localeCompare(b.name, 'zh-CN'));
    }).catch(() => []),
  ]);

  let list;
  const filter = kit.filterBar(ui.$('#au-filter', el), [
    { key: 'accountId', label: '操作人', type: 'select', width: '170px', options: people.map(p => ({ value: p.id, label: `${p.name}${p.kind !== '人' ? '（' + p.kind + '）' : ''}` })) },
    { key: 'actorKind', label: '操作方式', type: 'select', options: ['人', '智能体', '系统'] },
    { key: 'action', label: '操作类型', type: 'select', width: '150px', options: actions },
    { key: 'targetType', label: '对象类型', type: 'select', options: Object.entries(TARGET_TYPES).map(([value, label]) => ({ value, label })) },
    { key: 'from', label: '开始日期', type: 'date' },
    { key: 'to', label: '结束日期', type: 'date' },
    { key: 'q', label: '对象编号 / 关键字', type: 'text', grow: true, placeholder: '如 OD2026102613011、ST0001、事由中的字' },
  ], { onSearch: () => list && list.table.goto(1), initial: { q: query.targetId || query.q || '', accountId: query.accountId || '', action: query.action || '', targetType: query.targetType || '' } });

  const quick = ui.$('#au-quick', el);
  [['今天', 0], ['近 7 天', 6], ['近 30 天', 29]].forEach(([t, n]) => {
    const b = ui.el(`<button type="button" class="a-btn a-btn-text is-sm">${t}</button>`);
    b.addEventListener('click', () => { filter.set({ ...filter.values(), from: ago(n), to: dayStr(now) }, false); });
    quick.append(b);
  });

  const detail = async row => {
    const a = row;
    const who = `${esc(a.actorName)} ${a.actorKind !== '人' ? kit.chip(a.actorKind, KIND_CHIP[a.actorKind] || 'mute') : ''}`;
    const basis = a.actorKind === '智能体' ? '由运行、对账、接入或客服智能体按既定规则自动执行，规则触发的时间与依据见摘要与事由。'
      : a.actorKind === '系统' ? '由系统定时任务按规则执行（如到期自动解冻、超期押金抵扣）。' : a.actorKind === '用户' ? '由骑行用户本人在小程序内发起。' : '由本人登录管理后台后操作，事由为操作时填写。';
    return {
      title: a.action, sub: `${a.auditId} · ${ui.datetime(a.at, true)}`,
      body: `${kit.section('基本信息', kit.kv([
        ['操作时间', esc(ui.datetime(a.at, true))], ['操作人', who], ['操作类型', esc(a.action)],
        ['操作对象', `${esc(targetLabel(a.targetType))} <span class="a-mono">${esc(a.targetId)}</span>`], ['摘要', esc(a.summary)], ['事由', a.reason ? esc(a.reason) : '<span class="a-mute">本次操作没有填写事由</span>'],
      ]))}
        ${a.before != null || a.after != null ? kit.section('变更内容', changeHtml(a.before, a.after)) : ''}
        ${kit.section('记录依据', `<div class="a-alert is-info">${esc(basis)}</div>${kit.kv([['留痕编号', `<span class="a-mono">${esc(a.auditId)}</span>`], ['操作人编号', `<span class="a-mono">${esc(a.accountId)}</span>`]])}`)}`,
      footer: [{ label: '查看该对象的全部留痕', kind: 'default', onClick: h => { h.close(); filter.set({ q: a.targetId }, false); } }],
    };
  };

  list = kit.listDetail(ui.$('#au-table', el), {
    rowKey: 'auditId', pageSize: 20, titleOf: r => r.action, detail,
    empty: { title: '没有符合条件的留痕', text: '换个时间范围或操作类型试试，或点「重置」查看全部' },
    columns: [
      { key: 'at', title: '时间', width: '150px', render: r => `<span class="num">${esc(ui.datetime(r.at))}</span>` },
      { key: 'actor', title: '操作人', render: r => `${esc(r.actorName)}${r.actorKind !== '人' ? ' ' + kit.chip(r.actorKind, KIND_CHIP[r.actorKind] || 'mute') : ''}` },
      { key: 'action', title: '操作类型', render: r => kit.chip(r.action, 'brand') },
      { key: 'target', title: '对象', render: r => `${esc(targetLabel(r.targetType))} <span class="a-mono">${esc(r.targetId)}</span>` },
      { key: 'summary', title: '摘要', ellipsis: true, render: r => `<span title="${esc(r.summary)}">${esc(r.summary)}</span>` },
      { key: 'reason', title: '事由', ellipsis: true, render: r => r.reason ? esc(r.reason) : '<span class="a-mute">—</span>' },
    ],
    load: ({ page, pageSize }) => api.admin.audit.list({ ...filter.values(), page, pageSize }),
  });
  const off = api.on('audit.created', () => list.reload());
  return () => { off(); list.close(); };
}

export default { mount };

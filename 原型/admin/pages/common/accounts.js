/* 账号与授权（平台公司运维账号管理员）：账号列表、授权岗位与运营商范围、停用与启用，写操作全部进留痕（R-46、R-47） */
import { api } from '../../../shared/api.js';
import * as ui from '../../../shared/ui.js';
import { kit } from '../../kit.js';
import { permLabel, ROLES, ROLE_NOTE } from './labels.js';

const esc = ui.esc;
const isPerson = a => !!a.username;
const statusChip = a => !isPerson(a) ? kit.chip('自动运行', 'mute') : a.status === '正常' ? kit.chip('正常', 'ok') : a.status === '停用' ? kit.chip('已停用', 'err') : kit.chip('只读', 'warn');

async function mount(el, query, ctx) {
  const selfId = ctx.account.accountId;
  let all = [], operators = [];
  const loadAll = async () => { all = (await api.admin.accounts.list({ pageSize: 500 })).rows; };
  const loadOps = async () => {
    try { const o = await api.admin.access.overview(); const m = new Map(); o.brands.forEach(b => b.operator && m.set(b.operator.operatorId, b.operator)); operators = [...m.values()].sort((x, y) => x.operatorId.localeCompare(y.operatorId)); } catch { operators = []; }
  };
  const opName = id => { const o = operators.find(x => x.operatorId === id); return o ? (o.name || o.shortName) : id; };
  const scopeOf = a => a.operatorScope ? esc(opName(a.operatorScope)) : isPerson(a) ? '<span class="a-mute">全平台</span>' : '<span class="a-mute">—</span>';
  const permsOfRole = role => (all.find(x => x.role === role && isPerson(x)) || { permissions: [], menus: [] });

  await Promise.all([loadAll(), loadOps()]);
  el.innerHTML = `<div class="a-stats" id="ac-stats"></div><div id="ac-filter"></div><div id="ac-table"></div>`;

  const paintStats = () => {
    const p = all.filter(isPerson);
    ui.$('#ac-stats', el).innerHTML = [['人员账号', p.length, ''], ['正常使用', p.filter(a => a.status === '正常').length, ''], ['已停用', p.filter(a => a.status === '停用').length, ''], ['运营商账号', p.filter(a => a.operatorScope).length, '只读 ' + p.filter(a => /^只读/.test(a.status)).length + ' 个']]
      .map(([k, v, d]) => `<div class="a-stat"><div class="k">${k}</div><div class="v num">${v}</div>${d ? `<div class="d">${d}</div>` : ''}</div>`).join('');
  };

  const toggleBlock = a => {
    if (!isPerson(a)) return { why: '智能体与系统账号由平台自动管理，不能手工停用', next: '这类账号的动作可在「操作留痕」里查看' };
    if (a.accountId === selfId) return { why: '不能停用当前正在使用的账号', next: '请换另一位运维账号管理员来操作' };
    if (/^只读/.test(a.status)) return { why: '该账号因运营商退出已转为只读，不能单独启用', next: '如需恢复，请由主管部门先处理该运营商的退出设置' };
    return null;
  };
  const toggle = async (a, handle) => {
    const to = a.status === '停用' ? '正常' : '停用', verb = to === '停用' ? '停用' : '启用';
    const r = await kit.dangerAction({
      title: `${verb}账号「${a.name}」`, danger: to === '停用', confirmText: `确认${verb}`,
      message: to === '停用' ? `停用后 <b>${esc(a.username)}</b> 无法登录，正在使用的会话会在下一次操作时退出。` : `启用后 <b>${esc(a.username)}</b> 可以重新登录，权限与停用前一致。`,
      impact: `${a.role}${a.operatorScope ? ' · ' + opName(a.operatorScope) : ''} · ${a.org || ''}`,
      reasons: to === '停用' ? ['人员离职', '岗位调整', '账号长期未使用', '疑似账号泄露'] : ['人员复岗', '停用有误', '岗位恢复'],
      run: reason => api.admin.accounts.setStatus(a.accountId, to, reason), success: `账号已${verb}`,
    });
    if (r) { await loadAll(); paintStats(); applyFilter(); handle && handle.refresh(); }
    return r;
  };

  const filter = kit.filterBar(ui.$('#ac-filter', el), [
    { key: 'kind', label: '类型', type: 'select', options: ['人员', '智能体与系统'] },
    { key: 'role', label: '岗位', type: 'select', width: '150px', options: ROLES },
    { key: 'status', label: '状态', type: 'select', options: [{ value: '正常', label: '正常' }, { value: '停用', label: '已停用' }, { value: '只读', label: '只读' }] },
    { key: 'q', label: '姓名 / 账号 / 单位', type: 'text', grow: true, placeholder: '如 财务、yw.hao、青禾' },
  ], { onSearch: () => applyFilter(), initial: { kind: '人员' } });
  function applyFilter() {
    const f = filter.values(), q = (f.q || '').toLowerCase();
    list.table.setRows(all.filter(a => (!f.kind || (f.kind === '人员') === isPerson(a)) && (!f.role || a.role === f.role) && (!f.status || (f.status === '只读' ? /^只读/.test(a.status) : a.status === f.status)) && (!q || [a.name, a.username, a.org, a.accountId].some(s => s && String(s).toLowerCase().includes(q)))));
  }

  const detail = async (row, handle) => {
    const a = all.find(x => x.accountId === row.accountId) || row;
    const tpl = isPerson(a) ? a : null;
    const perms = a.permissions || [];
    const blk = toggleBlock(a);
    const toTxt = a.status === '停用' ? '启用账号' : '停用账号';
    return {
      title: a.name, sub: `${a.accountId} · ${a.role}`,
      body: `${kit.section('账号信息', kit.kv([
        ['登录账号', a.username ? `<span class="a-mono">${esc(a.username)}</span>` : null], ['状态', statusChip(a)], ['岗位', esc(a.role)], ['所属单位', esc(a.org)],
        ['数据范围', a.operatorScope ? `${esc(opName(a.operatorScope))}（只能查看和操作本运营商的数据）` : '平台全部数据'],
        ['最近登录', a.lastLoginAt ? esc(ui.datetime(a.lastLoginAt)) : '<span class="a-mute">从未登录</span>'], ['开通时间', esc(ui.datetime(a.createdAt))], ['开通人', a.createdBy ? esc(a.createdBy) : null],
      ]))}
        ${kit.section(`授权内容（${perms.length} 项）`, perms.length ? `<ul class="a-perm-list">${perms.map(p => `<li><span>${esc(permLabel(p))}</span><code>${esc(p)}</code></li>`).join('')}</ul>` : '<div class="a-mute">没有功能权限</div>')}
        ${(a.menus || []).length ? kit.section('可使用的功能', `<div style="display:flex;gap:6px;flex-wrap:wrap">${a.menus.map(m => kit.chip(m, 'mute')).join('')}</div>`) : ''}
        ${tpl ? `<div class="a-alert is-info">岗位与数据范围在开通时确定，开通后不能直接修改；需要调整请停用本账号，再按新岗位重新开通。</div>
        <div><a href="#/common/audit?targetId=${esc(a.accountId)}">查看该账号的授权与状态变更留痕</a></div>` : ''}`,
      footer: isPerson(a) ? [blk ? { label: toTxt, kind: 'default', why: blk.why, next: blk.next } : { label: toTxt, kind: a.status === '停用' ? 'primary' : 'danger', onClick: h => toggle(a, h) }] : [],
    };
  };

  const openCreate = () => {
    const form = ui.el(`<form class="a-form" novalidate autocomplete="off">
      <div class="a-item"><label class="req" for="nc-name">姓名</label><input id="nc-name" name="name" class="a-input" placeholder="真实姓名，页面上会脱敏显示"></div>
      <div class="a-item"><label class="req" for="nc-user">登录账号</label><input id="nc-user" name="username" class="a-input a-mono" placeholder="小写字母、数字和点，如 cw.zhang"><span class="tip">3～30 位，开通后不能修改</span></div>
      <div class="a-item"><label class="req" for="nc-role">岗位</label><select id="nc-role" name="role" class="a-select"><option value="">请选择岗位</option>${ROLES.filter(r => r !== '执法人员').map(r => `<option>${esc(r)}</option>`).join('')}</select><span class="tip" id="nc-note"></span></div>
      <div class="a-item" id="nc-op-item" hidden><label class="req" for="nc-op">所属运营商</label><select id="nc-op" name="operatorScope" class="a-select"><option value="">请选择运营商</option>${operators.map(o => `<option value="${esc(o.operatorId)}">${esc(o.name || o.shortName)}</option>`).join('')}</select><span class="tip">运营商账号只能查看和操作所属运营商的数据</span></div>
      <div class="a-item"><label for="nc-org">所属单位</label><input id="nc-org" name="org" class="a-input" placeholder="不填则沿用该岗位的默认单位"></div>
      <div class="a-item" id="nc-perm" hidden><label>该岗位将获得的权限</label><div id="nc-perm-list" class="a-perm-list"></div></div>
      <div class="a-item"><label class="req" for="nc-reason">开通事由</label><textarea id="nc-reason" name="reason" class="a-textarea" placeholder="如：新入职财务人员，负责月度结算"></textarea></div>
      <div class="a-alert is-err" id="nc-err" role="alert" hidden></div></form>`);
    const role = form.elements.role;
    role.addEventListener('change', () => {
      const r = role.value, isOp = /运营商/.test(r), t = permsOfRole(r);
      ui.$('#nc-op-item', form).hidden = !isOp; ui.$('#nc-note', form).textContent = ROLE_NOTE[r] || '';
      ui.$('#nc-perm', form).hidden = !r; ui.$('#nc-perm-list', form).innerHTML = (t.permissions || []).map(p => `<li><span>${esc(permLabel(p))}</span></li>`).join('');
    });
    let busy = false;
    const d = ui.drawer({ title: '开通账号', sub: '账号开通后立即生效，并写入操作留痕', body: form, wide: false, footer: [
      { label: '取消', kind: 'default' },
      { label: '确认开通', kind: 'primary', keepOpen: true, async onClick(L, btn) {
        if (busy) return false;
        const r = ui.validate(form, {
          name: { label: '姓名', required: true, min: 2, max: 10 },
          username: { label: '登录账号', required: true, pattern: /^[a-z0-9.]{3,30}$/, message: '账号只能用小写字母、数字和点，3～30 位' },
          role: { label: '岗位', required: true, requiredMessage: '请选择岗位' },
          operatorScope: { label: '所属运营商', custom: (v, all2) => /运营商/.test(all2.role || '') && !v && '运营商岗位须选择所属运营商' },
          reason: { label: '开通事由', required: true, min: 4 },
        });
        if (!r.ok) return false;
        const err = ui.$('#nc-err', form); err.hidden = true; busy = true; const old = btn.innerHTML; btn.innerHTML = '<span class="a-spin"></span>开通中'; btn.setAttribute('aria-busy', 'true');
        try {
          const v = r.values;
          const acc = await api.admin.accounts.create({ username: v.username, name: v.name, role: v.role, org: v.org || (/运营商/.test(v.role) ? opName(v.operatorScope) : undefined), operatorScope: /运营商/.test(v.role) ? v.operatorScope : null }, v.reason);
          L.close('ok');
          ui.toast(`已开通账号 ${acc.username}`, { type: 'ok', hint: '初始密码请通过线下渠道告知本人，首次登录后请立即修改；已写入操作留痕' });
          await loadAll(); paintStats(); applyFilter();
        } catch (e) { err.hidden = false; err.textContent = kit.errText(e) + (e.code === 'INVALID' ? '' : '，没有开通任何账号'); btn.innerHTML = old; }
        finally { busy = false; btn.removeAttribute('aria-busy'); }
        return false;
      } },
    ] });
    return d;
  };

  const list = kit.listDetail(ui.$('#ac-table', el), {
    rowKey: 'accountId', pageSize: 20, titleOf: a => a.name, detail, rows: [],
    empty: { title: '没有符合条件的账号', text: '换个岗位或状态试试，或点「重置」' },
    columns: [
      { key: 'name', title: '姓名', render: a => `<b>${esc(a.name)}</b>` },
      { key: 'username', title: '登录账号', render: a => a.username ? `<span class="a-mono">${esc(a.username)}</span>` : '<span class="a-mute">—</span>' },
      { key: 'role', title: '岗位' },
      { key: 'org', title: '所属单位', ellipsis: true, render: a => esc(a.org || '—') },
      { key: 'scope', title: '数据范围', render: scopeOf },
      { key: 'status', title: '状态', render: statusChip },
      { key: 'lastLoginAt', title: '最近登录', render: a => a.lastLoginAt ? `<span class="num">${esc(ui.datetime(a.lastLoginAt))}</span>` : '<span class="a-mute">从未登录</span>' },
      { key: 'act', title: '', render: a => { const b = toggleBlock(a); if (!isPerson(a)) return ''; const t = a.status === '停用' ? '启用' : '停用'; return `<span class="a-row-act"><button type="button" class="a-btn a-btn-text is-sm" data-act="toggle"${b ? ` aria-disabled="true" data-why="${esc(b.why)}" data-next="${esc(b.next)}"` : ''}>${t}</button></span>`; } },
    ],
    onAction: (act, a) => act === 'toggle' && toggle(a),
  });

  ctx.setActions(ui.el('<button type="button" class="a-btn a-btn-primary">开通账号</button>'));
  ui.$('#acts').firstElementChild.addEventListener('click', openCreate);

  paintStats(); applyFilter();
  const off = api.on('accounts.changed', async () => { await loadAll(); paintStats(); applyFilter(); });
  return () => { off(); list.close(); };
}

export default { mount };

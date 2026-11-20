/* 我的账号：个人信息、权限一览、修改密码 */
import { api } from '../../../shared/api.js';
import * as ui from '../../../shared/ui.js';
import { kit } from '../../kit.js';
import { permLabel } from './labels.js';

const esc = ui.esc;

/** 修改密码：旧密码不对时接口抛 LOGIN_FAILED，留痕由接口写入 */
const changePassword = (account, oldPwd, newPwd) => api.admin.changePassword(oldPwd, newPwd);

function strength(p) {
  let s = 0;
  if (p.length >= 8) s++;
  if (/[a-z]/i.test(p) && /\d/.test(p)) s++;
  if (/[^a-z0-9]/i.test(p) && p.length >= 10) s++;
  return s;
}

async function mount(el, query, ctx) {
  const me = await api.admin.me();
  const a = me.account, op = me.operator;
  const perms = me.permissions || [];
  el.innerHTML = `<div class="a-grid2">
    <div style="display:grid;gap:16px">
      <section class="a-panel"><div class="a-panel-head"><h3>账号信息</h3>${kit.chip(a.status === '正常' ? '正常' : a.status, a.status === '正常' ? 'ok' : 'warn')}</div><div class="a-panel-body">${kit.kv([
        ['姓名', esc(a.name)], ['登录账号', `<span class="a-mono">${esc(a.username)}</span>`], ['岗位', esc(a.role)], ['所属单位', esc(a.org)],
        ['数据范围', op ? `${esc(op.name || op.shortName)}（只能查看和操作本运营商的数据）` : '平台全部数据'],
        ['最近登录', a.lastLoginAt ? esc(ui.datetime(a.lastLoginAt)) : null], ['开通时间', esc(ui.date(a.createdAt))],
      ])}</div></section>
      <section class="a-panel"><div class="a-panel-head"><h3>修改密码</h3></div><div class="a-panel-body">
        <form id="pw" class="a-form" novalidate autocomplete="off">
          <div class="a-item"><label class="req" for="pw-o">当前密码</label><input id="pw-o" name="old" type="password" class="a-input" autocomplete="current-password"></div>
          <div class="a-item"><label class="req" for="pw-n">新密码</label><input id="pw-n" name="next" type="password" class="a-input" autocomplete="new-password"><div class="a-strength" id="pw-s" data-s="0"><i></i><i></i><i></i></div><span class="tip" id="pw-tip">至少 8 位，同时包含字母和数字；越长越安全</span></div>
          <div class="a-item"><label class="req" for="pw-c">确认新密码</label><input id="pw-c" name="again" type="password" class="a-input" autocomplete="new-password"></div>
          <div class="a-alert is-err" id="pw-err" role="alert" hidden></div>
          <div class="a-actions"><button type="submit" class="a-btn a-btn-primary" id="pw-go">保存新密码</button></div>
        </form></div></section>
    </div>
    <section class="a-panel"><div class="a-panel-head"><h3>我的权限</h3><span class="a-mute">共 ${perms.length} 项</span></div><div class="a-panel-body">
      ${perms.length ? `<ul class="a-perm-list">${perms.map(p => `<li><span>${esc(permLabel(p))}</span><code>${esc(p)}</code></li>`).join('')}</ul>` : '<div class="a-mute">当前账号没有开通任何功能权限。</div>'}
      <div class="a-alert is-info" style="margin-top:16px">权限由平台公司运维账号管理员授权；需要增加或调整，请联系管理员，所有授权变更都会写入操作留痕。</div>
    </div></section></div>`;

  const form = ui.$('#pw', el), err = ui.$('#pw-err', el), s = ui.$('#pw-s', el), btn = ui.$('#pw-go', el);
  form.elements.next.addEventListener('input', e => { const v = strength(e.target.value); s.dataset.s = e.target.value ? String(Math.max(1, v)) : '0'; });
  let busy = false;
  form.addEventListener('submit', async e => {
    e.preventDefault(); if (busy) return;
    err.hidden = true;
    const r = ui.validate(form, {
      old: { label: '当前密码', required: true },
      next: { label: '新密码', required: true, custom: v => (v.length < 8 || !/[a-z]/i.test(v) || !/\d/.test(v)) ? '新密码至少 8 位，且同时包含字母和数字' : (v === form.elements.old.value ? '新密码不能与当前密码相同' : '') },
      again: { label: '确认新密码', required: true, custom: (v, all) => v !== all.next && '两次输入的新密码不一致' },
    });
    if (!r.ok) return;
    busy = true; btn.setAttribute('aria-busy', 'true'); btn.innerHTML = '<span class="a-spin"></span>保存中';
    try {
      await changePassword(a, r.values.old, r.values.next);
      form.reset(); s.dataset.s = '0';
      ui.toast('密码已修改', { type: 'ok', hint: '下次登录请使用新密码；本次修改已写入操作留痕' });
    } catch (x) {
      if (x.code === 'LOGIN_FAILED') { const it = form.elements.old.closest('.a-item'); it.classList.add('is-error'); it.insertAdjacentHTML('beforeend', '<span class="err-msg" role="alert">当前密码不正确，请重新输入</span>'); form.elements.old.focus(); form.elements.old.addEventListener('input', () => { it.classList.remove('is-error'); ui.$('.err-msg', it)?.remove(); }, { once: true }); }
      else { err.hidden = false; err.textContent = kit.errText(x); }
    } finally { busy = false; btn.removeAttribute('aria-busy'); btn.textContent = '保存新密码'; }
  });
}

export default { mount };

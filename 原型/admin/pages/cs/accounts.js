// 账户处理：冻结账户人工解冻、协助退押、处理注销申请；全部二次确认并写事由。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $, $$, el, loadCss, query, syncClock, chip, fmtDt, waitChip, watch, failToast, errText, myPerms, openOrder } from './lib.js';
import { openTicket } from './ticket.js';

const TABS = [
  { key: 'freeze', label: '冻结账户', cats: ['账户冻结'] },
  { key: 'deposit', label: '退押协助', cats: ['退押', '旧押金咨询'] },
  { key: 'cancel', label: '注销申请', cats: ['注销'] },
];

/** 单个用户的账户处理抽屉：三类办理在同一处，按账户事实给出建议 */
export function openAccount(userId, opts = {}) {
  const dr = ui.drawer({ title: `账户处理 ${userId}`, sub: '解冻、退押、注销都在这里办理', body: ui.skeleton('lines', 9), wide: true, footer: [] });
  const foot = $('.a-drawer-foot', dr.el);
  let perms = null;
  async function render() {
    foot.innerHTML = ''; foot.hidden = true;
    dr.setBody(ui.skeleton('lines', 9));
    let u;
    try { perms = perms || await myPerms(); u = await api.admin.cs.user(userId); }
    catch (e) { dr.setBody(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: render })); return; }
    const f = u.account.freeze;
    const frozen = u.account.status === '冻结', cancelled = u.account.status === '已注销';
    const body = el('<div style="display:contents"></div>');

    // ---- 解冻建议 ----
    let freezeAdvice = '';
    if (frozen) {
      const arrearsCase = /欠费/.test(f.reason);
      const ageCase = /未达法定驾驶年龄|周岁/.test(f.reason);
      freezeAdvice = arrearsCase
        ? `该冻结由欠费追缴触发，用户结清 <b>${ui.money(u.arrears)}</b> 后会<b>自动解冻</b>，不需要人工处理。建议引导用户在小程序「我的订单」一键补付；只有核实为特殊情形（例如已线下结清）时才人工解冻。`
        : ageCase
          ? '核验未通过的原因是<b>未达法定驾驶年龄</b>，这类账户成年后重新核验才能解冻。仅在核实证件有误、已重新核验通过等情形下，才人工解冻。'
          : '该冻结不属于自动解冻情形，核实用户情况无误后可人工解冻。';
    }
    const dep = u.deposit;
    const refundBlock = u.refundBlockers || [];
    const cancelBlock = u.cancelBlockers || [];
    const canRefundDeposit = dep.mode === '押金' && dep.status !== '退押中' && dep.status !== '已退' && !refundBlock.length;
    const canCancel = !cancelled && !cancelBlock.length;
    const blockerList = list => `<ul class="cs-steps">${list.map(b => `<li><span class="dot">!</span><span>${esc(b.message)}${b.orderId ? ` <button type="button" class="cs-link" data-order="${esc(b.orderId)}">${esc(b.orderId)}</button>` : ''}${b.next && b.next.label ? `<span class="cs-hint"> · 下一步：${esc(b.next.label)}</span>` : ''}</span></li>`).join('')}</ul>`;

    body.innerHTML = `
      <div class="cs-row">${chip(u.account.status)} ${chip(`实名${u.realName.status}`, u.realName.status === '已核验' ? 'ok' : u.realName.status === '未通过' ? 'err' : 'warn')}
        <span class="cs-muted">${esc(u.name || '未实名')} ${esc(u.phoneMasked)} · 欠费 ${ui.money(u.arrears)} · 订单 ${u.orderCount} 单</span></div>
      <div class="cs-sec"><h3>账户事实</h3><div class="cs-box"><div class="cs-kv">
        <div><div class="k">账户状态</div><div class="v">${esc(u.account.status)}${f ? `，${fmtDt(f.at)} 冻结` : ''}</div></div>
        <div><div class="k">押金</div><div class="v">${dep.mode === '押金' ? `押金 ${ui.money(dep.balance)}（${esc(dep.status)}）` : dep.mode === '免押' ? `${esc(dep.provider)}信用免押` : '未开通'}</div></div>
        ${f ? `<div class="full"><div class="k">冻结原因</div><div class="v">${esc(f.reason)}</div></div><div class="full"><div class="k">自动解冻条件</div><div class="v">${esc(f.unfreezeHow)}</div></div>` : ''}
        <div class="full"><div class="k">账户记录</div><div class="v">${u.account.history.length ? u.account.history.map(h => `${fmtDt(h.at)} ${esc(h.action)}${h.reason ? `（${esc(h.reason)}）` : ''}`).join('<br>') : '没有冻结或解冻记录'}</div></div>
      </div></div></div>

      <div class="cs-sec"><h3>解冻</h3>
        ${frozen ? `<div class="cs-agent"><div class="who">客服智能体建议</div><div class="say">${freezeAdvice}</div></div>` : `<div class="cs-box is-soft cs-hint">${cancelled ? '账户已注销。' : '账户状态正常，不需要解冻。'}</div>`}
        <div class="cs-act"><div class="what">人工解冻后，用户可立即继续骑行。</div><button type="button" class="a-btn a-btn-danger" id="ac-unfreeze">人工解冻</button></div></div>

      <div class="cs-sec"><h3>协助退押</h3>
        ${dep.mode === '押金' ? `<div class="cs-box">${refundBlock.length ? `<div class="cs-hint" style="margin-bottom:6px">先处理下面的事项，才能退押：</div>${blockerList(refundBlock)}` : dep.status === '退押中' ? `<div class="cs-hint">已在退押中，预计 ${fmtDt(dep.refund && dep.refund.expectedAt)} 到账${dep.refund && dep.refund.awaitingPayee ? '；原路退回失败，等用户填写收款账户，财务人工转账' : ''}。</div>` : `<div class="cs-hint">可退押金 ${ui.money(dep.balance)}，按缴纳时金额 ${ui.money(dep.amountPaid)} 的余额原路退回。申请后立即停止开新单，约 48 小时到账。</div>`}</div>`
          : `<div class="cs-box is-soft cs-hint">${dep.mode === '免押' ? '该用户是信用免押，没有押金需要退。' : '该用户没有在平台缴纳押金。若咨询的是原品牌押金，请告知由原品牌退还，并建议开通免押。'}</div>`}
        <div class="cs-act"><div class="what">代用户申请退押。</div><button type="button" class="a-btn a-btn-default" id="ac-refund">协助退押</button></div></div>

      <div class="cs-sec"><h3>处理注销</h3>
        ${cancelled ? '<div class="cs-box is-soft cs-hint">账户已注销。</div>' : `<div class="cs-box">${cancelBlock.length ? `<div class="cs-hint" style="margin-bottom:6px">先处理下面的事项，才能注销：</div>${blockerList(cancelBlock)}` : '<div class="cs-hint">没有欠费、骑行中订单或未退押金，可以注销。注销后账户不能恢复，骑行记录按规定保留。</div>'}</div>`}
        <div class="cs-act"><div class="what">代用户办理注销申请。</div><button type="button" class="a-btn a-btn-danger" id="ac-cancel">协助注销</button></div></div>`;
    dr.setBody(body);
    $$('[data-order]', body).forEach(b => b.addEventListener('click', () => openOrder(b.dataset.order)));

    // ---- 按钮状态 ----
    const bU = $('#ac-unfreeze', body), bR = $('#ac-refund', body), bC = $('#ac-cancel', body);
    if (!perms.has('account.unfreeze')) ui.setDisabled(bU, '你的账号没有人工解冻的权限', '请换客服岗位处理');
    else if (!frozen) ui.setDisabled(bU, cancelled ? '账户已注销' : '账户当前没有冻结', '不需要解冻');
    if (!perms.has('deposit.assist')) ui.setDisabled(bR, '你的账号没有协助退押的权限', '请换客服岗位处理');
    else if (dep.mode !== '押金') ui.setDisabled(bR, dep.mode === '免押' ? '该用户是信用免押，没有押金' : '该用户没有缴纳押金', '原品牌押金由原品牌退还');
    else if (dep.status === '退押中' || dep.status === '已退') ui.setDisabled(bR, dep.status === '退押中' ? '已经在退押中' : '押金已退', '在联系记录里告知用户预计到账时间');
    else if (refundBlock.length) ui.setDisabled(bR, refundBlock[0].message, '先处理上方列出的事项');
    if (!perms.has('account.cancel.assist')) ui.setDisabled(bC, '你的账号没有协助注销的权限', '请换客服岗位处理');
    else if (cancelled) ui.setDisabled(bC, '账户已注销', '不需要再办理');
    else if (cancelBlock.length) ui.setDisabled(bC, cancelBlock[0].message, '先处理上方列出的事项，再办理注销');

    const done = async msg => { ui.toast(msg[0], { type: 'ok', hint: msg[1] }); opts.onChange && opts.onChange(); await render(); };
    bU.addEventListener('click', async () => {
      const ans = await ui.confirm({
        title: '人工解冻账户', danger: true, confirmText: '确认解冻',
        message: `<p>用户 <b>${esc(u.name || u.userId)}</b>（${esc(u.phoneMasked)}）当前冻结原因：${esc(f ? f.reason : '')}。</p>${/欠费/.test(f ? f.reason : '') ? `<div class="a-alert">该用户仍欠费 ${ui.money(u.arrears)}，人工解冻后欠费记录仍保留。</div>` : ''}<div class="cs-script">用户将收到：<b>账户已解冻，可以继续骑行。</b></div>`,
        impact: `账户 ${u.userId} 恢复正常，用户端立即可以开锁`,
        reasons: ['核实用户已线下结清欠费', '证件信息核实无误，重新核验通过', '冻结有误，核实后恢复'],
        onConfirm: async reason => { await api.admin.cs.unfreeze(userId, reason); },
      });
      if (ans) await done(['账户已解冻', '用户端已同步，可继续骑行']);
    });
    bR.addEventListener('click', async () => {
      const ans = await ui.confirm({
        title: '协助用户退押', danger: true, confirmText: '确认申请退押',
        message: `<p>将按押金余额 <b>${ui.money(dep.balance)}</b> 原路退回，约 48 小时到账。</p><div class="a-alert">申请后立即停止开新单；之后想继续骑行，需要重新开通免押或缴押金。</div>`,
        impact: `用户 ${u.userId} 押金 ${ui.money(dep.balance)} 原路退回，同时停止开新单`,
        reasons: ['用户来电要求退押，已告知后果', '用户不再使用，协助退押'],
        onConfirm: async reason => { await api.admin.cs.assistRefundDeposit(userId, reason); },
      });
      if (ans) await done(['已申请退押', '用户端押金页显示预计到账时间']);
    });
    bC.addEventListener('click', async () => {
      let res;
      const ans = await ui.confirm({
        title: '协助注销账户', danger: true, confirmText: '确认注销',
        message: `<p>用户 <b>${esc(u.name || u.userId)}</b>（${esc(u.phoneMasked)}）的账户将注销，<b>不能恢复</b>。</p>`,
        impact: `账户 ${u.userId} 注销并退出登录`,
        reasons: ['用户本人来电申请注销，已核实身份', '用户在小程序提交注销申请，已核实无待办'],
        onConfirm: async reason => { res = await api.admin.cs.assistCancel(userId, reason); },
      });
      if (ans) await done(['账户已注销', '注销记录已写入操作留痕']);
      void res;
    });

    // ---- 结案（来自工单时） ----
    if (opts.ticketId) {
      const t = el('<button type="button" class="a-btn a-btn-default">结案工单</button>');
      t.addEventListener('click', async () => {
        const ans = await ui.confirm({
          title: '结案工单', confirmText: '确认结案', danger: false,
          message: `<p>办完后结案，用户端显示“已解决”。事由会作为最后一条答复发给用户。</p>`, impact: `工单 ${opts.ticketId} 结案`,
          reasons: ['已按用户要求办理完毕并告知', '已说明处理方式，用户无其他疑问'],
          onConfirm: async reason => { await api.admin.cs.tickets.close(opts.ticketId, reason); },
        });
        if (ans) { ui.toast('工单已结案', { type: 'ok', hint: '用户端同步显示已解决' }); opts.onChange && opts.onChange(); dr.close(); }
      });
      const c = el('<button type="button" class="a-btn a-btn-default">查看对话</button>');
      c.addEventListener('click', () => openTicket(opts.ticketId, { onChange: opts.onChange }));
      foot.append(c, t);
    }
    foot.hidden = !foot.children.length;
  }
  render();
  return dr;
}

export default {
  path: '#/cs/accounts', title: '账户处理', menu: '账户处理', perm: 'user.view', sub: '冻结账户人工解冻、协助退押、处理注销申请；每一步都二次确认并写事由',
  async mount(root, params, pctx) {
    loadCss();
    root.innerHTML = '';
    const q = query(params);
    const page = el('<div class="cs-page"></div>');
    root.append(page);
    await syncClock();
    let tab = 'freeze', table = null;
        const finder = el(`<div class="a-filter"><div class="f"><label for="ac-q">直接查找用户</label><input id="ac-q" class="a-input a-search" placeholder="手机号、手机尾号（4 位以上）或用户编号" autocomplete="off" inputmode="numeric"></div>
      <button type="button" class="a-btn a-btn-primary" id="ac-find">查找并办理</button><span class="cs-hint" id="ac-find-tip"></span></div>`);
    const tabsBox = el('<div></div>');
    const tbl = el('<div></div>');
    page.append(finder, tabsBox, tbl);

    async function counts() {
      try {
        const [r, fz] = await Promise.all([api.admin.cs.tickets.list({ status: ['待处理', '处理中', '待主管确认'], pageSize: 100 }), api.admin.cs.listFrozenAccounts({ pageSize: 1 })]);
        const c = {};
        TABS.forEach(t => { c[t.key] = r.rows.filter(x => t.cats.includes(x.category)).length; });
        c.freeze = fz.total;
        return c;
      } catch { return {}; }
    }
    async function drawTabs() {
      const c = await counts();
      ui.tabs(tabsBox, TABS.map(t => ({ key: t.key, label: t.label, count: c[t.key] })), tab, k => { tab = k; build(); });
    }
    let fzKind = '';
    const KIND = { 欠费: ['欠费追缴', 'warn', '补付后自动解冻'], 核验: ['实名核验未通过', 'err', '重新核验通过后解冻'], 其他: ['需人工处理', 'brand', '核实后人工解冻'] };
    function buildFrozen() {
      const bar = el('<div class="a-filter"><div class="f"><label for="fz-k">冻结原因</label><select id="fz-k" class="a-select"><option value="">全部</option><option value="其他">需人工处理</option><option value="欠费">欠费追缴</option><option value="核验">实名核验未通过</option></select></div><span class="cs-hint" id="fz-c"></span></div>');
      const box = el('<div></div>');
      tbl.innerHTML = ''; tbl.append(bar, box);
      $('#fz-k', bar).value = fzKind;
      $('#fz-k', bar).addEventListener('change', e => { fzKind = e.target.value; table.goto(1); });
      table = ui.table(box, {
        rowKey: 'userId', pageSize: 10,
        empty: { title: '没有冻结的账户', text: '欠费追缴、实名核验未通过等原因冻结的账户会出现在这里' },
        load: async ({ page: pg, pageSize }) => {
          const r = await api.admin.cs.listFrozenAccounts({ kind: fzKind || undefined, page: pg, pageSize });
          $('#fz-c', bar).textContent = `欠费追缴 ${r.counts.欠费} · 实名核验未通过 ${r.counts.核验} · 需人工处理 ${r.counts.其他}`;
          return r;
        },
        columns: [
          { key: 'user', title: '用户', render: r => `${esc(r.name || '未实名')} <span class="cs-muted">${esc(r.phoneMasked)}</span>` },
          { key: 'kind', title: '冻结原因', render: r => chip(KIND[r.kind][0], KIND[r.kind][1]) },
          { key: 'reason', title: '说明', ellipsis: true, render: r => `<span title="${esc(r.freeze.reason || '')}">${esc(r.freeze.reason || '—')}</span>` },
          { key: 'arrears', title: '欠费', align: 'right', render: r => (r.arrears ? `<span class="num">${ui.money(r.arrears)}</span>` : '<span class="cs-muted">—</span>') },
          { key: 'at', title: '冻结时间', render: r => `<span class="num">${fmtDt(r.freeze.at)}</span>` },
          { key: 'frozenDays', title: '已冻结', align: 'right', render: r => (r.frozenDays == null ? '—' : `<span class="num">${r.frozenDays} 天</span>`) },
          { key: 'how', title: '解冻方式', render: r => `<span class="cs-muted">${KIND[r.kind][2]}</span>` },
          { key: 'op', title: '', render: () => '<div class="a-row-act"><button type="button" class="a-btn a-btn-default is-sm" data-act="go">办理</button></div>' },
        ],
        onRowClick: r => openAccount(r.userId, { onChange: refresh }),
        onAction: (a, r) => openAccount(r.userId, { onChange: refresh }),
      });
    }
    function build() {
      if (tab === 'freeze') { buildFrozen(); return; }
      tbl.innerHTML = '';
      const def = TABS.find(t => t.key === tab);
      const emptyText = { deposit: '用户咨询退押或旧押金时会出现在这里', cancel: '用户申请注销时会出现在这里' }[tab];
      table = ui.table(tbl, {
        rowKey: 'ticketId', pageSize: 10,
        empty: { title: '没有待办的工单', text: emptyText },
        load: async ({ page: pg, pageSize }) => {
          const res = await api.admin.cs.tickets.list({ pageSize: 100 });
          const rows = res.rows.filter(t => def.cats.includes(t.category) && t.status !== '已解决')
            .concat(res.rows.filter(t => def.cats.includes(t.category) && t.status === '已解决').slice(0, 5));
          return { rows: rows.slice((pg - 1) * pageSize, pg * pageSize), total: rows.length };
        },
        columns: [
          { key: 'ticketId', title: '工单号', render: r => `<span class="cs-mono">${esc(r.ticketId)}</span>` },
          { key: 'category', title: '类型', render: r => esc(r.category) },
          { key: 'user', title: '用户', render: r => `${esc(r.userName || '—')} <span class="cs-muted">${esc(r.userPhoneMasked || '')}</span>` },
          { key: 'ask', title: '用户说', ellipsis: true, render: r => `<span title="${esc((r.messages[0] || {}).text || '')}">${esc((r.messages[0] || {}).text || '—')}</span>` },
          { key: 'status', title: '状态', render: r => chip(r.status) },
          { key: 'wait', title: '已等待 / 结案', align: 'right', render: r => (r.status === '已解决' ? fmtDt(r.closedAt) : waitChip(r.createdAt)) },
          { key: 'assigneeName', title: '处理人', render: r => esc(r.assigneeName || '未接手') },
          { key: 'op', title: '', render: r => `<div class="a-row-act"><button type="button" class="a-btn a-btn-${r.status === '已解决' ? 'default' : 'primary'} is-sm" data-act="go">${r.status === '已解决' ? '查看' : '办理'}</button></div>` },
        ],
        onRowClick: r => openAccount(r.userId, { ticketId: r.ticketId, onChange: refresh }),
        onAction: (a, r) => openAccount(r.userId, { ticketId: r.ticketId, onChange: refresh }),
      });
    }
    const refresh = () => { table && table.reload(); drawTabs(); };
    drawTabs(); build();

    const find = async () => {
      const input = $('#ac-q', finder), tip = $('#ac-find-tip', finder), v = input.value.trim();
      const item = input.closest('.f');
      tip.textContent = '';
      if (!v) { tip.textContent = '请输入手机号、手机尾号或用户编号'; tip.style.color = 'var(--err)'; input.focus(); return; }
      const b = $('#ac-find', finder); b.setAttribute('aria-busy', 'true'); b.innerHTML = '<span class="a-spin"></span>查找中';
      try {
        const r = await api.admin.cs.search(v);
        if (!r.users.length) { tip.style.color = 'var(--err)'; tip.textContent = '没有找到这位用户，请核对号码后重试'; return; }
        if (r.users.length > 1) { tip.style.color = 'var(--ink-3)'; tip.textContent = `找到 ${r.users.length} 位用户，已打开第一位（${r.users[0].phoneMasked}）；请输入更完整的手机号精确查找`; }
        openAccount(r.users[0].userId, { onChange: refresh });
      } catch (e) { tip.style.color = 'var(--err)'; tip.textContent = `${errText(e)}，请重试`; }
      finally { b.removeAttribute('aria-busy'); b.textContent = '查找并办理'; void item; }
    };
    $('#ac-find', finder).addEventListener('click', find);
    $('#ac-q', finder).addEventListener('keydown', e => { if (e.key === 'Enter') find(); });

    const stop = watch(page, ['ticket.changed', 'user.created', 'users.changed'], refresh);
    if (q.user) openAccount(q.user, { onChange: refresh });
    void failToast;
    return () => stop();
  },
  unmount() {},
};

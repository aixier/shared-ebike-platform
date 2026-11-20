// 用户与订单查询：按手机号、订单号、车号查用户与订单；详情抽屉含扣费明细、押金与欠费、冻结状态、联系记录。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $, el, loadCss, query, syncClock, chip, fmtDt, watch, errText, openOrder, openUser } from './lib.js';
import { openTicket } from './ticket.js';
import { openAccount } from './accounts.js';

function kindOf(s) {
  if (/^1\d{10}$/.test(s)) return '手机号';
  if (/^\d{4,10}$/.test(s)) return '手机尾号';
  if (/^OD\d+$/i.test(s)) return '订单号';
  if (/^U\d+$/i.test(s)) return '用户编号';
  if (/^[A-Za-z]{2}\d{6}$/.test(s)) return '车号';
  return '';
}

export default {
  path: '#/cs/lookup', title: '用户与订单查询', menu: '用户与订单查询', perm: 'user.view', sub: '按手机号、订单号、车号或用户编号查询；手机号显示脱敏，扣费明细以订单事实为准',
  async mount(root, params, pctx) {
    loadCss();
    root.innerHTML = '';
    const qy = query(params);
    const page = el('<div class="cs-page"></div>');
    root.append(page);
    await syncClock();
    const ctx = { openTicket: id => openTicket(id, {}), openAccount: id => openAccount(id, {}) };

        const bar = el(`<div class="a-filter"><div class="f"><label for="lk-q">手机号、订单号、车号或用户编号</label>
      <input id="lk-q" class="a-input a-search" placeholder="例如 13612097326、OD2026120111070、QH179800" autocomplete="off" style="min-width:360px"></div>
      <button type="button" class="a-btn a-btn-primary" id="lk-go">查询</button><button type="button" class="a-btn a-btn-default" id="lk-clear">清空</button>
      <span class="cs-hint" id="lk-tip">手机尾号至少 4 位</span></div>`);
    const result = el('<div class="cs-page"></div>');
    page.append(bar, result);
    const input = $('#lk-q', bar), tip = $('#lk-tip', bar);

    /* 没有查询时：最近联系客服的用户 */
    function drawRecent() {
      result.innerHTML = '';
      const head = el('<div class="cs-row"><b>最近联系客服的用户</b><span class="cs-muted">输入上方条件可查任意用户或订单</span></div>');
      const box = el('<div></div>');
      result.append(head, box);
      ui.table(box, {
        rowKey: 'ticketId', pageSize: 10,
        empty: { title: '最近没有用户联系客服', text: '可以直接输入手机号或订单号查询' },
        load: async ({ page: pg, pageSize }) => { const r = await api.admin.cs.tickets.list({ page: pg, pageSize }); return { rows: r.rows, total: r.total }; },
        columns: [
          { key: 'createdAt', title: '联系时间', render: r => fmtDt(r.createdAt) },
          { key: 'user', title: '用户', render: r => `${esc(r.userName || '—')} <span class="cs-muted">${esc(r.userPhoneMasked || '')}</span>` },
          { key: 'category', title: '问题', render: r => esc(r.category) },
          { key: 'orderId', title: '订单', render: r => (r.orderId ? `<span class="cs-mono">${esc(r.orderId)}</span>` : '<span class="cs-muted">—</span>') },
          { key: 'status', title: '状态', render: r => chip(r.status) },
        ],
        onRowClick: r => openUser(r.userId, { ...ctx, tab: 'tickets' }),
      });
    }

    async function run() {
      const v = input.value.trim();
      tip.style.color = '';
      if (!v) { tip.textContent = '请先输入手机号、订单号、车号或用户编号'; tip.style.color = 'var(--err)'; input.focus(); return; }
      const k = kindOf(v);
      tip.textContent = k ? `按${k}查询` : '按关键字查询';
      result.innerHTML = ui.skeleton('lines', 6);
      const go = $('#lk-go', bar); go.setAttribute('aria-busy', 'true'); go.innerHTML = '<span class="a-spin"></span>查询中';
      try {
        const r = await api.admin.cs.search(v);
        result.innerHTML = '';
        if (!r.users.length && !r.orders.length) {
          result.append(ui.empty({ icon: '🔍', title: '没有找到匹配的用户或订单', text: '请核对号码或编号；手机尾号至少 4 位，车号形如 QH179800' }));
          return;
        }
        const u = el('<div class="cs-sec"><h3>用户（<span id="n1"></span>）</h3><div></div></div>');
        $('#n1', u).textContent = r.users.length;
        ui.table($('div', u), {
          rowKey: 'userId', pageSize: 10, rows: r.users,
          columns: [
            { key: 'userId', title: '用户编号', render: x => `<span class="cs-mono">${esc(x.userId)}</span>` },
            { key: 'name', title: '姓名', render: x => esc(x.name || '未实名') },
            { key: 'phoneMasked', title: '手机号', render: x => esc(x.phoneMasked) },
            { key: 'account', title: '账户', render: x => chip(x.account.status) },
            { key: 'deposit', title: '押金', render: x => (x.deposit.mode === '押金' ? `押金 ${ui.money(x.deposit.balance)}` : x.deposit.mode === '免押' ? '信用免押' : '未开通') },
            { key: 'arrears', title: '欠费', align: 'right', sortable: true, render: x => (x.arrears > 0 ? `<b style="color:var(--err)">${ui.money(x.arrears)}</b>` : ui.money(0)) },
            { key: 'orderCount', title: '订单数', align: 'right', render: x => x.orderCount },
            { key: 'ticketCount', title: '联系记录', align: 'right', render: x => x.ticketCount },
          ],
          onRowClick: x => openUser(x.userId, ctx),
        });
        const o = el('<div class="cs-sec"><h3>订单（<span id="n2"></span>）</h3><div></div></div>');
        $('#n2', o).textContent = r.orders.length;
        ui.table($('div', o), {
          rowKey: 'orderId', pageSize: 10, rows: r.orders,
          empty: { title: '没有匹配的订单', text: '这次查询只匹配到了用户' },
          columns: [
            { key: 'orderId', title: '订单号', render: x => `<span class="cs-mono">${esc(x.orderId)}</span>` },
            { key: 'user', title: '用户', render: x => `${esc(x.userName || '—')} <span class="cs-muted">${esc(x.userPhoneMasked || '')}</span>` },
            { key: 'plateNo', title: '车号', render: x => `${esc(x.plateNo)} <span class="cs-muted">${esc(x.brandName)}</span>` },
            { key: 'requestAt', title: '开锁请求', sortable: true, render: x => fmtDt(x.requestAt) },
            { key: 'status', title: '状态', render: x => chip(x.statusText || x.status, x.status === '骑行中' ? 'brand' : undefined) },
            { key: 'payable', title: '应付', align: 'right', sortable: true, sortValue: x => (x.fee ? x.fee.payable : -1), render: x => (x.fee ? ui.money(x.fee.payable) : '—') },
            { key: 'payStatus', title: '付款', render: x => (x.payStatus ? chip(x.payStatus) : '<span class="cs-muted">—</span>') },
          ],
          onRowClick: x => openOrder(x.orderId, ctx),
        });
        result.append(...(r.users.length ? [u] : []), ...(r.orders.length ? [o] : []));
      } catch (e) {
        result.innerHTML = '';
        result.append(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: run }));
      } finally { go.removeAttribute('aria-busy'); go.textContent = '查询'; }
    }
    $('#lk-go', bar).addEventListener('click', run);
    input.addEventListener('keydown', e => { if (e.key === 'Enter') run(); });
    $('#lk-clear', bar).addEventListener('click', () => { input.value = ''; tip.textContent = '手机尾号至少 4 位'; tip.style.color = ''; drawRecent(); input.focus(); });

    drawRecent();
    if (qy.q) { input.value = qy.q; run(); }
    if (qy.order) openOrder(qy.order, ctx);
    if (qy.user) openUser(qy.user, ctx);
    const stop = watch(page, ['ticket.changed'], () => { if (!input.value.trim()) drawRecent(); });
    return () => stop();
  },
  unmount() {},
};

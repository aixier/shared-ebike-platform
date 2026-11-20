// 退款处理：待处理工单队列；客服智能体依订单事实提议，客服确认或修改后退回。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $, el, loadCss, query, syncClock, chip, fmtDt, waitChip, watch, myPerms, errText } from './lib.js';
import { openTicket } from './ticket.js';

const TABS = [
  { key: 'open', label: '待处理', status: ['待处理'] },
  { key: 'doing', label: '处理中', status: ['处理中'] },
  { key: 'sup', label: '待主管确认', status: ['待主管确认'] },
  { key: 'done', label: '已解决', status: ['已解决'] },
];
const CATS = ['费用异议', '调度费异议', '费用咨询', '开锁失败', '订单无法结束', '稍后扣费咨询', '月卡与优惠券', '开具发票'];

export default {
  path: '#/cs/refunds', title: '退款处理', menu: '退款处理', perm: 'order.view', sub: '客服智能体依订单事实提议退款，你确认、修改或驳回；超出授权额度的转客服主管',
  async mount(root, params, pctx) {
    loadCss();
    root.innerHTML = '';
    const q = query(params);
    const page = el('<div class="cs-page"></div>');
    root.append(page);
    await syncClock();
    const state = { tab: 'open', category: '', q: '', onlyProposal: false };
    let table, tabsApi;
    let perms = null;
    try { perms = await myPerms(); } catch { /* 取不到权限时按钮按无权限显示 */ }

        const tabsBox = el('<div></div>');
    const filter = el(`<div class="a-filter">
      <div class="f"><label for="rf-q">工单号、订单号或用户编号</label><input id="rf-q" class="a-input a-search" placeholder="例如 TK202612010032" autocomplete="off"></div>
      <div class="f"><label for="rf-c">问题类型</label><select id="rf-c" class="a-select"><option value="">全部类型</option>${CATS.map(c => `<option>${c}</option>`).join('')}</select></div>
      <div class="f"><label>&nbsp;</label><label style="display:flex;gap:6px;align-items:center;font-size:13px;color:var(--ink-2)"><input type="checkbox" id="rf-p" style="accent-color:var(--orange-500)"> 只看有退款提议的</label></div>
      <span class="grow"></span><button type="button" class="a-btn a-btn-default" id="rf-reset">重置</button><button type="button" class="a-btn a-btn-primary" id="rf-go">查询</button></div>`);
    const tbl = el('<div></div>');
    page.append(tabsBox, filter, tbl);

    async function counts() {
      try {
        const [a, b] = await Promise.all([
          api.admin.cs.tickets.list({ status: ['待处理', '处理中', '待主管确认'], pageSize: 100 }),
          api.admin.cs.tickets.list({ status: ['已解决'], pageSize: 1 }),
        ]);
        const c = { open: 0, doing: 0, sup: 0, done: b.total };
        a.rows.forEach(t => { c[t.status === '待处理' ? 'open' : t.status === '处理中' ? 'doing' : 'sup']++; });
        return c;
      } catch { return null; }
    }
    async function drawTabs() {
      const c = await counts();
      tabsApi = ui.tabs(tabsBox, TABS.map(t => ({ key: t.key, label: t.label, count: c ? c[t.key] : undefined })), state.tab, k => { state.tab = k; table.goto(1); });
    }

    function build() {
      table = ui.table(tbl, {
        rowKey: 'ticketId', pageSize: 10, pageSizes: [10, 20, 50],
        empty: { title: state.tab === 'done' ? '还没有已解决的工单' : '这个队列已清空', text: state.tab === 'done' ? '换个筛选条件试试' : '新的咨询进来后会出现在这里，也可以切到其他队列看看' },
        load: async ({ page: pg, pageSize }) => {
          const status = TABS.find(t => t.key === state.tab).status;
          const res = await api.admin.cs.tickets.list({ status, category: state.category || undefined, q: state.q || undefined, page: pg, pageSize: state.onlyProposal ? 100 : pageSize });
          if (state.onlyProposal) {
            const rows = res.rows.filter(t => t.refundProposal);
            return { rows, total: rows.length };
          }
          return { rows: res.rows, total: res.total };
        },
        columns: [
          { key: 'ticketId', title: '工单号', render: r => `<span class="cs-mono">${esc(r.ticketId)}</span>` },
          { key: 'category', title: '问题类型', render: r => esc(r.category) },
          { key: 'user', title: '用户', render: r => `${esc(r.userName || '—')} <span class="cs-muted">${esc(r.userPhoneMasked || '')}</span>` },
          { key: 'orderId', title: '订单', render: r => (r.orderId ? `<span class="cs-mono">${esc(r.orderId)}</span>` : '<span class="cs-muted">不涉及订单</span>') },
          { key: 'proposal', title: '智能体提议', align: 'right', render: r => (r.refundProposal ? `${ui.money(r.refundProposal.amount)} <span class="cs-muted">${esc(r.refundProposal.kind)}</span>` : '<span class="cs-muted">无退款提议</span>') },
          { key: 'status', title: '状态', render: r => chip(r.status) + (r.awaitingPayee ? ' <span class="chip err">已转财务</span>' : '') },
          { key: 'wait', title: '已等待', align: 'right', render: r => (r.status === '已解决' ? fmtDt(r.closedAt) : waitChip(r.createdAt)) },
          { key: 'assigneeName', title: '处理人', render: r => esc(r.assigneeName || '未接手') },
          { key: 'op', title: '', render: r => `<div class="a-row-act"><button type="button" class="a-btn a-btn-${r.status === '已解决' ? 'default' : 'primary'} is-sm" data-act="open">${r.status === '已解决' ? '查看' : '处理'}</button></div>` },
        ],
        onRowClick: r => open(r.ticketId),
        onAction: (act, r) => open(r.ticketId),
      });
    }
    function open(id) {
      openTicket(id, { onChange: () => { table.reload(); drawTabs(); } });
    }
    build();
    await drawTabs();

    const apply = () => { state.q = $('#rf-q', filter).value.trim(); state.category = $('#rf-c', filter).value; state.onlyProposal = $('#rf-p', filter).checked; table.goto(1); };
    $('#rf-go', filter).addEventListener('click', apply);
    $('#rf-q', filter).addEventListener('keydown', e => { if (e.key === 'Enter') apply(); });
    $('#rf-c', filter).addEventListener('change', apply);
    $('#rf-p', filter).addEventListener('change', apply);
    $('#rf-reset', filter).addEventListener('click', () => { $('#rf-q', filter).value = ''; $('#rf-c', filter).value = ''; $('#rf-p', filter).checked = false; apply(); });

    const stop = watch(page, ['ticket.changed', 'tickets.changed'], () => { table.reload(); drawTabs(); });
    if (q.ticket) open(q.ticket);
    void perms; void errText;
    return () => stop();
  },
  unmount() {},
};

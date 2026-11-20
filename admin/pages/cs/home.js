// 客服首页：待办与时效。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';
import { esc, $, $$, el, loadCss, syncClock, bizNow, toMs, chip, waitChip, minutesSince, watch, errText, goto, myPerms } from './lib.js';
import { openTicket } from './ticket.js';

export default {
  path: '#/cs/home', title: '客服首页', menu: '首页', perm: 'user.view', sub: '今天要办的事，和需要尽快回应的事',
  async mount(root) {
    loadCss();
    root.innerHTML = '';
    const page = el('<div class="cs-page"></div>');
    root.append(page);
    const body = el('<div class="cs-page"></div>');
    page.append(body);

    async function draw() {
      body.innerHTML = `<div class="a-stats">${Array.from({ length: 5 }, () => '<div class="a-stat"><span class="skel" style="height:14px;width:60%"></span><span class="skel" style="height:28px;width:40%;margin-top:8px"></span></div>').join('')}</div>${ui.skeleton('lines', 6)}`;
      try {
        await syncClock();
        const [me, open, done, riding] = await Promise.all([
          myPerms(),
          api.admin.cs.tickets.list({ status: ['待处理', '处理中', '待主管确认'], pageSize: 100 }),
          api.admin.cs.tickets.list({ status: ['已解决'], pageSize: 100 }),
          api.admin.orders.list({ status: '骑行中', pageSize: 100 }),
        ]);
        if (!body.isConnected) return;
        const t = open.rows;
        const today = bizNow().toDateString();
        const doneToday = done.rows.filter(x => x.closedAt && new Date(toMs(x.closedAt)).toDateString() === today).length;
        const cnt = { wait: t.filter(x => x.status === '待处理').length, doing: t.filter(x => x.status === '处理中').length, sup: t.filter(x => x.status === '待主管确认').length, fe: t.filter(x => x.category === '订单无法结束').length, payee: t.filter(x => x.awaitingPayee).length };
        const slow = t.filter(x => x.status !== '待主管确认' && minutesSince(x.createdAt) >= 120).length;
        const stat = (k, v, d, route, cls = '') => `<button type="button" class="a-stat" data-go="${route}"><div class="k">${k}</div><div class="v num">${v}</div><div class="d ${cls}">${d || '&nbsp;'}</div></button>`;
        body.innerHTML = `
          <div class="a-stats">
            ${stat('待处理工单', cnt.wait, cnt.wait ? '还没人接手' : '没有积压', '#/cs/refunds', cnt.wait ? 'down' : 'up')}
            ${stat('处理中', cnt.doing, `全部待办中 ${slow} 单已超过 2 小时`, '#/cs/refunds', slow ? 'down' : '')}
            ${stat('待主管确认的退款', cnt.sup, me.has('refund.confirm.overLimit') ? '需要你确认' : '已提交客服主管', '#/cs/refunds')}
            ${stat('待强制结束的订单', cnt.fe, `当前骑行中共 ${riding.total} 单`, '#/cs/force-end')}
            ${stat('今日已解决', doneToday, cnt.payee ? `${cnt.payee} 单退款失败，等用户填收款账户` : '', '#/cs/refunds')}
          </div>
          <div class="cs-grid">
            <div class="a-panel"><div class="a-panel-head"><h3>优先处理</h3><span class="cs-muted">按等待时间排序</span></div><div class="a-panel-body" id="home-list"></div></div>
            <div class="a-panel"><div class="a-panel-head"><h3>回应时效</h3></div><div class="a-panel-body">
              <ul class="cs-steps">
                <li><span class="dot ok">1</span><span>新工单先看客服智能体的提议与依据，再决定确认、修改或驳回</span></li>
                <li><span class="dot ok">2</span><span>等待超过 1 小时标黄、超过 2 小时标红，优先回应</span></li>
                <li><span class="dot ok">3</span><span>退款超过你的授权额度时提交客服主管，工单转“待主管确认”</span></li>
                <li><span class="dot ok">4</span><span>退款原路退不回时，工单转财务人工转账，用户会收到填写收款账户的提示</span></li>
              </ul></div></div>
          </div>`;
        const list = $('#home-list', body);
        const rows = [...t].sort((a, b) => minutesSince(b.createdAt) - minutesSince(a.createdAt)).slice(0, 8);
        list.innerHTML = rows.map(x => `<div class="cs-todo"><div><div class="t"><b>${esc(x.category)}</b> · ${esc(x.userName || '—')} <span class="cs-muted">${esc(x.ticketId)}</span></div><div class="s">${esc(((x.messages[0] || {}).text || '').slice(0, 40))}${x.refundProposal ? ` · 智能体提议 ${ui.money(x.refundProposal.amount)}` : ''}</div></div>
          <div class="cs-row">${chip(x.status)}${waitChip(x.createdAt)}<button type="button" class="a-btn a-btn-primary is-sm" data-t="${esc(x.ticketId)}">处理</button></div></div>`).join('');
        if (!rows.length) list.append(ui.empty({ icon: '✅', title: '今天没有待办', text: '新的咨询进来后会出现在这里' }));
        $$('[data-go]', body).forEach(b => b.addEventListener('click', () => goto(b.dataset.go)));
        $$('[data-t]', body).forEach(b => b.addEventListener('click', () => openTicket(b.dataset.t, { onChange: draw })));
      } catch (e) {
        body.innerHTML = '';
        body.append(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: draw }));
      }
    }
    draw();
    const stop = watch(page, ['ticket.changed', 'order.updated'], draw);
    return () => stop();
  },
  unmount() {},
};

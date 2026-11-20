// 客服页面共用：样式加载、业务时间、错误提示、监听、订单与用户详情抽屉。
import * as ui from '../../../shared/ui.js';
import { api } from '../../../shared/api.js';

export const { esc, $, $$, el } = ui;

export function loadCss() {
  if (document.getElementById('cs-css')) return;
  const l = document.createElement('link');
  l.id = 'cs-css'; l.rel = 'stylesheet'; l.href = new URL('./cs.css', import.meta.url).href;
  document.head.append(l);
}

/* ---------- 路由参数：外壳传入的 params 与哈希里的查询串合并 ---------- */
export function query(params) {
  const raw = location.hash.split('?')[1] || '';
  return { ...Object.fromEntries(new URLSearchParams(raw)), ...(params || {}) };
}

/* ---------- 业务时间（以接口层时钟为准，不用浏览器时间比较业务时间） ---------- */
let delta = 0;
export async function syncClock() {
  try {
    const t = await api.system.time();
    delta = new Date(t.now.replace(' ', 'T')).getTime() - Date.now();
  } catch { /* 时钟取不到时退回浏览器时间 */ }
}
export const bizNow = () => new Date(Date.now() + delta);
export const toMs = s => new Date(String(s).replace(' ', 'T')).getTime();
export const minutesSince = s => Math.max(0, Math.floor((bizNow().getTime() - toMs(s)) / 60000));
export function waitText(mins) {
  if (mins < 1) return '刚刚';
  if (mins < 60) return `${mins} 分钟`;
  const h = Math.floor(mins / 60), m = mins % 60;
  if (h < 24) return m ? `${h} 小时 ${m} 分钟` : `${h} 小时`;
  return `${Math.floor(h / 24)} 天 ${h % 24} 小时`;
}
export function waitChip(startAt, warnMin = 60, errMin = 120) {
  const m = minutesSince(startAt);
  const cls = m >= errMin ? 'is-err' : m >= warnMin ? 'is-warn' : '';
  return `<span class="cs-wait num ${cls}">${waitText(m)}</span>`;
}
export const localInput = s => (s ? String(s).slice(0, 16).replace(' ', 'T') : '');
export const nowInput = () => { const d = bizNow(), p = n => String(n).padStart(2, '0'); return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`; };
export const fromInput = v => (v ? `${v.replace('T', ' ')}:00` : '');

/* ---------- 提示 ---------- */
export function errText(e) { return (e && e.message) || '服务繁忙，请稍后再试'; }
export function failToast(e, hint = '没有产生任何变更，可以重试') {
  ui.toast(errText(e), { type: 'err', hint: e && e.code === 'FORBIDDEN' ? '请换有权限的岗位处理' : hint });
}

/* ---------- 标签 ---------- */
const CHIP = {
  待处理: 'warn', 处理中: 'brand', 待主管确认: 'warn', 已解决: 'ok',
  正常: 'ok', 冻结: 'err', 已注销: 'mute',
  骑行中: 'brand', 已结束: 'mute', 开锁失败: 'err', 待人工处理: 'warn',
  已支付: 'ok', 无需支付: 'mute', 待支付: 'warn', 欠费: 'err', 押金抵扣: 'warn', 欠费已补付: 'ok',
  成功: 'ok', 失败: 'err', 处理中_流水: 'warn',
};
export const chip = (text, kind) => `<span class="chip ${kind || CHIP[text] || 'mute'}">${esc(text)}</span>`;
export const dash = v => (v == null || v === '' ? '—' : esc(v));
export const fmtDt = v => (v ? ui.datetime(v) : '—');

/* ---------- 监听接口层事件，节点离开页面后自动取消 ---------- */
export function watch(root, names, fn) {
  let timer;
  const offs = names.map(n => api.on(n, () => {
    if (!root.isConnected) { offs.forEach(o => o()); return; }
    clearTimeout(timer); timer = setTimeout(fn, 400);
  }));
  return () => { clearTimeout(timer); offs.forEach(o => o()); };
}

/* ---------- 带骨架与重试的取数渲染 ---------- */
export async function fetchInto(box, loader, render, skel = ['lines', 4]) {
  box.innerHTML = ui.skeleton(skel[0], skel[1]);
  try {
    const data = await loader();
    if (!box.isConnected) return;
    box.innerHTML = '';
    await render(data, box);
  } catch (e) {
    if (!box.isConnected) return;
    box.innerHTML = '';
    box.append(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: () => fetchInto(box, loader, render, skel) }));
  }
}

export async function myPerms() {
  const me = await api.admin.me();
  return { me, has: p => me.permissions.includes(p), account: me.account };
}

export function pageHead({ title, sub, actions = [] }) {
  const h = el(`<div class="a-head"><div><h1>${esc(title)}</h1>${sub ? `<div class="sub">${esc(sub)}</div>` : ''}</div><div class="a-actions"></div></div>`);
  const box = $('.a-actions', h);
  actions.forEach(a => {
    const b = el(`<button type="button" class="a-btn a-btn-${a.kind || 'default'}">${esc(a.label)}</button>`);
    b.addEventListener('click', () => a.onClick(b));
    box.append(b);
  });
  return h;
}

export function goto(path) { location.hash = path; }

/* ---------- 订单明细（扣费、时间线、流水） ---------- */
export function billHtml(bill) {
  if (!bill) return '<div class="cs-hint">本单没有金额（开锁失败、骑行中或待结算），不产生扣费。</div>';
  const lines = bill.lines.map(l => `<tr class="${l.total ? 'total' : ''}"><td>${esc(l.label)}${l.note ? `<div class="cs-hint">${esc(l.note)}</div>` : ''}</td><td class="r ${l.amount < 0 ? 'neg' : ''}">${ui.money(l.amount)}</td></tr>`).join('');
  const notes = bill.notes && bill.notes.length ? `<ul class="cs-notes">${bill.notes.map(n => `<li>${esc(n)}</li>`).join('')}</ul>` : '';
  return `<table class="cs-lines"><tbody>${lines}</tbody></table>${notes}
    <div class="cs-hint" style="margin-top:6px">${esc(bill.headline)}${bill.refunded ? `，已退款 ${ui.money(bill.refunded)}` : ''}</div>`;
}

export function paymentsTable(pays) {
  if (!pays || !pays.length) return '<div class="cs-hint">没有相关资金流水。</div>';
  const rows = pays.map(p => `<tr><td class="cs-mono">${esc(p.paymentId)}</td><td>${esc(p.type)}</td><td>${esc(p.channel)}</td><td class="r">${ui.money(p.direction === '付' ? -p.amount : p.amount)}</td><td>${chip(p.status === '处理中' ? '处理中' : p.status, p.status === '成功' ? 'ok' : p.status === '失败' ? 'err' : 'warn')}${p.failReason ? `<div class="cs-hint">${esc(p.failReason)}</div>` : ''}</td><td>${fmtDt(p.createdAt)}</td></tr>`).join('');
  return `<div class="a-table-wrap"><div class="a-table-scroll"><table class="a-table"><thead><tr><th>流水号</th><th>类型</th><th>渠道</th><th class="r">金额</th><th>结果</th><th>时间</th></tr></thead><tbody>${rows}</tbody></table></div></div>`;
}

export function orderTimeline(o) {
  const t = [];
  t.push([o.requestAt, '用户点开锁，平台向运营商发出开锁请求']);
  if (o.unlockResult === '失败') t.push([o.unlockReportAt || o.requestAt, `开锁失败：${o.unlockFailReason || '运营商报告失败'}，未扣费`]);
  if (o.startAt) t.push([o.startAt, `开锁成功，开始计时${o.startStationName ? `（${o.startStationName}）` : ''}`]);
  if (o.lockReportAt) t.push([o.lockReportAt, '运营商报告锁车']);
  if (o.pileInAt) t.push([o.pileInAt, '桩识别车辆入桩']);
  if (o.endAt) t.push([o.endAt, `订单结束（${o.endMethod || '—'}）${o.inStationBasis ? `，在点依据：${o.inStationBasis}` : ''}${o.endStationName ? `，${o.endStationName}` : ''}`]);
  if (o.billPushedAt) t.push([o.billPushedAt, '账单推送给用户']);
  if (o.forceEnd) t.push([o.forceEnd.at, `客服强制结束并锁车，按 ${ui.datetime(o.forceEnd.billedEndAt)} 计费（事由：${o.forceEnd.reason}）`]);
  return `<ol class="a-timeline" style="list-style:none;margin:0 0 0 6px">${t.map(([a, b]) => `<li><div class="t">${ui.datetime(a, true)}</div>${esc(b)}</li>`).join('')}</ol>`;
}

/* ---------- 订单详情抽屉 ---------- */
export function openOrder(orderId, opts = {}) {
  const dr = ui.drawer({ title: `订单 ${orderId}`, sub: '订单事实与扣费明细', body: ui.skeleton('lines', 8), wide: true });
  const load = async () => {
    dr.setBody(ui.skeleton('lines', 8));
    try {
      const o = await api.admin.orders.get(orderId);
      const names = [o.userName, o.userPhoneMasked].filter(Boolean).join(' · ');
      const body = el('<div style="display:contents"></div>');
      body.innerHTML = `
        <div class="cs-row">${chip(o.status)} ${o.payStatus ? chip(o.payStatus) : ''} ${o.refundStatus && o.refundStatus !== '无' ? chip(o.refundStatus, o.refundStatus.includes('失败') ? 'err' : 'warn') : ''} ${o.degraded ? '<span class="chip warn">费用稍后收取</span>' : ''}</div>
        <div class="cs-sec"><h3>订单信息</h3><div class="cs-kv">
          <div><div class="k">用户</div><div class="v">${dash(names)} <button type="button" class="cs-link" data-user="${esc(o.userId)}">${esc(o.userId)}</button></div></div>
          <div><div class="k">车辆</div><div class="v">${esc(o.plateNo)} · ${esc(o.brandName)}（${esc(o.operatorName)}）</div></div>
          <div><div class="k">开锁时间</div><div class="v">${fmtDt(o.startAt || o.requestAt)}</div></div>
          <div><div class="k">结束时间</div><div class="v">${fmtDt(o.endAt)}</div></div>
          <div><div class="k">计费</div><div class="v">${o.billedMinutes != null ? `${o.billedMinutes} 分钟 · ${o.distanceKm != null ? ui.km(o.distanceKm) : '—'}` : '—'}</div></div>
          <div><div class="k">计价规则</div><div class="v cs-mono">${esc(o.pricingVersionId)}（按开锁时刻）</div></div>
          <div><div class="k">付款方式</div><div class="v">${dash(o.payMethod)}</div></div>
          <div><div class="k">渠道</div><div class="v">${esc(o.channel)}</div></div>
          ${o.anomalyNote ? `<div class="full"><div class="k">异常说明</div><div class="v">${esc(o.anomalyNote)}</div></div>` : ''}
        </div></div>
        <div class="cs-sec"><h3>扣费明细</h3><div class="cs-box">${billHtml(o.bill)}</div>
          ${o.fee ? `<div class="cs-kv"><div><div class="k">渠道已收</div><div class="v num">${ui.money(o.fee.channelPaid)}</div></div><div><div class="k">押金抵扣</div><div class="v num">${ui.money(o.fee.depositDeducted)}</div></div><div><div class="k">已退款</div><div class="v num">${ui.money(o.fee.refunded)}</div></div><div><div class="k">未付</div><div class="v num">${ui.money(o.fee.unpaid)}</div></div></div>` : ''}</div>
        <div class="cs-sec"><h3>过程</h3>${orderTimeline(o)}</div>
        <div class="cs-sec"><h3>资金流水</h3>${paymentsTable(o.payments)}</div>
        ${o.tickets && o.tickets.length ? `<div class="cs-sec"><h3>联系记录</h3>${o.tickets.map(t => `<div class="cs-todo"><div><div class="t">${esc(t.ticketId)} · ${esc(t.category)}</div><div class="s">${fmtDt(t.createdAt)}</div></div>${chip(t.status)}</div>`).join('')}</div>` : ''}`;
      dr.setBody(body);
      $$('[data-user]', body).forEach(b => b.addEventListener('click', () => { dr.close(); openUser(b.dataset.user, opts); }));
    } catch (e) {
      dr.setBody(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: load }));
    }
  };
  load();
  return dr;
}

/* ---------- 用户详情抽屉：概览、订单、押金与欠费、联系记录 ---------- */
export function openUser(userId, opts = {}) {
  const dr = ui.drawer({ title: `用户 ${userId}`, sub: '账户、订单、押金与联系记录', body: ui.skeleton('lines', 8), wide: true, footer: [] });
  const foot = $('.a-drawer-foot', dr.el);
  const load = async () => {
    dr.setBody(ui.skeleton('lines', 8));
    try {
      const u = await api.admin.cs.user(userId);
      const body = el('<div style="display:contents"></div>');
      const f = u.account.freeze;
      body.innerHTML = `
        <div class="cs-row">${chip(u.account.status)} ${chip(`实名${u.realName.status}`, u.realName.status === '已核验' ? 'ok' : u.realName.status === '未通过' ? 'err' : 'warn')} ${chip(u.deposit.mode === '押金' ? `押金 ${u.deposit.status}` : u.deposit.mode === '免押' ? '信用免押' : '未开通押金或免押', u.deposit.mode === '未开通' ? 'warn' : 'brand')} ${u.arrears > 0 ? chip(`欠费 ${ui.money(u.arrears)}`, 'err') : ''}</div>
        ${f ? `<div class="a-alert is-err"><span class="grow"><b>账户冻结：</b>${esc(f.reason)}（${fmtDt(f.at)}）<br>解冻方式：${esc(f.unfreezeHow)}</span></div>` : ''}
        <div id="cs-user-tabs"></div><div id="cs-user-pane"></div>`;
      dr.setBody(body);
      const pane = $('#cs-user-pane', body);
      const panes = {
        info: () => `<div class="cs-kv">
          <div><div class="k">姓名</div><div class="v">${dash(u.name)}</div></div>
          <div><div class="k">手机号</div><div class="v">${esc(u.phoneMasked)}</div></div>
          <div><div class="k">注册</div><div class="v">${esc(u.channel)} · ${fmtDt(u.registeredAt)}</div></div>
          <div><div class="k">实名</div><div class="v">${esc(u.realName.status)}${u.realName.idMasked ? ` · ${esc(u.realName.idMasked)}` : ''}${u.realName.method ? ` · ${esc(u.realName.method)}` : ''}</div></div>
          <div><div class="k">月卡</div><div class="v">${u.monthCard ? `${esc(u.monthCard.status)}，至 ${fmtDt(u.monthCard.endAt)}，每单抵 ${ui.money(u.monthCard.perOrderCap)}` : '没有生效的月卡'}</div></div>
          <div><div class="k">可用优惠券</div><div class="v">${u.usableCouponCount} 张</div></div>
          <div><div class="k">骑行中订单</div><div class="v">${u.ridingOrderId ? `<button type="button" class="cs-link" data-order="${esc(u.ridingOrderId)}">${esc(u.ridingOrderId)}</button>` : '无'}</div></div>
          <div><div class="k">待支付订单</div><div class="v">${u.unpaidOrderIds.length ? u.unpaidOrderIds.map(i => `<button type="button" class="cs-link" data-order="${esc(i)}">${esc(i)}</button>`).join('、') : '无'}</div></div>
          <div class="full"><div class="k">账户记录</div><div class="v">${u.account.history.length ? u.account.history.map(h => `${fmtDt(h.at)} ${esc(h.action)}${h.reason ? `（${esc(h.reason)}）` : ''}`).join('<br>') : '没有冻结或解冻记录'}</div></div>
        </div>`,
        orders: () => '<div id="cs-user-orders"></div>',
        money: () => {
          const d = u.deposit;
          const dep = d.mode === '押金'
            ? `<div class="cs-kv"><div><div class="k">模式</div><div class="v">缴押金（${esc(d.status)}）</div></div><div><div class="k">缴纳 / 余额</div><div class="v num">${ui.money(d.amountPaid)} / ${ui.money(d.balance)}</div></div><div><div class="k">缴纳时间</div><div class="v">${fmtDt(d.paidAt)}</div></div>${d.refund ? `<div><div class="k">退押</div><div class="v">${fmtDt(d.refund.appliedAt)} 申请，预计 ${fmtDt(d.refund.expectedAt)} 到账${d.refund.awaitingPayee ? '，原路退回失败，等待用户填写收款账户' : ''}</div></div>` : ''}</div>`
            : d.mode === '免押' ? `<div class="cs-kv"><div><div class="k">模式</div><div class="v">${esc(d.provider)}信用免押</div></div><div><div class="k">开通时间</div><div class="v">${fmtDt(d.creditOpenedAt)}</div></div></div>`
              : '<div class="cs-hint">还没有开通免押或缴纳押金，开锁前需要先开通。</div>';
          return `<div class="cs-sec"><h3>押金与欠费</h3><div class="cs-box">${dep}<div class="cs-kv" style="margin-top:8px"><div><div class="k">当前欠费</div><div class="v num">${ui.money(u.arrears)}</div></div></div></div></div>
            <div class="cs-sec"><h3>最近资金流水（最多 50 条）</h3>${paymentsTable(u.payments.slice(0, 20))}</div>`;
        },
        tickets: () => (u.tickets.length
          ? u.tickets.map(t => `<div class="cs-box" style="margin-bottom:8px"><div class="cs-row"><b>${esc(t.category)}</b>${chip(t.status)}<span class="cs-muted">${esc(t.ticketId)} · ${fmtDt(t.createdAt)}${t.assigneeName ? ` · 处理人 ${esc(t.assigneeName)}` : ''}</span><button type="button" class="cs-link cs-right" data-ticket="${esc(t.ticketId)}">查看对话</button></div>
              <div class="cs-hint" style="margin-top:4px">${esc((t.messages[0] && t.messages[0].text) || '')}</div></div>`).join('')
          : '<div class="cs-hint">这位用户还没有联系过客服。</div>'),
      };
      const show = k => {
        pane.innerHTML = panes[k]();
        $$('[data-order]', pane).forEach(b => b.addEventListener('click', () => openOrder(b.dataset.order, opts)));
        $$('[data-ticket]', pane).forEach(b => b.addEventListener('click', () => opts.openTicket && opts.openTicket(b.dataset.ticket)));
        if (k === 'orders') {
          ui.table($('#cs-user-orders', pane), {
            rowKey: 'orderId', pageSize: 10, rows: u.orders,
            empty: { title: '没有订单', text: '这位用户还没有骑行记录' },
            columns: [
              { key: 'orderId', title: '订单号', render: r => `<span class="cs-mono">${esc(r.orderId)}</span>` },
              { key: 'plateNo', title: '车号', render: r => esc(r.plateNo) },
              { key: 'requestAt', title: '开锁请求', sortable: true, render: r => fmtDt(r.requestAt) },
              { key: 'status', title: '状态', render: r => chip(r.statusText || r.status, CHIP[r.status]) },
              { key: 'fee', title: '应付', align: 'right', sortValue: r => (r.fee ? r.fee.payable : -1), sortable: true, render: r => (r.fee ? ui.money(r.fee.payable) : '—') },
            ],
            onRowClick: r => openOrder(r.orderId, opts),
          });
        }
      };
      ui.tabs($('#cs-user-tabs', body), [{ key: 'info', label: '概览' }, { key: 'orders', label: '订单', count: u.orderCount }, { key: 'money', label: '押金与欠费' }, { key: 'tickets', label: '联系记录', count: u.tickets.length }], opts.tab || 'info', show);
      show(opts.tab || 'info');
      foot.innerHTML = '';
      foot.hidden = !opts.openAccount;
      if (opts.openAccount) {
        const b = el('<button type="button" class="a-btn a-btn-primary">去账户处理</button>');
        b.addEventListener('click', () => { dr.close(); opts.openAccount(userId); });
        foot.append(b);
      }
    } catch (e) {
      dr.setBody(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: load }));
    }
  };
  load();
  return dr;
}

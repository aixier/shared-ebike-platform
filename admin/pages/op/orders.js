// 订单明细（R-24）：按日期、品牌、状态、降级单筛选的每日订单明细，下载 CSV，详情抽屉。
import { api, ui, esc, op, injectStyle, getMe, getBrands, can, dt, dts, loc, chip, chipRaw, setHead, btn, busy, fail, errText, kv, watch, saveFile, nowStr, lacks, go } from './common.js';
import { addDays } from './common.js';

const ORDER_STATUS = ['已结束', '骑行中', '开锁失败', '待人工处理'];

export default {
  path: '#/op/orders', title: '订单明细', menu: '订单明细', perm: 'order.view.own',
  async mount(el, query, pctx) {
    injectStyle();
    el.innerHTML = '';
    let me, brands, today;
    try { [me, brands, today] = await Promise.all([getMe(), getBrands(), nowStr()]); }
    catch (e) { el.append(ui.retry({ text: `${errText(e)}，页面没有加载出来`, onRetry: () => this.mount(el, query, pctx) })); return; }
    today = today.slice(0, 10);
    const yesterday = addDays(today, -1);
    const O = op().orders;
    const F = { from: yesterday, to: yesterday, brandId: '', status: '', degraded: '', q: '' };

    const canExport = can(me, 'order.export.own');
    const bDown = btn('下载 CSV', 'primary', b => download(b));
    if (!canExport) lacks(bDown, '当前账号没有下载订单明细的权限', '请联系账号管理员调整授权');
    setHead(el, pctx, '订单明细', '订单明细含降级单，不含用户身份信息；可与自己的车辆记录逐单核对', [bDown]);

    const stats = ui.el('<div class="a-stats" aria-live="polite"></div>');
    el.append(stats);
    const skelStats = () => { stats.innerHTML = [1, 2, 3, 4].map(() => '<div class="a-stat"><span class="skel" style="width:60%;height:12px"></span><span class="skel" style="width:40%;height:26px;margin-top:8px"></span></div>').join(''); };
    skelStats();
    const drawStats = s => {
      const range = F.from && F.to ? (F.from === F.to ? F.from : `${F.from} 至 ${F.to}`) : '全部日期';
      stats.innerHTML = [
        ['订单数', ui.number(s.count), '笔', `${esc(range)}，含开锁失败与骑行中`],
        ['已产生费用的订单', ui.number(s.finished), '笔', '已结束并出账'],
        ['订单应收合计', ui.money(s.receivableSum), '', '未扣月卡与优惠券的应收'],
        ['降级单', ui.number(s.degraded), '笔', '平台故障期间开出的订单'],
      ].map(([k, v, u, d]) => `<div class="a-stat"><div class="k">${k}</div><div class="v">${v}${u ? `<small>${u}</small>` : ''}</div><div class="d">${d}</div></div>`).join('');
    };

    const flt = ui.el(`<form class="a-filter" autocomplete="off" role="search">
      <div class="f"><label for="of">开始日期</label><input id="of" name="from" type="date" class="a-input" value="${F.from}" max="${today}"></div>
      <div class="f"><label for="ot">结束日期</label><input id="ot" name="to" type="date" class="a-input" value="${F.to}" max="${today}"></div>
      <div class="f"><label>快速选择</label><span class="a-actions" id="op-quick">
        <button type="button" class="a-btn a-btn-default" data-r="y">昨天</button><button type="button" class="a-btn a-btn-default" data-r="t">今天</button>
        <button type="button" class="a-btn a-btn-default" data-r="7">近 7 天</button><button type="button" class="a-btn a-btn-default" data-r="m">本月</button><button type="button" class="a-btn a-btn-default" data-r="all">全部日期</button></span></div>
      <div class="f"><label for="ob">品牌</label><select id="ob" name="brandId" class="a-select"><option value="">全部品牌</option>${brands.map(b => `<option value="${esc(b.brandId)}">${esc(b.name)}</option>`).join('')}</select></div>
      <div class="f"><label for="os">订单状态</label><select id="os" name="status" class="a-select"><option value="">全部状态</option>${ORDER_STATUS.map(s => `<option>${s}</option>`).join('')}</select></div>
      <div class="f"><label for="od">降级单</label><select id="od" name="degraded" class="a-select"><option value="">全部</option><option value="1">仅降级单</option><option value="0">不含降级单</option></select></div>
      <div class="f grow"><label for="oq">订单号或车号</label><input id="oq" name="q" class="a-input a-search" style="width:100%" placeholder="输入订单号或车号"></div></form>`);
    el.append(flt);
    const hint = ui.el('<div class="op-note" style="margin:-4px 0 12px" id="op-dl-hint"></div>');
    el.append(hint);
    const drawHint = () => { hint.textContent = '「下载 CSV」导出当前日期与品牌范围内的全部订单（含降级单与开锁失败单），不受状态、降级单、关键字筛选影响。'; };
    drawHint();

    const sync = () => { flt.elements.from.value = F.from; flt.elements.to.value = F.to; };
    const reload = () => { skelStats(); t.goto(1); };
    ui.$('#op-quick', flt).addEventListener('click', e => {
      const b = e.target.closest('[data-r]'); if (!b) return;
      const r = b.dataset.r;
      if (r === 'y') [F.from, F.to] = [yesterday, yesterday];
      else if (r === 't') [F.from, F.to] = [today, today];
      else if (r === '7') [F.from, F.to] = [addDays(today, -6), today];
      else if (r === 'm') [F.from, F.to] = [`${today.slice(0, 8)}01`, today];
      else [F.from, F.to] = ['', ''];
      sync(); reload();
    });
    let qt;
    flt.addEventListener('input', e => { if (e.target.name === 'q') { clearTimeout(qt); qt = setTimeout(() => { F.q = e.target.value.trim(); reload(); }, 300); } });
    flt.addEventListener('change', e => {
      const n = e.target.name; if (!n || n === 'q') return;
      F[n] = e.target.value;
      if ((n === 'from' || n === 'to') && F.from && F.to && F.from > F.to) { ui.toast('开始日期晚于结束日期，已自动对调', { type: 'warn' }); [F.from, F.to] = [F.to, F.from]; sync(); }
      reload();
    });

    const box = ui.el('<div></div>');
    el.append(box);
    const params = () => ({ from: F.from || undefined, to: F.to || undefined, brandId: F.brandId || undefined, status: F.status || undefined, degraded: F.degraded === '' ? undefined : F.degraded === '1', q: F.q || undefined });
    const t = ui.table(box, {
      rowKey: 'orderId', pageSize: 20,
      columns: [
        { key: 'orderId', title: '订单号', render: r => `<span class="op-mono">${esc(r.orderId)}</span>` },
        { key: 'plateNo', title: '车号', render: r => `<span class="op-strong">${esc(r.plateNo)}</span>` },
        { key: 'brandName', title: '品牌' },
        { key: 'status', title: '状态', render: r => `${chip(r.status, 'order')}${r.degraded ? ' ' + chipRaw('降级单', 'warn') : ''}${r.lateReport ? ' ' + chipRaw('补报', 'mute') : ''}` },
        { key: 'requestAt', title: '开锁请求', sortable: true, render: r => dt(r.requestAt) },
        { key: 'startAt', title: '计费起点', render: r => dt(r.startAt) },
        { key: 'endAt', title: '计费终点', render: r => dt(r.endAt) },
        { key: 'durationSec', title: '时长', align: 'right', sortable: true, render: r => (r.durationSec == null ? '—' : ui.duration(r.durationSec)) },
        { key: 'distanceKm', title: '里程', align: 'right', sortable: true, render: r => (r.distanceKm == null ? '—' : ui.km(r.distanceKm)) },
        { key: 'receivable', title: '订单应收', align: 'right', render: r => (r.receivable == null ? '—' : ui.money(r.receivable)) },
        { key: 'settleMonth', title: '结算月', render: r => esc(r.settleMonth || '—') },
      ],
      load: async ({ page, pageSize, sort }) => {
        const r = await O.list({ ...params(), page, pageSize, sort: sort || undefined });
        drawStats(r.summary);
        return r;
      },
      onRowClick: r => openDetail(r),
      empty: { title: '这个范围内没有订单', text: '换个日期或品牌看看，或选择「全部日期」', action: '查看全部日期', onAction: () => { [F.from, F.to] = ['', '']; sync(); reload(); } },
      errorText: '订单明细没有加载出来，请重试',
    });
    const off = watch(el, ['order.created', 'order.updated'], () => t.reload(), 1200);
    pctx && pctx.onUnmount && pctx.onUnmount(off);

    /* 下载 */
    async function download(b) {
      await busy(b, async () => {
        try {
          const r = await O.export({ from: F.from || undefined, to: F.to || undefined, brandId: F.brandId || undefined });
          if (!r.count) { ui.toast('这个范围内没有订单，没有生成文件', { type: 'warn', hint: '换个日期范围再下载' }); return; }
          saveFile(r.fileName, r.csv);
          ui.toast(`已下载 ${ui.number(r.count)} 笔订单`, { type: 'ok', hint: r.fileName });
        } catch (e) { fail(e, '下载失败'); }
      });
    }

    /* 详情抽屉 */
    async function openDetail(row) {
      const d = ui.drawer({ title: row.orderId, sub: `${row.plateNo} · ${row.brandName}`, body: ui.skeleton('lines', 8), wide: false });
      const draw = async () => {
        d.setBody(ui.skeleton('lines', 8));
        let o;
        try { o = await O.get(row.orderId); }
        catch (e) { d.setBody(ui.retry({ text: `${errText(e)}，没有产生任何变更`, onRetry: draw })); return; }
        const tl = [];
        tl.push([o.requestAt, '开锁请求', '平台向运营商发出开锁请求']);
        if (o.unlockResult === '失败' || o.status === '开锁失败') tl.push([null, '开锁失败', esc(o.unlockFailReason || '运营商报告开锁失败') + '，不计费']);
        else if (o.startAt) tl.push([o.startAt, o.unlockResult === '超时后成功' ? '开锁成功（超时后才报告）' : '开锁成功', o.unlockResult === '超时后成功' ? '超过报告时限，平台立即请求结束，本单 0 元' : '计费起点']);
        if (o.pileInAt) tl.push([o.pileInAt, '桩识别在位', '桩识别到车辆入位']);
        if (o.lockReportAt) tl.push([o.lockReportAt, '锁车报告', '运营商报告锁车']);
        if (o.endAt) tl.push([o.endAt, '计费终点', '取锁车报告与桩在位中较早的时刻']);
        if (o.reportReceivedAt) tl.push([o.reportReceivedAt, '平台收到报告', o.lateReport ? '补报：出单后才到达' : '']);
        tl.sort((a, b) => (a[0] && b[0] ? (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0) : 0));
        const finished = o.status === '已结束' && o.settlement && o.settlement.settlementId;
        const body = ui.el(`<div style="display:contents">
          <div class="op-row">${chip(o.status, 'order')}${o.degraded ? chipRaw('降级单', 'warn') : ''}${o.lateReport ? chipRaw('补报', 'mute') : ''}</div>
          <div><div class="a-sec-title">事件时间</div><ul class="a-timeline">${tl.map(([at, k, s]) => `<li><div class="t">${at ? dts(at) : '—'}</div><b>${esc(k)}</b>${s ? `<div class="op-note">${s}</div>` : ''}</li>`).join('')}</ul></div>
          <div><div class="a-sec-title">计量</div>${kv([['计费时长', o.durationSec == null ? '—' : `${ui.duration(o.durationSec)}（${ui.number(o.durationSec)} 秒）`], ['计费分钟', o.billedMinutes == null ? '—' : `${o.billedMinutes} 分钟`], ['里程', o.distanceKm == null ? '—' : ui.km(o.distanceKm)], ['结束方式', esc(o.endMethod || '—')]])}</div>
          <div><div class="a-sec-title">位置</div>${kv([['开锁位置', `<span class="op-mono">${loc(o.startLocation)}</span>`], ['锁车位置', `<span class="op-mono">${loc(o.endLocation)}</span>`], ['是否在点', o.inStation == null ? '—' : o.inStation ? `站点内${o.endStationName ? '（' + esc(o.endStationName) + '）' : ''}` : '站点外'], ['在点依据', esc(o.inStationBasis || '—')]])}</div>
          <div><div class="a-sec-title">费用与结算</div>${kv([['订单应收', o.receivable == null ? '—' : ui.money(o.receivable)], ['其中调度费', o.dispatchFee ? ui.money(o.dispatchFee) : '—'], ['价格版本', `<span class="op-mono">${esc(o.pricingVersionId || '—')}</span>`], ['结算月', esc(o.settleMonth || '尚未结算')], ['结算单', o.settlement ? `<span class="op-mono">${esc(o.settlement.settlementId)}</span>（${esc(o.settlement.kind)}）` : '—'], ...(o.settlementNote ? [['结算说明', esc(o.settlementNote)]] : []), ['异议记录', (o.disputeIds || []).length ? o.disputeIds.map(x => `<span class="op-mono">${esc(x)}</span>`).join('、') : '无']])}</div>
          <p class="op-note">订单明细不含用户身份信息。订单应收为扣除月卡与优惠券之前的金额，服务费按结算单规则计算。</p></div>`);
        d.setBody(body);
        if (finished && can(me, 'dispute.submit.own') && !me.readonly) {
          let f = ui.$('.a-drawer-foot', d.el);
          if (!f) { f = ui.el('<footer class="a-drawer-foot"></footer>'); d.el.append(f); }
          f.innerHTML = '';
          const b = btn('对这一单的时长或里程有异议', 'default', () => { sessionStorage.setItem('op.prefill', JSON.stringify({ settlementId: o.settlement.settlementId, orderId: o.orderId })); d.close(); go('#/op/settlements'); });
          f.append(b);
        }
      };
      draw();
    }
  },
};

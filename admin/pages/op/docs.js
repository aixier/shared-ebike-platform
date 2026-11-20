// 接入文档（R-36、R-37、M-01～M-04）：3 项交互加 1 份车辆清单，请求与回调的字段、签名与重放保护、时限、异常含义与参考报文。
import { api, ui, esc, op, injectStyle, getMe, loadInto, errText, copyText, setHead } from './common.js';

const json = o => JSON.stringify(o, null, 2);
function code(text) {
  return `<div class="op-code-wrap"><button type="button" class="a-btn a-btn-default is-sm" data-copy-code aria-label="复制报文">复制</button><pre class="op-code">${esc(text)}</pre></div>`;
}
function fields(rows) {
  return `<div style="overflow:auto"><table class="op-mini"><thead><tr><th>字段</th><th>类型</th><th>必填</th><th>说明</th></tr></thead><tbody>${rows.map(([k, t, req, d]) => `<tr><td class="op-mono">${esc(k)}</td><td>${esc(t)}</td><td>${req ? '是' : '否'}</td><td>${d}</td></tr>`).join('')}</tbody></table></div>`;
}
const pretty = s => { try { return json(JSON.parse(s)); } catch { return s; } };

/* 各节正文；节号与接入规范目录一致 */
const BODY = {
  '1': ctx => `
    <p>接入方式有两种：运营商自行接入；或由车联网服务商代接，服务商接入一次即可覆盖其名下多家运营商。政府自有车队与运营商按同一份规范、同一套验收接入，规范不为任何一家定制。</p>
    <ul class="op-list"><li>所有接入经公网，全程 HTTPS。</li><li>每家一套独立凭证，随请求出示；任何一家的凭证都可以随时停用或轮换，不影响其他运营商。</li><li>联调凭证在「联调与测试」页自助查看与轮换；正式凭证在验收通过、上线时发放。</li>${ctx.baseUrl ? `<li>联调环境地址：<span class="op-mono">${esc(ctx.baseUrl)}</span></li>` : ''}</ul>
    <h3>3 项交互加 1 份清单</h3>
    <div style="overflow:auto"><table class="op-mini"><thead><tr><th>编号</th><th>方向</th><th>交互</th><th>触发</th><th>要点</th></tr></thead><tbody>
      <tr><td><b>M-01</b></td><td>平台 → 运营商</td><td>开锁</td><td>用户点开锁</td><td>1 秒内同步回「已受理」或「拒绝与原因」；同一订单重复请求不得重复开锁</td></tr>
      <tr><td><b>M-02</b></td><td>平台 → 运营商</td><td>结束骑行</td><td>用户点还车、入桩未锁、超时后才报告开锁成功、客服强制结束</td><td>收到后锁车并回报锁车事件</td></tr>
      <tr><td><b>M-03</b></td><td>运营商 → 平台</td><td>骑行事件</td><td>开锁完成、锁车</td><td>开锁结果 30 秒内回报；平台未确认的事件须在补报窗口内补报</td></tr>
      <tr><td><b>M-04</b></td><td>运营商 → 平台</td><td>车辆清单</td><td>一次性提交，车辆增减时更新</td><td>品牌、车号、车型、桩识别用车辆标识；系统提交或页面上传均可</td></tr></tbody></table></div>
    <p>每日订单明细由运营商在「订单明细」页自行下载，不需要对账文件互换。降级期间不给运营商增加任何交互。</p>`,
  '2': () => `
    ${fields([
      ['brand', '字符串', true, '品牌编码'],
      ['plateNo', '字符串', true, '车号，2 位字母加 6 位数字，品牌内统一前缀；全部车辆内唯一'],
      ['model', '字符串', true, '车型'],
      ['pileTag', '字符串', true, '桩识别用车辆标识，24 位十六进制；桩据此识别车辆在位，全部车辆内唯一'],
    ])}
    <ul class="op-list"><li>提交方式：由运营商系统提交，或在「车辆清单」页上传 CSV；两种方式的字段与校验相同。</li><li>整批校验，全部正确才入库；有错误的行会逐行指出原因。</li><li>入库即发放政府码，一车一码。已接入品牌的码立即启用；联调中品牌的码为「待启用」，验收上线后统一启用。</li><li>车辆有增减时更新清单；从清单移除的车辆，政府码随即失效。运营商退出时，其全部车辆的码失效。</li><li>验收前，运营商须提交车辆清单并完成车上政府码的换贴。</li></ul>`,
  '3.1': () => `
    <h3>平台请求运营商</h3>
    ${fields([
      ['orderId', '字符串', true, '平台订单号'],
      ['plateNo', '字符串', true, '车号'],
      ['idempotencyKey', '字符串', true, '幂等键；同一订单重复请求时取值相同'],
      ['degraded', '布尔', true, '是否降级单；为 true 时按普通开锁执行，不增加任何交互'],
    ])}
    <h3>运营商同步响应</h3>
    <ul class="op-list"><li>1 秒内返回「已受理」，或「拒绝」并写明原因（电量不足、车辆故障、车辆被占用等）；3 秒内没有响应，平台按失败处理。</li><li>开锁结果不在这里返回，通过 M-03 的开锁结果事件报告。</li><li>同一订单号重复请求时，不得重复开锁，直接返回首次处理的结果。</li></ul>
    ${code(json({ orderId: 'OD2026113012345', accepted: true }))}
    ${code(json({ orderId: 'OD2026113012345', accepted: false, reason: '电量不足' }))}`,
  '3.2': () => `
    ${fields([
      ['orderId', '字符串', true, '平台订单号'],
      ['event', '字符串', true, '固定为 unlock'],
      ['result', '字符串', true, 'success 或 fail'],
      ['reason', '字符串', false, '失败时必填：电量不足、车辆故障、车辆被占用或其他原因'],
      ['at', '时间', true, '开锁成功的时刻（北京时间，精确到秒）；成功时它就是计费起点'],
    ])}
    <ul class="op-list"><li><b>时限：</b>从平台发出请求起 30 秒内报告开锁成功或失败，超过时限平台判定开锁失败，不向用户收费。</li><li>车辆确认较慢时，可先对 M-01 回「已受理」，确认后再报告结果，但仍须在 30 秒内。</li><li><b>超时后才报告成功：</b>平台立即发出 M-02 请求结束，本单按 0 元处理；请在收到后立即锁车并回报。</li><li>开锁成功的时刻是计费起点，请以车辆实际开锁时刻上报，不要用上报时刻代替。</li></ul>`,
  '3.3': () => `
    <h3>M-02 平台请求结束骑行</h3>
    <div style="overflow:auto"><table class="op-mini"><thead><tr><th>触发场景</th><th>说明</th></tr></thead><tbody>
      <tr><td>用户点还车</td><td>站内、站外都会发出；站外还车平台另按规则收调度费，与运营商无关</td></tr>
      <tr><td>车入桩未锁</td><td>桩识别到车辆在位，但没有收到锁车报告，平台请求锁车</td></tr>
      <tr><td>超时后才报告开锁成功</td><td>开锁结果晚于时限到达，平台请求结束，本单 0 元</td></tr>
      <tr><td>客服强制结束</td><td>请求中带结束事由</td></tr></tbody></table></div>
    ${fields([
      ['orderId', '字符串', true, '平台订单号'],
      ['plateNo', '字符串', true, '车号'],
      ['reason', '字符串', true, '触发场景'],
      ['note', '字符串', false, '强制结束时的事由'],
    ])}
    <h3>M-03 锁车事件</h3>
    <p>用户手动锁车、收到 M-02 后锁车，以及车辆自行锁止，都要报告锁车事件。</p>
    ${fields([
      ['orderId', '字符串', true, '平台订单号'],
      ['event', '字符串', true, '固定为 lock'],
      ['at', '时间', true, '锁车时刻'],
      ['lng、lat', '数字', true, '锁车位置，经纬度'],
      ['inStation', '布尔', true, '车端对是否在站点内的判定'],
      ['durationSec', '整数', true, '本次骑行时长，秒'],
      ['distanceM', '整数', true, '本次骑行里程，米'],
    ])}
    <ul class="op-list"><li>计费终点取「锁车报告」与「桩识别在位」两者中较早的时刻。</li><li>有桩时以桩的在位识别判断是否在点；桩与车端判定冲突时以桩为准；桩故障或站点无桩时，以车端判定为准。</li></ul>`,
  '3.4': () => `
    <ul class="op-list"><li>平台没有确认收到的事件（开锁结果、锁车事件），由运营商在本地保留。</li><li>补报窗口为 72 小时：在窗口内按事件发生的先后顺序补报，报文中的时间用事件实际发生的时间。</li><li>同一事件重复上报，平台只处理一次，不会重复计费。</li><li>出单前到达的补报计入当月结算明细；出单之后才到达的，计入下一期结算单的调整项，并写明事由。</li><li>骑行中的车不受平台故障影响，照常还车；故障恢复后补报，订单照常结清。</li></ul>`,
  '4': () => `
    <p>重复或延迟到达的请求不得重复执行。下表列出常见异常的含义与应对。</p>
    <div style="overflow:auto"><table class="op-mini"><thead><tr><th>情形</th><th>含义</th><th>平台处理</th><th>运营商应做</th></tr></thead><tbody>
      <tr><td>重复请求</td><td>网络重试或平台重发，订单号与幂等键相同</td><td>—</td><td>返回首次处理结果，不得重复开锁或重复结束</td></tr>
      <tr><td>延迟到达的请求</td><td>请求晚于用户的后续操作到达</td><td>—</td><td>已处理过的按首次结果返回，不得重复执行</td></tr>
      <tr><td>开锁结果超时</td><td>超过 30 秒仍没有开锁结果</td><td>判定开锁失败，不收费</td><td>尽快回报；车辆确认慢时先回「已受理」</td></tr>
      <tr><td>超时后才报告开锁成功</td><td>平台已判失败后才收到成功</td><td>立即发 M-02 结束，本单 0 元</td><td>收到后立即锁车并回报锁车事件</td></tr>
      <tr><td>入桩未锁</td><td>桩识别车辆在位，但没有锁车报告</td><td>发 M-02 请求锁车</td><td>锁车并回报锁车事件</td></tr>
      <tr><td>平台收不到报告</td><td>网络中断等原因，事件没有送达</td><td>订单暂不闭合</td><td>本地保留，72 小时内按事件时间补报</td></tr>
      <tr><td>降级单</td><td>平台故障期间开出的订单，M-01 中 degraded 为 true</td><td>恢复后逐单匹配运营商报告</td><td>按普通开锁执行，不需要额外处理</td></tr>
      <tr><td>桩与车端判定冲突</td><td>桩识别在点，车端判定不在点（或相反）</td><td>以桩为准</td><td>无需处理</td></tr>
      <tr><td>签名错误、请求被重放、来源未登记</td><td>安全校验未通过</td><td>拒绝请求，不执行</td><td>检查密钥、时间戳、随机串与来源地址</td></tr>
      <tr><td>凭证已停用</td><td>该家凭证被停用或已轮换</td><td>拒绝全部请求</td><td>使用新凭证，或联系平台接入负责人</td></tr></tbody></table></div>`,
  '5': () => `
    <ul class="op-list"><li>全程 HTTPS。</li><li>每家运营商一把独立密钥，请求携带凭证编号、时间戳、随机串与签名；签名由该家的密钥对请求内容计算得出，平台据此校验请求没有被篡改。</li><li>时间戳超出允许的时间窗口，或随机串重复出现，一律按重放拒绝。</li><li>只接受已登记的来源地址，并对请求限流。</li><li>单家密钥泄露，只需停用或轮换这一家，不影响其他运营商。</li></ul>
    <p>签名错误、重放和未登记来源，都属于符合性测试中的安全用例，须全部通过。</p>`,
  '6': ctx => `
    <ol class="op-list" style="list-style:decimal">
      <li><b>自助联调：</b>用联调环境的模拟车辆、模拟用户、模拟支付跑通流程，不产生任何真实收费。</li>
      <li><b>符合性测试：</b>覆盖重复请求、延迟请求、补报、降级单与异常路径，共 ${ctx.caseCount || 18} 项，须<b>全部通过</b>，时限达标。失败项由接入智能体给出原因与修改建议。</li>
      <li><b>车辆清单与换码：</b>提交车辆清单，并完成车上政府码的换贴。</li>
      <li><b>真车联调：</b>用规定数量的真车实际骑行（${ctx.realVehicles ? `当前为 ${ctx.realVehicles} 辆` : '数量按平台公司参数'}），订单与锁车报告必须全部匹配。</li>
      <li><b>验收报告：</b>接入智能体汇总测试与联调结果，生成验收报告。</li>
      <li><b>确认上线：</b>平台公司接入负责人确认通过即上线：该品牌车辆开放可租，待启用的政府码启用；运营商同时关闭自有扫码入口。</li></ol>
    <p>联调到验收的目标时长${ctx.targetDays ? `为 ${ctx.targetDays} 天` : '由平台公司设定'}。</p>
    <h3>接入状态</h3>
    <p>共五种：未接入、联调中、已接入、暂停可租、已退出。接入状态只记录接入事实，不代表经营许可；「已退出」由主管部门管理员按线下结果设置，设置后该运营商车辆的政府码失效。品牌开锁成功率或可用率持续低于门槛时，会被标为「暂停可租」，指标恢复后自动回到「已接入」，已在骑行的订单正常结束。</p>`,
};

export default {
  path: '#/op/docs', title: '接入文档', menu: '接入文档', perm: 'integration.doc',
  async mount(el, query, pctx) {
    injectStyle();
    el.innerHTML = '';
    const host = ui.el('<div></div>');
    el.append(host);
    const printBtn = ui.el('<button type="button" class="a-btn a-btn-default">复制联调环境地址</button>');
    setHead(el, pctx, '接入文档', '正在读取接入规范…', [printBtn]);
    ui.setDisabled(printBtn, '联调环境地址读取中', '稍候片刻');

    await loadInto(host, async () => {
      const me = await getMe();
      const [spec, sandbox, integ] = await Promise.all([
        op().integration.spec(),
        me.permissions.includes('integration.sandbox.own') ? op().integration.sandbox().catch(() => null) : Promise.resolve(null),
        op().integration.get().catch(() => []),
      ]);
      return { spec, sandbox, integ };
    }, async ({ spec, sandbox, integ }, h) => {
      const subText = `${spec.name} · 版本 ${spec.version} · 发布于 ${ui.datetime(spec.publishedAt)}`;
      if (pctx && pctx.setSub) pctx.setSub(subText); else h.prepend(ui.el(`<p class="op-note">${esc(subText)}</p>`));
      if (sandbox && sandbox.baseUrl) { ui.setEnabled(printBtn); printBtn.onclick = () => copyText(sandbox.baseUrl, '联调环境地址'); } else ui.setDisabled(printBtn, '当前账号不能查看联调环境', '请联系账号管理员调整授权');
      const ctx = { baseUrl: sandbox && sandbox.baseUrl, targetDays: integ[0] && integ[0].targetDays, realVehicles: integ[0] && integ[0].acceptance && integ[0].acceptance.realVehiclesRequired, caseCount: integ[0] && integ[0].latestRun && integ[0].latestRun.summary.total };
      const parts = spec.sections.map(s => {
        const ex = s.no === '2' ? json({ brand: 'BR03', plateNo: 'CL384920', model: '驰路 C5 电单车', pileTag: 'E28011A93F07C21B5D4E0A7C' }) : s.example ? pretty(s.example) : null;
        return { id: `s${s.no.replace('.', '-')}`, no: s.no, title: s.title, html: `<p><b>${esc(s.summary)}</b></p>${(BODY[s.no] || (() => ''))(ctx)}${ex ? `<h3>参考报文</h3>${code(ex)}` : ''}` };
      });
      parts.push({ id: 'cases', no: '附', title: '符合性测试用例', html: '<div id="op-cases"><div class="op-pending"><span class="a-spin"></span>正在读取用例清单…</div></div>' });
      const wrap = ui.el(`<div class="op-doc"><nav class="op-toc" aria-label="目录">${parts.map(p => `<button type="button" data-t="${p.id}">${esc(p.no)}　${esc(p.title)}</button>`).join('')}</nav>
        <article class="op-article">${parts.map(p => `<section id="${p.id}"><h2>${esc(p.no)}　${esc(p.title)}</h2>${p.html}</section>`).join('')}</article></div>`);
      h.append(wrap);
      ui.$$('[data-copy-code]', wrap).forEach(b => b.addEventListener('click', () => copyText(ui.$('pre', b.parentElement).textContent, '报文')));
      const toc = ui.$$('.op-toc button', wrap);
      toc.forEach(b => b.addEventListener('click', () => { const s = ui.$(`#${b.dataset.t}`, wrap); s.scrollIntoView({ behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth', block: 'start' }); }));
      const io = new IntersectionObserver(es => { es.forEach(e => { if (e.isIntersecting) toc.forEach(b => b.classList.toggle('is-on', b.dataset.t === e.target.id)); }); }, { rootMargin: '0px 0px -70% 0px' });
      ui.$$('section', wrap).forEach(s => io.observe(s));
      toc[0] && toc[0].classList.add('is-on');
      loadCases(ui.$('#op-cases', wrap));
    });

    async function loadCases(box) {
      try {
        const runs = await op().integration.runs();
        const run = runs[0];
        if (!run) { box.innerHTML = '<p class="op-note">暂无用例清单。</p>'; return; }
        box.innerHTML = `<p>符合性测试共 ${run.summary.total} 项用例，覆盖 M-01～M-04 的重复请求、延迟请求、补报、降级单、异常路径与接入安全。每次测试可在「联调与测试」页自助发起。</p><div style="overflow:auto"><table class="op-mini"><thead><tr><th>用例编号</th><th>分组</th><th>验证内容</th></tr></thead><tbody>${run.cases.map(c => `<tr><td class="op-mono">${esc(c.caseId)}</td><td>${esc(c.group)}</td><td>${esc(c.name)}</td></tr>`).join('')}</tbody></table></div>`;
      } catch (e) { box.innerHTML = ''; box.append(ui.retry({ text: `用例清单没有加载出来：${errText(e)}`, onRetry: () => { box.innerHTML = ''; loadCases(box); } })); }
    }
  },
};

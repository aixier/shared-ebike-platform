// 欠费补付（R-09；BR-05）：欠费金额与明细，一键补付；补付后立即可以开锁。
import { ui, api, app, loadArrears, arrearsContent, payArrearsFlow, nowDate } from '../kit.js';

let S = null;
export default {
  route: '#/debt',
  title: '欠费补付',
  async mount(el) {
    S = { el };
    el.innerHTML = `<div class="m-body">${ui.skeleton('card', 2)}</div><div class="m-bottom"></div>`;
    await load();
  },
  unmount() { S = null; },
};

async function load() {
  const my = S;
  try {
    const [data, now] = await Promise.all([loadArrears(), nowDate()]);
    if (S !== my) return;
    const el = my.el;
    if (!data.rows.length) {
      el.innerHTML = '<div class="m-body"></div><div class="m-bottom"></div>';
      ui.$('.m-body', el).append(ui.empty({ icon: '✅', title: '没有待补付的欠费', text: '可以直接扫码骑行。' }));
      const b = ui.el('<button type="button" class="m-btn m-btn-primary">扫码用车</button>');
      b.addEventListener('click', () => app().replace('#/scan'));
      ui.$('.m-bottom', el).append(b);
      return;
    }
    el.innerHTML = '<div class="m-body"></div><div class="m-bottom"></div>';
    ui.$('.m-body', el).append(arrearsContent(data, now));
    const pay = ui.el(`<button type="button" class="m-btn m-btn-primary">一键补付 ${ui.money(data.total)}</button>`);
    pay.addEventListener('click', async () => {
      const res = await payArrearsFlow(data.total);
      if (S !== my || !res) return;
      ui.toast(`已补付 ${ui.money(res.paid)}，可以开锁了`, { type: 'ok' });
      load();
    });
    ui.$('.m-bottom', el).append(pay);
  } catch (e) {
    if (S !== my) return;
    my.el.innerHTML = '';
    my.el.append(ui.retry({ text: e.code === 'NOT_LOGGED_IN' ? '请先授权登录后查看欠费' : '欠费信息没有加载出来，请检查网络后重试', action: e.code === 'NOT_LOGGED_IN' ? '一键授权' : '重新加载', onRetry: async () => { if (e.code === 'NOT_LOGGED_IN') await app().authorize(); my.el.innerHTML = ''; load(); } }));
  }
}

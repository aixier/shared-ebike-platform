// 适配层纯函数自测：node 原型/shared/real/_自测/convert.test.mjs
import assert from 'node:assert/strict';
import { fromWire, toLocalTime, toRfc3339, moneyToWire, listQuery, centsToYuan } from '../convert.js';

let n = 0;
const t = (name, fn) => { fn(); n++; console.log('  ok', name); };

t('RFC3339 +08:00 直接截取', () => assert.equal(toLocalTime('2026-11-20T00:38:41.575+08:00'), '2026-11-20 00:38:41'));
t('RFC3339 Z 换算成北京时间', () => assert.equal(toLocalTime('2026-11-19T16:38:41Z'), '2026-11-20 00:38:41'));
t('非时间字符串原样', () => { assert.equal(toLocalTime('2026-11-20'), '2026-11-20'); assert.equal(toLocalTime('已支付'), '已支付'); assert.equal(toLocalTime(null), null); });
t('本地时间转 RFC3339', () => { assert.equal(toRfc3339('2026-11-20 08:00:00'), '2026-11-20T08:00:00+08:00'); assert.equal(toRfc3339('2026-11-20 08:00'), '2026-11-20T08:00:00+08:00'); assert.equal(toRfc3339('2026-11-20T08:00:00+08:00'), '2026-11-20T08:00:00+08:00'); });
t('金额分转元，只转 Money 字段的数字', () => {
  const r = fromWire({ price: { startFee: 150, perMinute: 50, deposit: 19900, creditFreeThreshold: 550 }, fee: { rideFee: 150, unpaid: 0 }, deposit: { amountPaid: 19900, score: 650 }, durationSec: 16, bill: { lines: [{ key: 'rideFee', amount: 150, total: false }] } });
  assert.deepEqual(r.price, { startFee: 1.5, perMinute: 0.5, deposit: 199, creditFreeThreshold: 550 });
  assert.equal(r.fee.rideFee, 1.5); assert.equal(r.deposit.amountPaid, 199); assert.equal(r.deposit.score, 650); assert.equal(r.durationSec, 16);
  assert.equal(r.bill.lines[0].amount, 1.5);
});
t('时间字段整体转换', () => assert.equal(fromWire({ a: [{ at: '2026-11-20T00:40:20+08:00' }] }).a[0].at, '2026-11-20 00:40:20'));
t('元转分取整，避免浮点误差', () => { assert.deepEqual(moneyToWire({ startFee: 1.1, perMinute: 0.55, name: 'x', coupons: [{ amount: 2.3 }] }, ['startFee', 'perMinute', 'amount']), { startFee: 110, perMinute: 55, name: 'x', coupons: [{ amount: 230 }] }); assert.equal(centsToYuan(null), null); });
t('列表参数：sort 拆开、时间转换、空值丢弃、数组逗号连接', () => assert.deepEqual(listQuery({ page: 2, sort: { key: 'startAt', dir: 'desc' }, from: '2026-11-01 00:00:00', q: '', status: undefined, orderIds: ['a', 'b'] }), { page: 2, sortKey: 'startAt', sortDir: 'desc', from: '2026-11-01T00:00:00+08:00', orderIds: 'a,b' }));
console.log(`${n} 项通过`);

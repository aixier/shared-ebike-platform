// 真实接口与页面之间的数据适配：金额（分↔元）、时间（RFC3339↔'YYYY-MM-DD HH:MM:SS'）、查询参数。
// 金额字段名取自 contracts/openapi 里声明为 Money 的属性；只在值是数字时转换。
// 不含 total：分页里的 total 是条数；结算汇总里叫 total 的金额字段如需转换由对应适配单独处理。

export const MONEY_KEYS = new Set([
  'adjustmentFee', 'alipay', 'amount', 'amountPaid', 'amountSoFar', 'arrears', 'balance', 'balanceAfter', 'base', 'baseDelta', 'baseSum',
  'channelActual', 'channelPaid', 'couponDeduct', 'csRefundLimit', 'deducted', 'deposit', 'depositAmount', 'depositDeducted', 'detailFee', 'diff',
  'dispatchFee', 'expected', 'fee', 'feeDelta', 'limitAmount', 'liveEstimate', 'liveMonthCardDeduct', 'liveRideFee', 'manualRefunded', 'maxAmount',
  'monthCardDeduct', 'monthCardIncome', 'monthCardShare', 'netReceived', 'newBase', 'orderNetReceived', 'orderPayments', 'paid', 'paidIn', 'payable',
  'payableFee', 'perMinute', 'perOrderCap', 'platformIncome', 'previousBase', 'price', 'receivable', 'receivableSum', 'received', 'refundAmount',
  'refundLimit', 'refundable', 'refunded', 'refundedAtGeneration', 'refundingAmount', 'refundsManual', 'refundsOriginal', 'rideFee', 'rideIncomeNet',
  'shortfall', 'startFee', 'toppedUp', 'unpaid', 'userBalanceSum', 'wechat', 'topupAmount',
]);

export const centsToYuan = c => (typeof c === 'number' ? Math.round(c) / 100 : c);
export const yuanToCents = y => (typeof y === 'number' ? Math.round(y * 100) : y);

const RFC = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2})(?:\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

/** RFC 3339 -> 'YYYY-MM-DD HH:MM:SS'（北京时间）。不是 RFC 3339 的原样返回 */
export function toLocalTime(s) {
  if (typeof s !== 'string') return s;
  const m = RFC.exec(s);
  if (!m) return s;
  if (m[3] === '+08:00') return `${m[1]} ${m[2]}`;
  const t = new Date(s);
  if (Number.isNaN(t.getTime())) return s;
  return new Date(t.getTime() + 8 * 3600e3).toISOString().slice(0, 19).replace('T', ' ');
}

/** 'YYYY-MM-DD HH:MM:SS'（北京时间）-> RFC 3339。已是 RFC 3339 或日期则原样 */
export function toRfc3339(s) {
  if (typeof s !== 'string') return s;
  const m = /^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}(?::\d{2})?)$/.exec(s);
  if (!m) return s;
  return `${m[1]}T${m[2].length === 5 ? m[2] + ':00' : m[2]}+08:00`;
}

/** 后端响应 -> 页面形态：递归转金额与时间 */
export function fromWire(v, key) {
  if (Array.isArray(v)) return v.map(x => fromWire(x, key));
  if (v && typeof v === 'object') {
    const o = {};
    for (const k of Object.keys(v)) o[k] = fromWire(v[k], k);
    return o;
  }
  if (typeof v === 'number') return MONEY_KEYS.has(key) ? centsToYuan(v) : v;
  if (typeof v === 'string') return toLocalTime(v);
  return v;
}

/** 页面请求体里的金额字段 -> 分（只转 keys 里列出的字段，浅层与数组内对象） */
export function moneyToWire(obj, keys) {
  if (Array.isArray(obj)) return obj.map(x => moneyToWire(x, keys));
  if (!obj || typeof obj !== 'object') return obj;
  const o = {};
  for (const k of Object.keys(obj)) {
    const v = obj[k];
    o[k] = keys.includes(k) && typeof v === 'number' ? yuanToCents(v) : (v && typeof v === 'object' ? moneyToWire(v, keys) : v);
  }
  return o;
}

/** 页面的分页与排序参数 -> 后端查询参数；时间筛选转 RFC 3339；空值丢弃 */
export function listQuery(p = {}) {
  const q = {};
  for (const [k, v] of Object.entries(p)) {
    if (v == null || v === '') continue;
    if (k === 'sort') { if (v.key) { q.sortKey = v.key; q.sortDir = v.dir || 'asc'; } continue; }
    if (k === 'from' || k === 'to') { q[k] = toRfc3339(v); continue; }
    q[k] = Array.isArray(v) ? v.join(',') : v;
  }
  return q;
}

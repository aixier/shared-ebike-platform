// 错误码目录。所有接口失败都以 ApiError 拒绝（reject）。
// message 是给用户看的"一句原因"；next 是"一个下一步"；charged 表示是否产生费用（用户侧失败必填）。

export class ApiError extends Error {
  constructor(code, message, extra = {}) {
    const def = CATALOG[code] || {};
    super(message || def.message || '操作没有成功，请稍后重试');
    this.name = 'ApiError';
    this.code = code;
    this.status = extra.status || def.status || 400;
    if ('charged' in extra || 'charged' in def) this.charged = 'charged' in extra ? extra.charged : def.charged;
    this.next = extra.next !== undefined ? extra.next : (def.next || null);
    for (const k of Object.keys(extra)) if (!['status', 'charged', 'next'].includes(k)) this[k] = extra[k];
  }
  toJSON() { const o = { code: this.code, message: this.message, status: this.status }; for (const k of Object.keys(this)) o[k] = this[k]; return o; }
}

const N = (label, action, params) => ({ label, action, ...(params ? { params } : {}) });

export const CATALOG = {
  /* 传输层 */
  NETWORK: { status: 503, message: '网络不稳定，请求没有完成', next: N('重试', 'retry') },
  SERVER_BUSY: { status: 503, message: '服务繁忙，请稍后再试', next: N('重试', 'retry') },
  /* 通用 */
  NOT_FOUND: { status: 404, message: '没有找到对应的记录' },
  INVALID: { status: 400, message: '填写的内容有误，请检查后再提交' },
  REASON_REQUIRED: { status: 400, message: '请填写操作事由（至少 4 个字）' },
  CONFLICT: { status: 409, message: '记录状态已变化，请刷新后再操作', next: N('刷新', 'retry') },
  DEBUG_ONLY: { status: 403, message: '仅在调试模式下可用' },
  /* 用户端：登录与账户 */
  NOT_LOGGED_IN: { status: 401, message: '请先授权登录', charged: false, next: N('一键授权', 'login') },
  AUTH_UNAVAILABLE: { status: 503, message: '微信或支付宝授权暂时不可用，请稍后再试', charged: false, next: N('重试', 'retry') },
  ACCOUNT_CANCELLED: { status: 403, message: '该账户已注销', charged: false, next: N('重新授权', 'login') },
  ACCOUNT_FROZEN: { status: 403, message: '账户已冻结', charged: false, next: N('查看解冻方法', 'unfreezeGuide') },
  NEED_ONBOARDING: { status: 403, message: '首次使用，完成授权与实名即可骑行', charged: false, next: N('去完成', 'onboarding') },
  REALNAME_INVALID_ID: { status: 400, message: '证件号码格式不正确，请核对后重新输入' },
  REALNAME_NAME_MISMATCH: { status: 400, message: '姓名与证件信息不一致，请核对后重新输入' },
  UNDER_AGE: { status: 403, message: '未满 16 周岁，不能骑行电动自行车', charged: false, next: N('知道了', 'close') },
  REALNAME_UNAVAILABLE: { status: 503, message: '实名核验服务暂时不可用，请稍后再试', next: N('重试', 'retry') },
  CREDIT_SCORE_LOW: { status: 403, message: '信用分暂未达到免押条件', charged: false, next: N('缴押金骑行', 'payDeposit') },
  ALREADY_DONE: { status: 409, message: '已经办理过了，无需重复操作' },
  /* 扫码与开锁 */
  CODE_NOT_FOUND: { status: 404, message: '未识别到政府码，请扫车把上的二维码', charged: false, next: N('重新扫码', 'rescan') },
  CODE_NOT_ENABLED: { status: 403, message: '这辆车的码还未启用', charged: false, next: N('查看附近可用车', 'viewNearby') },
  CODE_INVALID: { status: 403, message: '这辆车的码已失效，暂不能骑行', charged: false, next: N('查看附近可用车', 'viewNearby') },
  BRAND_PAUSED: { status: 403, message: '该品牌暂停可租', charged: false, next: N('查看附近其他品牌车辆', 'viewNearby') },
  BRAND_NOT_AVAILABLE: { status: 403, message: '该品牌暂未开放骑行', charged: false, next: N('查看附近可用车', 'viewNearby') },
  HAS_ARREARS: { status: 403, message: '你有未付的骑行费用，补付后即可开锁', charged: false, next: N('一键补付', 'payArrears') },
  UNPAID_ORDER: { status: 403, message: '上一单还未支付，支付后即可开锁', charged: false, next: N('去支付', 'payOrder') },
  DEPOSIT_REQUIRED: { status: 403, message: '开通信用免押或缴押金后即可骑行', charged: false, next: N('开通免押', 'openCreditFree') },
  DEPOSIT_INSUFFICIENT: { status: 403, message: '押金余额不足，补缴或开通免押后即可骑行', charged: false, next: N('补缴押金', 'topupDeposit') },
  DEPOSIT_REFUNDING: { status: 403, message: '押金退还中，暂不能开新单', charged: false, next: N('开通信用免押', 'openCreditFree') },
  HAS_RIDING: { status: 409, message: '你有一单正在骑行，还车后才能再开新单', charged: false, next: N('查看骑行中订单', 'goRide') },
  UNLOCK_NO_RESPONSE: { status: 504, message: '车辆没有响应，未扣费', charged: false, next: N('查看附近可用车', 'viewNearby') },
  UNLOCK_LOW_BATTERY: { status: 409, message: '车辆电量不足，未扣费', charged: false, next: N('查看附近可用车', 'viewNearby') },
  UNLOCK_FAULT: { status: 409, message: '车辆故障，未扣费', charged: false, next: N('查看附近可用车', 'viewNearby') },
  UNLOCK_OCCUPIED: { status: 409, message: '车辆正在被使用，未扣费', charged: false, next: N('查看附近可用车', 'viewNearby') },
  /* 降级准入 */
  DEGRADE_REJECT_ARREARS: { status: 403, message: '你有未付的骑行费用，平台恢复后补付即可骑行', charged: false, next: N('查看欠费', 'viewArrears') },
  DEGRADE_REJECT_REFUNDING: { status: 403, message: '押金退还中，暂不能开新单', charged: false, next: N('查看押金', 'viewDeposit') },
  DEGRADE_USER_LIMIT: { status: 403, message: '你还有一单费用稍后收取，收取后才能再开新单', charged: false, next: N('查看订单', 'viewOrders') },
  DEGRADE_PAUSED_TIGHT: { status: 503, message: '平台临时限制开新单，请稍后再试', charged: false, next: N('稍后重试', 'retry') },
  DEGRADE_STOPPED: { status: 503, message: '平台暂时无法开新单，正在恢复', charged: false, next: N('稍后重试', 'retry') },
  DEGRADE_NEW_USER_UNAVAILABLE: { status: 503, message: '平台暂时无法为新用户开单，请稍后再试', charged: false, next: N('稍后重试', 'retry') },
  /* 还车 */
  NO_RIDING: { status: 404, message: '当前没有骑行中的订单' },
  DISPATCH_CONFIRM_REQUIRED: { status: 409, message: '当前不在站点内，还车将收取调度费', charged: true, next: N('导航到最近站点', 'navStation') },
  PILE_FULL: { status: 409, message: '桩位已满，在站点内桩旁锁车即可，不收调度费', charged: false, next: N('桩旁锁车', 'lockBeside') },
  PILE_UNAVAILABLE: { status: 409, message: '该桩暂时不可用，在站点内锁车即可正常还车', charged: false, next: N('站内锁车', 'lockBeside') },
  /* 付款、押金 */
  PAY_DECLINED: { status: 402, message: '支付没有成功：支付账户余额不足', charged: false, next: N('换个方式重新支付', 'payOrder') },
  NOTHING_TO_PAY: { status: 409, message: '没有需要支付的费用' },
  REFUND_BLOCKED: { status: 409, message: '暂不能退押金', next: N('先去处理', 'resolveBlockers') },
  NOT_DEPOSIT_USER: { status: 409, message: '你当前没有在平台缴纳押金' },
  CANCEL_BLOCKED: { status: 409, message: '注销前还有事项需要处理', next: N('先去处理', 'resolveBlockers') },
  PAYEE_NAME_MISMATCH: { status: 400, message: '收款账户须为本人实名账户，户名与实名信息不一致' },
  COUPON_NOT_OPEN: { status: 409, message: '该券当前不在领取时间' },
  COUPON_LIMIT: { status: 409, message: '该券每人限领一张，你已领取' },
  /* 管理端 */
  ADMIN_NOT_LOGGED_IN: { status: 401, message: '登录已失效，请重新登录', next: N('去登录', 'login') },
  LOGIN_FAILED: { status: 401, message: '账号或密码不正确' },
  ACCOUNT_DISABLED: { status: 403, message: '该账号已停用，请联系平台公司账号管理员' },
  FORBIDDEN: { status: 403, message: '你的账号没有该操作的权限' },
  SCOPE_DENIED: { status: 403, message: '只能查看和操作本运营商的数据' },
  READONLY: { status: 403, message: '运营商已退出，账号为只读，不能提交或修改' },
  OVER_LIMIT_NEEDS_SUPERVISOR: { status: 403, message: '退款金额超出客服授权额度，需客服主管确认' },
  PRICE_NOT_DISPUTABLE: { status: 400, message: '价格由主管部门统一制定，不受理异议；可对时长、里程提出异议' },
  EVIDENCE_LOCKED: { status: 403, message: '证据提交后不可修改或删除' },
  SETTLEMENT_STATE: { status: 409, message: '结算单当前状态不允许该操作' },
  ADJUSTMENT_UNCONFIRMED: { status: 409, message: '还有调整项未确认，确认后才能审核' },
  MONTH_NOT_ENDED: { status: 409, message: '该结算月尚未结束，次月 1 日起才能生成' },
  ALREADY_GENERATED: { status: 409, message: '该结算月的结算单已生成' },
  VEHICLE_LIST_INVALID: { status: 400, message: '车辆清单有错误行，请修改后重新上传' },
  ACCEPTANCE_NOT_READY: { status: 409, message: '验收条件尚未满足，暂不能确认通过' },
  DEGRADE_NOT_ACTIVE: { status: 409, message: '当前没有处于降级开单' },
  NOT_RIDING: { status: 409, message: '该订单不在骑行中' },
  STATION_IN_USE: { status: 409, message: '站点仍在使用，暂不能删除' },
  CREDENTIAL_DISABLED: { status: 409, message: '联调凭证已停用，请联系平台公司接入负责人' },
};

/* 权限点、对象类型、留痕字段的中文名 */
export const PERM_LABELS = {
  'pricing.view': '查看价格与规则', 'pricing.edit': '录入价格与规则', 'station.edit': '编辑站点与电子围栏', 'pile.edit': '登记与管理桩',
  'access.view': '查看全市接入状态', 'access.setExit': '设置运营商退出', 'evidence.view': '查询执法证据', 'evidence.export': '导出执法证据', 'audit.view': '查询操作留痕',
  'vehicle.view.own': '查看本运营商车辆', 'vehicle.edit.own': '维护本运营商车辆清单', 'order.view.own': '查看本运营商订单', 'order.export.own': '下载本运营商订单明细',
  'settlement.view.own': '查看本运营商结算单', 'settlement.export.own': '下载本运营商结算明细', 'dispute.submit.own': '对时长、里程提出异议', 'access.view.own': '查看本运营商接入与验收进度',
  'integration.doc': '查看接入文档', 'integration.sandbox.own': '使用联调环境', 'integration.conformance.own': '发起符合性测试并提交真车联调', 'acceptance.view.own': '查看验收进度',
  'settlement.manage': '生成、审核、发送结算单', 'settlement.adjust': '录入与确认结算调整项', 'dispute.decide': '处理运营商异议', 'recon.manage': '收单对账与差异处理',
  'deposit.view': '查看押金专户', 'arrears.view': '查看欠费清单', 'refund.manualTransfer': '录入人工转账退款', 'degrade.closure.view': '查看降级恢复闭合',
  'user.view': '查询用户与订单', 'order.view': '查询订单与客服工单', 'refund.confirm': '确认退款（额度内）', 'refund.confirm.overLimit': '确认超额退款', 'order.forceEnd': '强制结束订单并锁车',
  'account.unfreeze': '人工解冻账户', 'deposit.assist': '协助用户退押', 'account.cancel.assist': '协助用户注销',
  'ops.monitor': '查看运行监控', 'degrade.control': '降级收紧、停止与放宽', 'degrade.closure.handle': '恢复闭合人工处理', 'access.pause': '暂停与恢复品牌可租', 'acceptance.confirm': '确认接入验收', 'code.issue': '发放政府码',
  'account.manage': '账号开通、授权与停用', 'enforce.scan': '扫码核查', 'enforce.capture': '现场取证', 'enforce.mine': '查看本人取证记录',
};
export const permLabel = p => PERM_LABELS[p] || p;

export const TARGET_TYPES = {
  station: '站点', pile: '桩', account: '账号', order: '订单', settlement: '结算单', user: '用户', brand: '品牌', degrade: '降级事件', pricing: '价格规则版本',
  integration: '接入', evidence: '执法证据', payment: '资金流水', dispute: '运营商异议', operator: '运营商', vehicle: '车辆', ticket: '客服工单',
};
export const targetLabel = t => TARGET_TYPES[t] || t || '—';

/** 留痕 before / after 里常见字段的中文名 */
export const FIELD_LABELS = {
  status: '状态', name: '名称', fence: '电子围栏', capacity: '容量', type: '类型', versionId: '规则版本',
  'ride.startFee': '起步价', 'ride.startMinutes': '起步含分钟', 'ride.perMinute': '分钟单价', dispatchFee: '调度费',
  'monthCard.price': '月卡价格', 'monthCard.days': '月卡天数', 'monthCard.perOrderCap': '月卡每单抵扣上限', 'monthCard.allocation': '月卡收入分配',
  'deposit.amount': '押金金额', 'creditFree.threshold': '免押信用分门槛', coupons: '优惠券模板',
  'riskLimits.perUserOpenDegradedOrders': '每用户未结降级单数', 'riskLimits.hourlyDegradedOrders': '每小时降级单上限', 'riskLimits.degradeDurationAlertMinutes': '降级持续告警（分钟）',
  'riskLimits.normal.maxMinutes': '常规档单笔最长（分钟）', 'riskLimits.normal.maxAmount': '常规档单笔上限', 'riskLimits.tight.maxMinutes': '紧档单笔最长（分钟）', 'riskLimits.tight.maxAmount': '紧档单笔上限',
};
export function fieldLabel(k) {
  if (FIELD_LABELS[k]) return FIELD_LABELS[k];
  const m = /^serviceFee\.ratios\.(BR\d+)$/.exec(k); if (m) return `服务费比例（${m[1]}）`;
  return k;
}

export const ROLES = ['主管部门管理员', '运营商业务', '运营商技术对接', '财务', '客服', '客服主管', '运维值班', '运维接入负责人', '运维账号管理员', '执法人员'];
export const ROLE_NOTE = {
  主管部门管理员: '全市范围：改价、站点、桩、接入状态、证据', 运营商业务: '本运营商：车辆、订单、结算与异议', 运营商技术对接: '本运营商：接入文档、联调与符合性测试',
  财务: '平台公司：结算、异议、对账、押金专户', 客服: '平台公司：用户与订单、额度内退款、强制结束、人工解冻', 客服主管: '在客服权限之上可确认超额退款',
  运维值班: '平台公司：运行监控、降级控制、恢复闭合', 运维接入负责人: '平台公司：接入验收、发码', 运维账号管理员: '平台公司：账号开通、授权与停用', 执法人员: '手机页：扫码核查与取证',
};

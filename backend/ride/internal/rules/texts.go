package rules

import (
	"fmt"
	"time"
)

// 每种失败的一句原因与一个下一步（R-05、NFR-03）。文案取原型 API.md 第 2 节的默认原因。
// 这里只放文案表，判定在 admission.go；改文案只改本文件。
var texts = map[string]struct {
	msg    string
	label  string
	action string
}{
	"NOT_LOGGED_IN":                {"请先授权登录", "授权登录", "login"},
	"ACCOUNT_CANCELLED":            {"该账户已注销", "联系客服", "contactCS"},
	"ACCOUNT_FROZEN":               {"账户已冻结", "看解冻方法", "unfreezeGuide"},
	"NEED_ONBOARDING":              {"首次使用，完成授权与实名即可骑行", "去实名", "verifyRealName"},
	"CODE_NOT_FOUND":               {"未识别到政府码，请扫车把上的二维码", "重新扫码", "rescan"},
	"CODE_NOT_ENABLED":             {"这辆车的码还未启用", "看附近车", "viewNearby"},
	"BRAND_PAUSED":                 {"该品牌暂停可租，附近其他品牌的车可以骑", "看附近车", "viewNearby"},
	"CODE_INVALID":                 {"这辆车的码已失效，暂不能骑行", "看附近车", "viewNearby"},
	"BRAND_NOT_AVAILABLE":          {"该品牌暂未开放骑行", "看附近车", "viewNearby"},
	"HAS_ARREARS":                  {"你有未付的骑行费用，补付后即可开锁", "一键补付", "payArrears"},
	"UNPAID_ORDER":                 {"上一单还有费用未支付，支付后即可开锁", "去支付", "payOrder"},
	"DEPOSIT_REQUIRED":             {"开通信用免押或缴押金后即可骑行", "开通免押", "openCreditFree"},
	"DEPOSIT_INSUFFICIENT":         {"押金余额不足，补缴或开通免押后即可骑行", "补缴押金", "topupDeposit"},
	"DEPOSIT_REFUNDING":            {"押金退还中，暂不能开新单", "查看押金", "viewDeposit"},
	"HAS_RIDING":                   {"你有一单正在骑行，还车后才能再开新单", "去骑行中", "goRide"},
	"UNLOCK_NO_RESPONSE":           {"车辆没有响应，未扣费", "看附近车", "viewNearby"},
	"UNLOCK_LOW_BATTERY":           {"车辆电量不足，未扣费", "看附近车", "viewNearby"},
	"UNLOCK_FAULT":                 {"车辆故障，未扣费", "看附近车", "viewNearby"},
	"UNLOCK_OCCUPIED":              {"车辆正在被使用，未扣费", "看附近车", "viewNearby"},
	"DEGRADE_REJECT_ARREARS":       {"你有未付的骑行费用，平台恢复后补付即可骑行", "查看欠费", "viewArrears"},
	"DEGRADE_REJECT_REFUNDING":     {"押金退还中，暂不能开新单", "查看押金", "viewDeposit"},
	"DEGRADE_USER_LIMIT":           {"你还有一单费用稍后收取，收取后才能再开新单", "查看订单", "viewOrders"},
	"DEGRADE_PAUSED_TIGHT":         {"平台临时限制开新单，请稍后再试", "稍后重试", "retry"},
	"DEGRADE_NEW_USER_UNAVAILABLE": {"平台暂时无法为新用户开单，请稍后再试", "稍后重试", "retry"},
	"DEGRADE_STOPPED":              {"平台暂时无法开新单，正在恢复", "稍后重试", "retry"},
	"NO_RIDING":                    {"当前没有骑行中的订单", "查看订单", "viewOrders"},
	"DISPATCH_CONFIRM_REQUIRED":    {"当前不在站点内，还车将收取调度费", "导航到最近站点", "navStation"},
	"PILE_FULL":                    {"桩位已满，在站点内桩旁锁车即可，不收调度费", "桩旁锁车", "lockBeside"},
	"PILE_UNAVAILABLE":             {"该桩暂时不可用，在站点内锁车即可正常还车", "桩旁锁车", "lockBeside"},
	"SERVER_BUSY":                  {"服务繁忙，请稍后再试", "重试", "retry"},
}

// NewFail 按错误码取默认文案；message 不为空时覆盖。
func NewFail(code, message string, extra map[string]any) *Fail {
	t, ok := texts[code]
	f := &Fail{Code: code, Message: message, Extra: extra}
	if ok {
		if f.Message == "" {
			f.Message = t.msg
		}
		f.Next = &Next{Label: t.label, Action: t.action}
	}
	if f.Message == "" {
		f.Message = code
	}
	return f
}

// DefaultMessage 错误码的默认原因。
func DefaultMessage(code string) string {
	if t, ok := texts[code]; ok {
		return t.msg
	}
	return ""
}

// DefaultNext 错误码的默认下一步。
func DefaultNext(code string) *Next {
	if t, ok := texts[code]; ok {
		return &Next{Label: t.label, Action: t.action}
	}
	return nil
}

// Yuan 分转"¥x.x"（用户侧一位小数）。
func Yuan(cents int64) string { return fmt.Sprintf("¥%.1f", float64(cents)/100) }

// cst 北京时间。
var cst = time.FixedZone("CST", 8*3600)

// MonthDay 时刻转"12月8日 09:00"。
func MonthDay(t time.Time) string {
	t = t.In(cst)
	return fmt.Sprintf("%d月%d日 %02d:%02d", int(t.Month()), t.Day(), t.Hour(), t.Minute())
}

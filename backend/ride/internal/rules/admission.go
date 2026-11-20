package rules

import (
	"fmt"
	"time"
)

// VehicleBlock 车辆本身能否租（码未启用 → 品牌暂停可租 → 码已失效 → 品牌未开放）。
// 附近车由调用方补到 Extra["nearby"]。
func VehicleBlock(v Vehicle, b *Brand) *Fail {
	if v.CodeStatus == CodePending {
		msg := "这辆车的码还未启用"
		if v.CodeEnableAt != nil {
			msg += "，" + MonthDay(*v.CodeEnableAt) + " 起可骑"
		}
		return NewFail("CODE_NOT_ENABLED", msg, map[string]any{"codeEnableAt": v.CodeEnableAt})
	}
	if b != nil && b.AccessStatus == AccessPaused {
		name := b.DisplayName
		if name == "" {
			name = "该品牌"
		}
		return NewFail("BRAND_PAUSED", name+"暂停可租，附近其他品牌的车可以骑", map[string]any{})
	}
	if v.CodeStatus == CodeInvalid || v.Removed {
		return NewFail("CODE_INVALID", "", map[string]any{"codeInvalidAt": v.CodeInvalidAt})
	}
	if b == nil || b.AccessStatus != AccessOn {
		return NewFail("BRAND_NOT_AVAILABLE", "", map[string]any{})
	}
	return nil
}

// DegradeInput 降级开单所需的本地状态。
type DegradeInput struct {
	Active           bool // 本实例正处于降级开单（自身熔断或 ops 宣布）
	DepositUnknown   bool // 故障组件含"押金与免押查询"
	Tightened        bool // 值班人收紧
	Stopped          bool // 值班人停止开新单
	HourCount        int  // 近 1 小时降级单数
	UserOpenDegraded int  // 该用户未结的降级单数
}

// UserCheckInput 用户准入判定的输入。
type UserCheckInput struct {
	User          *User // 快照里的用户；没有为 nil
	LoggedIn      bool
	RidingOrderID string // 该用户进行中的订单
	Version       *Version
	Degrade       DegradeInput
}

// UserCheck 用户能否开新单：未登录 → 已注销 → 冻结 → 已有骑行中 → 未实名或待核验 → 欠费 → 待支付 → 押金。
// 降级时按 BR-28 分三类并执行 6.7 节限额。可开时返回降级准入（常态为 nil）。
func UserCheck(in UserCheckInput) (*Admission, *Fail) {
	if !in.LoggedIn {
		return nil, NewFail("NOT_LOGGED_IN", "", nil)
	}
	u := in.User
	if u == nil {
		u = &User{RealName: RNNone, DepositMode: DepNone}
	}
	if u.Cancelled {
		return nil, NewFail("ACCOUNT_CANCELLED", "", nil)
	}
	if u.Frozen {
		msg := "账户已冻结"
		if u.FreezeReason != "" {
			msg += "：" + u.FreezeReason
		}
		return nil, NewFail("ACCOUNT_FROZEN", msg, map[string]any{"unfreezeHow": u.UnfreezeHow})
	}
	if in.RidingOrderID != "" {
		return nil, NewFail("HAS_RIDING", "", map[string]any{"orderId": in.RidingOrderID})
	}
	rn := u.RealName
	if rn == "" {
		rn = RNNone
	}
	if rn == RNNone || rn == RNFailed {
		return nil, NewFail("NEED_ONBOARDING", "", nil)
	}
	d := in.Degrade
	if rn == RNPending && !d.Active {
		f := NewFail("NEED_ONBOARDING", "完成实名核验后即可骑行", nil)
		f.Next = &Next{Label: "去核验", Action: "verifyRealName"}
		return nil, f
	}
	unpaidID, unpaid := firstUnpaid(u)
	if !d.Active {
		if u.ArrearsTotal > 0 {
			return nil, NewFail("HAS_ARREARS", fmt.Sprintf("你有 %s 未付的骑行费用，补付后即可开锁", Yuan(u.ArrearsTotal)), map[string]any{"amount": u.ArrearsTotal})
		}
		if unpaidID != "" {
			return nil, unpaidFail(unpaidID, unpaid)
		}
		if f := depositCheck(u, in.Version); f != nil {
			return nil, f
		}
		return nil, nil
	}
	// 降级开单（BR-27～BR-30、6.7）
	if d.Stopped {
		return nil, NewFail("DEGRADE_STOPPED", "", nil)
	}
	if u.ArrearsTotal > 0 {
		return nil, NewFail("DEGRADE_REJECT_ARREARS", "", map[string]any{"amount": u.ArrearsTotal})
	}
	if unpaidID != "" {
		return nil, unpaidFail(unpaidID, unpaid)
	}
	if u.DepositMode == DepPaid && u.DepositStatus == DepStRefunding {
		return nil, NewFail("DEGRADE_REJECT_REFUNDING", "", nil)
	}
	var cat string
	switch {
	case rn == RNPending:
		cat = AdmNewDefer
	case d.DepositUnknown:
		cat = AdmDepUnknown
	default:
		if f := depositCheck(u, in.Version); f != nil {
			return nil, f
		}
		cat = AdmOld
	}
	var risk RiskLimits
	if in.Version != nil {
		risk = in.Version.RiskLimits
	}
	tight := cat != AdmOld
	lim := risk.Normal
	if tight {
		lim = risk.Tight
	}
	if tight && (d.Tightened || (risk.HourlyDegradedOrders > 0 && d.HourCount >= risk.HourlyDegradedOrders)) {
		if cat == AdmNewDefer {
			return nil, NewFail("DEGRADE_NEW_USER_UNAVAILABLE", "", nil)
		}
		return nil, NewFail("DEGRADE_PAUSED_TIGHT", "", nil)
	}
	perUser := risk.PerUserOpenDegradedOrders
	if perUser <= 0 {
		perUser = 1
	}
	if d.UserOpenDegraded >= perUser {
		return nil, NewFail("DEGRADE_USER_LIMIT", "", nil)
	}
	adm := &Admission{Category: cat, LimitMinutes: lim.MaxMinutes, LimitAmount: lim.MaxAmount, Line: "费用将稍后收取"}
	switch cat {
	case AdmNewDefer:
		s := fmt.Sprintf("实名核验稍后完成，你可以先骑，本次最长 %d 分钟或 %s。核验未通过将冻结账户并追缴本次费用。", lim.MaxMinutes, Yuan(lim.MaxAmount))
		adm.Notice = &s
	case AdmDepUnknown:
		s := fmt.Sprintf("费用将稍后收取。本次最长 %d 分钟或 %s，到时请尽快还车。", lim.MaxMinutes, Yuan(lim.MaxAmount))
		adm.Notice = &s
	}
	return adm, nil
}

func firstUnpaid(u *User) (string, UnpaidItem) {
	best := ""
	var it UnpaidItem
	for id, x := range u.Unpaid {
		if x.Amount <= 0 {
			continue
		}
		if best == "" || id < best {
			best, it = id, x
		}
	}
	return best, it
}

func unpaidFail(id string, it UnpaidItem) *Fail {
	f := NewFail("UNPAID_ORDER", fmt.Sprintf("上一单还有 %s 未支付，支付后即可开锁", Yuan(it.Amount)), map[string]any{"orderId": id, "amount": it.Amount})
	f.Next.Params = map[string]any{"orderId": id}
	return f
}

func depositCheck(u *User, v *Version) *Fail {
	var amt int64
	if v != nil {
		amt = v.Deposit.Amount
	}
	required := func() *Fail {
		return NewFail("DEPOSIT_REQUIRED", fmt.Sprintf("开通信用免押或缴押金 %s 后即可骑行", Yuan(amt)), map[string]any{"depositAmount": amt})
	}
	switch u.DepositMode {
	case DepFree:
		return nil
	case DepPaid:
		switch u.DepositStatus {
		case DepStPaid:
			return nil
		case DepStShort:
			gap := u.DepositPaid - u.DepositBalance
			if gap < 0 {
				gap = 0
			}
			return NewFail("DEPOSIT_INSUFFICIENT", fmt.Sprintf("押金余额 %s，补缴 %s 或开通免押后即可骑行", Yuan(u.DepositBalance), Yuan(gap)), map[string]any{"topupAmount": gap})
		case DepStRefunding:
			return NewFail("DEPOSIT_REFUNDING", "", nil)
		}
		return required()
	}
	return required()
}

// EstimateRideFee 降级单金额上限的提醒判断用的骑行费估算（起步价 + 超出分钟 × 每分钟价，计费分钟向上取整、至少 1 分钟）。
// 只用于"超限只提醒"，不用于出账；正式计价归 bill。
func EstimateRideFee(v *Version, elapsedSec int64) int64 {
	if v == nil {
		return 0
	}
	mins := (elapsedSec + 59) / 60
	if mins < 1 {
		mins = 1
	}
	extra := mins - int64(v.Ride.StartMinutes)
	if extra < 0 {
		extra = 0
	}
	return v.Ride.StartFee + extra*v.Ride.PerMinute
}

// VersionAt 按时刻取有效版本：未撤销、生效时刻不晚于 t 的最晚一个。
func VersionAt(vs []*Version, t time.Time) *Version {
	var best *Version
	for _, v := range vs {
		if v.Revoked || v.EffectiveAt.After(t) {
			continue
		}
		if best == nil || v.EffectiveAt.After(best.EffectiveAt) {
			best = v
		}
	}
	return best
}

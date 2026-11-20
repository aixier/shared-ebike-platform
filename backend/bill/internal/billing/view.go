package billing

import (
	"fmt"
)

// 账单展示：逐行明细、账单说明、结束页大字、费用状态一句话。对照 原型/shared/mock/views.js 与 rules.js billLines。

// Yuan 把分格式化为"¥4.5"；不足一角的保留两位
func Yuan(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	if c%10 == 0 {
		return fmt.Sprintf("%s¥%d.%d", sign, c/100, (c%100)/10)
	}
	return fmt.Sprintf("%s¥%d.%02d", sign, c/100, c%100)
}

// View 生成展示所需的账单事实
type View struct {
	BilledMinutes      int
	RideFee            int64
	DispatchFee        int64
	MonthCardDeduct    int64
	CouponDeduct       int64
	CouponName         string
	Payable            int64
	ChannelPaid        int64
	DepositDeducted    int64
	Refunded           int64
	InStation          bool
	DistanceToStationM *int
	ZeroUnlock         bool // 超时后才报开锁成功
	PayStatus          string
	PayMethod          string
	RefundStatus       string
}

func (v View) amounts() Amounts {
	return Amounts{Payable: v.Payable, ChannelPaid: v.ChannelPaid, DepositDeducted: v.DepositDeducted, Refunded: v.Refunded}
}

// Line 账单明细的一行
type Line struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Amount int64  `json:"amount"`
	Note   string `json:"note,omitempty"`
	Total  bool   `json:"total,omitempty"`
}

// Lines 依次为骑行费、调度费、月卡抵扣、优惠券、应付、押金抵扣、已退款；金额为 0 的抵扣行不出现
func Lines(v View) []Line {
	ls := []Line{{Key: "rideFee", Label: fmt.Sprintf("骑行费（%d 分钟）", v.BilledMinutes), Amount: v.RideFee}}
	if v.DispatchFee != 0 {
		ls = append(ls, Line{Key: "dispatchFee", Label: "调度费", Amount: v.DispatchFee, Note: "站点外还车"})
	}
	if v.MonthCardDeduct != 0 {
		ls = append(ls, Line{Key: "monthCardDeduct", Label: "月卡抵扣", Amount: -v.MonthCardDeduct})
	}
	if v.CouponDeduct != 0 {
		label := "优惠券"
		if v.CouponName != "" {
			label = "优惠券（" + v.CouponName + "）"
		}
		ls = append(ls, Line{Key: "couponDeduct", Label: label, Amount: -v.CouponDeduct})
	}
	ls = append(ls, Line{Key: "payable", Label: "应付", Amount: v.Payable, Total: true})
	if v.DepositDeducted != 0 {
		ls = append(ls, Line{Key: "depositDeducted", Label: "押金抵扣", Amount: v.DepositDeducted})
	}
	if v.Refunded != 0 {
		ls = append(ls, Line{Key: "refunded", Label: "已退款", Amount: -v.Refunded})
	}
	return ls
}

// Notes 只含费用方面的说明：站外调度费、超时开锁 0 元、押金抵扣
func Notes(v View) []string {
	ns := []string{}
	if !v.InStation && v.DispatchFee > 0 {
		s := "站点外还车，含调度费 " + Yuan(v.DispatchFee)
		if v.DistanceToStationM != nil {
			s += fmt.Sprintf("（距最近站点 %d 米）", *v.DistanceToStationM)
		}
		ns = append(ns, s)
	}
	if v.ZeroUnlock {
		ns = append(ns, "车辆在超时后才开锁，平台已立即结束，本单 0 元")
	}
	if v.PayStatus == "押金抵扣" || v.DepositDeducted > 0 {
		ns = append(ns, "超过一键支付期限未付，已从押金抵扣 "+Yuan(v.DepositDeducted))
	}
	return ns
}

// Headline 结束页大字
func Headline(v View) string {
	a := v.amounts()
	switch {
	case v.PayStatus == "已支付" && v.PayMethod == "免押自动扣款":
		return "已自动扣款 " + Yuan(v.Payable)
	case v.PayStatus == "待支付":
		return "应付 " + Yuan(a.Unpaid())
	case v.PayStatus == "欠费":
		return "欠费 " + Yuan(a.Unpaid())
	case v.PayStatus == "无需支付":
		return "本单无需支付"
	case v.PayStatus == "押金抵扣":
		return "已从押金抵扣 " + Yuan(v.DepositDeducted)
	default:
		return "已支付 " + Yuan(v.ChannelPaid+v.DepositDeducted)
	}
}

// StatusText 费用状态一句话
func StatusText(v View) string {
	if v.ZeroUnlock {
		return "超时未开锁 · 0 元"
	}
	switch v.RefundStatus {
	case "全额退款":
		return "已全额退款"
	case "部分退款":
		return "已部分退款"
	case "原路失败·待人工转账":
		return "退款处理中"
	}
	m := map[string]string{"无需支付": "无需支付", "已支付": "已支付", "待支付": "待支付", "押金抵扣": "已从押金抵扣", "欠费": "欠费", "欠费已补付": "已补付"}
	if s, ok := m[v.PayStatus]; ok {
		return s
	}
	return "已结束"
}

// CanPay 现在能否支付：待支付或欠费、未付大于 0、且没有结果未知的扣款
func CanPay(v View, pending bool) bool {
	return (v.PayStatus == "待支付" || v.PayStatus == "欠费") && !pending && v.amounts().Unpaid() > 0
}

// RefundStatusOf 退款状态：原路失败待人工转账优先；已退款覆盖全部已收（实收为 0）为全额退款
func RefundStatusOf(a Amounts, originalFailed bool) string {
	switch {
	case originalFailed:
		return "原路失败·待人工转账"
	case a.Refunded <= 0:
		return "无"
	case a.NetReceived() <= 0:
		return "全额退款"
	default:
		return "部分退款"
	}
}

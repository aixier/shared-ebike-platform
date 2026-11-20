// Package billing 是 bill 的计价与金额算术：纯函数，不碰数据库与网络。
// 口径见 原型/mockdata/SCHEMA.md 4.1；需求 R-08 BR-14 BR-07 BR-03。金额一律整数分。
package billing

import (
	"sort"
	"time"
)

// RideRule 骑行单价（rule 版本的 ride 字段）
type RideRule struct {
	StartFee     int64 `json:"startFee"`
	StartMinutes int   `json:"startMinutes"`
	PerMinute    int64 `json:"perMinute"`
}

// MonthCardRule 月卡规则
type MonthCardRule struct {
	Price       int64  `json:"price"`
	Days        int    `json:"days"`
	PerOrderCap int64  `json:"perOrderCap"`
	AppliesTo   string `json:"appliesTo"`
}

// CouponTemplate 券模板
type CouponTemplate struct {
	TemplateID string `json:"templateId"`
	Name       string `json:"name"`
	Amount     int64  `json:"amount"`
	Audience   string `json:"audience"`
	ValidDays  int    `json:"validDays"`
	Issue      string `json:"issue"`
	PerUser    int    `json:"perUser"`
}

// Version 规则版本中 bill 用到的部分
type Version struct {
	VersionID   string           `json:"versionId"`
	EffectiveAt time.Time        `json:"effectiveAt"`
	Revoked     bool             `json:"-"`
	Ride        RideRule         `json:"ride"`
	DispatchFee int64            `json:"dispatchFee"`
	MonthCard   MonthCardRule    `json:"monthCard"`
	Coupons     []CouponTemplate `json:"coupons"`
}

// VersionAt 返回时刻 t 有效的版本（撤销的不参与）；没有返回 nil
func VersionAt(vs []Version, t time.Time) *Version {
	var cur *Version
	for i := range vs {
		v := &vs[i]
		if v.Revoked || v.EffectiveAt.After(t) {
			continue
		}
		if cur == nil || v.EffectiveAt.After(cur.EffectiveAt) {
			cur = v
		}
	}
	return cur
}

// BilledMinutes 计费分钟：向上取整，最少 1
func BilledMinutes(durationSec int64) int {
	if durationSec <= 0 {
		return 1
	}
	m := int((durationSec + 59) / 60)
	if m < 1 {
		m = 1
	}
	return m
}

// RideFee 骑行费 = 起步价 + 超出起步时长的分钟 × 每分钟价
func RideFee(r RideRule, minutes int) int64 {
	extra := minutes - r.StartMinutes
	if extra < 0 {
		extra = 0
	}
	return r.StartFee + int64(extra)*r.PerMinute
}

// Card 可用于抵扣的月卡
type Card struct {
	CardID      string
	StartAt     time.Time
	EndAt       time.Time
	PerOrderCap int64
}

// CardFor 返回 startAt ≤ 订单 startAt < endAt 的月卡
func CardFor(cards []Card, startAt time.Time) *Card {
	for i := range cards {
		c := &cards[i]
		if !c.StartAt.After(startAt) && startAt.Before(c.EndAt) {
			return c
		}
	}
	return nil
}

// Coupon 用户的一张券
type Coupon struct {
	CouponID string
	Name     string
	Amount   int64
	IssuedAt time.Time
	ExpireAt time.Time
	Used     bool
}

func minI(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// ChooseCoupon BR-07：在 at 时刻有效、未使用的券中选抵扣额 min(面额, remain) 最大的一张，同额选先到期
func ChooseCoupon(cps []Coupon, at time.Time, remain int64) *Coupon {
	if remain <= 0 {
		return nil
	}
	var cands []*Coupon
	for i := range cps {
		c := &cps[i]
		if c.Used || c.IssuedAt.After(at) || at.After(c.ExpireAt) {
			continue
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		di, dj := minI(cands[i].Amount, remain), minI(cands[j].Amount, remain)
		if di != dj {
			return di > dj
		}
		return cands[i].ExpireAt.Before(cands[j].ExpireAt)
	})
	return cands[0]
}

// Input 计价输入
type Input struct {
	Version     Version
	DurationSec int64
	InStation   bool
	Card        *Card    // 订单开始时刻生效的月卡
	Coupons     []Coupon // 用户的券
	BillAt      time.Time
	Zero        bool // BR-03：超时后才报开锁成功
}

// Result 计价结果
type Result struct {
	BilledMinutes   int
	RideFee         int64
	DispatchFee     int64
	Receivable      int64
	MonthCardID     string
	MonthCardDeduct int64
	CouponID        string
	CouponName      string
	CouponDeduct    int64
	Payable         int64
}

// Price 订单计价：骑行费、调度费、应收；先月卡（只抵骑行费、每单上限）再选一张券（可抵调度费）
func Price(in Input) Result {
	if in.Zero {
		return Result{}
	}
	mins := BilledMinutes(in.DurationSec)
	ride := RideFee(in.Version.Ride, mins)
	var disp int64
	if !in.InStation {
		disp = in.Version.DispatchFee
	}
	r := Result{BilledMinutes: mins, RideFee: ride, DispatchFee: disp, Receivable: ride + disp}
	if in.Card != nil {
		r.MonthCardID = in.Card.CardID
		r.MonthCardDeduct = minI(in.Card.PerOrderCap, ride)
	}
	remain := r.Receivable - r.MonthCardDeduct
	if cp := ChooseCoupon(in.Coupons, in.BillAt, remain); cp != nil {
		r.CouponID, r.CouponName = cp.CouponID, cp.Name
		r.CouponDeduct = minI(cp.Amount, remain)
	}
	r.Payable = r.Receivable - r.MonthCardDeduct - r.CouponDeduct
	return r
}

// Amounts 账单的收付金额
type Amounts struct {
	Payable         int64
	ChannelPaid     int64
	DepositDeducted int64
	Refunded        int64
}

// Unpaid 未付 = 应付 − 渠道已收 − 押金抵扣
func (a Amounts) Unpaid() int64 { return a.Payable - a.ChannelPaid - a.DepositDeducted }

// NetReceived 订单实收 = 渠道已收 + 押金抵扣 − 已退款
func (a Amounts) NetReceived() int64 { return a.ChannelPaid + a.DepositDeducted - a.Refunded }

// Refundable 现在最多可在线退的金额 = 渠道已收 − 已退款
func (a Amounts) Refundable() int64 { return a.ChannelPaid - a.Refunded }

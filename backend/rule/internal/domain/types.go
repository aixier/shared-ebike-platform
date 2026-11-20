// Package domain 是规则配置的纯逻辑：版本内容的结构、录入校验、只传改动项的合并、
// 版本链（状态与失效时刻）计算、事件载荷组装。这里不碰数据库与网络。
//
// 金额一律整数分；比例与门槛为小数。
package domain

import "time"

// Actor 操作人，对应契约 _common.yaml 的 Actor。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// RideRule 骑行单价。
type RideRule struct {
	VehicleType  *string `json:"vehicleType"`
	StartFee     int64   `json:"startFee"`
	StartMinutes int64   `json:"startMinutes"`
	PerMinute    int64   `json:"perMinute"`
	BillingUnit  *string `json:"billingUnit"`
}

// MonthCardRule 月卡规则；Allocation 是月卡收入分到各品牌的规则（R-19，版本内配置项）。
type MonthCardRule struct {
	Price       int64   `json:"price"`
	Days        int64   `json:"days"`
	PerOrderCap int64   `json:"perOrderCap"`
	AppliesTo   *string `json:"appliesTo"`
	Allocation  string  `json:"allocation"`
}

// CouponTemplate 优惠券模板。
type CouponTemplate struct {
	TemplateID string  `json:"templateId"`
	Name       string  `json:"name"`
	Amount     int64   `json:"amount"`
	Audience   *string `json:"audience"`
	ValidDays  int64   `json:"validDays"`
	Issue      *string `json:"issue"`
	PerUser    *int64  `json:"perUser"`
}

// DepositRule 押金。
type DepositRule struct {
	Amount int64 `json:"amount"`
}

// CreditFreeRule 信用免押。
type CreditFreeRule struct {
	Threshold int64    `json:"threshold"`
	Providers []string `json:"providers"`
}

// ServiceFeeRule 服务费：基数口径（Q3，版本内配置项）与各品牌比例。
type ServiceFeeRule struct {
	Basis  string             `json:"basis"`
	Ratios map[string]float64 `json:"ratios"`
}

// DegradeLimit 降级单单笔限额。
type DegradeLimit struct {
	MaxMinutes int64 `json:"maxMinutes"`
	MaxAmount  int64 `json:"maxAmount"`
}

// RiskLimits 风控限额。
type RiskLimits struct {
	PerUserOpenDegradedOrders   int64        `json:"perUserOpenDegradedOrders"`
	Normal                      DegradeLimit `json:"normal"`
	Tight                       DegradeLimit `json:"tight"`
	HourlyDegradedOrders        int64        `json:"hourlyDegradedOrders"`
	DegradeDurationAlertMinutes int64        `json:"degradeDurationAlertMinutes"`
}

// Content 版本内容：一个版本的完整规则快照，一经写入不可修改。
type Content struct {
	Ride        RideRule         `json:"ride"`
	DispatchFee int64            `json:"dispatchFee"`
	MonthCard   MonthCardRule    `json:"monthCard"`
	Coupons     []CouponTemplate `json:"coupons"`
	Deposit     DepositRule      `json:"deposit"`
	CreditFree  CreditFreeRule   `json:"creditFree"`
	ServiceFee  ServiceFeeRule   `json:"serviceFee"`
	RiskLimits  RiskLimits       `json:"riskLimits"`
}

// Version 已录入的规则版本（库里的一行）。状态与失效时刻不存，按时刻算。
type Version struct {
	VersionID   string
	Seq         int
	Name        string
	EffectiveAt time.Time
	CreatedAt   time.Time
	CreatedBy   Actor
	Note        *string
	RevokedAt   *time.Time
	RevokedBy   *Actor
	Content     Content
}

// 版本状态取值（契约 VersionStatus）。
const (
	StatusActive  = "生效中"
	StatusPending = "待生效"
	StatusExpired = "已失效"
	StatusRevoked = "已撤销"
)

// View 版本的对外形状（契约 PricingVersion）。
type View struct {
	VersionID      string  `json:"versionId"`
	Name           string  `json:"name"`
	Status         string  `json:"status"`
	EffectiveAt    string  `json:"effectiveAt"`
	EffectiveUntil *string `json:"effectiveUntil"`
	CreatedAt      string  `json:"createdAt"`
	CreatedBy      Actor   `json:"createdBy"`
	RevokedAt      *string `json:"revokedAt"`
	RevokedBy      *Actor  `json:"revokedBy"`
	Note           *string `json:"note"`
	Content
}

// FieldError 逐项校验错误（契约 Error.errors[]）。
type FieldError struct {
	Row     *int   `json:"row,omitempty"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// displayZone 对外输出时刻用的时区；RFC 3339 带偏移，任何时区都合法，这里统一为 +08:00 便于阅读。
var displayZone = time.FixedZone("+08:00", 8*3600)

// FormatTime 按 RFC 3339（到秒、带时区）输出时刻。
func FormatTime(t time.Time) string { return t.In(displayZone).Format(time.RFC3339) }

func fmtPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := FormatTime(*t)
	return &s
}

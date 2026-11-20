package domain

import (
	"encoding/json"
	"time"
)

// 事件类型与保序键（contracts/events/rule.md）。
const (
	EventVersionPublished = "rule.version.published"
	EventVersionRevoked   = "rule.version.revoked"
	EventParamsChanged    = "rule.params.changed"
	EventAuditRecorded    = "rule.audit.recorded"

	KeyPricing = "pricing"
	KeyParams  = "params"
)

type eventRide struct {
	VehicleType  *string `json:"vehicleType"`
	StartFee     int64   `json:"startFee"`
	StartMinutes int64   `json:"startMinutes"`
	PerMinute    int64   `json:"perMinute"`
}

type eventMonthCard struct {
	Price       int64  `json:"price"`
	Days        int64  `json:"days"`
	PerOrderCap int64  `json:"perOrderCap"`
	Allocation  string `json:"allocation"`
}

// PublishedEvent "规则版本已发布"载荷，字段严格按契约。
type PublishedEvent struct {
	VersionID         string           `json:"versionId"`
	Name              string           `json:"name"`
	EffectiveAt       string           `json:"effectiveAt"`
	PreviousVersionID *string          `json:"previousVersionId"`
	Ride              eventRide        `json:"ride"`
	DispatchFee       int64            `json:"dispatchFee"`
	MonthCard         eventMonthCard   `json:"monthCard"`
	Coupons           []CouponTemplate `json:"coupons"`
	Deposit           DepositRule      `json:"deposit"`
	CreditFree        CreditFreeRule   `json:"creditFree"`
	ServiceFee        ServiceFeeRule   `json:"serviceFee"`
	RiskLimits        RiskLimits       `json:"riskLimits"`
}

// PublishedPayload 组装"规则版本已发布"载荷；prevID 为它生效时失效的上一版本。
func PublishedPayload(v Version, prevID *string) PublishedEvent {
	c := v.Content
	coupons := c.Coupons
	if coupons == nil {
		coupons = []CouponTemplate{}
	}
	return PublishedEvent{
		VersionID:         v.VersionID,
		Name:              v.Name,
		EffectiveAt:       FormatTime(v.EffectiveAt),
		PreviousVersionID: prevID,
		Ride:              eventRide{c.Ride.VehicleType, c.Ride.StartFee, c.Ride.StartMinutes, c.Ride.PerMinute},
		DispatchFee:       c.DispatchFee,
		MonthCard:         eventMonthCard{c.MonthCard.Price, c.MonthCard.Days, c.MonthCard.PerOrderCap, c.MonthCard.Allocation},
		Coupons:           coupons,
		Deposit:           c.Deposit,
		CreditFree:        c.CreditFree,
		ServiceFee:        c.ServiceFee,
		RiskLimits:        c.RiskLimits,
	}
}

// RevokedEvent "规则版本已撤销"载荷。
type RevokedEvent struct {
	VersionID string `json:"versionId"`
	RevokedAt string `json:"revokedAt"`
}

// RevokedPayload 组装"规则版本已撤销"载荷。
func RevokedPayload(id string, at time.Time) RevokedEvent {
	return RevokedEvent{VersionID: id, RevokedAt: FormatTime(at)}
}

// ParamsChangedEvent "平台参数变更"载荷：全部参数的当前值加变更时刻。
type ParamsChangedEvent struct {
	ParamItems
	ChangedAt string `json:"changedAt"`
}

// ParamsChangedPayload 组装"平台参数变更"载荷。
func ParamsChangedPayload(items ParamItems, at time.Time) ParamsChangedEvent {
	return ParamsChangedEvent{ParamItems: items, ChangedAt: FormatTime(at)}
}

// AuditEvent 留痕载荷（contracts/events/README.md 第 3 节）。
type AuditEvent struct {
	Action     string         `json:"action"`
	TargetType string         `json:"targetType"`
	TargetID   string         `json:"targetId"`
	Summary    string         `json:"summary"`
	Reason     *string        `json:"reason"`
	Before     map[string]any `json:"before"`
	After      map[string]any `json:"after"`
}

// ContentDiff 列出两版内容中改动的部分（顶层项）；before 为 nil 时 after 为全部内容。
func ContentDiff(before *Content, after Content) (map[string]any, map[string]any) {
	if before == nil {
		var all map[string]any
		b, _ := json.Marshal(after)
		_ = json.Unmarshal(b, &all)
		return nil, all
	}
	return diffTop(*before, after)
}

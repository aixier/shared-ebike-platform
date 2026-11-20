// Package core 骑行服务本体：订单状态机、准入快照、开锁与结束、发件表与事件消费、HTTP 接口。
// 所有用数据库的代码集中在这个包里（测试只起一次 embedded-postgres）。
package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"bike/ride/internal/geo"
)

// 订单状态。"开锁中"：已建单、等运营商开锁结果，对外也是这个值（契约 OrderStatus）。
const (
	StUnlocking = "开锁中"
	StRiding    = "骑行中"
	StEnded     = "已结束"
	StFailed    = "开锁失败"
	StManual    = "待人工处理"

	UnlockOK   = "成功"
	UnlockFail = "失败"
	UnlockLate = "超时后成功"

	ReasonNoResponse = "车辆无响应"
	ReasonLowBattery = "电量不足"
	ReasonFault      = "车辆故障"
	ReasonOccupied   = "车辆被占用"

	EndPile       = "入桩识别"
	EndUserLock   = "用户锁车"
	EndPage       = "页面还车"
	EndPileNoLock = "入桩未锁·平台请求结束"
	EndLateUnlock = "超时开锁·平台请求结束"
	EndForce      = "客服强制结束"
)

var cst = time.FixedZone("CST", 8*3600)

// ts 统一成北京时间、精确到毫秒（RFC 3339 带时区输出）。
func ts(t time.Time) time.Time { return t.In(cst).Truncate(time.Millisecond) }

func tp(t time.Time) *time.Time { t = ts(t); return &t }
func sp(s string) *string       { return &s }
func bp(b bool) *bool           { return &b }

// Actor 操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// LatLng 开锁位置（契约 UnlockLocation：{lat, lng}，GCJ-02）。
type LatLng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// DegradedMark 降级标记。
type DegradedMark struct {
	EventID             *string    `json:"eventId"`
	Admission           string     `json:"admission"`
	LimitMinutes        int        `json:"limitMinutes"`
	LimitAmount         int64      `json:"limitAmount"`
	OverLimitRemindedAt *time.Time `json:"overLimitRemindedAt"`
	FeeLater            bool       `json:"feeLater"`
	Line                string     `json:"line"`
}

// ForceEnd 强制结束或补录记录。
type ForceEnd struct {
	At          time.Time `json:"at"`
	BilledEndAt time.Time `json:"billedEndAt"`
	By          Actor     `json:"by"`
	Reason      string    `json:"reason"`
	TicketID    *string   `json:"ticketId"`
}

// Pending 骑行中待结束的过程状态（内部字段，不对外）。
type Pending struct {
	PileID            string     `json:"pileId,omitempty"`
	PileStationID     string     `json:"pileStationId,omitempty"`
	PileInAt          *time.Time `json:"pileInAt,omitempty"`
	PageReturn        bool       `json:"pageReturn,omitempty"`
	DispatchConfirmed bool       `json:"dispatchConfirmed,omitempty"`
	EndCause          string     `json:"endCause,omitempty"`
	EndRequestedAt    *time.Time `json:"endRequestedAt,omitempty"`
}

// Order 骑行订单（字段与契约 AdminRideOrder 一致，另有内部字段）。
type Order struct {
	OrderID              string        `json:"orderId"`
	UserID               string        `json:"userId"`
	VehicleID            string        `json:"vehicleId"`
	BrandID              string        `json:"brandId"`
	BrandName            string        `json:"brandName"`
	DisplayName          string        `json:"displayName"`
	PlateNo              string        `json:"plateNo"`
	GovCode              string        `json:"govCode"`
	OperatorID           string        `json:"operatorId"`
	OperatorName         string        `json:"operatorName"`
	Channel              string        `json:"channel"`
	PricingVersionID     string        `json:"pricingVersionId"`
	Status               string        `json:"status"`
	RequestAt            time.Time     `json:"requestAt"`
	UnlockResult         *string       `json:"unlockResult"`
	UnlockFailReason     *string       `json:"unlockFailReason"`
	UnlockReportAt       *time.Time    `json:"unlockReportAt"`
	StartAt              *time.Time    `json:"startAt"`
	StartStationID       *string       `json:"startStationId"`
	StartStationName     *string       `json:"startStationName"`
	StartPileID          *string       `json:"-"`
	StartLocation        *geo.Pt       `json:"startLocation"`
	UnlockLocation       *LatLng       `json:"unlockLocation"`
	EndAt                *time.Time    `json:"endAt"`
	EndMethod            *string       `json:"endMethod"`
	EndStationID         *string       `json:"endStationId"`
	EndStationName       *string       `json:"endStationName"`
	EndPileID            *string       `json:"endPileId"`
	EndLocation          *geo.Pt       `json:"endLocation"`
	InStation            *bool         `json:"inStation"`
	InStationBasis       *string       `json:"inStationBasis"`
	DistanceToStationM   *int          `json:"distanceToStationM"`
	DurationSec          *int64        `json:"durationSec"`
	DistanceKm           *float64      `json:"distanceKm"`
	Degraded             *DegradedMark `json:"degraded"`
	ForceEnd             *ForceEnd     `json:"forceEnd"`
	LockReportAt         *time.Time    `json:"lockReportAt"`
	PileInAt             *time.Time    `json:"pileInAt"`
	ReportReceivedAt     *time.Time    `json:"reportReceivedAt"`
	LateReport           bool          `json:"lateReport"`
	VehicleSideInStation *bool         `json:"vehicleSideInStation"`
	DispatchPromptShown  bool          `json:"dispatchPromptShown"`
	FailNotifiedAt       *time.Time    `json:"failNotifiedAt"`
	AfterFailOrderID     *string       `json:"afterFailOrderId"`
	LockRequest          *string       `json:"lockRequest"`

	// 内部字段
	Pending          *Pending   `json:"_pending,omitempty"`
	UserClaimedEndAt *time.Time `json:"_userClaimedEndAt,omitempty"`
	Rev              int        `json:"_rev"`
	fromJournal      bool
}

// active 是否占着车与人（开锁中、骑行中）。
func (o *Order) active() bool { return o.Status == StUnlocking || o.Status == StRiding }

// adminView 订单事实（AdminRideOrder）。
func (o *Order) adminView() map[string]any {
	b, _ := json.Marshal(o)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k := range m {
		if len(k) > 0 && k[0] == '_' {
			delete(m, k)
		}
	}
	m["status"] = o.Status
	return m
}

// userView 订单的行程部分（RideOrder），不含用户编号与报告细节。
func (o *Order) userView() map[string]any {
	m := o.adminView()
	for _, k := range []string{"userId", "operatorId", "operatorName", "lockReportAt", "pileInAt", "reportReceivedAt", "lateReport", "vehicleSideInStation", "dispatchPromptShown", "failNotifiedAt", "afterFailOrderId", "lockRequest"} {
		delete(m, k)
	}
	return m
}

// Envelope 事件信封。
type Envelope struct {
	EventID    string          `json:"eventId"`
	Type       string          `json:"type"`
	Version    int             `json:"version"`
	OccurredAt time.Time       `json:"occurredAt"`
	Key        string          `json:"key"`
	Actor      Actor           `json:"actor"`
	Payload    json.RawMessage `json:"payload"`
	notBefore  *time.Time
}

func newEventID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "EV" + hex.EncodeToString(b)
}

func newEnvelope(typ, key string, at time.Time, actor Actor, payload any) Envelope {
	b, _ := json.Marshal(payload)
	return Envelope{EventID: newEventID(), Type: typ, Version: 1, OccurredAt: ts(at), Key: key, Actor: actor, Payload: b}
}

var systemActor = Actor{Kind: "系统", ID: "ride", Name: "骑行服务"}

// orderHead 订单头。
func orderHead(o *Order) map[string]any {
	return map[string]any{
		"orderId": o.OrderID, "userId": o.UserID, "vehicleId": o.VehicleID, "plateNo": o.PlateNo,
		"brandId": o.BrandID, "operatorId": o.OperatorID, "channel": nullStr(o.Channel),
		"pricingVersionId": o.PricingVersionID, "requestAt": o.RequestAt,
	}
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func degradedPayload(o *Order) any {
	if o.Degraded == nil {
		return nil
	}
	return map[string]any{"eventId": o.Degraded.EventID, "admission": o.Degraded.Admission, "limitMinutes": o.Degraded.LimitMinutes, "limitAmount": o.Degraded.LimitAmount}
}

func evCreated(o *Order, actor Actor) Envelope {
	p := orderHead(o)
	p["degraded"] = degradedPayload(o)
	return newEnvelope("ride.order.created", o.OrderID, o.RequestAt, actor, p)
}

func evUnlocked(o *Order) Envelope {
	p := orderHead(o)
	p["startAt"] = o.StartAt
	p["startStationId"] = o.StartStationID
	p["startPileId"] = o.StartPileID
	p["startLocation"] = o.StartLocation
	p["degraded"] = degradedPayload(o)
	return newEnvelope("ride.order.unlocked", o.OrderID, *o.StartAt, systemActor, p)
}

func evUnlockFailed(o *Order, at time.Time) Envelope {
	p := orderHead(o)
	p["reason"] = o.UnlockFailReason
	p["failedAt"] = ts(at)
	p["degraded"] = degradedPayload(o)
	return newEnvelope("ride.order.unlock_failed", o.OrderID, at, systemActor, p)
}

func evEnded(o *Order, actor Actor) Envelope {
	p := orderHead(o)
	var fe any
	if o.ForceEnd != nil && o.EndMethod != nil && *o.EndMethod == EndForce {
		fe = map[string]any{"reason": o.ForceEnd.Reason, "ticketId": o.ForceEnd.TicketID}
	}
	inSt := false
	if o.InStation != nil {
		inSt = *o.InStation
	}
	for k, v := range map[string]any{
		"unlockResult": o.UnlockResult, "startAt": o.StartAt, "endAt": o.EndAt, "durationSec": o.DurationSec,
		"distanceKm": o.DistanceKm, "startLocation": o.StartLocation, "endLocation": o.EndLocation,
		"startStationId": o.StartStationID, "endStationId": o.EndStationID, "endStationName": o.EndStationName,
		"endPileId": o.EndPileID, "endMethod": o.EndMethod, "inStation": inSt, "inStationBasis": o.InStationBasis,
		"distanceToStationM": o.DistanceToStationM, "dispatchPromptShown": o.DispatchPromptShown,
		"lockReportAt": o.LockReportAt, "pileInAt": o.PileInAt, "reportReceivedAt": o.ReportReceivedAt,
		"lateReport": o.LateReport, "degraded": degradedPayload(o), "forceEnd": fe,
	} {
		p[k] = v
	}
	return newEnvelope("ride.order.ended", o.OrderID, *o.EndAt, actor, p)
}

func evPendingManual(o *Order, missing []string, at time.Time) Envelope {
	p := orderHead(o)
	p["missing"] = missing
	p["degraded"] = degradedPayload(o)
	p["userClaimedEndAt"] = o.UserClaimedEndAt
	return newEnvelope("ride.order.pending_manual", o.OrderID, at, systemActor, p)
}

func evOverLimit(o *Order, at time.Time) Envelope {
	return newEnvelope("ride.order.over_limit_reminded", o.OrderID, at, systemActor, map[string]any{
		"orderId": o.OrderID, "userId": o.UserID, "limitMinutes": o.Degraded.LimitMinutes, "limitAmount": o.Degraded.LimitAmount, "remindedAt": ts(at),
	})
}

func evAudit(action, targetID, summary string, reason *string, before, after any, actor Actor, at time.Time) Envelope {
	return newEnvelope("ride.audit.recorded", "order:"+targetID, at, actor, map[string]any{
		"action": action, "targetType": "order", "targetId": targetID, "summary": summary, "reason": reason, "before": before, "after": after,
	})
}

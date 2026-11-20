// Package rules 骑行准入与在点判定的纯函数：只读传入的快照，不做 IO。
// 判定顺序、错误码与文案按 contracts/openapi/ride.yaml 与原型 API.md 第 2 节、4.3、4.4。
package rules

import (
	"time"

	"bike/ride/internal/geo"
)

// 枚举值（中文，与契约一致）。
const (
	CodeEnabled  = "已启用"
	CodePending  = "待启用"
	CodeInvalid  = "已失效"
	AccessOn     = "已接入"
	AccessPaused = "暂停可租"

	RNVerified = "已核验"
	RNPending  = "待核验"
	RNFailed   = "未通过"
	RNNone     = "未实名"

	DepFree = "免押"
	DepPaid = "押金"
	DepNone = "未开通"

	DepStPaid      = "已缴"
	DepStShort     = "押金不足"
	DepStRefunding = "退押中"
	DepStRefunded  = "已退"

	AdmOld        = "老用户"
	AdmDepUnknown = "押金状态未知的老用户"
	AdmNewDefer   = "先骑后验新用户"

	CompDB      = "数据库"
	CompBill    = "计费"
	CompPay     = "支付通道"
	CompAcct    = "实名核验"
	CompDeposit = "押金与免押查询"
)

// UnpaidItem 一键支付期限内未付的订单。
type UnpaidItem struct {
	Amount   int64      `json:"amount"`
	Deadline *time.Time `json:"deadline"`
}

// User 准入快照中的用户投影。
type User struct {
	UserID          string                `json:"userId"`
	RealName        string                `json:"realName"`
	Frozen          bool                  `json:"frozen"`
	FreezeReason    string                `json:"freezeReason"`
	UnfreezeHow     string                `json:"unfreezeHow"`
	Cancelled       bool                  `json:"cancelled"`
	DepositMode     string                `json:"depositMode"`
	DepositStatus   string                `json:"depositStatus"`
	DepositBalance  int64                 `json:"depositBalance"`
	DepositPaid     int64                 `json:"depositPaid"`
	RefundRequestID string                `json:"refundRequestId"`
	ArrearsTotal    int64                 `json:"arrearsTotal"`
	Unpaid          map[string]UnpaidItem `json:"unpaid"`
	DuringDegrade   bool                  `json:"duringDegrade"`
	Channel         string                `json:"channel"`
}

// Vehicle 车辆与码的投影。
type Vehicle struct {
	VehicleID     string     `json:"vehicleId"`
	BrandID       string     `json:"brandId"`
	OperatorID    string     `json:"operatorId"`
	PlateNo       string     `json:"plateNo"`
	GovCode       string     `json:"govCode"`
	Model         string     `json:"model"`
	CodeStatus    string     `json:"codeStatus"`
	CodeEnableAt  *time.Time `json:"codeEnableAt"`
	CodeInvalidAt *time.Time `json:"codeInvalidAt"`
	Removed       bool       `json:"removed"`
	Location      *geo.Pt    `json:"location"`
	StationID     string     `json:"stationId"`
}

// Brand 品牌投影。
type Brand struct {
	BrandID           string `json:"brandId"`
	OperatorID        string `json:"operatorId"`
	Name              string `json:"name"`
	DisplayName       string `json:"displayName"`
	OperatorName      string `json:"operatorName"`
	OperatorShortName string `json:"operatorShortName"`
	VehicleType       string `json:"vehicleType"`
	Color             string `json:"color"`
	AccessStatus      string `json:"accessStatus"`
}

// Station 站点投影。
type Station struct {
	StationID string   `json:"stationId"`
	Name      string   `json:"name"`
	Type      string   `json:"type"` // 有桩｜无桩
	Center    geo.Pt   `json:"center"`
	Fence     []geo.Pt `json:"fence"`
	Capacity  int      `json:"capacity"`
	Status    string   `json:"status"` // 启用｜停用（兼容旧值已停用）
}

// Pile 桩投影。
type Pile struct {
	PileID     string `json:"pileId"`
	StationID  string `json:"stationId"`
	No         int    `json:"no"`
	Location   geo.Pt `json:"location"`
	Status     string `json:"status"` // 在线｜离线｜故障
	OccupiedBy string `json:"occupiedBy"`
}

// Limits 一档风控限额。
type Limits struct {
	MaxMinutes int   `json:"maxMinutes"`
	MaxAmount  int64 `json:"maxAmount"`
}

// RiskLimits 规则版本里的风控限额。
type RiskLimits struct {
	PerUserOpenDegradedOrders int    `json:"perUserOpenDegradedOrders"`
	Normal                    Limits `json:"normal"`
	Tight                     Limits `json:"tight"`
	HourlyDegradedOrders      int    `json:"hourlyDegradedOrders"`
}

// Version 规则版本（只取本模块用到的字段）。
type Version struct {
	VersionID   string    `json:"versionId"`
	EffectiveAt time.Time `json:"effectiveAt"`
	Revoked     bool      `json:"revoked"`
	Ride        struct {
		VehicleType  string `json:"vehicleType"`
		StartFee     int64  `json:"startFee"`
		StartMinutes int    `json:"startMinutes"`
		PerMinute    int64  `json:"perMinute"`
	} `json:"ride"`
	DispatchFee int64 `json:"dispatchFee"`
	MonthCard   struct {
		Price       int64 `json:"price"`
		Days        int   `json:"days"`
		PerOrderCap int64 `json:"perOrderCap"`
	} `json:"monthCard"`
	Deposit struct {
		Amount int64 `json:"amount"`
	} `json:"deposit"`
	CreditFree struct {
		Threshold int `json:"threshold"`
	} `json:"creditFree"`
	RiskLimits RiskLimits `json:"riskLimits"`
}

// Params 平台参数（本模块用到的三项）。
type Params struct {
	UnlockReportTimeoutSec int `json:"unlockReportTimeoutSec"`
	NearbyRadiusM          int `json:"nearbyRadiusM"`
	BackfillWindowHours    int `json:"backfillWindowHours"`
}

// DefaultParams 未收到平台参数事件时的缺省值（取原型 pricing.json 的初始值）。
func DefaultParams() Params {
	return Params{UnlockReportTimeoutSec: 30, NearbyRadiusM: 500, BackfillWindowHours: 72}
}

// Next 失败后的一个下一步。
type Next struct {
	Label  string         `json:"label"`
	Action string         `json:"action"`
	Params map[string]any `json:"params,omitempty"`
}

// Fail 不能开锁的原因（结构同统一错误）。
type Fail struct {
	Code    string
	Message string
	Next    *Next
	Extra   map[string]any
}

// Body 转成错误体 map（code、message、charged、next 与附加字段）。
func (f *Fail) Body() map[string]any {
	m := map[string]any{"code": f.Code, "message": f.Message, "charged": false}
	if f.Next != nil {
		m["next"] = f.Next
	}
	for k, v := range f.Extra {
		m[k] = v
	}
	return m
}

// Admission 降级且可开时的准入信息。
type Admission struct {
	Category     string  `json:"category"`
	LimitMinutes int     `json:"limitMinutes"`
	LimitAmount  int64   `json:"limitAmount"`
	Line         string  `json:"line"`
	Notice       *string `json:"notice"`
}

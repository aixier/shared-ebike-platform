package app

import (
	"time"

	"bike/settle/internal/calc"
)

// 对外的 JSON 结构（与 contracts/openapi/settle.yaml 的 components.schemas 一一对应）。

// AdjustmentItemView 对应 AdjustmentItem。
type AdjustmentItemView struct {
	OrderID      string     `json:"orderId"`
	EndAt        time.Time  `json:"endAt"`
	PreviousBase int64      `json:"previousBase"`
	NewBase      int64      `json:"newBase"`
	BaseDelta    int64      `json:"baseDelta"`
	Ratio        calc.Ratio `json:"ratio"`
	FeeDelta     int64      `json:"feeDelta"`
}

// AdjustmentView 对应 Adjustment。
type AdjustmentView struct {
	AdjustmentID string               `json:"adjustmentId"`
	Type         string               `json:"type"`
	BrandID      string               `json:"brandId"`
	BrandName    string               `json:"brandName"`
	Items        []AdjustmentItemView `json:"items"`
	OrderIDs     []string             `json:"orderIds"`
	BaseDelta    int64                `json:"baseDelta"`
	FeeDelta     int64                `json:"feeDelta"`
	Reason       string               `json:"reason"`
	DisputeID    *string              `json:"disputeId"`
	CreatedBy    Actor                `json:"createdBy"`
	CreatedAt    time.Time            `json:"createdAt"`
	ConfirmedBy  *Actor               `json:"confirmedBy"`
	ConfirmedAt  *time.Time           `json:"confirmedAt"`
}

// LineView 对应 SettlementLine。
type LineView struct {
	OrderID              string     `json:"orderId"`
	BrandID              string     `json:"brandId"`
	StartAt              *time.Time `json:"startAt"`
	EndAt                time.Time  `json:"endAt"`
	BilledMinutes        int        `json:"billedMinutes"`
	DistanceKm           *float64   `json:"distanceKm"`
	Degraded             bool       `json:"degraded"`
	PricingVersionID     string     `json:"pricingVersionId"`
	Receivable           int64      `json:"receivable"`
	RefundedAtGeneration int64      `json:"refundedAtGeneration"`
	Base                 int64      `json:"base"`
	Ratio                calc.Ratio `json:"ratio"`
	Fee                  int64      `json:"fee"`
	Remark               *string    `json:"remark"`
}

// RatioGroupView 对应 RatioGroup。
type RatioGroupView struct {
	Ratio             calc.Ratio `json:"ratio"`
	PricingVersionIDs []string   `json:"pricingVersionIds"`
	OrderCount        int        `json:"orderCount"`
	BaseSum           int64      `json:"baseSum"`
	Fee               int64      `json:"fee"`
}

// BrandView 对应 SettlementBrand。
type BrandView struct {
	BrandID             string           `json:"brandId"`
	BrandName           string           `json:"brandName"`
	OrderCount          int              `json:"orderCount"`
	ReceivableSum       int64            `json:"receivableSum"`
	BaseSum             int64            `json:"baseSum"`
	DetailFee           int64            `json:"detailFee"`
	RatioBreakdown      []RatioGroupView `json:"ratioBreakdown"`
	AdjustmentFee       int64            `json:"adjustmentFee"`
	MonthCardOrderCount int              `json:"monthCardOrderCount"`
	MonthCardShare      int64            `json:"monthCardShare"`
}

// MonthCardView 对应 MonthCardAllocation。
type MonthCardView struct {
	PlatformIncome     int64   `json:"platformIncome"`
	AllocationRule     string  `json:"allocationRule"`
	PlatformCardOrders int     `json:"platformCardOrders"`
	Note               *string `json:"note"`
}

// BrandCard 某品牌的月卡单数与分得收入（出单时冻结）。
type BrandCard struct {
	Count int   `json:"count"`
	Share int64 `json:"share"`
}

// SettlementView 对应 Settlement / SettlementDetail（Details 非空时为后者）。
type SettlementView struct {
	SettlementID  string           `json:"settlementId"`
	OperatorID    string           `json:"operatorId"`
	OperatorName  string           `json:"operatorName"`
	Month         string           `json:"month"`
	GeneratedAt   time.Time        `json:"generatedAt"`
	DueDate       string           `json:"dueDate"`
	Status        string           `json:"status"`
	Overdue       bool             `json:"overdue"`
	ReviewedBy    *Actor           `json:"reviewedBy"`
	ReviewedAt    *time.Time       `json:"reviewedAt"`
	SentAt        *time.Time       `json:"sentAt"`
	PaidAt        *time.Time       `json:"paidAt"`
	BankTradeNo   *string          `json:"bankTradeNo"`
	InvoiceStatus *string          `json:"invoiceStatus"`
	Totals        calc.Totals      `json:"totals"`
	MonthCard     MonthCardView    `json:"monthCard"`
	Brands        []BrandView      `json:"brands"`
	Adjustments   []AdjustmentView `json:"adjustments"`
	DetailCount   int              `json:"detailCount"`
	Note          *string          `json:"note"`
	PreviewNote   *string          `json:"previewNote"`
	Details       *[]LineView      `json:"details,omitempty"`
}

// SettlementPointer 对应 SettlementPointer。
type SettlementPointer struct {
	SettlementID string  `json:"settlementId"`
	Kind         string  `json:"kind"`
	AdjustmentID *string `json:"adjustmentId"`
}

// OrderSettlementView 对应 OrderSettlement。
type OrderSettlementView struct {
	OrderID        string             `json:"orderId"`
	OperatorID     string             `json:"operatorId"`
	SettleMonth    *string            `json:"settleMonth"`
	Settlement     *SettlementPointer `json:"settlement"`
	SettlementNote *string            `json:"settlementNote"`
	DisputeIDs     []string           `json:"disputeIds"`
}

// OperatorOrderView 对应 OperatorOrder（不含任何用户字段）。
type OperatorOrderView struct {
	OrderID          string             `json:"orderId"`
	VehicleID        string             `json:"vehicleId"`
	PlateNo          *string            `json:"plateNo"`
	BrandID          string             `json:"brandId"`
	BrandName        string             `json:"brandName"`
	Status           string             `json:"status"`
	RequestAt        *time.Time         `json:"requestAt"`
	UnlockResult     *string            `json:"unlockResult"`
	UnlockFailReason *string            `json:"unlockFailReason"`
	StartAt          *time.Time         `json:"startAt"`
	StartLocation    any                `json:"startLocation"`
	EndAt            *time.Time         `json:"endAt"`
	LockReportAt     *time.Time         `json:"lockReportAt"`
	PileInAt         *time.Time         `json:"pileInAt"`
	EndLocation      any                `json:"endLocation"`
	EndMethod        *string            `json:"endMethod"`
	InStation        *bool              `json:"inStation"`
	InStationBasis   *string            `json:"inStationBasis"`
	EndStationID     *string            `json:"endStationId"`
	EndStationName   *string            `json:"endStationName"`
	DurationSec      *int               `json:"durationSec"`
	BilledMinutes    *int               `json:"billedMinutes"`
	DistanceKm       *float64           `json:"distanceKm"`
	PricingVersionID string             `json:"pricingVersionId"`
	Receivable       *int64             `json:"receivable"`
	DispatchFee      *int64             `json:"dispatchFee"`
	Degraded         bool               `json:"degraded"`
	LateReport       bool               `json:"lateReport"`
	ReportReceivedAt *time.Time         `json:"reportReceivedAt"`
	SettleMonth      *string            `json:"settleMonth"`
	Settlement       *SettlementPointer `json:"settlement"`
	SettlementNote   *string            `json:"settlementNote"`
	DisputeIDs       []string           `json:"disputeIds"`
}

// DisputeCorrection 对应 DisputeCorrection。
type DisputeCorrection struct {
	OrderID   string `json:"orderId"`
	Minutes   int    `json:"minutes"`
	BaseDelta int64  `json:"baseDelta"`
}

// DisputeView 对应 Dispute / DisputeDetail（Orders 非空时为后者）。
type DisputeView struct {
	DisputeID       string               `json:"disputeId"`
	OperatorID      string               `json:"operatorId"`
	OperatorName    string               `json:"operatorName"`
	SettlementID    string               `json:"settlementId"`
	OrderIDs        []string             `json:"orderIds"`
	Field           string               `json:"field"`
	OperatorClaim   string               `json:"operatorClaim"`
	PlatformFact    string               `json:"platformFact"`
	AgentComparison string               `json:"agentComparison"`
	AgentSuggestion *string              `json:"agentSuggestion"`
	Status          string               `json:"status"`
	SubmittedAt     time.Time            `json:"submittedAt"`
	SubmittedBy     Actor                `json:"submittedBy"`
	DecidedAt       *time.Time           `json:"decidedAt"`
	DecidedBy       *Actor               `json:"decidedBy"`
	DecisionReason  *string              `json:"decisionReason"`
	Corrections     []DisputeCorrection  `json:"corrections"`
	AdjustmentID    *string              `json:"adjustmentId"`
	Orders          *[]OperatorOrderView `json:"orders,omitempty"`
}

// Page 分页结果。
type Page[T any] struct {
	Rows     []T `json:"rows"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}

// OperatorOrderSummary 对应 OperatorOrderSummary。
type OperatorOrderSummary struct {
	Count         int   `json:"count"`
	Finished      int   `json:"finished"`
	ReceivableSum int64 `json:"receivableSum"`
	Degraded      int   `json:"degraded"`
}

// OperatorOrderPage 对应 OperatorOrderPage。
type OperatorOrderPage struct {
	Page[OperatorOrderView]
	Summary OperatorOrderSummary `json:"summary"`
}

// Todo 对应 Todo。
type Todo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
	Menu  string `json:"menu"`
}

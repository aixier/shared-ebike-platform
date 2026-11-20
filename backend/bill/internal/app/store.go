package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/bill/internal/billing"
)

// 支付状态、收款方式（swagger PayStatus、PayMethod、collectBy）
const (
	stNone        = "无需支付"
	stPaid        = "已支付"
	stPending     = "待支付"
	stDeposit     = "押金抵扣"
	stArrears     = "欠费"
	stArrearsPaid = "欠费已补付"

	byNone   = "无需支付"
	byCredit = "免押自动扣款"
	byOneTap = "一键支付"

	methodDeposit = "押金抵扣"

	// pending_kind：有一笔结果未定的动钱
	pendCredit = "免押扣款"
	pendOneTap = "一键支付"
	pendDeduct = "押金抵扣"
)

// Bill 账单行
type Bill struct {
	BillID, OrderID, UserID, BrandID, OperatorID, Channel, PricingVersionID string
	StartAt, EndAt                                                          time.Time
	DurationSec                                                             int64
	InStation                                                               bool
	DistanceToStationM                                                      *int
	ZeroUnlock, Degraded                                                    bool
	BilledMinutes                                                           int
	RideFee, DispatchFee, Receivable                                        int64
	MonthCardID                                                             *string
	MonthCardDeduct                                                         int64
	CouponID, CouponName                                                    *string
	CouponDeduct, Payable, ChannelPaid, DepositDeducted, Refunded           int64
	PayStatus                                                               string
	PayMethod                                                               *string
	CollectBy                                                               string
	PayDeadline                                                             *time.Time
	BillPushedAt                                                            time.Time
	ArrearsAt                                                               *time.Time
	ArrearsCause                                                            *string
	ArrearsPaidAt                                                           *time.Time
	PendingKind, PendingPaymentID, RefundFailedPaymentID                    *string
	PendingParams                                                           json.RawMessage // 一键支付未了结的调起参数
	PendingAt                                                               *time.Time
}

const billCols = `bill_id, order_id, user_id, brand_id, operator_id, channel, pricing_version_id, start_at, end_at, duration_sec,
in_station, distance_to_station_m, zero_unlock, degraded, billed_minutes, ride_fee, dispatch_fee, receivable, month_card_id,
month_card_deduct, coupon_id, coupon_name, coupon_deduct, payable, channel_paid, deposit_deducted, refunded, pay_status, pay_method,
collect_by, pay_deadline, bill_pushed_at, arrears_at, arrears_cause, arrears_paid_at, pending_kind, pending_payment_id, refund_failed_payment_id, pending_params, pending_at`

func scanBill(row pgx.Row) (*Bill, error) {
	var b Bill
	var params []byte
	err := row.Scan(&b.BillID, &b.OrderID, &b.UserID, &b.BrandID, &b.OperatorID, &b.Channel, &b.PricingVersionID, &b.StartAt, &b.EndAt, &b.DurationSec,
		&b.InStation, &b.DistanceToStationM, &b.ZeroUnlock, &b.Degraded, &b.BilledMinutes, &b.RideFee, &b.DispatchFee, &b.Receivable, &b.MonthCardID,
		&b.MonthCardDeduct, &b.CouponID, &b.CouponName, &b.CouponDeduct, &b.Payable, &b.ChannelPaid, &b.DepositDeducted, &b.Refunded, &b.PayStatus, &b.PayMethod,
		&b.CollectBy, &b.PayDeadline, &b.BillPushedAt, &b.ArrearsAt, &b.ArrearsCause, &b.ArrearsPaidAt, &b.PendingKind, &b.PendingPaymentID, &b.RefundFailedPaymentID, &params, &b.PendingAt)
	if err != nil {
		return nil, err
	}
	b.PendingParams = params
	return &b, nil
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// errNotFound 没有记录
var errNotFound = errors.New("没有找到")

func getBill(ctx context.Context, q querier, where string, args ...any) (*Bill, error) {
	b, err := scanBill(q.QueryRow(ctx, `SELECT `+billCols+` FROM bills WHERE `+where, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	return b, err
}

func listBills(ctx context.Context, q querier, where string, args ...any) ([]*Bill, error) {
	rows, err := q.Query(ctx, `SELECT `+billCols+` FROM bills WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Bill
	for rows.Next() {
		b, err := scanBill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// saveMoney 写回账单的可变部分
func saveMoney(ctx context.Context, tx pgx.Tx, b *Bill) error {
	_, err := tx.Exec(ctx, `UPDATE bills SET channel_paid=$2, deposit_deducted=$3, refunded=$4, pay_status=$5, pay_method=$6, pay_deadline=$7,
		arrears_at=$8, arrears_cause=$9, arrears_paid_at=$10, pending_kind=$11, pending_payment_id=$12, refund_failed_payment_id=$13,
		pending_params=$14, pending_at=$15, updated_at=now()
		WHERE bill_id=$1`, b.BillID, b.ChannelPaid, b.DepositDeducted, b.Refunded, b.PayStatus, b.PayMethod, b.PayDeadline,
		b.ArrearsAt, b.ArrearsCause, b.ArrearsPaidAt, b.PendingKind, b.PendingPaymentID, b.RefundFailedPaymentID, nullJSON(b.PendingParams), b.PendingAt)
	return err
}

func (b *Bill) amounts() billing.Amounts {
	return billing.Amounts{Payable: b.Payable, ChannelPaid: b.ChannelPaid, DepositDeducted: b.DepositDeducted, Refunded: b.Refunded}
}

func (b *Bill) unpaid() int64 { return b.amounts().Unpaid() }

func (b *Bill) pending() bool { return b.PendingKind != nil }

func (b *Bill) setPending(kind string, paymentID string) {
	if b.PendingKind == nil || *b.PendingKind != kind {
		b.PendingParams, b.PendingAt = nil, nil
	}
	b.PendingKind = &kind
	if paymentID != "" {
		b.PendingPaymentID = &paymentID
	} else {
		b.PendingPaymentID = nil
	}
}

// setIntent 一键支付已向 pay 发起、等用户调起收银台：记流水号、调起参数与时刻
func (b *Bill) setIntent(paymentID string, params json.RawMessage, at time.Time) {
	b.setPending(pendOneTap, paymentID)
	b.PendingParams, b.PendingAt = params, &at
}

func (b *Bill) clearPending() {
	b.PendingKind, b.PendingPaymentID, b.PendingParams, b.PendingAt = nil, nil, nil, nil
}

// nullJSON 空的调起参数存 NULL
func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return []byte(raw)
}

func sp(s string) *string { return &s }

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Profile 用户投影
type Profile struct {
	UserID         string
	DepositMode    *string
	DepositStatus  *string
	DepositBalance *int64
	RealnameStatus *string
	Frozen         bool
	Cancelled      bool
	Channel        *string
}

func getProfile(ctx context.Context, q querier, userID string) (*Profile, error) {
	p := Profile{UserID: userID}
	err := q.QueryRow(ctx, `SELECT deposit_mode, deposit_status, deposit_balance, realname_status, frozen, cancelled, channel FROM user_profiles WHERE user_id=$1`, userID).
		Scan(&p.DepositMode, &p.DepositStatus, &p.DepositBalance, &p.RealnameStatus, &p.Frozen, &p.Cancelled, &p.Channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return &p, nil
	}
	return &p, err
}

func ensureProfile(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO user_profiles (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID)
	return err
}

// collectChannel 收单渠道：按用户的付款入口（微信｜支付宝）
func collectChannel(ch string) string {
	if ch == "支付宝" {
		return "支付宝"
	}
	return "微信支付"
}

// Params 平台参数
type Params struct {
	PayWindowHours    int
	ArrearsFreezeDays int
	Known             bool
}

func getParams(ctx context.Context, q querier) (Params, error) {
	var p Params
	err := q.QueryRow(ctx, `SELECT pay_window_hours, arrears_freeze_days FROM platform_params WHERE id=1`).Scan(&p.PayWindowHours, &p.ArrearsFreezeDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	p.Known = err == nil
	return p, err
}

// versionBody 规则版本载荷中 bill 用到的字段
type versionBody struct {
	VersionID   string                   `json:"versionId"`
	Name        string                   `json:"name"`
	EffectiveAt time.Time                `json:"effectiveAt"`
	Ride        billing.RideRule         `json:"ride"`
	DispatchFee int64                    `json:"dispatchFee"`
	MonthCard   billing.MonthCardRule    `json:"monthCard"`
	Coupons     []billing.CouponTemplate `json:"coupons"`
}

func (v versionBody) toVersion() billing.Version {
	return billing.Version{VersionID: v.VersionID, EffectiveAt: v.EffectiveAt, Ride: v.Ride, DispatchFee: v.DispatchFee, MonthCard: v.MonthCard, Coupons: v.Coupons}
}

func getVersion(ctx context.Context, q querier, id string) (*billing.Version, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT body FROM pricing_versions WHERE version_id=$1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	var vb versionBody
	if err := json.Unmarshal(raw, &vb); err != nil {
		return nil, err
	}
	v := vb.toVersion()
	return &v, nil
}

// currentVersion 现在有效的版本
func currentVersion(ctx context.Context, q querier, now time.Time) (*billing.Version, error) {
	rows, err := q.Query(ctx, `SELECT body FROM pricing_versions WHERE revoked_at IS NULL AND effective_at <= $1 ORDER BY effective_at DESC LIMIT 1`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, errNotFound
	}
	var raw []byte
	if err := rows.Scan(&raw); err != nil {
		return nil, err
	}
	var vb versionBody
	if err := json.Unmarshal(raw, &vb); err != nil {
		return nil, err
	}
	v := vb.toVersion()
	return &v, nil
}

// userArrearsTotal 用户欠费合计
func userArrearsTotal(ctx context.Context, q querier, userID string) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(payable - channel_paid - deposit_deducted),0) FROM bills WHERE user_id=$1 AND pay_status=$2`, userID, stArrears).Scan(&n)
	return n, err
}

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/settle/internal/calc"
)

// 订阅事件的载荷（只取本服务用到的字段；userId 不解析、不落库）。

type rideEnded struct {
	OrderID          string          `json:"orderId"`
	VehicleID        string          `json:"vehicleId"`
	PlateNo          *string         `json:"plateNo"`
	BrandID          string          `json:"brandId"`
	OperatorID       string          `json:"operatorId"`
	PricingVersionID string          `json:"pricingVersionId"`
	RequestAt        *time.Time      `json:"requestAt"`
	UnlockResult     *string         `json:"unlockResult"`
	StartAt          *time.Time      `json:"startAt"`
	EndAt            time.Time       `json:"endAt"`
	DurationSec      *int            `json:"durationSec"`
	DistanceKm       *float64        `json:"distanceKm"`
	StartLocation    json.RawMessage `json:"startLocation"`
	EndLocation      json.RawMessage `json:"endLocation"`
	EndStationID     *string         `json:"endStationId"`
	EndStationName   *string         `json:"endStationName"`
	EndMethod        *string         `json:"endMethod"`
	InStation        *bool           `json:"inStation"`
	InStationBasis   *string         `json:"inStationBasis"`
	LockReportAt     *time.Time      `json:"lockReportAt"`
	PileInAt         *time.Time      `json:"pileInAt"`
	ReportReceivedAt time.Time       `json:"reportReceivedAt"`
	LateReport       bool            `json:"lateReport"`
	Degraded         json.RawMessage `json:"degraded"`
}

type billIssued struct {
	OrderID          string `json:"orderId"`
	BrandID          string `json:"brandId"`
	OperatorID       string `json:"operatorId"`
	PricingVersionID string `json:"pricingVersionId"`
	BilledMinutes    int    `json:"billedMinutes"`
	DispatchFee      int64  `json:"dispatchFee"`
	Receivable       int64  `json:"receivable"`
	MonthCardDeduct  int64  `json:"monthCardDeduct"`
	Payable          int64  `json:"payable"`
}

type rideUnlocked struct {
	OrderID string `json:"orderId"`
}

type rideUnlockFailed struct {
	OrderID string  `json:"orderId"`
	Reason  *string `json:"reason"`
}

type ridePendingManual struct {
	OrderID string          `json:"orderId"`
	Missing json.RawMessage `json:"missing"`
}

type billPaid struct {
	OrderID   string     `json:"orderId"`
	PayStatus string     `json:"payStatus"`
	PaidAt    *time.Time `json:"paidAt"`
}

type billRefunded struct {
	OrderID       string    `json:"orderId"`
	Amount        int64     `json:"amount"`
	RefundedTotal int64     `json:"refundedTotal"`
	NetReceived   int64     `json:"netReceived"`
	Receivable    int64     `json:"receivable"`
	Kind          string    `json:"kind"`
	RefundedAt    time.Time `json:"refundedAt"`
}

type cardPurchased struct {
	CardID string    `json:"cardId"`
	Price  int64     `json:"price"`
	PaidAt time.Time `json:"paidAt"`
}

type versionPublished struct {
	VersionID   string    `json:"versionId"`
	EffectiveAt time.Time `json:"effectiveAt"`
	Ride        struct {
		StartFee     int64 `json:"startFee"`
		StartMinutes int   `json:"startMinutes"`
		PerMinute    int64 `json:"perMinute"`
	} `json:"ride"`
	DispatchFee int64 `json:"dispatchFee"`
	MonthCard   struct {
		Allocation string `json:"allocation"`
	} `json:"monthCard"`
	ServiceFee struct {
		Basis  string                     `json:"basis"`
		Ratios map[string]json.RawMessage `json:"ratios"`
	} `json:"serviceFee"`
}

type versionRevoked struct {
	VersionID string    `json:"versionId"`
	RevokedAt time.Time `json:"revokedAt"`
}

type brandRegistered struct {
	BrandID           string `json:"brandId"`
	OperatorID        string `json:"operatorId"`
	Name              string `json:"name"`
	OperatorName      string `json:"operatorName"`
	OperatorShortName string `json:"operatorShortName"`
}

type operatorExited struct {
	OperatorID string    `json:"operatorId"`
	ExitedAt   time.Time `json:"exitedAt"`
	Reason     *string   `json:"reason"`
}

func jsonOrNull(b json.RawMessage) []byte {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return b
}

func applyProjection(ctx context.Context, tx pgx.Tx, env Envelope) error {
	bad := func(err error) error { return fmt.Errorf("%w: %s %v", errPoison, env.Type, err) }
	switch env.Type {
	case "ride.order.ended":
		var p rideEnded
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.OrderID == "" || p.EndAt.IsZero() {
			return bad(err)
		}
		degraded := len(jsonOrNull(p.Degraded)) > 0
		_, err := tx.Exec(ctx, `INSERT INTO orders(order_id, operator_id, brand_id, vehicle_id, plate_no, pricing_version_id, request_at, ride_ended,
				unlock_result, start_at, end_at, duration_sec, distance_km, start_location, end_location, end_station_id, end_station_name, end_method,
				in_station, in_station_basis, lock_report_at, pile_in_at, report_received_at, late_report, degraded, settle_month)
			VALUES ($1,$2,$3,$4,$5,$6,$7,true,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
			ON CONFLICT (order_id) DO UPDATE SET operator_id=EXCLUDED.operator_id, brand_id=EXCLUDED.brand_id, vehicle_id=EXCLUDED.vehicle_id,
				plate_no=EXCLUDED.plate_no, pricing_version_id=EXCLUDED.pricing_version_id, request_at=EXCLUDED.request_at, ride_ended=true,
				unlock_result=EXCLUDED.unlock_result, start_at=EXCLUDED.start_at, end_at=EXCLUDED.end_at, duration_sec=EXCLUDED.duration_sec,
				distance_km=EXCLUDED.distance_km, start_location=EXCLUDED.start_location, end_location=EXCLUDED.end_location,
				end_station_id=EXCLUDED.end_station_id, end_station_name=EXCLUDED.end_station_name, end_method=EXCLUDED.end_method,
				in_station=EXCLUDED.in_station, in_station_basis=EXCLUDED.in_station_basis, lock_report_at=EXCLUDED.lock_report_at,
				pile_in_at=EXCLUDED.pile_in_at, report_received_at=EXCLUDED.report_received_at, late_report=EXCLUDED.late_report,
				degraded=EXCLUDED.degraded, settle_month=EXCLUDED.settle_month`,
			p.OrderID, p.OperatorID, p.BrandID, p.VehicleID, p.PlateNo, p.PricingVersionID, p.RequestAt,
			p.UnlockResult, p.StartAt, p.EndAt, p.DurationSec, p.DistanceKm, jsonOrNull(p.StartLocation), jsonOrNull(p.EndLocation),
			p.EndStationID, p.EndStationName, p.EndMethod, p.InStation, p.InStationBasis, p.LockReportAt, p.PileInAt,
			p.ReportReceivedAt, p.LateReport, degraded, calc.MonthOf(p.EndAt))
		if err != nil {
			return err
		}
		// 订单已结束：以结束事实为准，开锁状态收口（开锁失败后超时成功的订单也走这里）
		return setUnlock(ctx, tx, p.OrderID, "已结束", nil, nil, true)
	case "ride.order.unlocked":
		var p rideUnlocked
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.OrderID == "" {
			return bad(err)
		}
		return setUnlock(ctx, tx, p.OrderID, "已开锁", nil, nil, true)
	case "ride.order.unlock_failed":
		var p rideUnlockFailed
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.OrderID == "" {
			return bad(err)
		}
		// 已开锁或已结束的订单不被迟到的失败事件降级
		return setUnlock(ctx, tx, p.OrderID, "开锁失败", p.Reason, nil, false)
	case "ride.order.pending_manual":
		var p ridePendingManual
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.OrderID == "" {
			return bad(err)
		}
		return setUnlock(ctx, tx, p.OrderID, "待人工处理", nil, jsonOrNull(p.Missing), false)
	case "bill.bill.paid":
		var p billPaid
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.OrderID == "" {
			return bad(err)
		}
		_, err := tx.Exec(ctx, `INSERT INTO order_pay(order_id, pay_status, paid_at) VALUES ($1,$2,$3)
			ON CONFLICT (order_id) DO UPDATE SET pay_status=EXCLUDED.pay_status, paid_at=EXCLUDED.paid_at`, p.OrderID, p.PayStatus, p.PaidAt)
		return err
	case "bill.bill.issued":
		var p billIssued
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.OrderID == "" {
			return bad(err)
		}
		_, err := tx.Exec(ctx, `INSERT INTO orders(order_id, operator_id, brand_id, pricing_version_id, has_bill, billed_minutes, receivable, dispatch_fee, payable, month_card_deduct)
			VALUES ($1,$2,$3,$4,true,$5,$6,$7,$8,$9)
			ON CONFLICT (order_id) DO UPDATE SET has_bill=true, billed_minutes=EXCLUDED.billed_minutes, receivable=EXCLUDED.receivable,
				dispatch_fee=EXCLUDED.dispatch_fee, payable=EXCLUDED.payable, month_card_deduct=EXCLUDED.month_card_deduct,
				pricing_version_id = CASE WHEN orders.pricing_version_id = '' THEN EXCLUDED.pricing_version_id ELSE orders.pricing_version_id END`,
			p.OrderID, p.OperatorID, p.BrandID, p.PricingVersionID, p.BilledMinutes, p.Receivable, p.DispatchFee, p.Payable, p.MonthCardDeduct)
		return err
	case "bill.bill.refunded":
		var p billRefunded
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.OrderID == "" || p.RefundedAt.IsZero() {
			return bad(err)
		}
		_, err := tx.Exec(ctx, `INSERT INTO refunds(event_id, order_id, amount, refunded_total, net_received, receivable, kind, refunded_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`,
			env.EventID, p.OrderID, p.Amount, p.RefundedTotal, p.NetReceived, p.Receivable, p.Kind, p.RefundedAt)
		return err
	case "bill.card.purchased":
		var p cardPurchased
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.CardID == "" {
			return bad(err)
		}
		_, err := tx.Exec(ctx, `INSERT INTO card_purchases(card_id, price, paid_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, p.CardID, p.Price, p.PaidAt)
		return err
	case "rule.version.published":
		var p versionPublished
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.VersionID == "" {
			return bad(err)
		}
		ratios := map[string]string{}
		for k, raw := range p.ServiceFee.Ratios {
			var r calc.Ratio
			if err := r.UnmarshalJSON(raw); err != nil {
				return bad(err)
			}
			ratios[k] = r.String()
		}
		rb, _ := json.Marshal(ratios)
		// 版本一经发布不可修改：重复到达不覆盖
		_, err := tx.Exec(ctx, `INSERT INTO versions(version_id, effective_at, basis, ratios, start_fee, start_minutes, per_minute, dispatch_fee, allocation)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`,
			p.VersionID, p.EffectiveAt, p.ServiceFee.Basis, rb, p.Ride.StartFee, p.Ride.StartMinutes, p.Ride.PerMinute, p.DispatchFee, p.MonthCard.Allocation)
		return err
	case "rule.version.revoked":
		var p versionRevoked
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.VersionID == "" {
			return bad(err)
		}
		_, err := tx.Exec(ctx, `UPDATE versions SET revoked_at = $2 WHERE version_id = $1`, p.VersionID, p.RevokedAt)
		return err
	case "fleet.brand.registered":
		var p brandRegistered
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.BrandID == "" {
			return bad(err)
		}
		_, err := tx.Exec(ctx, `INSERT INTO brands(brand_id, operator_id, name, operator_name, operator_short_name, updated_at) VALUES ($1,$2,$3,$4,$5,now())
			ON CONFLICT (brand_id) DO UPDATE SET operator_id=EXCLUDED.operator_id, name=EXCLUDED.name, operator_name=EXCLUDED.operator_name,
				operator_short_name=EXCLUDED.operator_short_name, updated_at=now()`,
			p.BrandID, p.OperatorID, p.Name, p.OperatorName, p.OperatorShortName)
		return err
	case "fleet.operator.exited":
		var p operatorExited
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.OperatorID == "" {
			return bad(err)
		}
		_, err := tx.Exec(ctx, `INSERT INTO operator_exits(operator_id, exited_at, reason) VALUES ($1,$2,$3)
			ON CONFLICT (operator_id) DO UPDATE SET exited_at=EXCLUDED.exited_at, reason=EXCLUDED.reason`, p.OperatorID, p.ExitedAt, p.Reason)
		return err
	}
	return nil // 未订阅的类型忽略
}

// setUnlock 记每单的开锁状态。force 为真时无条件覆盖（已开锁、已结束是确定事实）；
// 否则（开锁失败、待人工处理）不覆盖已开锁与已结束，避免迟到事件把已骑行的订单降级。
func setUnlock(ctx context.Context, tx pgx.Tx, orderID, state string, reason *string, missing []byte, force bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO order_unlock(order_id, state, reason, missing, updated_at) VALUES ($1,$2,$3,$4,now())
		ON CONFLICT (order_id) DO UPDATE SET state=EXCLUDED.state, reason=EXCLUDED.reason, missing=EXCLUDED.missing, updated_at=now()
		WHERE $5 OR order_unlock.state NOT IN ('已开锁','已结束')`, orderID, state, reason, missing, force)
	return err
}

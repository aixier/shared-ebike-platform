package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/opgw/internal/clients/ride"
)

// rideEventIn 运营商报告的骑行事件（M-03）。
type rideEventIn struct {
	EventID    string `json:"eventId"`
	Type       string `json:"type"`
	OrderID    string `json:"orderId"`
	PlateNo    string `json:"plateNo"`
	OccurredAt string `json:"occurredAt"`
	Unlock     *struct {
		Result     string    `json:"result"`
		FailReason string    `json:"failReason"`
		Location   []float64 `json:"location"`
	} `json:"unlock"`
	Lock *struct {
		Location    []float64 `json:"location"`
		InStation   *bool     `json:"inStation"`
		DurationSec *int64    `json:"durationSec"`
		DistanceKm  *float64  `json:"distanceKm"`
	} `json:"lock"`
}

var failReasons = map[string]bool{"电量不足": true, "车辆故障": true, "车辆被占用": true, "车辆无响应": true}

// validate 报文校验，返回给运营商的原因。
func (e *rideEventIn) validate() (time.Time, string) {
	if e.EventID == "" || len(e.EventID) > 64 {
		return time.Time{}, "eventId 必填，最长 64 个字符"
	}
	if e.OrderID == "" || e.PlateNo == "" {
		return time.Time{}, "orderId、plateNo 必填"
	}
	at, err := time.Parse(time.RFC3339, e.OccurredAt)
	if err != nil {
		return time.Time{}, "occurredAt 须为带时区的 RFC 3339 时刻"
	}
	switch e.Type {
	case "开锁结果":
		if e.Unlock == nil || (e.Unlock.Result != "成功" && e.Unlock.Result != "失败") {
			return at, "type=开锁结果 时 unlock.result 必填，取值 成功｜失败"
		}
		if e.Unlock.Result == "失败" && !failReasons[e.Unlock.FailReason] {
			return at, "开锁失败时 unlock.failReason 必填，取值 电量不足｜车辆故障｜车辆被占用｜车辆无响应"
		}
		if e.Unlock.Location != nil && len(e.Unlock.Location) != 2 {
			return at, "unlock.location 须为 [经度, 纬度]"
		}
	case "锁车":
		if e.Lock == nil || len(e.Lock.Location) != 2 || e.Lock.InStation == nil {
			return at, "type=锁车 时 lock.location（[经度, 纬度]）与 lock.inStation 必填"
		}
		if (e.Lock.DurationSec != nil && *e.Lock.DurationSec < 0) || (e.Lock.DistanceKm != nil && *e.Lock.DistanceKm < 0) {
			return at, "lock.durationSec、lock.distanceKm 不能为负"
		}
	default:
		return at, "type 取值 开锁结果｜锁车"
	}
	return at, ""
}

type rideEventAck struct {
	Received   bool   `json:"received"`
	EventID    string `json:"eventId"`
	ReceivedAt string `json:"receivedAt"`
	Duplicate  bool   `json:"duplicate"`
}

func deliveryKey(credID, eventID string) string {
	h := sha256.Sum256([]byte(credID + "\n" + eventID))
	return "re-" + hex.EncodeToString(h[:20])
}

func (a *App) handleRideEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	o := openFrom(ctx)
	var e rideEventIn
	if err := json.Unmarshal(o.Body, &e); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID", "报文不是合法 JSON")
		return
	}
	occurred, msg := e.validate()
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "INVALID", msg)
		return
	}
	now := a.Now()

	// 重复到达：只答复，不再生效
	var firstAt time.Time
	err := a.DB.QueryRow(ctx, `SELECT received_at FROM ride_events WHERE credential_id=$1 AND event_id=$2`, o.Cred.CredentialID, e.EventID).Scan(&firstAt)
	if err == nil {
		writeJSON(w, http.StatusOK, rideEventAck{Received: true, EventID: e.EventID, ReceivedAt: rfc3339(firstAt), Duplicate: true})
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		serverBusy(w)
		return
	}

	// 订单必须是本平台向这家运营商发过开锁请求的
	var opID, brandID, plate string
	err = a.DB.QueryRow(ctx, `SELECT operator_id, brand_id, plate_no FROM commands WHERE order_id=$1 AND kind='开锁'`, e.OrderID).Scan(&opID, &brandID, &plate)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "订单号不存在，请核对，不要重试")
		return
	} else if err != nil {
		serverBusy(w)
		return
	}
	if !o.Cred.covers(opID) {
		writeErr(w, http.StatusForbidden, "SCOPE_DENIED", "订单不属于这份凭证名下的运营商")
		return
	}
	if plate != e.PlateNo {
		writeErr(w, http.StatusBadRequest, "INVALID", "车号与订单的开锁请求不一致")
		return
	}
	var windowH, unlockTimeout int
	if err := a.DB.QueryRow(ctx, `SELECT backfill_window_hours, unlock_report_timeout_sec FROM params WHERE id=1`).Scan(&windowH, &unlockTimeout); err != nil {
		serverBusy(w)
		return
	}
	if now.Sub(occurred) > time.Duration(windowH)*time.Hour {
		writeErr(w, http.StatusBadRequest, "INVALID", "已超出补报窗口，平台不再接收")
		return
	}

	orderID := e.OrderID
	d := ride.Event{
		OperatorEventID: e.EventID, OrderID: &orderID, PlateNo: e.PlateNo, BrandID: brandID, OperatorID: opID,
		Kind: e.Type, ReceivedAt: rfc3339(now),
		Late: now.Sub(occurred) > time.Duration(unlockTimeout)*time.Second,
	}
	if e.Type == "开锁结果" {
		u := &ride.Unlock{Result: e.Unlock.Result, At: e.OccurredAt}
		// 运营商报告的失败原因原样转送（含"车辆无响应"）
		if e.Unlock.Result == "失败" {
			fr := e.Unlock.FailReason
			u.Reason = &fr
		}
		d.Unlock = u
	} else {
		var ended int
		if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM commands WHERE order_id=$1 AND kind LIKE '结束骑行:%'`, e.OrderID).Scan(&ended); err != nil {
			serverBusy(w)
			return
		}
		d.Lock = &ride.Lock{At: e.OccurredAt, Location: e.Lock.Location, VehicleSideInStation: e.Lock.InStation,
			DurationSec: e.Lock.DurationSec, DistanceKm: e.Lock.DistanceKm, Manual: ended == 0}
	}
	payload, _ := json.Marshal(d)
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		serverBusy(w)
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO ride_events (credential_id, event_id, order_id, delivery, received_at, next_attempt_at)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, o.Cred.CredentialID, e.EventID, e.OrderID, payload, now, now.Add(10*time.Second))
	if err != nil {
		serverBusy(w)
		return
	}
	if tag.RowsAffected() == 0 { // 并发重复到达
		writeJSON(w, http.StatusOK, rideEventAck{Received: true, EventID: e.EventID, ReceivedAt: rfc3339(now), Duplicate: true})
		return
	}
	// 开锁失败：首次收到时发"运营商报告开锁失败"，带运营商报告的原因原样取值
	if e.Type == "开锁结果" && e.Unlock.Result == "失败" {
		if err := a.enqueue(ctx, tx, "opgw.unlock.failed", e.OrderID, systemActor, map[string]any{
			"orderId": e.OrderID, "operatorId": opID, "brandId": brandID, "plateNo": e.PlateNo, "operatorEventId": e.EventID,
			"reason": e.Unlock.FailReason, "failedAt": e.OccurredAt, "late": d.Late,
		}); err != nil {
			serverBusy(w)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		serverBusy(w)
		return
	}
	// 同步送达 ride（重试任务 10 秒后才接手，避免两边同时送）；失败留给重试任务，不影响给运营商的确认
	a.deliverOne(ctx, o.Cred.CredentialID, e.EventID, d, 0)
	writeJSON(w, http.StatusOK, rideEventAck{Received: true, EventID: e.EventID, ReceivedAt: rfc3339(now), Duplicate: false})
}

func (a *App) deliverOne(ctx context.Context, credID, eventID string, d ride.Event, attempts int) bool {
	err := a.Ride.Deliver(ctx, deliveryKey(credID, eventID), d)
	if err == nil {
		_, _ = a.DB.Exec(ctx, `UPDATE ride_events SET delivered_at=$3, attempts=attempts+1, last_error=NULL WHERE credential_id=$1 AND event_id=$2`, credID, eventID, a.Now())
		return true
	}
	backoff := time.Duration(1<<min(attempts, 8)) * time.Second // 1 秒起，翻倍，最长约 4 分钟
	_, _ = a.DB.Exec(ctx, `UPDATE ride_events SET attempts=attempts+1, last_error=$3, next_attempt_at=$4 WHERE credential_id=$1 AND event_id=$2`,
		credID, eventID, err.Error(), a.Now().Add(backoff))
	a.Log.Warn("骑行事件未送达 ride，稍后重试", "eventId", eventID, "err", err.Error())
	return false
}

// RetryDeliveries 重试未送达 ride 的骑行事件；多实例下用行锁避免重复送。返回送达条数。
func (a *App) RetryDeliveries(ctx context.Context) (int, error) {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT credential_id, event_id, delivery, attempts FROM ride_events
		WHERE delivered_at IS NULL AND next_attempt_at <= $1 ORDER BY received_at LIMIT 100 FOR UPDATE SKIP LOCKED`, a.Now())
	if err != nil {
		return 0, err
	}
	type item struct {
		cred, eid string
		d         ride.Event
		attempts  int
	}
	var items []item
	for rows.Next() {
		var it item
		var raw []byte
		if err := rows.Scan(&it.cred, &it.eid, &raw, &it.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		_ = json.Unmarshal(raw, &it.d)
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		err := a.Ride.Deliver(ctx, deliveryKey(it.cred, it.eid), it.d)
		if err == nil {
			if _, err := tx.Exec(ctx, `UPDATE ride_events SET delivered_at=$3, attempts=attempts+1, last_error=NULL WHERE credential_id=$1 AND event_id=$2`, it.cred, it.eid, a.Now()); err != nil {
				return n, err
			}
			n++
			continue
		}
		backoff := time.Duration(1<<min(it.attempts, 8)) * time.Second
		if _, err2 := tx.Exec(ctx, `UPDATE ride_events SET attempts=attempts+1, last_error=$3, next_attempt_at=$4 WHERE credential_id=$1 AND event_id=$2`,
			it.cred, it.eid, err.Error(), a.Now().Add(backoff)); err2 != nil {
			return n, err2
		}
	}
	return n, tx.Commit(ctx)
}

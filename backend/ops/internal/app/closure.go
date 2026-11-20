package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/ops/internal/clients/pay"
	"bike/ops/internal/clients/ride"
	"bike/ops/internal/clients/svccall"
	"bike/ops/internal/httpx"
)

// orderRow 订单投影的一行。
type orderRow struct {
	OrderID             string
	UserID              *string
	BrandID             *string
	PricingVersionID    *string
	RequestAt           *time.Time
	Status              string
	StartAt             *time.Time
	UnlockFailedAt      *time.Time
	EndAt               *time.Time
	Degraded            bool
	DegradeEventID      *string
	Admission           *string
	OverLimitRemindedAt *time.Time
	ManualMissing       []string
	Receivable          *int64
	Payment             string
	PaymentID           *string
	PaymentUnknownSince *time.Time
	ClosedAt            *time.Time
	Note                *string
}

const orderCols = `order_id, user_id, brand_id, pricing_version_id, request_at, status, start_at, unlock_failed_at, end_at,
	degraded, degrade_event_id, admission, over_limit_reminded_at, manual_missing, receivable, payment, payment_id,
	payment_unknown_since, closed_at, note`

func scanOrder(row pgx.Row) (orderRow, error) {
	var o orderRow
	err := row.Scan(&o.OrderID, &o.UserID, &o.BrandID, &o.PricingVersionID, &o.RequestAt, &o.Status, &o.StartAt, &o.UnlockFailedAt, &o.EndAt,
		&o.Degraded, &o.DegradeEventID, &o.Admission, &o.OverLimitRemindedAt, &o.ManualMissing, &o.Receivable, &o.Payment, &o.PaymentID,
		&o.PaymentUnknownSince, &o.ClosedAt, &o.Note)
	return o, err
}

func queryOrders(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]orderRow, error) {
	rows, err := tx.Query(ctx, `SELECT `+orderCols+` FROM orders `+sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []orderRow
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ClosureItem 一笔降级单的闭合项（ops.yaml ClosureItem）。
type ClosureItem struct {
	OrderID      string   `json:"orderId"`
	EventID      *string  `json:"eventId"`
	UnlockResult bool     `json:"unlockResult"`
	LockFact     bool     `json:"lockFact"`
	Payment      string   `json:"payment"`
	Status       string   `json:"status"`
	Missing      []string `json:"missing"`
	ClosedAt     *string  `json:"closedAt"`
	Note         *string  `json:"note"`
}

// 收款结果中算作"已有结果"的取值。
var paymentSettled = map[string]bool{"已收款": true, "已推送一键支付": true, "已转欠费": true}

// closureFacts 三项事实与缺项（BR-32）。开锁失败的单不计费，不需要锁车与收款。
func closureFacts(o orderRow) (unlock, lock, paid bool, missing []string) {
	failed := o.UnlockFailedAt != nil && o.StartAt == nil
	unlock = o.StartAt != nil || o.UnlockFailedAt != nil || o.EndAt != nil
	lock = o.EndAt != nil || failed
	paid = paymentSettled[o.Payment] || failed
	missing = []string{}
	if !unlock {
		missing = append(missing, "开锁结果")
	}
	if !lock {
		missing = append(missing, "锁车事实")
	}
	if !paid {
		missing = append(missing, "收款结果")
	}
	return
}

// closureStatus 闭合状态：三项齐全为已闭合；ride 报待人工或收款结果未知超时为待人工；骑行中；其余待闭合。
func (a *App) closureStatus(o orderRow, now time.Time) string {
	_, _, _, missing := closureFacts(o)
	if len(missing) == 0 {
		return "已闭合"
	}
	if o.Status == "待人工处理" {
		return "待人工"
	}
	if o.Payment == "结果未知" && o.PaymentUnknownSince != nil &&
		now.Sub(*o.PaymentUnknownSince) >= time.Duration(a.Cfg.PaymentUnknownTimeoutMinutes)*time.Minute {
		return "待人工"
	}
	if o.Status == "骑行中" {
		return "骑行中"
	}
	return "待闭合"
}

func (a *App) closureItem(o orderRow, now time.Time) ClosureItem {
	unlock, lock, _, missing := closureFacts(o)
	note := o.Note
	if note == nil && o.UnlockFailedAt != nil && o.StartAt == nil {
		s := "开锁失败，不计费"
		note = &s
	}
	return ClosureItem{
		OrderID: o.OrderID, EventID: o.DegradeEventID, UnlockResult: unlock, LockFact: lock, Payment: o.Payment,
		Status: a.closureStatus(o, now), Missing: missing, ClosedAt: a.fmtTimePtr(o.ClosedAt), Note: note,
	}
}

// refreshClosure 降级单三项齐全时记闭合时刻，并刷新所属降级事件的闭合时间线。
func (a *App) refreshClosure(ctx context.Context, tx pgx.Tx, orderID string) error {
	o, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM orders WHERE order_id = $1`, orderID))
	if err != nil {
		return err
	}
	if !o.Degraded {
		return nil
	}
	_, _, _, missing := closureFacts(o)
	if len(missing) == 0 && o.ClosedAt == nil {
		if _, err := tx.Exec(ctx, `UPDATE orders SET closed_at = $2 WHERE order_id = $1`, orderID, a.now()); err != nil {
			return err
		}
	}
	if o.DegradeEventID != nil {
		return a.refreshEventClosureTimeline(ctx, tx, *o.DegradeEventID)
	}
	return nil
}

// refreshEventClosureTimeline 事件已退出后：全部闭合记"全部闭合"；只剩待人工记"部分闭合"。各记一次。
func (a *App) refreshEventClosureTimeline(ctx context.Context, tx pgx.Tx, eventID string) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM degrade_events WHERE event_id = $1`, eventID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if status != "已退出" {
		return nil
	}
	orders, err := queryOrders(ctx, tx, `WHERE degrade_event_id = $1`, eventID)
	if err != nil {
		return err
	}
	now := a.now()
	closed, manual, other := 0, 0, 0
	for _, o := range orders {
		switch a.closureStatus(o, now) {
		case "已闭合":
			closed++
		case "待人工":
			manual++
		default:
			other++
		}
	}
	has := func(action string) bool {
		var n int
		_ = tx.QueryRow(ctx, `SELECT count(*) FROM degrade_timeline WHERE event_id = $1 AND action = $2`, eventID, action).Scan(&n)
		return n > 0
	}
	if len(orders) > 0 && closed == len(orders) && !has("全部闭合") {
		return a.addTimeline(ctx, tx, eventID, SystemActor, "全部闭合", fmt.Sprintf("降级单 %d 笔三项事实齐全，全部闭合", len(orders)))
	}
	if manual > 0 && other == 0 && !has("部分闭合") && !has("全部闭合") {
		return a.addTimeline(ctx, tx, eventID, SystemActor, "部分闭合", fmt.Sprintf("已闭合 %d 笔，%d 笔缺项待人工处理", closed, manual))
	}
	return nil
}

// SweepClosures 定时刷新已退出且未全部闭合的降级事件（收款结果未知超时后转待人工需要据时间判断）。
func (a *App) SweepClosures(ctx context.Context) error {
	return a.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT event_id FROM degrade_events e WHERE status = '已退出'
			AND NOT EXISTS (SELECT 1 FROM degrade_timeline t WHERE t.event_id = e.event_id AND t.action = '全部闭合')`)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if err := a.refreshEventClosureTimeline(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListClosures 降级单闭合情况；两个参数都不传时返回全部未闭合的单。按开单时刻新到旧。
func (a *App) ListClosures(ctx context.Context, eventID, status string) ([]ClosureItem, error) {
	out := []ClosureItem{}
	err := a.read(ctx, func(tx pgx.Tx) error {
		cond := `WHERE degraded AND ($1 = '' OR degrade_event_id = $1)`
		if eventID == "" && status == "" {
			cond += ` AND closed_at IS NULL`
		}
		orders, err := queryOrders(ctx, tx, cond+` ORDER BY request_at DESC NULLS LAST, order_id DESC`, eventID)
		if err != nil {
			return err
		}
		now := a.now()
		for _, o := range orders {
			it := a.closureItem(o, now)
			if status != "" && it.Status != status {
				continue
			}
			out = append(out, it)
		}
		return nil
	})
	return out, err
}

// HandleClosureRequest 人工补齐缺项。
type HandleClosureRequest struct {
	Action    string  `json:"action"`
	EndAt     *string `json:"endAt"`
	StationID *string `json:"stationId"`
	Result    string  `json:"result"`
	Reason    string  `json:"reason"`
}

// derivedKey 由页面幂等键派生调用别的服务时的幂等键（≤64 字符）；页面没给时每次新生成。
func derivedKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return "ops-" + hex.EncodeToString(h.Sum(nil))[:40]
}

func randomKey() string { return newEventID() }

// remoteErr 把对方错误映射成本接口的错误码。
func remoteErr(err error, notFoundMsg string) error {
	var re *svccall.RemoteError
	if errors.As(err, &re) {
		msg := re.Message
		switch {
		case re.Status == 404:
			if msg == "" {
				msg = notFoundMsg
			}
			return httpx.NotFound(msg)
		case re.Code == "REASON_REQUIRED":
			return httpx.ReasonRequired()
		case re.Status == 400:
			if msg == "" {
				msg = "请求有误"
			}
			return httpx.Invalid(msg)
		case re.Status == 409:
			if msg == "" {
				msg = "当前状态不允许该操作"
			}
			return httpx.Conflict(msg)
		}
	}
	return httpx.ServerBusy("相关服务暂时不可用，请稍后重试")
}

// HandleClosure 人工补齐降级单缺项：补录锁车事实调 ride；确认收款结果调 pay。
func (a *App) HandleClosure(ctx context.Context, actor Actor, orderID string, req HandleClosureRequest, idemKey string) (ClosureItem, error) {
	if !httpx.ValidReason(req.Reason) {
		return ClosureItem{}, httpx.ReasonRequired()
	}
	var o orderRow
	err := a.read(ctx, func(tx pgx.Tx) error {
		var err error
		o, err = scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM orders WHERE order_id = $1`, orderID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !o.Degraded) {
		return ClosureItem{}, httpx.NotFound("没有找到该降级单")
	}
	if err != nil {
		return ClosureItem{}, err
	}
	if o.ClosedAt != nil {
		return ClosureItem{}, httpx.Conflict("该订单已闭合，不需要人工处理")
	}
	key := randomKey()
	if idemKey != "" {
		key = derivedKey("closure", actor.ID, orderID, idemKey)
	}
	now := a.now()
	var note string
	switch req.Action {
	case "补录锁车事实":
		_, lock, _, _ := closureFacts(o)
		if lock {
			return ClosureItem{}, httpx.Conflict("该订单已有锁车事实")
		}
		if req.EndAt == nil {
			return ClosureItem{}, httpx.Invalid("请填写锁车时刻")
		}
		endAt, err := time.Parse(time.RFC3339, *req.EndAt)
		if err != nil {
			return ClosureItem{}, httpx.Invalid("锁车时刻格式不对")
		}
		if endAt.After(now) || (o.StartAt != nil && !endAt.After(*o.StartAt)) {
			return ClosureItem{}, httpx.Invalid("锁车时刻须在开锁之后、现在之前")
		}
		if _, err := a.Ride.BackfillLockFact(ctx, orderID, key, ride.LockFactRequest{
			EndAt: a.fmtTime(endAt), StationID: req.StationID, Reason: req.Reason, Actor: ride.Actor(svcActor(actor)),
		}); err != nil {
			return ClosureItem{}, remoteErr(err, "骑行侧没有找到该订单")
		}
		note = "人工补录锁车事实：" + req.Reason
		err = a.tx(ctx, func(tx pgx.Tx) error {
			// ride 随后发"订单已结束"，这里先记下锁车事实，事件到达时幂等覆盖
			if _, err := tx.Exec(ctx, `UPDATE orders SET end_at = COALESCE(end_at, $2), status = '已结束', manual_missing = NULL, note = $3, updated_at = now()
				WHERE order_id = $1`, orderID, endAt, note); err != nil {
				return err
			}
			return a.afterHandle(ctx, tx, actor, orderID, "补录锁车事实", req.Reason)
		})
	case "确认收款结果":
		if o.Payment != "结果未知" || o.PaymentID == nil {
			return ClosureItem{}, httpx.Conflict("该订单没有结果未知的扣款")
		}
		if req.Result != "已收款" && req.Result != "未收款" {
			return ClosureItem{}, httpx.Invalid("请选择收款结果：已收款或未收款")
		}
		res, err := a.Pay.ConfirmPayment(ctx, *o.PaymentID, key, pay.ConfirmRequest{Result: req.Result, Reason: req.Reason, Actor: pay.Actor(svcActor(actor))})
		if err != nil {
			return ClosureItem{}, remoteErr(err, "收付款侧没有找到该流水")
		}
		switch res.Result {
		case "成功":
		case "结果未知":
			return ClosureItem{}, httpx.ServerBusy("收款结果仍在确认中，请稍后重试")
		default:
			msg := "收付款侧未接受该确认"
			if res.FailReason != nil && *res.FailReason != "" {
				msg = *res.FailReason
			}
			return ClosureItem{}, httpx.Conflict(msg)
		}
		payment := "已收款"
		if req.Result == "未收款" {
			payment = "已转欠费" // 未扣到款由 bill 转欠费追缴
		}
		note = "人工核实收款结果：" + req.Result + "（" + req.Reason + "）"
		err = a.tx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE orders SET payment = $2, payment_unknown_since = NULL, note = $3, updated_at = now() WHERE order_id = $1`,
				orderID, payment, note); err != nil {
				return err
			}
			return a.afterHandle(ctx, tx, actor, orderID, "确认收款结果："+req.Result, req.Reason)
		})
	default:
		return ClosureItem{}, httpx.Invalid("处理方式只能是补录锁车事实或确认收款结果")
	}
	if err != nil {
		return ClosureItem{}, err
	}
	var item ClosureItem
	err = a.read(ctx, func(tx pgx.Tx) error {
		o, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM orders WHERE order_id = $1`, orderID))
		if err != nil {
			return err
		}
		item = a.closureItem(o, a.now())
		return nil
	})
	return item, err
}

func (a *App) afterHandle(ctx context.Context, tx pgx.Tx, actor Actor, orderID, what, reason string) error {
	if err := a.refreshClosure(ctx, tx, orderID); err != nil {
		return err
	}
	r := reason
	return a.audit(ctx, tx, actor, "闭合订单", "order", orderID, "降级单 "+orderID+" 人工"+what, &r, nil, nil)
}

// eventClosure 事件级闭合汇总。
type EventClosureSummary struct {
	Status              string         `json:"status"`
	Closed              int            `json:"closed"`
	PendingOrderIDs     []string       `json:"pendingOrderIds"`
	PaymentResults      map[string]int `json:"paymentResults"`
	NewUserVerification map[string]int `json:"newUserVerification"`
}

func (a *App) eventClosure(ctx context.Context, tx pgx.Tx, active bool, orders []orderRow, now time.Time) (EventClosureSummary, error) {
	s := EventClosureSummary{PendingOrderIDs: []string{}, PaymentResults: map[string]int{}, NewUserVerification: map[string]int{}}
	newUsers := map[string]bool{}
	for _, o := range orders {
		if a.closureStatus(o, now) == "已闭合" {
			s.Closed++
		} else {
			s.PendingOrderIDs = append(s.PendingOrderIDs, o.OrderID)
		}
		s.PaymentResults[o.Payment]++
		if o.Admission != nil && *o.Admission == "先骑后验新用户" && o.UserID != nil {
			newUsers[*o.UserID] = true
		}
	}
	sort.Strings(s.PendingOrderIDs)
	for u := range newUsers {
		var st string
		err := tx.QueryRow(ctx, `SELECT status FROM users_realname WHERE user_id = $1`, u).Scan(&st)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return s, err
		}
		switch st {
		case "已核验":
			s.NewUserVerification["通过"]++
		case "未通过":
			s.NewUserVerification["未通过"]++
		default:
			s.NewUserVerification["待核验"]++
		}
	}
	switch {
	case active:
		s.Status = "未开始"
	case len(s.PendingOrderIDs) == 0:
		s.Status = "已全部闭合"
	default:
		s.Status = "部分待人工"
	}
	return s, nil
}

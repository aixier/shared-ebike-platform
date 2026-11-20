package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 订单头（events/ride.md）。
type orderHead struct {
	OrderID          string        `json:"orderId"`
	UserID           string        `json:"userId"`
	BrandID          string        `json:"brandId"`
	OperatorID       string        `json:"operatorId"`
	PricingVersionID string        `json:"pricingVersionId"`
	RequestAt        *time.Time    `json:"requestAt"`
	Degraded         *degradedMark `json:"degraded"`
}

type degradedMark struct {
	EventID      *string `json:"eventId"`
	Admission    string  `json:"admission"`
	LimitMinutes *int    `json:"limitMinutes"`
	LimitAmount  *int64  `json:"limitAmount"`
}

func (a *App) dispatch(ctx context.Context, tx pgx.Tx, env Envelope) error {
	switch env.Type {
	case "ride.order.created", "ride.order.unlocked", "ride.order.unlock_failed", "ride.order.ended", "ride.order.pending_manual":
		return a.onRideOrder(ctx, tx, env)
	case "ride.order.over_limit_reminded":
		return a.onOverLimit(ctx, tx, env)
	case "ride.dependency.state_changed":
		return a.onDependency(ctx, tx, env)
	case "opgw.operator.unreachable", "opgw.operator.recovered":
		return a.onOperatorReach(ctx, tx, env)
	case "bill.bill.issued", "bill.bill.paid", "bill.arrears.created":
		return a.onBill(ctx, tx, env)
	case "pay.payment.succeeded", "pay.payment.failed", "pay.payment.unknown":
		return a.onPay(ctx, tx, env)
	case "acct.realname.changed":
		return a.onRealname(ctx, tx, env)
	case "rule.version.published", "rule.version.revoked", "rule.params.changed":
		return a.onRule(ctx, tx, env)
	case "fleet.brand.registered", "fleet.brand.access_changed", "fleet.operator.exited":
		return a.onFleet(ctx, tx, env)
	}
	return nil
}

func decode(env Envelope, v any) error {
	if err := json.Unmarshal(env.Payload, v); err != nil {
		return fmt.Errorf("%s 载荷无法解析：%w", env.Type, err)
	}
	return nil
}

// ===== ride =====

func (a *App) onRideOrder(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		orderHead
		StartAt          *time.Time `json:"startAt"`
		FailedAt         *time.Time `json:"failedAt"`
		EndAt            *time.Time `json:"endAt"`
		Missing          []string   `json:"missing"`
		UserClaimedEndAt *time.Time `json:"userClaimedEndAt"`
	}
	if err := decode(env, &p); err != nil {
		return err
	}
	if p.OrderID == "" {
		return nil
	}
	if err := a.upsertOrderHead(ctx, tx, p.orderHead); err != nil {
		return err
	}
	var err error
	switch env.Type {
	case "ride.order.created":
		// 状态保持（同一订单按 orderId 保序，created 一定最先到）
	case "ride.order.unlocked":
		_, err = tx.Exec(ctx, `UPDATE orders SET start_at = $2,
			status = CASE WHEN status IN ('已结束','待人工处理') THEN status ELSE '骑行中' END, updated_at = now()
			WHERE order_id = $1`, p.OrderID, p.StartAt)
	case "ride.order.unlock_failed":
		_, err = tx.Exec(ctx, `UPDATE orders SET unlock_failed_at = $2, status = '开锁失败', updated_at = now() WHERE order_id = $1`, p.OrderID, p.FailedAt)
	case "ride.order.ended":
		_, err = tx.Exec(ctx, `UPDATE orders SET end_at = $2, start_at = COALESCE($3, start_at), status = '已结束',
			manual_missing = NULL, updated_at = now() WHERE order_id = $1`, p.OrderID, p.EndAt, p.StartAt)
	case "ride.order.pending_manual":
		_, err = tx.Exec(ctx, `UPDATE orders SET status = '待人工处理', manual_missing = $2, user_claimed_end_at = $3, updated_at = now()
			WHERE order_id = $1`, p.OrderID, p.Missing, p.UserClaimedEndAt)
	}
	if err != nil {
		return err
	}
	if p.Degraded != nil {
		if err := a.refreshClosure(ctx, tx, p.OrderID); err != nil {
			return err
		}
		if env.Type == "ride.order.created" {
			if err := a.trackDegradeMaxima(ctx, tx, p.OrderID); err != nil {
				return err
			}
			return a.checkLimits(ctx, tx)
		}
	}
	return nil
}

// upsertOrderHead 写订单头与降级标记；降级单的事件编号缺失时按 resolveEventID 归属。
func (a *App) upsertOrderHead(ctx context.Context, tx pgx.Tx, h orderHead) error {
	var eventID *string
	var admission *string
	var lm *int
	var la *int64
	degraded := h.Degraded != nil
	if degraded {
		ev, err := a.resolveEventID(ctx, tx, h.Degraded.EventID, h.RequestAt)
		if err != nil {
			return err
		}
		eventID = ev
		if h.Degraded.Admission != "" {
			admission = &h.Degraded.Admission
		}
		lm, la = h.Degraded.LimitMinutes, h.Degraded.LimitAmount
	}
	_, err := tx.Exec(ctx, `INSERT INTO orders (order_id, user_id, brand_id, operator_id, pricing_version_id, request_at,
			degraded, degrade_event_id, admission, limit_minutes, limit_amount)
		VALUES ($1, NULLIF($2,''), NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), $6, $7, $8, $9, $10, $11)
		ON CONFLICT (order_id) DO UPDATE SET
			user_id = COALESCE(orders.user_id, EXCLUDED.user_id),
			brand_id = COALESCE(orders.brand_id, EXCLUDED.brand_id),
			operator_id = COALESCE(orders.operator_id, EXCLUDED.operator_id),
			pricing_version_id = COALESCE(orders.pricing_version_id, EXCLUDED.pricing_version_id),
			request_at = COALESCE(orders.request_at, EXCLUDED.request_at),
			degraded = orders.degraded OR EXCLUDED.degraded,
			degrade_event_id = COALESCE(EXCLUDED.degrade_event_id, orders.degrade_event_id),
			admission = COALESCE(EXCLUDED.admission, orders.admission),
			limit_minutes = COALESCE(EXCLUDED.limit_minutes, orders.limit_minutes),
			limit_amount = COALESCE(EXCLUDED.limit_amount, orders.limit_amount),
			updated_at = now()`,
		h.OrderID, h.UserID, h.BrandID, h.OperatorID, h.PricingVersionID, h.RequestAt, degraded, eventID, admission, lm, la)
	return err
}

// resolveEventID 降级单归属哪个降级事件：载荷给了就用；否则用进行中的事件；
// 否则用开单时刻落在其进入与退出之间的事件；都没有则暂不归属（下次进入降级时并入）。
func (a *App) resolveEventID(ctx context.Context, tx pgx.Tx, given *string, requestAt *time.Time) (*string, error) {
	if given != nil && *given != "" {
		return given, nil
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT event_id FROM degrade_events WHERE status = '进行中'`).Scan(&id)
	if err == nil {
		return &id, nil
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}
	if requestAt == nil {
		return nil, nil
	}
	err = tx.QueryRow(ctx, `SELECT event_id FROM degrade_events WHERE entered_at <= $1 AND exited_at >= $1
		ORDER BY entered_at DESC LIMIT 1`, *requestAt).Scan(&id)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (a *App) onOverLimit(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		OrderID      string     `json:"orderId"`
		UserID       string     `json:"userId"`
		LimitMinutes int        `json:"limitMinutes"`
		LimitAmount  int64      `json:"limitAmount"`
		RemindedAt   *time.Time `json:"remindedAt"`
	}
	if err := decode(env, &p); err != nil {
		return err
	}
	at := a.now()
	if p.RemindedAt != nil {
		at = *p.RemindedAt
	}
	_, err := tx.Exec(ctx, `INSERT INTO orders (order_id, user_id, degraded, over_limit_reminded_at) VALUES ($1, NULLIF($2,''), true, $3)
		ON CONFLICT (order_id) DO UPDATE SET over_limit_reminded_at = COALESCE(orders.over_limit_reminded_at, EXCLUDED.over_limit_reminded_at),
		degraded = true, updated_at = now()`, p.OrderID, p.UserID, at)
	if err != nil {
		return err
	}
	var eventID, brandID *string
	_ = tx.QueryRow(ctx, `SELECT degrade_event_id, brand_id FROM orders WHERE order_id = $1`, p.OrderID).Scan(&eventID, &brandID)
	text := fmt.Sprintf("降级单 %s 已超过单笔上限（%d 分钟或 %s），已提醒用户尽快还车", p.OrderID, p.LimitMinutes, yuan(p.LimitAmount))
	return a.raiseAlert(ctx, tx, "警告", "降级单超限", text, brandID, eventID, "overlimit:"+p.OrderID)
}

func (a *App) onDependency(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		Component string     `json:"component"`
		State     string     `json:"state"`
		At        *time.Time `json:"at"`
		Instance  string     `json:"instance"`
	}
	if err := decode(env, &p); err != nil {
		return err
	}
	at := a.now()
	if p.At != nil {
		at = *p.At
	}
	if _, err := tx.Exec(ctx, `INSERT INTO dependency_states (component, instance, state, at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (component, instance) DO UPDATE SET state = EXCLUDED.state, at = EXCLUDED.at
		WHERE dependency_states.at <= EXCLUDED.at`, p.Component, p.Instance, p.State, at); err != nil {
		return err
	}
	// 运行智能体未接入：只告警并通知值班人，由值班人手动宣布进入与退出降级
	key := "dep:" + p.Component + ":" + p.Instance + ":" + p.State + ":" + at.UTC().Format(time.RFC3339Nano)
	if p.State == "熔断" {
		text := fmt.Sprintf("骑行对「%s」的依赖已熔断（实例 %s），新单按降级开单；请值班人判断是否宣布进入降级", p.Component, p.Instance)
		return a.raiseAlert(ctx, tx, "严重", AlertDependencyTripped, text, nil, nil, key)
	}
	text := fmt.Sprintf("骑行对「%s」的依赖已恢复（实例 %s）；如故障已排除，请值班人判断是否退出降级", p.Component, p.Instance)
	return a.raiseAlert(ctx, tx, "提示", AlertDependencyRecovered, text, nil, nil, key)
}

func (a *App) onOperatorReach(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		OperatorID  string     `json:"operatorId"`
		BrandIDs    []string   `json:"brandIds"`
		Since       *time.Time `json:"since"`
		LastError   string     `json:"lastError"`
		RecoveredAt *time.Time `json:"recoveredAt"`
	}
	if err := decode(env, &p); err != nil {
		return err
	}
	if p.BrandIDs == nil {
		p.BrandIDs = []string{}
	}
	var brandID *string
	if len(p.BrandIDs) == 1 {
		brandID = &p.BrandIDs[0]
	}
	names := a.brandNames(ctx, tx, p.BrandIDs)
	if env.Type == "opgw.operator.unreachable" {
		since := a.now()
		if p.Since != nil {
			since = *p.Since
		}
		var open int
		_ = tx.QueryRow(ctx, `SELECT count(*) FROM operator_outages WHERE operator_id = $1 AND recovered_at IS NULL`, p.OperatorID).Scan(&open)
		if open == 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO operator_outages (operator_id, brand_ids, since, last_error) VALUES ($1,$2,$3,$4)`,
				p.OperatorID, p.BrandIDs, since, p.LastError); err != nil {
				return err
			}
		}
		text := fmt.Sprintf("运营商通道不可达（%s，品牌：%s），自 %s 起；最近一次失败：%s", p.OperatorID, names, a.fmtTime(since), p.LastError)
		return a.raiseAlert(ctx, tx, "警告", AlertOperatorUnreachable, text, brandID, nil, "unreach:"+env.EventID)
	}
	at := a.now()
	if p.RecoveredAt != nil {
		at = *p.RecoveredAt
	}
	if _, err := tx.Exec(ctx, `UPDATE operator_outages SET recovered_at = $2 WHERE operator_id = $1 AND recovered_at IS NULL`, p.OperatorID, at); err != nil {
		return err
	}
	text := fmt.Sprintf("运营商通道已恢复（%s，品牌：%s）", p.OperatorID, names)
	return a.raiseAlert(ctx, tx, "提示", AlertOperatorRecovered, text, brandID, nil, "recover:"+env.EventID)
}

// ===== bill / pay：收款结果 =====

func (a *App) onBill(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		OrderID      string     `json:"orderId"`
		UserID       string     `json:"userId"`
		BrandID      string     `json:"brandId"`
		OperatorID   string     `json:"operatorId"`
		Receivable   *int64     `json:"receivable"`
		CollectBy    string     `json:"collectBy"`
		BillPushedAt *time.Time `json:"billPushedAt"`
		OrderEndAt   *time.Time `json:"orderEndAt"`
		Degraded     bool       `json:"degraded"`
	}
	if err := decode(env, &p); err != nil {
		return err
	}
	if p.OrderID == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO orders (order_id, user_id, brand_id, operator_id, degraded) VALUES ($1, NULLIF($2,''), NULLIF($3,''), NULLIF($4,''), $5)
		ON CONFLICT (order_id) DO UPDATE SET degraded = orders.degraded OR EXCLUDED.degraded,
		brand_id = COALESCE(orders.brand_id, EXCLUDED.brand_id), operator_id = COALESCE(orders.operator_id, EXCLUDED.operator_id)`,
		p.OrderID, p.UserID, p.BrandID, p.OperatorID, p.Degraded); err != nil {
		return err
	}
	var err error
	switch env.Type {
	case "bill.bill.issued":
		// 一键支付：账单已推送即为一种收款结果；无需支付：应付为 0，视同已收
		next := ""
		switch p.CollectBy {
		case "一键支付":
			next = "已推送一键支付"
		case "无需支付":
			next = "已收款"
		}
		_, err = tx.Exec(ctx, `UPDATE orders SET receivable = $2, bill_pushed_at = $3, order_end_at = $4,
			payment = CASE WHEN $5 <> '' AND payment = '未收款' THEN $5 ELSE payment END, updated_at = now()
			WHERE order_id = $1`, p.OrderID, p.Receivable, p.BillPushedAt, p.OrderEndAt, next)
	case "bill.bill.paid":
		_, err = tx.Exec(ctx, `UPDATE orders SET payment = '已收款', payment_unknown_since = NULL, updated_at = now() WHERE order_id = $1`, p.OrderID)
	case "bill.arrears.created":
		_, err = tx.Exec(ctx, `UPDATE orders SET payment = '已转欠费', payment_unknown_since = NULL, updated_at = now()
			WHERE order_id = $1 AND payment <> '已收款'`, p.OrderID)
	}
	if err != nil {
		return err
	}
	return a.refreshClosure(ctx, tx, p.OrderID)
}

func (a *App) onPay(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		PaymentID string     `json:"paymentId"`
		Type      string     `json:"type"`
		OrderID   *string    `json:"orderId"`
		Since     *time.Time `json:"since"`
	}
	if err := decode(env, &p); err != nil {
		return err
	}
	if p.OrderID == nil || *p.OrderID == "" {
		return nil
	}
	switch p.Type {
	case "订单扣款", "订单支付", "欠费补付":
	default:
		return nil
	}
	oid := *p.OrderID
	if _, err := tx.Exec(ctx, `INSERT INTO orders (order_id) VALUES ($1) ON CONFLICT DO NOTHING`, oid); err != nil {
		return err
	}
	var err error
	switch env.Type {
	case "pay.payment.succeeded":
		_, err = tx.Exec(ctx, `UPDATE orders SET payment = '已收款', payment_id = $2, payment_unknown_since = NULL, updated_at = now() WHERE order_id = $1`, oid, p.PaymentID)
	case "pay.payment.failed":
		// 扣款失败由 bill 转欠费，这里只撤掉"结果未知"
		_, err = tx.Exec(ctx, `UPDATE orders SET payment = CASE WHEN payment = '结果未知' THEN '未收款' ELSE payment END,
			payment_unknown_since = NULL, updated_at = now() WHERE order_id = $1`, oid)
	case "pay.payment.unknown":
		since := a.now()
		if p.Since != nil {
			since = *p.Since
		}
		_, err = tx.Exec(ctx, `UPDATE orders SET payment = '结果未知', payment_id = $2, payment_unknown_since = $3, updated_at = now()
			WHERE order_id = $1 AND payment NOT IN ('已收款','已转欠费')`, oid, p.PaymentID, since)
	}
	if err != nil {
		return err
	}
	return a.refreshClosure(ctx, tx, oid)
}

// ===== acct / rule / fleet =====

func (a *App) onRealname(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		UserID       string `json:"userId"`
		Status       string `json:"status"`
		AfterDegrade bool   `json:"afterDegrade"`
	}
	if err := decode(env, &p); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO users_realname (user_id, status, after_degrade) VALUES ($1,$2,$3)
		ON CONFLICT (user_id) DO UPDATE SET status = EXCLUDED.status, after_degrade = EXCLUDED.after_degrade, updated_at = now()`,
		p.UserID, p.Status, p.AfterDegrade)
	return err
}

func (a *App) onRule(ctx context.Context, tx pgx.Tx, env Envelope) error {
	switch env.Type {
	case "rule.version.published":
		var p struct {
			VersionID   string          `json:"versionId"`
			EffectiveAt time.Time       `json:"effectiveAt"`
			Ride        json.RawMessage `json:"ride"`
			RiskLimits  json.RawMessage `json:"riskLimits"`
		}
		if err := decode(env, &p); err != nil {
			return err
		}
		// 版本一经生效不可修改：重复到达不覆盖
		_, err := tx.Exec(ctx, `INSERT INTO rule_versions (version_id, effective_at, ride, risk_limits) VALUES ($1,$2,$3,$4)
			ON CONFLICT (version_id) DO NOTHING`, p.VersionID, p.EffectiveAt, nullJSON(p.Ride), nullJSON(p.RiskLimits))
		return err
	case "rule.version.revoked":
		var p struct {
			VersionID string `json:"versionId"`
		}
		if err := decode(env, &p); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE rule_versions SET revoked = true WHERE version_id = $1`, p.VersionID)
		return err
	case "rule.params.changed":
		var p struct {
			ChangedAt *time.Time `json:"changedAt"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		_, err := tx.Exec(ctx, `INSERT INTO platform_params (id, body, changed_at) VALUES (1,$1,$2)
			ON CONFLICT (id) DO UPDATE SET body = EXCLUDED.body, changed_at = EXCLUDED.changed_at
			WHERE platform_params.changed_at IS NULL OR EXCLUDED.changed_at IS NULL OR platform_params.changed_at <= EXCLUDED.changed_at`,
			[]byte(env.Payload), p.ChangedAt)
		return err
	}
	return nil
}

func (a *App) onFleet(ctx context.Context, tx pgx.Tx, env Envelope) error {
	switch env.Type {
	case "fleet.brand.registered":
		var p struct {
			BrandID      string `json:"brandId"`
			OperatorID   string `json:"operatorId"`
			Name         string `json:"name"`
			AccessStatus string `json:"accessStatus"`
		}
		if err := decode(env, &p); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO brands (brand_id, operator_id, name, access_status) VALUES ($1,$2,$3,$4)
			ON CONFLICT (brand_id) DO UPDATE SET operator_id = EXCLUDED.operator_id, name = EXCLUDED.name,
			access_status = EXCLUDED.access_status, updated_at = now()`, p.BrandID, p.OperatorID, p.Name, p.AccessStatus)
		return err
	case "fleet.brand.access_changed":
		var p struct {
			BrandID    string     `json:"brandId"`
			OperatorID string     `json:"operatorId"`
			From       string     `json:"from"`
			To         string     `json:"to"`
			ChangedAt  *time.Time `json:"changedAt"`
		}
		if err := decode(env, &p); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO brands (brand_id, operator_id, access_status) VALUES ($1, NULLIF($2,''), $3)
			ON CONFLICT (brand_id) DO UPDATE SET access_status = EXCLUDED.access_status,
			operator_id = COALESCE(EXCLUDED.operator_id, brands.operator_id), updated_at = now()`, p.BrandID, p.OperatorID, p.To); err != nil {
			return err
		}
		if p.From == "暂停可租" && p.To != "暂停可租" {
			at := a.now()
			if p.ChangedAt != nil {
				at = *p.ChangedAt
			}
			_, err := tx.Exec(ctx, `UPDATE pauses SET resumed_at = $2 WHERE brand_id = $1 AND resumed_at IS NULL`, p.BrandID, at)
			return err
		}
		return nil
	case "fleet.operator.exited":
		var p struct {
			OperatorID string   `json:"operatorId"`
			BrandIDs   []string `json:"brandIds"`
		}
		if err := decode(env, &p); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE brands SET access_status = '已退出', updated_at = now() WHERE brand_id = ANY($1) OR operator_id = $2`, p.BrandIDs, p.OperatorID)
		return err
	}
	return nil
}

// ===== 小工具 =====

func nullJSON(r json.RawMessage) []byte {
	if len(r) == 0 || string(r) == "null" {
		return nil
	}
	return []byte(r)
}

// brandName 品牌名（投影里没有时用编号）。
func (a *App) brandName(ctx context.Context, tx pgx.Tx, brandID string) string {
	var name *string
	_ = tx.QueryRow(ctx, `SELECT name FROM brands WHERE brand_id = $1`, brandID).Scan(&name)
	if name == nil || *name == "" {
		return brandID
	}
	return *name
}

func (a *App) brandNames(ctx context.Context, tx pgx.Tx, ids []string) string {
	var out []string
	for _, id := range ids {
		out = append(out, a.brandName(ctx, tx, id))
	}
	if len(out) == 0 {
		return "无"
	}
	return strings.Join(out, "、")
}

// yuan 分 → "¥x.xx"，用于告警文案。
func yuan(fen int64) string {
	sign := ""
	if fen < 0 {
		sign, fen = "-", -fen
	}
	return fmt.Sprintf("%s¥%d.%02d", sign, fen/100, fen%100)
}


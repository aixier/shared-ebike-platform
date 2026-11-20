package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/bill/internal/billing"
)

// HandleEvent 订阅事件的总入口
func (a *App) HandleEvent(ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	switch env.Type {
	case "ride.order.ended":
		return a.onOrderEnded(ctx, tx, env)
	case "ride.order.unlock_failed":
		return nil, a.onUnlockFailed(ctx, tx, env)
	case "ride.order.unlocked":
		return nil, a.onUnlocked(ctx, tx, env)
	case "ride.order.pending_manual":
		return nil, a.onPendingManual(ctx, tx, env)
	case "acct.user.registered":
		return nil, a.onUserRegistered(ctx, tx, env)
	case "fleet.operator.exited":
		return nil, a.onOperatorExited(ctx, tx, env)
	case "pay.payment.succeeded", "pay.payment.failed", "pay.payment.unknown":
		return nil, a.onPayment(ctx, tx, env)
	case "pay.refund.succeeded", "pay.refund.failed":
		return nil, a.onRefundResult(ctx, tx, env)
	case "deposit.status.changed":
		return nil, a.onDepositStatus(ctx, tx, env)
	case "deposit.deducted":
		return nil, a.onDepositDeducted(ctx, tx, env)
	case "rule.version.published":
		return nil, a.onVersionPublished(ctx, tx, env)
	case "rule.version.revoked":
		return nil, a.onVersionRevoked(ctx, tx, env)
	case "rule.params.changed":
		return nil, a.onParamsChanged(ctx, tx, env)
	case "acct.realname.changed":
		return nil, a.onRealname(ctx, tx, env)
	case "acct.account.frozen", "acct.account.unfrozen", "acct.account.cancelled":
		return nil, a.onAccount(ctx, tx, env)
	}
	return nil, nil
}

/* ---------------- ride ---------------- */

type orderEnded struct {
	OrderID            string          `json:"orderId"`
	UserID             string          `json:"userId"`
	BrandID            string          `json:"brandId"`
	OperatorID         string          `json:"operatorId"`
	Channel            string          `json:"channel"`
	PricingVersionID   string          `json:"pricingVersionId"`
	UnlockResult       string          `json:"unlockResult"`
	StartAt            time.Time       `json:"startAt"`
	EndAt              time.Time       `json:"endAt"`
	DurationSec        int64           `json:"durationSec"`
	InStation          bool            `json:"inStation"`
	DistanceToStationM *int            `json:"distanceToStationM"`
	Degraded           json.RawMessage `json:"degraded"`
}

func isDegraded(raw json.RawMessage) bool {
	s := string(raw)
	return len(raw) > 0 && s != "null"
}

// onOrderEnded 订单结束即计价出账（R-08 R-09 BR-03 BR-14）；降级单补到的结束事件同样处理（R-34）
func (a *App) onOrderEnded(ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var e orderEnded
	if err := json.Unmarshal(env.Payload, &e); err != nil {
		return nil, nil
	}
	if _, err := getBill(ctx, tx, `order_id=$1`, e.OrderID); err == nil {
		return nil, nil // 一单一张账单
	} else if !errors.Is(err, errNotFound) {
		return nil, err
	}
	// 开锁失败的订单不收费（BR-02）；超时后才报开锁成功的单按 0 元出账（BR-03）
	if st, err := unlockStateOf(ctx, tx, e.OrderID); err != nil {
		return nil, err
	} else if st == ulFailed && e.UnlockResult != "超时后成功" {
		return nil, nil
	}
	ver, err := getVersion(ctx, tx, e.PricingVersionID)
	if errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("%w：%s", errUnknownVersion, e.PricingVersionID)
	}
	if err != nil {
		return nil, err
	}
	if err := ensureProfile(ctx, tx, e.UserID); err != nil {
		return nil, err
	}
	if e.Channel != "" {
		if _, err := tx.Exec(ctx, `UPDATE user_profiles SET channel=$2, updated_at=now() WHERE user_id=$1`, e.UserID, e.Channel); err != nil {
			return nil, err
		}
	}
	prof, err := getProfile(ctx, tx, e.UserID)
	if err != nil {
		return nil, err
	}
	cards, err := paidCards(ctx, tx, e.UserID)
	if err != nil {
		return nil, err
	}
	cps, err := usableCoupons(ctx, tx, e.UserID)
	if err != nil {
		return nil, err
	}
	now := a.Now()
	r := billing.Price(billing.Input{Version: *ver, DurationSec: e.DurationSec, InStation: e.InStation, Card: billing.CardFor(cards, e.StartAt),
		Coupons: cps, BillAt: now, Zero: e.UnlockResult == "超时后成功"})

	b := &Bill{BillID: "BL" + trimPrefix(e.OrderID, "OD"), OrderID: e.OrderID, UserID: e.UserID, BrandID: e.BrandID, OperatorID: e.OperatorID,
		Channel: e.Channel, PricingVersionID: e.PricingVersionID, StartAt: e.StartAt, EndAt: e.EndAt, DurationSec: e.DurationSec,
		InStation: e.InStation, DistanceToStationM: e.DistanceToStationM, ZeroUnlock: e.UnlockResult == "超时后成功", Degraded: isDegraded(e.Degraded),
		BilledMinutes: r.BilledMinutes, RideFee: r.RideFee, DispatchFee: r.DispatchFee, Receivable: r.Receivable, MonthCardDeduct: r.MonthCardDeduct,
		CouponDeduct: r.CouponDeduct, Payable: r.Payable, BillPushedAt: now}
	if r.MonthCardDeduct > 0 {
		b.MonthCardID = sp(r.MonthCardID)
	}
	if r.CouponID != "" {
		b.CouponID, b.CouponName = sp(r.CouponID), sp(r.CouponName)
	}
	params, err := getParams(ctx, tx)
	if err != nil {
		return nil, err
	}
	switch {
	case b.Payable == 0:
		b.CollectBy, b.PayStatus = byNone, stNone
	case str(prof.DepositMode) == "免押":
		b.CollectBy, b.PayStatus, b.PayMethod = byCredit, stPending, sp(byCredit)
		b.setPending(pendCredit, "")
	default: // 押金用户、先骑后验新用户与押金状态未知的用户：推送一键支付
		b.CollectBy, b.PayStatus, b.PayMethod = byOneTap, stPending, sp(byOneTap)
		if params.Known {
			d := now.Add(time.Duration(params.PayWindowHours) * time.Hour)
			b.PayDeadline = &d
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bills (bill_id, order_id, user_id, brand_id, operator_id, channel, pricing_version_id, start_at, end_at, duration_sec,
		in_station, distance_to_station_m, zero_unlock, degraded, billed_minutes, ride_fee, dispatch_fee, receivable, month_card_id, month_card_deduct,
		coupon_id, coupon_name, coupon_deduct, payable, pay_status, pay_method, collect_by, pay_deadline, bill_pushed_at, pending_kind)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30)`,
		b.BillID, b.OrderID, b.UserID, b.BrandID, b.OperatorID, b.Channel, b.PricingVersionID, b.StartAt, b.EndAt, b.DurationSec,
		b.InStation, b.DistanceToStationM, b.ZeroUnlock, b.Degraded, b.BilledMinutes, b.RideFee, b.DispatchFee, b.Receivable, b.MonthCardID, b.MonthCardDeduct,
		b.CouponID, b.CouponName, b.CouponDeduct, b.Payable, b.PayStatus, b.PayMethod, b.CollectBy, b.PayDeadline, b.BillPushedAt, b.PendingKind); err != nil {
		return nil, err
	}
	if b.CouponID != nil {
		if _, err := tx.Exec(ctx, `UPDATE coupons SET used_order_id=$2, used_at=$3 WHERE coupon_id=$1`, *b.CouponID, b.OrderID, now); err != nil {
			return nil, err
		}
	}
	if err := emit(ctx, tx, "bill.bill.issued", b.OrderID, SystemActor, now, map[string]any{
		"orderId": b.OrderID, "userId": b.UserID, "brandId": b.BrandID, "operatorId": b.OperatorID, "pricingVersionId": b.PricingVersionID,
		"billedMinutes": b.BilledMinutes, "rideFee": b.RideFee, "dispatchFee": b.DispatchFee, "receivable": b.Receivable,
		"monthCardDeduct": b.MonthCardDeduct, "monthCardId": b.MonthCardID, "couponDeduct": b.CouponDeduct, "payable": b.Payable,
		"collectBy": b.CollectBy, "payDeadline": b.PayDeadline, "billPushedAt": now, "orderEndAt": b.EndAt, "degraded": b.Degraded,
	}); err != nil {
		return nil, err
	}
	switch {
	case b.PayStatus == stNone:
		return nil, emit(ctx, tx, "bill.bill.paid", b.OrderID, SystemActor, now, paidPayload{OrderID: b.OrderID, UserID: b.UserID, PayStatus: stNone, PaidAt: now, Degraded: b.Degraded})
	case b.CollectBy == byOneTap && b.Degraded && str(prof.RealnameStatus) == "未通过":
		// BR-34：先骑后验核验不通过的用户，本次费用转欠费追缴
		return nil, a.toArrears(ctx, tx, b, causePursue, SystemActor)
	case b.CollectBy == byCredit:
		id := b.BillID
		return func(ctx context.Context) { a.chargeCredit(ctx, id) }, nil
	}
	return nil, nil
}

func trimPrefix(s, p string) string {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}

func paidCards(ctx context.Context, q querier, userID string) ([]billing.Card, error) {
	rows, err := q.Query(ctx, `SELECT card_id, start_at, end_at, per_order_cap FROM month_cards WHERE user_id=$1 AND state='已支付' ORDER BY start_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []billing.Card
	for rows.Next() {
		var c billing.Card
		if err := rows.Scan(&c.CardID, &c.StartAt, &c.EndAt, &c.PerOrderCap); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func usableCoupons(ctx context.Context, tx pgx.Tx, userID string) ([]billing.Coupon, error) {
	rows, err := tx.Query(ctx, `SELECT coupon_id, name, amount, issued_at, expire_at FROM coupons WHERE user_id=$1 AND used_order_id IS NULL ORDER BY coupon_id FOR UPDATE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []billing.Coupon
	for rows.Next() {
		var c billing.Coupon
		if err := rows.Scan(&c.CouponID, &c.Name, &c.Amount, &c.IssuedAt, &c.ExpireAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type unlockFailed struct {
	OrderID          string    `json:"orderId"`
	UserID           string    `json:"userId"`
	PricingVersionID string    `json:"pricingVersionId"`
	Reason           *string   `json:"reason"`
	FailedAt         time.Time `json:"failedAt"`
}

// onUnlockFailed 当天首次开锁失败发关怀券（发放方式写明"开锁失败"的模板）
func (a *App) onUnlockFailed(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e unlockFailed
	if err := json.Unmarshal(env.Payload, &e); err != nil {
		return nil
	}
	at := e.FailedAt
	if at.IsZero() {
		at = a.Now()
	}
	if e.OrderID != "" {
		// 已经报过开锁成功的订单（超时后才成功的单，事件乱序到达时）不改回失败
		if st, err := unlockStateOf(ctx, tx, e.OrderID); err != nil {
			return err
		} else if st != ulOK {
			if err := setUnlockState(ctx, tx, e.OrderID, ulFailed, e.Reason, at); err != nil {
				return err
			}
		}
	}
	ver, err := currentVersion(ctx, tx, at)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	tpl := billing.CareTemplate(ver.Coupons)
	if tpl == nil {
		return nil
	}
	d := at.In(a.Loc)
	dayStart := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, a.Loc)
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM coupons WHERE user_id=$1 AND template_id=$2 AND issued_at >= $3 AND issued_at < $4`,
		e.UserID, tpl.TemplateID, dayStart, dayStart.AddDate(0, 0, 1)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err = a.issueCoupon(ctx, tx, e.UserID, *tpl, "系统发放", a.Now())
	return err
}

/* ---------------- pay ---------------- */

type payEvent struct {
	PaymentID        string     `json:"paymentId"`
	BizNo            string     `json:"bizNo"`
	Type             string     `json:"type"`
	UserID           string     `json:"userId"`
	OrderID          *string    `json:"orderId"`
	Amount           int64      `json:"amount"`
	RelatedPaymentID *string    `json:"relatedPaymentId"`
	CompletedAt      *time.Time `json:"completedAt"`
}

func orderPayType(t string) bool {
	return t == "订单扣款" || t == "订单支付" || t == "欠费补付"
}

func (a *App) onPayment(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e payEvent
	if err := json.Unmarshal(env.Payload, &e); err != nil {
		return nil
	}
	if e.Type == "月卡购买" {
		return a.onCardPayment(ctx, tx, env.Type, e)
	}
	if !orderPayType(e.Type) || e.OrderID == nil {
		return nil
	}
	b, err := lockBill(ctx, tx, *e.OrderID)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch env.Type {
	case "pay.payment.succeeded":
		return a.applyChannelPaid(ctx, tx, b, e.PaymentID, e.Type, e.Amount, SystemActor)
	case "pay.payment.failed":
		return a.applyPayFailed(ctx, tx, b, e.PaymentID, e.Type)
	default: // 结果未知：保持处理中，不重复发起
		if b.unpaid() > 0 && (b.PendingKind == nil || *b.PendingKind != pendDeduct) {
			kind := pendOneTap
			if e.Type == "订单扣款" {
				kind = pendCredit
			}
			b.setPending(kind, e.PaymentID)
		}
		return saveMoney(ctx, tx, b)
	}
}

// applyPayFailed 收款失败：免押扣款失败转欠费；一键支付失败只解除处理中
func (a *App) applyPayFailed(ctx context.Context, tx pgx.Tx, b *Bill, paymentID, typ string) error {
	if typ == "订单扣款" {
		if b.PendingKind != nil && *b.PendingKind == pendCredit {
			b.clearPending()
		}
		return a.toArrears(ctx, tx, b, causeChargeFailed, SystemActor)
	}
	if b.PendingPaymentID != nil && *b.PendingPaymentID == paymentID {
		b.clearPending()
	}
	return saveMoney(ctx, tx, b)
}

/* ---------------- deposit ---------------- */

type depositStatus struct {
	UserID  string  `json:"userId"`
	Mode    string  `json:"mode"`
	Status  *string `json:"status"`
	Balance *int64  `json:"balance"`
}

func (a *App) onDepositStatus(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e depositStatus
	if err := json.Unmarshal(env.Payload, &e); err != nil || e.UserID == "" {
		return nil
	}
	if err := ensureProfile(ctx, tx, e.UserID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE user_profiles SET deposit_mode=$2, deposit_status=$3, deposit_balance=$4, updated_at=now() WHERE user_id=$1`, e.UserID, e.Mode, e.Status, e.Balance)
	return err
}

type depositDeducted struct {
	UserID       string `json:"userId"`
	OrderID      string `json:"orderId"`
	Amount       int64  `json:"amount"`
	BalanceAfter int64  `json:"balanceAfter"`
}

func (a *App) onDepositDeducted(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e depositDeducted
	if err := json.Unmarshal(env.Payload, &e); err != nil {
		return nil
	}
	b, err := lockBill(ctx, tx, e.OrderID)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE user_profiles SET deposit_balance=$2, updated_at=now() WHERE user_id=$1`, e.UserID, e.BalanceAfter); err != nil {
		return err
	}
	return a.applyDeposit(ctx, tx, b, e.Amount, SystemActor)
}

/* ---------------- rule ---------------- */

func (a *App) onVersionPublished(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var v versionBody
	if err := json.Unmarshal(env.Payload, &v); err != nil || v.VersionID == "" {
		return nil
	}
	// 版本不可变：同一版本号只存第一次
	_, err := tx.Exec(ctx, `INSERT INTO pricing_versions (version_id, effective_at, body) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, v.VersionID, v.EffectiveAt, []byte(env.Payload))
	return err
}

func (a *App) onVersionRevoked(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e struct {
		VersionID string    `json:"versionId"`
		RevokedAt time.Time `json:"revokedAt"`
	}
	if err := json.Unmarshal(env.Payload, &e); err != nil {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE pricing_versions SET revoked_at=$2 WHERE version_id=$1 AND revoked_at IS NULL`, e.VersionID, e.RevokedAt)
	return err
}

func (a *App) onParamsChanged(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e struct {
		PayWindowHours    *int      `json:"payWindowHours"`
		ArrearsFreezeDays *int      `json:"arrearsFreezeDays"`
		ChangedAt         time.Time `json:"changedAt"`
	}
	if err := json.Unmarshal(env.Payload, &e); err != nil || e.PayWindowHours == nil || e.ArrearsFreezeDays == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO platform_params (id, pay_window_hours, arrears_freeze_days, changed_at) VALUES (1,$1,$2,$3)
		ON CONFLICT (id) DO UPDATE SET pay_window_hours=EXCLUDED.pay_window_hours, arrears_freeze_days=EXCLUDED.arrears_freeze_days, changed_at=EXCLUDED.changed_at
		WHERE platform_params.changed_at <= EXCLUDED.changed_at`, *e.PayWindowHours, *e.ArrearsFreezeDays, e.ChangedAt)
	return err
}

/* ---------------- acct ---------------- */

func (a *App) onRealname(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e struct {
		UserID string `json:"userId"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(env.Payload, &e); err != nil || e.UserID == "" {
		return nil
	}
	if err := ensureProfile(ctx, tx, e.UserID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE user_profiles SET realname_status=$2, updated_at=now() WHERE user_id=$1`, e.UserID, e.Status); err != nil {
		return err
	}
	if e.Status != "未通过" {
		return nil
	}
	// BR-34：补验未通过，把降级期间的待支付费用转欠费追缴
	bills, err := listBills(ctx, tx, `user_id=$1 AND degraded AND pay_status=$2 AND pending_kind IS NULL ORDER BY bill_pushed_at FOR UPDATE`, e.UserID, stPending)
	if err != nil {
		return err
	}
	for _, b := range bills {
		if err := a.toArrears(ctx, tx, b, causePursue, SystemActor); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) onAccount(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e struct {
		UserID string `json:"userId"`
	}
	if err := json.Unmarshal(env.Payload, &e); err != nil || e.UserID == "" {
		return nil
	}
	if err := ensureProfile(ctx, tx, e.UserID); err != nil {
		return err
	}
	var q string
	switch env.Type {
	case "acct.account.frozen":
		q = `UPDATE user_profiles SET frozen=true, updated_at=now() WHERE user_id=$1`
	case "acct.account.unfrozen":
		q = `UPDATE user_profiles SET frozen=false, updated_at=now() WHERE user_id=$1`
	default:
		q = `UPDATE user_profiles SET cancelled=true, updated_at=now() WHERE user_id=$1`
	}
	_, err := tx.Exec(ctx, q, e.UserID)
	return err
}

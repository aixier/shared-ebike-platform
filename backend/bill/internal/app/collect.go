package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/bill/internal/clients/deposit"
	"bike/bill/internal/clients/pay"
)

// 收款编排：免押自动扣款、一键支付、支付期限届满押金抵扣、欠费超期（R-09 BR-22 BR-05 R-14）。

// chargeCredit 对免押用户自动扣款。同一账单用同一幂等键，重试拿到的是 pay 的最新结果
func (a *App) chargeCredit(ctx context.Context, billID string) {
	b, err := getBill(ctx, a.Pool, `bill_id=$1`, billID)
	if err != nil || b.PendingKind == nil || *b.PendingKind != pendCredit || b.unpaid() <= 0 {
		return
	}
	res, err := a.Pay.ChargeCreditFree(ctx, "credit-"+billID, pay.CreditChargeCommand{
		BizNo: billID, UserID: b.UserID, OrderID: b.OrderID, Amount: b.unpaid(), Channel: collectChannel(b.Channel),
	})
	if err != nil {
		a.Log.Warn("免押扣款调用失败，稍后重试", "billId", billID, "err", err)
		return
	}
	amount := b.unpaid()
	err = inTx(ctx, a.Pool, func(tx pgx.Tx) error {
		b, err := lockBillByID(ctx, tx, billID)
		if err != nil {
			return err
		}
		switch res.Result {
		case "成功":
			amt := res.Amount
			if amt <= 0 {
				amt = amount
			}
			return a.applyChannelPaid(ctx, tx, b, res.PaymentID, "订单扣款", amt, SystemActor)
		case "失败":
			if res.FailCode != nil && *res.FailCode == "ALREADY_PAID" {
				return nil // 已成功过，等收款成功事件
			}
			return a.applyPayFailed(ctx, tx, b, res.PaymentID, "订单扣款")
		default:
			if b.PendingKind != nil && *b.PendingKind == pendCredit {
				b.setPending(pendCredit, res.PaymentID)
			}
			return saveMoney(ctx, tx, b)
		}
	})
	if err != nil {
		a.Log.Warn("免押扣款结果入账失败", "billId", billID, "err", err)
	}
}

// RetryCredit 重试尚未得到结果的免押扣款（调用失败或结果未知）
func (a *App) RetryCredit(ctx context.Context) error {
	ids, err := a.idsWhere(ctx, `SELECT bill_id FROM bills WHERE pending_kind=$1 AND pay_status=$2 AND updated_at < $3 ORDER BY bill_pushed_at LIMIT 200`,
		pendCredit, stPending, a.Now().Add(-20*time.Second))
	if err != nil {
		return err
	}
	for _, id := range ids {
		a.chargeCredit(ctx, id)
	}
	return nil
}

func (a *App) idsWhere(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := a.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RunDeadlines 一键支付期限届满未付：押金用户调 deposit 抵扣（不足记欠费），其余直接记欠费（BR-22）
func (a *App) RunDeadlines(ctx context.Context) error {
	params, err := getParams(ctx, a.Pool)
	if err != nil || !params.Known {
		return err // 还没收到平台参数，不判定期限
	}
	now := a.Now()
	ids, err := a.idsWhere(ctx, `SELECT bill_id FROM bills WHERE pay_status=$1 AND collect_by=$2 AND (pending_kind IS NULL OR pending_kind=$3)
		AND COALESCE(pay_deadline, bill_pushed_at + make_interval(hours => $4)) <= $5 ORDER BY bill_pushed_at LIMIT 200`,
		stPending, byOneTap, pendDeduct, params.PayWindowHours, now)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := a.expireOne(ctx, id); err != nil {
			a.Log.Warn("支付期限处理失败", "billId", id, "err", err)
		}
	}
	return nil
}

func depositEligible(p *Profile) bool {
	if str(p.DepositMode) != "押金" {
		return false
	}
	st := str(p.DepositStatus)
	if st != "已缴" && st != "押金不足" {
		return false
	}
	return p.DepositBalance == nil || *p.DepositBalance > 0
}

func (a *App) expireOne(ctx context.Context, billID string) error {
	var amount int64
	var userID, orderID string
	deduct := false
	err := inTx(ctx, a.Pool, func(tx pgx.Tx) error {
		b, err := lockBillByID(ctx, tx, billID)
		if err != nil {
			return err
		}
		if b.PayStatus != stPending || (b.PendingKind != nil && *b.PendingKind != pendDeduct) {
			return nil
		}
		prof, err := getProfile(ctx, tx, b.UserID)
		if err != nil {
			return err
		}
		if b.PendingKind == nil && !depositEligible(prof) {
			// 不是可抵扣的押金用户（先骑后验新用户、押金状态未知等）：期限届满没付，直接记欠费
			return a.toArrears(ctx, tx, b, causeExpired, SystemActor)
		}
		if b.PendingKind == nil {
			b.setPending(pendDeduct, "")
			if err := saveMoney(ctx, tx, b); err != nil {
				return err
			}
		}
		deduct, amount, userID, orderID = true, b.unpaid(), b.UserID, b.OrderID
		return nil
	})
	if err != nil || !deduct {
		return err
	}
	res, err := a.Deposit.Deduct(ctx, "deduct-"+billID, deposit.DeductCommand{UserID: userID, OrderID: orderID, Amount: amount})
	if err != nil {
		return err // 保持处理中，下一轮用同一幂等键重试
	}
	return inTx(ctx, a.Pool, func(tx pgx.Tx) error {
		b, err := lockBillByID(ctx, tx, billID)
		if err != nil {
			return err
		}
		switch res.Result {
		case "成功":
			return a.applyDeposit(ctx, tx, b, res.Deducted, SystemActor)
		case "失败":
			if b.PendingKind != nil && *b.PendingKind == pendDeduct {
				b.clearPending()
			}
			return a.toArrears(ctx, tx, b, causeDepositShort, SystemActor)
		default:
			return nil // 结果未知：等"押金已抵扣"事件或下一轮重试
		}
	})
}

// RunOverdue 用户最早一笔欠费超过参数天数时发一次"欠费超期"（R-14）
func (a *App) RunOverdue(ctx context.Context) error {
	params, err := getParams(ctx, a.Pool)
	if err != nil || !params.Known {
		return err
	}
	now := a.Now()
	limit := now.Add(-time.Duration(params.ArrearsFreezeDays) * 24 * time.Hour)
	rows, err := a.Pool.Query(ctx, `SELECT user_id, MIN(bill_pushed_at) FROM bills WHERE pay_status=$1 AND user_id NOT IN (SELECT user_id FROM arrears_overdue)
		GROUP BY user_id HAVING MIN(bill_pushed_at) < $2`, stArrears, limit)
	if err != nil {
		return err
	}
	type hit struct {
		user   string
		oldest time.Time
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.user, &h.oldest); err != nil {
			rows.Close()
			return err
		}
		hits = append(hits, h)
	}
	rows.Close()
	for _, h := range hits {
		err := inTx(ctx, a.Pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `INSERT INTO arrears_overdue (user_id, notified_at) VALUES ($1,$2) ON CONFLICT DO NOTHING`, h.user, now)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			total, err := userArrearsTotal(ctx, tx, h.user)
			if err != nil {
				return err
			}
			return emit(ctx, tx, "bill.arrears.overdue", h.user, SystemActor, now, map[string]any{
				"userId": h.user, "userArrearsTotal": total, "oldestAt": h.oldest, "days": int(now.Sub(h.oldest).Hours() / 24),
			})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// payError 用户支付的失败结果
type payError struct {
	code, msg string
	paymentID string
}

func (e *payError) Error() string { return e.msg }

// payIntent 一键支付第一步的结果
type payIntent struct {
	Bill      *Bill
	OrderID   string
	PaymentID string
	Amount    int64
	PayParams json.RawMessage // 调起参数，原样透传；已直接付清为空
	Paid      bool            // 模拟渠道直接成功
}

// hasIntent 该单有一笔已向 pay 发起、等用户调起收银台的支付（可重复取同一份调起参数）
func (b *Bill) hasIntent() bool {
	return b.PendingKind != nil && *b.PendingKind == pendOneTap && b.PendingPaymentID != nil && nullJSON(b.PendingParams) != nil
}

// intentTTL 调起参数的有效时长：超过仍没有支付结果就放开该单，用户可以重新发起（迟到的成功事件照常入账）
const intentTTL = 30 * time.Minute

// payOne 一键支付第一步：按该单未付金额向 pay 发起支付（订单支付或欠费补付），返回调起参数。
// 支付结果以 pay.payment.succeeded 事件为准；模拟渠道直接成功时当场入账。
func (a *App) payOne(ctx context.Context, userID, orderID, attempt string) (*payIntent, error) {
	var b *Bill
	var typ string
	var existing *payIntent
	err := inTx(ctx, a.Pool, func(tx pgx.Tx) error {
		var err error
		b, err = lockBill(ctx, tx, orderID)
		if err != nil {
			return err
		}
		if b.UserID != userID {
			return errNotFound
		}
		if (b.PayStatus != stPending && b.PayStatus != stArrears) || b.unpaid() <= 0 {
			return &payError{code: "NOTHING_TO_PAY", msg: "没有需要支付的费用"}
		}
		if b.hasIntent() {
			existing = &payIntent{Bill: b, OrderID: orderID, PaymentID: *b.PendingPaymentID, Amount: b.unpaid(), PayParams: b.PendingParams}
			return nil
		}
		if b.pending() {
			return &payError{code: "CONFLICT", msg: "该单有一笔扣款结果尚未返回，稍后再试"}
		}
		typ = "订单支付"
		if b.PayStatus == stArrears {
			typ = "欠费补付"
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	amount := b.unpaid()
	res, err := a.Pay.StartOneTap(ctx, "pay-"+b.BillID+"-"+attempt, pay.OneTapPaymentCommand{
		BizNo: b.BillID, Type: typ, UserID: userID, OrderID: orderID, Amount: amount, Channel: collectChannel(b.Channel),
	})
	if err != nil {
		return nil, &payError{code: "CONFLICT", msg: "支付服务暂时没有响应，请稍后再试"}
	}
	var out *payIntent
	var unknown bool
	err = inTx(ctx, a.Pool, func(tx pgx.Tx) error {
		b, err := lockBill(ctx, tx, orderID)
		if err != nil {
			return err
		}
		switch {
		case res.Result == "成功":
			amt := res.Amount
			if amt <= 0 {
				amt = amount
			}
			if err := a.applyChannelPaid(ctx, tx, b, res.PaymentID, typ, amt, Actor{Kind: "用户", ID: userID}); err != nil {
				return err
			}
			out = &payIntent{Bill: b, OrderID: orderID, PaymentID: res.PaymentID, Amount: amount, Paid: true}
			return nil
		case res.Result == "失败":
			return nil
		case res.HasPayParams(): // 两步支付：等用户调起收银台，结果以支付成功事件为准
			if b.unpaid() > 0 && (!b.pending() || str(b.PendingKind) == pendOneTap) {
				b.setIntent(res.PaymentID, res.PayParams, a.Now())
			}
			out = &payIntent{Bill: b, OrderID: orderID, PaymentID: res.PaymentID, Amount: amount, PayParams: res.PayParams}
			return saveMoney(ctx, tx, b)
		default: // 结果未知且没有调起参数：记处理中并提交，不重复发起
			unknown = true
			if b.unpaid() > 0 && !b.pending() {
				b.setPending(pendOneTap, res.PaymentID)
			}
			return saveMoney(ctx, tx, b)
		}
	})
	if err != nil {
		return nil, err
	}
	if unknown {
		return nil, &payError{code: "CONFLICT", msg: "支付结果尚未返回，稍后再试", paymentID: res.PaymentID}
	}
	if out == nil {
		reason := res.Reason()
		if reason == "" {
			reason = "渠道未返回原因"
		}
		return nil, &payError{code: "PAY_DECLINED", msg: "支付没有成功：" + reason, paymentID: res.PaymentID}
	}
	return out, nil
}

// ReleaseStaleIntents 放开调起参数已过期、始终没有支付结果的账单与月卡购买
func (a *App) ReleaseStaleIntents(ctx context.Context) error {
	cut := a.Now().Add(-intentTTL)
	if _, err := a.Pool.Exec(ctx, `UPDATE bills SET pending_kind=NULL, pending_payment_id=NULL, pending_params=NULL, pending_at=NULL, updated_at=now()
		WHERE pending_kind=$1 AND pending_params IS NOT NULL AND pending_at < $2`, pendOneTap, cut); err != nil {
		return err
	}
	_, err := a.Pool.Exec(ctx, `UPDATE month_cards SET state=$1, pay_params=NULL WHERE state=$2 AND pay_params IS NOT NULL AND created_at < $3`, cardFailed, cardPaying, cut)
	return err
}

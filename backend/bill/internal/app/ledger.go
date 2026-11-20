package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/bill/internal/billing"
)

// 账单状态机：动钱只记一次（receipts），状态只按金额推进，事件随状态变化在同一事务里写发件表。

// addReceipt 记一笔动钱；键已存在返回 false（重复到达）
func addReceipt(ctx context.Context, tx pgx.Tx, key, billID, kind string, amount int64, at time.Time) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO receipts (receipt_key, bill_id, kind, amount, at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, key, billID, kind, amount, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

type paidPayload struct {
	OrderID         string    `json:"orderId"`
	UserID          string    `json:"userId"`
	PayStatus       string    `json:"payStatus"`
	ChannelPaid     int64     `json:"channelPaid"`
	DepositDeducted int64     `json:"depositDeducted"`
	PaidAt          time.Time `json:"paidAt"`
	Degraded        bool      `json:"degraded"`
}

// settle 金额变化后推进状态：未付归零则付清（发"账单已付清"），欠费单付清后检查欠费是否结清
func (a *App) settle(ctx context.Context, tx pgx.Tx, b *Bill, actor Actor) error {
	now := a.Now()
	prev := b.PayStatus
	if b.unpaid() <= 0 {
		switch prev {
		case stArrears:
			b.PayStatus = stArrearsPaid
			b.ArrearsPaidAt = &now
		case stPending:
			if b.ChannelPaid == 0 && b.DepositDeducted > 0 {
				b.PayStatus = stDeposit
			} else {
				b.PayStatus = stPaid
			}
		}
	}
	if err := saveMoney(ctx, tx, b); err != nil {
		return err
	}
	if prev == b.PayStatus {
		return nil
	}
	if err := emit(ctx, tx, "bill.bill.paid", b.OrderID, actor, now, paidPayload{
		OrderID: b.OrderID, UserID: b.UserID, PayStatus: b.PayStatus, ChannelPaid: b.ChannelPaid, DepositDeducted: b.DepositDeducted, PaidAt: now, Degraded: b.Degraded,
	}); err != nil {
		return err
	}
	if prev == stArrears {
		return a.maybeCleared(ctx, tx, b.UserID, actor)
	}
	return nil
}

// maybeCleared 欠费合计归零时发"欠费结清"，并结束本轮超期记录
func (a *App) maybeCleared(ctx context.Context, tx pgx.Tx, userID string, actor Actor) error {
	total, err := userArrearsTotal(ctx, tx, userID)
	if err != nil || total > 0 {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM arrears_overdue WHERE user_id=$1`, userID); err != nil {
		return err
	}
	now := a.Now()
	return emit(ctx, tx, "bill.arrears.cleared", userID, actor, now, map[string]any{"userId": userID, "clearedAt": now})
}

// 欠费原因（contracts/events/bill.md）
const (
	causeChargeFailed = "扣款失败"
	causeDepositShort = "押金不足"
	causeExpired      = "期限届满未付"
	causePursue       = "核验未通过追缴"
)

// toArrears 待支付且未付大于 0 的单转欠费，发"欠费产生"
func (a *App) toArrears(ctx context.Context, tx pgx.Tx, b *Bill, cause string, actor Actor) error {
	if b.PayStatus != stPending || b.unpaid() <= 0 {
		return saveMoney(ctx, tx, b)
	}
	now := a.Now()
	b.PayStatus = stArrears
	b.ArrearsAt = &now
	b.ArrearsCause = &cause
	if err := saveMoney(ctx, tx, b); err != nil {
		return err
	}
	total, err := userArrearsTotal(ctx, tx, b.UserID)
	if err != nil {
		return err
	}
	return emit(ctx, tx, "bill.arrears.created", b.OrderID, actor, now, map[string]any{
		"orderId": b.OrderID, "userId": b.UserID, "amount": b.unpaid(), "userArrearsTotal": total, "cause": cause, "createdAt": now, "degraded": b.Degraded,
	})
}

// applyChannelPaid 渠道收款成功（免押扣款、一键支付、欠费补付）
func (a *App) applyChannelPaid(ctx context.Context, tx pgx.Tx, b *Bill, paymentID, typ string, amount int64, actor Actor) error {
	added, err := addReceipt(ctx, tx, paymentID, b.BillID, typ, amount, a.Now())
	if err != nil || !added {
		return err
	}
	b.ChannelPaid += amount
	if b.PendingKind != nil && *b.PendingKind != pendDeduct {
		b.clearPending()
	}
	if typ == "订单支付" && str(b.PayMethod) != byCredit {
		b.PayMethod = sp(byOneTap)
	}
	return a.settle(ctx, tx, b, actor)
}

// applyDeposit 押金抵扣到账；不足部分转欠费；留痕
func (a *App) applyDeposit(ctx context.Context, tx pgx.Tx, b *Bill, amount int64, actor Actor) error {
	if amount > 0 {
		added, err := addReceipt(ctx, tx, "deduct:"+b.OrderID, b.BillID, "押金抵扣", amount, a.Now())
		if err != nil {
			return err
		}
		if added {
			b.DepositDeducted += amount
			b.PayMethod = sp(methodDeposit)
			reason := "一键支付期限届满未付，按 BR-22 从本人押金抵扣"
			if err := emitAudit(ctx, tx, actor, a.Now(), Audit{Action: "抵扣押金", TargetType: "order", TargetID: b.OrderID,
				Summary: fmt.Sprintf("订单 %s 一键支付期限届满未付，从押金抵扣 %s", b.OrderID, billing.Yuan(amount)), Reason: &reason,
				After: map[string]any{"depositDeducted": b.DepositDeducted}}); err != nil {
				return err
			}
		}
	}
	if b.PendingKind != nil && *b.PendingKind == pendDeduct {
		b.clearPending()
	}
	if b.unpaid() > 0 {
		return a.toArrears(ctx, tx, b, causeDepositShort, actor)
	}
	return a.settle(ctx, tx, b, actor)
}

// lockBill 事务内按订单号锁账单
func lockBill(ctx context.Context, tx pgx.Tx, orderID string) (*Bill, error) {
	return getBill(ctx, tx, `order_id=$1 FOR UPDATE`, orderID)
}

func lockBillByID(ctx context.Context, tx pgx.Tx, billID string) (*Bill, error) {
	return getBill(ctx, tx, `bill_id=$1 FOR UPDATE`, billID)
}

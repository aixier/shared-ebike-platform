package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"bike/bill/internal/billing"
	"bike/bill/internal/clients/pay"
)

// 退款命令（R-10 BR-06 R-11）：退不退、退多少由 cs 与人决定，这里只执行。

// RefundCommand cs 发来的退款命令
type RefundCommand struct {
	OrderID  string `json:"orderId"`
	Amount   int64  `json:"amount"`
	Reason   string `json:"reason"`
	TicketID string `json:"ticketId"`
	Actor    *Actor `json:"actor"`
}

// RefundResult 命令结果
type RefundResult struct {
	Result        string  `json:"result"`
	FailCode      *string `json:"failCode"`
	FailReason    *string `json:"failReason"`
	OrderID       string  `json:"orderId"`
	PaymentID     *string `json:"paymentId"`
	Amount        int64   `json:"amount"`
	Refunded      int64   `json:"refunded"`
	RefundStatus  *string `json:"refundStatus"`
	AwaitingPayee bool    `json:"awaitingPayee"`
}

func refundFail(b *Bill, amount int64, code, reason string) RefundResult {
	rs := billing.RefundStatusOf(b.amounts(), b.RefundFailedPaymentID != nil)
	return RefundResult{Result: "失败", FailCode: &code, FailReason: &reason, OrderID: b.OrderID, Amount: amount, Refunded: b.Refunded, RefundStatus: &rs}
}

// inflightRefunds 已发出、结果未定的退款合计
func inflightRefunds(ctx context.Context, q querier, billID string) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(amount),0) FROM refunds WHERE bill_id=$1 AND status='处理中'`, billID).Scan(&n)
	return n, err
}

// Refund 执行退款：校验可退金额，调 pay 原路退回，更新已退款与退款状态。幂等键由调用方给
func (a *App) Refund(ctx context.Context, idem string, cmd RefundCommand) (RefundResult, error) {
	actor := SystemActor
	if cmd.Actor != nil {
		actor = *cmd.Actor
	}
	var out RefundResult
	var send bool
	var b0 *Bill
	err := inTx(ctx, a.Pool, func(tx pgx.Tx) error {
		b, err := lockBill(ctx, tx, cmd.OrderID)
		if err != nil {
			return err
		}
		b0 = b
		if b.ChannelPaid <= 0 {
			out = refundFail(b, cmd.Amount, "NOTHING_TO_REFUND", "该单没有渠道收款，押金抵扣部分不在线退")
			return nil
		}
		inflight, err := inflightRefunds(ctx, tx, b.BillID)
		if err != nil {
			return err
		}
		if cmd.Amount > b.amounts().Refundable()-inflight {
			out = refundFail(b, cmd.Amount, "EXCEED_REFUNDABLE", fmt.Sprintf("超过可退金额，最多可退 %s", billing.Yuan(b.amounts().Refundable()-inflight)))
			return nil
		}
		actorJSON, _ := json.Marshal(actor)
		if _, err := tx.Exec(ctx, `INSERT INTO refunds (idem_key, bill_id, amount, status, ticket_id, reason, actor, created_at) VALUES ($1,$2,$3,'处理中',$4,$5,$6,$7)`,
			idem, b.BillID, cmd.Amount, nullIfEmpty(cmd.TicketID), cmd.Reason, actorJSON, a.Now()); err != nil {
			return err
		}
		send = true
		return nil
	})
	if err != nil || !send {
		return out, err
	}
	pc := &pay.Actor{Kind: actor.Kind, ID: actor.ID, Name: actor.Name}
	res, err := a.Pay.Refund(ctx, "refund-"+idem, pay.RefundCommand{UserID: b0.UserID, OrderID: b0.OrderID, Amount: cmd.Amount, TicketID: cmd.TicketID, Actor: pc, Reason: cmd.Reason})
	if err != nil {
		// 调用失败：结果未知，退款记录保持处理中，以 pay 的退款事件为准
		a.Log.Warn("退款调用失败", "orderId", cmd.OrderID, "err", err)
		rs := billing.RefundStatusOf(b0.amounts(), b0.RefundFailedPaymentID != nil)
		return RefundResult{Result: "结果未知", OrderID: b0.OrderID, Amount: cmd.Amount, Refunded: b0.Refunded, RefundStatus: &rs}, nil
	}
	err = inTx(ctx, a.Pool, func(tx pgx.Tx) error {
		b, err := lockBill(ctx, tx, cmd.OrderID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE refunds SET payment_id=$2 WHERE idem_key=$1`, idem, res.PaymentID); err != nil {
			return err
		}
		switch res.Result {
		case "成功":
			if err := a.applyRefund(ctx, tx, b, res.PaymentID, nil, cmd.Amount, actor); err != nil {
				return err
			}
			pid := res.PaymentID
			rs := billing.RefundStatusOf(b.amounts(), b.RefundFailedPaymentID != nil)
			out = RefundResult{Result: "成功", OrderID: b.OrderID, PaymentID: &pid, Amount: cmd.Amount, Refunded: b.Refunded, RefundStatus: &rs}
		case "失败":
			if err := a.applyRefundFailed(ctx, tx, b, res.PaymentID); err != nil {
				return err
			}
			code := "CHANNEL_FAILED"
			reason := res.Reason()
			pid := res.PaymentID
			rs := billing.RefundStatusOf(b.amounts(), b.RefundFailedPaymentID != nil)
			out = RefundResult{Result: "失败", FailCode: &code, FailReason: &reason, OrderID: b.OrderID, PaymentID: &pid, Amount: cmd.Amount,
				Refunded: b.Refunded, RefundStatus: &rs, AwaitingPayee: true}
		default:
			pid := res.PaymentID
			rs := billing.RefundStatusOf(b.amounts(), b.RefundFailedPaymentID != nil)
			out = RefundResult{Result: "结果未知", OrderID: b.OrderID, PaymentID: &pid, Amount: cmd.Amount, Refunded: b.Refunded, RefundStatus: &rs}
		}
		return emitAudit(ctx, tx, actor, a.Now(), Audit{Action: "执行退款", TargetType: "order", TargetID: b.OrderID,
			Summary: fmt.Sprintf("订单 %s 原路退款 %s：%s", b.OrderID, billing.Yuan(cmd.Amount), out.Result), Reason: &cmd.Reason,
			After: map[string]any{"refunded": b.Refunded, "ticketId": nullIfEmpty(cmd.TicketID)}})
	})
	return out, err
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// applyRefund 退款成功（原路或人工转账）：累计已退款、更新退款状态、发"已退款"
func (a *App) applyRefund(ctx context.Context, tx pgx.Tx, b *Bill, paymentID string, related *string, amount int64, actor Actor) error {
	added, err := addReceipt(ctx, tx, paymentID, b.BillID, "退款", amount, a.Now())
	if err != nil || !added {
		return err
	}
	b.Refunded += amount
	if related != nil && b.RefundFailedPaymentID != nil && *related == *b.RefundFailedPaymentID {
		b.RefundFailedPaymentID = nil // 人工转账已补上原路失败的那笔
	}
	if err := saveMoney(ctx, tx, b); err != nil {
		return err
	}
	var ticket *string
	if err := tx.QueryRow(ctx, `UPDATE refunds SET status='成功' WHERE payment_id=$1 RETURNING ticket_id`, paymentID).Scan(&ticket); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if ticket == nil && related != nil {
		_ = tx.QueryRow(ctx, `SELECT ticket_id FROM refunds WHERE payment_id=$1`, *related).Scan(&ticket)
	}
	kind := "部分退款"
	if b.amounts().NetReceived() <= 0 {
		kind = "全额退款"
	}
	now := a.Now()
	return emit(ctx, tx, "bill.bill.refunded", b.OrderID, actor, now, map[string]any{
		"orderId": b.OrderID, "userId": b.UserID, "brandId": b.BrandID, "operatorId": b.OperatorID, "amount": amount, "refundedTotal": b.Refunded,
		"netReceived": b.amounts().NetReceived(), "receivable": b.Receivable, "kind": kind, "refundedAt": now, "ticketId": ticket,
	})
}

// applyRefundFailed 原路退回失败：等用户提交收款账户后人工转账
func (a *App) applyRefundFailed(ctx context.Context, tx pgx.Tx, b *Bill, paymentID string) error {
	b.RefundFailedPaymentID = &paymentID
	if _, err := tx.Exec(ctx, `UPDATE refunds SET status='失败' WHERE payment_id=$1`, paymentID); err != nil {
		return err
	}
	return saveMoney(ctx, tx, b)
}

func (a *App) onRefundResult(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e payEvent
	if err := json.Unmarshal(env.Payload, &e); err != nil {
		return nil
	}
	if (e.Type != "退款" && e.Type != "人工转账退款") || e.OrderID == nil {
		return nil
	}
	b, err := lockBill(ctx, tx, *e.OrderID)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if env.Type == "pay.refund.succeeded" {
		return a.applyRefund(ctx, tx, b, e.PaymentID, e.RelatedPaymentID, e.Amount, env.Actor)
	}
	if e.Type == "退款" {
		return a.applyRefundFailed(ctx, tx, b, e.PaymentID)
	}
	return nil
}

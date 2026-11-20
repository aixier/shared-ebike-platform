package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// 订阅的事件（contracts/events/pay.md、rule.md）。
var Subscriptions = map[string][]string{
	"EVT_PAY":   {"pay.refund.succeeded", "pay.refund.failed", "pay.payee.submitted"},
	"EVT_RULE":  {"rule.params.changed"},
	"EVT_FLEET": {"fleet.operator.exited"},
}

type payRefundPayload struct {
	PaymentID        string     `json:"paymentId"`
	Type             string     `json:"type"`
	Account          string     `json:"account"`
	UserID           string     `json:"userId"`
	OrderID          *string    `json:"orderId"`
	Amount           int64      `json:"amount"`
	RelatedPaymentID *string    `json:"relatedPaymentId"`
	FailReason       string     `json:"failReason"`
	CompletedAt      *time.Time `json:"completedAt"`
}

type payeePayload struct {
	PaymentID string  `json:"paymentId"`
	Account   string  `json:"account"`
	UserID    string  `json:"userId"`
	OrderID   *string `json:"orderId"`
}

type paramsPayload struct {
	CSRefundLimit *int64     `json:"csRefundLimit"`
	ChangedAt     *time.Time `json:"changedAt"`
}

// HandleEvent 处理一条订阅的事件：按 eventId 去重后在同一事务里更新工单。返回错误时不确认，由总线重投。
func (a *App) HandleEvent(ctx context.Context, data []byte) error {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		// 信封都解析不了，重投也没用：记日志后确认
		return nil
	}
	if env.EventID == "" {
		return nil
	}
	return a.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO inbox (event_id, type) VALUES ($1, $2) ON CONFLICT DO NOTHING`, env.EventID, env.Type)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // 重复送达
		}
		switch env.Type {
		case "pay.refund.succeeded":
			var p payRefundPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return nil
			}
			by := env.Actor.ID
			if by == "" {
				by = "AC0900"
			}
			return a.onRefundSucceeded(ctx, tx, p, by)
		case "pay.refund.failed":
			var p payRefundPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return nil
			}
			return a.onRefundFailed(ctx, tx, p)
		case "pay.payee.submitted":
			var p payeePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return nil
			}
			return a.onPayeeSubmitted(ctx, tx, p)
		case "fleet.operator.exited":
			var p struct {
				OperatorID    string     `json:"operatorId"`
				ExitedAt      *time.Time `json:"exitedAt"`
				ReadonlyUntil *time.Time `json:"readonlyUntil"`
			}
			if err := json.Unmarshal(env.Payload, &p); err != nil || p.OperatorID == "" {
				return nil
			}
			at := env.OccurredAt
			if p.ExitedAt != nil {
				at = *p.ExitedAt
			}
			_, err := tx.Exec(ctx, `INSERT INTO exited_operators (operator_id, exited_at, readonly_until) VALUES ($1, $2, $3)
				ON CONFLICT (operator_id) DO UPDATE SET exited_at = EXCLUDED.exited_at, readonly_until = EXCLUDED.readonly_until`,
				p.OperatorID, at, p.ReadonlyUntil)
			return err
		case "rule.params.changed":
			var p paramsPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil || p.CSRefundLimit == nil {
				return nil
			}
			_, err := tx.Exec(ctx, `INSERT INTO platform_params (id, refund_limit, changed_at) VALUES (1, $1, $2)
				ON CONFLICT (id) DO UPDATE SET refund_limit = EXCLUDED.refund_limit, changed_at = EXCLUDED.changed_at
				WHERE platform_params.changed_at IS NULL OR EXCLUDED.changed_at IS NULL OR EXCLUDED.changed_at >= platform_params.changed_at`,
				*p.CSRefundLimit, p.ChangedAt)
			return err
		}
		return nil
	})
}

// findTicketByPayment 按退款流水号找工单；找不到时按订单号找等待退款结果的工单。
func findTicketByPayment(ctx context.Context, tx pgx.Tx, paymentID string, orderID *string) (*Ticket, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT ticket_id FROM ticket_actions WHERE payment_id = $1 ORDER BY id DESC LIMIT 1`, paymentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) && orderID != nil {
		err = tx.QueryRow(ctx, `SELECT ticket_id FROM tickets WHERE order_id = $1 AND refund_pending ORDER BY created_at DESC LIMIT 1`, *orderID).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return loadTicket(ctx, tx, id, true)
}

func (a *App) onRefundSucceeded(ctx context.Context, tx pgx.Tx, p payRefundPayload, by string) error {
	now := a.now()
	switch p.Type {
	case "退款":
		t, err := findTicketByPayment(ctx, tx, p.PaymentID, p.OrderID)
		if err != nil || t == nil {
			return err
		}
		if !t.RefundPending {
			return nil // 命令同步返回成功时已更新过
		}
		t.RefundPending, t.Status, t.ClosedAt = false, StatusResolved, &now
		if err := addMessage(ctx, tx, t, FromStaff, fmt.Sprintf("已原路退回 %s，一般 1～3 天到账。", yuan(p.Amount)), now); err != nil {
			return err
		}
		if err := saveTicket(ctx, tx, t); err != nil {
			return err
		}
		return emitTicketChanged(ctx, tx, t, "已退款", SystemActor, now)
	case "人工转账退款":
		if p.RelatedPaymentID == nil {
			return nil
		}
		var id string
		err := tx.QueryRow(ctx, `SELECT ticket_id FROM tickets WHERE refund_failed_payment_id = $1 ORDER BY created_at DESC LIMIT 1`, *p.RelatedPaymentID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		t, err := loadTicket(ctx, tx, id, true)
		if err != nil {
			return err
		}
		for _, act := range t.Actions {
			if act.Type == "人工转账" && act.PaymentID != nil && *act.PaymentID == p.PaymentID {
				return nil
			}
		}
		pid := p.PaymentID
		if err := addAction(ctx, tx, t, "人工转账", &pid, by, now); err != nil {
			return err
		}
		t.AwaitingPayee, t.Status, t.ClosedAt = false, StatusResolved, &now
		if err := addMessage(ctx, tx, t, FromStaff, fmt.Sprintf("退款 %s 已人工转账到你填写的收款账户。", yuan(p.Amount)), now); err != nil {
			return err
		}
		if err := saveTicket(ctx, tx, t); err != nil {
			return err
		}
		return emitTicketChanged(ctx, tx, t, "已退款", SystemActor, now)
	}
	return nil
}

func (a *App) onRefundFailed(ctx context.Context, tx pgx.Tx, p payRefundPayload) error {
	if p.Type != "退款" {
		return nil
	}
	t, err := findTicketByPayment(ctx, tx, p.PaymentID, p.OrderID)
	if err != nil || t == nil {
		return err
	}
	if t.AwaitingPayee && t.RefundFailedPaymentID != nil && *t.RefundFailedPaymentID == p.PaymentID {
		return nil // 命令同步返回失败时已更新过
	}
	now := a.now()
	pid := p.PaymentID
	t.RefundPending, t.AwaitingPayee, t.RefundFailedPaymentID = false, true, &pid
	t.Status, t.ClosedAt = StatusProcessing, nil
	if err := addMessage(ctx, tx, t, FromStaff, fmt.Sprintf("退款 %s 原路退回没有成功。请在下方填写本人实名的收款账户，平台公司将人工转账。", yuan(p.Amount)), now); err != nil {
		return err
	}
	if err := saveTicket(ctx, tx, t); err != nil {
		return err
	}
	return emitTicketChanged(ctx, tx, t, "客服回复", SystemActor, now)
}

func (a *App) onPayeeSubmitted(ctx context.Context, tx pgx.Tx, p payeePayload) error {
	if p.Account != "" && p.Account != "骑行收入户" {
		return nil // 退押的收款账户归 deposit
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT ticket_id FROM tickets WHERE refund_failed_payment_id = $1 AND awaiting_payee ORDER BY created_at DESC LIMIT 1`, p.PaymentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	t, err := loadTicket(ctx, tx, id, true)
	if err != nil {
		return err
	}
	now := a.now()
	t.AwaitingPayee = false
	if err := addMessage(ctx, tx, t, FromStaff, "已收到你填写的收款账户，平台公司将尽快人工转账。", now); err != nil {
		return err
	}
	if err := saveTicket(ctx, tx, t); err != nil {
		return err
	}
	return emitTicketChanged(ctx, tx, t, "客服回复", SystemActor, now)
}

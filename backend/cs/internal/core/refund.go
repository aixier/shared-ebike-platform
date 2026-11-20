package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/cs/internal/auth"
	"bike/cs/internal/clients/bill"
	"bike/cs/internal/httpx"
)

// RefundResult 是 cs.yaml RefundResult。
type RefundResult struct {
	PaymentID  string  `json:"paymentId"`
	Amount     int64   `json:"amount"`
	Status     string  `json:"status"` // 成功｜失败｜结果未知
	FailReason *string `json:"failReason"`
}

// 退款提议的类型由金额与可退金额算出（算术，不是判断）。
func refundKind(amount, refundableTotal int64) string {
	switch {
	case amount <= 0:
		return "不退款"
	case amount >= refundableTotal:
		return "全额退款"
	}
	return "部分退款"
}

// ConfirmRefund 客服确认退款。没有人确认不发退款命令；超出授权额度且当前账号不是客服主管时转"待主管确认"。
// amount 为 nil 时取退款提议的金额；没有提议时客服直接填金额，提议人记为该客服。
func (a *App) ConfirmRefund(ctx context.Context, ad *auth.Admin, ticketID string, amount *int64, reason string) (bool, *RefundResult, *Ticket, error) {
	why, err := checkReason(reason)
	if err != nil {
		return false, nil, nil, err
	}
	if amount != nil && *amount <= 0 {
		return false, nil, nil, httpx.E("INVALID", "退款金额须大于 0")
	}
	now := a.now()
	var (
		t       *Ticket
		pending bool
		res     *RefundResult
	)
	err = a.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		if t, err = loadTicket(ctx, tx, ticketID, true); err != nil {
			return err
		}
		if err := scopeCheck(ad, t); err != nil {
			return err
		}
		if t.Status == StatusResolved {
			return httpx.E("ALREADY_DONE", "工单已解决")
		}
		if t.OrderID == nil {
			return httpx.E("INVALID", "该工单没有关联订单，不能退款")
		}
		amt := int64(0)
		if amount != nil {
			amt = *amount
		} else if t.RefundProposal != nil {
			amt = t.RefundProposal.Amount
		}
		if amt <= 0 {
			return httpx.E("INVALID", "请填写退款金额")
		}
		facts, err := a.Bill.GetFacts(ctx, *t.OrderID)
		var he *httpx.Error
		if errors.As(err, &he) && he.Code == "NOT_FOUND" {
			return httpx.E("INVALID", "该订单还没有账单，不能退款")
		}
		if err != nil {
			return err
		}
		if amt > facts.Refundable {
			return httpx.E("INVALID", "超过可原路退回的金额 "+yuan(facts.Refundable))
		}
		limit, err := a.refundLimit(ctx, tx)
		if err != nil {
			return err
		}
		over := amt > limit
		total := facts.Refundable
		if facts.Fee != nil {
			total = facts.Fee.ChannelPaid
		}
		p := t.RefundProposal
		if p == nil || p.Decision == DecisionRejected {
			p = &RefundProposal{Basis: why, ProposedAt: now, ProposedBy: ad.AccountID}
		}
		if p.Amount != amt || p.Kind == "" {
			p.Kind = refundKind(amt, total)
		}
		p.Amount, p.OverLimit = amt, over
		t.RefundProposal = p
		takeOver(t, ad)
		if over && !ad.Has("refund.confirm.overLimit") {
			p.Decision, p.DecidedBy, p.DecidedAt = DecisionSupervisor, nil, nil
			t.Status, t.ClosedAt = StatusSupervisor, nil
			if err := addMessage(ctx, tx, t, FromStaff, fmt.Sprintf("退款 %s 超过客服授权额度，已提交客服主管确认，确认后原路退回，会在这里通知你。", yuan(amt)), now); err != nil {
				return err
			}
			if err := saveTicket(ctx, tx, t); err != nil {
				return err
			}
			if err := emitTicketChanged(ctx, tx, t, "待主管确认", staffActor(ad), now); err != nil {
				return err
			}
			pending = true
			return emitAudit(ctx, tx, staffActor(ad), now, Audit{Action: "确认退款", TargetType: "ticket", TargetID: t.TicketID,
				Summary: fmt.Sprintf("工单 %s 订单 %s 退款 %s 超出授权额度 %s，提交客服主管确认", t.TicketID, *t.OrderID, yuan(amt), yuan(limit)), Reason: &why,
				After: map[string]any{"amount": amt, "decision": p.Decision}})
		}
		decider := ad.AccountID
		p.Decision, p.DecidedBy, p.DecidedAt = DecisionConfirmed, &decider, &now
		res, err = a.executeRefund(ctx, tx, t, ad, amt, over, why, now)
		return err
	})
	if err != nil {
		return false, nil, nil, err
	}
	return pending, res, t, nil
}

// ApproveOverLimit 客服主管确认超额退款。
func (a *App) ApproveOverLimit(ctx context.Context, ad *auth.Admin, ticketID, reason string) (*RefundResult, *Ticket, error) {
	why, err := checkReason(reason)
	if err != nil {
		return nil, nil, err
	}
	now := a.now()
	var (
		t   *Ticket
		res *RefundResult
	)
	err = a.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		if t, err = loadTicket(ctx, tx, ticketID, true); err != nil {
			return err
		}
		if err := scopeCheck(ad, t); err != nil {
			return err
		}
		if t.Status != StatusSupervisor || t.RefundProposal == nil || t.OrderID == nil {
			return httpx.E("CONFLICT", "该工单没有待主管确认的退款")
		}
		p := t.RefundProposal
		decider := ad.AccountID
		p.Decision, p.DecidedBy, p.DecidedAt = DecisionConfirmed, &decider, &now
		res, err = a.executeRefund(ctx, tx, t, ad, p.Amount, true, why, now)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return res, t, nil
}

// executeRefund 调 bill 退款并按结果更新工单。下游幂等键由工单号、退款序号与金额决定，重试不会重复退款。
func (a *App) executeRefund(ctx context.Context, tx pgx.Tx, t *Ticket, ad *auth.Admin, amt int64, over bool, why string, now time.Time) (*RefundResult, error) {
	key := fmt.Sprintf("cs-rf-%s-%d-%d", t.TicketID, t.RefundSeq+1, amt)
	r, err := a.Bill.Refund(ctx, key, bill.RefundCommand{OrderID: *t.OrderID, Amount: amt, Reason: why, TicketID: t.TicketID,
		Actor: bill.Actor{Kind: "人", ID: ad.AccountID, Name: ad.Name}})
	if err != nil {
		return nil, err
	}
	failCode := ""
	if r.FailCode != nil {
		failCode = *r.FailCode
	}
	if r.Result == "失败" && failCode != "CHANNEL_FAILED" {
		msg := "退款没有成功"
		if r.FailReason != nil && *r.FailReason != "" {
			msg = *r.FailReason
		}
		return nil, httpx.E("INVALID", msg)
	}
	t.RefundSeq++
	actType, auditAction := "退款", "确认退款"
	if over {
		actType, auditAction = "超额退款确认", "确认超额退款"
	}
	if err := addAction(ctx, tx, t, actType, r.PaymentID, ad.AccountID, now); err != nil {
		return nil, err
	}
	takeOver(t, ad)
	res := &RefundResult{Amount: amt, Status: r.Result, FailReason: r.FailReason}
	if r.PaymentID != nil {
		res.PaymentID = *r.PaymentID
	}
	change, summary := "已退款", ""
	switch r.Result {
	case "成功":
		t.Status, t.ClosedAt, t.AwaitingPayee, t.RefundPending = StatusResolved, &now, false, false
		if err := addMessage(ctx, tx, t, FromStaff, fmt.Sprintf("已原路退回 %s，一般 1～3 天到账。", yuan(amt)), now); err != nil {
			return nil, err
		}
		summary = "已原路退回"
	case "失败": // CHANNEL_FAILED：原路退回失败，等用户填写本人实名收款账户（R-11）
		t.Status, t.ClosedAt, t.AwaitingPayee, t.RefundPending = StatusProcessing, nil, true, false
		t.RefundFailedPaymentID = r.PaymentID
		if err := addMessage(ctx, tx, t, FromStaff, fmt.Sprintf("退款 %s 原路退回没有成功。请在下方填写本人实名的收款账户，平台公司将人工转账。", yuan(amt)), now); err != nil {
			return nil, err
		}
		change, summary = "客服回复", "原路退回失败，待用户提供收款账户"
	default: // 结果未知：以 pay 的退款结果事件为准
		t.Status, t.ClosedAt, t.RefundPending = StatusProcessing, nil, true
		if err := addMessage(ctx, tx, t, FromStaff, fmt.Sprintf("退款 %s 已提交，正在等待原路退回的结果，有结果会在这里通知你。", yuan(amt)), now); err != nil {
			return nil, err
		}
		change, summary = "客服回复", "退款结果未知，等待渠道结果"
	}
	if err := saveTicket(ctx, tx, t); err != nil {
		return nil, err
	}
	if err := emitTicketChanged(ctx, tx, t, change, staffActor(ad), now); err != nil {
		return nil, err
	}
	return res, emitAudit(ctx, tx, staffActor(ad), now, Audit{Action: auditAction, TargetType: "ticket", TargetID: t.TicketID,
		Summary: fmt.Sprintf("工单 %s 订单 %s 退款 %s，%s", t.TicketID, *t.OrderID, yuan(amt), summary), Reason: &why,
		After: map[string]any{"amount": amt, "paymentId": r.PaymentID, "result": r.Result}})
}

// RejectRefund 驳回退款提议并结案。
func (a *App) RejectRefund(ctx context.Context, ad *auth.Admin, ticketID, reason string) (*Ticket, error) {
	why, err := checkReason(reason)
	if err != nil {
		return nil, err
	}
	now := a.now()
	var t *Ticket
	err = a.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		if t, err = loadTicket(ctx, tx, ticketID, true); err != nil {
			return err
		}
		if err := scopeCheck(ad, t); err != nil {
			return err
		}
		p := t.RefundProposal
		if p == nil {
			return httpx.E("CONFLICT", "该工单没有退款提议")
		}
		if p.Decision != DecisionPending && p.Decision != DecisionSupervisor {
			return httpx.E("CONFLICT", "该退款提议已处理")
		}
		if t.Status == StatusSupervisor && !ad.Has("refund.confirm.overLimit") {
			return httpx.E("OVER_LIMIT_NEEDS_SUPERVISOR", "该退款待客服主管处理")
		}
		decider := ad.AccountID
		p.Decision, p.DecidedBy, p.DecidedAt = DecisionRejected, &decider, &now
		t.Status, t.ClosedAt = StatusResolved, &now
		takeOver(t, ad)
		if err := addMessage(ctx, tx, t, FromStaff, why, now); err != nil {
			return err
		}
		if err := saveTicket(ctx, tx, t); err != nil {
			return err
		}
		if err := emitTicketChanged(ctx, tx, t, "已驳回", staffActor(ad), now); err != nil {
			return err
		}
		return emitAudit(ctx, tx, staffActor(ad), now, Audit{Action: "驳回退款", TargetType: "ticket", TargetID: t.TicketID,
			Summary: fmt.Sprintf("驳回工单 %s 的退款提议 %s", t.TicketID, yuan(p.Amount)), Reason: &why,
			After: map[string]any{"decision": p.Decision}})
	})
	return t, err
}

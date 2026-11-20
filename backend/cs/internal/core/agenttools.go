package core

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/cs/internal/httpx"
)

// 客服智能体的工具在 core 里的落点。这里只做契约校验（枚举、金额范围、时刻范围、状态机），
// 退不退、退多少、按哪个时刻计费由智能体依订单事实快照与规则文本判断，客服确认后才执行。
// trace: backend/cs/README.md「客服智能体」；cs.yaml RefundProposal、ForceEndProposal

var agentActor = ActorRef{Kind: "智能体", ID: AgentAccountID, Name: "客服智能体"}

// AgentTicketView 是智能体读到的工单：订单事实快照、对话、已有提议与授权额度。
type AgentTicketView struct {
	TicketID         string            `json:"ticketId"`
	UserID           string            `json:"userId"`
	OrderID          *string           `json:"orderId"`
	Category         string            `json:"category"`
	Entry            string            `json:"entry"`
	Status           string            `json:"status"`
	Facts            *Facts            `json:"facts"`
	Messages         []Message         `json:"messages"`
	RefundProposal   *RefundProposal   `json:"refundProposal"`
	ForceEndProposal *ForceEndProposal `json:"forceEndProposal"`
	RefundLimit      int64             `json:"refundLimit"`
}

// AgentReadTicket 读订单事实快照与对话。
func (a *App) AgentReadTicket(ctx context.Context, ticketID string) (*AgentTicketView, error) {
	t, err := loadTicket(ctx, a.DB, ticketID, false)
	if err != nil {
		return nil, err
	}
	limit, err := a.refundLimit(ctx, a.DB)
	if err != nil {
		return nil, err
	}
	return &AgentTicketView{TicketID: t.TicketID, UserID: t.UserID, OrderID: t.OrderID, Category: t.Category, Entry: t.Entry, Status: t.Status,
		Facts: t.Facts, Messages: t.Messages, RefundProposal: t.RefundProposal, ForceEndProposal: t.ForceEndProposal, RefundLimit: limit}, nil
}

// RefundLimit 读当前客服退款授权额度（分）。
func (a *App) RefundLimit(ctx context.Context) (int64, error) { return a.refundLimit(ctx, a.DB) }

func (a *App) agentMutate(ctx context.Context, ticketID string, fn func(tx pgx.Tx, t *Ticket, now time.Time) (string, error)) (*Ticket, error) {
	now := a.now()
	var t *Ticket
	err := a.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		if t, err = loadTicket(ctx, tx, ticketID, true); err != nil {
			return err
		}
		change, err := fn(tx, t, now)
		if err != nil {
			return err
		}
		if err := saveTicket(ctx, tx, t); err != nil {
			return err
		}
		if change == "" {
			return nil
		}
		return emitTicketChanged(ctx, tx, t, change, agentActor, now)
	})
	return t, err
}

// AgentReply 智能体在对话里回复用户。
func (a *App) AgentReply(ctx context.Context, ticketID, text string) (*Ticket, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, httpx.E("INVALID", "回复内容不能为空")
	}
	return a.agentMutate(ctx, ticketID, func(tx pgx.Tx, t *Ticket, now time.Time) (string, error) {
		return "", addMessage(ctx, tx, t, FromAgent, text, now)
	})
}

// AgentSetCategory 智能体按留言与订单事实判定类别。
func (a *App) AgentSetCategory(ctx context.Context, ticketID, category string) (*Ticket, error) {
	if !Categories[category] {
		return nil, httpx.E("INVALID", "类别不对")
	}
	return a.agentMutate(ctx, ticketID, func(tx pgx.Tx, t *Ticket, now time.Time) (string, error) {
		t.Category = category
		return "", nil
	})
}

// RefundProposalInput 是写退款提议的输入。
type RefundProposalInput struct {
	Amount int64  `json:"amount"`
	Kind   string `json:"kind"`
	Basis  string `json:"basis"`
}

// AgentProposeRefund 写退款提议（待客服确认）。不发退款命令。
func (a *App) AgentProposeRefund(ctx context.Context, ticketID string, in RefundProposalInput) (*Ticket, error) {
	if !RefundKind[in.Kind] {
		return nil, httpx.E("INVALID", "提议类型须为 全额退款、部分退款 或 不退款")
	}
	if strings.TrimSpace(in.Basis) == "" {
		return nil, httpx.E("INVALID", "提议须写明依据")
	}
	if in.Kind == "不退款" && in.Amount != 0 {
		return nil, httpx.E("INVALID", "不退款的提议金额须为 0")
	}
	if in.Kind != "不退款" && in.Amount <= 0 {
		return nil, httpx.E("INVALID", "退款提议金额须大于 0")
	}
	limit, err := a.refundLimit(ctx, a.DB)
	if err != nil {
		return nil, err
	}
	return a.agentMutate(ctx, ticketID, func(tx pgx.Tx, t *Ticket, now time.Time) (string, error) {
		if t.OrderID == nil || t.Facts == nil {
			return "", httpx.E("CONFLICT", "该工单没有关联订单，不能提议退款")
		}
		if t.Status == StatusResolved || t.Status == StatusSupervisor {
			return "", httpx.E("CONFLICT", "工单当前状态不能写退款提议")
		}
		if p := t.RefundProposal; p != nil && p.Decision != DecisionPending && p.Decision != DecisionRejected {
			return "", httpx.E("CONFLICT", "已有处理过的退款提议")
		}
		if t.Facts.NetReceived != nil && in.Amount > *t.Facts.NetReceived {
			return "", httpx.E("INVALID", "提议金额超过订单实收 "+yuan(*t.Facts.NetReceived))
		}
		t.RefundProposal = &RefundProposal{Amount: in.Amount, Kind: in.Kind, Basis: strings.TrimSpace(in.Basis), ProposedAt: now,
			ProposedBy: AgentAccountID, Decision: DecisionPending, OverLimit: in.Amount > limit}
		return "退款提议", nil
	})
}

// ForceEndProposalInput 是写强制结束提议的输入。
type ForceEndProposalInput struct {
	BilledEndAt time.Time `json:"billedEndAt"`
	StationID   *string   `json:"stationId"`
}

// AgentProposeForceEnd 写强制结束提议（待客服确认）。不发强制结束命令。
func (a *App) AgentProposeForceEnd(ctx context.Context, ticketID string, in ForceEndProposalInput) (*Ticket, error) {
	return a.agentMutate(ctx, ticketID, func(tx pgx.Tx, t *Ticket, now time.Time) (string, error) {
		if t.OrderID == nil || t.Facts == nil {
			return "", httpx.E("CONFLICT", "该工单没有关联订单，不能提议强制结束")
		}
		if t.Status == StatusResolved || t.ForceEnd != nil {
			return "", httpx.E("CONFLICT", "工单当前状态不能写强制结束提议")
		}
		end := in.BilledEndAt.In(CST).Truncate(time.Second)
		if t.Facts.StartAt == nil || end.Before(*t.Facts.StartAt) || end.After(now) {
			return "", httpx.E("INVALID", "计费终点须在开锁之后、现在之前")
		}
		t.ForceEndProposal = &ForceEndProposal{BilledEndAt: end, StationID: in.StationID}
		return "强制结束提议", nil
	})
}

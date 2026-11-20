package core

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"bike/cs/internal/auth"
	"bike/cs/internal/httpx"
)

// OpenTicketRequest 是新建对话的请求。
type OpenTicketRequest struct {
	OrderID  *string `json:"orderId"`
	Entry    string  `json:"entry"`
	Text     string  `json:"text"`
	Category *string `json:"category"`
}

// OpenTicket 用户新建对话：带订单号时从 ride、bill 读一次订单事实并保存为快照。
// 类别取页面预选，未选时为"费用咨询"；客服智能体接入后按留言重新判定（见 AgentSetCategory）。
func (a *App) OpenTicket(ctx context.Context, userID string, req OpenTicketRequest) (*Ticket, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return nil, httpx.E("INVALID", "请输入要咨询的问题")
	}
	if !Entries[req.Entry] {
		return nil, httpx.E("INVALID", "发起入口不对")
	}
	category := "费用咨询"
	if req.Category != nil && *req.Category != "" {
		if !Categories[*req.Category] {
			return nil, httpx.E("INVALID", "类别不对")
		}
		category = *req.Category
	}
	var facts *Facts
	var orderID, operatorID *string
	if req.OrderID != nil && strings.TrimSpace(*req.OrderID) != "" {
		oid := strings.TrimSpace(*req.OrderID)
		f, op, err := a.snapshot(ctx, userID, oid)
		if err != nil {
			return nil, err
		}
		facts, orderID, operatorID = f, &oid, &op
	}
	now := a.now()
	var t *Ticket
	err := a.withTx(ctx, func(tx pgx.Tx) error {
		id, err := nextTicketID(ctx, tx, now)
		if err != nil {
			return err
		}
		t = &Ticket{TicketID: id, UserID: userID, OrderID: orderID, OperatorID: operatorID, Category: category, Entry: req.Entry,
			Status: StatusPending, CreatedAt: now, Facts: facts}
		if err := insertTicket(ctx, tx, t); err != nil {
			return err
		}
		if err := addMessage(ctx, tx, t, FromUser, text, now); err != nil {
			return err
		}
		return emitTicketChanged(ctx, tx, t, "新建", ActorRef{Kind: "用户", ID: userID}, now)
	})
	if err != nil {
		return nil, err
	}
	a.notifyAgent(ctx, t.TicketID)
	return loadTicket(ctx, a.DB, t.TicketID, false)
}

// snapshot 读订单事实快照。订单不是本人的按找不到处理。
func (a *App) snapshot(ctx context.Context, userID, orderID string) (*Facts, string, error) {
	o, err := a.Ride.GetOrder(ctx, orderID)
	var he *httpx.Error
	if errors.As(err, &he) && he.Code == "NOT_FOUND" {
		return nil, "", httpx.E("NOT_FOUND", "没有找到该订单")
	}
	if err != nil {
		return nil, "", err
	}
	if o.UserID != userID {
		return nil, "", httpx.E("NOT_FOUND", "没有找到该订单")
	}
	f := &Facts{OrderID: o.OrderID, StartAt: cstp(o.StartAt), EndAt: cstp(o.EndAt), DistanceKm: o.DistanceKm, InStation: o.InStation, InStationBasis: o.InStationBasis}
	b, err := a.Bill.GetFacts(ctx, orderID)
	if errors.As(err, &he) && he.Code == "NOT_FOUND" {
		// 还没有账单（开锁失败、骑行中）：金额为 null
		return f, o.OperatorID, nil
	}
	if err != nil {
		return nil, "", err
	}
	f.BilledMinutes = b.BilledMinutes
	if b.Fee != nil {
		r, p, n := b.Fee.Receivable, b.Fee.Payable, b.Fee.NetReceived
		f.Receivable, f.Payable, f.NetReceived = &r, &p, &n
	}
	return f, o.OperatorID, nil
}

// SendUserMessage 用户继续发消息；已解决的对话回到"处理中"。
func (a *App) SendUserMessage(ctx context.Context, userID, ticketID, text string) (*Ticket, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, httpx.E("INVALID", "请输入内容")
	}
	now := a.now()
	var t *Ticket
	err := a.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		t, err = loadTicket(ctx, tx, ticketID, true)
		if err != nil {
			return err
		}
		if t.UserID != userID {
			return httpx.E("NOT_FOUND", "没有找到该对话")
		}
		if err := addMessage(ctx, tx, t, FromUser, text, now); err != nil {
			return err
		}
		if t.Status == StatusResolved {
			t.Status, t.ClosedAt = StatusProcessing, nil
		}
		if err := saveTicket(ctx, tx, t); err != nil {
			return err
		}
		return emitTicketChanged(ctx, tx, t, "用户新消息", ActorRef{Kind: "用户", ID: userID}, now)
	})
	if err != nil {
		return nil, err
	}
	a.notifyAgent(ctx, ticketID)
	return loadTicket(ctx, a.DB, ticketID, false)
}

// GetUserTicket 读本人的对话。
func (a *App) GetUserTicket(ctx context.Context, userID, ticketID string) (*Ticket, error) {
	t, err := loadTicket(ctx, a.DB, ticketID, false)
	if err != nil {
		return nil, err
	}
	if t.UserID != userID {
		return nil, httpx.E("NOT_FOUND", "没有找到该对话")
	}
	return t, nil
}

// ListUserTickets 本人的全部对话，新到旧。
func (a *App) ListUserTickets(ctx context.Context, userID string) ([]*Ticket, error) {
	return queryTickets(ctx, a.DB, `WHERE user_id = $1 ORDER BY created_at DESC, ticket_id DESC`, userID)
}

// ListUserOrderTickets 本人某一单的对话。
func (a *App) ListUserOrderTickets(ctx context.Context, userID, orderID string) ([]*Ticket, error) {
	return queryTickets(ctx, a.DB, `WHERE user_id = $1 AND order_id = $2 ORDER BY created_at DESC, ticket_id DESC`, userID, orderID)
}

// GetTicket 客服读工单。
func (a *App) GetTicket(ctx context.Context, ad *auth.Admin, ticketID string) (*Ticket, error) {
	t, err := loadTicket(ctx, a.DB, ticketID, false)
	if err != nil {
		return nil, err
	}
	if err := scopeCheck(ad, t); err != nil {
		return nil, err
	}
	return t, nil
}

func takeOver(t *Ticket, ad *auth.Admin) {
	if t.Assignee == nil {
		id, name := ad.AccountID, ad.Name
		t.Assignee = &id
		if name != "" {
			t.AssigneeName = &name
		}
	}
}

// Reply 客服回复；第一次回复即接手，"待处理"转"处理中"。
func (a *App) Reply(ctx context.Context, ad *auth.Admin, ticketID, text string) (*Ticket, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, httpx.E("INVALID", "请输入回复内容")
	}
	now := a.now()
	var t *Ticket
	err := a.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		if t, err = loadTicket(ctx, tx, ticketID, true); err != nil {
			return err
		}
		if err := scopeCheck(ad, t); err != nil {
			return err
		}
		if err := addMessage(ctx, tx, t, FromStaff, text, now); err != nil {
			return err
		}
		if t.Status == StatusPending {
			t.Status = StatusProcessing
		}
		takeOver(t, ad)
		if err := saveTicket(ctx, tx, t); err != nil {
			return err
		}
		return emitTicketChanged(ctx, tx, t, "客服回复", staffActor(ad), now)
	})
	return t, err
}

// Close 结案；事由作为一条客服消息发给用户。
func (a *App) Close(ctx context.Context, ad *auth.Admin, ticketID, reason string) (*Ticket, error) {
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
		switch t.Status {
		case StatusResolved:
			return httpx.E("ALREADY_DONE", "工单已解决")
		case StatusSupervisor:
			return httpx.E("CONFLICT", "该工单的退款待客服主管确认，暂不能结案")
		}
		before := t.Status
		t.Status, t.ClosedAt = StatusResolved, &now
		takeOver(t, ad)
		if err := addMessage(ctx, tx, t, FromStaff, why, now); err != nil {
			return err
		}
		if err := saveTicket(ctx, tx, t); err != nil {
			return err
		}
		if err := emitTicketChanged(ctx, tx, t, "结案", staffActor(ad), now); err != nil {
			return err
		}
		return emitAudit(ctx, tx, staffActor(ad), now, Audit{Action: "结案工单", TargetType: "ticket", TargetID: t.TicketID,
			Summary: "工单 " + t.TicketID + " 结案", Reason: &why, Before: map[string]any{"status": before}, After: map[string]any{"status": t.Status}})
	})
	return t, err
}

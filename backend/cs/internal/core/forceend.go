package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/cs/internal/auth"
	"bike/cs/internal/clients/ride"
	"bike/cs/internal/httpx"
)

// ForceEndRequest 是强制结束的请求。stationId 区分"没传"（取提议）与"传 null"（按车辆当前位置）。
type ForceEndRequest struct {
	BilledEndAt *time.Time
	StationSet  bool
	StationID   *string
	TicketID    *string
	Reason      string
}

// UnmarshalJSON 区分 stationId 缺省与 null。
func (r *ForceEndRequest) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if v, ok := raw["billedEndAt"]; ok && string(v) != "null" {
		var t time.Time
		if err := json.Unmarshal(v, &t); err != nil {
			return err
		}
		r.BilledEndAt = &t
	}
	if v, ok := raw["stationId"]; ok {
		r.StationSet = true
		if err := json.Unmarshal(v, &r.StationID); err != nil {
			return err
		}
	}
	if v, ok := raw["ticketId"]; ok {
		if err := json.Unmarshal(v, &r.TicketID); err != nil {
			return err
		}
	}
	if v, ok := raw["reason"]; ok {
		if err := json.Unmarshal(v, &r.Reason); err != nil {
			return err
		}
	}
	return nil
}

// ForceEndResult 是 cs.yaml ForceEndResult。
type ForceEndResult struct {
	OrderID       string         `json:"orderId"`
	ForceEnd      ForceEndRecord `json:"forceEnd"`
	BilledMinutes *int64         `json:"billedMinutes"`
	Ticket        *AdminTicket   `json:"ticket"`
	ticket        *Ticket
}

// ForceEnd 客服确认并写事由后调 ride 强制结束。计费终点缺省取客服智能体的提议，没有提议取当前时刻。
func (a *App) ForceEnd(ctx context.Context, ad *auth.Admin, orderID string, req ForceEndRequest) (*ForceEndResult, error) {
	why, err := checkReason(req.Reason)
	if err != nil {
		return nil, err
	}
	o, err := a.Ride.GetOrder(ctx, orderID)
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) && he.Code == "NOT_FOUND" {
			return nil, httpx.E("NOT_FOUND", "没有找到该订单")
		}
		return nil, err
	}
	if ad.Scope != "" && o.OperatorID != ad.Scope {
		return nil, httpx.E("SCOPE_DENIED", "该订单不在你的数据范围内")
	}
	if o.Status != "骑行中" {
		return nil, httpx.E("NOT_RIDING", "订单不在骑行中，不需要强制结束")
	}
	now := a.now()
	out := &ForceEndResult{OrderID: orderID}
	err = a.withTx(ctx, func(tx pgx.Tx) error {
		var t *Ticket
		if req.TicketID != nil && *req.TicketID != "" {
			var err error
			if t, err = loadTicket(ctx, tx, *req.TicketID, true); err != nil {
				return err
			}
			if t.OrderID == nil || *t.OrderID != orderID {
				return httpx.E("INVALID", "该工单不是这个订单的")
			}
		} else {
			var id string
			err := tx.QueryRow(ctx, `SELECT ticket_id FROM tickets WHERE order_id = $1 AND status <> '已解决' ORDER BY created_at DESC, ticket_id DESC LIMIT 1`, orderID).Scan(&id)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if id != "" {
				if t, err = loadTicket(ctx, tx, id, true); err != nil {
					return err
				}
			}
		}
		end := now
		switch {
		case req.BilledEndAt != nil:
			end = req.BilledEndAt.In(CST).Truncate(time.Second)
		case t != nil && t.ForceEndProposal != nil:
			end = t.ForceEndProposal.BilledEndAt.In(CST)
		}
		var station *string
		switch {
		case req.StationSet:
			station = req.StationID
		case t != nil && t.ForceEndProposal != nil:
			station = t.ForceEndProposal.StationID
		}
		if o.StartAt == nil || end.Before(*o.StartAt) || end.After(now.Add(time.Second)) {
			return httpx.E("INVALID", "计费终点须在开锁之后、现在之前")
		}
		var tid *string
		if t != nil {
			tid = &t.TicketID
		}
		key := fmt.Sprintf("cs-fe-%s-%d", orderID, end.Unix())
		if _, err := a.Ride.ForceEnd(ctx, orderID, key, ride.ForceEndRequest{BilledEndAt: &end, StationID: station, TicketID: tid, Reason: why,
			Actor: ride.Actor{Kind: "人", ID: ad.AccountID, Name: ad.Name}}); err != nil {
			return err
		}
		rec := ForceEndRecord{At: now, By: ad.AccountID, Reason: why, BilledEndAt: end}
		out.ForceEnd = rec
		if t != nil {
			t.ForceEnd = &rec
			if err := addAction(ctx, tx, t, "强制结束", nil, ad.AccountID, now); err != nil {
				return err
			}
			if err := addMessage(ctx, tx, t, FromStaff, fmt.Sprintf("已为你结束订单并锁车，按 %s 还车计费，费用以账单为准。", end.Format("01-02 15:04")), now); err != nil {
				return err
			}
			t.Status, t.ClosedAt = StatusResolved, &now
			takeOver(t, ad)
			if err := saveTicket(ctx, tx, t); err != nil {
				return err
			}
			if err := emitTicketChanged(ctx, tx, t, "强制结束", staffActor(ad), now); err != nil {
				return err
			}
			out.ticket = t
		}
		// 强制结束的留痕由 ride 记（操作人取请求里的 actor），cs 只记工单本身
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

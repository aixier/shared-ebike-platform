package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// ActorRef 是信封里的操作人。
type ActorRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Envelope 是事件信封（contracts/events/README.md 第 1 节）。
type Envelope struct {
	EventID    string          `json:"eventId"`
	Type       string          `json:"type"`
	Version    int             `json:"version"`
	OccurredAt time.Time       `json:"occurredAt"`
	Key        string          `json:"key"`
	Actor      ActorRef        `json:"actor"`
	Payload    json.RawMessage `json:"payload"`
}

// SystemActor 是本服务自己引起的变化（如收到事件后更新工单）。
var SystemActor = ActorRef{Kind: "系统", ID: "cs"}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// emit 在业务事务里写发件表。
func emit(ctx context.Context, tx pgx.Tx, typ, key string, actor ActorRef, at time.Time, payload any) error {
	p, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := Envelope{EventID: newID("cs-"), Type: typ, Version: 1, OccurredAt: at.In(CST).Truncate(time.Second), Key: key, Actor: actor, Payload: p}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (event_id, subject, body) VALUES ($1, $2, $3)`, env.EventID, typ, body)
	return err
}

// emitTicketChanged 发 cs.ticket.changed（contracts/events/cs.md）。
func emitTicketChanged(ctx context.Context, tx pgx.Tx, t *Ticket, change string, actor ActorRef, at time.Time) error {
	return emit(ctx, tx, "cs.ticket.changed", t.TicketID, actor, at, map[string]any{
		"ticketId": t.TicketID, "userId": t.UserID, "orderId": t.OrderID, "category": t.Category,
		"status": t.Status, "change": change, "at": at.In(CST).Truncate(time.Second),
	})
}

// Audit 是留痕事件载荷（contracts/events/README.md 第 3 节）。
type Audit struct {
	Action     string  `json:"action"`
	TargetType string  `json:"targetType"`
	TargetID   string  `json:"targetId"`
	Summary    string  `json:"summary"`
	Reason     *string `json:"reason"`
	Before     any     `json:"before"`
	After      any     `json:"after"`
}

// emitAudit 发 cs.audit.recorded。
func emitAudit(ctx context.Context, tx pgx.Tx, actor ActorRef, at time.Time, a Audit) error {
	return emit(ctx, tx, "cs.audit.recorded", a.TargetType+":"+a.TargetID, actor, at, a)
}

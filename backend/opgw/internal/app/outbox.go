package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// querier 事务与连接池共用的执行接口。
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Actor 操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

var systemActor = Actor{Kind: "系统", ID: "opgw", Name: "运营商通道"}

// Envelope 事件信封（contracts/events/README.md 第 1 节）。
type Envelope struct {
	EventID    string          `json:"eventId"`
	Type       string          `json:"type"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurredAt"`
	Key        string          `json:"key"`
	Actor      Actor           `json:"actor"`
	Payload    json.RawMessage `json:"payload"`
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func rfc3339(t time.Time) string { return t.Format(time.RFC3339) }

// enqueue 在业务事务里写发件表。
func (a *App) enqueue(ctx context.Context, q querier, typ, key string, actor Actor, payload any) error {
	p, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := Envelope{EventID: newID("opgw-"), Type: typ, Version: 1, OccurredAt: rfc3339(a.Now()), Key: key, Actor: actor, Payload: p}
	b, _ := json.Marshal(env)
	_, err = q.Exec(ctx, `INSERT INTO outbox (event_id, subject, envelope, created_at) VALUES ($1,$2,$3,$4)`, env.EventID, typ, b, a.Now())
	return err
}

// auditPayload 留痕载荷。
type auditPayload struct {
	Action     string  `json:"action"`
	TargetType string  `json:"targetType"`
	TargetID   string  `json:"targetId"`
	Summary    string  `json:"summary"`
	Reason     *string `json:"reason"`
	Before     any     `json:"before"`
	After      any     `json:"after"`
}

// audit 写"留痕已记录"事件。
func (a *App) audit(ctx context.Context, q querier, actor Actor, action, targetID, summary string, reason *string, before, after any) error {
	return a.enqueue(ctx, q, "opgw.audit.recorded", "credential:"+targetID, actor, auditPayload{
		Action: action, TargetType: "credential", TargetID: targetID, Summary: summary, Reason: reason, Before: before, After: after,
	})
}

// EnsureStream 确保本模块的流存在。
// retentionDays 为事件保留天数（EVENT_RETENTION_DAYS），不大于 0 按 30 天。
func EnsureStream(ctx context.Context, js jetstream.JetStream, retentionDays int) error {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      "EVT_OPGW",
		Subjects:  []string{"opgw.>"},
		Storage:   jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy,
		MaxAge:    time.Duration(retentionDays) * 24 * time.Hour,
	})
	return err
}

// PublishOutbox 把未发的事件按顺序发到事件总线；总线不可用时留在发件表下次再发。返回发出条数。
func (a *App) PublishOutbox(ctx context.Context) (int, error) {
	if a.JS == nil {
		return 0, nil
	}
	n := 0
	for {
		tx, err := a.DB.Begin(ctx)
		if err != nil {
			return n, err
		}
		// 多实例时只让一个实例发，保持顺序
		var got bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(830301)`).Scan(&got); err != nil || !got {
			_ = tx.Rollback(ctx)
			return n, err
		}
		rows, err := tx.Query(ctx, `SELECT id, event_id, subject, envelope FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT 100`)
		if err != nil {
			_ = tx.Rollback(ctx)
			return n, err
		}
		type item struct {
			id      int64
			eid     string
			subject string
			env     []byte
		}
		var items []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.eid, &it.subject, &it.env); err != nil {
				rows.Close()
				_ = tx.Rollback(ctx)
				return n, err
			}
			items = append(items, it)
		}
		rows.Close()
		if len(items) == 0 {
			return n, tx.Commit(ctx)
		}
		for _, it := range items {
			msg := &nats.Msg{Subject: it.subject, Data: it.env, Header: nats.Header{}}
			msg.Header.Set(nats.MsgIdHdr, it.eid)
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, err := a.JS.PublishMsg(pctx, msg)
			cancel()
			if err != nil {
				_ = tx.Commit(ctx)
				return n, err
			}
			if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at=$2 WHERE id=$1`, it.id, a.Now()); err != nil {
				_ = tx.Rollback(ctx)
				return n, err
			}
			n++
		}
		if err := tx.Commit(ctx); err != nil {
			return n, err
		}
	}
}

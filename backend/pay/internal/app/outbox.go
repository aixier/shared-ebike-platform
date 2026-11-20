package app

// 发件表与事件发布（events/README.md；wire.md 第 4 节）。
// 业务事务里写发件表；发布循环把未发的事件发到 JetStream，总线不可用时留在表里，恢复后补发。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"
)

// Actor 操作人
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

var systemActor = Actor{Kind: "系统", ID: "pay", Name: "收付款"}

// Envelope 事件信封
type Envelope struct {
	EventID    string `json:"eventId"`
	Type       string `json:"type"`
	Version    int    `json:"version"`
	OccurredAt string `json:"occurredAt"`
	Key        string `json:"key"`
	Actor      Actor  `json:"actor"`
	Payload    any    `json:"payload"`
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// enqueue 在事务里写发件表
func (a *App) enqueue(ctx context.Context, tx pgx.Tx, typ, key string, actor Actor, payload any) error {
	env := Envelope{EventID: newID("EVpay-"), Type: typ, Version: 1, OccurredAt: fmtTime(a.Now()), Key: key, Actor: actor, Payload: payload}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (event_id, subject, body, created_at) VALUES ($1,$2,$3,$4)`, env.EventID, typ, body, a.Now())
	return err
}

// audit 写"留痕已记录"事件（R-47）
func (a *App) audit(ctx context.Context, tx pgx.Tx, actor Actor, action, targetType, targetID, summary string, reason *string, before, after any) error {
	return a.enqueue(ctx, tx, "pay.audit.recorded", targetType+":"+targetID, actor, map[string]any{
		"action": action, "targetType": targetType, "targetId": targetID, "summary": summary,
		"reason": reason, "before": before, "after": after,
	})
}

// EnsureStream 确保本服务的流存在
func EnsureStream(ctx context.Context, js jetstream.JetStream, maxAge time.Duration) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "EVT_PAY", Subjects: []string{"pay.>"}, Storage: jetstream.FileStorage, MaxAge: maxAge,
	})
	return err
}

// PublishPending 发一批未发的事件，返回发出的条数
func (a *App) PublishPending(ctx context.Context, js jetstream.JetStream) (int, error) {
	if js == nil {
		return 0, errors.New("事件总线未连接")
	}
	rows, err := a.Pool.Query(ctx, `SELECT id, event_id, subject, body FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id      int64
		eid     string
		subject string
		body    []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.eid, &it.subject, &it.body); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := js.Publish(pctx, it.subject, it.body, jetstream.WithMsgID(it.eid))
		cancel()
		if err != nil {
			return n, err // 保序：失败即停，下次从这条重试
		}
		if _, err := a.Pool.Exec(ctx, `UPDATE outbox SET published_at = $2 WHERE id = $1`, it.id, time.Now()); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

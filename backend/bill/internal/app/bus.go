package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Actor 操作人
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// SystemActor 本模块定时任务与事件处理
var SystemActor = Actor{Kind: "系统", ID: "SYS-bill", Name: "计费服务"}

// Envelope 事件信封（contracts/events/README.md 第 1 节）
type Envelope struct {
	EventID    string          `json:"eventId"`
	Type       string          `json:"type"`
	Version    int             `json:"version"`
	OccurredAt time.Time       `json:"occurredAt"`
	Key        string          `json:"key"`
	Actor      Actor           `json:"actor"`
	Payload    json.RawMessage `json:"payload"`
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// emit 在业务事务里写发件表
func emit(ctx context.Context, tx pgx.Tx, typ, key string, actor Actor, at time.Time, payload any) error {
	p, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := Envelope{EventID: "bill-" + newID(), Type: typ, Version: 1, OccurredAt: at, Key: key, Actor: actor, Payload: p}
	body, _ := json.Marshal(env)
	_, err = tx.Exec(ctx, `INSERT INTO outbox (event_id, subject, body) VALUES ($1,$2,$3)`, env.EventID, typ, body)
	return err
}

// Audit 留痕载荷
type Audit struct {
	Action     string         `json:"action"`
	TargetType string         `json:"targetType"`
	TargetID   string         `json:"targetId"`
	Summary    string         `json:"summary"`
	Reason     *string        `json:"reason"`
	Before     map[string]any `json:"before"`
	After      map[string]any `json:"after"`
}

func emitAudit(ctx context.Context, tx pgx.Tx, actor Actor, at time.Time, a Audit) error {
	return emit(ctx, tx, "bill.audit.recorded", a.TargetType+":"+a.TargetID, actor, at, a)
}

// Publisher 把发件表里的事件发到事件总线；总线不可用时事件留在表里，恢复后补发
type Publisher struct {
	Pool *pgxpool.Pool
	JS   jetstream.JetStream
	Log  *slog.Logger
}

// EnsureStream 确保本模块的流 EVT_BILL 存在；retention 为按时间保留的期限（EVENT_RETENTION_DAYS）
func EnsureStream(ctx context.Context, js jetstream.JetStream, retention time.Duration) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "EVT_BILL",
		Subjects: []string{"bill.>"},
		Storage:  jetstream.FileStorage,
		MaxAge:   retention,
	})
	return err
}

// PublishPending 发一批未发的事件，返回发出的条数
func (p *Publisher) PublishPending(ctx context.Context) (int, error) {
	rows, err := p.Pool.Query(ctx, `SELECT id, event_id, subject, body FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT 100`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id       int64
		eid, sub string
		body     []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.eid, &it.sub, &it.body); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		msg := &nats.Msg{Subject: it.sub, Data: it.body, Header: nats.Header{}}
		msg.Header.Set(nats.MsgIdHdr, it.eid)
		if _, err := p.JS.PublishMsg(ctx, msg); err != nil {
			return n, err
		}
		if _, err := p.Pool.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE id = $1`, it.id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Run 循环补发
func (p *Publisher) Run(ctx context.Context) {
	for {
		n, err := p.PublishPending(ctx)
		if err != nil && ctx.Err() == nil {
			p.Log.Warn("发件表补发失败", "err", err)
		}
		wait := 300 * time.Millisecond
		if n == 100 {
			wait = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Handler 处理一条事件：在 tx 里改库；返回的 after 在提交后执行（调别的服务）
type Handler func(ctx context.Context, tx pgx.Tx, env Envelope) (after func(context.Context), err error)

// Subscription 订阅一个流上的若干主题
type Subscription struct {
	Stream   string
	Subjects []string
}

// Subscriptions bill 订阅的事件（contracts/events 各发布方文件里登记的 bill 订阅）
var Subscriptions = []Subscription{
	{Stream: "EVT_RIDE", Subjects: []string{"ride.order.ended", "ride.order.unlock_failed", "ride.order.unlocked", "ride.order.pending_manual"}},
	{Stream: "EVT_PAY", Subjects: []string{"pay.payment.succeeded", "pay.payment.failed", "pay.payment.unknown", "pay.refund.succeeded", "pay.refund.failed"}},
	{Stream: "EVT_DEPOSIT", Subjects: []string{"deposit.status.changed", "deposit.deducted"}},
	{Stream: "EVT_RULE", Subjects: []string{"rule.version.published", "rule.version.revoked", "rule.params.changed"}},
	{Stream: "EVT_ACCT", Subjects: []string{"acct.user.registered", "acct.realname.changed", "acct.account.frozen", "acct.account.unfrozen", "acct.account.cancelled"}},
	{Stream: "EVT_FLEET", Subjects: []string{"fleet.operator.exited"}},
}

// Process 按 eventId 去重后在一个事务里处理
func Process(ctx context.Context, pool *pgxpool.Pool, h Handler, data []byte) error {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil // 坏消息无法处理，确认丢弃
	}
	var after func(context.Context)
	err := inTx(ctx, pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO inbox (event_id, type) VALUES ($1,$2) ON CONFLICT DO NOTHING`, env.EventID, env.Type)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // 重复到达
		}
		after, err = h(ctx, tx, env)
		return err
	})
	if err != nil {
		return err
	}
	if after != nil {
		after(ctx)
	}
	return nil
}

// Consume 在一个流上建持久消费者 bill 并顺序处理；流尚不存在时等待重试
func Consume(ctx context.Context, js jetstream.JetStream, pool *pgxpool.Pool, sub Subscription, h Handler, log *slog.Logger) {
	for ctx.Err() == nil {
		cons, err := js.CreateOrUpdateConsumer(ctx, sub.Stream, jetstream.ConsumerConfig{
			Durable:        "bill",
			FilterSubjects: sub.Subjects,
			AckPolicy:      jetstream.AckExplicitPolicy,
			MaxAckPending:  1,
			AckWait:        30 * time.Second,
		})
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Debug("等待事件流", "stream", sub.Stream, "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		cc, err := cons.Consume(func(m jetstream.Msg) {
			if err := Process(ctx, pool, h, m.Data()); err != nil {
				log.Warn("事件处理失败，等待重投", "subject", m.Subject(), "err", err)
				_ = m.NakWithDelay(time.Second)
				return
			}
			_ = m.Ack()
		})
		if err != nil {
			log.Warn("订阅失败", "stream", sub.Stream, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		<-ctx.Done()
		cc.Stop()
		return
	}
}

// errUnknownVersion 规则版本尚未到达，稍后重投
var errUnknownVersion = fmt.Errorf("规则版本尚未到达")

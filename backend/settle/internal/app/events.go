package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Envelope 是事件信封（contracts/events/README.md 第 1 节）。
type Envelope struct {
	EventID    string          `json:"eventId"`
	Type       string          `json:"type"`
	Version    int             `json:"version"`
	OccurredAt time.Time       `json:"occurredAt"`
	Key        string          `json:"key"`
	Actor      Actor           `json:"actor"`
	Payload    json.RawMessage `json:"payload"`
}

func newEventID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "settle-" + hex.EncodeToString(b)
}

// emit 在业务事务里把事件写进发件表。
func emit(ctx context.Context, tx pgx.Tx, typ, key string, actor Actor, at time.Time, payload any) error {
	p, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := Envelope{EventID: newEventID(), Type: typ, Version: 1, OccurredAt: at, Key: key, Actor: actor, Payload: p}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox(event_id, subject, body) VALUES ($1, $2, $3)`, env.EventID, typ, body)
	return err
}

// audit 写"留痕已记录"事件（settle.audit.recorded），由 iam 保存。
func audit(ctx context.Context, tx pgx.Tx, actor Actor, at time.Time, action, targetType, targetID, summary string, reason *string, before, after any) error {
	return emit(ctx, tx, "settle.audit.recorded", targetType+":"+targetID, actor, at, map[string]any{
		"action": action, "targetType": targetType, "targetId": targetID, "summary": summary,
		"reason": reason, "before": before, "after": after,
	})
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---------------- 发布 ----------------

// Bus 是事件总线连接。
type Bus struct {
	NC *nats.Conn
	JS jetstream.JetStream
}

// ConnectBus 连接事件总线。
func ConnectBus(url string) (*Bus, error) {
	nc, err := nats.Connect(url, nats.Name("settle"), nats.MaxReconnects(-1), nats.ReconnectWait(2*time.Second))
	if err != nil {
		return nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return &Bus{NC: nc, JS: js}, nil
}

// EnsureStream 确保本服务的流 EVT_SETTLE 存在。
func (b *Bus) EnsureStream(ctx context.Context, retention time.Duration) error {
	_, err := b.JS.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "EVT_SETTLE", Subjects: []string{"settle.>"}, Storage: jetstream.FileStorage, MaxAge: retention,
		Duplicates: 2 * time.Minute,
	})
	return err
}

// PublishPending 把发件表里未发的事件发到总线，返回本次发出的条数。总线不可用时返回错误，事件留在发件表。
func (a *App) PublishPending(ctx context.Context, b *Bus) (int, error) {
	rows, err := a.DB.Query(ctx, `SELECT id, event_id, subject, body FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id      int64
		eventID string
		subject string
		body    []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.eventID, &it.subject, &it.body); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := b.JS.Publish(pctx, it.subject, it.body, jetstream.WithMsgID(it.eventID))
		cancel()
		if err != nil {
			return n, err
		}
		if _, err := a.DB.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE id = $1`, it.id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RunPublisher 循环补发发件表，直到 ctx 结束。
func (a *App) RunPublisher(ctx context.Context, b *Bus, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := a.PublishPending(ctx, b); err != nil && ctx.Err() == nil {
			a.Log.Warn("发件表补发失败，稍后重试", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---------------- 订阅 ----------------

// Subscriptions 本服务订阅的流与主题（已在各发布方事件文件登记）。
var Subscriptions = map[string][]string{
	"EVT_RIDE":  {"ride.order.ended", "ride.order.unlocked", "ride.order.unlock_failed", "ride.order.pending_manual"},
	"EVT_BILL":  {"bill.bill.issued", "bill.bill.paid", "bill.bill.refunded", "bill.card.purchased"},
	"EVT_RULE":  {"rule.version.published", "rule.version.revoked"},
	"EVT_FLEET": {"fleet.brand.registered", "fleet.operator.exited"},
}

// errPoison 表示消息无法解析，重投也没用。
var errPoison = errors.New("事件无法解析")

// HandleMessage 处理一条总线消息：按 eventId 去重后更新投影。
func (a *App) HandleMessage(ctx context.Context, body []byte) error {
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil || env.EventID == "" || env.Type == "" {
		return fmt.Errorf("%w: %v", errPoison, err)
	}
	return a.HandleEvent(ctx, env)
}

// HandleEvent 处理信封（同一事务里记去重与更新投影）。
func (a *App) HandleEvent(ctx context.Context, env Envelope) error {
	return a.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO processed_events(event_id, type) VALUES ($1, $2) ON CONFLICT DO NOTHING`, env.EventID, env.Type)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // 重复送达
		}
		return applyProjection(ctx, tx, env)
	})
}

// RunConsumers 为每个订阅的流建持久消费者并顺序处理；流尚不存在时定期重试。
func (a *App) RunConsumers(ctx context.Context, b *Bus) {
	for stream, subjects := range Subscriptions {
		go a.runConsumer(ctx, b, stream, subjects)
	}
}

func (a *App) runConsumer(ctx context.Context, b *Bus, stream string, subjects []string) {
	for ctx.Err() == nil {
		cons, err := b.JS.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
			Durable: "settle", AckPolicy: jetstream.AckExplicitPolicy, FilterSubjects: subjects,
			MaxAckPending: 1, DeliverPolicy: jetstream.DeliverAllPolicy, AckWait: 30 * time.Second,
		})
		if err != nil {
			a.Log.Info("等待事件流就绪", "stream", stream, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		cc, err := cons.Consume(func(m jetstream.Msg) {
			err := a.HandleMessage(ctx, m.Data())
			switch {
			case err == nil:
				_ = m.Ack()
			case errors.Is(err, errPoison):
				a.Log.Error("丢弃无法解析的事件", "stream", stream, "err", err)
				_ = m.Term()
			default:
				a.Log.Warn("事件处理失败，等待重投", "stream", stream, "err", err)
				_ = m.NakWithDelay(2 * time.Second)
			}
		})
		if err != nil {
			a.Log.Warn("订阅失败，稍后重试", "stream", stream, "err", err)
			time.Sleep(3 * time.Second)
			continue
		}
		<-ctx.Done()
		cc.Stop()
		return
	}
}

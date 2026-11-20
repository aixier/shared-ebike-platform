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
	"github.com/nats-io/nats.go/jetstream"
)

// Actor 操作人（kernel.md）。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// svcActor 同步调 fleet、ride、pay 时的操作人：只填 人 或 系统（智能体与定时任务触发的一律填系统）。
// 事件信封与留痕里仍如实记录智能体。
func svcActor(a Actor) Actor {
	if a.Kind == "人" {
		return a
	}
	a.Kind = "系统"
	return a
}

// SystemActor 本模块定时任务。
var SystemActor = Actor{Kind: "系统", ID: "ops-system", Name: "运行值守定时任务"}

// Envelope 事件信封（events/README.md 第 1 节）。
type Envelope struct {
	EventID    string          `json:"eventId"`
	Type       string          `json:"type"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurredAt"`
	Key        string          `json:"key"`
	Actor      Actor           `json:"actor"`
	Payload    json.RawMessage `json:"payload"`
}

func newEventID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "ops-" + hex.EncodeToString(b)
}

// emit 在业务事务里写发件表。
func (a *App) emit(ctx context.Context, tx pgx.Tx, typ, key string, actor Actor, payload any) error {
	p, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := Envelope{EventID: newEventID(), Type: typ, Version: 1, OccurredAt: a.fmtTime(a.now()), Key: key, Actor: actor, Payload: p}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (event_id, subject, body) VALUES ($1,$2,$3)`, env.EventID, typ, body)
	return err
}

// audit 写"留痕已记录"事件（events/README.md 第 3 节）。
func (a *App) audit(ctx context.Context, tx pgx.Tx, actor Actor, action, targetType, targetID, summary string, reason *string, before, after any) error {
	return a.emit(ctx, tx, "ops.audit.recorded", targetType+":"+targetID, actor, map[string]any{
		"action": action, "targetType": targetType, "targetId": targetID, "summary": summary,
		"reason": reason, "before": before, "after": after,
	})
}

// ===== 发布 =====

// EnsureStream 确保本模块的流 EVT_OPS 存在。
func EnsureStream(ctx context.Context, js jetstream.JetStream, maxAge time.Duration) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "EVT_OPS", Subjects: []string{"ops.>"}, Storage: jetstream.FileStorage, MaxAge: maxAge,
	})
	return err
}

// PublishPending 把发件表里未发的事件按顺序发出；返回发出条数。多实例时用 SKIP LOCKED 只由一个实例发同一批。
func (a *App) PublishPending(ctx context.Context, js jetstream.JetStream) (int, error) {
	n := 0
	err := a.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, event_id, subject, body FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT 200 FOR UPDATE SKIP LOCKED`)
		if err != nil {
			return err
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
				return err
			}
			items = append(items, it)
		}
		rows.Close()
		for _, it := range items {
			if _, err := js.Publish(ctx, it.subject, it.body, jetstream.WithMsgID(it.eventID)); err != nil {
				// 总线不可用：已发的记下，余下留在发件表等下一轮
				return nil
			}
			if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE id = $1`, it.id); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// RunPublisher 每秒补发一次，直到 ctx 结束。
func (a *App) RunPublisher(ctx context.Context, js jetstream.JetStream) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if _, err := a.PublishPending(ctx, js); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("发件表补发失败", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ===== 消费 =====

// Subscriptions 本模块订阅的流与主题（README 订阅事件；各发布方文件已登记 ops 为订阅方）。
var Subscriptions = map[string][]string{
	"EVT_RIDE":  {"ride.order.created", "ride.order.unlocked", "ride.order.unlock_failed", "ride.order.ended", "ride.order.pending_manual", "ride.order.over_limit_reminded", "ride.dependency.state_changed"},
	"EVT_OPGW":  {"opgw.operator.unreachable", "opgw.operator.recovered"},
	"EVT_BILL":  {"bill.bill.issued", "bill.bill.paid", "bill.arrears.created"},
	"EVT_PAY":   {"pay.payment.succeeded", "pay.payment.failed", "pay.payment.unknown"},
	"EVT_ACCT":  {"acct.realname.changed"},
	"EVT_RULE":  {"rule.version.published", "rule.version.revoked", "rule.params.changed"},
	"EVT_FLEET": {"fleet.brand.registered", "fleet.brand.access_changed", "fleet.operator.exited"},
}

// HandleEnvelope 按事件编号去重后在一个事务里处理。重复到达直接返回 nil。
func (a *App) HandleEnvelope(ctx context.Context, env Envelope) error {
	if env.EventID == "" || env.Type == "" {
		return nil
	}
	return a.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO inbox (event_id, type) VALUES ($1,$2) ON CONFLICT DO NOTHING`, env.EventID, env.Type)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return a.dispatch(ctx, tx, env)
	})
}

// RunConsumers 为每个订阅的流建持久消费者 ops，一次只处理一条以保序；流还不存在时稍后重试。
func (a *App) RunConsumers(ctx context.Context, js jetstream.JetStream) {
	for stream, subjects := range Subscriptions {
		go a.consumeStream(ctx, js, stream, subjects)
	}
}

func (a *App) consumeStream(ctx context.Context, js jetstream.JetStream, stream string, subjects []string) {
	for ctx.Err() == nil {
		s, err := js.Stream(ctx, stream)
		if err != nil {
			sleep(ctx, 3*time.Second)
			continue
		}
		cons, err := s.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
			Durable: "ops", AckPolicy: jetstream.AckExplicitPolicy, FilterSubjects: subjects,
			MaxAckPending: 1, DeliverPolicy: jetstream.DeliverAllPolicy, AckWait: 30 * time.Second,
		})
		if err != nil {
			slog.Warn("建消费者失败", "stream", stream, "err", err)
			sleep(ctx, 3*time.Second)
			continue
		}
		cc, err := cons.Consume(func(msg jetstream.Msg) {
			var env Envelope
			if err := json.Unmarshal(msg.Data(), &env); err != nil {
				slog.Error("事件不是合法信封，丢弃", "subject", msg.Subject(), "err", err)
				_ = msg.Term()
				return
			}
			if err := a.HandleEnvelope(ctx, env); err != nil {
				slog.Warn("事件处理失败，等待重投", "type", env.Type, "eventId", env.EventID, "err", err)
				_ = msg.NakWithDelay(2 * time.Second)
				return
			}
			_ = msg.Ack()
		})
		if err != nil {
			sleep(ctx, 3*time.Second)
			continue
		}
		<-ctx.Done()
		cc.Stop()
		return
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// ===== 编号 =====

// nextID 生成 前缀-YYYYMMDD-NN（日序号两位起，超过 99 自然变长）。
func (a *App) nextID(ctx context.Context, tx pgx.Tx, prefix string, at time.Time) (string, error) {
	day := at.In(a.Loc).Format("20060102")
	var n int
	err := tx.QueryRow(ctx, `INSERT INTO id_seq (prefix, day, n) VALUES ($1,$2,1)
		ON CONFLICT (prefix, day) DO UPDATE SET n = id_seq.n + 1 RETURNING n`, prefix, day).Scan(&n)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%02d", prefix, day, n), nil
}

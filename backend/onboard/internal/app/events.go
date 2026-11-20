package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"
)

// Actor 操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Envelope 事件信封（contracts/events/README.md 第 1 节）。
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
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "EVT-onboard-" + hex.EncodeToString(b)
}

// emit 在业务事务里写发件表；事务成功事件才存在。
func (a *App) emit(ctx context.Context, tx pgx.Tx, typ, key string, actor Actor, payload any) error {
	p, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := Envelope{EventID: newEventID(), Type: typ, Version: 1, OccurredAt: a.now(), Key: key, Actor: actor, Payload: p}
	body, _ := json.Marshal(env)
	_, err = tx.Exec(ctx, `INSERT INTO outbox (event_id, subject, body) VALUES ($1,$2,$3)`, env.EventID, typ, body)
	return err
}

// audit 写"留痕已记录"事件（contracts/events/README.md 第 3 节）。
func (a *App) audit(ctx context.Context, tx pgx.Tx, actor Actor, action, targetType, targetID, summary string, reason *string, before, after any) error {
	return a.emit(ctx, tx, "onboard.audit.recorded", targetType+":"+targetID, actor, map[string]any{
		"action": action, "targetType": targetType, "targetId": targetID, "summary": summary,
		"reason": reason, "before": before, "after": after,
	})
}

// EnsureStream 启动时确保本模块的流存在。
func (a *App) EnsureStream(ctx context.Context) error {
	if a.JS == nil {
		return nil
	}
	_, err := a.JS.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "EVT_ONBOARD", Subjects: []string{"onboard.>"}, Storage: jetstream.FileStorage,
		MaxAge: a.Cfg.EventMaxAge, Duplicates: 2 * time.Minute,
	})
	return err
}

// PublishPending 把发件表里未发的事件发到总线；总线不可用时留在表里下次再发。
func (a *App) PublishPending(ctx context.Context) (int, error) {
	if a.JS == nil {
		return 0, nil
	}
	rows, err := a.DB.Query(ctx, `SELECT id, event_id, subject, body FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id        int64
		eid, subj string
		body      []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.eid, &it.subj, &it.body); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		if _, err := a.JS.Publish(ctx, it.subj, it.body, jetstream.WithMsgID(it.eid)); err != nil {
			return n, err
		}
		if _, err := a.DB.Exec(ctx, `UPDATE outbox SET published_at=now() WHERE id=$1`, it.id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// 订阅清单：流 → 主题（contracts/events/fleet.md、rule.md 里 onboard 登记的订阅）。
var subscriptions = map[string][]string{
	"EVT_FLEET": {"fleet.brand.registered", "fleet.brand.access_changed", "fleet.vehicle.listed", "fleet.vehicle.removed", "fleet.code.status_changed", "fleet.operator.exited"},
	"EVT_RULE":  {"rule.params.changed"},
}

// StartConsumers 为每个流建持久消费者（名字 onboard，显式确认，一次一条），流还不存在时稍后重试。
func (a *App) StartConsumers(ctx context.Context) {
	if a.JS == nil {
		return
	}
	for stream, subjects := range subscriptions {
		go func(stream string, subjects []string) {
			for {
				cons, err := a.JS.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
					Durable: "onboard", AckPolicy: jetstream.AckExplicitPolicy, FilterSubjects: subjects,
					MaxAckPending: 1, DeliverPolicy: jetstream.DeliverAllPolicy, AckWait: 30 * time.Second,
				})
				if err == nil {
					cc, err := cons.Consume(func(m jetstream.Msg) {
						if err := a.HandleMessage(ctx, m.Data()); err != nil {
							a.logger().Warn("事件处理失败，等待重投", "stream", stream, "err", err)
							_ = m.NakWithDelay(time.Second)
							return
						}
						_ = m.Ack()
					})
					if err == nil {
						<-ctx.Done()
						cc.Stop()
						return
					}
				}
				a.logger().Info("订阅暂不可用，稍后重试", "stream", stream, "err", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}(stream, subjects)
	}
}

// HandleMessage 按 eventId 去重后在一个事务里处理一条事件。
func (a *App) HandleMessage(ctx context.Context, data []byte) error {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		a.logger().Warn("丢弃无法解析的事件", "err", err)
		return nil
	}
	if env.EventID == "" {
		return nil
	}
	var after []func()
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO processed_events (event_id) VALUES ($1) ON CONFLICT DO NOTHING`, env.EventID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		f, err := a.applyEvent(ctx, tx, env)
		if f != nil {
			after = append(after, f)
		}
		return err
	})
	if err != nil {
		return err
	}
	for _, f := range after {
		f()
	}
	return nil
}

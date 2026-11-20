package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Envelope 事件信封（contracts/events/README.md 第 1 节）
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
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "EVT-site-" + hex.EncodeToString(b)
}

// emit 在业务事务里把事件写进发件表
func (a *App) emit(ctx context.Context, tx pgx.Tx, typ, key string, actor Actor, at time.Time, payload any) error {
	p, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := Envelope{EventID: newEventID(), Type: typ, Version: 1, OccurredAt: a.fmtTime(at), Key: key, Actor: actor, Payload: p}
	body, _ := json.Marshal(env)
	if _, err := tx.Exec(ctx, `INSERT INTO outbox(event_id, subject, body) VALUES ($1,$2,$3)`, env.EventID, typ, string(body)); err != nil {
		return err
	}
	select {
	case a.kick <- struct{}{}:
	default:
	}
	return nil
}

// audit 发"留痕已记录"事件（R-47）
func (a *App) audit(ctx context.Context, tx pgx.Tx, actor Actor, action, targetType, targetID, summary, reason string, before, after any) error {
	return a.emit(ctx, tx, "site.audit.recorded", targetType+":"+targetID, actor, a.now(), map[string]any{
		"action": action, "targetType": targetType, "targetId": targetID, "summary": summary,
		"reason": nullStr(reason), "before": before, "after": after,
	})
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// EnsureStream 确保本服务的流 EVT_SITE 存在；retentionDays 为保留天数（0 或负数按默认 30）
func EnsureStream(ctx context.Context, js jetstream.JetStream, retentionDays int) error {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "EVT_SITE", Subjects: []string{"site.>"}, Storage: jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy, MaxAge: time.Duration(retentionDays) * 24 * time.Hour, Duplicates: 2 * time.Minute,
	})
	return err
}

// PublishPending 把发件表里未发出的事件按顺序发到总线；遇错停止，下次再补
func (a *App) PublishPending(ctx context.Context, js jetstream.JetStream) (int, error) {
	rows, err := a.db.Query(ctx, `SELECT seq, event_id, subject, body FROM outbox WHERE published_at IS NULL ORDER BY seq LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type item struct {
		seq     int64
		id, sub string
		body    []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.seq, &it.id, &it.sub, &it.body); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		if _, err := js.Publish(ctx, it.sub, it.body, jetstream.WithMsgID(it.id)); err != nil {
			return n, err
		}
		if _, err := a.db.Exec(ctx, `UPDATE outbox SET published_at=now() WHERE seq=$1`, it.seq); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RunPublisher 发布循环：有新事件立即发，否则每秒检查一次；总线不可用时事件留在发件表
func (a *App) RunPublisher(ctx context.Context, nc *nats.Conn) {
	js, err := jetstream.New(nc)
	if err != nil {
		a.log.Error("jetstream 初始化失败", "err", err)
		return
	}
	streamOK := false
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if !streamOK {
			if err := EnsureStream(ctx, js, a.cfg.RetentionDays); err == nil {
				streamOK = true
			} else {
				a.log.Warn("事件流暂不可用", "err", err)
			}
		}
		if streamOK {
			if _, err := a.PublishPending(ctx, js); err != nil && ctx.Err() == nil {
				a.log.Warn("事件发布失败，稍后补发", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-a.kick:
		case <-t.C:
		}
	}
}

// 本服务订阅的事件（已在发布方 fleet.md、ride.md 登记）
var subscriptions = []struct {
	stream  string
	filters []string
}{
	{"EVT_FLEET", []string{"fleet.vehicle.listed", "fleet.vehicle.removed"}},
	{"EVT_RIDE", []string{"ride.order.unlocked", "ride.order.ended"}},
}

// RunConsumers 为每个订阅的流建持久消费者 site；流还不存在时等发布方建好再连
func (a *App) RunConsumers(ctx context.Context, nc *nats.Conn) {
	js, err := jetstream.New(nc)
	if err != nil {
		a.log.Error("jetstream 初始化失败", "err", err)
		return
	}
	for _, s := range subscriptions {
		go func(stream string, filters []string) {
			for ctx.Err() == nil {
				cc, err := a.Consume(ctx, js, stream, filters)
				if err == nil {
					<-ctx.Done()
					cc.Stop()
					return
				}
				a.log.Warn("订阅暂不可用，稍后重试", "stream", stream, "err", err)
				select {
				case <-ctx.Done():
				case <-time.After(3 * time.Second):
				}
			}
		}(s.stream, s.filters)
	}
}

// Consume 建（或复用）持久消费者并开始消费：一次一条、处理成功才确认
func (a *App) Consume(ctx context.Context, js jetstream.JetStream, stream string, filters []string) (jetstream.ConsumeContext, error) {
	cons, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable: "site", AckPolicy: jetstream.AckExplicitPolicy, FilterSubjects: filters,
		MaxAckPending: 1, DeliverPolicy: jetstream.DeliverAllPolicy, AckWait: 30 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return cons.Consume(func(m jetstream.Msg) {
		if err := a.HandleEvent(ctx, m.Data()); err != nil {
			a.log.Error("事件处理失败，等待重投", "subject", m.Subject(), "err", err)
			_ = m.NakWithDelay(time.Second)
			return
		}
		_ = m.Ack()
	})
}

// errBadEnvelope 信封无法解析：记录后确认，避免毒消息卡住队列
var errBadEnvelope = errors.New("信封无法解析")

// HandleEvent 按 eventId 去重后更新投影（同一事务）
func (a *App) HandleEvent(ctx context.Context, body []byte) error {
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil || env.EventID == "" || env.Type == "" {
		a.log.Error("收到无法解析的事件，已丢弃", "err", errBadEnvelope)
		return nil
	}
	return a.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO inbox(event_id, type) VALUES ($1,$2) ON CONFLICT DO NOTHING`, env.EventID, env.Type)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // 重复到达
		}
		return a.applyProjection(ctx, tx, env)
	})
}

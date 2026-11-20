package core

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/robfig/cron/v3"
)

// Bus 把发件表里的事件发到事件总线，并消费订阅的事件。
// trace: contracts/wire.md 第 4 节；contracts/events/README.md 第 2、4 节
type Bus struct {
	App *App
	JS  jetstream.JetStream

	mu        sync.Mutex
	streamOK  bool
	consumers map[string]jetstream.ConsumeContext
}

// NewBus 建总线收发器。
func NewBus(app *App, nc *nats.Conn) (*Bus, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	return &Bus{App: app, JS: js, consumers: map[string]jetstream.ConsumeContext{}}, nil
}

// EnsureStream 确保本服务的流 EVT_CS 存在。
func (b *Bus) EnsureStream(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.streamOK {
		return nil
	}
	days := b.App.EventRetentionDays
	if days < 1 {
		days = 30
	}
	_, err := b.JS.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "EVT_CS", Subjects: []string{"cs.>"}, Storage: jetstream.FileStorage, Retention: jetstream.LimitsPolicy,
		MaxAge: time.Duration(days) * 24 * time.Hour, Duplicates: 10 * time.Minute,
	})
	if err == nil {
		b.streamOK = true
	}
	return err
}

// outboxLockKey 是发件表补发的数据库锁，多实例时只有一个在发。
const outboxLockKey = 0x63730001

// PublishPending 把未发出的事件按顺序发到总线；返回发出条数。总线不可用时事件留在发件表，下次再发。
func (b *Bus) PublishPending(ctx context.Context) (int, error) {
	if err := b.EnsureStream(ctx); err != nil {
		return 0, err
	}
	n := 0
	err := b.App.withTx(ctx, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, outboxLockKey).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT id, event_id, subject, body FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT 200`)
		if err != nil {
			return err
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
				return err
			}
			items = append(items, it)
		}
		rows.Close()
		for _, it := range items {
			msg := &nats.Msg{Subject: it.subj, Data: it.body, Header: nats.Header{}}
			msg.Header.Set(nats.MsgIdHdr, it.eid)
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, err := b.JS.PublishMsg(pctx, msg)
			cancel()
			if err != nil {
				break // 保序：后面的不跳过发，下次从这条接着发
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

// StartConsumers 为每个订阅的流建持久消费者 "cs"（按主题过滤、显式确认、一次一条以保序）并开始消费。
// 发布方的流还不存在时返回错误，调用方稍后重试；已建好的不重复建。
func (b *Bus) StartConsumers(ctx context.Context) error {
	var errs []error
	for stream, subjects := range Subscriptions {
		b.mu.Lock()
		_, started := b.consumers[stream]
		b.mu.Unlock()
		if started {
			continue
		}
		c, err := b.JS.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
			Durable: "cs", FilterSubjects: subjects, AckPolicy: jetstream.AckExplicitPolicy, MaxAckPending: 1,
			DeliverPolicy: jetstream.DeliverAllPolicy, AckWait: 30 * time.Second,
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		cc, err := c.Consume(func(m jetstream.Msg) {
			hctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := b.App.HandleEvent(hctx, m.Data()); err != nil {
				slog.Warn("处理事件失败，等待重投", "subject", m.Subject(), "err", err)
				_ = m.NakWithDelay(2 * time.Second)
				return
			}
			_ = m.Ack()
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		b.mu.Lock()
		b.consumers[stream] = cc
		b.mu.Unlock()
	}
	return errors.Join(errs...)
}

// Stop 停止消费。
func (b *Bus) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, cc := range b.consumers {
		cc.Stop()
	}
}

// Schedule 注册定时任务：每秒补发发件表，每 5 秒检查订阅是否建好。
func (b *Bus) Schedule(c *cron.Cron) error {
	if _, err := c.AddFunc("@every 1s", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := b.PublishPending(ctx); err != nil {
			slog.Debug("补发事件未完成", "err", err)
		}
	}); err != nil {
		return err
	}
	_, err := c.AddFunc("@every 5s", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.StartConsumers(ctx); err != nil {
			slog.Debug("订阅尚未建好", "err", err)
		}
	})
	return err
}

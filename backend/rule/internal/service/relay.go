package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// 流名与主题（contracts/events/README.md 第 4 节）。
const (
	streamName    = "EVT_RULE"
	streamSubject = "rule.>"
	relayBatch    = 100
	relayInterval = 2 * time.Second
)

// Relay 把发件表里的事件按写入顺序发到事件总线。多实例时用会话级咨询锁保证只有一个实例在发，从而保序。
// 总线不可用时事件留在发件表，恢复后补发；对外接口不受影响。
type Relay struct {
	Pool      *pgxpool.Pool
	JS        jetstream.JetStream
	Retention time.Duration
	Log       *slog.Logger

	wake          chan struct{}
	streamEnsured bool
}

// NewRelay 建发布循环。
func NewRelay(pool *pgxpool.Pool, nc *nats.Conn, retention time.Duration, log *slog.Logger) (*Relay, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	return &Relay{Pool: pool, JS: js, Retention: retention, Log: log, wake: make(chan struct{}, 1)}, nil
}

// Wake 提醒发布循环立即发一轮（不阻塞）。
func (r *Relay) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run 循环发布直到 ctx 结束。
func (r *Relay) Run(ctx context.Context) {
	t := time.NewTicker(relayInterval)
	defer t.Stop()
	for {
		if _, err := r.Flush(ctx); err != nil && ctx.Err() == nil && r.Log != nil {
			r.Log.Warn("事件发布暂未成功，留在发件表稍后重试", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.wake:
		}
	}
}

func (r *Relay) ensureStream(ctx context.Context) error {
	if r.streamEnsured {
		return nil
	}
	_, err := r.JS.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: streamName, Subjects: []string{streamSubject}, Storage: jetstream.FileStorage, MaxAge: r.Retention,
	})
	if err == nil {
		r.streamEnsured = true
	}
	return err
}

// ErrRelayBusy 另一个实例正在发布。
var ErrRelayBusy = errors.New("另一个实例正在发布")

// Flush 发一轮：取未发布的事件按顺序发出，成功一条标记一条；遇到失败即停，下次从该条继续。返回本轮发出的条数。
func (r *Relay) Flush(ctx context.Context) (int, error) {
	conn, err := r.Pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockRelay).Scan(&got); err != nil {
		return 0, err
	}
	if !got {
		return 0, ErrRelayBusy
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lockRelay)

	if err := r.ensureStream(ctx); err != nil {
		return 0, err
	}
	sent := 0
	for {
		rows, err := conn.Query(ctx, `SELECT id, event_id, subject, body::text FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT $1`, relayBatch)
		if err != nil {
			return sent, err
		}
		type item struct {
			id               int64
			eventID, subject string
			body             string
		}
		var items []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.eventID, &it.subject, &it.body); err != nil {
				rows.Close()
				return sent, err
			}
			items = append(items, it)
		}
		rows.Close()
		if len(items) == 0 {
			return sent, nil
		}
		for _, it := range items {
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, err := r.JS.Publish(pctx, it.subject, []byte(it.body), jetstream.WithMsgID(it.eventID))
			cancel()
			if err != nil {
				return sent, err
			}
			if _, err := conn.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE id = $1`, it.id); err != nil {
				return sent, err
			}
			sent++
		}
	}
}

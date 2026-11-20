package fleet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/robfig/cron/v3"
)

// 发布任务的数据库锁编号：多实例时只有一个在发，保证同一 key 按发件顺序送达。
const relayLockID = 1830501

// Relay 把发件表里的事件发到事件总线（流 EVT_FLEET，主题 = 事件类型）。
type Relay struct {
	db        *pgxpool.Pool
	js        jetstream.JetStream
	retention time.Duration
	ensured   bool
}

func NewRelay(db *pgxpool.Pool, js jetstream.JetStream, retention time.Duration) *Relay {
	return &Relay{db: db, js: js, retention: retention}
}

// ensureStream 确保本服务的流存在。
func (r *Relay) ensureStream(ctx context.Context) error {
	if r.ensured {
		return nil
	}
	_, err := r.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       "EVT_FLEET",
		Subjects:   []string{"fleet.>"},
		Storage:    jetstream.FileStorage,
		Retention:  jetstream.LimitsPolicy,
		MaxAge:     r.retention,
		Duplicates: 10 * time.Minute,
	})
	if err == nil {
		r.ensured = true
	}
	return err
}

// PublishPending 按发件顺序发出未发事件，返回发出的条数。总线不可用时返回错误，事件留在发件表里待下次补发。
func (r *Relay) PublishPending(ctx context.Context) (int, error) {
	if r.js == nil {
		return 0, errors.New("事件总线未连接")
	}
	if err := r.ensureStream(ctx); err != nil {
		return 0, fmt.Errorf("确保事件流: %w", err)
	}
	total := 0
	for {
		n, err := r.publishBatch(ctx, 100)
		total += n
		if err != nil || n < 100 {
			return total, err
		}
	}
}

func (r *Relay) publishBatch(ctx context.Context, limit int) (int, error) {
	sent := 0
	var batchErr error
	err := pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT seq, event_id, type, body FROM outbox WHERE published_at IS NULL ORDER BY seq LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		type ev struct {
			seq     int64
			id, typ string
			body    []byte
		}
		var evs []ev
		for rows.Next() {
			var e ev
			if err := rows.Scan(&e.seq, &e.id, &e.typ, &e.body); err != nil {
				rows.Close()
				return err
			}
			evs = append(evs, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var pubErr error
		var done []int64
		for _, e := range evs {
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, err := r.js.Publish(pctx, e.typ, e.body, jetstream.WithMsgID(e.id))
			cancel()
			if err != nil {
				pubErr = err
				break
			}
			done = append(done, e.seq)
		}
		if len(done) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE seq = ANY($1)`, done); err != nil {
				return err
			}
		}
		sent = len(done)
		batchErr = pubErr // 已发出的照常标记；没发出的留待下次
		return nil
	})
	if err != nil {
		return sent, err
	}
	return sent, batchErr
}

// RunOnce 在数据库锁下跑一次发布；拿不到锁说明别的实例在发。
func (r *Relay) RunOnce(ctx context.Context) {
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		slog.Warn("发布任务取连接失败", "err", err)
		return
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, relayLockID).Scan(&got); err != nil || !got {
		return
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, relayLockID)
	if _, err := r.PublishPending(ctx); err != nil {
		slog.Warn("事件暂未发出，留在发件表", "err", err)
	}
}

// StartCron 启动定时任务：每秒补发一次发件表。
func (r *Relay) StartCron(ctx context.Context) *cron.Cron {
	c := cron.New(cron.WithSeconds())
	_, _ = c.AddFunc("@every 1s", func() { r.RunOnce(ctx) })
	c.Start()
	return c
}

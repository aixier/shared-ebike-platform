package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// 本模块订阅的事件（contracts/events/rule.md「本模块订阅的事件」）。rule 不调用任何服务，订阅只用来建本地投影。
const (
	fleetStream        = "EVT_FLEET"
	consumerName       = "rule"
	subjectBrandListed = "fleet.brand.registered"
)

// errBadEvent 事件本身有问题（格式不对），重投也不会好；确认后丢弃并记日志。
var errBadEvent = errors.New("事件格式不正确")

// Consumer 订阅 fleet 的品牌事件，维护品牌投影；按 eventId 去重，处理成功才确认。
type Consumer struct {
	Pool *pgxpool.Pool
	NC   *nats.Conn
	Log  *slog.Logger
	Now  func() time.Time
}

func (c *Consumer) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Consumer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Run 建持久消费者并处理消息直到 ctx 结束；fleet 的流还不存在或总线暂不可用时每 2 秒重试。
func (c *Consumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := c.runOnce(ctx); err != nil && ctx.Err() == nil {
			c.log().Warn("订阅 fleet 品牌事件暂未成功，稍后重试", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *Consumer) runOnce(ctx context.Context) error {
	js, err := jetstream.New(c.NC)
	if err != nil {
		return err
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, fleetStream, jetstream.ConsumerConfig{
		Durable: consumerName, FilterSubject: subjectBrandListed,
		AckPolicy: jetstream.AckExplicitPolicy, MaxAckPending: 1, DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return err
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		err := c.Handle(ctx, m.Subject(), m.Data())
		switch {
		case err == nil:
			_ = m.Ack()
		case errors.Is(err, errBadEvent):
			c.log().Error("丢弃格式不正确的事件", "subject", m.Subject(), "err", err)
			_ = m.Term()
		default:
			c.log().Warn("事件处理失败，等待重投", "subject", m.Subject(), "err", err)
			_ = m.Nak()
		}
	})
	if err != nil {
		return err
	}
	defer cc.Stop()
	<-ctx.Done()
	return nil
}

type inEnvelope struct {
	EventID    string          `json:"eventId"`
	Type       string          `json:"type"`
	OccurredAt string          `json:"occurredAt"`
	Payload    json.RawMessage `json:"payload"`
}

type brandRegistered struct {
	BrandID    string `json:"brandId"`
	OperatorID string `json:"operatorId"`
	Name       string `json:"name"`
}

// Handle 处理一条事件：同一 eventId 只处理一次；未订阅的类型忽略。
func (c *Consumer) Handle(ctx context.Context, subject string, data []byte) error {
	var env inEnvelope
	if err := json.Unmarshal(data, &env); err != nil || env.EventID == "" {
		return fmt.Errorf("%w：缺 eventId 或不是 JSON", errBadEvent)
	}
	if env.Type != subjectBrandListed && subject != subjectBrandListed {
		return nil
	}
	var p brandRegistered
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.BrandID == "" {
		return fmt.Errorf("%w：品牌事件缺 brandId", errBadEvent)
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO consumed_events (event_id) VALUES ($1) ON CONFLICT DO NOTHING`, env.EventID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil // 已处理过
	}
	at := c.now()
	if t, err := time.Parse(time.RFC3339, env.OccurredAt); err == nil {
		at = t
	}
	if _, err := tx.Exec(ctx, `INSERT INTO brands (brand_id, operator_id, name, updated_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (brand_id) DO UPDATE SET operator_id = EXCLUDED.operator_id, name = EXCLUDED.name, updated_at = EXCLUDED.updated_at`,
		p.BrandID, p.OperatorID, p.Name, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// missingBrands 返回 ids 里不在品牌投影中的编号（升序）。
func missingBrands(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT brand_id FROM brands WHERE brand_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		have[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []string
	for _, id := range ids {
		if !have[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

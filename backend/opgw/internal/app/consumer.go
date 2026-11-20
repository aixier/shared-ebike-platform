package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// 订阅：fleet 的品牌接入状态变更、运营商已退出；rule 的平台参数变更。
var subscriptions = []struct {
	Stream   string
	Subjects []string
}{
	{"EVT_FLEET", []string{"fleet.brand.access_changed", "fleet.operator.exited"}},
	{"EVT_RULE", []string{"rule.params.changed"}},
}

// HandleEnvelope 按 eventId 去重后处理一条事件；去重与业务改动在同一事务。
func (a *App) HandleEnvelope(ctx context.Context, data []byte) error {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.EventID == "" {
		return nil // 无法识别的消息不重投
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO consumed_events (event_id, consumed_at) VALUES ($1,$2) ON CONFLICT DO NOTHING`, env.EventID, a.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	switch env.Type {
	case "fleet.brand.access_changed":
		var p struct {
			BrandID    string  `json:"brandId"`
			OperatorID string  `json:"operatorId"`
			From       string  `json:"from"`
			To         string  `json:"to"`
			Reason     *string `json:"reason"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil
		}
		err = a.onAccessChanged(ctx, tx, p.BrandID, p.OperatorID, p.To, p.Reason)
	case "fleet.operator.exited":
		var p struct {
			OperatorID string   `json:"operatorId"`
			BrandIDs   []string `json:"brandIds"`
			Reason     string   `json:"reason"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil
		}
		err = a.onOperatorExited(ctx, tx, p.OperatorID, p.BrandIDs, p.Reason)
	case "rule.params.changed":
		var p struct {
			BackfillWindowHours    *int    `json:"backfillWindowHours"`
			UnlockReportTimeoutSec *int    `json:"unlockReportTimeoutSec"`
			ChangedAt              *string `json:"changedAt"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE params SET backfill_window_hours=COALESCE($1, backfill_window_hours),
			unlock_report_timeout_sec=COALESCE($2, unlock_report_timeout_sec), changed_at=$3 WHERE id=1`,
			p.BackfillWindowHours, p.UnlockReportTimeoutSec, a.Now())
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RunConsumers 建持久消费者并顺序处理；流尚未建立时稍后重试。阻塞到 ctx 结束。
func (a *App) RunConsumers(ctx context.Context) {
	for _, s := range subscriptions {
		go a.consumeLoop(ctx, s.Stream, s.Subjects)
	}
	<-ctx.Done()
}

func (a *App) consumeLoop(ctx context.Context, stream string, subjects []string) {
	for ctx.Err() == nil {
		if err := a.consumeOnce(ctx, stream, subjects); err != nil {
			a.Log.Warn("事件消费暂停，稍后重试", "stream", stream, "err", err.Error())
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

func (a *App) consumeOnce(ctx context.Context, stream string, subjects []string) error {
	if a.JS == nil {
		return fmt.Errorf("未连接事件总线")
	}
	cons, err := a.JS.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:        "opgw",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: subjects,
		MaxAckPending:  1,
		AckWait:        30 * time.Second,
	})
	if err != nil {
		return err
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		if err := a.HandleEnvelope(ctx, m.Data()); err != nil {
			a.Log.Error("事件处理失败，等待重投", "subject", m.Subject(), "err", err.Error())
			_ = m.Nak()
			return
		}
		_ = m.Ack()
	})
	if err != nil {
		return err
	}
	defer cc.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-cc.Closed():
		return fmt.Errorf("消费中断")
	}
}

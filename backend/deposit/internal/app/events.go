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
	return "EV-deposit-" + hex.EncodeToString(b)
}

// emit 在业务事务里把事件写进发件表。
func (a *App) emit(ctx context.Context, tx pgx.Tx, typ, key string, actor Actor, payload any) error {
	p, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := Envelope{EventID: newEventID(), Type: typ, Version: 1, OccurredAt: a.now(), Key: key, Actor: actor, Payload: p}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (event_id, subject, body, created_at) VALUES ($1,$2,$3,$4)`, env.EventID, typ, body, a.now())
	return err
}

// audit 发"留痕已记录"事件（R-47）。
func (a *App) audit(ctx context.Context, tx pgx.Tx, actor Actor, action, targetType, targetID, summary string, reason *string, before, after any) error {
	return a.emit(ctx, tx, "deposit.audit.recorded", targetType+":"+targetID, actor, map[string]any{
		"action": action, "targetType": targetType, "targetId": targetID, "summary": summary, "reason": reason, "before": before, "after": after,
	})
}

// EnsureStream 启动时确保本模块的流存在：EVT_DEPOSIT，主题 deposit.>，文件存储，按时间保留。
func EnsureStream(ctx context.Context, js jetstream.JetStream, maxAge time.Duration) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "EVT_DEPOSIT", Subjects: []string{"deposit.>"}, Storage: jetstream.FileStorage, MaxAge: maxAge,
	})
	return err
}

// PublishPending 把发件表里未发布的事件按顺序发到总线，返回发布条数。遇到错误即停，下次接着发。
func (a *App) PublishPending(ctx context.Context, js jetstream.JetStream) (int, error) {
	rows, err := a.DB.Query(ctx, `SELECT seq, event_id, subject, body FROM outbox WHERE published_at IS NULL ORDER BY seq LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type item struct {
		seq      int64
		id, subj string
		body     []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.seq, &it.id, &it.subj, &it.body); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		msg := &nats.Msg{Subject: it.subj, Data: it.body, Header: nats.Header{}}
		msg.Header.Set(nats.MsgIdHdr, it.id)
		if _, err := js.PublishMsg(ctx, msg); err != nil {
			return n, err
		}
		if _, err := a.DB.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE seq = $1`, it.seq); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RunPublisher 周期发布发件表，直到 ctx 结束。总线不可用时事件留在发件表。
func (a *App) RunPublisher(ctx context.Context, js jetstream.JetStream, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := a.PublishPending(ctx, js); err != nil && ctx.Err() == nil {
			a.Log.Warn("发布事件失败，稍后重试", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// 订阅清单（contracts/events 各发布方文件里登记了 deposit 的事件）。
var subscriptions = map[string][]string{
	"EVT_PAY":  {"pay.payment.succeeded", "pay.payment.failed", "pay.payment.unknown", "pay.refund.succeeded", "pay.refund.failed", "pay.payee.submitted"},
	"EVT_RIDE": {"ride.order.created", "ride.order.unlock_failed", "ride.order.ended", "ride.admission.blocked"},
	"EVT_BILL": {"bill.bill.issued", "bill.bill.paid", "bill.arrears.created", "bill.arrears.cleared"},
	"EVT_ACCT": {"acct.user.registered", "acct.realname.changed", "acct.account.frozen", "acct.account.unfrozen", "acct.account.cancelled"},
	"EVT_RULE": {"rule.version.published", "rule.version.revoked", "rule.params.changed"},
}

// RunConsumers 为每个订阅的流建持久消费者 deposit 并顺序处理；流还不存在时隔 retry 再试。
func (a *App) RunConsumers(ctx context.Context, js jetstream.JetStream, retry time.Duration) {
	for stream, subjects := range subscriptions {
		go a.consumeStream(ctx, js, stream, subjects, retry)
	}
	<-ctx.Done()
}

func (a *App) consumeStream(ctx context.Context, js jetstream.JetStream, stream string, subjects []string, retry time.Duration) {
	var cons jetstream.Consumer
	for ctx.Err() == nil {
		c, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
			Durable: "deposit", AckPolicy: jetstream.AckExplicitPolicy, FilterSubjects: subjects, MaxAckPending: 1, DeliverPolicy: jetstream.DeliverAllPolicy,
		})
		if err == nil {
			cons = c
			break
		}
		if !errors.Is(err, jetstream.ErrStreamNotFound) {
			a.Log.Warn("建消费者失败", "stream", stream, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
	for ctx.Err() == nil {
		batch, err := cons.Fetch(1, jetstream.FetchMaxWait(retry))
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(retry):
			}
			continue
		}
		for m := range batch.Messages() {
			if err := a.HandleEvent(ctx, m.Data()); err != nil {
				a.Log.Warn("处理事件失败，等待重投", "subject", m.Subject(), "err", err)
				_ = m.Nak()
				continue
			}
			_ = m.Ack()
		}
	}
}

// HandleEvent 处理一条订阅事件：按 eventId 去重，投影与业务处理在同一事务里；事务提交后再做对外调用。
func (a *App) HandleEvent(ctx context.Context, raw []byte) error {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		a.Log.Error("事件无法解析，丢弃", "err", err)
		return nil
	}
	if env.EventID == "" {
		a.Log.Error("事件缺 eventId，丢弃", "type", env.Type)
		return nil
	}
	var after func(context.Context)
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO inbox (event_id, type, processed_at) VALUES ($1,$2,now()) ON CONFLICT DO NOTHING`, env.EventID, env.Type)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // 已处理过
		}
		h, ok := eventHandlers[env.Type]
		if !ok {
			return nil
		}
		after, err = h(a, ctx, tx, env)
		if err != nil {
			return fmt.Errorf("%s：%w", env.Type, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if after != nil {
		after(ctx)
	}
	return nil
}

type eventHandler func(a *App, ctx context.Context, tx pgx.Tx, env Envelope) (after func(context.Context), err error)

var eventHandlers map[string]eventHandler

func init() {
	eventHandlers = map[string]eventHandler{
		"ride.order.created":       onRideCreated,
		"ride.order.unlock_failed": onRideFinished,
		"ride.order.ended":         onRideFinished,
		"ride.admission.blocked":   onAdmissionBlocked,
		"bill.bill.issued":         onBillIssued,
		"bill.bill.paid":           onBillPaid,
		"bill.arrears.created":     onArrearsCreated,
		"bill.arrears.cleared":     onArrearsCleared,
		"acct.user.registered":     onUserRegistered,
		"acct.realname.changed":    onRealname,
		"acct.account.frozen":      onFrozen,
		"acct.account.unfrozen":    onUnfrozen,
		"acct.account.cancelled":   onCancelled,
		"rule.version.published":   onRulePublished,
		"rule.version.revoked":     onRuleRevoked,
		"rule.params.changed":      onParamsChanged,
		"pay.payment.succeeded":    onPaymentResult,
		"pay.payment.failed":       onPaymentResult,
		"pay.payment.unknown":      onPaymentResult,
		"pay.refund.succeeded":     onRefundSucceeded,
		"pay.refund.failed":        onRefundFailed,
		"pay.payee.submitted":      onPayeeSubmitted,
	}
}

func decode(env Envelope, v any) error {
	if err := json.Unmarshal(env.Payload, v); err != nil {
		return fmt.Errorf("载荷无法解析：%w", err)
	}
	return nil
}

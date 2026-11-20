package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// envelope 事件信封
type envelope struct {
	EventID    string          `json:"eventId"`
	Type       string          `json:"type"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurredAt"`
	Key        string          `json:"key"`
	Payload    json.RawMessage `json:"payload"`
}

// subscriptions 本服务订阅的流与主题（登记见各发布方的 contracts/events/<模块>.md）
var subscriptions = map[string][]string{
	"EVT_BILL":    {"bill.bill.issued", "bill.bill.paid", "bill.arrears.created", "bill.arrears.overdue", "bill.arrears.cleared", "bill.bill.refunded"},
	"EVT_RIDE":    {"ride.order.created", "ride.order.unlock_failed", "ride.order.ended", "ride.order.over_limit_reminded"},
	"EVT_DEPOSIT": {"deposit.status.changed", "deposit.deducted", "deposit.refunded"},
	"EVT_OPS":     {"ops.degrade.entered", "ops.degrade.exited"},
	"EVT_RULE":    {"rule.version.published", "rule.version.revoked"},
}

// 载荷里用到的字段（订阅方只依赖发布方文件里写出的字段）
type evPayload struct {
	UserID      string          `json:"userId"`
	OrderID     *string         `json:"orderId"`
	Amount      int64           `json:"amount"`
	Cause       string          `json:"cause"`
	CreatedAt   *time.Time      `json:"createdAt"`
	ClearedAt   *time.Time      `json:"clearedAt"`
	Days        int             `json:"days"`
	CollectBy   string          `json:"collectBy"`
	Payable     int64           `json:"payable"`
	Minutes     int             `json:"billedMinutes"`
	PayDeadline *time.Time      `json:"payDeadline"`
	PayStatus   string          `json:"payStatus"`
	ChannelPaid int64           `json:"channelPaid"`
	Mode        string          `json:"mode"`
	Status      *string         `json:"status"`
	Balance     *int64          `json:"balance"`
	BalanceAft  int64           `json:"balanceAfter"`
	ChangedAt   *time.Time      `json:"changedAt"`
	RefundedAt  *time.Time      `json:"refundedAt"`
	LimitMin    int             `json:"limitMinutes"`
	LimitAmt    int64           `json:"limitAmount"`
	EventID     string          `json:"eventId"` // ops 降级事件编号
	Components  []string        `json:"components"`
	EnteredAt   *time.Time      `json:"enteredAt"`
	ExitedAt    *time.Time      `json:"exitedAt"`
	VersionID   string          `json:"versionId"`
	EffectiveAt *time.Time      `json:"effectiveAt"`
	RiskLimits  *riskLimits     `json:"riskLimits"`
	Raw         json.RawMessage `json:"-"`
}

type riskLimits struct {
	Tight *struct {
		MaxMinutes int   `json:"maxMinutes"`
		MaxAmount  int64 `json:"maxAmount"`
	} `json:"tight"`
}

// HandleEvent 处理一条订阅事件：按 eventId 去重，投影与副作用在同一事务里
func (a *App) HandleEvent(ctx context.Context, data []byte) error {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		a.Log.Warn("事件信封无法解析，丢弃", "err", err)
		return nil
	}
	if env.EventID == "" {
		return nil
	}
	var p evPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		a.Log.Warn("事件载荷无法解析，丢弃", "type", env.Type, "eventId", env.EventID, "err", err)
		return nil
	}
	p.Raw = env.Payload
	occurred, _ := time.Parse(time.RFC3339, env.OccurredAt)
	reverify := false
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO processed_events (event_id, type, processed_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, env.EventID, env.Type, a.Now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // 已处理过
		}
		reverify, err = a.apply(ctx, tx, env.Type, occurred, p)
		return err
	})
	if err != nil {
		return err
	}
	if reverify {
		// 降级退出：对先骑后验用户补做实名核验（调外部服务，不在事务里）
		a.goBackground(func(ctx context.Context) {
			if err := a.reverifyPending(ctx); err != nil {
				a.Log.Error("补验失败", "err", err)
			}
		})
	}
	return nil
}

func orderOf(p evPayload) string { return deref(p.OrderID) }

// lockUser 读并锁住用户；不存在的用户返回 nil（投影照常维护）
func lockUser(ctx context.Context, tx pgx.Tx, uid string) (*userRow, error) {
	u, err := loadUser(ctx, tx, uid, true)
	if errors.Is(err, errUserNotFound) {
		return nil, nil
	}
	return u, err
}

func tsOr(t *time.Time, d time.Time) time.Time {
	if t != nil {
		return *t
	}
	return d
}

// apply 事件的投影与副作用；返回是否需要补验
func (a *App) apply(ctx context.Context, tx pgx.Tx, typ string, occurred time.Time, p evPayload) (bool, error) {
	now := a.Now()
	if occurred.IsZero() {
		occurred = now
	}
	oid := orderOf(p)
	var oidp *string
	if oid != "" {
		oidp = &oid
	}
	switch typ {
	// ---------------- ride ----------------
	case "ride.order.created":
		if _, err := tx.Exec(ctx, `INSERT INTO riding_orders (order_id, user_id, created_at) SELECT $1,$2,$3
			WHERE NOT EXISTS (SELECT 1 FROM closed_orders WHERE order_id=$1) ON CONFLICT DO NOTHING`, oid, p.UserID, occurred); err != nil {
			return false, err
		}
	case "ride.order.unlock_failed", "ride.order.ended":
		if _, err := tx.Exec(ctx, `DELETE FROM riding_orders WHERE order_id=$1`, oid); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO closed_orders (order_id) VALUES ($1) ON CONFLICT DO NOTHING`, oid); err != nil {
			return false, err
		}
		if typ == "ride.order.ended" {
			if _, err := tx.Exec(ctx, `UPDATE users SET has_ridden=true WHERE user_id=$1`, p.UserID); err != nil {
				return false, err
			}
		}
	case "ride.order.over_limit_reminded":
		return false, a.addNotice(ctx, tx, p.UserID, "骑行", "请尽快还车",
			fmt.Sprintf("本次骑行已超过上限（%d 分钟或 ¥%s），请尽快就近还车。骑行不会被中断，继续计费。", p.LimitMin, yuan(p.LimitAmt)), oidp)

	// ---------------- bill ----------------
	case "bill.bill.issued":
		if _, err := tx.Exec(ctx, `INSERT INTO order_bills (order_id, user_id, collect_by, payable, billed_minutes, pay_deadline)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (order_id) DO NOTHING`, oid, p.UserID, p.CollectBy, p.Payable, p.Minutes, p.PayDeadline); err != nil {
			return false, err
		}
		switch {
		case p.CollectBy == "一键支付" && p.Payable > 0:
			text := fmt.Sprintf("%d 分钟，应付 ¥%s，请尽快一键支付", p.Minutes, yuan(p.Payable))
			if p.PayDeadline != nil {
				text = fmt.Sprintf("%d 分钟，应付 ¥%s，请在 %s 前一键支付", p.Minutes, yuan(p.Payable), p.PayDeadline.In(cst).Format("1月2日 15:04"))
			}
			return false, a.addNotice(ctx, tx, p.UserID, "账单", "已还车，请支付本单费用", text, oidp)
		case p.Payable == 0 || p.CollectBy == "无需支付":
			return false, a.addNotice(ctx, tx, p.UserID, "账单", "已还车，本单无需支付", fmt.Sprintf("%d 分钟，本单无需支付", p.Minutes), oidp)
		}
		// 免押自动扣款：等扣款结果再通知
	case "bill.bill.paid":
		var collect string
		var minutes int
		err := tx.QueryRow(ctx, `UPDATE order_bills SET paid=true WHERE order_id=$1 RETURNING collect_by, billed_minutes`, oid).Scan(&collect, &minutes)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM arrears_orders WHERE order_id=$1`, oid); err != nil {
			return false, err
		}
		if p.PayStatus == "已支付" && collect == "免押自动扣款" {
			if err := a.addNotice(ctx, tx, p.UserID, "账单", "已还车，已自动扣款", fmt.Sprintf("%d 分钟，已自动扣款 ¥%s", minutes, yuan(p.ChannelPaid)), oidp); err != nil {
				return false, err
			}
		}
		// 押金抵扣的通知由 deposit.deducted 发（D-23），这里不重复
		return false, a.autoUnfreezeUser(ctx, tx, p.UserID)
	case "bill.arrears.created":
		if _, err := tx.Exec(ctx, `INSERT INTO arrears_orders (order_id, user_id, amount, created_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT (order_id) DO UPDATE SET amount=EXCLUDED.amount`, oid, p.UserID, p.Amount, tsOr(p.CreatedAt, occurred)); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE order_bills SET in_arrears=true WHERE order_id=$1`, oid); err != nil {
			return false, err
		}
		title, text := "本单已记为欠费", fmt.Sprintf("订单 %s ¥%s 已记为欠费，补付后即可继续骑行", oid, yuan(p.Amount))
		switch p.Cause {
		case "扣款失败":
			title, text = "自动扣款没有成功", fmt.Sprintf("本单 ¥%s 已记为欠费，下次扫码时一键补付即可", yuan(p.Amount))
		case "押金不足":
			text = fmt.Sprintf("订单 %s 超时未支付且押金不足以抵扣，¥%s 已记为欠费，补付后即可继续骑行", oid, yuan(p.Amount))
		case "核验未通过追缴":
			title, text = "本单费用已转为欠费", fmt.Sprintf("实名核验未通过，订单 %s 的费用 ¥%s 已转为欠费", oid, yuan(p.Amount))
		}
		return false, a.addNotice(ctx, tx, p.UserID, "欠费", title, text, oidp)
	case "bill.arrears.overdue":
		u, err := lockUser(ctx, tx, p.UserID)
		if err != nil || u == nil {
			return false, err
		}
		reason := "欠费追缴未结"
		if p.Days > 0 {
			reason = fmt.Sprintf("欠费追缴未结（已欠 %d 天）", p.Days)
		}
		return false, a.freeze(ctx, tx, u, "欠费", reason, "结清欠费后自动解冻", systemActor())
	case "bill.arrears.cleared":
		if _, err := tx.Exec(ctx, `DELETE FROM arrears_orders WHERE user_id=$1 AND created_at <= $2`, p.UserID, tsOr(p.ClearedAt, occurred)); err != nil {
			return false, err
		}
		return false, a.autoUnfreezeUser(ctx, tx, p.UserID)
	case "bill.bill.refunded":
		return false, a.addNotice(ctx, tx, p.UserID, "退款", "退款已退回", fmt.Sprintf("订单 %s 退款 ¥%s 已退回。", oid, yuan(p.Amount)), oidp)

	// ---------------- deposit ----------------
	case "deposit.status.changed":
		at := tsOr(p.ChangedAt, occurred)
		_, err := tx.Exec(ctx, `INSERT INTO user_deposit (user_id, mode, status, balance, changed_at) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (user_id) DO UPDATE SET mode=EXCLUDED.mode, status=EXCLUDED.status, balance=EXCLUDED.balance, changed_at=EXCLUDED.changed_at
			WHERE user_deposit.changed_at <= EXCLUDED.changed_at`, p.UserID, p.Mode, p.Status, p.Balance, at)
		return false, err
	case "deposit.deducted":
		return false, a.addNotice(ctx, tx, p.UserID, "押金抵扣", "已从押金抵扣本单费用",
			fmt.Sprintf("订单 %s 已从押金抵扣 ¥%s，押金余额 ¥%s。补缴押金后即可继续骑行。", oid, yuan(p.Amount), yuan(p.BalanceAft)), oidp)
	case "deposit.refunded":
		at := tsOr(p.RefundedAt, occurred)
		if _, err := tx.Exec(ctx, `INSERT INTO user_deposit (user_id, mode, status, balance, changed_at) VALUES ($1,'押金','已退',0,$2)
			ON CONFLICT (user_id) DO UPDATE SET status='已退', balance=0, changed_at=EXCLUDED.changed_at
			WHERE user_deposit.changed_at <= EXCLUDED.changed_at`, p.UserID, at); err != nil {
			return false, err
		}
		how := "原路退回"
		if p.Mode == "人工转账" {
			how = "通过人工转账退回"
		}
		return false, a.addNotice(ctx, tx, p.UserID, "押金", "押金已退回", fmt.Sprintf("押金 ¥%s 已%s。", yuan(p.Amount), how), nil)

	// ---------------- ops ----------------
	case "ops.degrade.entered":
		comps := p.Components
		if comps == nil {
			comps = []string{}
		}
		_, err := tx.Exec(ctx, `INSERT INTO degrade_state (id, event_id, components, active, entered_at, exited_at) VALUES (1,$1,$2,true,$3,NULL)
			ON CONFLICT (id) DO UPDATE SET event_id=EXCLUDED.event_id, components=EXCLUDED.components, active=true, entered_at=EXCLUDED.entered_at, exited_at=NULL`,
			p.EventID, comps, tsOr(p.EnteredAt, occurred))
		return false, err
	case "ops.degrade.exited":
		if _, err := tx.Exec(ctx, `UPDATE degrade_state SET active=false, exited_at=$2 WHERE id=1 AND (event_id=$1 OR $1='')`, p.EventID, tsOr(p.ExitedAt, occurred)); err != nil {
			return false, err
		}
		return true, nil

	// ---------------- rule ----------------
	case "rule.version.published":
		var mins *int
		var amt *int64
		if p.RiskLimits != nil && p.RiskLimits.Tight != nil {
			mins, amt = &p.RiskLimits.Tight.MaxMinutes, &p.RiskLimits.Tight.MaxAmount
		}
		_, err := tx.Exec(ctx, `INSERT INTO rule_versions (version_id, effective_at, tight_minutes, tight_amount, body) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (version_id) DO NOTHING`, p.VersionID, tsOr(p.EffectiveAt, occurred), mins, amt, []byte(p.Raw))
		return false, err
	case "rule.version.revoked":
		_, err := tx.Exec(ctx, `UPDATE rule_versions SET revoked=true WHERE version_id=$1`, p.VersionID)
		return false, err
	}
	return false, nil
}

func (a *App) autoUnfreezeUser(ctx context.Context, tx pgx.Tx, uid string) error {
	u, err := lockUser(ctx, tx, uid)
	if err != nil || u == nil {
		return err
	}
	return a.tryAutoUnfreeze(ctx, tx, u)
}

// ---------------- 事件总线 ----------------

// EnsureStream 确保本服务的流 EVT_ACCT 存在
func (a *App) EnsureStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "EVT_ACCT", Subjects: []string{"acct.>"}, Storage: jetstream.FileStorage, MaxAge: a.Cfg.EventRetention,
	})
	return err
}

// PublishPending 把发件表里未发布的事件按顺序发到总线；总线不可用时留在发件表里，恢复后补发
func (a *App) PublishPending(ctx context.Context, js jetstream.JetStream) (int, error) {
	rows, err := a.Pool.Query(ctx, `SELECT id, event_id, subject, body FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT 200`)
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
		msg := &nats.Msg{Subject: it.subj, Data: it.body, Header: nats.Header{}}
		msg.Header.Set(nats.MsgIdHdr, it.eid)
		if _, err := js.PublishMsg(ctx, msg); err != nil {
			return n, err
		}
		if _, err := a.Pool.Exec(ctx, `UPDATE outbox SET published_at=$2 WHERE id=$1`, it.id, a.Now()); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RunPublisher 持续发布发件表（有新事件时立即发，另每秒兜底一次）
func (a *App) RunPublisher(ctx context.Context, js jetstream.JetStream) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	streamOK := false
	for {
		if !streamOK {
			if err := a.EnsureStream(ctx, js); err != nil {
				a.Log.Warn("事件流暂不可用", "err", err)
			} else {
				streamOK = true
			}
		}
		if streamOK {
			if _, err := a.PublishPending(ctx, js); err != nil {
				a.Log.Warn("发布事件失败，稍后补发", "err", err)
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

// RunConsumers 在各发布方的流上建持久消费者 acct，一次只处理一条未确认消息以保序
func (a *App) RunConsumers(ctx context.Context, js jetstream.JetStream) {
	for stream, subjects := range subscriptions {
		go a.consumeStream(ctx, js, stream, subjects)
	}
	<-ctx.Done()
}

func (a *App) consumeStream(ctx context.Context, js jetstream.JetStream, stream string, subjects []string) {
	for ctx.Err() == nil {
		cons, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
			Durable: "acct", AckPolicy: jetstream.AckExplicitPolicy, FilterSubjects: subjects, MaxAckPending: 1,
			DeliverPolicy: jetstream.DeliverAllPolicy, AckWait: 30 * time.Second,
		})
		if err != nil {
			// 发布方的流还没建好：稍后重试
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		for ctx.Err() == nil {
			batch, err := cons.Fetch(1, jetstream.FetchMaxWait(time.Second))
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				break // 重建消费者
			}
			for msg := range batch.Messages() {
				if err := a.HandleEvent(ctx, msg.Data()); err != nil {
					a.Log.Error("处理事件失败，等待重投", "subject", msg.Subject(), "err", err)
					_ = msg.NakWithDelay(time.Second)
					continue
				}
				_ = msg.Ack()
			}
		}
	}
}

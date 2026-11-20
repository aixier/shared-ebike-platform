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

const (
	streamName     = "EVT_ENFORCE"
	fleetStream    = "EVT_FLEET"
	consumerName   = "enforce"
	publishBatch   = 100
	busRetryPeriod = time.Second
)

// 订阅的 fleet 事件（contracts/events/fleet.md 中登记了 enforce 的）
var fleetSubjects = []string{
	"fleet.brand.registered",
	"fleet.brand.access_changed",
	"fleet.vehicle.listed",
	"fleet.vehicle.removed",
	"fleet.code.status_changed",
	"fleet.operator.exited",
}

// ---------- 发布 ----------

// EnsureStream 确保本服务的流存在。
func (a *App) EnsureStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{"enforce.>"},
		Storage:  jetstream.FileStorage,
		MaxAge:   a.StreamMaxAge,
	})
	return err
}

// PublishPending 把发件表里未发布的事件按顺序发到总线，返回发布条数。
// 总线不可用时返回错误，事件留在发件表，下次补发。
func (a *App) PublishPending(ctx context.Context, js jetstream.JetStream) (int, error) {
	rows, err := a.Pool.Query(ctx, "SELECT id, event_id, subject, body FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT $1", publishBatch)
	if err != nil {
		return 0, err
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
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, it := range items {
		msg := &nats.Msg{Subject: it.subject, Data: it.body, Header: nats.Header{}}
		msg.Header.Set(nats.MsgIdHdr, it.eventID)
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := js.PublishMsg(pctx, msg)
		cancel()
		if err != nil {
			return n, fmt.Errorf("发布事件 %s 失败：%w", it.eventID, err)
		}
		if _, err := a.Pool.Exec(ctx, "UPDATE outbox SET published_at = now() WHERE id = $1", it.id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RunPublisher 持续补发发件表，直到 ctx 结束。
func (a *App) RunPublisher(ctx context.Context, js jetstream.JetStream) {
	streamReady := false
	t := time.NewTicker(busRetryPeriod)
	defer t.Stop()
	for {
		if !streamReady {
			if err := a.EnsureStream(ctx, js); err != nil {
				a.Log.Warn("确保事件流失败，稍后重试", "err", err)
			} else {
				streamReady = true
			}
		}
		if streamReady {
			if _, err := a.PublishPending(ctx, js); err != nil {
				a.Log.Warn("补发事件失败，稍后重试", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---------- 消费 ----------

// RunConsumer 在 fleet 的流上建持久消费者 enforce 并顺序处理，直到 ctx 结束。
// fleet 的流尚未建立时等待重试。
func (a *App) RunConsumer(ctx context.Context, js jetstream.JetStream) error {
	for {
		cons, err := js.CreateOrUpdateConsumer(ctx, fleetStream, jetstream.ConsumerConfig{
			Durable:        consumerName,
			AckPolicy:      jetstream.AckExplicitPolicy,
			DeliverPolicy:  jetstream.DeliverAllPolicy,
			FilterSubjects: fleetSubjects,
			MaxAckPending:  1,
			AckWait:        30 * time.Second,
		})
		if err == nil {
			cc, err := cons.Consume(func(msg jetstream.Msg) {
				if err := a.HandleEvent(ctx, msg.Data()); err != nil {
					a.Log.Error("处理事件失败，等待重投", "subject", msg.Subject(), "err", err)
					_ = msg.NakWithDelay(busRetryPeriod)
					return
				}
				_ = msg.Ack()
			})
			if err == nil {
				<-ctx.Done()
				cc.Stop()
				return nil
			}
			a.Log.Warn("开始消费失败，稍后重试", "err", err)
		} else {
			a.Log.Warn("建消费者失败，稍后重试", "stream", fleetStream, "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(busRetryPeriod):
		}
	}
}

type inEnvelope struct {
	EventID    string          `json:"eventId"`
	Type       string          `json:"type"`
	OccurredAt string          `json:"occurredAt"`
	Payload    json.RawMessage `json:"payload"`
}

// HandleEvent 处理一条订阅的事件：按 eventId 去重后更新投影，同一事务内完成。
func (a *App) HandleEvent(ctx context.Context, data []byte) error {
	var env inEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("事件不是有效的信封：%w", err)
	}
	if env.EventID == "" || env.Type == "" {
		return errors.New("事件缺少 eventId 或 type")
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, "INSERT INTO processed_events (event_id, type) VALUES ($1,$2) ON CONFLICT DO NOTHING", env.EventID, env.Type)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx) // 已处理过
	}
	if err := applyFleetEvent(ctx, tx, env.Type, env.Payload); err != nil {
		return fmt.Errorf("事件 %s（%s）：%w", env.EventID, env.Type, err)
	}
	return tx.Commit(ctx)
}

func parseTimePtr(s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// applyFleetEvent 把 fleet 事件落到车辆、品牌、运营商投影。只做字段搬运，不做业务判断。
func applyFleetEvent(ctx context.Context, tx pgx.Tx, typ string, raw json.RawMessage) error {
	switch typ {
	case "fleet.brand.registered":
		var p struct {
			BrandID, OperatorID, Name, OperatorName, OperatorShortName, AccessStatus string
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO brands (brand_id, operator_id, name, access_status) VALUES ($1,$2,$3,$4)
			ON CONFLICT (brand_id) DO UPDATE SET operator_id=EXCLUDED.operator_id, name=EXCLUDED.name, access_status=EXCLUDED.access_status, updated_at=now()`,
			p.BrandID, p.OperatorID, p.Name, p.AccessStatus); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO operators (operator_id, name, short_name) VALUES ($1,$2,$3)
			ON CONFLICT (operator_id) DO UPDATE SET name=EXCLUDED.name, short_name=EXCLUDED.short_name, updated_at=now()`,
			p.OperatorID, p.OperatorName, p.OperatorShortName)
		return err

	case "fleet.brand.access_changed":
		var p struct{ BrandID, OperatorID, To string }
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO brands (brand_id, operator_id, access_status) VALUES ($1,$2,$3)
			ON CONFLICT (brand_id) DO UPDATE SET access_status=EXCLUDED.access_status, updated_at=now()`,
			p.BrandID, p.OperatorID, p.To)
		return err

	case "fleet.vehicle.listed":
		var p struct {
			VehicleID, BrandID, OperatorID, PlateNo, GovCode, CodeStatus string
			Model                                                        *string
			CodeEnableAt                                                 *string
			ListedAt                                                     *string
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		enableAt, err := parseTimePtr(p.CodeEnableAt)
		if err != nil {
			return err
		}
		listedAt, err := parseTimePtr(p.ListedAt)
		if err != nil {
			return err
		}
		// 契约没有发码时刻，以入清单时刻作发码时刻（首次入清单时记，之后不变）
		_, err = tx.Exec(ctx, `INSERT INTO vehicles (vehicle_id, gov_code, plate_no, model, brand_id, operator_id, code_status, code_issued_at, code_enable_at, code_invalid_at, removed)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULL,false)
			ON CONFLICT (vehicle_id) DO UPDATE SET gov_code=EXCLUDED.gov_code, plate_no=EXCLUDED.plate_no, model=EXCLUDED.model,
				brand_id=EXCLUDED.brand_id, operator_id=EXCLUDED.operator_id, code_status=EXCLUDED.code_status,
				code_issued_at=COALESCE(vehicles.code_issued_at, EXCLUDED.code_issued_at), code_enable_at=EXCLUDED.code_enable_at,
				code_invalid_at=NULL, removed=false, updated_at=now()`,
			p.VehicleID, p.GovCode, p.PlateNo, p.Model, p.BrandID, p.OperatorID, p.CodeStatus, listedAt, enableAt)
		return err

	case "fleet.vehicle.removed":
		var p struct {
			VehicleID, BrandID, OperatorID, GovCode string
			RemovedAt                               *string
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		at, err := parseTimePtr(p.RemovedAt)
		if err != nil {
			return err
		}
		// 移出清单的车码随之失效：保留投影并标记，扫到时显示"码已失效"
		_, err = tx.Exec(ctx, `INSERT INTO vehicles (vehicle_id, gov_code, brand_id, operator_id, code_status, code_invalid_at, removed)
			VALUES ($1,$2,$3,$4,'已失效',$5,true)
			ON CONFLICT (vehicle_id) DO UPDATE SET code_status='已失效', code_invalid_at=COALESCE(vehicles.code_invalid_at, EXCLUDED.code_invalid_at),
				removed=true, updated_at=now()`,
			p.VehicleID, p.GovCode, p.BrandID, p.OperatorID, at)
		return err

	case "fleet.code.status_changed":
		var p struct {
			VehicleID, BrandID, GovCode, To string
			ChangedAt                       *string
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		at, err := parseTimePtr(p.ChangedAt)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO vehicles (vehicle_id, gov_code, brand_id, code_status, code_invalid_at)
			VALUES ($1,$2,$3,$4, CASE WHEN $4='已失效' THEN $5::timestamptz END)
			ON CONFLICT (vehicle_id) DO UPDATE SET code_status=EXCLUDED.code_status, code_invalid_at=EXCLUDED.code_invalid_at, updated_at=now()`,
			p.VehicleID, p.GovCode, p.BrandID, p.To, at)
		return err

	case "fleet.operator.exited":
		var p struct {
			OperatorID string
			ExitedAt   *string
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		at, err := parseTimePtr(p.ExitedAt)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO operators (operator_id, status, exited_at) VALUES ($1,'已退出',$2)
			ON CONFLICT (operator_id) DO UPDATE SET status='已退出', exited_at=EXCLUDED.exited_at, updated_at=now()`,
			p.OperatorID, at)
		return err
	}
	return nil // 未订阅的类型：记去重后忽略
}

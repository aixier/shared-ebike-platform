package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"

	"bike/ride/internal/geo"
	"bike/ride/internal/rules"
)

// Subscriptions 订阅的流与事件类型（ride/README.md 订阅事件）。
var Subscriptions = map[string][]string{
	"EVT_ACCT":    {"acct.user.registered", "acct.realname.changed", "acct.account.frozen", "acct.account.unfrozen", "acct.account.cancelled"},
	"EVT_DEPOSIT": {"deposit.status.changed", "deposit.refund.requested"},
	"EVT_BILL":    {"bill.bill.issued", "bill.bill.paid", "bill.arrears.created", "bill.arrears.cleared"},
	"EVT_FLEET":   {"fleet.brand.registered", "fleet.brand.access_changed", "fleet.vehicle.listed", "fleet.vehicle.removed", "fleet.code.status_changed"},
	"EVT_SITE":    {"site.station.changed", "site.pile.status_changed", "site.pile.occupancy"},
	"EVT_RULE":    {"rule.version.published", "rule.version.revoked", "rule.params.changed"},
	"EVT_OPS":     {"ops.degrade.entered", "ops.degrade.tightened", "ops.degrade.stopped", "ops.degrade.relaxed", "ops.degrade.exited"},
}

type snapChange struct {
	kind, id string
	doc      any
}

// HandleEvent 处理一条订阅的事件：按 eventId 去重，更新准入快照（同一事务），提交后刷新本实例内存。
func (s *Service) HandleEvent(ctx context.Context, env Envelope) error {
	if env.EventID == "" {
		return nil
	}
	// 涉及订单的先做（幂等），再在事务里去重并写快照
	if env.Type == "site.pile.occupancy" {
		var p struct {
			PileID, StationID, VehicleID string
			Present                      bool
			At                           time.Time
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil
		}
		if err := s.OnPileOccupancy(ctx, p.PileID, p.StationID, p.VehicleID, p.Present, p.At); err != nil {
			return err
		}
	}
	tx, err := s.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO inbox(event_id, type) VALUES ($1,$2) ON CONFLICT DO NOTHING`, env.EventID, env.Type)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	changes, err := s.applyEvent(ctx, tx, env)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, c := range changes {
		b, _ := json.Marshal(c.doc)
		s.snap.apply(c.kind, c.id, b)
	}
	if env.Type == "ops.degrade.exited" {
		go s.Replay(context.Background())
	}
	return nil
}

func mutU(ctx context.Context, tx pgx.Tx, id string, fn func(*rules.User)) (snapChange, error) {
	v, err := mutateSnap(ctx, tx, kUser, id, func(u *rules.User) {
		u.UserID = id
		if u.RealName == "" {
			u.RealName = rules.RNNone
		}
		if u.DepositMode == "" {
			u.DepositMode = rules.DepNone
		}
		fn(u)
	})
	return snapChange{kUser, id, v}, err
}

func one(c snapChange, err error) ([]snapChange, error) {
	if err != nil {
		return nil, err
	}
	return []snapChange{c}, nil
}

// applyEvent 各事件对快照的影响。字段只用发布方文件里写出的。
func (s *Service) applyEvent(ctx context.Context, tx pgx.Tx, env Envelope) ([]snapChange, error) {
	var p map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil, nil // 载荷坏了不重投（重投也不会好）
	}
	str := func(k string) string {
		var v string
		_ = json.Unmarshal(p[k], &v)
		return v
	}
	i64 := func(k string) int64 {
		var v int64
		_ = json.Unmarshal(p[k], &v)
		return v
	}
	tm := func(k string) *time.Time {
		var v *time.Time
		_ = json.Unmarshal(p[k], &v)
		return v
	}
	bl := func(k string) bool {
		var v bool
		_ = json.Unmarshal(p[k], &v)
		return v
	}
	uid := str("userId")
	switch env.Type {
	// ---------- acct ----------
	case "acct.user.registered":
		return one(mutU(ctx, tx, uid, func(u *rules.User) { u.DuringDegrade = bl("duringDegrade"); u.Channel = str("channel") }))
	case "acct.realname.changed":
		return one(mutU(ctx, tx, uid, func(u *rules.User) { u.RealName = str("status") }))
	case "acct.account.frozen":
		return one(mutU(ctx, tx, uid, func(u *rules.User) { u.Frozen, u.FreezeReason, u.UnfreezeHow = true, str("reason"), str("unfreezeHow") }))
	case "acct.account.unfrozen":
		return one(mutU(ctx, tx, uid, func(u *rules.User) { u.Frozen, u.FreezeReason, u.UnfreezeHow = false, "", "" }))
	case "acct.account.cancelled":
		return one(mutU(ctx, tx, uid, func(u *rules.User) { u.Cancelled = true }))
	// ---------- deposit ----------
	case "deposit.status.changed":
		return one(mutU(ctx, tx, uid, func(u *rules.User) {
			u.DepositMode, u.DepositStatus = str("mode"), str("status")
			u.DepositBalance, u.DepositPaid = i64("balance"), i64("amountPaid")
			if u.DepositStatus != rules.DepStRefunding {
				u.RefundRequestID = ""
			}
		}))
	case "deposit.refund.requested":
		// 把该用户改为不可骑，回"已阻止开单"作为退押回执（BR-24）。
		c, err := mutU(ctx, tx, uid, func(u *rules.User) {
			u.DepositMode, u.DepositStatus, u.RefundRequestID = rules.DepPaid, rules.DepStRefunding, str("requestId")
		})
		if err != nil {
			return nil, err
		}
		var riding *string
		var rid string
		err = tx.QueryRow(ctx, `SELECT order_id FROM orders WHERE user_id=$1 AND status IN ('开锁中','骑行中') LIMIT 1`, uid).Scan(&rid)
		if err == nil {
			riding = &rid
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		// 各实例按刷新间隔从库里取快照：等两个刷新间隔后所有实例都已生效，回执在那之后才发出
		eff := s.now().Add(2*s.opt.SnapshotRefresh + time.Second)
		e := newEnvelope("ride.admission.blocked", uid, eff, systemActor, map[string]any{
			"userId": uid, "requestId": str("requestId"), "blockedAt": ts(eff), "ridingOrderId": riding,
		})
		e.notBefore = &eff
		if err := insertOutbox(ctx, tx, []Envelope{e}); err != nil {
			return nil, err
		}
		return []snapChange{c}, nil
	// ---------- bill ----------
	case "bill.bill.issued":
		if str("collectBy") != "一键支付" || i64("payable") <= 0 {
			return nil, nil
		}
		oid, amt, dl := str("orderId"), i64("payable"), tm("payDeadline")
		return one(mutU(ctx, tx, uid, func(u *rules.User) {
			if u.Unpaid == nil {
				u.Unpaid = map[string]rules.UnpaidItem{}
			}
			u.Unpaid[oid] = rules.UnpaidItem{Amount: amt, Deadline: dl}
		}))
	case "bill.bill.paid":
		oid := str("orderId")
		if _, err := tx.Exec(ctx, `UPDATE orders SET fee_settled=true WHERE order_id=$1`, oid); err != nil {
			return nil, err
		}
		return one(mutU(ctx, tx, uid, func(u *rules.User) { delete(u.Unpaid, oid) }))
	case "bill.arrears.created":
		oid := str("orderId")
		if _, err := tx.Exec(ctx, `UPDATE orders SET fee_settled=true WHERE order_id=$1`, oid); err != nil {
			return nil, err
		}
		return one(mutU(ctx, tx, uid, func(u *rules.User) { delete(u.Unpaid, oid); u.ArrearsTotal = i64("userArrearsTotal") }))
	case "bill.arrears.cleared":
		return one(mutU(ctx, tx, uid, func(u *rules.User) { u.ArrearsTotal = 0 }))
	// ---------- fleet ----------
	case "fleet.brand.registered", "fleet.brand.access_changed":
		id := str("brandId")
		v, err := mutateSnap(ctx, tx, kBrand, id, func(b *rules.Brand) {
			b.BrandID = id
			if op := str("operatorId"); op != "" {
				b.OperatorID = op
			}
			if env.Type == "fleet.brand.access_changed" {
				b.AccessStatus = str("to")
				return
			}
			b.Name, b.DisplayName, b.OperatorName, b.OperatorShortName = str("name"), str("displayName"), str("operatorName"), str("operatorShortName")
			b.VehicleType, b.Color = str("vehicleType"), str("color")
			if a := str("accessStatus"); a != "" {
				b.AccessStatus = a
			}
		})
		return one(snapChange{kBrand, id, v}, err)
	case "fleet.vehicle.listed":
		id := str("vehicleId")
		v, err := mutateSnap(ctx, tx, kVehicle, id, func(x *rules.Vehicle) {
			x.VehicleID, x.BrandID, x.OperatorID, x.PlateNo, x.GovCode, x.Model = id, str("brandId"), str("operatorId"), str("plateNo"), str("govCode"), str("model")
			x.CodeStatus, x.CodeEnableAt, x.Removed = str("codeStatus"), tm("codeEnableAt"), false
		})
		return one(snapChange{kVehicle, id, v}, err)
	case "fleet.vehicle.removed":
		id := str("vehicleId")
		v, err := mutateSnap(ctx, tx, kVehicle, id, func(x *rules.Vehicle) {
			x.VehicleID, x.Removed, x.CodeStatus, x.CodeInvalidAt = id, true, rules.CodeInvalid, tm("removedAt")
			if x.GovCode == "" {
				x.GovCode = str("govCode")
			}
		})
		return one(snapChange{kVehicle, id, v}, err)
	case "fleet.code.status_changed":
		id := str("vehicleId")
		v, err := mutateSnap(ctx, tx, kVehicle, id, func(x *rules.Vehicle) {
			x.VehicleID, x.CodeStatus = id, str("to")
			if x.BrandID == "" {
				x.BrandID = str("brandId")
			}
			if x.GovCode == "" {
				x.GovCode = str("govCode")
			}
			switch x.CodeStatus {
			case rules.CodeInvalid:
				x.CodeInvalidAt = tm("changedAt")
			case rules.CodeEnabled:
				x.CodeInvalidAt = nil
			}
		})
		return one(snapChange{kVehicle, id, v}, err)
	// ---------- site ----------
	case "site.station.changed":
		id := str("stationId")
		var fence []geo.Pt
		var center geo.Pt
		_ = json.Unmarshal(p["fence"], &fence)
		_ = json.Unmarshal(p["center"], &center)
		st := rules.Station{StationID: id, Name: str("name"), Type: str("type"), Center: center, Fence: fence, Capacity: int(i64("capacity")), Status: str("status")}
		return one(snapChange{kStation, id, st}, upsertSnap(ctx, tx, kStation, id, st))
	case "site.pile.status_changed":
		id := str("pileId")
		var loc geo.Pt
		_ = json.Unmarshal(p["location"], &loc)
		v, err := mutateSnap(ctx, tx, kPile, id, func(x *rules.Pile) {
			x.PileID, x.StationID, x.No, x.Location, x.Status = id, str("stationId"), int(i64("no")), loc, str("status")
		})
		return one(snapChange{kPile, id, v}, err)
	case "site.pile.occupancy":
		id, vid := str("pileId"), str("vehicleId")
		present := bl("present")
		v, err := mutateSnap(ctx, tx, kPile, id, func(x *rules.Pile) {
			x.PileID = id
			if x.StationID == "" {
				x.StationID = str("stationId")
			}
			if x.Status == "" {
				x.Status = "在线"
			}
			if present {
				x.OccupiedBy = vid
			} else if x.OccupiedBy == vid {
				x.OccupiedBy = ""
			}
		})
		return one(snapChange{kPile, id, v}, err)
	// ---------- rule ----------
	case "rule.version.published":
		id := str("versionId")
		var doc map[string]any
		_ = json.Unmarshal(env.Payload, &doc)
		return one(snapChange{kVersion, id, doc}, upsertSnap(ctx, tx, kVersion, id, doc))
	case "rule.version.revoked":
		id := str("versionId")
		v, err := mutateSnap(ctx, tx, kVersion, id, func(m *map[string]any) {
			if *m == nil {
				*m = map[string]any{"versionId": id}
			}
			(*m)["revoked"] = true
		})
		return one(snapChange{kVersion, id, v}, err)
	case "rule.params.changed":
		var doc map[string]any
		_ = json.Unmarshal(env.Payload, &doc)
		return one(snapChange{kParams, "params", doc}, upsertSnap(ctx, tx, kParams, "params", doc))
	// ---------- ops ----------
	case "ops.degrade.entered", "ops.degrade.tightened", "ops.degrade.stopped", "ops.degrade.relaxed", "ops.degrade.exited":
		v, err := mutateSnap(ctx, tx, kDegrade, "degrade", func(d *DegradeCtl) {
			switch env.Type {
			case "ops.degrade.entered":
				var comps []string
				_ = json.Unmarshal(p["components"], &comps)
				*d = DegradeCtl{Active: true, EventID: str("eventId"), Components: comps}
			case "ops.degrade.tightened":
				d.Tightened = true
			case "ops.degrade.stopped":
				d.Stopped = true
			case "ops.degrade.relaxed":
				d.Stopped = false
				d.Tightened = str("to") == "收紧"
			case "ops.degrade.exited":
				*d = DegradeCtl{}
			}
		})
		return one(snapChange{kDegrade, "degrade", v}, err)
	}
	return nil, nil
}

// ---------------- 事件总线 ----------------

// EnsureStream 确保本服务的流 EVT_RIDE 存在。
func EnsureStream(ctx context.Context, js jetstream.JetStream, retention time.Duration) error {
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "EVT_RIDE", Subjects: []string{"ride.>"}, Storage: jetstream.FileStorage, Retention: jetstream.LimitsPolicy,
		MaxAge: retention, Duplicates: 2 * time.Minute,
	})
	return err
}

// StartConsumers 为每个订阅的流建持久消费者 ride 并开始处理；流还不存在时后台重试。
func (s *Service) StartConsumers(ctx context.Context, js jetstream.JetStream) {
	for stream, subjects := range Subscriptions {
		go s.consumeLoop(ctx, js, stream, subjects)
	}
}

func (s *Service) consumeLoop(ctx context.Context, js jetstream.JetStream, stream string, subjects []string) {
	for {
		cons, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
			Durable: "ride", AckPolicy: jetstream.AckExplicitPolicy, DeliverPolicy: jetstream.DeliverAllPolicy,
			FilterSubjects: subjects, MaxAckPending: 1, AckWait: 30 * time.Second,
		})
		if err == nil {
			cc, err2 := cons.Consume(func(m jetstream.Msg) {
				var env Envelope
				if json.Unmarshal(m.Data(), &env) != nil {
					_ = m.Term()
					return
				}
				hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				herr := s.HandleEvent(hctx, env)
				cancel()
				if herr != nil {
					s.log.Warn("事件处理失败，等待重投", "type", env.Type, "eventId", env.EventID, "err", herr)
					_ = m.NakWithDelay(2 * time.Second)
					return
				}
				_ = m.Ack()
			})
			if err2 == nil {
				<-ctx.Done()
				cc.Stop()
				return
			}
			err = err2
		}
		s.log.Info("订阅暂不可用，稍后重试", "stream", stream, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// PublishOutbox 把发件表里到期未发的事件发到总线；总线不可用时留在发件表，下一轮再发。
func (s *Service) PublishOutbox(ctx context.Context, js jetstream.JetStream) (int, error) {
	tx, err := s.store.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id, event_id, subject, body FROM outbox WHERE published_at IS NULL AND not_before <= now()
		ORDER BY id LIMIT 200 FOR UPDATE SKIP LOCKED`)
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
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := js.Publish(pctx, it.subj, it.body, jetstream.WithMsgID(it.eid))
		cancel()
		if err != nil {
			break // 保序：前一条没发出去，后面的下一轮再发
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at=now() WHERE id=$1`, it.id); err != nil {
			return n, err
		}
		n++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if n < len(items) {
		return n, fmt.Errorf("事件总线暂不可用")
	}
	return n, nil
}

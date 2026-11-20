package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func jsConn(t *testing.T) jetstream.JetStream {
	t.Helper()
	nc, err := nats.Connect(testNats.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	return js
}

func TestOutboxPublishToBus(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	js := jsConn(t)
	_ = js.DeleteStream(ctx, "EVT_SITE")
	if err := EnsureStream(ctx, js, 30); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, h.admin("POST", "/api/site/admin/stations", createBody(nil), "station.edit"), 200)
	n, err := h.app.PublishPending(ctx, js)
	if err != nil || n != 2 {
		t.Fatalf("应发出 2 条（站点变更 + 留痕）：%d %v", n, err)
	}
	if left := countRows(t, `SELECT count(*) FROM outbox WHERE published_at IS NULL`); left != 0 {
		t.Fatalf("发出后应标记已发：%d", left)
	}
	if n, _ := h.app.PublishPending(ctx, js); n != 0 {
		t.Fatal("不应重复发布")
	}
	s, _ := js.Stream(ctx, "EVT_SITE")
	msg, err := s.GetMsg(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	_ = json.Unmarshal(msg.Data, &env)
	if msg.Subject != "site.station.changed" || env.Type != "site.station.changed" || msg.Header.Get("Nats-Msg-Id") != env.EventID || env.Version != 1 {
		t.Fatalf("总线上的消息错误：%s %v", msg.Subject, msg.Header)
	}
}

func TestPublisherKeepsEventsWhenBusDown(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// 总线不可用：接口照常成功，事件留在发件表
	mustStatus(t, h.admin("POST", "/api/site/admin/stations", createBody(nil), "station.edit"), 200)
	if n := countRows(t, `SELECT count(*) FROM outbox WHERE published_at IS NULL`); n != 2 {
		t.Fatalf("事件应留在发件表：%d", n)
	}
	// 发布循环接上总线后补发
	nc, err := nats.Connect(testNats.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go h.app.RunPublisher(cctx, nc)
	waitFor(t, "补发", func() bool { return countRows(t, `SELECT count(*) FROM outbox WHERE published_at IS NULL`) == 0 })
}

func TestConsumeFleetAndRideFromBus(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	ctx := context.Background()
	js := jsConn(t)
	// 替身：按发布方约定建流
	for name, subj := range map[string]string{"EVT_FLEET": "fleet.>", "EVT_RIDE": "ride.>"} {
		_ = js.DeleteStream(ctx, name)
		if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: name, Subjects: []string{subj}}); err != nil {
			t.Fatal(err)
		}
	}
	pub := func(subject string, body []byte) {
		var env Envelope
		_ = json.Unmarshal(body, &env)
		if _, err := js.Publish(ctx, subject, body, jetstream.WithMsgID(env.EventID)); err != nil {
			t.Fatal(err)
		}
	}
	pub("fleet.vehicle.listed", fleetVehicleListed("VH009999", "QH999999", "蓝牙", "BLE-0001"))
	pub("fleet.brand.registered", envelope("fleet.brand.registered", "BR01", map[string]any{"brandId": "BR01"})) // 未订阅，不应送达
	pub("ride.order.unlocked", rideUnlocked("OD2026120100001", "VH009999", "ST0002"))

	var ccs []jetstream.ConsumeContext
	for _, s := range subscriptions {
		cc, err := h.app.Consume(ctx, js, s.stream, s.filters)
		if err != nil {
			t.Fatal(err)
		}
		ccs = append(ccs, cc)
	}
	defer func() {
		for _, cc := range ccs {
			cc.Stop()
		}
	}()
	waitFor(t, "车辆标识投影", func() bool {
		return countRows(t, `SELECT count(*) FROM vehicle_tags WHERE vehicle_id='VH009999' AND tag_type='蓝牙'`) == 1
	})
	waitFor(t, "骑行投影", func() bool {
		return countRows(t, `SELECT count(*) FROM ride_orders WHERE order_id='OD2026120100001'`) == 1
	})
	if countRows(t, `SELECT count(*) FROM inbox WHERE type='fleet.brand.registered'`) != 0 {
		t.Fatal("未订阅的事件不应送达")
	}
	// 持久消费者名为 site
	if c, err := js.Consumer(ctx, "EVT_FLEET", "site"); err != nil || c.CachedInfo().Config.Durable != "site" {
		t.Fatalf("应建持久消费者 site：%v", err)
	}
	// 新识别的蓝牙标识能被桩识别
	p := freeSlot(t, "ST0003")
	h.device("occupancy", "dv3-secret", map[string]any{"msgId": "b-1", "deviceId": "DV0003", "ts": nowTS,
		"slots": []any{map[string]any{"slotNo": p.No, "present": true, "tag": "BLE-0001", "tagType": "蓝牙"}}})
	if ev := h.outboxEvents("site.pile.occupancy"); len(ev) != 1 || payload(ev[0])["vehicleId"] != "VH009999" {
		t.Fatalf("按识别方式 + 标识找车：%v", ev)
	}
	time.Sleep(10 * time.Millisecond)
}

func TestProjectionDedupeAndRules(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ev := fleetVehicleListed("VH000001", "QH000001", "RFID", "TAG-A")
	for i := 0; i < 3; i++ { // 至少送达一次：重复到达只处理一次
		if err := h.app.HandleEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if countRows(t, `SELECT count(*) FROM inbox`) != 1 || countRows(t, `SELECT count(*) FROM vehicle_tags`) != 1 {
		t.Fatal("按 eventId 去重")
	}
	// 同一标识换给另一辆车：以最新清单为准
	_ = h.app.HandleEvent(ctx, fleetVehicleListed("VH000002", "QH000002", "RFID", "TAG-A"))
	if countRows(t, `SELECT count(*) FROM vehicle_tags WHERE tag='TAG-A' AND vehicle_id='VH000002'`) != 1 || countRows(t, `SELECT count(*) FROM vehicle_tags`) != 1 {
		t.Fatal("标识应让给最新登记的车")
	}
	// 车辆移除
	_ = h.app.HandleEvent(ctx, rideEnded("OD1", "VH000002", nil, "ST0001", true))
	_ = h.app.HandleEvent(ctx, fleetVehicleRemoved("VH000002"))
	if countRows(t, `SELECT count(*) FROM vehicle_tags`) != 0 || countRows(t, `SELECT count(*) FROM parked_vehicles`) != 0 {
		t.Fatal("移除车辆应清投影")
	}
	// 乱序：先收到结束再收到开锁，不回退为骑行中
	_ = h.app.HandleEvent(ctx, rideEnded("OD2", "VH000003", "ST0001", nil, false))
	_ = h.app.HandleEvent(ctx, rideUnlocked("OD2", "VH000003", "ST0001"))
	if countRows(t, `SELECT count(*) FROM ride_orders WHERE status='骑行中'`) != 0 {
		t.Fatal("已结束的订单不应回退")
	}
	// 坏信封丢弃不报错（避免卡住队列）；载荷缺字段报错等待重投
	if err := h.app.HandleEvent(ctx, []byte(`not json`)); err != nil {
		t.Fatal("坏信封应丢弃")
	}
	if err := h.app.HandleEvent(ctx, envelope("fleet.vehicle.listed", "x", map[string]any{})); err == nil {
		t.Fatal("载荷缺车辆编号应报错")
	}
	if countRows(t, `SELECT count(*) FROM inbox WHERE type='fleet.vehicle.listed'`) != 2 {
		t.Fatal("处理失败的事件不应记入收件表")
	}
}

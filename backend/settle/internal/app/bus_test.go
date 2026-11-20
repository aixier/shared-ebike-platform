package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// 真事件总线：订阅方持久消费者建投影（按 eventId 去重），发件表补发到 EVT_SETTLE。
func TestBusConsumeAndPublish(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	a, clk := newApp(t, "2026-11-01T00:30:00+08:00")
	bus, err := ConnectBus(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.NC.Close()
	// 替身：发布方的流（真服务由各自模块创建）
	for stream := range Subscriptions {
		subj := map[string]string{"EVT_RIDE": "ride.>", "EVT_BILL": "bill.>", "EVT_RULE": "rule.>", "EVT_FLEET": "fleet.>"}[stream]
		if _, err := bus.JS.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: stream, Subjects: []string{subj}, Storage: jetstream.MemoryStorage}); err != nil {
			t.Fatal(err)
		}
	}
	pub := func(id, typ string, payload any) {
		p, _ := json.Marshal(payload)
		b, _ := json.Marshal(Envelope{EventID: id, Type: typ, Version: 1, OccurredAt: time.Now(), Payload: p})
		if _, err := bus.JS.Publish(ctx, typ, b, jetstream.WithMsgID(id)); err != nil {
			t.Fatal(err)
		}
	}
	pub("e1", "fleet.brand.registered", map[string]any{"brandId": "BR01", "operatorId": "OP01", "name": "青禾", "operatorShortName": "青禾出行"})
	pub("e2", "rule.version.published", map[string]any{"versionId": "PR0001", "effectiveAt": "2026-07-01T00:00:00+08:00", "ride": map[string]any{"startFee": 150, "startMinutes": 3, "perMinute": 50}, "dispatchFee": 500, "monthCard": map[string]any{"allocation": "按骑行单数"}, "serviceFee": map[string]any{"basis": "订单应收", "ratios": map[string]any{"BR01": 0.62}}})
	pub("e3", "ride.order.ended", ended("OD2026100200001", "BR01", "OP01", "PR0001", "2026-10-02T08:00:00+08:00", "2026-10-02T08:18:00+08:00", "2026-10-02T08:18:01+08:00", 18))
	pub("e4", "bill.bill.issued", issued("OD2026100200001", "BR01", "OP01", "PR0001", 18, 750, 0))
	pub("e5", "bill.bill.unrelated", map[string]any{}) // 未订阅的主题不会送达
	if err := testPool.QueryRow(ctx, `SELECT 1`).Scan(new(int)); err != nil {
		t.Fatal(err)
	}
	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()
	a.RunConsumers(cctx, bus)
	deadline := time.Now().Add(30 * time.Second)
	for {
		var n int
		_ = testPool.QueryRow(ctx, `SELECT count(*) FROM processed_events`).Scan(&n)
		if n >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("消费超时，已处理 %d", n)
		}
		time.Sleep(200 * time.Millisecond)
	}
	var hasBill, ended bool
	if err := testPool.QueryRow(ctx, `SELECT has_bill, ride_ended FROM orders WHERE order_id = 'OD2026100200001'`).Scan(&hasBill, &ended); err != nil || !hasBill || !ended {
		t.Fatalf("投影: %v %v %v", hasBill, ended, err)
	}
	// 出单后发件表补发到 EVT_SETTLE
	clk.Set("2026-11-01T00:30:00+08:00")
	if _, err := a.Generate(ctx, "2026-10", "自动出单", SystemActor); err != nil {
		t.Fatal(err)
	}
	if err := bus.EnsureStream(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	n, err := a.PublishPending(ctx, bus)
	if err != nil || n < 2 {
		t.Fatalf("补发 %d %v", n, err)
	}
	if n2, _ := a.PublishPending(ctx, bus); n2 != 0 {
		t.Errorf("已发的不应重发: %d", n2)
	}
	st, err := bus.JS.Stream(ctx, "EVT_SETTLE")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := st.Info(ctx)
	if info.State.Msgs < 2 {
		t.Errorf("EVT_SETTLE 消息数 %d", info.State.Msgs)
	}
	msg, err := st.GetLastMsgForSubject(ctx, "settle.settlement.generated")
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	_ = json.Unmarshal(msg.Data, &env)
	var p map[string]any
	_ = json.Unmarshal(env.Payload, &p)
	if env.Key != "ST-M-202610-OP01" || p["payableFee"].(float64) != 465 || p["trigger"] != "自动出单" || msg.Header.Get("Nats-Msg-Id") != env.EventID {
		t.Errorf("generated 事件: %+v %v", env, p)
	}
}

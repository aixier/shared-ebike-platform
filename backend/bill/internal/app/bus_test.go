package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// 进程内 nats-server（只监听 127.0.0.1，存储放 .tmp/）
func startNats(t *testing.T) jetstream.JetStream {
	t.Helper()
	dir, _ := filepath.Abs("../../.tmp")
	store, err := os.MkdirTemp(dir, "nats-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(store) })
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: store, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats 未就绪")
	}
	t.Cleanup(ns.Shutdown)
	nc, err := nats.Connect(ns.ClientURL())
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

func publishEnv(t *testing.T, js jetstream.JetStream, typ string, payload any) {
	t.Helper()
	p, _ := json.Marshal(payload)
	env := Envelope{EventID: newID(), Type: typ, Version: 1, OccurredAt: time.Now(), Key: "k", Actor: SystemActor, Payload: p}
	b, _ := json.Marshal(env)
	msg := &nats.Msg{Subject: typ, Data: b, Header: nats.Header{}}
	msg.Header.Set(nats.MsgIdHdr, env.EventID)
	if _, err := js.PublishMsg(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
}

// 走真事件总线：订阅 rule、deposit、ride 的流出账，发件表补发到 EVT_BILL
func TestBus_EndToEnd(t *testing.T) {
	e := newEnv(t)
	js := startNats(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, name := range []struct{ stream, subj string }{{"EVT_RULE", "rule.>"}, {"EVT_DEPOSIT", "deposit.>"}, {"EVT_RIDE", "ride.>"}} {
		if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: name.stream, Subjects: []string{name.subj}}); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, s := range Subscriptions {
		go Consume(ctx, js, testPool, s, e.a.HandleEvent, log)
	}
	publishEnv(t, js, "rule.version.published", map[string]any{"versionId": "PR0002", "effectiveAt": at("2026-10-15 00:00:00"),
		"ride": map[string]any{"startFee": 150, "startMinutes": 3, "perMinute": 50}, "dispatchFee": 500,
		"monthCard": map[string]any{"price": 2500, "days": 30, "perOrderCap": 300}, "coupons": []any{}})
	publishEnv(t, js, "deposit.status.changed", map[string]any{"userId": "U1", "mode": "押金", "status": "已缴", "balance": 19900})
	time.Sleep(300 * time.Millisecond)
	publishEnv(t, js, "ride.order.ended", map[string]any{"orderId": "OD1", "userId": "U1", "brandId": "BR01", "operatorId": "OP01", "channel": "支付宝",
		"pricingVersionId": "PR0002", "unlockResult": "成功", "startAt": e.clock.Add(-10 * time.Minute), "endAt": e.clock, "durationSec": 600, "inStation": true, "degraded": nil})

	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := getBill(ctx, testPool, `order_id='OD1'`); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("事件总线上的订单结束没有出账")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if b := e.bill("OD1"); b.Payable != 500 || b.CollectBy != byOneTap || b.Channel != "支付宝" {
		t.Fatalf("%+v", b)
	}

	// 发件表补发
	if err := EnsureStream(ctx, js, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	pub := &Publisher{Pool: testPool, JS: js, Log: log}
	n, err := pub.PublishPending(ctx)
	if err != nil || n < 1 {
		t.Fatalf("补发 %d 条：%v", n, err)
	}
	st, err := js.Stream(ctx, "EVT_BILL")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := st.Info(ctx)
	if info.Config.MaxAge != 30*24*time.Hour {
		t.Fatalf("事件流保留期应为配置的 30 天：%v", info.Config.MaxAge)
	}
	if info.State.Msgs != uint64(n) {
		t.Fatalf("流里 %d 条，补发 %d 条", info.State.Msgs, n)
	}
	m, err := st.GetLastMsgForSubject(ctx, "bill.bill.issued")
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	_ = json.Unmarshal(m.Data, &env)
	if env.Key != "OD1" || env.Version != 1 || m.Header.Get(nats.MsgIdHdr) != env.EventID {
		t.Fatalf("%+v", env)
	}
	if n2, _ := pub.PublishPending(ctx); n2 != 0 {
		t.Fatal("已发的不再发")
	}
}

// 总线不可用：对外接口不受影响，事件留在发件表
func TestBus_DownKeepsOutbox(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "押金", ptr("已缴"), ptr(int64(19900)))
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 5, inStation: true}))
	var n int
	e.must(testPool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n))
	if n == 0 {
		t.Fatal("事件应留在发件表")
	}
	if st, _, _ := e.call("GET", "/api/bill/me/orders/OD1", e.userToken("U1"), nil, nil); st != 200 {
		t.Fatal(st)
	}
}

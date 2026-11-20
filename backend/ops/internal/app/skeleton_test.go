package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestHealthzAndAuth(t *testing.T) {
	e := newEnv(t, "2026-12-01 12:00:00")
	if r := e.do("GET", "/healthz", "", nil, ""); r.status != 200 || r.body["ok"] != true {
		t.Fatalf("healthz %d %s", r.status, r.raw)
	}
	if r := e.do("GET", "/api/ops/degrade/status", "", nil, ""); r.status != 401 || r.code() != "ADMIN_NOT_LOGGED_IN" {
		t.Fatalf("未登录 %d %s", r.status, r.raw)
	}
	if r := e.do("GET", "/api/ops/degrade/status", "garbage", nil, ""); r.status != 401 {
		t.Fatalf("坏令牌 %d", r.status)
	}
	cs := e.token("AC0501", []string{"refund.confirm"}, nil, false)
	if r := e.do("GET", "/api/ops/degrade/status", cs, nil, ""); r.status != 403 || r.code() != "FORBIDDEN" {
		t.Fatalf("无权限 %d %s", r.status, r.raw)
	}
	ro := e.token("AC0402", []string{"degrade.control"}, nil, true)
	if r := e.do("POST", "/api/ops/degrade/enter", ro, map[string]any{"preset": "支付通道故障"}, ""); r.status != 403 || r.code() != "READONLY" {
		t.Fatalf("只读账号写操作 %d %s", r.status, r.raw)
	}
	if r := e.do("GET", "/api/ops/monitor?hours=0", e.duty(), nil, ""); r.status != 400 || r.code() != "INVALID" {
		t.Fatalf("参数越界 %d %s", r.status, r.raw)
	}
}

func TestIdempotencyReplay(t *testing.T) {
	e := newEnv(t, "2026-12-01 12:00:00")
	e.seedRules()
	r1 := e.do("POST", "/api/ops/degrade/enter", e.duty(), map[string]any{"preset": "支付通道故障", "reason": "微信回调持续超时"}, "k-1")
	if r1.status != 200 {
		t.Fatalf("进入 %d %s", r1.status, r1.raw)
	}
	r2 := e.do("POST", "/api/ops/degrade/exit", e.duty(), nil, "k-2")
	if r2.status != 200 {
		t.Fatalf("退出 %d %s", r2.status, r2.raw)
	}
	// 同一键重放：返回第一次的结果（进行中），不再新建事件
	r3 := e.do("POST", "/api/ops/degrade/enter", e.duty(), map[string]any{"preset": "支付通道故障"}, "k-1")
	if string(r3.raw) != string(r1.raw) || r3.body["status"] != "进行中" {
		t.Fatalf("幂等重放应返回首次结果：%s", r3.raw)
	}
	var n int
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FROM degrade_events`).Scan(&n)
	if n != 1 {
		t.Fatalf("幂等键重复到达不应重复执行，事件数 %d", n)
	}
	// 不同登录人用同一个键互不影响
	other := e.token("AC0402", []string{"degrade.control"}, nil, false)
	if r := e.do("POST", "/api/ops/degrade/enter", other, map[string]any{"preset": "授权不可用"}, "k-1"); r.status != 200 || r.body["eventId"] == r1.body["eventId"] {
		t.Fatalf("另一人同键应正常执行：%s", r.raw)
	}
}

func TestInboxDedup(t *testing.T) {
	e := newEnv(t, "2026-12-01 12:00:00")
	p, _ := json.Marshal(map[string]any{"brandId": "BR09", "operatorId": "OP09", "name": "九号", "accessStatus": "已接入"})
	env := Envelope{EventID: "dup-1", Type: "fleet.brand.registered", Version: 1, Payload: p}
	for i := 0; i < 3; i++ {
		if err := e.a.HandleEnvelope(e.ctx(), env); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FROM inbox WHERE event_id = 'dup-1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("按事件编号去重，得 %d", n)
	}
	// 同一 eventId 的重复依赖熔断只告一次警
	dep, _ := json.Marshal(map[string]any{"component": "支付", "state": "熔断", "at": e.nowTS(), "instance": "ride-1"})
	d := Envelope{EventID: "dup-2", Type: "ride.dependency.state_changed", Version: 1, Payload: dep}
	_ = e.a.HandleEnvelope(e.ctx(), d)
	_ = e.a.HandleEnvelope(e.ctx(), d)
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FROM alerts`).Scan(&n)
	if n != 1 {
		t.Fatalf("重复事件不重复告警，得 %d", n)
	}
}

// 进程内 nats-server：发件表发布到 EVT_OPS，消费者从 EVT_RIDE 取事件并处理。
func TestBusPublishAndConsume(t *testing.T) {
	e := newEnv(t, "2026-12-01 12:00:00")
	dir, _ := filepath.Abs(filepath.Join("..", "..", ".tmp", "nats-"+time.Now().Format("150405.000")))
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ns, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats 未就绪")
	}
	defer ns.Shutdown()
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 发布：总线可用前写入发件表的事件，恢复后补发
	e.seedRules()
	if r := e.do("POST", "/api/ops/degrade/enter", e.duty(), map[string]any{"preset": "实名核验不可用"}, ""); r.status != 200 {
		t.Fatalf("%s", r.raw)
	}
	if err := EnsureStream(ctx, js, time.Hour); err != nil {
		t.Fatal(err)
	}
	n, err := e.a.PublishPending(ctx, js)
	if err != nil || n == 0 {
		t.Fatalf("补发 %d %v", n, err)
	}
	if n2, _ := e.a.PublishPending(ctx, js); n2 != 0 {
		t.Fatal("已发的不再发")
	}
	cons, err := js.OrderedConsumer(ctx, "EVT_OPS", jetstream.OrderedConsumerConfig{FilterSubjects: []string{"ops.degrade.entered"}})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	_ = json.Unmarshal(msg.Data(), &env)
	if env.Type != "ops.degrade.entered" || env.Key != "degrade" || msg.Headers().Get("Nats-Msg-Id") != env.EventID {
		t.Fatalf("信封 %+v 头 %v", env, msg.Headers())
	}
	var payload map[string]any
	_ = json.Unmarshal(env.Payload, &payload)
	if payload["limitsVersionId"] != "PR0003" {
		t.Fatalf("载荷 %v", payload)
	}

	// 消费：ride 发布方的流里放一条已开锁事件，ops 的持久消费者取到并写投影
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "EVT_RIDE", Subjects: []string{"ride.>"}}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(Envelope{EventID: "ride-evt-1", Type: "ride.order.created", Version: 1, Key: "OD1",
		Payload: mustJSON(e.head("OD2026120100001", "U100001", "BR01", "2026-12-01 11:59:00", nil))})
	if _, err := js.Publish(ctx, "ride.order.created", body, jetstream.WithMsgID("ride-evt-1")); err != nil {
		t.Fatal(err)
	}
	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()
	e.a.RunConsumers(cctx, js)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var c int
		_ = testPool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE order_id = 'OD2026120100001'`).Scan(&c)
		if c == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("消费者没有处理事件")
		}
		time.Sleep(100 * time.Millisecond)
	}
	s, _ := js.Stream(ctx, "EVT_RIDE")
	c, err := s.Consumer(ctx, "ops")
	if err != nil {
		t.Fatalf("持久消费者名应为 ops：%v", err)
	}
	if c.CachedInfo().Config.MaxAckPending != 1 || c.CachedInfo().Config.AckPolicy != jetstream.AckExplicitPolicy {
		t.Fatal("一次只处理一条、显式确认")
	}
}

func TestCronLock(t *testing.T) {
	e := newEnv(t, "2026-12-01 12:00:00")
	conn, _ := testPool.Acquire(e.ctx())
	defer conn.Release()
	_, _ = conn.Exec(e.ctx(), `SELECT pg_advisory_lock($1)`, lockMetrics)
	ran, err := e.a.withLock(e.ctx(), lockMetrics, func(context.Context) error { return nil })
	if err != nil || ran {
		t.Fatal("别的实例持锁时跳过")
	}
	_, _ = conn.Exec(e.ctx(), `SELECT pg_advisory_unlock($1)`, lockMetrics)
	ran, err = e.a.withLock(e.ctx(), lockMetrics, func(context.Context) error { return nil })
	if err != nil || !ran {
		t.Fatal("拿到锁时执行")
	}
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func TestReadonlyRejectsEveryWrite(t *testing.T) {
	e := newEnv(t, "2026-12-01 08:00:00")
	e.seedRules()
	e.seedBrands()
	all := []string{"ops.monitor", "degrade.control", "degrade.closure.view", "degrade.closure.handle", "access.pause"}
	ro := e.token("AC0402", all, nil, true)
	body := map[string]any{"reason": "只读账号不得写入", "on": true, "action": "确认收款结果"}
	for _, c := range [][2]string{
		{"POST", "/api/ops/degrade/enter"}, {"POST", "/api/ops/degrade/exit"}, {"POST", "/api/ops/degrade/tighten"},
		{"POST", "/api/ops/degrade/stop"}, {"POST", "/api/ops/degrade/relax"}, {"POST", "/api/ops/closures/OD1/handle"},
		{"POST", "/api/ops/alerts/OPS-AL-X/ack"}, {"POST", "/api/ops/suspend-suggestions/X/confirm"},
		{"POST", "/api/ops/suspend-suggestions/X/dismiss"}, {"PUT", "/api/ops/auto-suspend"},
		{"POST", "/api/ops/brands/BR01/pause"}, {"POST", "/api/ops/brands/BR01/resume"},
	} {
		if r := e.do(c[0], c[1], ro, body, ""); r.status != 403 || r.code() != "READONLY" {
			t.Fatalf("%s %s 应 403 READONLY：%d %s", c[0], c[1], r.status, r.raw)
		}
	}
	if r := e.do("GET", "/api/ops/alerts", ro, nil, ""); r.status != 200 {
		t.Fatalf("只读账号仍可查看 %s", r.raw)
	}
}

func TestExitedOperatorScopeReadonly(t *testing.T) {
	e := newEnv(t, "2026-12-01 08:00:00")
	e.seedRules()
	e.seedBrands()
	op := e.token("AC0302", []string{"access.pause", "access.view.own"}, "OP02", false)
	if r := e.do("POST", "/api/ops/brands/BR02/pause", op, map[string]any{"reason": "运营商自检暂停"}, ""); r.code() == "READONLY" {
		t.Fatalf("未退出时不拒绝 %s", r.raw)
	}
	e.publish("fleet.operator.exited", map[string]any{"operatorId": "OP02", "brandIds": []string{"BR02"}, "exitedAt": e.nowTS(), "readonlyUntil": e.ts("2027-03-01 00:00:00")})
	if r := e.do("POST", "/api/ops/brands/BR02/resume", op, map[string]any{"reason": "运营商自检恢复"}, ""); r.status != 403 || r.code() != "READONLY" {
		t.Fatalf("运营商退出后写操作拒绝 %s", r.raw)
	}
	if r := e.do("GET", "/api/ops/pauses", op, nil, ""); r.status != 200 {
		t.Fatalf("退出后仍可只读 %s", r.raw)
	}
}

func TestNonUTF8BodyRejected(t *testing.T) {
	e := newEnv(t, "2026-12-01 08:00:00")
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/ops/degrade/enter", strings.NewReader("{\"cause\":\"\xd6\xd0\xce\xc4\",\"components\":[\"计费\"]}"))
	req.Header.Set("Authorization", "Bearer "+e.duty())
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var b map[string]any
	_ = json.NewDecoder(res.Body).Decode(&b)
	if res.StatusCode != 400 || b["code"] != "INVALID" || !strings.Contains(b["message"].(string), "UTF-8") {
		t.Fatalf("%d %v", res.StatusCode, b)
	}
}

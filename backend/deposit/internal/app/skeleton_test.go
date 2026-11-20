package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// trace: 任务 4 工程骨架 · /healthz
func TestHealthz(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/healthz", nil, nil)
	mustStatus(t, r, 200)
	if r.body["ok"] != true {
		t.Fatalf("应返回 ok:true：%s", r.raw)
	}
}

// trace: wire.md 第 2 节 令牌校验
func TestAuthRequired(t *testing.T) {
	e := newEnv(t)
	mustCode(t, e.do("GET", "/api/deposit/me/state", nil, nil), 401, "NOT_LOGGED_IN")
	mustCode(t, e.do("GET", "/api/deposit/me/state", map[string]string{"Authorization": "Bearer bad"}, nil), 401, "NOT_LOGGED_IN")
	mustCode(t, e.do("GET", "/api/deposit/admin/summary", nil, nil), 401, "ADMIN_NOT_LOGGED_IN")
	// 用户令牌不能当管理令牌用
	mustCode(t, e.do("GET", "/api/deposit/admin/summary", map[string]string{"Authorization": "Bearer " + userToken("U1")}, nil), 401, "ADMIN_NOT_LOGGED_IN")
	mustCode(t, e.do("GET", "/api/deposit/admin/summary", map[string]string{"Authorization": "Bearer " + adminToken("AC0301", nil, "user.view")}, nil), 403, "FORBIDDEN")
	mustCode(t, e.do("GET", "/api/deposit/admin/summary", map[string]string{"Authorization": "Bearer " + adminToken("AC0101", "OP01", "deposit.view")}, nil), 403, "SCOPE_DENIED")
}

// trace: wire.md 第 3 节 服务凭证校验；deductions 只允许 bill，assist-refunds 只允许 cs
func TestInternalAuth(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"userId": "U1", "orderId": "OD1", "amount": 100}
	if r := e.internal("/internal/deposit/deductions", "bill", "wrong", "k", body); r.status != 401 {
		t.Fatalf("凭证错应 401：%s", r.raw)
	}
	if r := e.internal("/internal/deposit/deductions", "cs", "cs-key", "k", body); r.status != 403 {
		t.Fatalf("cs 不能调抵扣应 403：%s", r.raw)
	}
	mustCode(t, e.internal("/internal/deposit/deductions", "bill", "bill-key", "", body), 400, "INVALID")
}

// trace: wire.md 第 4 节 订阅方按 eventId 去重
func TestEventDedup(t *testing.T) {
	e := newEnv(t)
	p := map[string]any{"orderId": "OD1", "userId": "U1", "vehicleId": "VH1", "plateNo": "1", "brandId": "BR01", "operatorId": "OP01", "channel": "微信", "pricingVersionId": "PR0002", "requestAt": e.now.Format(time.RFC3339), "degraded": nil}
	e.eventWithID("E1", "ride.order.created", "OD1", p)
	e.eventWithID("E2", "ride.order.ended", "OD1", p)
	e.eventWithID("E1", "ride.order.created", "OD1", p) // 重投
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM proj_riding`).Scan(&n)
	if n != 0 {
		t.Fatal("重复的 created 不应再次生效")
	}
	// 不认识的事件类型直接确认
	e.event("ride.order.over_limit_reminded", "OD1", map[string]any{})
}

// trace: 任务 4 发件表与事件发布；事件消费（经 NATS JetStream）
func TestOutboxPublishAndConsume(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	if abs, err := filepath.Abs(filepath.Join("..", "..", ".tmp", "nats")); err == nil {
		dir = filepath.Join(abs, t.Name())
	}
	_ = os.RemoveAll(dir) // 上次运行留下的流与消费者状态会让去重吞掉本次消息
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true})
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
	js, _ := jetstream.New(nc)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := EnsureStream(ctx, js, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	// 他方的流（模拟 rule 发布）
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "EVT_RULE", Subjects: []string{"rule.>"}}); err != nil {
		t.Fatal(err)
	}
	go e.app.RunConsumers(ctx, js, 200*time.Millisecond)

	ruleEvt, _ := json.Marshal(map[string]any{"eventId": "R1", "type": "rule.version.published", "version": 1, "occurredAt": e.now.Format(time.RFC3339), "key": "pricing",
		"actor": map[string]any{"kind": "人", "id": "AC0001"}, "payload": map[string]any{"versionId": "PR0002", "effectiveAt": "2026-10-15T00:00:00+08:00",
			"deposit": map[string]any{"amount": 19900}, "creditFree": map[string]any{"threshold": 550, "providers": []string{"微信支付分"}}}})
	if _, err := js.Publish(ctx, "rule.version.published", ruleEvt, jetstream.WithMsgID("R1")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		_ = testPool.QueryRow(ctx, `SELECT count(*) FROM proj_rule_versions`).Scan(&n)
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("消费者没有处理 rule 事件")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 产生一条本模块事件并发布
	e.verified("U100087")
	mustStatus(t, e.user("POST", "/api/deposit/me/credit-free", "U100087", "c1", nil), 200)
	n, err := e.app.PublishPending(ctx, js)
	if err != nil || n == 0 {
		t.Fatalf("发布发件表失败：%d %v", n, err)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, "EVT_DEPOSIT", jetstream.ConsumerConfig{Durable: "probe", FilterSubject: "deposit.status.changed"})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for m := range batch.Messages() {
		got++
		if m.Headers().Get("Nats-Msg-Id") == "" {
			t.Fatal("消息头要带 Nats-Msg-Id")
		}
		var env map[string]any
		_ = json.Unmarshal(m.Data(), &env)
		if env["key"] != "U100087" || env["payload"].(map[string]any)["mode"] != "免押" {
			t.Fatalf("信封不对：%s", m.Data())
		}
	}
	if got != 1 {
		t.Fatal("应收到 1 条 deposit.status.changed")
	}
	if n2, _ := e.app.PublishPending(ctx, js); n2 != 0 {
		t.Fatal("已发布的不应重发")
	}
}

// trace: wire.md 第 1 节 幂等：同一个键重复到达返回第一次的结果
func TestIdempotentReplay(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U100004")
	r1 := e.user("POST", "/api/deposit/me/payment", "U100004", "pay-1", nil)
	mustStatus(t, r1, 200)
	r2 := e.user("POST", "/api/deposit/me/payment", "U100004", "pay-1", nil)
	mustStatus(t, r2, 200)
	if r1.body["paymentId"] != r2.body["paymentId"] {
		t.Fatal("同一幂等键应返回同一结果")
	}
	if len(e.pay.Calls()) != 1 {
		t.Fatalf("pay 只应被调用一次，实际 %d", len(e.pay.Calls()))
	}
	// 换个键：已经缴过
	mustCode(t, e.user("POST", "/api/deposit/me/payment", "U100004", "pay-2", nil), 409, "ALREADY_DONE")
}

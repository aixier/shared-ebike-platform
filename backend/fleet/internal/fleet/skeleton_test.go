package fleet

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestHealthz(t *testing.T) {
	r := call(t, "GET", "/healthz", "", nil, nil)
	if r.status != 200 || r.body["ok"] != true {
		t.Fatalf("healthz: %d %s", r.status, r.raw)
	}
}

func TestAdminAuth(t *testing.T) {
	reset(t)
	wantCode(t, call(t, "GET", "/api/fleet/operators", "", nil, nil), 401, "ADMIN_NOT_LOGGED_IN")
	wantCode(t, call(t, "GET", "/api/fleet/operators", "garbage", nil, nil), 401, "ADMIN_NOT_LOGGED_IN")
	// 无权限点
	noPerm := token(t, tokOpt{perms: []string{"order.view"}})
	wantCode(t, call(t, "GET", "/api/fleet/access/overview", noPerm, nil, nil), 403, "FORBIDDEN")
	// 只读账号写操作
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/VH001061/remove", op3Tok(t), map[string]any{"reason": "测试移除车辆"}, nil), 403, "READONLY")
}

func TestInternalAuth(t *testing.T) {
	reset(t)
	body := map[string]any{"actor": map[string]any{"kind": "人", "id": "AC0403"}}
	// 凭证不对
	r := call(t, "POST", "/internal/fleet/brands/BR06/start-integration", "wrong", body, map[string]string{"X-Caller": "onboard", "Idempotency-Key": "a1"})
	if r.status != 401 {
		t.Fatalf("错误凭证应 401，得到 %d", r.status)
	}
	// 缺幂等键
	wantCode(t, internalCall(t, "onboard", "/internal/fleet/brands/BR06/start-integration", "", body), 400, "INVALID")
	// 正常
	r = internalCall(t, "onboard", "/internal/fleet/brands/BR06/start-integration", "a1", body)
	if r.status != 200 {
		t.Fatalf("应成功: %d %s", r.status, r.raw)
	}
}

func TestIdempotency(t *testing.T) {
	reset(t)
	body := map[string]any{"actor": map[string]any{"kind": "智能体", "id": "AC0901"}, "metric": "人工", "reason": "人工暂停可租测试"}
	r1 := internalCall(t, "ops", "/internal/fleet/brands/BR01/pause", "same-key", body)
	if r1.status != 200 {
		t.Fatalf("首次暂停应成功: %d %s", r1.status, r1.raw)
	}
	r2 := internalCall(t, "ops", "/internal/fleet/brands/BR01/pause", "same-key", body)
	if r2.status != 200 || string(r2.raw) != string(r1.raw) {
		t.Fatalf("同一个键应返回第一次的结果: %d %s", r2.status, r2.raw)
	}
	// 只记了一条暂停与一条状态变更事件
	if n := countType(outboxTypes(t), "fleet.brand.access_changed"); n != 1 {
		t.Fatalf("重复请求不应重复执行，状态变更事件 %d 条", n)
	}
	// 换个键就按真实状态判断
	wantCode(t, internalCall(t, "ops", "/internal/fleet/brands/BR01/pause", "other-key", body), 409, "ALREADY_DONE")
}

func TestEnvelopeAndRelay(t *testing.T) {
	reset(t)
	body := map[string]any{"actor": map[string]any{"kind": "人", "id": "AC0403", "name": "毛＊＊"}}
	if r := internalCall(t, "onboard", "/internal/fleet/brands/BR06/start-integration", "k-relay", body); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	evs := outboxEvents(t, "fleet.brand.access_changed")
	if len(evs) != 1 {
		t.Fatalf("应有 1 条状态变更事件，得到 %d", len(evs))
	}
	e := evs[0]
	for _, k := range []string{"eventId", "type", "version", "occurredAt", "key", "actor", "payload"} {
		if _, ok := e[k]; !ok {
			t.Fatalf("信封缺 %s: %v", k, e)
		}
	}
	if e["key"] != "BR06" || e["version"].(float64) != 1 {
		t.Fatalf("信封不对: %v", e)
	}

	ctx := context.Background()
	nc, err := nats.Connect(testNATS.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	relay := NewRelay(testPool, js, 24*time.Hour)

	n, err := relay.PublishPending(ctx)
	if err != nil || n < 2 { // 状态变更 + 留痕
		t.Fatalf("应发出事件: n=%d err=%v", n, err)
	}
	var left int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&left)
	if left != 0 {
		t.Fatalf("发出后应标记已发，剩 %d", left)
	}
	// 总线上能按主题取到，消息编号是 eventId
	stream, err := js.Stream(ctx, "EVT_FLEET")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := stream.GetLastMsgForSubject(ctx, "fleet.brand.access_changed")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Nats-Msg-Id") != e["eventId"] {
		t.Fatalf("消息编号应为 eventId")
	}
	var got map[string]any
	_ = json.Unmarshal(msg.Data, &got)
	p := got["payload"].(map[string]any)
	if p["from"] != "未接入" || p["to"] != "联调中" || p["brandId"] != "BR06" || p["operatorId"] != "OP05" {
		t.Fatalf("载荷不对: %v", p)
	}
	// 再跑一次没有新事件
	if n, _ := relay.PublishPending(ctx); n != 0 {
		t.Fatalf("不应重复发布，发了 %d", n)
	}
}

func TestRelayBusDown(t *testing.T) {
	// 总线不可用时事件留在发件表，接口不受影响
	reset(t)
	body := map[string]any{"actor": map[string]any{"kind": "人", "id": "AC0403"}}
	if r := internalCall(t, "onboard", "/internal/fleet/brands/BR06/start-integration", "k-down", body); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	nc, err := nats.Connect("nats://127.0.0.1:1", nats.Timeout(200*time.Millisecond))
	if err == nil {
		nc.Close()
		t.Skip("意外连上")
	}
	relay := NewRelay(testPool, nil, time.Hour)
	if _, err := relay.PublishPending(context.Background()); err == nil {
		t.Fatal("无总线时应返回错误")
	}
	var left int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&left)
	if left == 0 {
		t.Fatal("事件应留在发件表")
	}
}

func TestSeedLoaded(t *testing.T) {
	reset(t)
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM brands`).Scan(&n)
	if n != 6 {
		t.Fatalf("应有 6 个品牌，得到 %d", n)
	}
	// 再装一次不重复（登记只新增）
	data := mustReadSeed(t)
	added, err := testSvc.LoadSeed(context.Background(), data)
	if err != nil || added != 0 {
		t.Fatalf("重复装入不应新增: %d %v", added, err)
	}
}

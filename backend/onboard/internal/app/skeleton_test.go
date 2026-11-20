package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// trace: 任务 4 工程骨架

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	st, m, _ := e.call(who{}, "GET", "/healthz", nil)
	if st != 200 || m["ok"] != true {
		t.Fatalf("healthz: %d %v", st, m)
	}
}

func TestAdminAuth(t *testing.T) {
	e := newEnv(t)
	if st, m, _ := e.call(who{}, "GET", "/api/onboard/spec", nil); st != 401 || m["code"] != "ADMIN_NOT_LOGGED_IN" {
		t.Fatalf("未登录: %d %v", st, m)
	}
	if st, m, _ := e.call(dutyOps, "GET", "/api/onboard/spec", nil); st != 403 || m["code"] != "FORBIDDEN" {
		t.Fatalf("无权限点: %d %v", st, m)
	}
	if st, m, _ := e.call(exited, "PUT", "/api/onboard/sandbox/payment-mode", map[string]any{"mode": "余额不足"}); st != 403 || m["code"] != "READONLY" {
		t.Fatalf("只读账号写操作: %d %v", st, m)
	}
}

func TestInternalCallerCheck(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest("POST", e.srv.URL+"/internal/onboard/x", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Fatalf("没有服务凭证应 401: %d", resp.StatusCode)
	}
	req.Header.Set("Authorization", "Bearer ops-secret")
	req.Header.Set("X-Caller", "ops")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 404 {
		t.Fatalf("凭证对但本模块没有内部接口应 404: %d", resp.StatusCode)
	}
}

// trace: wire.md 幂等——同一个键重复到达返回第一次的结果，不重复执行
func TestIdempotency(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	_, first, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/rotate", map[string]any{"reason": "首次领取联调密钥"}, "Idempotency-Key", "rot-1")
	_, again, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/rotate", map[string]any{"reason": "首次领取联调密钥"}, "Idempotency-Key", "rot-1")
	if first["secret"] == nil || first["secret"] != again["secret"] {
		t.Fatalf("重复的幂等键应返回第一次的结果: %v / %v", first["secret"], again["secret"])
	}
	_, third, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/rotate", map[string]any{"reason": "再次轮换联调密钥"}, "Idempotency-Key", "rot-2")
	if third["secret"] == first["secret"] {
		t.Fatal("新的幂等键应再执行一次")
	}
}

// trace: 发件表与事件发布、事件消费与去重
func TestOutboxPublishAndConsume(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	js, err := jetstream.New(natsConn(t))
	if err != nil {
		t.Fatal(err)
	}
	e.app.JS = js
	if err := e.app.EnsureStream(ctx); err != nil {
		t.Fatal(err)
	}
	// 发布：发件表里的事件发到 EVT_ONBOARD
	e.registerBR05()
	e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/revoke", map[string]any{"reason": "测试吊销凭证"})
	n, err := e.app.PublishPending(ctx)
	if err != nil || n == 0 {
		t.Fatalf("应发出事件: %d %v", n, err)
	}
	if again, _ := e.app.PublishPending(ctx); again != 0 {
		t.Fatal("已发的不应再发")
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, "EVT_ONBOARD", jetstream.ConsumerConfig{FilterSubject: "onboard.audit.recorded", DeliverPolicy: jetstream.DeliverLastPolicy})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	_ = json.Unmarshal(msg.Data(), &env)
	if env.Type != "onboard.audit.recorded" || msg.Headers().Get("Nats-Msg-Id") != env.EventID {
		t.Fatalf("信封或消息编号不对: %+v", env)
	}

	// 订阅：fleet 发的事件经总线进投影，重复投递只处理一次
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: "EVT_FLEET", Subjects: []string{"fleet.>"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: "EVT_RULE", Subjects: []string{"rule.>"}}); err != nil {
		t.Fatal(err)
	}
	cctx, stop := context.WithCancel(ctx)
	defer stop()
	e.app.StartConsumers(cctx)
	p, _ := json.Marshal(map[string]any{"brandId": "BR06", "operatorId": "OP05", "name": "测试品牌", "accessStatus": "未接入"})
	body, _ := json.Marshal(Envelope{EventID: "EVT-fleet-dup-1", Type: "fleet.brand.registered", Version: 1, OccurredAt: time.Now(), Key: "BR06", Actor: acAccess, Payload: p})
	for i := 0; i < 2; i++ {
		if _, err := js.Publish(ctx, "fleet.brand.registered", body); err != nil {
			t.Fatal(err)
		}
	}
	rp, _ := json.Marshal(map[string]any{"acceptance": map[string]any{"realVehicles": 5, "realOrders": 10, "targetDays": 14}, "unlockReportTimeoutSec": 30})
	rb, _ := json.Marshal(Envelope{EventID: "EVT-rule-1", Type: "rule.params.changed", Version: 1, OccurredAt: time.Now(), Key: "params", Actor: Actor{Kind: "人", ID: "AC0404"}, Payload: rp})
	if _, err := js.Publish(ctx, "rule.params.changed", rb); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n, hist, rv int
		_ = testPool.QueryRow(ctx, `SELECT count(*) FROM integrations WHERE brand_id='BR06'`).Scan(&n)
		_ = testPool.QueryRow(ctx, `SELECT count(*) FROM status_history WHERE integration_id='IN-BR06'`).Scan(&hist)
		_ = testPool.QueryRow(ctx, `SELECT real_vehicles FROM params`).Scan(&rv)
		if n == 1 && rv == 5 {
			if hist != 1 {
				t.Fatalf("重复投递应只处理一次，状态记录 %d 条", hist)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("事件未进投影: 接入记录 %d 条，参数 %d", n, rv)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

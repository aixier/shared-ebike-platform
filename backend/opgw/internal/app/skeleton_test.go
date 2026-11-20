package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// 任务 4：工程骨架

func TestHealthz(t *testing.T) {
	e := newEnv(t, nil)
	req, _ := http.NewRequest("GET", e.srv.URL+"/healthz", nil)
	st, m := do(t, req)
	if st != 200 || m["ok"] != true {
		t.Fatalf("%d %v", st, m)
	}
}

func TestLoadConfig(t *testing.T) {
	envs := map[string]string{
		"DB_DSN":                     "postgres://x",
		"OPGW_SECRET_KEY":            b64(make([]byte, 32)),
		"ALLOWED_CALLERS":            "ride=abc, ops=def",
		"ADMIN_JWT_PUBKEYS":          "k1=" + b64(adminPub),
		"OPGW_PROVIDERS":             "SP01=OP02|OP06",
		"OPGW_IP_WHITELISTS":         "OP01=1.2.3.4|10.0.0.0/8",
		"OPGW_TRUSTED_PROXIES":       "127.0.0.1",
		"OPGW_OPERATOR_TIMEOUT_MS":   "2500",
		"OPGW_UNREACHABLE_THRESHOLD": "5",
	}
	c, err := LoadConfig(func(k string) string { return envs[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenHost != "127.0.0.1" || c.Port != 18303 {
		t.Fatalf("默认只监听本机 18303：%s:%d", c.ListenHost, c.Port)
	}
	if c.AllowedCallers["ops"] != "def" || c.ProviderOf("OP06") != "SP01" || c.ProviderOf("OP01") != "" {
		t.Fatal("解析键值不对")
	}
	if len(c.IPWhitelists["OP01"]) != 2 || c.OperatorTimeout != 2500*time.Millisecond || c.UnreachableAfter != 5 || len(c.TrustedProxies) != 1 {
		t.Fatalf("%+v", c)
	}
	if _, err := LoadConfig(func(k string) string { return "" }); err == nil {
		t.Fatal("缺 DB_DSN 应报错")
	}
	envs["OPGW_SECRET_KEY"] = "short"
	if _, err := LoadConfig(func(k string) string { return envs[k] }); err == nil {
		t.Fatal("主密钥长度不对应报错")
	}
}

func TestMigrateIdempotent(t *testing.T) {
	if err := Migrate(context.Background(), testPool); err != nil {
		t.Fatal("重复执行迁移应无事", err)
	}
}

func TestInternalAuth(t *testing.T) {
	e := newEnv(t, nil)
	body := map[string]any{"orderId": "OD1", "operatorId": "OP01", "brandId": "BR01", "plateNo": "QH000004", "degraded": false}
	for _, c := range []struct{ caller, key string }{{"ride", "wrong"}, {"ops", "ride-key"}, {"", ""}} {
		req, _ := http.NewRequest("POST", e.srv.URL+"/internal/opgw/unlock", jsonReader(body))
		req.Header.Set("X-Caller", c.caller)
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Idempotency-Key", "k")
		if st, _ := do(t, req); st != 401 {
			t.Fatalf("%v 应 401，得 %d", c, st)
		}
	}
}

func TestAdminAuth(t *testing.T) {
	e := newEnv(t, nil)
	if st, m := e.admin("GET", "/api/opgw/credentials", "", "", nil); st != 401 || m["code"] != "ADMIN_NOT_LOGGED_IN" {
		t.Fatalf("%d %v", st, m)
	}
	if st, _ := e.admin("GET", "/api/opgw/credentials", adminToken(e, tokenOpt{exp: -time.Minute}), "", nil); st != 401 {
		t.Fatal("过期令牌应 401")
	}
	if st, m := e.admin("GET", "/api/opgw/credentials", adminToken(e, tokenOpt{perms: []string{"order.view"}}), "", nil); st != 403 || m["code"] != "FORBIDDEN" {
		t.Fatalf("无权限点应 403：%d %v", st, m)
	}
	if st, _ := e.admin("GET", "/api/opgw/credentials", adminToken(e, tokenOpt{}), "", nil); st != 200 {
		t.Fatal("有权限应 200")
	}
	if st, m := e.admin("POST", "/api/opgw/credentials/CRD0001/disable", adminToken(e, tokenOpt{readonly: true}), "", map[string]string{"reason": "测试只读账号"}); st != 403 || m["code"] != "READONLY" {
		t.Fatalf("只读账号写应 403 READONLY：%d %v", st, m)
	}
}

// 发件表经事件总线发出；总线不可用时事件留在发件表
func TestOutboxPublish(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	if len(outboxTypes(t)) == 0 {
		t.Fatal("生成凭证应有留痕事件")
	}
	// 未连总线：不报错，事件留着
	if n, err := e.a.PublishOutbox(context.Background()); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	nc, js := connectJS(t)
	defer nc.Close()
	ctx := context.Background()
	if err := EnsureStream(ctx, js, 30); err != nil {
		t.Fatal(err)
	}
	_ = js.DeleteStream(ctx, "EVT_OPGW")
	if err := EnsureStream(ctx, js, 30); err != nil {
		t.Fatal(err)
	}
	e.a.JS = js
	want := len(outboxTypes(t))
	n, err := e.a.PublishOutbox(ctx)
	if err != nil || n != want {
		t.Fatalf("应发出 %d 条：%d %v", want, n, err)
	}
	if n, _ := e.a.PublishOutbox(ctx); n != 0 {
		t.Fatal("已发的不再发")
	}
	st, _ := js.Stream(ctx, "EVT_OPGW")
	info, _ := st.Info(ctx)
	if int(info.State.Msgs) != want {
		t.Fatalf("流内应有 %d 条，得 %d", want, info.State.Msgs)
	}
	msg, err := st.GetMsg(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	_ = json.Unmarshal(msg.Data, &env)
	if msg.Header.Get("Nats-Msg-Id") != env.EventID || env.Version != 1 || env.Type != msg.Subject {
		t.Fatalf("信封与消息头不符：%+v", env)
	}
}

// 事件消费：经总线持久消费者收到 fleet 事件并按 eventId 去重
func TestConsumerViaBus(t *testing.T) {
	e := newEnv(t, nil)
	nc, js := connectJS(t)
	defer nc.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, s := range []string{"EVT_FLEET", "EVT_RULE"} {
		_ = js.DeleteStream(ctx, s)
	}
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "EVT_FLEET", Subjects: []string{"fleet.>"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "EVT_RULE", Subjects: []string{"rule.>"}}); err != nil {
		t.Fatal(err)
	}
	e.a.JS = js
	go e.a.RunConsumers(ctx)
	pub := func(id, subj string, payload any) {
		p, _ := json.Marshal(payload)
		b, _ := json.Marshal(Envelope{EventID: id, Type: subj, Version: 1, OccurredAt: rfc3339(e.clock), Key: "k", Actor: systemActor, Payload: p})
		if _, err := js.Publish(ctx, subj, b); err != nil {
			t.Fatal(err)
		}
	}
	ac := map[string]any{"brandId": "BR01", "operatorId": "OP01", "from": "联调中", "to": "已接入", "changedAt": rfc3339(e.clock), "reason": nil}
	pub("F1", "fleet.brand.access_changed", ac)
	pub("F1", "fleet.brand.access_changed", ac) // 重复送达
	pub("F2", "fleet.vehicle.listed", map[string]any{"vehicleId": "VH000004"})
	pub("R1", "rule.params.changed", map[string]any{"backfillWindowHours": 48, "unlockReportTimeoutSec": 20})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n, h int
		_ = testPool.QueryRow(ctx, `SELECT count(*) FROM credentials`).Scan(&n)
		_ = testPool.QueryRow(ctx, `SELECT backfill_window_hours FROM params`).Scan(&h)
		if n == 1 && h == 48 {
			var consumed int
			_ = testPool.QueryRow(ctx, `SELECT count(*) FROM consumed_events WHERE event_id='F2'`).Scan(&consumed)
			if consumed != 0 {
				t.Fatal("未订阅的主题不应收到")
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("没有经总线收到事件")
}

func TestConsumeDedupe(t *testing.T) {
	e := newEnv(t, nil)
	e.accessChanged("E1", "BR01", "OP01", "已接入")
	e.accessChanged("E1", "BR01", "OP01", "已接入")
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE subject='opgw.audit.recorded'`).Scan(&n)
	if n != 1 {
		t.Fatalf("同一事件只处理一次，留痕应 1 条，得 %d", n)
	}
}

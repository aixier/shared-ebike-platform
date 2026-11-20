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

func startNATS(t *testing.T) *server.Server {
	t.Helper()
	dir, _ := filepath.Abs("../../.tmp")
	dir, err := os.MkdirTemp(dir, "nats-")
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats 未就绪")
	}
	t.Cleanup(func() {
		s.Shutdown()
		s.WaitForShutdown()
		_ = os.RemoveAll(dir)
	})
	return s
}

// 发件表发布到总线、从订阅流消费并去重
func TestEventBusRoundTrip(t *testing.T) {
	e := newEnv(t)
	s := startNATS(t)
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 发布方（bill）的流
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "EVT_BILL", Subjects: []string{"bill.>"}}); err != nil {
		t.Fatal(err)
	}
	uid, tok := e.login("13812345678")
	if err := e.app.EnsureStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	n, err := e.app.PublishPending(ctx, js)
	if err != nil || n == 0 {
		t.Fatalf("应发布发件表事件: %d %v", n, err)
	}
	if n2, _ := e.app.PublishPending(ctx, js); n2 != 0 {
		t.Fatalf("已发布的不应重发: %d", n2)
	}
	st, _ := js.Stream(ctx, "EVT_ACCT")
	msg, err := st.GetLastMsgForSubject(ctx, "acct.user.registered")
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	_ = json.Unmarshal(msg.Data, &env)
	if env["type"] != "acct.user.registered" || msg.Header.Get("Nats-Msg-Id") != env["eventId"] {
		t.Fatalf("消息不对: %v %v", env, msg.Header)
	}

	// 订阅：同一 eventId 发两次（总线外的重复），只处理一次
	go e.app.RunConsumers(ctx, js)
	body, _ := json.Marshal(map[string]any{"eventId": "bill-x1", "type": "bill.arrears.overdue", "version": 1, "occurredAt": e.clk.Now().Format(time.RFC3339),
		"key": uid, "actor": map[string]any{"kind": "系统", "id": "AC0900", "name": nil},
		"payload": map[string]any{"userId": uid, "userArrearsTotal": 100, "oldestAt": e.clk.Now().Format(time.RFC3339), "days": 31}})
	for i := 0; i < 2; i++ {
		if _, err := js.Publish(ctx, "bill.arrears.overdue", body); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if me := e.do("GET", "/api/acct/me", nil, bearer(tok)); me.body["account"].(map[string]any)["status"] == "冻结" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if me := e.do("GET", "/api/acct/me", nil, bearer(tok)); me.body["account"].(map[string]any)["status"] != "冻结" {
		t.Fatal("消费欠费超期事件后应冻结")
	}
	time.Sleep(500 * time.Millisecond)
	if n := len(e.outbox("acct.account.frozen")); n != 1 {
		t.Fatalf("重复事件只应处理一次: %d", n)
	}
	var cnt int
	_ = sharedPool.QueryRow(ctx, `SELECT count(*) FROM processed_events WHERE event_id='bill-x1'`).Scan(&cnt)
	if cnt != 1 {
		t.Fatal("应按 eventId 记已处理")
	}
}

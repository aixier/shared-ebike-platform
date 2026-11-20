package iam

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"bike/iam/internal/bus"
	"bike/iam/internal/config"
)

func exitedEvent(eventID, op string) []byte {
	b, _ := json.Marshal(map[string]any{
		"eventId": eventID, "type": "fleet.operator.exited", "version": 1, "occurredAt": "2026-12-01T10:00:00+08:00",
		"key": op, "actor": map[string]any{"kind": "人", "id": "AC0001", "name": "周＊＊"},
		"payload": map[string]any{"operatorId": op, "brandIds": []string{"BR01", "BR02"}, "exitedAt": "2026-12-01T10:00:00+08:00", "reason": "线下协商退出"},
	})
	return b
}

func TestOperatorExitedTurnsAccountsReadonly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.login("qhcx.he", "管理端")
	// OP01 有一个停用的账号：保持停用
	_, _ = e.db.Exec(ctx, "UPDATE iam_accounts SET status='停用' WHERE account_id='AC0111'")
	if err := e.svc.HandleEvent(ctx, "fleet.operator.exited", exitedEvent("F1", "OP01")); err != nil {
		t.Fatal(err)
	}
	// 重复送达只处理一次
	if err := e.svc.HandleEvent(ctx, "fleet.operator.exited", exitedEvent("F1", "OP01")); err != nil {
		t.Fatal(err)
	}
	var s1, s2, s3 string
	_ = e.db.QueryRow(ctx, "SELECT status FROM iam_accounts WHERE account_id='AC0101'").Scan(&s1)
	_ = e.db.QueryRow(ctx, "SELECT status FROM iam_accounts WHERE account_id='AC0111'").Scan(&s2)
	_ = e.db.QueryRow(ctx, "SELECT status FROM iam_accounts WHERE account_id='AC0102'").Scan(&s3)
	if s1 != "只读（运营商已退出）" || s2 != "停用" || s3 != "正常" {
		t.Fatalf("状态不对: %s %s %s", s1, s2, s3)
	}
	// 当前会话立即按只读处理
	if r := e.do("GET", "/api/iam/me", tok, nil); r.Body["readonly"] != true {
		t.Fatalf("应只读: %s", r.Raw)
	}
	var n int
	_ = e.db.QueryRow(ctx, "SELECT count(*) FROM iam_outbox WHERE subject='iam.account.changed' AND body->'payload'->>'change'='转只读' AND body->'payload'->>'status'='只读'").Scan(&n)
	if n != 1 {
		t.Fatalf("应发 1 条转只读事件，实际 %d", n)
	}
	_ = e.db.QueryRow(ctx, "SELECT count(*) FROM iam_audit WHERE action='调整权限' AND target_id='AC0101' AND actor_kind='系统' AND account_id='AC0900'").Scan(&n)
	if n != 1 {
		t.Fatalf("应记 1 条系统留痕，实际 %d", n)
	}
	// 停用账号此后恢复，回到只读
	admin := e.login("yw.hao", "管理端")
	r := e.do("PUT", "/api/iam/accounts/AC0111/status", admin, map[string]any{"status": "正常", "reason": "恢复查询权限"})
	if r.Body["status"] != "只读（运营商已退出）" {
		t.Fatalf("应回到只读: %s", r.Raw)
	}
}

func cleanStreams(t *testing.T, js jetstream.JetStream) {
	t.Helper()
	ctx := context.Background()
	names := js.StreamNames(ctx)
	var list []string
	for n := range names.Name() {
		list = append(list, n)
	}
	for _, n := range list {
		_ = js.DeleteStream(ctx, n)
	}
}

func jsConn(t *testing.T) jetstream.JetStream {
	t.Helper()
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, _ := jetstream.New(nc)
	return js
}

// 整个进程：别的服务的流启动时不存在也能启动；流出现后订阅接上；本服务的留痕与账号变更经发件表发出。
func TestAppEndToEndWithBus(t *testing.T) {
	js := jsConn(t)
	cleanStreams(t, js)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	name := fmt.Sprintf("iam_t%d", dbSeq.Add(1))
	if err := execAdmin(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = execAdmin(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cfg, err := config.FromEnv(func(k string) string {
		return map[string]string{
			"PORT":              fmt.Sprint(port),
			"DB_DSN":            dsnFor(name),
			"NATS_URL":          natsURL,
			"ADMIN_JWT_KID":     "k1",
			"ADMIN_JWT_PRIVKEY": base64.StdEncoding.EncodeToString(priv.Seed()),
			"EVENT_RETRY":       "200ms",
			"OUTBOX_INTERVAL":   "100ms",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := Start(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer app.Close()
	base := "http://" + cfg.Addr()
	waitUntil(t, func() bool {
		r, err := http.Get(base + "/healthz")
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	})

	// 别的服务稍后才建流并发留痕
	if err := bus.EnsureStream(ctx, js, "EVT_RIDE", []string{"ride.>"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	_ = bus.EnsureStream(ctx, js, "EVT_FLEET", []string{"fleet.>"}, time.Hour)
	ev := []byte(`{"eventId":"R1","type":"ride.audit.recorded","version":1,"occurredAt":"2026-12-01T10:00:00+08:00","key":"order:OD2026120111070","actor":{"kind":"人","id":"AC0301","name":"苏＊＊"},"payload":{"action":"强制结束订单","targetType":"order","targetId":"OD2026120111070","summary":"客服强制结束订单","reason":"车辆无法上报锁车","before":{"status":"骑行中"},"after":{"status":"已结束"}}}`)
	if err := bus.Publish(ctx, js, "ride.audit.recorded", "R1", ev); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(ctx, js, "fleet.operator.exited", "F9", exitedEvent("F9", "OP02")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		var n int
		_ = app.svc.db.QueryRow(ctx, "SELECT count(*) FROM iam_audit WHERE event_id='R1'").Scan(&n)
		return n == 1
	})
	waitUntil(t, func() bool {
		var s string
		_ = app.svc.db.QueryRow(ctx, "SELECT status FROM iam_accounts WHERE account_id='AC0102'").Scan(&s)
		return s == "只读（运营商已退出）"
	})

	// 本服务的事件经发件表发到 EVT_IAM，留痕再被自己消费时去重
	waitUntil(t, func() bool {
		s, err := js.Stream(ctx, "EVT_IAM")
		if err != nil {
			return false
		}
		info, _ := s.Info(ctx)
		return info.State.Msgs >= 2 // 转只读的账号变更 + 权限调整留痕（OP02 两个账号各一组）
	})
	waitUntil(t, func() bool {
		var n int
		_ = app.svc.db.QueryRow(ctx, "SELECT count(*) FROM iam_outbox WHERE published_at IS NULL").Scan(&n)
		return n == 0
	})
	time.Sleep(500 * time.Millisecond)
	var n int
	_ = app.svc.db.QueryRow(ctx, "SELECT count(*) FROM iam_audit WHERE action='调整权限'").Scan(&n)
	if n != 2 {
		t.Fatalf("OP02 两个账号各一条留痕，自己消费不应重复，实际 %d", n)
	}
	m, err := js.Stream(ctx, "EVT_IAM")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := m.GetLastMsgForSubject(ctx, "iam.account.changed")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Nats-Msg-Id") == "" {
		t.Fatal("应带 Nats-Msg-Id")
	}
}

// 事件总线不可用时，接口照常工作，事件留在发件表。
func TestAppStartsWithoutBus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	name := fmt.Sprintf("iam_t%d", dbSeq.Add(1))
	_ = execAdmin(ctx, "CREATE DATABASE "+name)
	t.Cleanup(func() { _ = execAdmin(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	deadNATS := "nats://" + ln.Addr().String()
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cfg, err := config.FromEnv(func(k string) string {
		return map[string]string{
			"PORT": fmt.Sprint(port), "DB_DSN": dsnFor(name), "NATS_URL": deadNATS,
			"ADMIN_JWT_KID": "k1", "ADMIN_JWT_PRIVKEY": base64.StdEncoding.EncodeToString(priv.Seed()),
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := Start(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("总线不可用不应启动失败: %v", err)
	}
	defer app.Close()
	waitUntil(t, func() bool {
		r, err := http.Get("http://" + cfg.Addr() + "/healthz")
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	})
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

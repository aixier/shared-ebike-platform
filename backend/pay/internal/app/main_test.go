package app

// 测试环境：embedded-postgres（端口 19309，文件在 .tmp/）、进程内 nats-server（127.0.0.1 随机端口）、acct 替身（httptest）。
// trace: 技术约定"本机开发与测试"

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bike/pay/internal/auth"
	"bike/pay/internal/channel"
	"bike/pay/internal/clients/acct"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/golang-jwt/jwt/v5"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

var (
	tApp      *App
	tSrv      *httptest.Server
	tUserKey  ed25519.PrivateKey
	tAdminKey ed25519.PrivateKey
	tJS       jetstream.JetStream
	tMocks    map[string]*channel.Mock
	clockMu   sync.Mutex
	clock     = time.Date(2026, 12, 1, 12, 0, 0, 0, bj)
)

func setClock(t time.Time) {
	clockMu.Lock()
	clock = t
	clockMu.Unlock()
}

func now() time.Time {
	clockMu.Lock()
	defer clockMu.Unlock()
	return clock
}

// acct 替身：实名姓名表
var realNames = map[string]string{"U100484": "薛小明", "U200001": "张三", "U200002": "李四"}

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	root, _ := filepath.Abs("../../.tmp")
	pgDir := filepath.Join(root, "pg-app")
	os.RemoveAll(pgDir)
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).Port(19309).Database("bike_pay").Locale("C").Encoding("SQL_ASCII").StartTimeout(90 * time.Second).
		RuntimePath(filepath.Join(pgDir, "runtime")).DataPath(filepath.Join(pgDir, "data")).BinariesPath(filepath.Join(pgDir, "bin")).
		Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Println("启动 embedded-postgres 失败：", err)
		return 1
	}
	defer pg.Stop()
	dsn := "postgres://postgres:postgres@127.0.0.1:19309/bike_pay?sslmode=disable"

	ns, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: filepath.Join(root, "nats-app"), NoLog: true, NoSigs: true})
	if err != nil {
		fmt.Println(err)
		return 1
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		fmt.Println("nats-server 未就绪")
		return 1
	}
	defer ns.Shutdown()
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer nc.Close()
	tJS, _ = jetstream.New(nc)
	if err := EnsureStream(context.Background(), tJS, time.Hour); err != nil {
		fmt.Println(err)
		return 1
	}

	acctMux := http.NewServeMux()
	acctMux.HandleFunc("POST /internal/acct/users/{userId}/payee-name-check", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pay-key" || r.Header.Get("X-Caller") != "pay" {
			w.WriteHeader(401)
			return
		}
		uid := r.PathValue("userId")
		name, ok := realNames[uid]
		if !ok {
			w.WriteHeader(404)
			w.Write([]byte(`{"code":"NOT_FOUND","message":"没有该用户"}`))
			return
		}
		var b struct{ Name string }
		json.NewDecoder(r.Body).Decode(&b)
		json.NewEncoder(w).Encode(map[string]bool{"match": b.Name == name})
	})
	acctSrv := httptest.NewServer(acctMux)
	defer acctSrv.Close()

	if err := Migrate(dsn); err != nil {
		fmt.Println(err)
		return 1
	}
	pool, err := OpenPool(context.Background(), dsn)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer pool.Close()

	upub, upriv, _ := ed25519.GenerateKey(rand.Reader)
	apub, apriv, _ := ed25519.GenerateKey(rand.Reader)
	tUserKey, tAdminKey = upriv, apriv
	callers, _ := auth.ParseCallers("bill=bill-key,deposit=deposit-key,ops=ops-key")
	tApp = &App{Pool: pool, Verifier: &auth.Verifier{UserKeys: map[string]ed25519.PublicKey{"u1": upub}, AdminKeys: map[string]ed25519.PublicKey{"a1": apub}},
		Callers: callers, Now: now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Acct: &acct.Client{BaseURL: acctSrv.URL, ServiceKey: "pay-key"}, IntentTTL: 15 * time.Minute, MockCallbackDelay: 3 * time.Second}
	cfg := Config{ChannelMode: "mock", MockDefault: "成功", MockUsers: "UFAIL:失败,UUNK:未知,UNEXT:跨日,UNOSET:未到账,U100484:失败", MockResolve: "成功"}
	if tApp.Ch, err = BuildChannels(cfg, pool, now); err != nil {
		fmt.Println(err)
		return 1
	}
	tMocks = map[string]*channel.Mock{}
	for k, v := range tApp.Ch {
		tMocks[k] = v.(*channel.Mock)
	}
	tSrv = httptest.NewServer(tApp.Handler())
	defer tSrv.Close()
	return m.Run()
}

// ---------- 请求工具 ----------

func userToken(uid string) string {
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "bike-acct", "sub": uid, "exp": time.Now().Add(time.Hour).Unix()})
	tk.Header["kid"] = "u1"
	s, _ := tk.SignedString(tUserKey)
	return s
}

func adminToken(id string, perms []string, scope any, readonly bool) string {
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "bike-iam", "sub": id, "name": "财务甲", "role": "财务", "client": "管理端",
		"perms": perms, "scope": scope, "readonly": readonly, "exp": time.Now().Add(time.Hour).Unix()})
	tk.Header["kid"] = "a1"
	s, _ := tk.SignedString(tAdminKey)
	return s
}

var finance = func() string {
	return adminToken("AC0301", []string{"recon.manage", "refund.manualTransfer", "order.view", "user.view", "deposit.view"}, nil, false)
}

type resp struct {
	Status int
	Body   map[string]any
	List   []map[string]any
	Raw    []byte
}

func do(t *testing.T, method, path, bearer string, hdr map[string]string, body any) resp {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, tSrv.URL+path, rd)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Raw: raw}
	_ = json.Unmarshal(raw, &out.Body)
	_ = json.Unmarshal(raw, &out.List)
	return out
}

var keySeq int

func newKey() string {
	keySeq++
	return fmt.Sprintf("k-%d-%d", time.Now().UnixNano(), keySeq)
}

// internal 调内部命令
func internal(t *testing.T, caller, path, key string, body any) resp {
	return do(t, "POST", path, caller+"-key", map[string]string{"X-Caller": caller, "Idempotency-Key": key}, body)
}

// eventsOf 从发件表取某业务键的事件类型（按顺序）
func eventsOf(t *testing.T, key string) []string {
	t.Helper()
	rows, err := tApp.Pool.Query(context.Background(), `SELECT subject FROM outbox WHERE body->>'key' = $1 ORDER BY id`, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func lastEvent(t *testing.T, subject, key string) map[string]any {
	t.Helper()
	var body []byte
	err := tApp.Pool.QueryRow(context.Background(), `SELECT body FROM outbox WHERE subject=$1 AND body->>'key'=$2 ORDER BY id DESC LIMIT 1`, subject, key).Scan(&body)
	if err != nil {
		t.Fatalf("没有事件 %s %s：%v", subject, key, err)
	}
	var m map[string]any
	json.Unmarshal(body, &m)
	return m
}

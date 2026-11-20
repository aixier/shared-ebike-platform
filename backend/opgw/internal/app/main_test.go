package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"bike/opgw/internal/sign"
)

// 测试库端口 = 服务端口 + 1000
const testPGPort = 19303

var (
	testPool *pgxpool.Pool
	testNS   *server.Server
	adminPub ed25519.PublicKey
	adminPri ed25519.PrivateKey
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	wd, _ := os.Getwd()
	tmp := filepath.Join(wd, "..", "..", ".tmp")
	_ = os.MkdirAll(tmp, 0o755)
	_ = os.RemoveAll(filepath.Join(tmp, "pgdata"))
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).Port(testPGPort).Database("bike_opgw").
		RuntimePath(filepath.Join(tmp, "pgrt")).DataPath(filepath.Join(tmp, "pgdata")).
		Locale("C").Encoding("SQL_ASCII").Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Println("启动测试库失败：", err)
		return 1
	}
	defer pg.Stop()

	_ = os.RemoveAll(filepath.Join(tmp, "nats"))
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: filepath.Join(tmp, "nats"), NoLog: true, NoSigs: true})
	if err != nil {
		fmt.Println("启动测试总线失败：", err)
		return 1
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		fmt.Println("测试总线未就绪")
		return 1
	}
	testNS = ns
	defer ns.Shutdown()

	ctx := context.Background()
	testPool, err = pgxpool.New(ctx, fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/bike_opgw?sslmode=disable", testPGPort))
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer testPool.Close()
	if err := Migrate(ctx, testPool); err != nil {
		fmt.Println("迁移失败：", err)
		return 1
	}
	adminPub, adminPri, _ = ed25519.GenerateKey(nil)
	return m.Run()
}

// ---- 测试环境 ----

type env struct {
	t     *testing.T
	a     *App
	srv   *httptest.Server
	clock time.Time
	mu    sync.Mutex

	// 替身
	op         *httptest.Server
	opHandler  func(w http.ResponseWriter, r *http.Request, body []byte)
	opCalls    int
	rideSrv    *httptest.Server
	rideStatus int
	rideGot    []map[string]any
	fleetSrv   *httptest.Server
	fleetFn    func(w http.ResponseWriter, body map[string]any)
	fleetGot   []map[string]any
}

func resetDB(t *testing.T) {
	_, err := testPool.Exec(context.Background(), `TRUNCATE credentials, brands, nonces, rate_counters, idempotency, commands, ride_events, operator_health, outbox, consumed_events, exited_operators;
		ALTER SEQUENCE credential_seq RESTART; UPDATE params SET backfill_window_hours=72, unlock_report_timeout_sec=30, changed_at=NULL`)
	if err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T, mod func(*Config)) *env {
	t.Helper()
	resetDB(t)
	e := &env{t: t, clock: time.Date(2026, 12, 1, 12, 0, 5, 0, time.FixedZone("CST", 8*3600)), rideStatus: 200}
	e.op = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.opCalls++
		h := e.opHandler
		e.mu.Unlock()
		if h == nil {
			_, _ = w.Write([]byte(`{"result":"已受理"}`))
			return
		}
		h(w, r, body)
	}))
	e.rideSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		e.mu.Lock()
		st := e.rideStatus
		if st == 200 {
			e.rideGot = append(e.rideGot, m)
		}
		e.mu.Unlock()
		if r.Header.Get("X-Caller") != "opgw" || r.Header.Get("Authorization") != "Bearer opgw-key" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(st)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	e.fleetSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		e.mu.Lock()
		e.fleetGot = append(e.fleetGot, m)
		fn := e.fleetFn
		e.mu.Unlock()
		if fn != nil {
			fn(w, m)
			return
		}
		_, _ = w.Write([]byte(`{"added":0,"vehicles":[]}`))
	}))
	cfg := Config{
		ListenHost:          "127.0.0.1",
		Port:                18303,
		AdminPubKeys:        map[string]ed25519.PublicKey{"k1": adminPub},
		ServiceKey:          "opgw-key",
		AllowedCallers:      map[string]string{"ride": "ride-key"},
		RideURL:             e.rideSrv.URL,
		FleetURL:            e.fleetSrv.URL,
		SecretKey:           bytes.Repeat([]byte{7}, 32),
		Providers:           map[string][]string{"SP01": {"OP02", "OP06"}},
		HolderNames:         map[string]string{"OP01": "青禾出行", "SP01": "泰达车联网"},
		BaseURLs:            map[string]string{"OP01": e.op.URL, "SP01": e.op.URL, "OP04": e.op.URL},
		IPWhitelists:        map[string][]string{},
		RateLimitPerMinute:  600,
		UnreachableAfter:    3,
		OperatorTimeout:     300 * time.Millisecond,
		SignWindowSec:       300,
		DeliveryRetryPeriod: time.Second,
	}
	if mod != nil {
		mod(&cfg)
	}
	e.a = New(cfg, testPool, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.a.Now = func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.clock }
	e.a.Op.Now = e.a.Now
	e.srv = httptest.NewServer(e.a.Handler())
	t.Cleanup(func() { e.srv.Close(); e.op.Close(); e.rideSrv.Close(); e.fleetSrv.Close() })
	return e
}

func (e *env) advance(d time.Duration) { e.mu.Lock(); e.clock = e.clock.Add(d); e.mu.Unlock() }

// event 投递一条订阅事件（直接走处理函数）。
func (e *env) event(id, typ string, payload any) {
	e.t.Helper()
	p, _ := json.Marshal(payload)
	b, _ := json.Marshal(Envelope{EventID: id, Type: typ, Version: 1, OccurredAt: rfc3339(e.clock), Key: "k", Actor: Actor{Kind: "系统", ID: "fleet"}, Payload: p})
	if err := e.a.HandleEnvelope(context.Background(), b); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) accessChanged(id, brand, op, to string) {
	e.event(id, "fleet.brand.access_changed", map[string]any{"brandId": brand, "operatorId": op, "from": "联调中", "to": to, "changedAt": rfc3339(e.clock), "reason": nil})
}

// seedOperators 原型样例：OP01 自行接入（BR01、BR02），OP02 经服务商 SP01（BR03），OP04 政府自有车队（BR05）。
func (e *env) seedOperators() {
	e.accessChanged("seed-1", "BR01", "OP01", "已接入")
	e.accessChanged("seed-2", "BR02", "OP01", "已接入")
	e.accessChanged("seed-3", "BR03", "OP02", "已接入")
	e.accessChanged("seed-4", "BR05", "OP04", "已接入")
}

// cred 取运营商的凭证与密钥明文。
func (e *env) cred(op string) (*Credential, string) {
	e.t.Helper()
	cs, err := e.a.credsCovering(context.Background(), testPool, op)
	if err != nil || len(cs) == 0 {
		e.t.Fatalf("没有 %s 的凭证：%v", op, err)
	}
	s, err := e.a.secretOf(cs[0])
	if err != nil {
		e.t.Fatal(err)
	}
	return cs[0], s
}

var nonceSeq int

// signed 以运营商身份发签名请求。
func (e *env) signed(op, path string, body any, hdr map[string]string) (int, map[string]any) {
	e.t.Helper()
	c, secret := e.cred(op)
	raw, ok := body.([]byte)
	if !ok {
		raw, _ = json.Marshal(body)
	}
	nonceSeq++
	ts := strconv.FormatInt(e.a.Now().Unix(), 10)
	nonce := fmt.Sprintf("nonce-%012d", nonceSeq)
	req, _ := http.NewRequest("POST", e.srv.URL+path, bytes.NewReader(raw))
	sign.Apply(req, secret, c.AppID, ts, nonce, raw)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return do(e.t, req)
}

func do(t *testing.T, req *http.Request) (int, map[string]any) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func (e *env) internal(path, key string, body any) (int, map[string]any) {
	e.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer ride-key")
	req.Header.Set("X-Caller", "ride")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return do(e.t, req)
}

type tokenOpt struct {
	perms    []string
	scope    *string
	readonly bool
	exp      time.Duration
}

func adminToken(e *env, o tokenOpt) string {
	if o.exp == 0 {
		o.exp = time.Hour
	}
	if o.perms == nil {
		o.perms = []string{"credential.manage"}
	}
	c := AdminClaims{Name: "王＊", Role: "运维", Client: "管理端", Perms: o.perms, Scope: o.scope, Readonly: o.readonly,
		RegisteredClaims: jwt.RegisteredClaims{Issuer: "bike-iam", Subject: "AC0501", IssuedAt: jwt.NewNumericDate(e.clock), ExpiresAt: jwt.NewNumericDate(e.clock.Add(o.exp))}}
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tk.Header["kid"] = "k1"
	s, _ := tk.SignedString(adminPri)
	return s
}

func (e *env) admin(method, path, token, key string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return do(e.t, req)
}

// outboxTypes 发件表里的事件类型（按顺序）。
func outboxTypes(t *testing.T) []string {
	rows, err := testPool.Query(context.Background(), `SELECT subject FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func outboxPayloads(t *testing.T, subject string) []map[string]any {
	rows, err := testPool.Query(context.Background(), `SELECT envelope FROM outbox WHERE subject=$1 ORDER BY id`, subject)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var b []byte
		_ = rows.Scan(&b)
		var env map[string]any
		_ = json.Unmarshal(b, &env)
		out = append(out, env)
	}
	return out
}

func connectJS(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	nc, err := nats.Connect(testNS.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	js, _ := jetstream.New(nc)
	return nc, js
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func jsonReader(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bike/acct/internal/config"
	"bike/acct/internal/external"
)

// 测试库端口 = 服务端口 18301 + 1000
const testPGPort = 19301

var testDSN = fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/bike_acct?sslmode=disable", testPGPort)

var sharedPool *pgxpool.Pool

func TestMain(m *testing.M) {
	tmp, _ := filepath.Abs("../../.tmp")
	_ = os.MkdirAll(tmp, 0o755)
	_ = os.RemoveAll(filepath.Join(tmp, "pg-data"))
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(testPGPort).
		Database("bike_acct").
		RuntimePath(filepath.Join(tmp, "pg-runtime")).
		DataPath(filepath.Join(tmp, "pg-data")).
		Locale("C").
		Encoding("SQL_ASCII").
		StartTimeout(60 * time.Second).
		Logger(nil))
	if err := pg.Start(); err != nil {
		fmt.Println("启动测试数据库失败:", err)
		os.Exit(1)
	}
	code := func() int {
		ctx := context.Background()
		if err := Migrate(ctx, testDSN); err != nil {
			fmt.Println("迁移失败:", err)
			return 1
		}
		p, err := pgxpool.New(ctx, testDSN)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		sharedPool = p
		defer p.Close()
		return m.Run()
	}()
	_ = pg.Stop()
	os.Exit(code)
}

// ---------------- 测试夹具 ----------------

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t        *testing.T
	app      *App
	srv      *httptest.Server
	clk      *clock
	auth     *external.MockAuthorizer
	rn       *external.MockRealName
	adminKey ed25519.PrivateKey
	cfg      *config.Config
}

func resetDB(t *testing.T) {
	_, err := sharedPool.Exec(context.Background(), `TRUNCATE users, account_history, revoked_tokens, notices, idempotency, outbox,
		processed_events, riding_orders, closed_orders, order_bills, arrears_orders, user_deposit, degrade_state, rule_versions;
		ALTER SEQUENCE user_no_seq RESTART WITH 100001; ALTER SEQUENCE notice_no_seq RESTART WITH 1`)
	if err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	resetDB(t)
	_, upriv, _ := ed25519.GenerateKey(rand.Reader)
	apub, apriv, _ := ed25519.GenerateKey(rand.Reader)
	cfg := &config.Config{
		UserKey: upriv, UserKid: "u1",
		UserPubKeys:    map[string]ed25519.PublicKey{"u1": upriv.Public().(ed25519.PublicKey)},
		AdminPubKeys:   map[string]ed25519.PublicKey{"a1": apub},
		AllowedCallers: map[string]string{"cs": "cs-key", "pay": "pay-key"},
		TokenTTL:       168 * time.Hour, DeferredDays: 7, LegalAge: 16,
	}
	clk := &clock{t: time.Date(2026, 12, 1, 12, 0, 0, 0, cst)}
	e := &env{t: t, clk: clk, auth: &external.MockAuthorizer{}, rn: external.NewMockRealName("", "李鬼"), adminKey: apriv, cfg: cfg}
	e.app = New(Deps{Pool: sharedPool, Cfg: cfg, Auth: e.auth, RealName: e.rn, Now: clk.Now})
	e.srv = httptest.NewServer(e.app.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

type resp struct {
	status int
	body   map[string]any
	list   []any
}

func (r resp) str(k string) string { s, _ := r.body[k].(string); return s }

func (e *env) do(method, path string, body any, hdr map[string]string) resp {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	out := resp{status: res.StatusCode}
	var raw any
	_ = json.NewDecoder(res.Body).Decode(&raw)
	switch v := raw.(type) {
	case map[string]any:
		out.body = v
	case []any:
		out.list = v
	}
	return out
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func idem(h map[string]string, key string) map[string]string {
	m := map[string]string{"Idempotency-Key": key}
	for k, v := range h {
		m[k] = v
	}
	return m
}

func internalHdr(caller, key, idemKey string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + key, "X-Caller": caller}
	if idemKey != "" {
		h["Idempotency-Key"] = idemKey
	}
	return h
}

func (e *env) adminToken(perms []string, scope any) string {
	c := jwt.MapClaims{"iss": "bike-iam", "sub": "AC0301", "name": "苏＊＊", "role": "客服", "client": "管理端",
		"perms": perms, "scope": scope, "readonly": false, "iat": e.clk.Now().Unix(), "exp": e.clk.Now().Add(time.Hour).Unix()}
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tk.Header["kid"] = "a1"
	s, _ := tk.SignedString(e.adminKey)
	return s
}

func (e *env) readonlyAdminToken(perms []string) string {
	c := jwt.MapClaims{"iss": "bike-iam", "sub": "AC0301", "name": "苏＊＊", "role": "客服", "client": "管理端",
		"perms": perms, "scope": nil, "readonly": true, "iat": e.clk.Now().Unix(), "exp": e.clk.Now().Add(time.Hour).Unix()}
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tk.Header["kid"] = "a1"
	s, _ := tk.SignedString(e.adminKey)
	return s
}

// login 授权登录，返回用户编号与令牌
func (e *env) login(phone string) (string, string) {
	e.t.Helper()
	r := e.do("POST", "/api/acct/authorize", map[string]any{"channel": "微信", "phone": phone}, nil)
	if r.status != 200 {
		e.t.Fatalf("登录失败 %d %v", r.status, r.body)
	}
	return r.body["user"].(map[string]any)["userId"].(string), r.str("token")
}

// event 投递一条订阅事件（信封格式见 contracts/events/README.md）
func (e *env) event(id, typ string, key string, payload map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"eventId": id, "type": typ, "version": 1, "occurredAt": e.clk.Now().Format(time.RFC3339),
		"key": key, "actor": map[string]any{"kind": "系统", "id": "AC0900", "name": nil}, "payload": payload})
	if err := e.app.HandleEvent(context.Background(), b); err != nil {
		e.t.Fatalf("处理事件 %s 失败: %v", typ, err)
	}
}

// outbox 取发件表里某类型的事件载荷
func (e *env) outbox(typ string) []map[string]any {
	e.t.Helper()
	rows, err := sharedPool.Query(context.Background(), `SELECT body FROM outbox WHERE subject=$1 ORDER BY id`, typ)
	if err != nil {
		e.t.Fatal(err)
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

func validID(first17 string) string {
	w := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	s := 0
	for i := 0; i < 17; i++ {
		s += w[i] * int(first17[i]-'0')
	}
	return first17 + string("10X98765432"[s%11])
}

var (
	adultID = validID("11010519900307299") // 1990 年生
	minorID = validID("11010520150101001") // 2015 年生，未满 16
)

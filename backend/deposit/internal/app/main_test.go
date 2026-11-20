package app

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
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bike/deposit/internal/auth"
	"bike/deposit/internal/clients/pay"
	"bike/deposit/internal/clients/pay/payfake"
	"bike/deposit/internal/credit"
)

// 测试库：embedded-postgres，端口 = 18310 + 1000，运行与数据目录放本服务 .tmp/。
const testPGPort = 19310

var (
	testDSN  = fmt.Sprintf("postgres://deposit:deposit@127.0.0.1:%d/bike_deposit?sslmode=disable", testPGPort)
	testPool *pgxpool.Pool
	signKey  ed25519.PrivateKey
	pubKey   ed25519.PublicKey
	loc      = time.FixedZone("UTC+8", 8*3600)
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	tmp, _ := filepath.Abs(filepath.Join("..", "..", ".tmp"))
	_ = os.MkdirAll(tmp, 0o755)
	_ = os.RemoveAll(filepath.Join(tmp, "pg-data"))
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).Port(testPGPort).Locale("C").Encoding("UTF8").
		Username("deposit").Password("deposit").Database("bike_deposit").
		RuntimePath(filepath.Join(tmp, "pg-runtime")).DataPath(filepath.Join(tmp, "pg-data")).
		// 仓库路径含中文，initdb 把二进制旁的 share 路径写进 SQL 时按 GBK 传入会报 UTF8 错误；
		// 所以只把二进制解压到系统临时目录下本服务专用的纯 ASCII 目录，数据目录仍在 .tmp/。
		BinariesPath(filepath.Join(os.TempDir(), "bike-deposit-pgbin")).
		StartTimeout(60 * time.Second).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "起测试库失败：", err)
		return 1
	}
	defer func() { _ = pg.Stop() }()
	if err := Migrate(testDSN); err != nil {
		fmt.Fprintln(os.Stderr, "迁移失败：", err)
		return 1
	}
	var err error
	testPool, err = pgxpool.New(context.Background(), testDSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer testPool.Close()
	pubKey, signKey, _ = ed25519.GenerateKey(rand.Reader)
	return m.Run()
}

// env 一次测试的环境：清空的库、pay 替身、可调的时钟。
type env struct {
	t    *testing.T
	app  *App
	pay  *payfake.Server
	srv  *httptest.Server
	now  time.Time
	nEvt int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	_, err := testPool.Exec(ctx, `TRUNCATE accounts, deposits, charges, deductions, refund_requests, ledger, proj_riding, proj_bills,
		proj_users, proj_rule_versions, proj_params, outbox, inbox, idempotency`)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, now: time.Date(2026, 12, 1, 12, 0, 0, 0, loc)}
	e.pay = payfake.New()
	t.Cleanup(e.pay.Close)
	scorer, _ := credit.NewMock("U100087=700,U100127=500", 650)
	callers, _ := auth.ParseCallers("bill=bill-key,cs=cs-key")
	v := auth.NewVerifier(map[string]ed25519.PublicKey{"k1": pubKey})
	e.app = New(Deps{
		DB: testPool, Pay: pay.New(e.pay.URL, "deposit-key"), Credit: scorer,
		UserVerifier: v, AdminVerifier: v, Callers: callers, Loc: loc,
		Now: func() time.Time { return e.now }, RefundWorkdaysDefault: 3, DefaultChannel: "微信支付",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	e.srv = httptest.NewServer(e.app.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

func userToken(uid string) string {
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "bike-acct", "sub": uid, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	tk.Header["kid"] = "k1"
	s, _ := tk.SignedString(signKey)
	return s
}

func adminToken(acc string, scope any, perms ...string) string {
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "bike-iam", "sub": acc, "name": "吕＊＊", "role": "财务", "client": "管理端",
		"perms": perms, "scope": scope, "readonly": false, "exp": time.Now().Add(time.Hour).Unix()})
	tk.Header["kid"] = "k1"
	s, _ := tk.SignedString(signKey)
	return s
}

type resp struct {
	status int
	body   map[string]any
	raw    []byte
}

func (e *env) do(method, path string, headers map[string]string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	raw, _ := io.ReadAll(r.Body)
	out := resp{status: r.StatusCode, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (e *env) user(method, path, uid, idem string, body any) resp {
	h := map[string]string{"Authorization": "Bearer " + userToken(uid)}
	if idem != "" {
		h["Idempotency-Key"] = idem
	}
	return e.do(method, path, h, body)
}

func (e *env) internal(path, caller, key, idem string, body any) resp {
	h := map[string]string{"Authorization": "Bearer " + key, "X-Caller": caller}
	if idem != "" {
		h["Idempotency-Key"] = idem
	}
	return e.do("POST", path, h, body)
}

// event 构造并处理一条订阅事件。
func (e *env) event(typ, key string, payload map[string]any) {
	e.t.Helper()
	e.nEvt++
	id := fmt.Sprintf("EVT-%s-%d-%d", typ, time.Now().UnixNano(), e.nEvt)
	e.eventWithID(id, typ, key, payload)
}

func (e *env) eventWithID(id, typ, key string, payload map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"eventId": id, "type": typ, "version": 1, "occurredAt": e.now.Format(time.RFC3339), "key": key,
		"actor": map[string]any{"kind": "系统", "id": "AC0900", "name": "系统"}, "payload": payload})
	if err := e.app.HandleEvent(context.Background(), b); err != nil {
		e.t.Fatalf("处理事件 %s 失败：%v", typ, err)
	}
}

// 常用前置：规则版本、平台参数、实名用户。
func (e *env) rule(versionID string, effective time.Time, amount int64, threshold int) {
	e.event("rule.version.published", "pricing", map[string]any{"versionId": versionID, "name": versionID, "effectiveAt": effective.Format(time.RFC3339),
		"previousVersionId": nil, "deposit": map[string]any{"amount": amount}, "creditFree": map[string]any{"threshold": threshold, "providers": []string{"微信支付分", "芝麻信用"}}})
}

func (e *env) verified(uid string) {
	e.event("acct.realname.changed", uid, map[string]any{"userId": uid, "status": "已核验", "adult": true, "deadline": nil, "failReason": nil, "afterDegrade": false})
}

func (e *env) baseline() {
	e.rule("PR0002", time.Date(2026, 10, 15, 0, 0, 0, 0, loc), 19900, 550)
}

// outboxTypes 发件表里的事件类型（按顺序）。
func (e *env) outbox() []map[string]any {
	e.t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT body FROM outbox ORDER BY seq`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var b []byte
		_ = rows.Scan(&b)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		out = append(out, m)
	}
	return out
}

func (e *env) lastOf(typ string) map[string]any {
	e.t.Helper()
	var found map[string]any
	for _, m := range e.outbox() {
		if m["type"] == typ {
			found = m
		}
	}
	if found == nil {
		e.t.Fatalf("发件表里没有 %s", typ)
	}
	return found["payload"].(map[string]any)
}

func (e *env) countOf(typ string) int {
	n := 0
	for _, m := range e.outbox() {
		if m["type"] == typ {
			n++
		}
	}
	return n
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func mustStatus(t *testing.T, r resp, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("HTTP %d，期望 %d：%s", r.status, want, r.raw)
	}
}

func mustCode(t *testing.T, r resp, status int, code string) {
	t.Helper()
	if r.status != status || r.body["code"] != code {
		t.Fatalf("期望 %d %s，得到 %d %s", status, code, r.status, r.raw)
	}
}

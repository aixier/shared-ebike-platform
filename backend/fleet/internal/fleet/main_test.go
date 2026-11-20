package fleet

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	natsserver "github.com/nats-io/nats-server/v2/server"

	"bike/fleet/internal/auth"
	"bike/fleet/internal/db"
)

// 测试数据库端口 = 服务端口 18305 + 1000
const testPGPort = 19305

var (
	testPool    *pgxpool.Pool
	testNATS    *natsserver.Server
	adminPriv   ed25519.PrivateKey
	testHandler http.Handler
	testSvc     *Service
	clockNow    = time.Date(2026, 12, 1, 12, 0, 0, 0, CST)
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	root, _ := filepath.Abs(filepath.Join("..", "..", ".tmp"))
	_ = os.RemoveAll(filepath.Join(root, "pgdata"))
	_ = os.MkdirAll(root, 0o755)
	// 仓库路径含中文，initdb 会把可执行文件旁的 share 路径写进 SQL，Windows 上按 GBK 传入导致 UTF8 报错；
	// 所以二进制解压到系统临时目录下本服务专用的纯 ASCII 目录，运行与数据目录仍在 .tmp/。
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(testPGPort).
		Database("bike_fleet").
		RuntimePath(filepath.Join(root, "pgrt")).
		BinariesPath(filepath.Join(os.TempDir(), "bike-fleet-pgbin")).
		DataPath(filepath.Join(root, "pgdata")).
		Locale("C").
		Encoding("UTF8").
		StartTimeout(60 * time.Second).
		Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Println("启动测试数据库失败:", err)
		return 1
	}
	defer pg.Stop()

	ctx := context.Background()
	var err error
	testPool, err = db.Open(ctx, fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/bike_fleet?sslmode=disable", testPGPort))
	if err != nil {
		fmt.Println("连接测试数据库失败:", err)
		return 1
	}
	defer testPool.Close()
	if err := db.Migrate(ctx, testPool); err != nil {
		fmt.Println("迁移失败:", err)
		return 1
	}

	storeDir, _ := os.MkdirTemp(root, "nats-")
	defer os.RemoveAll(storeDir)
	testNATS, err = natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: storeDir, NoLog: true, NoSigs: true})
	if err != nil {
		fmt.Println("启动 nats 失败:", err)
		return 1
	}
	go testNATS.Start()
	if !testNATS.ReadyForConnections(5 * time.Second) {
		fmt.Println("nats 未就绪")
		return 1
	}
	defer testNATS.Shutdown()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	adminPriv = priv
	callers, _ := auth.ParseCallers("opgw=k-opgw,onboard=k-onboard,ops=k-ops")
	testSvc = NewService(testPool, func() time.Time { return clockNow }, Actor{Kind: "系统", ID: "AC0900", Name: "系统定时任务"})
	testHandler = NewHandler(testSvc, auth.NewAdminVerifier(map[string]ed25519.PublicKey{"k1": pub}), callers)
	return m.Run()
}

// reset 清空库并装入原型样例（testdata/seed.json）。
func reset(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	_, err := testPool.Exec(ctx, `TRUNCATE providers, operators, brands, brand_status_history, vehicles, pauses, counters, outbox, idempotency RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "seed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testSvc.LoadSeed(ctx, data); err != nil {
		t.Fatal(err)
	}
	_, _ = testPool.Exec(ctx, `DELETE FROM outbox`)
}

func mustReadSeed(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "seed.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type tokOpt struct {
	perms    []string
	scope    any
	readonly bool
	sub      string
}

func token(t *testing.T, o tokOpt) string {
	t.Helper()
	sub := o.sub
	if sub == "" {
		sub = "AC0001"
	}
	c := jwt.MapClaims{"iss": "bike-iam", "sub": sub, "name": "测试＊", "role": "测试", "client": "管理端",
		"perms": o.perms, "scope": o.scope, "readonly": o.readonly, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tk.Header["kid"] = "k1"
	s, err := tk.SignedString(adminPriv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// 常用身份
func govTok(t *testing.T) string {
	return token(t, tokOpt{perms: []string{"access.view", "access.setExit", "vehicle.view.own"}, scope: nil})
}
func op1Tok(t *testing.T) string {
	return token(t, tokOpt{sub: "AC0101", perms: []string{"vehicle.view.own", "vehicle.edit.own", "access.view.own"}, scope: "OP01"})
}
func op3Tok(t *testing.T) string {
	return token(t, tokOpt{sub: "AC0103", perms: []string{"vehicle.view.own", "vehicle.edit.own", "access.view.own"}, scope: "OP03", readonly: true})
}

type resp struct {
	status int
	body   map[string]any
	arr    []any
	raw    []byte
}

func call(t *testing.T, method, path, tok string, body any, hdr map[string]string) resp {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	r.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	testHandler.ServeHTTP(w, r)
	out := resp{status: w.Code, raw: w.Body.Bytes()}
	_ = json.Unmarshal(out.raw, &out.body)
	_ = json.Unmarshal(out.raw, &out.arr)
	return out
}

// internalCall 以某个调用方的服务凭证调 /internal 接口。
func internalCall(t *testing.T, caller, path, idem string, body any) resp {
	t.Helper()
	h := map[string]string{"X-Caller": caller}
	if idem != "" {
		h["Idempotency-Key"] = idem
	}
	return call(t, "POST", path, "k-"+caller, body, h)
}

func wantCode(t *testing.T, r resp, status int, code string) {
	t.Helper()
	if r.status != status || (code != "" && r.body["code"] != code) {
		t.Fatalf("期望 %d %s，得到 %d %s", status, code, r.status, string(r.raw))
	}
}

// outboxTypes 返回发件表里的事件类型（按写入顺序）。
func outboxTypes(t *testing.T) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT type FROM outbox ORDER BY seq`)
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

// outboxEvents 返回发件表里某类事件的信封。
func outboxEvents(t *testing.T, typ string) []map[string]any {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT body FROM outbox WHERE type=$1 ORDER BY seq`, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var b []byte
		_ = rows.Scan(&b)
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		out = append(out, m)
	}
	return out
}

func countType(types []string, typ string) int {
	n := 0
	for _, s := range types {
		if s == typ {
			n++
		}
	}
	return n
}

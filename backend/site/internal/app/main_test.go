package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	natsserver "github.com/nats-io/nats-server/v2/server"

	"bike/site/internal/config"
)

// 测试库端口 = 服务端口 + 1000（技术约定）。
// 本目录路径含中文，initdb 在 UTF8 下读路径会报字节序列错误，故测试库用 SQL_ASCII（字节原样存取，客户端仍按 UTF-8 读写）
const testPGPort = 19306

var (
	testPool *pgxpool.Pool
	testNats *natsserver.Server
	tmpRoot  string
	adminPub ed25519.PublicKey
	adminKey ed25519.PrivateKey
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	var err error
	tmpRoot, err = filepath.Abs(filepath.Join("..", "..", ".tmp"))
	if err != nil {
		panic(err)
	}
	_ = os.MkdirAll(tmpRoot, 0o755)
	dataPath := filepath.Join(tmpRoot, "pg-data")
	_ = os.RemoveAll(dataPath)
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).Port(testPGPort).Locale("C").Encoding("SQL_ASCII").
		RuntimePath(filepath.Join(tmpRoot, "pg-runtime")).DataPath(dataPath).
		Database("bike_site").Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "embedded-postgres 启动失败:", err)
		return 1
	}
	defer pg.Stop()

	dsn := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/bike_site?sslmode=disable", testPGPort)
	ctx := context.Background()
	if testPool, err = OpenDB(ctx, dsn); err != nil {
		fmt.Fprintln(os.Stderr, "连库失败:", err)
		return 1
	}
	defer testPool.Close()

	storeDir, _ := os.MkdirTemp(tmpRoot, "nats-")
	defer os.RemoveAll(storeDir)
	testNats, err = natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: storeDir, NoLog: true, NoSigs: true})
	if err != nil {
		panic(err)
	}
	go testNats.Start()
	if !testNats.ReadyForConnections(10 * time.Second) {
		panic("nats 未就绪")
	}
	defer testNats.Shutdown()

	adminPub, adminKey, _ = ed25519.GenerateKey(rand.Reader)
	return m.Run()
}

// 测试时钟：固定在原型的 NOW（2026-12-01 12:00:00 +08:00），可拨动
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

var protoNow = time.Date(2026, 12, 1, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))

type harness struct {
	t     *testing.T
	app   *App
	srv   *httptest.Server
	clock *clock
}

// 每个测试一个干净的库
func resetDB(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `TRUNCATE controllers, stations, piles, pile_msgs, pile_unrecognized, vehicle_tags, ride_orders, parked_vehicles, outbox, inbox, idempotency CASCADE;
		ALTER SEQUENCE station_seq RESTART WITH 1; ALTER SEQUENCE pile_seq RESTART WITH 1;`)
	if err != nil {
		t.Fatal(err)
	}
}

func testConfig(mod ...func(*config.Config)) config.Config {
	env := map[string]string{
		"DB_DSN":            "unused",
		"ADMIN_JWT_PUBKEYS": "k1=" + b64(adminPub),
		"PILE_ADAPTERS":     "acme",
		"PILE_VENDOR_KEYS":  "acme=acme-vendor-key",
		"PILE_DEVICES": `[{"deviceId":"DV0001","stationId":"ST0001","secret":"dv1-secret"},
			{"deviceId":"DV0003","stationId":"ST0003","secret":"dv3-secret"},
			{"deviceId":"DV0101","stationId":"ST0004","vendor":"acme","secret":"dv101-secret"},
			{"deviceId":"DV9001","pileId":"PL00012","secret":"dv9001-secret"}]`,
		"MQTT_ADDR": "127.0.0.1:0",
	}
	c, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		panic(err)
	}
	for _, f := range mod {
		f(&c)
	}
	return c
}

func newHarness(t *testing.T, mod ...func(*config.Config)) *harness {
	t.Helper()
	resetDB(t)
	ck := &clock{t: protoNow}
	a, err := New(testConfig(mod...), testPool, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	a.now = ck.Now
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &harness{t: t, app: a, srv: srv, clock: ck}
}

func token(perms ...string) string { return tokenWith(nil, perms...) }

func tokenWith(extra map[string]any, perms ...string) string {
	if perms == nil {
		perms = []string{}
	}
	c := jwt.MapClaims{"iss": "bike-iam", "sub": "AC0001", "name": "王＊", "role": "主管部门", "client": "管理端",
		"perms": perms, "scope": nil, "readonly": false, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	for k, v := range extra {
		c[k] = v
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tok.Header["kid"] = "k1"
	s, _ := tok.SignedString(adminKey)
	return s
}

type resp struct {
	Status int
	Body   map[string]any
	Raw    string
}

// req 发请求；headers 成对给出
func (h *harness) req(method, path string, body any, headers ...string) resp {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		default:
			bs, _ := json.Marshal(b)
			rd = bytes.NewReader(bs)
		}
	}
	r, _ := http.NewRequest(method, h.srv.URL+path, rd)
	r.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Raw: string(raw)}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

func (h *harness) admin(method, path string, body any, perms ...string) resp {
	return h.req(method, path, body, "Authorization", "Bearer "+token(perms...))
}

// outboxEvents 按类型取发件表里的事件信封
func (h *harness) outboxEvents(typ string) []map[string]any {
	h.t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT body FROM outbox WHERE subject=$1 ORDER BY seq`, typ)
	if err != nil {
		h.t.Fatal(err)
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

func payload(e map[string]any) map[string]any { p, _ := e["payload"].(map[string]any); return p }

func mustStatus(t *testing.T, r resp, want int) {
	t.Helper()
	if r.Status != want {
		t.Fatalf("状态码 %d，期望 %d：%s", r.Status, want, r.Raw)
	}
}

func mustCode(t *testing.T, r resp, status int, code string) {
	t.Helper()
	mustStatus(t, r, status)
	if r.Body["code"] != code {
		t.Fatalf("错误码 %v，期望 %s：%s", r.Body["code"], code, r.Raw)
	}
	if m, _ := r.Body["message"].(string); m == "" {
		t.Fatalf("错误体缺 message：%s", r.Raw)
	}
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

type configT = config.Config

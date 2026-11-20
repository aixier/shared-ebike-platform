package iam

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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats-server/v2/server"

	"bike/iam/internal/token"
)

// 测试库端口 = 服务端口 + 1000
const pgPort = 19315

var (
	adminDSN string
	natsURL  string
	dbSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	tmp, _ := filepath.Abs(filepath.Join("..", "..", ".tmp"))
	_ = os.MkdirAll(tmp, 0o755)
	_ = os.RemoveAll(filepath.Join(tmp, "pg-data"))
	// 本仓库路径含中文，initdb 会把可执行文件旁的 share 路径写进 SQL，
	// Windows 上按 GBK 传入导致 UTF8 报错；所以二进制解压到系统临时目录下本服务专用的纯 ASCII 目录。
	binDir := filepath.Join(os.TempDir(), "bike-iam-pgbin")
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(pgPort).
		RuntimePath(filepath.Join(tmp, "pg-runtime")).
		BinariesPath(binDir).
		DataPath(filepath.Join(tmp, "pg-data")).
		Locale("C").
		Encoding("UTF8").
		Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Println("启动 embedded-postgres 失败:", err)
		return 1
	}
	defer func() { _ = pg.Stop() }()
	adminDSN = fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", pgPort)

	// 模板库：迁移一次，各测试从模板复制
	ctx := context.Background()
	if err := execAdmin(ctx, "CREATE DATABASE iam_tpl"); err != nil {
		fmt.Println(err)
		return 1
	}
	if err := Migrate(ctx, dsnFor("iam_tpl")); err != nil {
		fmt.Println("迁移失败:", err)
		return 1
	}

	natsDir := filepath.Join(tmp, "nats-iam")
	_ = os.RemoveAll(natsDir)
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: natsDir, NoLog: true, NoSigs: true})
	if err != nil {
		fmt.Println(err)
		return 1
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		fmt.Println("nats 未就绪")
		return 1
	}
	natsURL = ns.ClientURL()
	defer func() { ns.Shutdown(); ns.WaitForShutdown(); _ = os.RemoveAll(natsDir) }()

	return m.Run()
}

func dsnFor(db string) string {
	return fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/%s?sslmode=disable", pgPort, db)
}

func execAdmin(ctx context.Context, sql string) error {
	p, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		return err
	}
	defer p.Close()
	_, err = p.Exec(ctx, sql)
	return err
}

// testEnv 是一个测试用的服务实例：独立数据库、固定时钟、httptest 服务器。
type testEnv struct {
	t      *testing.T
	svc    *Service
	db     *pgxpool.Pool
	srv    *httptest.Server
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
	now    time.Time
	dbName string
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("iam_t%d", dbSeq.Add(1))
	if err := execAdmin(ctx, "CREATE DATABASE "+name+" TEMPLATE iam_tpl"); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsnFor(name))
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	e := &testEnv{t: t, db: pool, pub: pub, priv: priv, dbName: name, now: time.Date(2026, 12, 1, 9, 0, 0, 0, loc)}
	e.svc = NewService(Options{
		DB:             pool,
		Signer:         token.NewSigner("k2026", priv, 30*time.Minute),
		ExtraPubKeys:   map[string]ed25519.PublicKey{},
		AllowedCallers: map[string]string{"ride": "ride-secret"},
		Location:       loc,
		Now:            func() time.Time { return e.now },
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	e.srv = httptest.NewServer(e.svc.Handler())
	t.Cleanup(func() {
		e.srv.Close()
		pool.Close()
		_ = execAdmin(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return e
}

type resp struct {
	Status int
	Body   map[string]any
	Raw    []byte
}

func (e *testEnv) do(method, path, tok string, body any, hdr ...string) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	raw, _ := io.ReadAll(r.Body)
	out := resp{Status: r.StatusCode, Raw: raw}
	if len(raw) > 0 && raw[0] == '{' {
		_ = json.Unmarshal(raw, &out.Body)
	}
	return out
}

func (e *testEnv) login(username, client string) string {
	e.t.Helper()
	r := e.do("POST", "/api/iam/login", "", map[string]any{"username": username, "password": "Shifan@2026", "client": client})
	if r.Status != 200 {
		e.t.Fatalf("登录 %s 失败: %d %s", username, r.Status, r.Raw)
	}
	return r.Body["token"].(string)
}

func expectErr(t *testing.T, r resp, status int, code string) {
	t.Helper()
	if r.Status != status || r.Body["code"] != code {
		t.Fatalf("期望 %d %s，实际 %d %s", status, code, r.Status, r.Raw)
	}
	if m, _ := r.Body["message"].(string); strings.TrimSpace(m) == "" {
		t.Fatalf("错误体缺 message: %s", r.Raw)
	}
}

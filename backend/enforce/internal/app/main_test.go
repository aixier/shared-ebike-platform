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
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"bike/enforce/internal/auth"
)

// 测试库端口 = 服务端口 18314 + 1000
const pgPort = 19314

var (
	tmpRoot string
	natsURL string
	dbSeq   atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	root, err := filepath.Abs("../../.tmp")
	if err != nil {
		panic(err)
	}
	tmpRoot = root
	_ = os.MkdirAll(tmpRoot, 0o755)
	_ = os.RemoveAll(filepath.Join(tmpRoot, "pg-data"))

	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(pgPort).
		Locale("C").
		StartTimeout(60 * time.Second).
		RuntimePath(filepath.Join(tmpRoot, "pg-runtime")).
		DataPath(filepath.Join(tmpRoot, "pg-data")).
		Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Println("启动测试库失败：", err)
		return 1
	}
	defer pg.Stop()

	storeDir, _ := os.MkdirTemp(tmpRoot, "nats-")
	defer os.RemoveAll(storeDir)
	ns, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: storeDir, NoLog: true, NoSigs: true})
	if err != nil {
		fmt.Println("启动事件总线失败：", err)
		return 1
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		fmt.Println("事件总线未就绪")
		return 1
	}
	defer ns.Shutdown()
	natsURL = ns.ClientURL()

	return m.Run()
}

// ---------- 测试环境 ----------

var fixedNow = time.Date(2026, 12, 1, 12, 0, 0, 0, shanghai())

type env struct {
	t     *testing.T
	app   *App
	srv   *httptest.Server
	pool  *pgxpool.Pool
	priv  ed25519.PrivateKey
	now   time.Time
	photo string
}

// newEnv 每个测试一个全新数据库，证据表不可清空所以不复用。
func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("t%d_%d", time.Now().UnixNano()%1e9, dbSeq.Add(1))
	admin, err := pgxpool.New(ctx, fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", pgPort))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	admin.Close()
	pool, err := pgxpool.New(ctx, fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/%s?sslmode=disable", pgPort, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	photoDir, err := os.MkdirTemp(tmpRoot, "photos-")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pool: pool, priv: priv, now: fixedNow, photo: photoDir}
	callers, _ := auth.ParseAllowedCallers("ops=ops-secret")
	e.app = &App{
		Pool:      pool,
		Verifier:  auth.NewAdminVerifier(map[string]ed25519.PublicKey{"k1": pub}),
		Callers:   callers,
		PhotoDir:  photoDir,
		OrphanTTL: 24 * time.Hour,
		Now:       func() time.Time { return e.now },
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	e.srv = httptest.NewServer(e.app.Handler())
	t.Cleanup(func() {
		e.srv.Close()
		pool.Close()
		_ = os.RemoveAll(photoDir)
	})
	return e
}

// ---------- 令牌 ----------

type tokOpt func(jwt.MapClaims)

func withPerms(p ...string) tokOpt { return func(c jwt.MapClaims) { c["perms"] = p } }
func withScope(s string) tokOpt    { return func(c jwt.MapClaims) { c["scope"] = s } }
func withReadonly() tokOpt         { return func(c jwt.MapClaims) { c["readonly"] = true } }

func (e *env) token(sub, role string, opts ...tokOpt) string {
	c := jwt.MapClaims{
		"iss": "bike-iam", "sub": sub, "name": sub + "姓名", "role": role, "perms": []string{}, "scope": nil, "readonly": false,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}
	if role == "执法人员" {
		c["client"] = "执法端"
		c["badgeNo"] = "B" + sub
	} else {
		c["client"] = "管理端"
	}
	for _, o := range opts {
		o(c)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(e.priv)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) officer(id string) string { return e.token(id, "执法人员") }
func (e *env) supervisor(opts ...tokOpt) string {
	return e.token("AC0001", "主管部门管理员", append([]tokOpt{withPerms("evidence.view", "evidence.export")}, opts...)...)
}

// ---------- HTTP ----------

type resp struct {
	status int
	header http.Header
	raw    []byte
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.raw, &m); err != nil {
		t.Fatalf("响应不是 JSON 对象：%s", r.raw)
	}
	return m
}

func (r resp) code(t *testing.T) string {
	t.Helper()
	c, _ := r.json(t)["code"].(string)
	return c
}

func (e *env) do(method, path, token string, body any, hdr ...string) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return e.send(req)
}

func (e *env) send(req *http.Request) resp {
	e.t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, raw: b}
}

func (e *env) upload(token string, content []byte, idem string) resp {
	e.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "a.jpg")
	_, _ = fw.Write(content)
	_ = w.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/enforce/photos", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	return e.send(req)
}

// 最小可识别的 JPEG 与 PNG 内容（按文件头识别类型）
func jpegBytes(tag string) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}, []byte(tag)...)
}
func pngBytes(tag string) []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte(tag)...)
}

// uploadPhoto 上传一张照片并返回 photoId
func (e *env) uploadPhoto(token, tag string) string {
	e.t.Helper()
	r := e.upload(token, jpegBytes(tag), "")
	if r.status != http.StatusCreated {
		e.t.Fatalf("上传照片失败：%d %s", r.status, r.raw)
	}
	return r.json(e.t)["photoId"].(string)
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// ---------- 事件总线 ----------

func connectJS(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// 各测试独立：先清掉上一个测试留下的流
	for _, s := range []string{"EVT_FLEET", "EVT_ENFORCE"} {
		_ = js.DeleteStream(ctx, s)
	}
	t.Cleanup(nc.Close)
	return nc, js
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

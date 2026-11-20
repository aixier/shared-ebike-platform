package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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

	"bike/cs/internal/auth"
	"bike/cs/internal/clients/acct"
	"bike/cs/internal/clients/bill"
	"bike/cs/internal/clients/deposit"
	"bike/cs/internal/clients/ride"
	"bike/cs/internal/fakes"
)

// 测试库：embedded-postgres，端口 = 服务端口 18312 + 1000；临时文件放 backend/cs/.tmp/。
const testDSN = "postgres://postgres:postgres@127.0.0.1:19312/bike_cs?sslmode=disable"

var (
	pool      *pgxpool.Pool
	natsSrv   *natsserver.Server
	tmpRoot   string
	userPriv  ed25519.PrivateKey
	adminPriv ed25519.PrivateKey
	userKeys  auth.Keys
	adminKeys auth.Keys
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
	dataPath := filepath.Join(tmpRoot, "pg-data")
	_ = os.RemoveAll(dataPath)
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).Port(19312).Database("postgres").Locale("C").Encoding("SQL_ASCII").
		RuntimePath(filepath.Join(tmpRoot, "pg-runtime")).DataPath(dataPath).
		StartTimeout(60 * time.Second).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Println("启动测试数据库失败：", err)
		return 1
	}
	defer pg.Stop() //nolint:errcheck
	// 本仓库路径含中文，initdb 只能用 SQL_ASCII 建集群；业务库按生产一样用 UTF8 另建
	if err := createUTF8DB(); err != nil {
		fmt.Println("建测试库失败：", err)
		return 1
	}

	if err := Migrate(testDSN); err != nil {
		fmt.Println(err)
		return 1
	}
	pool, err = pgxpool.New(context.Background(), testDSN)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer pool.Close()

	natsDir, _ := os.MkdirTemp(tmpRoot, "nats-")
	defer os.RemoveAll(natsDir)
	natsSrv, err = natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: natsDir, NoLog: true, NoSigs: true})
	if err != nil {
		fmt.Println(err)
		return 1
	}
	natsSrv.Start()
	defer natsSrv.Shutdown()
	if !natsSrv.ReadyForConnections(10 * time.Second) {
		fmt.Println("nats 未就绪")
		return 1
	}

	var upub, apub ed25519.PublicKey
	upub, userPriv, _ = ed25519.GenerateKey(rand.Reader)
	apub, adminPriv, _ = ed25519.GenerateKey(rand.Reader)
	userKeys, _ = auth.ParseKeys("u1=" + base64.StdEncoding.EncodeToString(upub))
	adminKeys, _ = auth.ParseKeys("a1=" + base64.StdEncoding.EncodeToString(apub))
	return m.Run()
}

func createUTF8DB() error {
	ctx := context.Background()
	c, err := pgxpool.New(ctx, "postgres://postgres:postgres@127.0.0.1:19312/postgres?sslmode=disable")
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Exec(ctx, `CREATE DATABASE bike_cs ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0`)
	return err
}

// 固定的服务器时间：2026-12-01 11:40:00 +08:00（原型种子的"现在"附近）。
var testNow = time.Date(2026, 12, 1, 11, 40, 0, 0, CST)

type env struct {
	t     *testing.T
	app   *App
	world *fakes.World
	srv   *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE exited_operators, tickets, ticket_messages, ticket_actions, ticket_day_seq, platform_params, outbox, inbox, idempotency`); err != nil {
		t.Fatal(err)
	}
	w := fakes.NewWorld("svc-key")
	w.Seed()
	rideSrv := httptest.NewServer(w.Ride())
	billSrv := httptest.NewServer(w.Bill())
	acctSrv := httptest.NewServer(w.Acct())
	depSrv := httptest.NewServer(w.Deposit())
	t.Cleanup(func() { rideSrv.Close(); billSrv.Close(); acctSrv.Close(); depSrv.Close() })
	app := &App{
		DB: pool, Ride: ride.New(rideSrv.URL, "svc-key"), Bill: bill.New(billSrv.URL, "svc-key"),
		Acct: acct.New(acctSrv.URL, "svc-key"), Deposit: deposit.New(depSrv.URL, "svc-key"),
		UserKeys: userKeys, AdminKeys: adminKeys, Callers: auth.Callers{"ops": "ops-key"},
		Now: func() time.Time { return testNow }, DefaultRefundLimit: 2000,
	}
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, app: app, world: w, srv: srv}
}

func signTok(priv ed25519.PrivateKey, kid string, c jwt.MapClaims) string {
	c["iat"] = time.Now().Unix()
	c["exp"] = time.Now().Add(time.Hour).Unix()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tok.Header["kid"] = kid
	s, _ := tok.SignedString(priv)
	return s
}

func userTok(uid string) string {
	return signTok(userPriv, "u1", jwt.MapClaims{"iss": "bike-acct", "sub": uid})
}

func adminTok(id, name string, perms ...string) string {
	return signTok(adminPriv, "a1", jwt.MapClaims{"iss": "bike-iam", "sub": id, "name": name, "role": "客服", "client": "管理端", "perms": perms, "scope": nil, "readonly": false})
}

func adminTokWith(id string, scope any, readonly bool, perms ...string) string {
	return signTok(adminPriv, "a1", jwt.MapClaims{"iss": "bike-iam", "sub": id, "name": id, "perms": perms, "scope": scope, "readonly": readonly})
}

// 常用账号
var (
	staffPerms = []string{"order.view", "user.view", "refund.confirm", "order.forceEnd", "account.unfreeze", "deposit.assist", "account.cancel.assist"}
	staff      = func() string { return adminTok("AC0301", "客服甲", staffPerms...) }
	supervisor = func() string {
		return adminTok("AC0302", "客服主管乙", append(staffPerms, "refund.confirm.overLimit")...)
	}
)

type resp struct {
	status int
	body   map[string]any
	list   []any
	raw    []byte
}

func (e *env) do(method, path, token string, body any, idem string) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	raw, _ := io.ReadAll(r.Body)
	out := resp{status: r.StatusCode, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	_ = json.Unmarshal(raw, &out.list)
	return out
}

func (r resp) code() string { s, _ := r.body["code"].(string); return s }

func (e *env) expect(r resp, status int, code string) {
	e.t.Helper()
	if r.status != status || (code != "" && r.code() != code) {
		e.t.Fatalf("想要 %d %s，得到 %d %s", status, code, r.status, string(r.raw))
	}
}

// open 用户新建对话，返回工单号。
func (e *env) open(uid, orderID, entry, text string) string {
	e.t.Helper()
	body := map[string]any{"entry": entry, "text": text}
	if orderID != "" {
		body["orderId"] = orderID
	}
	r := e.do("POST", "/api/cs/tickets", userTok(uid), body, "")
	e.expect(r, 201, "")
	return r.body["ticketId"].(string)
}

func (e *env) outbox(subject string) []map[string]any {
	e.t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT body FROM outbox WHERE subject = $1 ORDER BY id`, subject)
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

func payload(ev map[string]any) map[string]any { p, _ := ev["payload"].(map[string]any); return p }

func mustMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("不是对象：%v", v)
	}
	return m
}

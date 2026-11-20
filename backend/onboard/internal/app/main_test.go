package app

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

	"bike/onboard/internal/auth"
	"bike/onboard/internal/clients/fleet"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// 测试用基础设施：embedded-postgres（端口 19304 = 18304 + 1000）与进程内 nats-server（只监听 127.0.0.1）。

var (
	testPool *pgxpool.Pool
	testNATS *natsserver.Server
	adminPub ed25519.PublicKey
	adminKey ed25519.PrivateKey
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	tmp, _ := filepath.Abs(filepath.Join("..", "..", ".tmp"))
	_ = os.MkdirAll(tmp, 0o755)
	_ = os.RemoveAll(filepath.Join(tmp, "pg-data"))
	_ = os.RemoveAll(filepath.Join(tmp, "pg-runtime"))
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).Port(19304).Database("bike_onboard").
		Locale("C").Encoding("SQL_ASCII"). // 项目路径含中文，默认编码初始化会失败（技术约定）
		RuntimePath(filepath.Join(tmp, "pg-runtime")).DataPath(filepath.Join(tmp, "pg-data")).
		Logger(io.Discard).StartTimeout(60 * time.Second))
	if err := pg.Start(); err != nil {
		fmt.Println("启动测试数据库失败:", err)
		return 1
	}
	defer func() { _ = pg.Stop() }()

	storeDir := filepath.Join(tmp, fmt.Sprintf("nats-%d", os.Getpid()))
	defer os.RemoveAll(storeDir)
	ns, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: storeDir, NoLog: true, NoSigs: true})
	if err != nil {
		fmt.Println("启动测试事件总线失败:", err)
		return 1
	}
	ns.Start()
	defer ns.Shutdown()
	if !ns.ReadyForConnections(10 * time.Second) {
		fmt.Println("测试事件总线未就绪")
		return 1
	}
	testNATS = ns

	ctx := context.Background()
	testPool, err = pgxpool.New(ctx, "postgres://postgres:postgres@127.0.0.1:19304/bike_onboard?sslmode=disable")
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer testPool.Close()
	if err := Migrate(ctx, testPool); err != nil {
		fmt.Println("迁移失败:", err)
		return 1
	}
	adminPub, adminKey, _ = ed25519.GenerateKey(rand.Reader)
	return m.Run()
}

func testConfig() Config {
	c := DefaultConfig()
	c.ReplyLimit = 300 * time.Millisecond
	c.CallTimeout = time.Second
	c.Grace = 400 * time.Millisecond
	c.LockWait = 1500 * time.Millisecond
	c.Poll = 20 * time.Millisecond
	return c
}

// fleetStub fleet 的替身，按 fleet.yaml 返回。
type fleetStub struct {
	calls  []string
	actors []string          // 每次调用的 actor.kind
	status map[string]string // brandId → 接入状态
	down   bool
}

func (f *fleetStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down {
			w.WriteHeader(500)
			return
		}
		f.calls = append(f.calls, r.URL.Path)
		var reqBody struct {
			Actor struct {
				Kind string `json:"kind"`
			} `json:"actor"`
		}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		f.actors = append(f.actors, reqBody.Actor.Kind)
		var brand, op string
		_, _ = fmt.Sscanf(r.URL.Path, "/internal/fleet/brands/%4s/", &brand)
		op = r.URL.Path[len("/internal/fleet/brands/")+len(brand)+1:]
		cur := f.status[brand]
		switch op {
		case "start-integration":
			if cur == "未接入" {
				f.status[brand] = "联调中"
			}
			writeJSON(w, 200, map[string]any{"brandId": brand, "accessStatus": f.status[brand], "statusSince": time.Now().Format(time.RFC3339)})
		case "go-live":
			if cur == "已接入" {
				writeJSON(w, 409, map[string]string{"code": "ALREADY_DONE", "message": "已是已接入"})
				return
			}
			if cur != "联调中" {
				writeJSON(w, 409, map[string]string{"code": "CONFLICT", "message": "品牌不在联调中"})
				return
			}
			f.status[brand] = "已接入"
			writeJSON(w, 200, map[string]any{"brand": map[string]any{"brandId": brand}, "enabledCodes": 60})
		default:
			w.WriteHeader(404)
		}
	})
}

type testEnv struct {
	t     *testing.T
	app   *App
	fleet *fleetStub
	srv   *httptest.Server // 本服务（联调入口要被运营商替身回调）
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	resetDB(t)
	fs := &fleetStub{status: map[string]string{}}
	fsrv := httptest.NewServer(fs.handler())
	t.Cleanup(fsrv.Close)
	callers, _ := auth.ParseCallers("ops=ops-secret")
	a := &App{DB: testPool, Fleet: fleet.New(fsrv.URL, "onboard-secret"), Admin: &auth.AdminVerifier{Keys: map[string]ed25519.PublicKey{"k1": adminPub}},
		Callers: callers, Cfg: testConfig()}
	srv := httptest.NewServer(a.Routes())
	t.Cleanup(srv.Close)
	return &testEnv{t: t, app: a, fleet: fs, srv: srv}
}

func resetDB(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `TRUNCATE integrations, status_history, credentials, sandbox_settings, fleet_vehicles, runs, run_cases,
		sandbox_orders, sandbox_deliveries, vehicle_submissions, sandbox_nonces, reports, idempotency, outbox, processed_events, exited_operators;
		UPDATE params SET real_vehicles=20, real_orders=60, target_days=21, unlock_timeout_sec=30, backfill_window_hours=72, changed_at=NULL;`)
	if err != nil {
		t.Fatal(err)
	}
}

// 测试账号取自原型 accounts.json：AC0114 公交集团车队技术对接（OP04），AC0403 运维接入负责人。
type who struct {
	id, name, role string
	perms          []string
	scope          any
	readonly       bool
}

var (
	techOP04 = who{"AC0114", "黎＊", "运营商技术对接", []string{"integration.doc", "integration.sandbox.own", "integration.conformance.own", "acceptance.view.own", "access.view.own"}, "OP04", false}
	techOP01 = who{"AC0111", "邓＊", "运营商技术对接", []string{"integration.doc", "integration.sandbox.own", "integration.conformance.own", "acceptance.view.own", "access.view.own"}, "OP01", false}
	exited   = who{"AC0103", "韦＊＊", "运营商业务", []string{"integration.sandbox.own", "acceptance.view.own"}, "OP03", true}
	leadOps  = who{"AC0403", "毛＊＊", "运维接入负责人", []string{"ops.monitor", "acceptance.confirm"}, nil, false}
	dutyOps  = who{"AC0401", "贺＊＊", "运维值班", []string{"ops.monitor"}, nil, false}
)

func token(t *testing.T, w who) string {
	t.Helper()
	now := time.Now().Unix()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "bike-iam", "sub": w.id, "name": w.name, "role": w.role, "client": "管理端",
		"perms": w.perms, "scope": w.scope, "readonly": w.readonly, "iat": now, "exp": now + 3600})
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(adminKey)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// call 以某个账号调本服务的页面接口。
func (e *testEnv) call(w who, method, path string, body any, hdr ...string) (int, map[string]any, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if w.id != "" {
		req.Header.Set("Authorization", "Bearer "+token(e.t, w))
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, raw
}

// event 直接投递一条订阅事件（不经总线）。
func (e *testEnv) event(typ string, actor Actor, payload any) {
	e.t.Helper()
	p, _ := json.Marshal(payload)
	env := Envelope{EventID: newEventID(), Type: typ, Version: 1, OccurredAt: time.Now(), Key: "k", Actor: actor, Payload: p}
	b, _ := json.Marshal(env)
	if err := e.app.HandleMessage(context.Background(), b); err != nil {
		e.t.Fatal(err)
	}
}

var acAccess = Actor{Kind: "人", ID: "AC0404", Name: "郝＊"}

// registerBR05 原型样例：市民骑行（BR05，OP04 公交集团车队），登记后未接入。
func (e *testEnv) registerBR05() {
	e.fleet.status["BR05"] = "未接入"
	e.event("fleet.brand.registered", acAccess, map[string]any{"brandId": "BR05", "operatorId": "OP04", "name": "市民骑行", "displayName": "【市民骑行】电单车",
		"operatorName": "公交集团车队", "operatorShortName": "公交车队", "operatorType": "政府自有车队", "vehicleType": "电单车", "color": "#2f7d32", "accessStatus": "未接入"})
}

// outboxTypes 发件表里的事件类型。
func outboxTypes(t *testing.T) []string {
	t.Helper()
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

func lastAudit(t *testing.T) map[string]any {
	t.Helper()
	var body []byte
	if err := testPool.QueryRow(context.Background(), `SELECT body FROM outbox WHERE subject='onboard.audit.recorded' ORDER BY id DESC LIMIT 1`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	_ = json.Unmarshal(body, &env)
	return env
}

func natsConn(t *testing.T) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(testNATS.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

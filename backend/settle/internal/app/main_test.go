package app

import (
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
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats-server/v2/server"

	"bike/settle/internal/auth"
	"bike/settle/migrations"
)

// 测试用真库：embedded-postgres 端口 = 服务端口 18311 + 1000；临时文件放本目录 .tmp/。
// 测试用事件总线：进程内 nats-server，只监听 127.0.0.1，随机端口。

var (
	testPool *pgxpool.Pool
	natsURL  string
	privKey  ed25519.PrivateKey
	verifier *auth.Verifier
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	root, _ := filepath.Abs(filepath.Join("..", "..", ".tmp"))
	_ = os.MkdirAll(root, 0o755)
	_ = os.RemoveAll(filepath.Join(root, "pg-data"))
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).Port(19311).Locale("C").Encoding("SQL_ASCII").
		RuntimePath(filepath.Join(root, "pg-runtime")).DataPath(filepath.Join(root, "pg-data")).
		Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Println("启动 embedded-postgres 失败:", err)
		return 1
	}
	defer pg.Stop() //nolint:errcheck

	natsDir, _ := os.MkdirTemp(root, "nats-")
	defer os.RemoveAll(natsDir)
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: natsDir, NoLog: true, NoSigs: true})
	if err != nil {
		fmt.Println("启动 nats 失败:", err)
		return 1
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		fmt.Println("nats 未就绪")
		return 1
	}
	defer ns.Shutdown()
	natsURL = ns.ClientURL()

	ctx := context.Background()
	testPool, err = pgxpool.New(ctx, "postgres://postgres:postgres@127.0.0.1:19311/postgres?sslmode=disable")
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer testPool.Close()
	if err := Migrate(ctx, testPool, migrations.FS); err != nil {
		fmt.Println("迁移失败:", err)
		return 1
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	privKey = priv
	verifier = auth.NewVerifier(map[string]ed25519.PublicKey{"k1": pub})
	return m.Run()
}

// clock 是可调的测试时钟。
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(s string)   { c.mu.Lock(); defer c.mu.Unlock(); c.t = mustT(s) }

func mustT(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// newApp 清空全部表并返回使用测试时钟的 App。
func newApp(t *testing.T, now string) (*App, *clock) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `TRUNCATE processed_events, outbox, idempotency, brands, operator_exits, versions, orders, refunds,
		card_purchases, order_unlock, order_pay, settle_months, settlements, settlement_lines, adjustments, adjustment_items, order_settle, disputes, corrections, seqs`)
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{}
	c.Set(now)
	return New(testPool, verifier, slog.New(slog.NewTextHandler(io.Discard, nil)), c.Now), c
}

// ---------------- 事件构造 ----------------

var evSeq int
var evMu sync.Mutex

func feed(t *testing.T, a *App, typ string, payload any) {
	t.Helper()
	evMu.Lock()
	evSeq++
	id := fmt.Sprintf("test-%d-%d", time.Now().UnixNano(), evSeq)
	evMu.Unlock()
	b, _ := json.Marshal(payload)
	if err := a.HandleEvent(context.Background(), Envelope{EventID: id, Type: typ, Version: 1, OccurredAt: time.Now(), Actor: Actor{Kind: "系统", ID: "x"}, Payload: b}); err != nil {
		t.Fatalf("处理 %s 失败: %v", typ, err)
	}
}

func feedBrands(t *testing.T, a *App) {
	feed(t, a, "fleet.brand.registered", map[string]any{"brandId": "BR01", "operatorId": "OP01", "name": "青禾", "operatorName": "青禾出行科技有限公司", "operatorShortName": "青禾出行"})
	feed(t, a, "fleet.brand.registered", map[string]any{"brandId": "BR02", "operatorId": "OP01", "name": "木棉", "operatorName": "青禾出行科技有限公司", "operatorShortName": "青禾出行"})
	feed(t, a, "fleet.brand.registered", map[string]any{"brandId": "BR03", "operatorId": "OP02", "name": "驰路", "operatorName": "驰路智行有限公司", "operatorShortName": "驰路智行"})
}

func feedVersions(t *testing.T, a *App) {
	v := func(id, eff string, br02 float64) map[string]any {
		return map[string]any{"versionId": id, "name": id, "effectiveAt": eff, "previousVersionId": nil,
			"ride": map[string]any{"vehicleType": "电单车", "startFee": 150, "startMinutes": 3, "perMinute": 50}, "dispatchFee": 500,
			"monthCard":  map[string]any{"price": 2500, "days": 30, "perOrderCap": 300, "allocation": "按骑行单数"},
			"serviceFee": map[string]any{"basis": "订单应收", "ratios": map[string]any{"BR01": 0.62, "BR02": br02, "BR03": 0.6}}}
	}
	feed(t, a, "rule.version.published", v("PR0001", "2026-07-01T00:00:00+08:00", 0.58))
	feed(t, a, "rule.version.published", v("PR0002", "2026-10-15T00:00:00+08:00", 0.6))
}

// ride 结束事件（载荷带 userId，settle 不落库）。
func ended(orderID, brand, op, ver, req, end, received string, minutes int) map[string]any {
	e := mustT(end)
	return map[string]any{"orderId": orderID, "userId": "U100004", "vehicleId": "VH000004", "plateNo": "QH000004", "brandId": brand, "operatorId": op,
		"channel": "微信", "pricingVersionId": ver, "requestAt": req, "unlockResult": "成功", "startAt": e.Add(-time.Duration(minutes) * time.Minute).Format(time.RFC3339),
		"endAt": end, "durationSec": minutes * 60, "distanceKm": 2.6, "startLocation": []float64{104.05, 30.6}, "endLocation": []float64{104.06, 30.61},
		"startStationId": nil, "endStationId": "ST0026", "endStationName": "天府广场站", "endPileId": nil, "endMethod": "用户锁车", "inStation": true,
		"inStationBasis": "车端判定", "distanceToStationM": nil, "dispatchPromptShown": false, "lockReportAt": end, "pileInAt": nil,
		"reportReceivedAt": received, "lateReport": mustT(received).Sub(e) > time.Hour, "degraded": nil, "forceEnd": nil}
}

func issued(orderID, brand, op, ver string, minutes int, receivable, cardDeduct int64) map[string]any {
	return map[string]any{"orderId": orderID, "userId": "U100004", "brandId": brand, "operatorId": op, "pricingVersionId": ver, "billedMinutes": minutes,
		"rideFee": receivable, "dispatchFee": 0, "receivable": receivable, "monthCardDeduct": cardDeduct, "monthCardId": nil, "couponDeduct": 0,
		"payable": receivable - cardDeduct, "collectBy": "免押自动扣款", "payDeadline": nil, "billPushedAt": "2026-10-02T08:20:10+08:00",
		"orderEndAt": "2026-10-02T08:20:00+08:00", "degraded": false}
}

// ---------------- HTTP 帮助 ----------------

type tokenSpec struct {
	sub, role, scope string
	perms            []string
	readonly         bool
}

var (
	finance  = tokenSpec{sub: "AC0201", role: "财务", perms: []string{"settlement.manage", "settlement.adjust", "dispute.decide", "order.view"}}
	operator = tokenSpec{sub: "AC0101", role: "运营商业务", scope: "OP01", perms: []string{"order.view.own", "order.export.own", "settlement.view.own", "settlement.export.own", "dispute.submit.own"}}
	op2      = tokenSpec{sub: "AC0102", role: "运营商业务", scope: "OP02", perms: []string{"order.view.own", "settlement.view.own", "dispute.submit.own"}}
	exitedOp = tokenSpec{sub: "AC0103", role: "运营商业务", scope: "OP01", readonly: true, perms: []string{"settlement.view.own", "dispute.submit.own"}}
	cs       = tokenSpec{sub: "AC0301", role: "客服", perms: []string{"user.view"}}
)

func token(s tokenSpec) string {
	var scope any
	if s.scope != "" {
		scope = s.scope
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "bike-iam", "sub": s.sub, "name": s.sub, "role": s.role, "client": "管理端",
		"perms": s.perms, "scope": scope, "readonly": s.readonly, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	tok.Header["kid"] = "k1"
	str, _ := tok.SignedString(privKey)
	return str
}

type resp struct {
	code   int
	body   []byte
	header http.Header
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("解析响应失败: %v; body=%s", err, r.body)
	}
}

func (r resp) errCode(t *testing.T) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Code
}

func call(t *testing.T, h http.Handler, method, path string, who *tokenSpec, body any, idem string) resp {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, rd)
	if who != nil {
		req.Header.Set("Authorization", "Bearer "+token(*who))
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return resp{code: w.Code, body: w.Body.Bytes(), header: w.Header()}
}

func outboxTypes(t *testing.T) map[string]int {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT subject, count(*) FROM outbox GROUP BY subject`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		_ = rows.Scan(&s, &n)
		m[s] = n
	}
	return m
}

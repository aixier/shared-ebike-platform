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

	"bike/bill/internal/auth"
	"bike/bill/internal/clients/deposit"
	"bike/bill/internal/clients/pay"
)

// 测试库：embedded-postgres，端口 = 18308 + 1000，运行与数据目录放本模块 .tmp/
var testDSN = "postgres://bill:bill@127.0.0.1:19308/bike_bill?sslmode=disable"

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	root, _ := filepath.Abs("../../.tmp")
	_ = os.MkdirAll(root, 0o755)
	_ = os.RemoveAll(filepath.Join(root, "pgdata"))
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).Port(19308).Locale("C").Encoding("SQL_ASCII").Username("bill").Password("bill").Database("bike_bill").
		RuntimePath(filepath.Join(root, "pgrt")).DataPath(filepath.Join(root, "pgdata")).
		StartTimeout(60 * time.Second).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Println("启动测试库失败：", err)
		os.Exit(1)
	}
	code := func() int {
		pool, err := OpenDB(context.Background(), testDSN)
		if err != nil {
			fmt.Println("迁移失败：", err)
			return 1
		}
		testPool = pool
		defer pool.Close()
		return m.Run()
	}()
	_ = pg.Stop()
	os.Exit(code)
}

/* ---------------- 替身：按 pay、deposit 的 swagger 返回 ---------------- */

type payCall struct {
	Path string
	Idem string
	Body map[string]any
}

// fakePay pay 替身：按 path 给结果；同一幂等键返回首次结果
type fakePay struct {
	mu      sync.Mutex
	calls   []payCall
	results map[string]pay.Result // path → 结果
	byIdem  map[string]pay.Result
	n       int
}

func newFakePay() *fakePay {
	return &fakePay{results: map[string]pay.Result{}, byIdem: map[string]pay.Result{}}
}

func (f *fakePay) set(path string, r pay.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[path] = r
}

func (f *fakePay) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Path == path {
			n++
		}
	}
	return n
}

func (f *fakePay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Caller") != "bill" || r.Header.Get("Authorization") != "Bearer bill-key" || r.Header.Get("Idempotency-Key") == "" {
		w.WriteHeader(401)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	idem := r.Header.Get("Idempotency-Key")
	path := r.URL.Path
	if path == pay.PaymentsPath { // 第二轮的发起支付路径与旧的一键支付路径同一份结果
		path = pay.LegacyOneTapPath
	}
	f.calls = append(f.calls, payCall{Path: path, Idem: idem, Body: body})
	if res, ok := f.byIdem[idem]; ok {
		_ = json.NewEncoder(w).Encode(res)
		return
	}
	res, ok := f.results[path]
	if !ok {
		res = pay.Result{Result: "成功"}
	}
	f.n++
	if res.PaymentID == "" {
		res.PaymentID = fmt.Sprintf("PY%06d", f.n)
	}
	if amt, ok := body["amount"].(float64); ok && res.Amount == 0 {
		res.Amount = int64(amt)
	}
	f.byIdem[idem] = res
	_ = json.NewEncoder(w).Encode(res)
}

// fakeDeposit deposit 替身：余额抵多少算多少
type fakeDeposit struct {
	mu      sync.Mutex
	balance int64
	result  string
	calls   int
}

func (f *fakeDeposit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var cmd deposit.DeductCommand
	_ = json.NewDecoder(r.Body).Decode(&cmd)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	res := deposit.DeductResult{Result: f.result, UserID: cmd.UserID, OrderID: cmd.OrderID}
	if res.Result == "" {
		res.Result = "成功"
	}
	if res.Result == "成功" {
		d := cmd.Amount
		if f.balance < d {
			d = f.balance
		}
		f.balance -= d
		res.Deducted, res.Shortfall, res.BalanceAfter = d, cmd.Amount-d, f.balance
	}
	if res.Result == "失败" {
		c := "NO_BALANCE"
		res.FailCode = &c
		res.Shortfall = cmd.Amount
	}
	_ = json.NewEncoder(w).Encode(res)
}

/* ---------------- 测试环境 ---------------- */

type env struct {
	t        *testing.T
	a        *App
	pay      *fakePay
	dep      *fakeDeposit
	srv      *httptest.Server
	payBase  string
	clock    time.Time
	userPriv ed25519.PrivateKey
	admPriv  ed25519.PrivateKey
}

var cst = time.FixedZone("CST", 8*3600)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, cst)
	if err != nil {
		panic(err)
	}
	return t
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `TRUNCATE pricing_versions, platform_params, user_profiles, receipts, refunds, bills, month_cards, coupons,
		arrears_overdue, outbox, inbox, idempotency, order_unlock_states, exited_operators, coupon_batches, coupon_grants`); err != nil {
		t.Fatal(err)
	}
	up, upriv, _ := ed25519.GenerateKey(rand.Reader)
	ap, apriv, _ := ed25519.GenerateKey(rand.Reader)
	fp := newFakePay()
	fd := &fakeDeposit{}
	ps := httptest.NewServer(fp)
	ds := httptest.NewServer(fd)
	t.Cleanup(ps.Close)
	t.Cleanup(ds.Close)
	e := &env{t: t, pay: fp, dep: fd, payBase: ps.URL, clock: at("2026-12-01 12:00:00"), userPriv: upriv, admPriv: apriv}
	callers, _ := auth.ParseCallers("cs=cs-key")
	e.a = New(testPool, pay.New(ps.URL, "bill-key"), deposit.New(ds.URL, "bill-key"),
		auth.Verifier{UserKeys: map[string]ed25519.PublicKey{"u1": up}, AdminKeys: map[string]ed25519.PublicKey{"a1": ap}}, callers,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.a.Now = func() time.Time { return e.clock }
	e.srv = httptest.NewServer(e.a.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

// deliver 把一条事件交给消费处理（与总线消费者同一入口）
func (e *env) deliver(typ string, payload any) error {
	return e.deliverID(newID(), typ, payload)
}

func (e *env) deliverID(id, typ string, payload any) error {
	p, _ := json.Marshal(payload)
	body, _ := json.Marshal(Envelope{EventID: id, Type: typ, Version: 1, OccurredAt: e.clock, Key: "k", Actor: SystemActor, Payload: p})
	return Process(context.Background(), testPool, e.a.HandleEvent, body)
}

func (e *env) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

// PR0002 / PR0003 价格（分）；PR0003 带领取券与开锁失败关怀券
func (e *env) seedRules() {
	tpls := []map[string]any{
		{"templateId": "CPT01", "name": "新用户体验券", "amount": 200, "validDays": 7, "issue": "注册后自动发放", "perUser": 1},
		{"templateId": "CPT02", "name": "周末骑行券", "amount": 200, "validDays": 3, "issue": "用户领券中心领取（每周五 17:00 开放）", "perUser": 1},
		{"templateId": "CPT04", "name": "开锁失败关怀券", "amount": 100, "validDays": 7, "issue": "开锁失败后自动发放", "perUser": 1},
	}
	for _, v := range []struct {
		id, eff string
		per     int64
	}{{"PR0001", "2026-07-01 00:00:00", 40}, {"PR0002", "2026-10-15 00:00:00", 50}} {
		e.must(e.deliver("rule.version.published", map[string]any{
			"versionId": v.id, "name": v.id, "effectiveAt": at(v.eff), "previousVersionId": nil,
			"ride":        map[string]any{"vehicleType": "电单车", "startFee": 150, "startMinutes": 3, "perMinute": v.per},
			"dispatchFee": 500, "monthCard": map[string]any{"price": 2500, "days": 30, "perOrderCap": 300, "allocation": "按骑行单数"},
			"coupons": tpls, "deposit": map[string]any{"amount": 19900},
		}))
	}
	e.must(e.deliver("rule.params.changed", map[string]any{"payWindowHours": 24, "arrearsFreezeDays": 30, "csRefundLimit": 2000, "changedAt": at("2026-10-10 11:20:00")}))
}

func (e *env) depositMode(user, mode string, status *string, balance *int64) {
	e.must(e.deliver("deposit.status.changed", map[string]any{"userId": user, "mode": mode, "status": status, "balance": balance, "rideable": true, "changedAt": e.clock}))
}

func (e *env) realname(user, st string) {
	e.must(e.deliver("acct.realname.changed", map[string]any{"userId": user, "status": st, "afterDegrade": false}))
}

type endOpt struct {
	order, user, version, unlock string
	minutes                      int64
	inStation                    bool
	degraded                     bool
	dist                         *int
}

func (e *env) endOrder(o endOpt) error {
	if o.version == "" {
		o.version = "PR0002"
	}
	if o.unlock == "" {
		o.unlock = "成功"
	}
	start := e.clock.Add(-time.Duration(o.minutes) * time.Minute)
	var dg any
	if o.degraded {
		dg = map[string]any{"eventId": "OPS-DG-1", "admission": "先骑后验新用户", "limitMinutes": 30, "limitAmount": 1500}
	}
	return e.deliver("ride.order.ended", map[string]any{
		"orderId": o.order, "userId": o.user, "vehicleId": "VH000004", "plateNo": "A1", "brandId": "BR01", "operatorId": "OP01", "channel": "微信",
		"pricingVersionId": o.version, "requestAt": start, "unlockResult": o.unlock, "startAt": start, "endAt": e.clock, "durationSec": o.minutes * 60,
		"inStation": o.inStation, "inStationBasis": "车端判定", "distanceToStationM": o.dist, "endMethod": "用户锁车", "reportReceivedAt": e.clock,
		"lateReport": false, "degraded": dg, "forceEnd": nil,
	})
}

func (e *env) bill(order string) *Bill {
	e.t.Helper()
	b, err := getBill(context.Background(), testPool, `order_id=$1`, order)
	if err != nil {
		e.t.Fatalf("取账单 %s：%v", order, err)
	}
	return b
}

// events 发件表里某类型的载荷
func (e *env) events(typ string) []map[string]any {
	rows, err := testPool.Query(context.Background(), `SELECT body FROM outbox WHERE subject=$1 ORDER BY id`, typ)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		_ = rows.Scan(&raw)
		var env Envelope
		_ = json.Unmarshal(raw, &env)
		var p map[string]any
		_ = json.Unmarshal(env.Payload, &p)
		out = append(out, p)
	}
	return out
}

func (e *env) userToken(user string) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "bike-acct", "sub": user, "iat": e.clock.Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	tok.Header["kid"] = "u1"
	s, _ := tok.SignedString(e.userPriv)
	return s
}

func (e *env) adminToken(perms []string, scope any) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "bike-iam", "sub": "AC0902", "name": "财务", "role": "财务", "client": "管理端",
		"perms": perms, "scope": scope, "readonly": false, "exp": time.Now().Add(time.Hour).Unix()})
	tok.Header["kid"] = "a1"
	s, _ := tok.SignedString(e.admPriv)
	return s
}

// call 发请求；token 以 "Bearer " 外的形式给出；hdr 追加请求头
func (e *env) call(method, path, token string, body any, hdr map[string]string) (int, map[string]any, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
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

func ptr[T any](v T) *T { return &v }

func (e *env) payURL() string { return e.payBase }

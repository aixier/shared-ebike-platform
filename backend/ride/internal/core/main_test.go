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
	"log/slog"
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

	"bike/ride/internal/auth"
	"bike/ride/internal/clients/opgw"
	"bike/ride/internal/journal"
)

// 测试用数据库：embedded-postgres，端口 = 18302 + 1000。
const testDSN = "postgres://postgres:postgres@127.0.0.1:19302/postgres?sslmode=disable"

var testPool *pgxpool.Pool
var tmpRoot string

func TestMain(m *testing.M) {
	wd, _ := os.Getwd()
	tmpRoot = filepath.Join(wd, "..", "..", ".tmp")
	_ = os.MkdirAll(tmpRoot, 0o755)
	_ = os.RemoveAll(filepath.Join(tmpRoot, "pg-data"))
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).Port(19302).
		RuntimePath(filepath.Join(tmpRoot, "pg-runtime")).DataPath(filepath.Join(tmpRoot, "pg-data")).
		Locale("C").Encoding("SQL_ASCII").Logger(io.Discard).StartTimeout(60 * time.Second))
	if err := pg.Start(); err != nil {
		fmt.Println("embedded-postgres 启动失败：", err)
		os.Exit(1)
	}
	code := func() int {
		ctx := context.Background()
		p, err := pgxpool.New(ctx, testDSN)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		defer p.Close()
		if err := Migrate(ctx, p); err != nil {
			fmt.Println("迁移失败：", err)
			return 1
		}
		testPool = p
		return m.Run()
	}()
	_ = pg.Stop()
	os.Exit(code)
}

// ---------------- 运营商（opgw）替身 ----------------

// fakeOpgw 按 opgw.yaml 返回；收到开锁命令后按设定的行为异步送达开锁报告。
type fakeOpgw struct {
	mu        sync.Mutex
	svc       *Service
	unlock    string        // 成功｜失败｜拒绝｜无报告｜晚到成功｜不可达
	reason    string        // 失败或拒绝的原因
	delay     time.Duration // 报告延迟
	endReply  string        // 结束骑行的同步答复
	lockOnEnd *lockSpec     // 收到结束命令后送达锁车报告
	calls     []string
	srv       *httptest.Server
	seq       int
}

type lockSpec struct {
	loc  [2]float64
	side *bool
}

func newFakeOpgw() *fakeOpgw {
	f := &fakeOpgw{unlock: "成功", endReply: "已受理", delay: 50 * time.Millisecond}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *fakeOpgw) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Caller") != "ride" || r.Header.Get("Idempotency-Key") == "" {
		w.WriteHeader(400)
		return
	}
	var cmd map[string]any
	_ = json.NewDecoder(r.Body).Decode(&cmd)
	oid, _ := cmd["orderId"].(string)
	f.mu.Lock()
	f.calls = append(f.calls, r.URL.Path+"|"+oid+"|"+fmt.Sprint(cmd["cause"]))
	mode, reason, delay, endReply, lock := f.unlock, f.reason, f.delay, f.endReply, f.lockOnEnd
	f.mu.Unlock()
	now := time.Now().In(cst).Format(time.RFC3339)
	if r.URL.Path == "/internal/opgw/unlock" {
		switch mode {
		case "拒绝":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "拒绝", "rejectReason": reason, "at": now})
			return
		case "不可达":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "不可达", "at": now})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "已受理", "at": now})
		if mode == "无报告" {
			return
		}
		go func() {
			time.Sleep(delay)
			res := "成功"
			var rs *string
			if mode == "失败" {
				res, rs = "失败", &reason
			}
			e := &RideEvent{OperatorEventID: f.nextID(), OrderID: &oid, PlateNo: cmd["plateNo"].(string), BrandID: cmd["brandId"].(string),
				OperatorID: cmd["operatorId"].(string), Kind: "开锁结果", ReceivedAt: f.svc.now()}
			e.Unlock = &struct {
				Result string    `json:"result"`
				Reason *string   `json:"reason"`
				At     time.Time `json:"at"`
			}{res, rs, f.svc.now()}
			_, _ = f.svc.DeliverRideEvent(context.Background(), e)
		}()
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"result": endReply, "at": now})
	if lock != nil && endReply == "已受理" {
		go func() {
			time.Sleep(delay)
			e := lockEvent(f.nextID(), oid, cmd["plateNo"].(string), f.svc.now(), lock.loc, lock.side)
			_, _ = f.svc.DeliverRideEvent(context.Background(), e)
		}()
	}
}

func (f *fakeOpgw) nextID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	return fmt.Sprintf("OPE%d-%d", time.Now().UnixNano(), f.seq)
}

func (f *fakeOpgw) set(fn func(f *fakeOpgw)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeOpgw) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func lockEvent(id, orderID, plate string, at time.Time, loc [2]float64, side *bool) *RideEvent {
	e := &RideEvent{OperatorEventID: id, PlateNo: plate, BrandID: "BR01", OperatorID: "OP01", Kind: "锁车", ReceivedAt: at}
	if orderID != "" {
		e.OrderID = &orderID
	}
	b, _ := json.Marshal(map[string]any{"at": at, "location": loc, "vehicleSideInStation": side, "distanceKm": 1.26})
	_ = json.Unmarshal([]byte(`{"lock":`+string(b)+`}`), e)
	return e
}

// ---------------- 测试环境 ----------------

type testEnv struct {
	t     *testing.T
	svc   *Service
	og    *fakeOpgw
	v     *auth.Verifier
	srv   *httptest.Server
	priv  ed25519.PrivateKey
	clock *testClock
	jdir  string
}

type testClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.offset += d
	c.mu.Unlock()
}

func resetDB(t *testing.T) {
	_, err := testPool.Exec(context.Background(), `TRUNCATE snap, orders, order_day_seq, ride_reports, outbox, inbox, idem`)
	if err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	resetDB(t)
	jdir := filepath.Join(tmpRoot, "journal-"+t.Name()+fmt.Sprint(time.Now().UnixNano()))
	t.Cleanup(func() { os.RemoveAll(jdir) })
	j, err := journal.Open(jdir)
	if err != nil {
		t.Fatal(err)
	}
	og := newFakeOpgw()
	t.Cleanup(og.srv.Close)
	clock := &testClock{}
	svc := New(testPool, NewSnapshot(), j, opgw.New(og.srv.URL, "key-ride"), Options{InstanceID: "test-1", PollInterval: 20 * time.Millisecond,
		UnlockTimeoutMin: 800 * time.Millisecond, ReturnWait: 600 * time.Millisecond, PileNoLockWait: time.Second, SnapshotRefresh: 50 * time.Millisecond},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetClock(clock.now)
	og.svc = svc
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys, _ := auth.ParseKeys("k1=" + base64.StdEncoding.EncodeToString(pub))
	v := &auth.Verifier{UserKeys: keys, AdminKeys: keys, Callers: auth.ParseCallers("cs=key-cs,ops=key-ops,opgw=key-opgw")}
	srv := httptest.NewServer(svc.Handler(v))
	t.Cleanup(srv.Close)
	e := &testEnv{t: t, svc: svc, og: og, v: v, srv: srv, priv: priv, clock: clock, jdir: jdir}
	e.seed()
	return e
}

// ev 投递一条订阅事件。
func (e *testEnv) ev(typ string, payload map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(payload)
	env := Envelope{EventID: newEventID(), Type: typ, Version: 1, OccurredAt: time.Now(), Actor: systemActor, Payload: b}
	if err := e.svc.HandleEvent(context.Background(), env); err != nil {
		e.t.Fatalf("处理事件 %s 失败：%v", typ, err)
	}
}

func sq(x, y float64) [][2]float64 {
	return [][2]float64{{x, y}, {x + 0.001, y}, {x + 0.001, y + 0.001}, {x, y + 0.001}, {x, y}}
}

// seed 原型样例转成的测试数据（SCHEMA 第 7 节、API.md 3.1）。
func (e *testEnv) seed() {
	e.ev("rule.version.published", map[string]any{"versionId": "PR0003", "name": "现行", "effectiveAt": "2026-01-01T00:00:00+08:00",
		"ride": map[string]any{"vehicleType": "电单车", "startFee": 150, "startMinutes": 3, "perMinute": 50}, "dispatchFee": 500,
		"monthCard": map[string]any{"price": 2500, "days": 30, "perOrderCap": 300}, "deposit": map[string]any{"amount": 19900},
		"creditFree": map[string]any{"threshold": 550}, "riskLimits": map[string]any{"perUserOpenDegradedOrders": 1,
			"normal": map[string]any{"maxMinutes": 120, "maxAmount": 6000}, "tight": map[string]any{"maxMinutes": 30, "maxAmount": 1500}, "hourlyDegradedOrders": 200}})
	e.ev("fleet.brand.registered", map[string]any{"brandId": "BR01", "operatorId": "OP01", "name": "青禾", "displayName": "【青禾】电单车",
		"operatorName": "青禾出行科技有限公司", "operatorShortName": "青禾出行", "vehicleType": "电单车", "color": "#2BA471", "accessStatus": "已接入"})
	e.ev("fleet.brand.registered", map[string]any{"brandId": "BR02", "operatorId": "OP01", "name": "木棉", "displayName": "【木棉】电单车",
		"operatorShortName": "青禾出行", "vehicleType": "电单车", "color": "#E34D59", "accessStatus": "暂停可租"})
	e.ev("fleet.brand.registered", map[string]any{"brandId": "BR05", "operatorId": "OP04", "name": "市民骑行", "displayName": "【市民骑行】电单车", "accessStatus": "联调中"})
	for _, v := range []map[string]any{
		{"vehicleId": "VH000004", "brandId": "BR01", "operatorId": "OP01", "plateNo": "QH0004", "govCode": "SF86020226", "model": "Q3", "codeStatus": "已启用"},
		{"vehicleId": "VH000005", "brandId": "BR01", "operatorId": "OP01", "plateNo": "QH0005", "govCode": "SF86020227", "model": "Q3", "codeStatus": "已启用"},
		{"vehicleId": "VH000481", "brandId": "BR02", "operatorId": "OP01", "plateNo": "MM0481", "govCode": "SF50000481", "model": "M1", "codeStatus": "已启用"},
		{"vehicleId": "VH001061", "brandId": "BR01", "operatorId": "OP01", "plateNo": "JT1061", "govCode": "SF71009215", "model": "J1", "codeStatus": "已启用"},
		{"vehicleId": "VH001151", "brandId": "BR05", "operatorId": "OP04", "plateNo": "SM1151", "govCode": "SF27239795", "model": "S1", "codeStatus": "待启用", "codeEnableAt": "2026-12-08T09:00:00+08:00"},
	} {
		e.ev("fleet.vehicle.listed", v)
	}
	e.ev("fleet.code.status_changed", map[string]any{"vehicleId": "VH001061", "brandId": "BR01", "govCode": "SF71009215", "from": "已启用", "to": "已失效", "changedAt": "2026-11-01T00:00:00+08:00"})
	e.ev("site.station.changed", map[string]any{"stationId": "ST0001", "name": "天府广场东", "district": "锦江区", "type": "有桩", "center": [2]float64{104.0005, 30.0005}, "fence": sq(104.0, 30.0), "capacity": 4, "status": "启用"})
	e.ev("site.station.changed", map[string]any{"stationId": "ST0002", "name": "春熙路", "district": "锦江区", "type": "无桩", "center": [2]float64{104.0105, 30.0005}, "fence": sq(104.01, 30.0), "capacity": 10, "status": "启用"})
	e.ev("site.pile.status_changed", map[string]any{"pileId": "PL00001", "stationId": "ST0001", "no": 1, "location": [2]float64{104.0002, 30.0002}, "status": "在线"})
	e.ev("site.pile.status_changed", map[string]any{"pileId": "PL00002", "stationId": "ST0001", "no": 2, "location": [2]float64{104.0004, 30.0002}, "status": "在线"})
	users := []struct {
		id   string
		evts [][2]any
	}{
		{"U100341", [][2]any{{"acct.realname.changed", map[string]any{"status": "已核验"}}, {"deposit.status.changed", map[string]any{"mode": "免押", "rideable": true}}}},
		{"U100004", [][2]any{{"acct.realname.changed", map[string]any{"status": "已核验"}}, {"deposit.status.changed", map[string]any{"mode": "押金", "status": "已缴", "amountPaid": 9900, "balance": 9900, "rideable": true}}}},
		{"U100036", [][2]any{{"acct.realname.changed", map[string]any{"status": "已核验"}}, {"deposit.status.changed", map[string]any{"mode": "免押", "rideable": true}}, {"bill.arrears.created", map[string]any{"orderId": "OD2026090410000", "amount": 1100, "userArrearsTotal": 1100, "cause": "扣款失败"}}}},
		{"U100087", [][2]any{{"acct.realname.changed", map[string]any{"status": "已核验"}}}},
		{"U100127", nil},
		{"U100026", [][2]any{{"acct.realname.changed", map[string]any{"status": "待核验"}}}},
		{"U100712", [][2]any{{"acct.realname.changed", map[string]any{"status": "已核验"}}, {"acct.account.frozen", map[string]any{"reason": "欠费追缴", "unfreezeHow": "补付欠费后自动解冻"}}}},
		{"U100054", [][2]any{{"acct.realname.changed", map[string]any{"status": "已核验"}}, {"deposit.status.changed", map[string]any{"mode": "押金", "status": "押金不足", "amountPaid": 9900, "balance": 9600, "rideable": false}}}},
		{"U101097", [][2]any{{"acct.realname.changed", map[string]any{"status": "已核验"}}, {"deposit.status.changed", map[string]any{"mode": "押金", "status": "退押中", "amountPaid": 9900, "balance": 9900, "rideable": false}}}},
		{"U100598", [][2]any{{"acct.account.cancelled", map[string]any{"cancelledAt": "2026-11-01T00:00:00+08:00"}}}},
	}
	for _, u := range users {
		e.ev("acct.user.registered", map[string]any{"userId": u.id, "channel": "微信", "registeredAt": "2026-09-01T00:00:00+08:00", "duringDegrade": false})
		for _, x := range u.evts {
			p := x[1].(map[string]any)
			p["userId"] = u.id
			e.ev(x[0].(string), p)
		}
	}
	// 车辆最后一次锁车位置（原型 vehicles.json 的位置），附近车据此推算
	for vid, loc := range map[string][2]float64{"VH000004": {104.0005, 30.0005}, "VH000005": {104.0006, 30.0006}, "VH000481": {104.0007, 30.0004}, "VH001061": {104.0004, 30.0004}, "VH001151": {104.0003, 30.0003}} {
		_, err := testPool.Exec(context.Background(), `UPDATE snap SET doc = doc || jsonb_build_object('location', $2::jsonb), rev=nextval('snap_rev') WHERE kind='vehicle' AND id=$1`, vid, fmt.Sprintf("[%v,%v]", loc[0], loc[1]))
		if err != nil {
			e.t.Fatal(err)
		}
	}
	if err := e.svc.LoadSnapshot(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) userToken(uid string) string {
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.RegisteredClaims{Issuer: "bike-acct", Subject: uid, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))})
	tk.Header["kid"] = "k1"
	s, _ := tk.SignedString(e.priv)
	return s
}

func (e *testEnv) adminToken(perms []string, scope *string) string {
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, &auth.Admin{RegisteredClaims: jwt.RegisteredClaims{Issuer: "bike-iam", Subject: "AC0101", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, Role: "客服", Perms: perms, Scope: scope})
	tk.Header["kid"] = "k1"
	s, _ := tk.SignedString(e.priv)
	return s
}

type resp struct {
	status int
	body   map[string]any
	raw    []byte
}

// do 发请求。auth：用户编号、"admin:..."（直接给令牌）、"internal:模块"。
func (e *testEnv) do(method, path, who string, body any, headers ...string) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	switch {
	case who == "":
	case len(who) > 9 && who[:9] == "internal:":
		mod := who[9:]
		req.Header.Set("X-Caller", mod)
		req.Header.Set("Authorization", "Bearer key-"+mod)
	case len(who) > 6 && who[:6] == "admin:":
		req.Header.Set("Authorization", "Bearer "+who[6:])
	default:
		req.Header.Set("Authorization", "Bearer "+e.userToken(who))
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp{res.StatusCode, m, raw}
}

// outbox 发件表里某类型的事件载荷。
func (e *testEnv) outbox(typ string) []map[string]any {
	e.t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT body FROM outbox WHERE subject=$1 ORDER BY id`, typ)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var b []byte
		_ = rows.Scan(&b)
		var env struct {
			Payload map[string]any `json:"payload"`
			Actor   map[string]any `json:"actor"`
		}
		_ = json.Unmarshal(b, &env)
		env.Payload["_actor"] = env.Actor
		out = append(out, env.Payload)
	}
	return out
}

func (e *testEnv) order(id string) *Order {
	e.t.Helper()
	o, err := e.svc.GetOrder(context.Background(), id)
	if err != nil {
		e.t.Fatalf("读订单 %s：%v", id, err)
	}
	return o
}

// unlockOK 开锁成功并返回订单号。
func (e *testEnv) unlockOK(uid, code string) string {
	e.t.Helper()
	r := e.do("POST", "/api/ride/unlock", uid, map[string]any{"code": code})
	if r.status != 200 {
		e.t.Fatalf("开锁应成功：%d %s", r.status, r.raw)
	}
	return r.body["orderId"].(string)
}

func eventually(t *testing.T, d time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

var jsonUnmarshal = json.Unmarshal

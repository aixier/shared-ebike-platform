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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	_ "time/tzdata"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bike/ops/internal/auth"
	"bike/ops/internal/clients/fleet"
	"bike/ops/internal/clients/pay"
	"bike/ops/internal/clients/ride"
	"bike/ops/internal/config"
)

// 测试库：embedded-postgres，端口 = 服务端口 + 1000，运行与数据目录放本模块 .tmp/。
var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	root, _ := filepath.Abs(filepath.Join("..", "..", ".tmp"))
	_ = os.MkdirAll(root, 0o755)
	_ = os.RemoveAll(filepath.Join(root, "pg-data"))
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).Port(19313).Database("bike_ops").Locale("C").Encoding("UTF8").
		RuntimePath(filepath.Join(root, "pg-runtime")).DataPath(filepath.Join(root, "pg-data")).
		// 仓库路径含中文，initdb 按 GBK 传入 share 路径会报 UTF8 错误；二进制放系统临时目录下本服务专用的纯 ASCII 目录
		BinariesPath(filepath.Join(os.TempDir(), "bike-ops-pgbin")).
		StartTimeout(60 * time.Second).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "embedded-postgres 启动失败：", err)
		return 1
	}
	defer func() { _ = pg.Stop() }()
	ctx := context.Background()
	var err error
	testPool, err = pgxpool.New(ctx, "postgres://postgres:postgres@127.0.0.1:19313/bike_ops?sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer testPool.Close()
	if err := Migrate(ctx, testPool); err != nil {
		fmt.Fprintln(os.Stderr, "迁移失败：", err)
		return 1
	}
	// 迁移重复执行无害
	if err := Migrate(ctx, testPool); err != nil {
		fmt.Fprintln(os.Stderr, "重复迁移失败：", err)
		return 1
	}
	return m.Run()
}

// ===== 替身：按 fleet / ride / pay 的 swagger 返回 =====

type standIns struct {
	mu        sync.Mutex
	brands    map[string]string // brandId → 接入状态
	rideOrds  map[string]string // orderId → 订单状态（ride 侧）
	payments  map[string]string // paymentId → 处理中｜成功｜失败
	calls     []string
	actors    []string // 同步调用里 actor.kind 的记录
	keys      map[string]bool
	healthy   bool
	failFleet bool
}

func (s *standIns) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	check := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer svc-ops" || r.Header.Get("X-Caller") != "ops" {
			write(w, 401, map[string]string{"code": "UNAUTHORIZED", "message": "凭证不对"})
			return false
		}
		k := r.Header.Get("Idempotency-Key")
		if k == "" || len(k) > 64 {
			write(w, 400, map[string]string{"code": "INVALID", "message": "缺幂等键"})
			return false
		}
		s.calls = append(s.calls, r.Method+" "+r.URL.Path)
		s.keys[k] = true
		return true
	}
	mux.HandleFunc("POST /internal/fleet/brands/{brandId}/pause", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !check(w, r) {
			return
		}
		if s.failFleet {
			write(w, 500, map[string]string{"code": "INTERNAL", "message": "x"})
			return
		}
		var body fleet.PauseRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.actors = append(s.actors, "pause:"+body.Actor.Kind)
		if len([]rune(body.Reason)) < 4 || body.Actor.ID == "" || body.Metric == "" {
			write(w, 400, map[string]string{"code": "REASON_REQUIRED", "message": "请填写事由"})
			return
		}
		b := r.PathValue("brandId")
		st, ok := s.brands[b]
		switch {
		case !ok:
			write(w, 404, map[string]string{"code": "NOT_FOUND", "message": "没有找到该品牌"})
		case st == "暂停可租":
			write(w, 409, map[string]string{"code": "ALREADY_DONE", "message": "该品牌已暂停可租"})
		case st != "已接入":
			write(w, 409, map[string]string{"code": "CONFLICT", "message": "品牌当前为" + st + "，不能暂停可租"})
		default:
			s.brands[b] = "暂停可租"
			write(w, 200, map[string]any{"brand": map[string]any{"brandId": b, "accessStatus": "暂停可租"},
				"pause": map[string]any{"pauseId": "FPS-" + b, "brandId": b, "pausedAt": time.Now().Format(time.RFC3339), "metric": body.Metric}})
		}
	})
	mux.HandleFunc("POST /internal/fleet/brands/{brandId}/resume", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !check(w, r) {
			return
		}
		var rb fleet.ResumeRequest
		_ = json.NewDecoder(r.Body).Decode(&rb)
		s.actors = append(s.actors, "resume:"+rb.Actor.Kind)
		b := r.PathValue("brandId")
		st, ok := s.brands[b]
		switch {
		case !ok:
			write(w, 404, map[string]string{"code": "NOT_FOUND", "message": "没有找到该品牌"})
		case st != "暂停可租":
			write(w, 409, map[string]string{"code": "CONFLICT", "message": "该品牌未处于暂停可租"})
		default:
			s.brands[b] = "已接入"
			write(w, 200, map[string]any{"brand": map[string]any{"brandId": b, "accessStatus": "已接入"},
				"pause": map[string]any{"pauseId": "FPS-" + b, "brandId": b, "pausedAt": "2026-11-30T08:00:00+08:00", "metric": "开锁成功率"}})
		}
	})
	mux.HandleFunc("POST /internal/ride/orders/{orderId}/lock-fact", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !check(w, r) {
			return
		}
		var body ride.LockFactRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.actors = append(s.actors, "lockfact:"+body.Actor.Kind)
		o := r.PathValue("orderId")
		st, ok := s.rideOrds[o]
		switch {
		case !ok:
			write(w, 404, map[string]string{"code": "NOT_FOUND", "message": "没有找到该订单"})
		case st != "待人工处理":
			write(w, 409, map[string]string{"code": "CONFLICT", "message": "该订单已有锁车事实"})
		default:
			s.rideOrds[o] = "已结束"
			write(w, 200, map[string]any{"orderId": o, "status": "已结束"})
		}
	})
	mux.HandleFunc("POST /internal/pay/payments/{paymentId}/confirm", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !check(w, r) {
			return
		}
		var body pay.ConfirmRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.actors = append(s.actors, "confirm:"+body.Actor.Kind)
		p := r.PathValue("paymentId")
		st, ok := s.payments[p]
		if !ok {
			write(w, 404, map[string]string{"code": "NOT_FOUND", "message": "没有找到该流水"})
			return
		}
		if st != "处理中" {
			write(w, 200, map[string]any{"result": "失败", "failCode": "NOT_PENDING", "failReason": "该流水不在结果未知状态", "paymentId": p})
			return
		}
		if body.Result == "已收款" {
			s.payments[p] = "成功"
		} else {
			s.payments[p] = "失败"
		}
		write(w, 200, map[string]any{"result": "成功", "paymentId": p})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.healthy {
			w.WriteHeader(503)
			return
		}
		write(w, 200, map[string]bool{"ok": true})
	})
	return mux
}

// ===== 测试环境 =====

type env struct {
	t     *testing.T
	a     *App
	now   time.Time
	si    *standIns
	priv  ed25519.PrivateKey
	srv   *httptest.Server
	seq   int
	loc   *time.Location
	clock *time.Time
}

func resetDB(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `TRUNCATE outbox, inbox, idempotency, id_seq, settings, brands, rule_versions, platform_params,
		orders, users_realname, dependency_states, operator_outages, degrade_timeline, degrade_events, brand_metrics_hourly,
		brand_metrics_daily, entry_probes, platform_daily, brand_low_state, pauses, suspend_suggestions, alerts RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
}

// newEnv 清库，时钟固定在 start（本市时间）。
func newEnv(t *testing.T, start string) *env {
	t.Helper()
	resetDB(t)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now, err := time.ParseInLocation("2006-01-02 15:04:05", start, loc)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	v, err := auth.NewAdminVerifier("k1=" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	si := &standIns{brands: map[string]string{}, rideOrds: map[string]string{}, payments: map[string]string{}, keys: map[string]bool{}, healthy: true}
	stub := httptest.NewServer(si.handler(t))
	t.Cleanup(stub.Close)
	cfg := config.Config{Timezone: "Asia/Shanghai", LimitNearRatio: 0.8, PaymentUnknownTimeoutMinutes: 30, OnDuty: []string{"AC0401", "AC0402"}}
	a, err := New(testPool, cfg, fleet.New(stub.URL, "svc-ops"), ride.New(stub.URL, "svc-ops"), pay.New(stub.URL, "svc-ops"), v)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, a: a, si: si, priv: priv, loc: loc}
	e.clock = &now
	a.Now = func() time.Time { return *e.clock }
	e.srv = httptest.NewServer(a.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, e.loc)
	if err != nil {
		e.t.Fatal(err)
	}
	return t
}

func (e *env) setNow(s string)               { t := e.at(s); *e.clock = t }
func (e *env) advance(d time.Duration)       { t := e.clock.Add(d); *e.clock = t }
func (e *env) ts(s string) string            { return e.at(s).Format(time.RFC3339) }
func (e *env) nowTS() string                 { return e.clock.Format(time.RFC3339) }
func (e *env) ctx() context.Context          { return context.Background() }
func (e *env) fatalf(f string, args ...any) { e.t.Helper(); e.t.Fatalf(f, args...) }

// token 签一张管理令牌。
func (e *env) token(sub string, perms []string, scope any, readonly bool) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"iss": "bike-iam", "sub": sub, "name": "值班" + sub, "role": "运维", "client": "管理端",
		"perms": perms, "scope": scope, "readonly": readonly, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(e.priv)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// 常用岗位令牌。
func (e *env) duty() string {
	return e.token("AC0401", []string{"ops.monitor", "degrade.control", "degrade.closure.view", "degrade.closure.handle", "access.pause"}, nil, false)
}
func (e *env) gov() string { return e.token("AC0201", []string{"metrics.unlockRate.view", "access.view"}, nil, false) }

type resp struct {
	status int
	body   map[string]any
	arr    []any
	raw    []byte
}

func (e *env) do(method, path, tok string, body any, idem string) resp {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	r := resp{status: res.StatusCode, raw: buf.Bytes()}
	_ = json.Unmarshal(r.raw, &r.body)
	_ = json.Unmarshal(r.raw, &r.arr)
	return r
}

func (r resp) code() string { s, _ := r.body["code"].(string); return s }

// publish 以订阅方身份收到一条事件。
func (e *env) publish(typ string, payload any) {
	e.t.Helper()
	e.seq++
	p, _ := json.Marshal(payload)
	env := Envelope{EventID: fmt.Sprintf("test-%s-%d", typ, e.seq), Type: typ, Version: 1, OccurredAt: e.nowTS(), Key: "k", Actor: Actor{Kind: "系统", ID: "x"}, Payload: p}
	if err := e.a.HandleEnvelope(e.ctx(), env); err != nil {
		e.t.Fatalf("处理 %s 失败：%v", typ, err)
	}
}

// outboxTypes 发件表里的事件类型（按顺序）。
func (e *env) outboxTypes() []string {
	rows, err := testPool.Query(e.ctx(), `SELECT subject FROM outbox ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
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

// outboxPayloads 某类事件的载荷。
func (e *env) outboxPayloads(typ string) []map[string]any {
	rows, err := testPool.Query(e.ctx(), `SELECT body FROM outbox WHERE subject = $1 ORDER BY id`, typ)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var b []byte
		_ = rows.Scan(&b)
		var env Envelope
		_ = json.Unmarshal(b, &env)
		var p map[string]any
		_ = json.Unmarshal(env.Payload, &p)
		p["_actor"] = env.Actor
		p["_key"] = env.Key
		out = append(out, p)
	}
	return out
}

func count(xs []string, x string) int {
	n := 0
	for _, y := range xs {
		if y == x {
			n++
		}
	}
	return n
}

// ===== 原型样例（SCHEMA 5.7、5.12）=====

// seedRules 规则版本 PR0002（10-15 起）与 PR0003（12-01 起）、平台参数门槛。金额按分。
func (e *env) seedRules() {
	limits := func(hourly, tightMin int, tightAmt int64) map[string]any {
		return map[string]any{"perUserOpenDegradedOrders": 1, "normal": map[string]any{"maxMinutes": 120, "maxAmount": 6000},
			"tight": map[string]any{"maxMinutes": tightMin, "maxAmount": tightAmt}, "hourlyDegradedOrders": hourly, "degradeDurationAlertMinutes": 30}
	}
	e.publish("rule.version.published", map[string]any{"versionId": "PR0002", "name": "秋季价", "effectiveAt": e.ts("2026-10-15 00:00:00"), "previousVersionId": "PR0001",
		"ride": map[string]any{"vehicleType": "电单车", "startFee": 150, "startMinutes": 3, "perMinute": 50}, "riskLimits": limits(300, 45, 2000)})
	e.publish("rule.version.published", map[string]any{"versionId": "PR0003", "name": "冬季价", "effectiveAt": e.ts("2026-12-01 00:00:00"), "previousVersionId": "PR0002",
		"ride": map[string]any{"vehicleType": "电单车", "startFee": 150, "startMinutes": 3, "perMinute": 50}, "riskLimits": limits(200, 30, 1500)})
	e.publish("rule.params.changed", map[string]any{"billPushTargetSec": 10, "availabilityTarget": 0.995, "unlockP90TargetSec": 5,
		"pauseThreshold": map[string]any{"unlockSuccessRate": 0.9, "availability": 0.8, "sustainMinutes": 120}, "changedAt": e.ts("2026-10-10 11:20:00")})
}

// seedBrands 青禾（BR01，OP01）、木棉（BR02，OP02）、简途（BR03，OP03）均已接入。
func (e *env) seedBrands() {
	for _, b := range [][3]string{{"BR01", "OP01", "青禾"}, {"BR02", "OP02", "木棉"}, {"BR03", "OP03", "简途"}} {
		e.publish("fleet.brand.registered", map[string]any{"brandId": b[0], "operatorId": b[1], "name": b[2], "accessStatus": "已接入"})
		e.si.brands[b[0]] = "已接入"
	}
}

// degraded 降级标记。
func degraded(eventID any, admission string) map[string]any {
	return map[string]any{"eventId": eventID, "admission": admission, "limitMinutes": 120, "limitAmount": 6000}
}

// rideOrder 订单头。
func (e *env) head(orderID, userID, brandID, requestAt string, deg any) map[string]any {
	return map[string]any{"orderId": orderID, "userId": userID, "vehicleId": "VH000001", "plateNo": "A1", "brandId": brandID, "operatorId": "OP0" + brandID[3:],
		"channel": "微信", "pricingVersionId": "PR0002", "requestAt": e.ts(requestAt), "degraded": deg}
}

func merge(a map[string]any, kv ...any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

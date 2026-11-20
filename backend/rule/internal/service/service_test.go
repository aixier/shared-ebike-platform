package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"bike/rule/internal/auth"
)

// trace: contracts/openapi/rule.yaml 全部接口；R-19～R-22 BR-14 BR-15 6.9；任务 4 5 6 7 8 9 10 12 13

var cst = time.FixedZone("+08:00", 8*3600)

// anchor 原型的"现在"：2026-12-01 12:00:00。
var anchor = time.Date(2026, 12, 1, 12, 0, 0, 0, cst)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

type env struct {
	t     *testing.T
	srv   *httptest.Server
	clock *clock
	svc   *Service
}

func resetDB(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `TRUNCATE pricing_versions, platform_params, platform_params_history, idempotency_keys, outbox, brands, consumed_events RESTART IDENTITY`)
	if err != nil {
		t.Fatal(err)
	}
}

// newEnv 清库并起服务；seeded 为真时载入原型样例（时钟在锚点）。
func newEnv(t *testing.T, seeded bool) *env {
	t.Helper()
	resetDB(t)
	c := &clock{t: anchor}
	svc := &Service{
		Pool:    testPool,
		Now:     c.Now,
		Admin:   &auth.AdminVerifier{Keys: map[string]ed25519.PublicKey{"k1": testPub}},
		Callers: auth.Callers{"bill": "bill-secret"},
	}
	if seeded {
		if err := SeedIfEmpty(context.Background(), testPool, anchor); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, clock: c, svc: svc}
}

func token(perms []string, mod func(jwt.MapClaims)) string {
	now := time.Now()
	c := jwt.MapClaims{
		"iss": "bike-iam", "sub": "AC0002", "name": "叶＊", "role": "主管部门管理员", "client": "管理端",
		"perms": perms, "scope": nil, "readonly": false, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
	if mod != nil {
		mod(c)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tok.Header["kid"] = "k1"
	s, _ := tok.SignedString(testPriv)
	return s
}

var (
	editor   = token([]string{"pricing.view", "pricing.edit"}, nil)
	viewer   = token([]string{"pricing.view"}, nil)
	loggedIn = token([]string{"order.view"}, nil)
	opsTok   = token([]string{"platformParams.edit"}, func(c jwt.MapClaims) { c["sub"] = "AC0404"; c["role"] = "运维" })
)

type resp struct {
	status int
	body   map[string]any
	list   []any
	raw    []byte
}

func (e *env) do(method, path, tok string, body any, hdr map[string]string) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rd = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	r := resp{status: res.StatusCode, raw: raw}
	if len(raw) > 0 && raw[0] == '[' {
		_ = json.Unmarshal(raw, &r.list)
	} else {
		_ = json.Unmarshal(raw, &r.body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		e.t.Errorf("%s %s 的 Content-Type 应为 JSON，实得 %q", method, path, ct)
	}
	return r
}

func (r resp) code() string { s, _ := r.body["code"].(string); return s }

func expect(t *testing.T, r resp, status int, code string) {
	t.Helper()
	if r.status != status || (code != "" && r.code() != code) {
		t.Fatalf("应返回 %d %s，实得 %d %s", status, code, r.status, r.raw)
	}
	if status >= 400 {
		if msg, _ := r.body["message"].(string); msg == "" {
			t.Errorf("错误体须有一句原因：%s", r.raw)
		}
	}
}

func num(v any) int64 { f, _ := v.(float64); return int64(f) }

func path(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

type outboxRow struct {
	Subject string
	Body    map[string]any
}

func outboxRows(t *testing.T) []outboxRow {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT subject, body FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.Subject, &r.Body); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func countVersions(t *testing.T) int {
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM pricing_versions`).Scan(&n)
	return n
}

// ---------------------------------------------------------------- 基础件

func TestHealthz(t *testing.T) {
	e := newEnv(t, false)
	r := e.do("GET", "/healthz", "", nil, nil)
	if r.status != 200 || r.body["ok"] != true {
		t.Fatalf("healthz 应返回 200 {ok:true}：%d %s", r.status, r.raw)
	}
}

func TestNotFoundRoute(t *testing.T) {
	e := newEnv(t, false)
	expect(t, e.do("GET", "/api/rule/nothing", editor, nil, nil), 404, "NOT_FOUND")
}

func TestAdminAuth(t *testing.T) {
	e := newEnv(t, true)
	expect(t, e.do("GET", "/api/rule/pricing/versions", "", nil, nil), 401, "ADMIN_NOT_LOGGED_IN")
	expect(t, e.do("GET", "/api/rule/pricing/versions", "bad.token.x", nil, nil), 401, "ADMIN_NOT_LOGGED_IN")
	expired := token([]string{"pricing.view"}, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() })
	expect(t, e.do("GET", "/api/rule/pricing/versions", expired, nil, nil), 401, "ADMIN_NOT_LOGGED_IN")
	r := e.do("GET", "/api/rule/pricing/versions", loggedIn, nil, nil)
	expect(t, r, 403, "FORBIDDEN")
	if !strings.Contains(r.body["message"].(string), "主管部门管理员") {
		t.Errorf("无权限说明应带岗位：%s", r.raw)
	}
	expect(t, e.do("GET", "/api/rule/pricing/versions", viewer, nil, nil), 200, "")
	expect(t, e.do("GET", "/api/rule/pricing/current", loggedIn, nil, nil), 200, "")
	expect(t, e.do("GET", "/api/rule/platform-params", loggedIn, nil, nil), 200, "")
	expect(t, e.do("POST", "/api/rule/pricing/versions", viewer, map[string]any{"name": "x", "reason": "政府调价通知"}, nil), 403, "FORBIDDEN")
	ro := token([]string{"pricing.view", "pricing.edit"}, func(c jwt.MapClaims) { c["readonly"] = true })
	expect(t, e.do("POST", "/api/rule/pricing/versions", ro, map[string]any{"name": "x", "reason": "政府调价通知"}, nil), 403, "READONLY")
	expect(t, e.do("GET", "/api/rule/pricing/versions", ro, nil, nil), 200, "")
}

// ---------------------------------------------------------------- 查询

func TestList_样例版本新到旧(t *testing.T) {
	e := newEnv(t, true)
	r := e.do("GET", "/api/rule/pricing/versions", viewer, nil, nil)
	expect(t, r, 200, "")
	if len(r.list) != 4 {
		t.Fatalf("应有 4 个版本：%s", r.raw)
	}
	wantIDs := []string{"PR0004", "PR0003", "PR0002", "PR0001"}
	wantSt := []string{"待生效", "生效中", "已失效", "已失效"}
	for i, x := range r.list {
		v := x.(map[string]any)
		if v["versionId"] != wantIDs[i] || v["status"] != wantSt[i] {
			t.Errorf("第 %d 个应为 %s %s，实得 %v %v", i, wantIDs[i], wantSt[i], v["versionId"], v["status"])
		}
	}
	pr1 := r.list[3].(map[string]any)
	if num(path(pr1, "ride", "perMinute")) != 40 || num(pr1["dispatchFee"]) != 300 || pr1["effectiveUntil"] != "2026-10-15T00:00:00+08:00" {
		t.Errorf("PR0001 内容不对：%v", pr1)
	}
	if pr1["effectiveAt"] != "2026-07-01T00:00:00+08:00" || path(pr1, "createdBy", "id") != "AC0001" || pr1["revokedAt"] != nil {
		t.Errorf("PR0001 元数据不对：%v", pr1)
	}
	for _, k := range []string{"versionId", "name", "status", "effectiveAt", "createdAt", "createdBy", "ride", "dispatchFee", "monthCard", "coupons", "deposit", "creditFree", "serviceFee", "riskLimits"} {
		if _, ok := pr1[k]; !ok {
			t.Errorf("缺少必有字段 %s", k)
		}
	}
}

func TestGetAndCurrent(t *testing.T) {
	e := newEnv(t, true)
	r := e.do("GET", "/api/rule/pricing/current", loggedIn, nil, nil)
	expect(t, r, 200, "")
	if r.body["versionId"] != "PR0003" || r.body["status"] != "生效中" {
		t.Errorf("现行版本应为 PR0003：%s", r.raw)
	}
	r = e.do("GET", "/api/rule/pricing/versions/PR0002", viewer, nil, nil)
	expect(t, r, 200, "")
	if r.body["versionId"] != "PR0002" || num(path(r.body, "deposit", "amount")) != 19900 {
		t.Errorf("PR0002 不对：%s", r.raw)
	}
	expect(t, e.do("GET", "/api/rule/pricing/versions/PR0099", viewer, nil, nil), 404, "NOT_FOUND")
	expect(t, e.do("GET", "/api/rule/pricing/versions/abc", viewer, nil, nil), 404, "NOT_FOUND")
	expect(t, e.do("GET", "/api/rule/pricing/versions/PR0002", loggedIn, nil, nil), 403, "FORBIDDEN")

	// 时间走到 PR0004 生效：无需任何操作，状态按时刻算
	e.clock.Set(time.Date(2027, 1, 1, 0, 0, 0, 0, cst))
	r = e.do("GET", "/api/rule/pricing/current", loggedIn, nil, nil)
	if r.body["versionId"] != "PR0004" {
		t.Errorf("到点后现行版本应为 PR0004：%s", r.raw)
	}
}

func TestCurrent_库为空(t *testing.T) {
	e := newEnv(t, false)
	expect(t, e.do("GET", "/api/rule/pricing/current", loggedIn, nil, nil), 404, "NOT_FOUND")
	r := e.do("GET", "/api/rule/pricing/versions", viewer, nil, nil)
	expect(t, r, 200, "")
	if string(bytes.TrimSpace(r.raw)) != "[]" {
		t.Errorf("没有版本时应返回空数组：%s", r.raw)
	}
}

// ---------------------------------------------------------------- 新增版本

func TestCreate_立即生效只传改动项(t *testing.T) {
	e := newEnv(t, true)
	r := e.do("POST", "/api/rule/pricing/versions", editor, map[string]any{
		"name": "分钟价调整", "reason": "政府调价通知", "ride": map[string]any{"perMinute": 60}, "serviceFee": map[string]any{"ratios": map[string]any{"BR02": 0.65}},
	}, nil)
	expect(t, r, 200, "")
	v := r.body
	if v["versionId"] != "PR0005" || v["status"] != "生效中" || v["effectiveAt"] != "2026-12-01T12:00:00+08:00" || v["createdAt"] != "2026-12-01T12:00:00+08:00" {
		t.Errorf("新版本元数据不对：%s", r.raw)
	}
	if num(path(v, "ride", "perMinute")) != 60 || num(path(v, "ride", "startFee")) != 150 || num(path(v, "riskLimits", "tight", "maxAmount")) != 1500 {
		t.Errorf("未改项应沿用 PR0003：%s", r.raw)
	}
	if path(v, "serviceFee", "ratios", "BR02").(float64) != 0.65 || path(v, "serviceFee", "ratios", "BR03").(float64) != 0.61 {
		t.Errorf("比例合并不对：%s", r.raw)
	}
	if v["note"] != "政府调价通知" {
		t.Errorf("未填说明时取录入事由：%v", v["note"])
	}
	if path(v, "createdBy", "id") != "AC0002" || path(v, "createdBy", "kind") != "人" {
		t.Errorf("录入人不对：%v", v["createdBy"])
	}
	// 新版本生效到 PR0004 生效为止；PR0003 在新版本生效时失效
	if v["effectiveUntil"] != "2027-01-01T00:00:00+08:00" {
		t.Errorf("失效时刻应为下一版本生效时刻：%v", v["effectiveUntil"])
	}
	old := e.do("GET", "/api/rule/pricing/versions/PR0003", viewer, nil, nil).body
	if old["status"] != "已失效" || old["effectiveUntil"] != "2026-12-01T12:00:00+08:00" || num(path(old, "ride", "perMinute")) != 50 {
		t.Errorf("旧版本应已失效且内容不变：%v", old)
	}
	cur := e.do("GET", "/api/rule/pricing/current", loggedIn, nil, nil).body
	if cur["versionId"] != "PR0005" {
		t.Errorf("现行版本应为 PR0005：%v", cur["versionId"])
	}

	rows := outboxRows(t)
	if len(rows) != 2 || rows[0].Subject != "rule.version.published" || rows[1].Subject != "rule.audit.recorded" {
		t.Fatalf("发件表应有已发布与留痕两条：%+v", rows)
	}
	ev := rows[0].Body
	if ev["type"] != "rule.version.published" || num(ev["version"]) != 1 || ev["key"] != "pricing" || ev["eventId"] == "" || path(ev, "actor", "id") != "AC0002" {
		t.Errorf("信封不对：%v", ev)
	}
	p := ev["payload"].(map[string]any)
	if p["versionId"] != "PR0005" || p["previousVersionId"] != "PR0003" || p["effectiveAt"] != "2026-12-01T12:00:00+08:00" || num(path(p, "ride", "perMinute")) != 60 {
		t.Errorf("已发布载荷不对：%v", p)
	}
	if _, has := p["status"]; has {
		t.Error("载荷不应带契约外字段")
	}
	au := rows[1].Body
	ap := au["payload"].(map[string]any)
	if au["key"] != "pricing:PR0005" || ap["action"] != "新增规则版本" || ap["targetType"] != "pricing" || ap["targetId"] != "PR0005" || ap["reason"] != "政府调价通知" {
		t.Errorf("留痕不对：%v", au)
	}
	if path(ap, "before", "versionId") != "PR0003" || path(ap, "after", "ride", "perMinute") == nil || path(ap, "after", "deposit") != nil {
		t.Errorf("留痕前后值应只含改动部分：%v", ap)
	}
}

func TestCreate_定时生效以生效时刻的前一版为底(t *testing.T) {
	e := newEnv(t, true)
	r := e.do("POST", "/api/rule/pricing/versions", editor, map[string]any{
		"name": "2月调整", "reason": "政府调价通知", "effectiveAt": "2027-02-01T00:00:00+08:00", "note": "押金下调", "deposit": map[string]any{"amount": 9900},
	}, nil)
	expect(t, r, 200, "")
	v := r.body
	if v["status"] != "待生效" || v["effectiveUntil"] != nil || v["note"] != "押金下调" {
		t.Errorf("待生效版本不对：%s", r.raw)
	}
	// 以 PR0004 为底：免押门槛 600、5 张券
	if num(path(v, "creditFree", "threshold")) != 600 || len(v["coupons"].([]any)) != 5 || num(path(v, "deposit", "amount")) != 9900 {
		t.Errorf("应以 PR0004 为底：%s", r.raw)
	}
	p := outboxRows(t)[0].Body["payload"].(map[string]any)
	if p["previousVersionId"] != "PR0004" {
		t.Errorf("上一版本应为 PR0004：%v", p["previousVersionId"])
	}
	pr4 := e.do("GET", "/api/rule/pricing/versions/PR0004", viewer, nil, nil).body
	if pr4["effectiveUntil"] != "2027-02-01T00:00:00+08:00" {
		t.Errorf("PR0004 失效时刻应更新为新版本生效时刻：%v", pr4["effectiveUntil"])
	}
}

func TestCreate_校验(t *testing.T) {
	e := newEnv(t, true)
	before := countVersions(t)
	expect(t, e.do("POST", "/api/rule/pricing/versions", editor, map[string]any{"name": "x", "reason": "调价"}, nil), 400, "REASON_REQUIRED")
	expect(t, e.do("POST", "/api/rule/pricing/versions", editor, `{"name":`, nil), 400, "INVALID")
	r := e.do("POST", "/api/rule/pricing/versions", editor, map[string]any{
		"name": "x", "reason": "政府调价通知", "ride": map[string]any{"startFee": 155, "perMinute": 600}, "creditFree": map[string]any{"threshold": 300},
	}, nil)
	expect(t, r, 400, "INVALID")
	errs, _ := r.body["errors"].([]any)
	if len(errs) != 3 {
		t.Errorf("应逐项返回 3 个错误：%s", r.raw)
	}
	r = e.do("POST", "/api/rule/pricing/versions", editor, map[string]any{"name": "x", "reason": "政府调价通知", "effectiveAt": "2026-11-30T00:00:00+08:00"}, nil)
	expect(t, r, 400, "INVALID")
	if !strings.Contains(string(r.raw), "effectiveAt") {
		t.Errorf("应指出生效时间：%s", r.raw)
	}
	r = e.do("POST", "/api/rule/pricing/versions", editor, map[string]any{"name": "x", "reason": "政府调价通知", "effectiveAt": "2027-01-01T00:00:00+08:00"}, nil)
	expect(t, r, 400, "INVALID")
	if !strings.Contains(string(r.raw), "PR0004") {
		t.Errorf("同一生效时刻已有版本时应指出是哪个：%s", r.raw)
	}
	if countVersions(t) != before || len(outboxRows(t)) != 0 {
		t.Error("校验失败不得写入任何东西")
	}
}

func TestCreate_首个版本须完整(t *testing.T) {
	e := newEnv(t, false)
	r := e.do("POST", "/api/rule/pricing/versions", editor, map[string]any{"name": "首版", "reason": "首版计价规则", "ride": map[string]any{"startFee": 150}}, nil)
	expect(t, r, 400, "INVALID")
	full := map[string]any{
		"name": "首版", "reason": "首版计价规则",
		"ride":        map[string]any{"startFee": 150, "startMinutes": 3, "perMinute": 40},
		"dispatchFee": 300,
		"monthCard":   map[string]any{"price": 2000, "days": 30, "perOrderCap": 300, "allocation": "按骑行单数"},
		"deposit":     map[string]any{"amount": 9900},
		"creditFree":  map[string]any{"threshold": 550},
		"serviceFee":  map[string]any{"basis": "订单应收", "ratios": map[string]any{"BR01": 0.62}},
		"riskLimits":  map[string]any{"perUserOpenDegradedOrders": 1, "normal": map[string]any{"maxMinutes": 120, "maxAmount": 6000}, "tight": map[string]any{"maxMinutes": 45, "maxAmount": 2000}, "hourlyDegradedOrders": 300, "degradeDurationAlertMinutes": 30},
	}
	r = e.do("POST", "/api/rule/pricing/versions", editor, full, nil)
	expect(t, r, 200, "")
	if r.body["versionId"] != "PR0001" || r.body["status"] != "生效中" {
		t.Errorf("首版应为 PR0001 生效中：%s", r.raw)
	}
	p := outboxRows(t)[0].Body["payload"].(map[string]any)
	if v, ok := p["previousVersionId"]; !ok || v != nil {
		t.Errorf("首版上一版本应为 null：%v", p)
	}
}

func TestCreate_幂等(t *testing.T) {
	e := newEnv(t, true)
	body := map[string]any{"name": "幂等测试", "reason": "政府调价通知", "dispatchFee": 600}
	h := map[string]string{"Idempotency-Key": "k-001"}
	r1 := e.do("POST", "/api/rule/pricing/versions", editor, body, h)
	expect(t, r1, 200, "")
	e.clock.Set(anchor.Add(time.Minute))
	r2 := e.do("POST", "/api/rule/pricing/versions", editor, body, h)
	expect(t, r2, 200, "")
	if !bytes.Equal(r1.raw, r2.raw) {
		t.Errorf("同一幂等键应返回第一次的结果：\n%s\n%s", r1.raw, r2.raw)
	}
	if countVersions(t) != 5 || len(outboxRows(t)) != 2 {
		t.Error("同一幂等键不得重复执行")
	}
	r3 := e.do("POST", "/api/rule/pricing/versions", editor, body, map[string]string{"Idempotency-Key": "k-002"})
	if r3.body["versionId"] != "PR0006" {
		t.Errorf("不同幂等键应新建：%s", r3.raw)
	}
	expect(t, e.do("POST", "/api/rule/pricing/versions", editor, body, map[string]string{"Idempotency-Key": strings.Repeat("x", 65)}), 400, "INVALID")
}

// ---------------------------------------------------------------- 撤销

func TestRevoke(t *testing.T) {
	e := newEnv(t, true)
	expect(t, e.do("POST", "/api/rule/pricing/versions/PR0004/revoke", editor, map[string]any{"reason": "撤"}, nil), 400, "REASON_REQUIRED")
	r := e.do("POST", "/api/rule/pricing/versions/PR0004/revoke", editor, map[string]any{"reason": "门槛另行研究"}, nil)
	expect(t, r, 200, "")
	if r.body["status"] != "已撤销" || r.body["revokedAt"] != "2026-12-01T12:00:00+08:00" || path(r.body, "revokedBy", "id") != "AC0002" || r.body["effectiveUntil"] != nil {
		t.Errorf("撤销结果不对：%s", r.raw)
	}
	pr3 := e.do("GET", "/api/rule/pricing/versions/PR0003", viewer, nil, nil).body
	if pr3["effectiveUntil"] != nil || pr3["status"] != "生效中" {
		t.Errorf("撤销后 PR0003 应一直生效：%v", pr3)
	}
	rows := outboxRows(t)
	if len(rows) != 2 || rows[0].Subject != "rule.version.revoked" || rows[1].Subject != "rule.audit.recorded" {
		t.Fatalf("发件表不对：%+v", rows)
	}
	if p := rows[0].Body["payload"].(map[string]any); p["versionId"] != "PR0004" || p["revokedAt"] != "2026-12-01T12:00:00+08:00" || rows[0].Body["key"] != "pricing" {
		t.Errorf("撤销载荷不对：%v", rows[0].Body)
	}
	expect(t, e.do("POST", "/api/rule/pricing/versions/PR0004/revoke", editor, map[string]any{"reason": "门槛另行研究"}, nil), 409, "CONFLICT")
	expect(t, e.do("POST", "/api/rule/pricing/versions/PR0003/revoke", editor, map[string]any{"reason": "门槛另行研究"}, nil), 409, "CONFLICT")
	expect(t, e.do("POST", "/api/rule/pricing/versions/PR0001/revoke", editor, map[string]any{"reason": "门槛另行研究"}, nil), 409, "CONFLICT")
	expect(t, e.do("POST", "/api/rule/pricing/versions/PR0077/revoke", editor, map[string]any{"reason": "门槛另行研究"}, nil), 404, "NOT_FOUND")
	// 列表里仍保留已撤销的版本
	if l := e.do("GET", "/api/rule/pricing/versions", viewer, nil, nil).list; len(l) != 4 {
		t.Errorf("撤销后记录应保留：%d", len(l))
	}
}

func TestVersionImmutable_库层兜底(t *testing.T) {
	newEnv(t, true)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE pricing_versions SET content = content || '{"dispatchFee":1}' WHERE version_id='PR0003'`); err == nil {
		t.Error("改版本内容应被拒绝")
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM pricing_versions WHERE version_id='PR0001'`); err == nil {
		t.Error("删除版本应被拒绝")
	}
	if _, err := testPool.Exec(ctx, `UPDATE pricing_versions SET revoked_at=now(), revoked_by='{"kind":"人","id":"x"}' WHERE version_id='PR0002'`); err == nil {
		t.Error("撤销已生效版本应被拒绝")
	}
}

// ---------------------------------------------------------------- 内部接口

func TestInternal(t *testing.T) {
	e := newEnv(t, true)
	ok := map[string]string{"X-Caller": "bill"}
	expect(t, e.do("GET", "/internal/rule/versions/PR0001", "", nil, ok), 401, "")
	expect(t, e.do("GET", "/internal/rule/versions/PR0001", "wrong", nil, ok), 401, "")
	expect(t, e.do("GET", "/internal/rule/versions/PR0001", "bill-secret", nil, map[string]string{"X-Caller": "ride"}), 401, "")
	expect(t, e.do("GET", "/internal/rule/versions/PR0001", editor, nil, nil), 401, "")

	r := e.do("GET", "/internal/rule/versions/PR0001", "bill-secret", nil, ok)
	expect(t, r, 200, "")
	if r.body["versionId"] != "PR0001" || num(path(r.body, "ride", "perMinute")) != 40 {
		t.Errorf("按版本号取版本不对：%s", r.raw)
	}
	expect(t, e.do("GET", "/internal/rule/versions/PR0100", "bill-secret", nil, ok), 404, "NOT_FOUND")

	// 已撤销的版本也能取到
	e.do("POST", "/api/rule/pricing/versions/PR0004/revoke", editor, map[string]any{"reason": "门槛另行研究"}, nil)
	r = e.do("GET", "/internal/rule/versions/PR0004", "bill-secret", nil, ok)
	if r.status != 200 || r.body["status"] != "已撤销" {
		t.Errorf("已撤销版本应可取：%s", r.raw)
	}

	cases := map[string]string{
		"":                                "PR0003",
		"?at=2026-10-14T23:51:00%2B08:00": "PR0001", // BR-14 样例 OD2026101412805
		"?at=2026-10-15T00:00:00%2B08:00": "PR0002",
		"?at=2027-06-01T00:00:00%2B08:00": "PR0003", // PR0004 已撤销
	}
	for q, want := range cases {
		r := e.do("GET", "/internal/rule/effective-version"+q, "bill-secret", nil, ok)
		if r.status != 200 || r.body["versionId"] != want {
			t.Errorf("at%s 应取 %s：%d %s", q, want, r.status, r.raw)
		}
	}
	expect(t, e.do("GET", "/internal/rule/effective-version?at=2026-06-01T00:00:00%2B08:00", "bill-secret", nil, ok), 404, "NOT_FOUND")
	expect(t, e.do("GET", "/internal/rule/effective-version?at=yesterday", "bill-secret", nil, ok), 400, "INVALID")
}

// ---------------------------------------------------------------- 平台参数

func TestParams_空库与样例(t *testing.T) {
	e := newEnv(t, false)
	r := e.do("GET", "/api/rule/platform-params", loggedIn, nil, nil)
	expect(t, r, 200, "")
	if r.body["owner"] != "平台公司" || r.body["updatedAt"] != nil || path(r.body, "items", "payWindowHours") != nil {
		t.Errorf("未设置时各项为 null：%s", r.raw)
	}
	e = newEnv(t, true)
	r = e.do("GET", "/api/rule/platform-params", loggedIn, nil, nil)
	if num(path(r.body, "items", "csRefundLimit")) != 2000 || path(r.body, "items", "pauseThreshold", "availability").(float64) != 0.8 || path(r.body, "updatedBy", "id") != "AC0404" {
		t.Errorf("样例参数不对：%s", r.raw)
	}
}

func TestParams_修改(t *testing.T) {
	e := newEnv(t, true)
	body := map[string]any{"items": map[string]any{"payWindowHours": 48, "pauseThreshold": map[string]any{"availability": 0.75}}, "reason": "平台公司调整期限"}
	expect(t, e.do("PATCH", "/api/rule/platform-params", editor, body, nil), 403, "FORBIDDEN")
	expect(t, e.do("PATCH", "/api/rule/platform-params", opsTok, map[string]any{"items": map[string]any{"payWindowHours": 48}}, nil), 400, "REASON_REQUIRED")
	r := e.do("PATCH", "/api/rule/platform-params", opsTok, map[string]any{"items": map[string]any{"payWindowHours": 0, "availabilityTarget": 1.5}, "reason": "平台公司调整期限"}, nil)
	expect(t, r, 400, "INVALID")
	if errs, _ := r.body["errors"].([]any); len(errs) != 2 {
		t.Errorf("应逐项返回 2 个错误：%s", r.raw)
	}
	expect(t, e.do("PATCH", "/api/rule/platform-params", opsTok, map[string]any{"items": map[string]any{}, "reason": "平台公司调整期限"}, nil), 400, "INVALID")

	e.clock.Set(anchor.Add(time.Hour))
	r = e.do("PATCH", "/api/rule/platform-params", opsTok, body, map[string]string{"Idempotency-Key": "p-1"})
	expect(t, r, 200, "")
	if num(path(r.body, "items", "payWindowHours")) != 48 || path(r.body, "items", "pauseThreshold", "availability").(float64) != 0.75 ||
		path(r.body, "items", "pauseThreshold", "unlockSuccessRate").(float64) != 0.9 || num(path(r.body, "items", "nearbyRadiusM")) != 500 {
		t.Errorf("修改结果不对：%s", r.raw)
	}
	if r.body["updatedAt"] != "2026-12-01T13:00:00+08:00" || path(r.body, "updatedBy", "id") != "AC0404" {
		t.Errorf("修改人与时刻不对：%s", r.raw)
	}
	again := e.do("PATCH", "/api/rule/platform-params", opsTok, body, map[string]string{"Idempotency-Key": "p-1"})
	if !bytes.Equal(again.raw, r.raw) {
		t.Error("同一幂等键应返回第一次的结果")
	}
	var hist int
	var before, after map[string]any
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM platform_params_history`).Scan(&hist)
	_ = testPool.QueryRow(context.Background(), `SELECT before, after FROM platform_params_history ORDER BY id DESC LIMIT 1`).Scan(&before, &after)
	if hist != 1 || num(before["payWindowHours"]) != 24 || num(after["payWindowHours"]) != 48 {
		t.Errorf("旧值应留历史：%d %v %v", hist, before, after)
	}
	rows := outboxRows(t)
	if len(rows) != 2 || rows[0].Subject != "rule.params.changed" || rows[1].Subject != "rule.audit.recorded" {
		t.Fatalf("发件表不对：%+v", rows)
	}
	p := rows[0].Body["payload"].(map[string]any)
	if rows[0].Body["key"] != "params" || num(p["payWindowHours"]) != 48 || num(p["backfillWindowHours"]) != 72 || p["changedAt"] != "2026-12-01T13:00:00+08:00" {
		t.Errorf("参数变更载荷应带全部当前值：%v", rows[0].Body)
	}
	ap := rows[1].Body["payload"].(map[string]any)
	if ap["reason"] != "平台公司调整期限" || path(ap, "before", "payWindowHours") == nil || path(ap, "after", "nearbyRadiusM") != nil {
		t.Errorf("留痕应只含改动项：%v", ap)
	}
	if _, err := testPool.Exec(context.Background(), `DELETE FROM platform_params_history`); err == nil {
		t.Error("参数历史应只追加")
	}
}

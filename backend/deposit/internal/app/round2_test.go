package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func readonlyAdminToken(perms ...string) string {
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "bike-iam", "sub": "AC0201", "name": "吕＊＊", "role": "财务", "client": "管理端",
		"perms": perms, "scope": nil, "readonly": true, "exp": time.Now().Add(time.Hour).Unix()})
	tk.Header["kid"] = "k1"
	s, _ := tk.SignedString(signKey)
	return s
}

func (e *env) registered(uid, channel string) {
	e.event("acct.user.registered", uid, map[string]any{"userId": uid, "channel": channel, "registeredAt": e.now.Format(time.RFC3339), "duringDegrade": false})
}

func (e *env) auditActions() []string {
	var out []string
	for _, m := range e.outbox() {
		if m["type"] == "deposit.audit.recorded" {
			out = append(out, m["payload"].(map[string]any)["action"].(string))
		}
	}
	return out
}

// trace: 第二轮 全体 1 · 管理端写接口收到只读令牌一律 403 READONLY；读接口不受影响
func TestAdminWriteRejectsReadonly(t *testing.T) {
	e := newEnv(t)
	called := false
	h := e.app.adminAuth([]string{"deposit.view"}, true, func(w http.ResponseWriter, _ *http.Request) { called = true })
	req := httptest.NewRequest("POST", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+readonlyAdminToken("deposit.view"))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"READONLY"`) || called {
		t.Fatalf("只读令牌写应 403 READONLY：%d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("POST", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken("AC0201", nil, "deposit.view"))
	rec = httptest.NewRecorder()
	h(rec, req)
	if !called {
		t.Fatalf("非只读应放行：%d", rec.Code)
	}
	r := e.do("GET", "/api/deposit/admin/summary", map[string]string{"Authorization": "Bearer " + readonlyAdminToken("deposit.view")}, nil)
	mustStatus(t, r, 200)
}

// trace: 第二轮 全体 3 · 非 UTF-8 请求体 400 INVALID
func TestNonUTF8Body(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U100087")
	gbk := append(append([]byte(`{"provider":"`), 0xCE, 0xA2, 0xD0, 0xC5), '"', '}') // GBK 字节
	post := func(path string, hdr map[string]string) resp {
		req, _ := http.NewRequest("POST", e.srv.URL+path, bytes.NewReader(gbk))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		out := resp{status: r.StatusCode, raw: raw}
		_ = json.Unmarshal(raw, &out.body)
		return out
	}
	r := post("/api/deposit/me/credit-free", map[string]string{"Authorization": "Bearer " + userToken("U100087"), "Idempotency-Key": "g1"})
	mustCode(t, r, 400, "INVALID")
	if !strings.Contains(r.body["message"].(string), "请求体不是 UTF-8 编码") || r.body["charged"] != false {
		t.Fatalf("提示不对：%s", r.raw)
	}
	r = post("/internal/deposit/deductions", map[string]string{"Authorization": "Bearer bill-key", "X-Caller": "bill", "Idempotency-Key": "g2"})
	mustCode(t, r, 400, "INVALID")
	r = post("/internal/deposit/assist-refunds", map[string]string{"Authorization": "Bearer cs-key", "X-Caller": "cs", "Idempotency-Key": "g3"})
	mustCode(t, r, 400, "INVALID")
}

// trace: 第二轮 deposit · 订阅 acct.user.registered 建押金投影；注册渠道决定免押缺省信用服务与收款渠道
func TestUserRegisteredProjection(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.registered("U200001", "支付宝")
	e.registered("U200001", "支付宝") // 重复到达不坏
	r := e.do("GET", "/api/deposit/admin/users/U200001", map[string]string{"Authorization": "Bearer " + adminToken("AC0201", nil, "user.view")}, nil)
	mustStatus(t, r, 200)
	if r.body["deposit"].(map[string]any)["mode"] != "未开通" {
		t.Fatalf("应为未开通：%s", r.raw)
	}
	// 已有的账户不被覆盖：先开免押再收到注册事件
	e.verified("U100087")
	e.registered("U100087", "微信")
	mustStatus(t, e.user("POST", "/api/deposit/me/credit-free", "U100087", "c1", nil), 200)
	e.registered("U100087", "微信")
	st := e.user("GET", "/api/deposit/me/state", "U100087", "", nil)
	if st.body["mode"] != "免押" || st.body["provider"] != "微信支付分" {
		t.Fatalf("微信注册缺省微信支付分且不被覆盖：%s", st.raw)
	}
	// 支付宝注册：免押缺省芝麻信用
	e.verified("U200001")
	r = e.user("POST", "/api/deposit/me/credit-free", "U200001", "c2", nil)
	mustStatus(t, r, 200)
	if r.body["deposit"].(map[string]any)["provider"] != "芝麻信用" {
		t.Fatalf("支付宝注册缺省芝麻信用：%s", r.raw)
	}
	// 收款渠道按注册渠道
	e.registered("U200002", "支付宝")
	e.verified("U200002")
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U200002", "p1", nil), 200)
	e.registered("U200003", "微信")
	e.verified("U200003")
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U200003", "p2", nil), 200)
	calls := e.pay.Calls()
	if len(calls) != 2 || calls[0].Body["channel"] != "支付宝" || calls[1].Body["channel"] != "微信支付" {
		t.Fatalf("收款渠道应随注册渠道：%+v", calls)
	}
}

// trace: 第二轮 deposit · 押金动作留痕：缴纳、补缴、抵扣、退还、免押开通与关闭
func TestDepositAuditActions(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.depositUser("U100004") // 缴纳
	mustStatus(t, e.deduct("d1", "U100004", "OD1", 2000), 200)
	mustStatus(t, e.user("POST", "/api/deposit/me/topup", "U100004", "t1", nil), 200)
	e.verified("U100087")
	mustStatus(t, e.user("POST", "/api/deposit/me/credit-free", "U100087", "c1", nil), 200)
	e.event("acct.account.cancelled", "U100087", map[string]any{"userId": "U100087", "cancelledAt": e.now.Format(time.RFC3339)})
	got := strings.Join(e.auditActions(), ",")
	for _, want := range []string{"缴纳押金", "抵扣押金", "补缴押金", "开通免押", "关闭免押"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺留痕动作 %s：%s", want, got)
		}
	}
	st := e.user("GET", "/api/deposit/me/state", "U100087", "", nil)
	if st.body["mode"] != "未开通" {
		t.Fatalf("注销后免押授权应关闭：%s", st.raw)
	}
	if ev := e.lastOf("deposit.status.changed"); ev["mode"] != "未开通" || ev["rideable"] != false {
		t.Fatalf("状态事件不对：%v", ev)
	}
	for _, m := range e.outbox() {
		if m["type"] != "deposit.audit.recorded" {
			continue
		}
		p := m["payload"].(map[string]any)
		if p["action"] == "缴纳押金" && (m["actor"].(map[string]any)["kind"] != "用户" || p["targetType"] != "user" || p["targetId"] != "U100004") {
			t.Fatalf("缴纳留痕不对：%v", m)
		}
	}
	mustStatus(t, e.user("POST", "/api/deposit/me/refund", "U100004", "r1", nil), 200)
	if !strings.Contains(strings.Join(e.auditActions(), ","), "申请退押") {
		t.Fatal("申请退押应留痕退押处理")
	}
}

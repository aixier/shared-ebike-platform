package iam

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"bike/iam/internal/token"
)

func tokenClaimsRaw(t *testing.T, tok string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func TestTokenBadgeNoOnlyForEnforcement(t *testing.T) {
	e := newEnv(t)
	enfTok := e.login("zf031207", "执法端")
	enf, err := e.svc.verifier.Verify(enfTok, e.now)
	if err != nil || enf.BadgeNo == nil || *enf.BadgeNo != "031207" {
		t.Fatalf("执法端令牌应带 badgeNo: %v %+v", err, enf)
	}
	adm, err := e.svc.verifier.Verify(e.login("kf.su", "管理端"), e.now)
	if err != nil || adm.BadgeNo != nil {
		t.Fatalf("管理端令牌 badgeNo 应为 null: %v %+v", err, adm)
	}
	// 线上声明里必须有 badgeNo 键，管理端为 null
	m := tokenClaimsRaw(t, e.login("kf.su", "管理端"))
	if v, ok := m["badgeNo"]; !ok || v != nil {
		t.Fatalf("badgeNo 应存在且为 null: %v", m)
	}
	if m := tokenClaimsRaw(t, enfTok); m["badgeNo"] != "031207" {
		t.Fatalf("执法令牌 badgeNo: %v", m)
	}
}

func TestTokenDefaultTTLThirtyMinutes(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/iam/login", "", map[string]any{"username": "kf.su", "password": "Shifan@2026", "client": "管理端"})
	c, err := e.svc.verifier.Verify(r.Body["token"].(string), e.now)
	if err != nil || c.ExpiresAt.Sub(c.IssuedAt) != 30*time.Minute {
		t.Fatalf("有效期应为 30 分钟: %v %v", err, c.ExpiresAt.Sub(c.IssuedAt))
	}
}

func TestRefreshToken(t *testing.T) {
	e := newEnv(t)
	old := e.login("zf031207", "执法端")
	e.now = e.now.Add(20 * time.Minute)
	r := e.do("POST", "/api/iam/token/refresh", old, nil)
	if r.Status != 200 {
		t.Fatalf("续期: %s", r.Raw)
	}
	nt := r.Body["token"].(string)
	if nt == old || r.Body["account"].(map[string]any)["accountId"] != "AC0501" {
		t.Fatalf("应换出新令牌: %s", r.Raw)
	}
	c, err := e.svc.verifier.Verify(nt, e.now)
	if err != nil || c.Client != "执法端" || c.BadgeNo == nil || *c.BadgeNo != "031207" || c.ExpiresAt.Sub(e.now) != 30*time.Minute {
		t.Fatalf("新令牌声明不对: %v %+v", err, c)
	}
	exp, _ := time.Parse(time.RFC3339, r.Body["expiresAt"].(string))
	if !exp.Equal(c.ExpiresAt) {
		t.Fatalf("expiresAt 不一致")
	}
	// 新令牌可用，旧令牌（本身未过期）会话已作废
	if r := e.do("GET", "/api/iam/me", nt, nil); r.Status != 200 {
		t.Fatalf("新令牌应可用: %s", r.Raw)
	}
	expectErr(t, e.do("GET", "/api/iam/me", old, nil), 401, "ADMIN_NOT_LOGGED_IN")
	// 过期后不能续期
	e.now = e.now.Add(31 * time.Minute)
	expectErr(t, e.do("POST", "/api/iam/token/refresh", nt, nil), 401, "ADMIN_NOT_LOGGED_IN")
	expectErr(t, e.do("POST", "/api/iam/token/refresh", "", nil), 401, "ADMIN_NOT_LOGGED_IN")
}

func TestRefreshDisabledAccount(t *testing.T) {
	e := newEnv(t)
	tok := e.login("kf.su", "管理端")
	_, _ = e.db.Exec(context.Background(), "UPDATE iam_accounts SET status='停用' WHERE username='kf.su'")
	expectErr(t, e.do("POST", "/api/iam/token/refresh", tok, nil), 403, "ACCOUNT_DISABLED")
}

func TestRefreshReadonlyStaysReadonly(t *testing.T) {
	e := newEnv(t)
	tok := e.login("qhcx.he", "管理端")
	_ = e.svc.HandleEvent(context.Background(), "fleet.operator.exited", exitedEvent("F9", "OP01"))
	r := e.do("POST", "/api/iam/token/refresh", tok, nil)
	if r.Status != 200 {
		t.Fatal(string(r.Raw))
	}
	c, _ := e.svc.verifier.Verify(r.Body["token"].(string), e.now)
	if !c.Readonly {
		t.Fatal("续期后的令牌应只读")
	}
}

// 令牌声明 readonly=true 即拒绝写操作，即便库里账号状态仍是正常。
func TestReadonlyTokenClaimBlocksWrites(t *testing.T) {
	e := newEnv(t)
	tok, _, err := e.svc.signer.Sign(token.Claims{Subject: "AC0404", Name: "t", Role: "运维账号管理员", Client: "管理端", Readonly: true, SessionID: "s-ro"}, e.now)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = e.db.Exec(context.Background(), `INSERT INTO iam_sessions(session_id, account_id, client, created_at, expires_at) VALUES ('s-ro','AC0404','管理端',$1,$2)`, e.now, e.now.Add(time.Hour))
	expectErr(t, e.do("POST", "/api/iam/accounts", tok, map[string]any{"username": "kf.x", "role": "客服", "reason": "客服扩编"}), 403, "READONLY")
	expectErr(t, e.do("PUT", "/api/iam/accounts/AC0302/status", tok, map[string]any{"status": "停用", "reason": "人员离职停用"}), 403, "READONLY")
	expectErr(t, e.do("POST", "/api/iam/password", tok, map[string]any{"oldPassword": "Shifan@2026", "newPassword": "abcd12345"}), 403, "READONLY")
}

func TestNonUTF8BodyRejected(t *testing.T) {
	e := newEnv(t)
	gbk := []byte{0xb9, 0xdc, 0xc0, 0xed, 0xb6, 0xcb} // "管理端" 的 GBK 编码
	body := append(append([]byte(`{"username":"kf.su","password":"x","client":"`), gbk...), []byte(`"}`)...)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/iam/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != 400 || out["code"] != "INVALID" || out["message"] != "请求体不是 UTF-8 编码" {
		t.Fatalf("应 400 INVALID 并提示非 UTF-8: %d %v", res.StatusCode, out)
	}
	tok := e.login("yw.hao", "管理端")
	req, _ = http.NewRequest("POST", e.srv.URL+"/api/iam/password", bytes.NewReader([]byte{'{', '"', 'a', '"', ':', '"', 0xff, '"', '}'}))
	req.Header.Set("Authorization", "Bearer "+tok)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	var out2 map[string]any
	_ = json.NewDecoder(res2.Body).Decode(&out2)
	if res2.StatusCode != 400 || out2["message"] != "请求体不是 UTF-8 编码" {
		t.Fatalf("改密码也应拒绝: %d %v", res2.StatusCode, out2)
	}
}

func TestDemoAccountStatusDistinctFromExited(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.svc.SeedDevAdmin(ctx, true); err != nil {
		t.Fatal(err)
	}
	var st string
	_ = e.db.QueryRow(ctx, "SELECT status FROM iam_accounts WHERE username='admin'").Scan(&st)
	if st != "只读（演示账号）" {
		t.Fatalf("演示账号状态: %s", st)
	}
	tok := e.login("admin", "管理端")
	me := e.do("GET", "/api/iam/me", tok, nil)
	if me.Body["readonly"] != true || me.Body["account"].(map[string]any)["status"] != "只读（演示账号）" {
		t.Fatalf("me: %s", me.Raw)
	}
	_ = e.svc.HandleEvent(ctx, "fleet.operator.exited", exitedEvent("F8", "OP01"))
	_ = e.db.QueryRow(ctx, "SELECT status FROM iam_accounts WHERE username='admin'").Scan(&st)
	if st != "只读（演示账号）" {
		t.Fatalf("演示账号不应被改: %s", st)
	}
}

// 第二轮补进动作清单的留痕：只存不判，按动作可查。
func TestIngestRound2Actions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cases := []struct{ mod, action, tt, id string }{
		{"onboard", "开始联调", "integration", "B01"},
		{"onboard", "上线品牌", "integration", "B01"},
		{"enforce", "提交取证记录", "evidence", "EV01"},
		{"settle", "提交异议", "dispute", "DS01"},
		{"pay", "执行退款", "payment", "RF01"},
		{"bill", "购买月卡", "user", "U01"},
	}
	for _, c := range cases {
		ev, _ := json.Marshal(map[string]any{
			"eventId": "R2-" + c.action, "type": c.mod + ".audit.recorded", "version": 1,
			"occurredAt": e.now.Format(time.RFC3339), "key": c.tt + ":" + c.id,
			"actor":   map[string]any{"kind": "人", "id": "AC0404", "name": nil},
			"payload": map[string]any{"action": c.action, "targetType": c.tt, "targetId": c.id, "summary": "第二轮动作 " + c.action},
		})
		if err := e.svc.HandleEvent(ctx, c.mod+".audit.recorded", ev); err != nil {
			t.Fatal(err)
		}
	}
	tok := e.login("zgbm.zhou", "管理端")
	acts := e.do("GET", "/api/iam/audit/actions", tok, nil)
	for _, c := range cases {
		if !bytes.Contains(acts.Raw, []byte(c.action)) {
			t.Fatalf("动作 %s 应可查: %s", c.action, acts.Raw)
		}
		r := e.do("GET", "/api/iam/audit?action="+urlq(c.action), tok, nil)
		if r.Status != 200 || r.Body["total"] != float64(1) {
			t.Fatalf("按动作 %s 查: %s", c.action, r.Raw)
		}
	}
}

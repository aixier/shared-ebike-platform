package iam

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"testing"
	"time"

	"bike/iam/internal/token"
)

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/healthz", "", nil)
	if r.Status != 200 || r.Body["ok"] != true {
		t.Fatalf("healthz: %d %s", r.Status, r.Raw)
	}
}

// 原型 accounts.json 的每个可登录账号都能用初始密码从正确的端登录；令牌声明齐全。
func TestLoginAllPrototypeAccounts(t *testing.T) {
	e := newEnv(t)
	raw, err := os.ReadFile("testdata/accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Accounts []struct {
			AccountID     string   `json:"accountId"`
			Role          string   `json:"role"`
			Username      *string  `json:"username"`
			OperatorScope *string  `json:"operatorScope"`
			Permissions   []string `json:"permissions"`
			Menus         []string `json:"menus"`
			Status        string   `json:"status"`
		} `json:"accounts"`
	}
	_ = json.Unmarshal(raw, &data)
	if len(data.Accounts) != 28 {
		t.Fatalf("样例账号数应为 28，实际 %d", len(data.Accounts))
	}
	v := token.NewVerifier(map[string]ed25519.PublicKey{"k2026": e.pub})
	for _, a := range data.Accounts {
		if a.Username == nil {
			continue
		}
		client := "管理端"
		if a.Role == "执法人员" {
			client = "执法端"
		}
		r := e.do("POST", "/api/iam/login", "", map[string]any{"username": *a.Username, "password": "Shifan@2026", "client": client})
		if r.Status != 200 {
			t.Fatalf("%s 登录失败: %s", *a.Username, r.Raw)
		}
		c, err := v.Verify(r.Body["token"].(string), e.now)
		if err != nil {
			t.Fatal(err)
		}
		if c.Subject != a.AccountID || c.Role != a.Role || c.Client != client {
			t.Fatalf("%s 令牌声明不对: %+v", *a.Username, c)
		}
		if (a.OperatorScope == nil) != (c.Scope == nil) || (c.Scope != nil && *c.Scope != *a.OperatorScope) {
			t.Fatalf("%s scope 不对", *a.Username)
		}
		if c.Readonly != (a.Status == "只读（运营商已退出）") {
			t.Fatalf("%s readonly 不对", *a.Username)
		}
		for _, p := range a.Permissions {
			if !contains(c.Perms, p) {
				t.Fatalf("%s 缺权限点 %s", *a.Username, p)
			}
		}
		acc := r.Body["account"].(map[string]any)
		if _, has := acc["password"]; has {
			t.Fatal("返回不得含密码")
		}
		if acc["lastLoginAt"] == nil {
			t.Fatal("应记最近登录时间")
		}
		if r.Body["expiresAt"] != e.now.Add(30*time.Minute).Format(time.RFC3339) {
			t.Fatalf("expiresAt 不对: %v", r.Body["expiresAt"])
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestLoginFailures(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{"密码错", map[string]any{"username": "kf.su", "password": "wrong", "client": "管理端"}, 401, "LOGIN_FAILED"},
		{"无此账号", map[string]any{"username": "nobody", "password": "Shifan@2026", "client": "管理端"}, 401, "LOGIN_FAILED"},
		{"执法人员从管理端登录", map[string]any{"username": "zf031207", "password": "Shifan@2026", "client": "管理端"}, 401, "LOGIN_FAILED"},
		{"管理岗位从执法端登录", map[string]any{"username": "kf.su", "password": "Shifan@2026", "client": "执法端"}, 401, "LOGIN_FAILED"},
		{"缺 client", map[string]any{"username": "kf.su", "password": "Shifan@2026"}, 400, "INVALID"},
		{"client 非法", map[string]any{"username": "kf.su", "password": "Shifan@2026", "client": "手机"}, 400, "INVALID"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectErr(t, e.do("POST", "/api/iam/login", "", c.body), c.status, c.code)
		})
	}
	// 反例：智能体账号无账号名，任何账号名都对不上
	expectErr(t, e.do("POST", "/api/iam/login", "", map[string]any{"username": "运行智能体", "password": "", "client": "管理端"}), 401, "LOGIN_FAILED")
}

func TestLoginDisabledAccount(t *testing.T) {
	e := newEnv(t)
	_, _ = e.db.Exec(context.Background(), "UPDATE iam_accounts SET status='停用' WHERE username='kf.zhong'")
	expectErr(t, e.do("POST", "/api/iam/login", "", map[string]any{"username": "kf.zhong", "password": "Shifan@2026", "client": "管理端"}), 403, "ACCOUNT_DISABLED")
	// 停用账号密码错仍按登录失败，不泄露账号状态
	expectErr(t, e.do("POST", "/api/iam/login", "", map[string]any{"username": "kf.zhong", "password": "x", "client": "管理端"}), 401, "LOGIN_FAILED")
}

func TestReadonlyAccountCanLogin(t *testing.T) {
	e := newEnv(t)
	tok := e.login("jtgx.wei", "管理端")
	r := e.do("GET", "/api/iam/me", tok, nil)
	if r.Status != 200 || r.Body["readonly"] != true {
		t.Fatalf("只读账号 me: %s", r.Raw)
	}
	op := r.Body["operator"].(map[string]any)
	if op["operatorId"] != "OP03" {
		t.Fatalf("operator 不对: %s", r.Raw)
	}
}

func TestMe(t *testing.T) {
	e := newEnv(t)
	tok := e.login("kf.tan", "管理端")
	r := e.do("GET", "/api/iam/me", tok, nil)
	if r.Status != 200 {
		t.Fatal(string(r.Raw))
	}
	if r.Body["readonly"] != false || r.Body["operator"] != nil {
		t.Fatalf("me 不对: %s", r.Raw)
	}
	perms := toStrings(r.Body["permissions"])
	if !contains(perms, "refund.confirm.overLimit") {
		t.Fatal("客服主管应有超额退款确认")
	}
	if !contains(toStrings(r.Body["menus"]), "操作留痕") {
		t.Fatal("菜单不对")
	}
	// 执法端
	ztok := e.login("zf031322", "执法端")
	zr := e.do("GET", "/api/iam/me", ztok, nil)
	acc := zr.Body["account"].(map[string]any)
	if acc["badgeNo"] != "031322" || acc["role"] != "执法人员" {
		t.Fatalf("执法 me 不对: %s", zr.Raw)
	}
	// 未登录
	expectErr(t, e.do("GET", "/api/iam/me", "", nil), 401, "ADMIN_NOT_LOGGED_IN")
	expectErr(t, e.do("GET", "/api/iam/me", "garbage", nil), 401, "ADMIN_NOT_LOGGED_IN")
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestOpsRolesHaveExtraPermissions(t *testing.T) {
	e := newEnv(t)
	tok := e.login("yw.mao", "管理端")
	perms := toStrings(e.do("GET", "/api/iam/me", tok, nil).Body["permissions"])
	for _, p := range []string{"credential.manage", "platformParams.edit", "acceptance.confirm"} {
		if !contains(perms, p) {
			t.Fatalf("运维接入负责人缺 %s", p)
		}
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	tok := e.login("kf.su", "管理端")
	if r := e.do("POST", "/api/iam/logout", tok, nil); r.Status != 200 || r.Body["ok"] != true {
		t.Fatal(string(r.Raw))
	}
	// 会话作废
	expectErr(t, e.do("GET", "/api/iam/me", tok, nil), 401, "ADMIN_NOT_LOGGED_IN")
	// 令牌已失效时也返回成功
	if r := e.do("POST", "/api/iam/logout", tok, nil); r.Status != 200 {
		t.Fatal(string(r.Raw))
	}
	if r := e.do("POST", "/api/iam/logout", "", nil); r.Status != 200 {
		t.Fatal(string(r.Raw))
	}
}

func TestTokenExpiry(t *testing.T) {
	e := newEnv(t)
	tok := e.login("kf.su", "管理端")
	e.now = e.now.Add(31 * time.Minute)
	expectErr(t, e.do("GET", "/api/iam/me", tok, nil), 401, "ADMIN_NOT_LOGGED_IN")
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	tok := e.login("kf.su", "管理端")
	p := "/api/iam/password"
	expectErr(t, e.do("POST", p, tok, map[string]any{"oldPassword": "bad", "newPassword": "abcd1234"}), 401, "LOGIN_FAILED")
	expectErr(t, e.do("POST", p, tok, map[string]any{"oldPassword": "Shifan@2026", "newPassword": "abc123"}), 400, "INVALID")
	expectErr(t, e.do("POST", p, tok, map[string]any{"oldPassword": "Shifan@2026", "newPassword": "abcdefgh"}), 400, "INVALID")
	expectErr(t, e.do("POST", p, tok, map[string]any{"oldPassword": "Shifan@2026", "newPassword": "12345678"}), 400, "INVALID")
	expectErr(t, e.do("POST", p, tok, map[string]any{"oldPassword": "Shifan@2026", "newPassword": "Shifan@2026"}), 400, "INVALID")
	r := e.do("POST", p, tok, map[string]any{"oldPassword": "Shifan@2026", "newPassword": "newpass2026"}, "Idempotency-Key", "pw-1")
	if r.Status != 200 || r.Body["ok"] != true {
		t.Fatal(string(r.Raw))
	}
	// 同一幂等键重复到达：返回第一次结果，不再执行（旧密码已不对也不报错）
	r2 := e.do("POST", p, tok, map[string]any{"oldPassword": "Shifan@2026", "newPassword": "newpass2026"}, "Idempotency-Key", "pw-1")
	if r2.Status != 200 {
		t.Fatalf("幂等重放应返回第一次结果: %s", r2.Raw)
	}
	// 新密码可登录，旧密码不行
	expectErr(t, e.do("POST", "/api/iam/login", "", map[string]any{"username": "kf.su", "password": "Shifan@2026", "client": "管理端"}), 401, "LOGIN_FAILED")
	if r := e.do("POST", "/api/iam/login", "", map[string]any{"username": "kf.su", "password": "newpass2026", "client": "管理端"}); r.Status != 200 {
		t.Fatal(string(r.Raw))
	}
	// 留痕"修改密码"，操作人是本人；密码不进留痕
	var n int
	var before, after *string
	_ = e.db.QueryRow(context.Background(), "SELECT count(*) FROM iam_audit WHERE action='修改密码' AND account_id='AC0301' AND actor_kind='人' AND target_type='account' AND target_id='AC0301'").Scan(&n)
	if n != 1 {
		t.Fatalf("修改密码留痕应为 1 条，实际 %d", n)
	}
	_ = e.db.QueryRow(context.Background(), "SELECT before::text, after::text FROM iam_audit WHERE action='修改密码'").Scan(&before, &after)
	if before != nil || after != nil {
		t.Fatal("密码不应进留痕")
	}
	// 发件表有留痕事件
	_ = e.db.QueryRow(context.Background(), "SELECT count(*) FROM iam_outbox WHERE subject='iam.audit.recorded'").Scan(&n)
	if n != 1 {
		t.Fatalf("应有 1 条留痕事件待发，实际 %d", n)
	}
}

func TestChangePasswordReadonly(t *testing.T) {
	e := newEnv(t)
	tok := e.login("jtgx.wei", "管理端")
	expectErr(t, e.do("POST", "/api/iam/password", tok, map[string]any{"oldPassword": "Shifan@2026", "newPassword": "abcd12345"}), 403, "READONLY")
}

func TestTokenKeys(t *testing.T) {
	e := newEnv(t)
	p := "/internal/iam/token-keys"
	expectErr(t, e.do("GET", p, "", nil), 401, "UNAUTHORIZED")
	expectErr(t, e.do("GET", p, "wrong", nil, "X-Caller", "ride"), 401, "UNAUTHORIZED")
	expectErr(t, e.do("GET", p, "ride-secret", nil, "X-Caller", "fleet"), 401, "UNAUTHORIZED")
	r := e.do("GET", p, "ride-secret", nil, "X-Caller", "ride")
	if r.Status != 200 {
		t.Fatal(string(r.Raw))
	}
	keys := r.Body["keys"].([]any)
	k := keys[0].(map[string]any)
	if k["kid"] != "k2026" || k["alg"] != "EdDSA" || k["publicKey"] != token.PublicKeyPEM(e.pub) {
		t.Fatalf("公钥不对: %s", r.Raw)
	}
}

package iam

import (
	"context"
	"encoding/json"
	"testing"
)

func TestListAccounts(t *testing.T) {
	e := newEnv(t)
	tok := e.login("yw.hao", "管理端")
	r := e.do("GET", "/api/iam/accounts?pageSize=200", tok, nil)
	if r.Status != 200 {
		t.Fatal(string(r.Raw))
	}
	if r.Body["total"].(float64) != 28 || r.Body["page"].(float64) != 1 || r.Body["pageSize"].(float64) != 200 {
		t.Fatalf("分页不对: %v %v %v", r.Body["total"], r.Body["page"], r.Body["pageSize"])
	}
	rows := r.Body["rows"].([]any)
	var agent map[string]any
	for _, x := range rows {
		m := x.(map[string]any)
		if _, has := m["password"]; has {
			t.Fatal("不得返回密码")
		}
		if _, has := m["passwordHash"]; has {
			t.Fatal("不得返回密码哈希")
		}
		if m["accountId"] == "AC0901" {
			agent = m
		}
	}
	if agent == nil || agent["username"] != nil || agent["agentRole"] == nil || agent["role"] != "智能体" {
		t.Fatalf("应含智能体账号，username 为 null: %v", agent)
	}
	// 按岗位筛选
	r = e.do("GET", "/api/iam/accounts?role="+urlq("客服"), tok, nil)
	if r.Body["total"].(float64) != 3 {
		t.Fatalf("客服应 3 个: %s", r.Raw)
	}
	// 按机构包含匹配，分页
	r = e.do("GET", "/api/iam/accounts?q="+urlq("执法支队")+"&page=2&pageSize=3", tok, nil)
	if r.Body["total"].(float64) != 4 || len(r.Body["rows"].([]any)) != 1 {
		t.Fatalf("q 与分页不对: %s", r.Raw)
	}
	// 按账号名匹配
	r = e.do("GET", "/api/iam/accounts?q=qhcx", tok, nil)
	if r.Body["total"].(float64) != 2 {
		t.Fatalf("账号名匹配不对: %s", r.Raw)
	}
	// 无权限
	ktok := e.login("kf.su", "管理端")
	expectErr(t, e.do("GET", "/api/iam/accounts", ktok, nil), 403, "FORBIDDEN")
}

func TestCreateAccount(t *testing.T) {
	e := newEnv(t)
	tok := e.login("yw.hao", "管理端")
	p := "/api/iam/accounts"
	body := map[string]any{"username": "kf.new", "name": "新客服", "role": "客服", "reason": "客服中心扩编"}
	r := e.do("POST", p, tok, body, "Idempotency-Key", "c-1")
	if r.Status != 201 {
		t.Fatal(string(r.Raw))
	}
	if r.Body["accountId"] != "AC0305" || r.Body["status"] != "正常" || r.Body["createdBy"] != "AC0404" || r.Body["post"] != "平台公司客服" {
		t.Fatalf("开通结果不对: %s", r.Raw)
	}
	if !contains(toStrings(r.Body["permissions"]), "refund.confirm") || contains(toStrings(r.Body["permissions"]), "refund.confirm.overLimit") {
		t.Fatal("权限点应按岗位给")
	}
	if r.Body["org"] != "示范市城市出行服务有限公司 客服中心" {
		t.Fatal("机构缺省应取岗位默认机构")
	}
	// 幂等重放返回同一结果
	r2 := e.do("POST", p, tok, body, "Idempotency-Key", "c-1")
	if r2.Status != 201 || r2.Body["accountId"] != "AC0305" {
		t.Fatalf("幂等重放不对: %s", r2.Raw)
	}
	var n int
	_ = e.db.QueryRow(context.Background(), "SELECT count(*) FROM iam_accounts WHERE username='kf.new'").Scan(&n)
	if n != 1 {
		t.Fatal("不应重复开通")
	}
	// 用统一初始密码登录
	e.login("kf.new", "管理端")
	// 留痕与账号变更事件
	_ = e.db.QueryRow(context.Background(), "SELECT count(*) FROM iam_audit WHERE action='开通账号' AND target_id='AC0305' AND account_id='AC0404' AND reason='客服中心扩编'").Scan(&n)
	if n != 1 {
		t.Fatal("应记留痕账号开通")
	}
	var bodyJSON []byte
	_ = e.db.QueryRow(context.Background(), "SELECT body FROM iam_outbox WHERE subject='iam.account.changed'").Scan(&bodyJSON)
	var env map[string]any
	_ = json.Unmarshal(bodyJSON, &env)
	pl := env["payload"].(map[string]any)
	if env["type"] != "iam.account.changed" || env["key"] != "AC0305" || pl["change"] != "开通" || pl["status"] != "正常" || pl["role"] != "客服" {
		t.Fatalf("账号变更事件不对: %s", bodyJSON)
	}
	if _, ok := pl["operatorScope"]; !ok {
		t.Fatal("无值字段应给 null 不省略")
	}
}

func TestCreateAccountValidation(t *testing.T) {
	e := newEnv(t)
	tok := e.login("yw.hao", "管理端")
	p := "/api/iam/accounts"
	cases := []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{"缺事由", map[string]any{"username": "abc", "role": "客服"}, 400, "REASON_REQUIRED"},
		{"事由太短", map[string]any{"username": "abc", "role": "客服", "reason": "扩编"}, 400, "REASON_REQUIRED"},
		{"账号名含大写", map[string]any{"username": "Abc", "role": "客服", "reason": "客服扩编"}, 400, "INVALID"},
		{"账号名太短", map[string]any{"username": "ab", "role": "客服", "reason": "客服扩编"}, 400, "INVALID"},
		{"账号名重复", map[string]any{"username": "kf.su", "role": "客服", "reason": "客服扩编"}, 400, "INVALID"},
		{"岗位不可开通", map[string]any{"username": "agent.x", "role": "智能体", "reason": "新增智能体"}, 400, "INVALID"},
		{"运营商岗位缺运营商", map[string]any{"username": "op.x", "role": "运营商业务", "reason": "新增运营商账号"}, 400, "INVALID"},
		{"运营商编号格式错", map[string]any{"username": "op.x", "role": "运营商业务", "operatorScope": "青禾", "reason": "新增运营商账号"}, 400, "INVALID"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectErr(t, e.do("POST", p, tok, c.body), c.status, c.code)
		})
	}
	// 非运营商岗位即使传了运营商也不带数据范围
	r := e.do("POST", p, tok, map[string]any{"username": "cw.new", "role": "财务", "operatorScope": "OP01", "reason": "财务部扩编"})
	if r.Status != 201 || r.Body["operatorScope"] != nil {
		t.Fatalf("非运营商岗位不应带数据范围: %s", r.Raw)
	}
	// 运营商岗位带数据范围
	r = e.do("POST", p, tok, map[string]any{"username": "qhcx.new", "role": "运营商技术对接", "operatorScope": "OP01", "org": "青禾出行科技有限公司", "reason": "青禾新增对接人"})
	if r.Status != 201 || r.Body["operatorScope"] != "OP01" || r.Body["accountId"] != "AC0115" {
		t.Fatalf("运营商账号开通不对: %s", r.Raw)
	}
	// 已退出运营商新开账号直接只读
	r = e.do("POST", p, tok, map[string]any{"username": "jtgx.new", "role": "运营商业务", "operatorScope": "OP03", "reason": "补开查询账号"})
	if r.Status != 201 || r.Body["status"] != "只读（运营商已退出）" {
		t.Fatalf("已退出运营商的账号应只读: %s", r.Raw)
	}
	// 执法人员账号名为 zf+工号时，工号作执法证号
	r = e.do("POST", p, tok, map[string]any{"username": "zf031999", "role": "执法人员", "reason": "新进执法人员"})
	if r.Status != 201 || r.Body["badgeNo"] != "031999" || r.Body["accountId"] != "AC0505" {
		t.Fatalf("执法账号不对: %s", r.Raw)
	}
	// 无权限
	ktok := e.login("kf.su", "管理端")
	expectErr(t, e.do("POST", p, ktok, map[string]any{"username": "kf.x", "role": "客服", "reason": "客服扩编"}), 403, "FORBIDDEN")
}

func TestSetAccountStatus(t *testing.T) {
	e := newEnv(t)
	tok := e.login("yw.hao", "管理端")
	victim := e.login("kf.zhong", "管理端")
	p := "/api/iam/accounts/AC0302/status"
	r := e.do("PUT", p, tok, map[string]any{"status": "停用", "reason": "人员离职停用"}, "Idempotency-Key", "s-1")
	if r.Status != 200 || r.Body["status"] != "停用" {
		t.Fatal(string(r.Raw))
	}
	// 已签发的会话同时作废；再登录 ACCOUNT_DISABLED
	expectErr(t, e.do("GET", "/api/iam/me", victim, nil), 403, "ACCOUNT_DISABLED")
	expectErr(t, e.do("POST", "/api/iam/login", "", map[string]any{"username": "kf.zhong", "password": "Shifan@2026", "client": "管理端"}), 403, "ACCOUNT_DISABLED")
	var before, after string
	_ = e.db.QueryRow(context.Background(), "SELECT before::text, after::text FROM iam_audit WHERE action='调整权限' AND target_id='AC0302'").Scan(&before, &after)
	if before != `{"status": "正常"}` || after != `{"status": "停用"}` {
		t.Fatalf("留痕前后值不对: %s %s", before, after)
	}
	// 恢复
	r = e.do("PUT", p, tok, map[string]any{"status": "正常", "reason": "返岗恢复账号"})
	if r.Status != 200 || r.Body["status"] != "正常" {
		t.Fatal(string(r.Raw))
	}
	// 旧会话不复活
	expectErr(t, e.do("GET", "/api/iam/me", victim, nil), 401, "ADMIN_NOT_LOGGED_IN")
	var changes []string
	rows, _ := e.db.Query(context.Background(), "SELECT body->'payload'->>'change' FROM iam_outbox WHERE subject='iam.account.changed' ORDER BY seq")
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		changes = append(changes, c)
	}
	rows.Close()
	if len(changes) != 2 || changes[0] != "停用" || changes[1] != "启用" {
		t.Fatalf("账号变更事件不对: %v", changes)
	}

	expectErr(t, e.do("PUT", "/api/iam/accounts/AC0901/status", tok, map[string]any{"status": "停用", "reason": "停用智能体账号"}), 400, "INVALID")
	expectErr(t, e.do("PUT", "/api/iam/accounts/AC9999/status", tok, map[string]any{"status": "停用", "reason": "停用不存在账号"}), 404, "NOT_FOUND")
	expectErr(t, e.do("PUT", p, tok, map[string]any{"status": "只读（运营商已退出）", "reason": "尝试设只读"}), 400, "INVALID")
	expectErr(t, e.do("PUT", p, tok, map[string]any{"status": "停用"}), 400, "REASON_REQUIRED")
	expectErr(t, e.do("PUT", "/api/iam/accounts/AC0404/status", tok, map[string]any{"status": "停用", "reason": "停用我自己"}), 400, "INVALID")
}

// 已退出运营商的账号恢复后回到只读，不会变回正常。
func TestRestoreExitedOperatorAccountStaysReadonly(t *testing.T) {
	e := newEnv(t)
	tok := e.login("yw.hao", "管理端")
	p := "/api/iam/accounts/AC0103/status"
	if r := e.do("PUT", p, tok, map[string]any{"status": "停用", "reason": "退出后停用账号"}); r.Body["status"] != "停用" {
		t.Fatal(string(r.Raw))
	}
	r := e.do("PUT", p, tok, map[string]any{"status": "正常", "reason": "恢复查询权限"})
	if r.Body["status"] != "只读（运营商已退出）" {
		t.Fatalf("应回到只读: %s", r.Raw)
	}
}

func TestReadonlyAccountManagerCannotWrite(t *testing.T) {
	e := newEnv(t)
	// 假设账号管理员处于只读（数据构造），写操作返回 READONLY
	_, _ = e.db.Exec(context.Background(), "UPDATE iam_accounts SET status='只读（运营商已退出）' WHERE account_id='AC0404'")
	tok := e.login("yw.hao", "管理端")
	expectErr(t, e.do("POST", "/api/iam/accounts", tok, map[string]any{"username": "kf.x", "role": "客服", "reason": "客服扩编"}), 403, "READONLY")
	expectErr(t, e.do("PUT", "/api/iam/accounts/AC0302/status", tok, map[string]any{"status": "停用", "reason": "人员离职停用"}), 403, "READONLY")
	// 读仍可以
	if r := e.do("GET", "/api/iam/accounts", tok, nil); r.Status != 200 {
		t.Fatal(string(r.Raw))
	}
}

func TestAccountInvariantsInDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// 运营商账号必须带数据范围
	if _, err := e.db.Exec(ctx, "UPDATE iam_accounts SET operator_scope=NULL WHERE account_id='AC0101'"); err == nil {
		t.Fatal("运营商账号去掉数据范围应被数据库拒绝")
	}
	// 智能体账号不能有账号名
	if _, err := e.db.Exec(ctx, "UPDATE iam_accounts SET username='agent' WHERE account_id='AC0901'"); err == nil {
		t.Fatal("智能体账号设账号名应被数据库拒绝")
	}
}

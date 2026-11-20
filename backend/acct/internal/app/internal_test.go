package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInternalAuth(t *testing.T) {
	e := newEnv(t)
	uid, _ := e.login("13812345678")
	if r := e.do("GET", "/internal/acct/users/"+uid, nil, nil); r.status != 401 {
		t.Fatalf("无凭证应 401: %d", r.status)
	}
	if r := e.do("GET", "/internal/acct/users/"+uid, nil, internalHdr("cs", "wrong", "")); r.status != 401 {
		t.Fatalf("凭证不对应 401: %d", r.status)
	}
	if r := e.do("GET", "/internal/acct/users/"+uid, nil, internalHdr("ride", "cs-key", "")); r.status != 401 {
		t.Fatalf("调用方不在表里应 401: %d", r.status)
	}
	r := e.do("GET", "/internal/acct/users/"+uid, nil, internalHdr("cs", "cs-key", ""))
	if r.status != 200 || r.str("userId") != uid || r.str("phoneMasked") != "138****5678" {
		t.Fatalf("读用户: %d %v", r.status, r.body)
	}
	if _, has := r.body["phone"]; has {
		t.Fatal("内部读用户不含完整手机号")
	}
	if r := e.do("GET", "/internal/acct/users/U999999", nil, internalHdr("cs", "cs-key", "")); r.status != 404 || r.str("code") != "NOT_FOUND" {
		t.Fatalf("不存在应 404: %v", r.body)
	}
}

func TestInternalLookup(t *testing.T) {
	e := newEnv(t)
	uid, _ := e.login("13812345678")
	e.login("13912345678")
	h := internalHdr("cs", "cs-key", "")
	r := e.do("GET", "/internal/acct/users/lookup?phone=13812345678", nil, h)
	users := r.body["users"].([]any)
	if r.status != 200 || len(users) != 1 || users[0].(map[string]any)["userId"] != uid || users[0].(map[string]any)["accountStatus"] != "正常" {
		t.Fatalf("按手机号找: %v", r.body)
	}
	if _, has := users[0].(map[string]any)["phone"]; has {
		t.Fatal("不得返回完整手机号")
	}
	r = e.do("GET", "/internal/acct/users/lookup?phoneTail=5678", nil, h)
	if len(r.body["users"].([]any)) != 2 {
		t.Fatalf("按尾号应命中 2 个: %v", r.body)
	}
	for _, q := range []string{"", "?phoneTail=123", "?phone=123", "?phone=13812345678&phoneTail=5678"} {
		if r := e.do("GET", "/internal/acct/users/lookup"+q, nil, h); r.status != 400 || r.str("code") != "INVALID" {
			t.Errorf("%q 应 INVALID: %d %v", q, r.status, r.body)
		}
	}
}

func TestInternalUnfreeze(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.login("13812345678")
	cmd := map[string]any{"reason": "用户已线下说明情况", "actor": map[string]any{"kind": "人", "id": "AC0301", "name": "苏＊＊"}, "ticketId": "TK1"}
	if r := e.do("POST", "/internal/acct/users/"+uid+"/unfreeze", cmd, internalHdr("cs", "cs-key", "")); r.status != 400 || r.str("code") != "INVALID" {
		t.Fatalf("缺幂等键应 400 INVALID: %d %v", r.status, r.body)
	}
	if r := e.do("POST", "/internal/acct/users/"+uid+"/unfreeze", cmd, internalHdr("cs", "cs-key", "u0")); r.status != 409 || r.str("code") != "CONFLICT" {
		t.Fatalf("未冻结应 CONFLICT: %d %v", r.status, r.body)
	}
	bad := map[string]any{"reason": "短", "actor": cmd["actor"]}
	if r := e.do("POST", "/internal/acct/users/"+uid+"/unfreeze", bad, internalHdr("cs", "cs-key", "u1")); r.status != 400 || r.str("code") != "REASON_REQUIRED" {
		t.Fatalf("事由不足 4 字应 REASON_REQUIRED: %d %v", r.status, r.body)
	}
	e.event("b1", "bill.arrears.overdue", uid, map[string]any{"userId": uid, "userArrearsTotal": 100, "days": 31})
	r := e.do("POST", "/internal/acct/users/"+uid+"/unfreeze", cmd, internalHdr("cs", "cs-key", "u2"))
	if r.status != 200 || r.body["account"].(map[string]any)["status"] != "正常" {
		t.Fatalf("人工解冻: %d %v", r.status, r.body)
	}
	h := r.body["account"].(map[string]any)["history"].([]any)
	last := h[len(h)-1].(map[string]any)
	if last["action"] != "人工解冻" || last["by"] != "AC0301" || last["reason"] != "用户已线下说明情况" {
		t.Fatalf("记录不对: %v", last)
	}
	// 幂等重放
	r2 := e.do("POST", "/internal/acct/users/"+uid+"/unfreeze", cmd, internalHdr("cs", "cs-key", "u2"))
	if r2.status != 200 {
		t.Fatalf("同一幂等键应返回第一次结果: %d %v", r2.status, r2.body)
	}
	un := e.outbox("acct.account.unfrozen")
	if len(un) != 1 || un[0]["payload"].(map[string]any)["mode"] != "人工解冻" || un[0]["actor"].(map[string]any)["id"] != "AC0301" {
		t.Fatalf("应发人工解冻一次: %v", un)
	}
	found := false
	for _, a := range e.outbox("acct.audit.recorded") {
		p := a["payload"].(map[string]any)
		if p["action"] == "解冻账户" && p["targetId"] == uid && p["reason"] == "用户已线下说明情况" && strings.Contains(p["summary"].(string), "TK1") {
			found = true
		}
	}
	if !found {
		t.Fatal("人工解冻应留痕且记工单号")
	}
	if me := e.do("GET", "/api/acct/me", nil, bearer(tok)); me.body["unreadNotices"].(float64) < 2 {
		t.Fatal("解冻应通知用户")
	}
}

func TestInternalCancel(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.login("13812345678")
	cmd := map[string]any{"reason": "用户来电申请注销", "actor": map[string]any{"kind": "人", "id": "AC0301"}}
	e.event("r1", "ride.order.created", "OD1", map[string]any{"orderId": "OD1", "userId": uid})
	r := e.do("POST", "/internal/acct/users/"+uid+"/cancel", cmd, internalHdr("cs", "cs-key", "c1"))
	if r.status != 409 || r.str("code") != "CANCEL_BLOCKED" || len(r.body["blockers"].([]any)) != 1 {
		t.Fatalf("有骑行中应 CANCEL_BLOCKED: %d %v", r.status, r.body)
	}
	e.event("r2", "ride.order.unlock_failed", "OD1", map[string]any{"orderId": "OD1", "userId": uid, "reason": "车辆无响应"})
	r = e.do("POST", "/internal/acct/users/"+uid+"/cancel", cmd, internalHdr("cs", "cs-key", "c2"))
	if r.status != 200 || r.str("userId") != uid || r.str("cancelledAt") == "" {
		t.Fatalf("协助注销: %d %v", r.status, r.body)
	}
	if r := e.do("GET", "/api/acct/notices", nil, bearer(tok)); r.status != 401 {
		t.Fatal("注销后令牌应作废")
	}
	if r := e.do("POST", "/internal/acct/users/"+uid+"/cancel", cmd, internalHdr("cs", "cs-key", "c3")); r.status != 409 || r.str("code") != "ALREADY_DONE" {
		t.Fatalf("已注销应 ALREADY_DONE: %v", r.body)
	}
	if r := e.do("POST", "/internal/acct/users/U999999/cancel", cmd, internalHdr("cs", "cs-key", "c4")); r.status != 404 {
		t.Fatalf("不存在应 404: %d", r.status)
	}
	found := false
	for _, a := range e.outbox("acct.audit.recorded") {
		if p := a["payload"].(map[string]any); p["action"] == "注销账户" && p["targetId"] == uid {
			found = true
		}
	}
	if !found {
		t.Fatal("注销应留痕")
	}
}

func TestPayeeNameCheck(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.login("13812345678")
	h := internalHdr("pay", "pay-key", "")
	if r := e.do("POST", "/internal/acct/users/"+uid+"/payee-name-check", map[string]any{"name": "张三丰"}, h); r.body["match"] != false {
		t.Fatalf("未实名应 false: %v", r.body)
	}
	e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, bearer(tok))
	if r := e.do("POST", "/internal/acct/users/"+uid+"/payee-name-check", map[string]any{"name": " 张三丰 "}, h); r.status != 200 || r.body["match"] != true {
		t.Fatalf("一致应 true: %v", r.body)
	}
	r := e.do("POST", "/internal/acct/users/"+uid+"/payee-name-check", map[string]any{"name": "张三"}, h)
	if r.body["match"] != false {
		t.Fatalf("不一致应 false: %v", r.body)
	}
	if _, has := r.body["name"]; has {
		t.Fatal("实名姓名不出本模块")
	}
	if r := e.do("POST", "/internal/acct/users/U999999/payee-name-check", map[string]any{"name": "张三"}, h); r.status != 404 {
		t.Fatalf("不存在应 404")
	}
}

func TestTokenKeys(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/internal/acct/token-keys", nil, internalHdr("cs", "cs-key", ""))
	keys := r.body["keys"].([]any)
	k := keys[0].(map[string]any)
	if r.status != 200 || len(keys) != 1 || k["kid"] != "u1" || k["alg"] != "EdDSA" || !strings.Contains(k["publicKey"].(string), "BEGIN PUBLIC KEY") {
		t.Fatalf("公钥: %v", r.body)
	}
}

func TestAdminFrozenAndCsUser(t *testing.T) {
	e := newEnv(t)
	uid, _ := e.login("13812345678")
	uid2, _ := e.login("13912340000")
	e.login("13712345678")
	e.event("b1", "bill.arrears.overdue", uid, map[string]any{"userId": uid, "userArrearsTotal": 100, "days": 31})
	e.clk.Add(48 * time.Hour)
	degradeEnter(e, "OPS-DG-9", "实名核验")
	// uid2 走先骑后验后逾期冻结
	_, tok2 := e.login("13912340000")
	e.rn.SetDown(true)
	e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, bearer(tok2))
	e.clk.Add(8 * 24 * time.Hour)
	_ = e.app.RunJobs(context.Background())

	if r := e.do("GET", "/api/acct/admin/frozen-accounts", nil, nil); r.status != 401 || r.str("code") != "ADMIN_NOT_LOGGED_IN" {
		t.Fatalf("未登录应 ADMIN_NOT_LOGGED_IN: %d %v", r.status, r.body)
	}
	if r := e.do("GET", "/api/acct/admin/frozen-accounts", nil, bearer(e.adminToken([]string{"order.view"}, nil))); r.status != 403 || r.str("code") != "FORBIDDEN" {
		t.Fatalf("无权限应 FORBIDDEN: %d", r.status)
	}
	if r := e.do("GET", "/api/acct/admin/frozen-accounts", nil, bearer(e.adminToken([]string{"user.view"}, "OP01"))); r.status != 403 {
		t.Fatalf("运营商账号不能看用户: %d", r.status)
	}
	adm := bearer(e.adminToken([]string{"account.unfreeze"}, nil))
	r := e.do("GET", "/api/acct/admin/frozen-accounts", nil, adm)
	if r.status != 200 || r.body["total"] != float64(2) {
		t.Fatalf("冻结列表: %d %v", r.status, r.body)
	}
	rows := r.body["rows"].([]any)
	first := rows[0].(map[string]any)
	if first["userId"] != uid2 || first["kind"] != "核验" || first["autoUnfreeze"] != true || first["realNameStatus"] != "待核验" {
		t.Fatalf("应新到旧且字段正确: %v", first)
	}
	second := rows[1].(map[string]any)
	if second["kind"] != "欠费" || second["frozenDays"] != float64(10) || second["phoneMasked"] != "138****5678" {
		t.Fatalf("第二行不对: %v", second)
	}
	counts := r.body["counts"].(map[string]any)
	if counts["欠费"] != float64(1) || counts["核验"] != float64(1) || counts["其他"] != float64(0) {
		t.Fatalf("计数不对: %v", counts)
	}
	r = e.do("GET", "/api/acct/admin/frozen-accounts?kind=%E6%AC%A0%E8%B4%B9", nil, adm)
	if r.body["total"] != float64(1) || r.body["counts"].(map[string]any)["核验"] != float64(1) {
		t.Fatalf("按类别筛选，计数不受影响: %v", r.body)
	}
	if r := e.do("GET", "/api/acct/admin/frozen-accounts?q=0000", nil, adm); r.body["total"] != float64(1) {
		t.Fatalf("按尾号查: %v", r.body)
	}
	if r := e.do("GET", "/api/acct/admin/frozen-accounts?q="+uid, nil, adm); r.body["total"] != float64(1) {
		t.Fatalf("按用户编号查: %v", r.body)
	}
	if r := e.do("GET", "/api/acct/admin/frozen-accounts?page=2&pageSize=1", nil, adm); len(r.body["rows"].([]any)) != 1 || r.body["page"] != float64(2) {
		t.Fatalf("分页: %v", r.body)
	}
	// 客服读用户：含完整手机号与待处理项，读取留痕
	cs := e.do("GET", "/api/acct/admin/users/"+uid, nil, bearer(e.adminToken([]string{"user.view"}, nil)))
	if cs.status != 200 || cs.str("phone") != "13812345678" || cs.body["cancelBlockers"] == nil || cs.body["lastActiveAt"] == nil {
		t.Fatalf("客服读用户: %d %v", cs.status, cs.body)
	}
	if r := e.do("GET", "/api/acct/admin/users/"+uid, nil, adm); r.status != 403 {
		t.Fatalf("只有 account.unfreeze 不能读用户: %d", r.status)
	}
	if r := e.do("GET", "/api/acct/admin/users/U999999", nil, bearer(e.adminToken([]string{"user.view"}, nil))); r.status != 404 {
		t.Fatalf("不存在 404: %d", r.status)
	}
	found := false
	for _, a := range e.outbox("acct.audit.recorded") {
		if p := a["payload"].(map[string]any); p["targetId"] == uid && a["actor"].(map[string]any)["id"] == "AC0301" && p["action"] == "查看用户信息" {
			found = true
		}
	}
	if !found {
		t.Fatal("客服读用户应留痕")
	}
}

// 测试数据：原型 users.json 中与本模块相关的样例
func TestPrototypeUsers(t *testing.T) {
	e := newEnv(t)
	b, err := os.ReadFile("testdata/prototype_users.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Users []protoUser `json:"users"`
	}
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	for _, u := range fx.Users {
		if err := seedProtoUser(context.Background(), sharedPool, u); err != nil {
			t.Fatal(err)
		}
	}
	adm := bearer(e.adminToken([]string{"user.view"}, nil))
	r := e.do("GET", "/api/acct/admin/frozen-accounts", nil, adm)
	c := r.body["counts"].(map[string]any)
	// 原型样例：U100712、U100886、U101014 欠费冻结；U101538、U101761 先骑后验核验未通过冻结
	if c["欠费"] != float64(3) || c["核验"] != float64(2) || c["其他"] != float64(0) {
		t.Fatalf("原型样例冻结计数不对: %v", c)
	}
	u := e.do("GET", "/api/acct/admin/users/U100026", nil, adm)
	rn := u.body["realName"].(map[string]any)
	if rn["status"] != "待核验" || rn["deadline"] != "2026-12-04T23:59:59+08:00" || u.str("degradedSignupEventId") != "OPS-DG-20261127-01" {
		t.Fatalf("先骑后验样例不对: %v", u.body)
	}
	look := e.do("GET", "/internal/acct/users/lookup?phone=17314425184", nil, internalHdr("cs", "cs-key", ""))
	us := look.body["users"].([]any)
	if len(us) != 1 || us[0].(map[string]any)["accountStatus"] != "已注销" {
		t.Fatalf("按手机号找应含已注销账户: %v", look.body)
	}
	// 已注销用户的手机号可重新注册
	a := e.do("POST", "/api/acct/authorize", map[string]any{"channel": "支付宝", "phone": "17314425184"}, nil)
	if a.body["isNew"] != true {
		t.Fatalf("已注销手机号应按新用户注册: %v", a.body)
	}
	// 欠费冻结的原型用户收到欠费结清后自动解冻
	e.event("p1", "bill.arrears.cleared", "U100712", map[string]any{"userId": "U100712", "clearedAt": e.clk.Now().Format(time.RFC3339)})
	if s := e.do("GET", "/internal/acct/users/U100712", nil, internalHdr("cs", "cs-key", "")).body["account"].(map[string]any)["status"]; s != "正常" {
		t.Fatalf("U100712 结清应自动解冻: %v", s)
	}
	// 核验冻结的原型用户结清欠费也不解冻（实名未通过）
	e.event("p2", "bill.arrears.cleared", "U101538", map[string]any{"userId": "U101538", "clearedAt": e.clk.Now().Format(time.RFC3339)})
	if s := e.do("GET", "/internal/acct/users/U101538", nil, internalHdr("cs", "cs-key", "")).body["account"].(map[string]any)["status"]; s != "冻结" {
		t.Fatalf("U101538 实名未通过不应解冻: %v", s)
	}
}

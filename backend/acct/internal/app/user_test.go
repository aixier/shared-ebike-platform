package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/healthz", nil, nil)
	if r.status != 200 || r.body["ok"] != true {
		t.Fatalf("healthz: %d %v", r.status, r.body)
	}
}

// R-03：已注册即登录，未注册即注册；一个手机号一个账户
func TestAuthorizeRegisterAndLogin(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/acct/authorize", map[string]any{"channel": "微信", "phone": "13812345678"}, nil)
	if r.status != 200 || r.body["isNew"] != true || r.str("token") == "" || r.str("expiresAt") == "" {
		t.Fatalf("首次授权应注册: %d %v", r.status, r.body)
	}
	u := r.body["user"].(map[string]any)
	if u["userId"] != "U100001" || u["phoneMasked"] != "138****5678" || u["channel"] != "微信" || u["name"] != nil {
		t.Fatalf("用户视图不对: %v", u)
	}
	if strings.Contains(strings.Join([]string{r.str("token")}, ""), "13812345678") {
		t.Fatal("令牌里不应有手机号")
	}
	if rn := u["realName"].(map[string]any); rn["status"] != "未实名" {
		t.Fatalf("新用户应未实名: %v", rn)
	}
	if acc := u["account"].(map[string]any); acc["status"] != "正常" || acc["freeze"] != nil {
		t.Fatalf("新用户账户状态不对: %v", acc)
	}
	ev := e.outbox("acct.user.registered")
	if len(ev) != 1 || ev[0]["key"] != "U100001" {
		t.Fatalf("应发布用户已注册: %v", ev)
	}
	p := ev[0]["payload"].(map[string]any)
	if p["channel"] != "微信" || p["duringDegrade"] != false || p["registeredAt"] == nil {
		t.Fatalf("注册事件载荷不对: %v", p)
	}
	if _, has := p["phone"]; has {
		t.Fatal("事件载荷不得出现手机号")
	}
	// 再次授权：登录
	r2 := e.do("POST", "/api/acct/authorize", map[string]any{"channel": "支付宝", "phone": "13812345678"}, nil)
	if r2.status != 200 || r2.body["isNew"] != false || r2.body["user"].(map[string]any)["userId"] != "U100001" {
		t.Fatalf("已注册应登录: %v", r2.body)
	}
	if len(e.outbox("acct.user.registered")) != 1 {
		t.Fatal("登录不应再发注册事件")
	}
	// 令牌可用
	me := e.do("GET", "/api/acct/me", nil, bearer(r2.str("token")))
	if me.status != 200 || me.str("userId") != "U100001" || me.body["unreadNotices"] != float64(0) {
		t.Fatalf("me: %d %v", me.status, me.body)
	}
}

func TestAuthorizeErrorsAndIdempotency(t *testing.T) {
	e := newEnv(t)
	if r := e.do("POST", "/api/acct/authorize", map[string]any{"channel": "微信", "phone": "1234"}, nil); r.status != 400 || r.str("code") != "INVALID" {
		t.Fatalf("手机号格式不对应 INVALID: %d %v", r.status, r.body)
	}
	if r := e.do("POST", "/api/acct/authorize", map[string]any{"channel": "QQ", "phone": "13812345678"}, nil); r.status != 400 || r.str("code") != "INVALID" {
		t.Fatalf("渠道不对应 INVALID: %v", r.body)
	}
	e.auth.SetDown(true)
	r := e.do("POST", "/api/acct/authorize", map[string]any{"channel": "微信", "phone": "13812345678"}, nil)
	if r.status != 409 || r.str("code") != "AUTH_UNAVAILABLE" || r.str("message") == "" {
		t.Fatalf("授权故障应 AUTH_UNAVAILABLE: %d %v", r.status, r.body)
	}
	e.auth.SetDown(false)
	h := idem(nil, "k-1")
	a := e.do("POST", "/api/acct/authorize", map[string]any{"channel": "微信", "phone": "13812345679"}, h)
	b := e.do("POST", "/api/acct/authorize", map[string]any{"channel": "微信", "phone": "13812345679"}, h)
	if a.str("token") != b.str("token") || b.body["isNew"] != true {
		t.Fatalf("同一幂等键应返回第一次的结果: %v / %v", a.body, b.body)
	}
}

func TestMeAndLogout(t *testing.T) {
	e := newEnv(t)
	if r := e.do("GET", "/api/acct/me", nil, nil); r.status != 401 || r.str("code") != "NOT_LOGGED_IN" {
		t.Fatalf("未登录应 401: %v", r.body)
	}
	if r := e.do("GET", "/api/acct/me", nil, bearer("garbage")); r.status != 401 {
		t.Fatalf("坏令牌应 401")
	}
	_, tok := e.login("13812345678")
	if r := e.do("POST", "/api/acct/logout", nil, bearer(tok)); r.status != 200 || r.body["ok"] != true {
		t.Fatalf("退出: %v", r.body)
	}
	if r := e.do("GET", "/api/acct/me", nil, bearer(tok)); r.status != 401 {
		t.Fatalf("退出后令牌应作废: %d", r.status)
	}
	// 已失效的令牌退出也成功
	if r := e.do("POST", "/api/acct/logout", nil, bearer(tok)); r.status != 200 {
		t.Fatalf("失效令牌退出也应成功: %d", r.status)
	}
	// 令牌过期
	_, tok2 := e.login("13812345670")
	e.clk.Add(169 * time.Hour)
	if r := e.do("GET", "/api/acct/me", nil, bearer(tok2)); r.status != 401 {
		t.Fatalf("过期令牌应 401")
	}
}

// R-03：实名核验通过，对外脱敏
func TestVerifyRealNameOK(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.login("13812345678")
	r := e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, idem(bearer(tok), "rn1"))
	if r.status != 200 || r.str("status") != "已核验" || r.body["deferred"] != false {
		t.Fatalf("核验应通过: %d %v", r.status, r.body)
	}
	u := r.body["user"].(map[string]any)
	rn := u["realName"].(map[string]any)
	if u["name"] != "张＊＊" || rn["idMasked"] != adultID[:3]+"***********"+adultID[14:] || rn["adult"] != true || rn["method"] != "在线实名核验" {
		t.Fatalf("实名视图不对: %v", u)
	}
	ev := e.outbox("acct.realname.changed")
	if len(ev) != 1 {
		t.Fatalf("应发实名状态变更: %v", ev)
	}
	p := ev[0]["payload"].(map[string]any)
	if p["userId"] != uid || p["status"] != "已核验" || p["adult"] != true || p["afterDegrade"] != false || p["deadline"] != nil {
		t.Fatalf("实名事件载荷不对: %v", p)
	}
	if r := e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, bearer(tok)); r.status != 409 || r.str("code") != "ALREADY_DONE" {
		t.Fatalf("重复核验应 ALREADY_DONE: %v", r.body)
	}
}

func TestVerifyRealNameErrors(t *testing.T) {
	e := newEnv(t)
	_, tok := e.login("13812345678")
	cases := []struct {
		name, id string
		status   int
		code     string
	}{
		{"Tom", adultID, 400, "INVALID"},
		{"张三", "110105199003072990", 409, "REALNAME_INVALID_ID"},
		{"李鬼", adultID, 409, "REALNAME_NAME_MISMATCH"},
	}
	for _, c := range cases {
		r := e.do("POST", "/api/acct/real-name", map[string]any{"name": c.name, "idNo": c.id}, bearer(tok))
		if r.status != c.status || r.str("code") != c.code {
			t.Errorf("%s/%s: 应 %d %s，得 %d %v", c.name, c.id, c.status, c.code, r.status, r.body)
		}
	}
	// 未达法定年龄：未用过车只拒绝不冻结
	r := e.do("POST", "/api/acct/real-name", map[string]any{"name": "王小明", "idNo": minorID}, bearer(tok))
	if r.status != 409 || r.str("code") != "UNDER_AGE" {
		t.Fatalf("未满 16 应 UNDER_AGE: %v", r.body)
	}
	me := e.do("GET", "/api/acct/me", nil, bearer(tok))
	rn := me.body["realName"].(map[string]any)
	if rn["status"] != "未通过" || rn["adult"] != false || rn["failReason"] == nil {
		t.Fatalf("应记为未通过: %v", rn)
	}
	if me.body["account"].(map[string]any)["status"] != "正常" {
		t.Fatal("未用过车不应冻结")
	}
}

// R-03 R-14：未达法定年龄且已用过车的同时冻结
func TestUnderAgeAfterRidingFreezes(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.login("13812345678")
	e.event("e1", "ride.order.created", "OD1", map[string]any{"orderId": "OD1", "userId": uid})
	e.event("e2", "ride.order.ended", "OD1", map[string]any{"orderId": "OD1", "userId": uid})
	r := e.do("POST", "/api/acct/real-name", map[string]any{"name": "王小明", "idNo": minorID}, bearer(tok))
	if r.str("code") != "UNDER_AGE" {
		t.Fatalf("应 UNDER_AGE: %v", r.body)
	}
	acc := e.do("GET", "/api/acct/me", nil, bearer(tok)).body["account"].(map[string]any)
	if acc["status"] != "冻结" || acc["freeze"] == nil {
		t.Fatalf("已用过车应冻结: %v", acc)
	}
	fr := e.outbox("acct.account.frozen")
	if len(fr) != 1 || fr[0]["payload"].(map[string]any)["reason"] != "核验未通过" {
		t.Fatalf("应发账户已冻结（核验未通过）: %v", fr)
	}
	if len(e.outbox("acct.audit.recorded")) == 0 {
		t.Fatal("冻结应留痕")
	}
}

func degradeEnter(e *env, id string, comps ...string) {
	c := make([]any, len(comps))
	for i, s := range comps {
		c[i] = s
	}
	e.event("ops-"+id, "ops.degrade.entered", "degrade", map[string]any{"eventId": id, "cause": "实名服务故障", "components": c,
		"enteredAt": e.clk.Now().Format(time.RFC3339), "limitsVersionId": "PR0004"})
}

func publishRule(e *env) {
	e.event("rule-PR0004", "rule.version.published", "pricing", map[string]any{
		"versionId": "PR0004", "name": "现行", "effectiveAt": e.clk.Now().Add(-24 * time.Hour).Format(time.RFC3339), "previousVersionId": nil,
		"riskLimits": map[string]any{"perUserOpenDegradedOrders": 1, "normal": map[string]any{"maxMinutes": 60, "maxAmount": 3000},
			"tight": map[string]any{"maxMinutes": 30, "maxAmount": 1500}, "hourlyDegradedOrders": 200, "degradeDurationAlertMinutes": 30},
	})
}

// R-32 BR-34：先骑后验
func TestDeferredRealName(t *testing.T) {
	e := newEnv(t)
	publishRule(e)
	degradeEnter(e, "OPS-DG-20261201-01", "实名核验")
	uid, tok := e.login("13812345678")
	if p := e.outbox("acct.user.registered")[0]["payload"].(map[string]any); p["duringDegrade"] != true {
		t.Fatalf("实名故障降级期间注册应标记: %v", p)
	}
	e.rn.SetDown(true)
	r := e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, bearer(tok))
	if r.status != 200 || r.body["deferred"] != true || r.str("status") != "待核验" {
		t.Fatalf("应进入先骑后验: %d %v", r.status, r.body)
	}
	if r.str("deadline") != "2026-12-08T23:59:59+08:00" || r.body["limitMinutes"] != float64(30) || r.body["limitAmount"] != float64(1500) {
		t.Fatalf("截止时刻或限额不对: %v", r.body)
	}
	if !strings.Contains(r.str("text"), "30 分钟") || !strings.Contains(r.str("text"), "¥15.0") {
		t.Fatalf("说明文字不对: %s", r.str("text"))
	}
	ev := e.outbox("acct.realname.changed")
	if len(ev) != 1 || ev[0]["payload"].(map[string]any)["status"] != "待核验" || ev[0]["payload"].(map[string]any)["deadline"] == nil {
		t.Fatalf("应发待核验: %v", ev)
	}
	// 再次提交返回同一截止
	r2 := e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, bearer(tok))
	if r2.str("deadline") != r.str("deadline") {
		t.Fatalf("重复提交应返回同一截止: %v", r2.body)
	}
	// 降级退出后补验通过
	e.rn.SetDown(false)
	e.event("ops-exit", "ops.degrade.exited", "degrade", map[string]any{"eventId": "OPS-DG-20261201-01", "exitedAt": e.clk.Now().Format(time.RFC3339), "durationMinutes": 30})
	e.app.WaitBackground()
	me := e.do("GET", "/api/acct/me", nil, bearer(tok))
	rn := me.body["realName"].(map[string]any)
	if rn["status"] != "已核验" || rn["method"] != "先骑后验（降级期间注册，恢复后补验通过）" || rn["deadline"] != nil {
		t.Fatalf("补验应通过: %v", rn)
	}
	ev = e.outbox("acct.realname.changed")
	last := ev[len(ev)-1]["payload"].(map[string]any)
	if last["status"] != "已核验" || last["afterDegrade"] != true || last["userId"] != uid {
		t.Fatalf("补验事件不对: %v", last)
	}
	cs := e.do("GET", "/api/acct/admin/users/"+uid, nil, bearer(e.adminToken([]string{"user.view"}, nil)))
	if cs.str("degradedSignupEventId") != "OPS-DG-20261201-01" {
		t.Fatalf("应记降级事件编号: %v", cs.body)
	}
}

func TestDeferredUnavailableWithoutDegrade(t *testing.T) {
	e := newEnv(t)
	_, tok := e.login("13812345678")
	e.rn.SetDown(true)
	r := e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, bearer(tok))
	if r.status != 409 || r.str("code") != "SERVER_BUSY" {
		t.Fatalf("不在降级期间实名服务不可用应提示稍后再试: %d %v", r.status, r.body)
	}
	// 降级但故障组件不含实名核验
	degradeEnter(e, "OPS-DG-1", "支付通道")
	if r := e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, bearer(tok)); r.str("code") != "SERVER_BUSY" {
		t.Fatalf("故障组件不含实名核验不走先骑后验: %v", r.body)
	}
}

// BR-34：补验不通过冻结；逾期未验冻结
func TestReverifyFailAndOverdue(t *testing.T) {
	e := newEnv(t)
	degradeEnter(e, "OPS-DG-2", "实名核验")
	_, tok := e.login("13812345678")
	_, tok2 := e.login("13812345679")
	e.rn.SetDown(true)
	e.do("POST", "/api/acct/real-name", map[string]any{"name": "李鬼", "idNo": adultID}, bearer(tok))
	e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, bearer(tok2))
	// 降级退出时实名服务仍不可用：两人都保持待核验
	e.event("ops-exit", "ops.degrade.exited", "degrade", map[string]any{"eventId": "OPS-DG-2", "exitedAt": e.clk.Now().Format(time.RFC3339), "durationMinutes": 30})
	e.app.WaitBackground()
	if s := e.do("GET", "/api/acct/me", nil, bearer(tok)).body["realName"].(map[string]any)["status"]; s != "待核验" {
		t.Fatalf("服务仍不可用应保持待核验: %v", s)
	}
	// 服务恢复，定时任务补验：李鬼不一致→未通过并冻结
	e.rn.SetDown(false)
	if err := e.app.RunJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	me := e.do("GET", "/api/acct/me", nil, bearer(tok))
	if me.body["realName"].(map[string]any)["status"] != "未通过" || me.body["account"].(map[string]any)["status"] != "冻结" {
		t.Fatalf("补验不一致应未通过并冻结: %v", me.body)
	}
	if e.do("GET", "/api/acct/me", nil, bearer(tok2)).body["realName"].(map[string]any)["status"] != "已核验" {
		t.Fatal("一致的应补验通过")
	}
	// 逾期：新的降级期间注册、截止后仍不可用
	degradeEnter(e, "OPS-DG-3", "实名核验")
	_, tok3 := e.login("13812345670")
	e.rn.SetDown(true)
	e.do("POST", "/api/acct/real-name", map[string]any{"name": "赵六", "idNo": adultID}, bearer(tok3))
	e.clk.Add(8 * 24 * time.Hour)
	if err := e.app.RunJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, tok3 = e.login("13812345670") // 令牌 7 天过期，重新登录（冻结账户可登录）
	acc := e.do("GET", "/api/acct/account", nil, bearer(tok3)).body
	fz, _ := acc["freeze"].(map[string]any)
	if fz == nil || !strings.Contains(fz["reason"].(string), "逾期") {
		t.Fatalf("逾期未验应冻结: %v", acc)
	}
	ss := acc["selfService"].([]any)
	if len(ss) != 1 || ss[0].(map[string]any)["action"] != "verifyRealName" {
		t.Fatalf("自助入口应为重新实名: %v", ss)
	}
	// 定时任务重复跑不重复冻结
	n := len(e.outbox("acct.account.frozen"))
	_ = e.app.RunJobs(context.Background())
	if len(e.outbox("acct.account.frozen")) != n {
		t.Fatal("重复执行不应重复冻结")
	}
	// 本人补做核验通过且无欠费：自动解冻
	e.rn.SetDown(false)
	e.event("ops-exit3", "ops.degrade.exited", "degrade", map[string]any{"eventId": "OPS-DG-3", "exitedAt": e.clk.Now().Format(time.RFC3339), "durationMinutes": 30})
	e.app.WaitBackground()
	me3 := e.do("GET", "/api/acct/me", nil, bearer(tok3))
	if me3.body["account"].(map[string]any)["status"] != "正常" {
		t.Fatalf("核验通过且无欠费应自动解冻: %v", me3.body["account"])
	}
	un := e.outbox("acct.account.unfrozen")
	if len(un) == 0 || un[len(un)-1]["payload"].(map[string]any)["mode"] != "自动解冻" {
		t.Fatalf("应发自动解冻: %v", un)
	}
}

// R-14：欠费超期冻结，结清自动解冻
func TestArrearsFreezeAndAutoUnfreeze(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.login("13812345678")
	e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, bearer(tok))
	e.event("b1", "bill.arrears.created", "OD1", map[string]any{"orderId": "OD1", "userId": uid, "amount": 1100, "userArrearsTotal": 1100,
		"cause": "扣款失败", "createdAt": e.clk.Now().Format(time.RFC3339), "degraded": false})
	e.event("b2", "bill.arrears.overdue", uid, map[string]any{"userId": uid, "userArrearsTotal": 1100, "oldestAt": e.clk.Now().Format(time.RFC3339), "days": 31})
	acc := e.do("GET", "/api/acct/account", nil, bearer(tok)).body
	fz, _ := acc["freeze"].(map[string]any)
	if fz == nil || fz["unfreezeHow"] != "结清欠费后自动解冻" {
		t.Fatalf("欠费超期应冻结: %v", acc)
	}
	ss := acc["selfService"].([]any)
	if len(ss) != 1 || ss[0].(map[string]any)["action"] != "payArrears" {
		t.Fatalf("自助入口应为补付欠费: %v", ss)
	}
	if p := e.outbox("acct.account.frozen")[0]["payload"].(map[string]any); p["reason"] != "欠费追缴" || p["unfreezeHow"] == nil {
		t.Fatalf("冻结事件载荷不对: %v", p)
	}
	// 重复投递同一事件不重复处理
	e.event("b2", "bill.arrears.overdue", uid, map[string]any{"userId": uid, "userArrearsTotal": 1100, "days": 31})
	if len(e.outbox("acct.account.frozen")) != 1 {
		t.Fatal("同一 eventId 应只处理一次")
	}
	e.event("b3", "bill.arrears.cleared", uid, map[string]any{"userId": uid, "clearedAt": e.clk.Now().Format(time.RFC3339)})
	me := e.do("GET", "/api/acct/me", nil, bearer(tok))
	acc2 := me.body["account"].(map[string]any)
	if acc2["status"] != "正常" {
		t.Fatalf("结清应自动解冻: %v", acc2)
	}
	h := acc2["history"].([]any)
	if len(h) != 2 || h[1].(map[string]any)["action"] != "自动解冻" || h[1].(map[string]any)["by"] != "AC0900" {
		t.Fatalf("状态变更记录不对: %v", h)
	}
	// 冻结与解冻都有通知
	if me.body["unreadNotices"].(float64) < 2 {
		t.Fatalf("冻结解冻应通知用户: %v", me.body["unreadNotices"])
	}
}

// 核验冻结：核验通过但仍有欠费时不解冻，结清后才解冻
func TestVerifyFreezeNeedsArrearsCleared(t *testing.T) {
	e := newEnv(t)
	degradeEnter(e, "OPS-DG-4", "实名核验")
	uid, tok := e.login("13812345678")
	e.rn.SetDown(true)
	e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, bearer(tok))
	e.clk.Add(8 * 24 * time.Hour)
	_ = e.app.RunJobs(context.Background()) // 逾期冻结（降级仍未退出，不补验）
	_, tok = e.login("13812345678")
	e.event("b1", "bill.arrears.created", "OD9", map[string]any{"orderId": "OD9", "userId": uid, "amount": 400, "userArrearsTotal": 400,
		"cause": "核验未通过追缴", "createdAt": e.clk.Now().Format(time.RFC3339), "degraded": true})
	e.rn.SetDown(false)
	e.event("ops-x", "ops.degrade.exited", "degrade", map[string]any{"eventId": "OPS-DG-4", "exitedAt": e.clk.Now().Format(time.RFC3339), "durationMinutes": 1})
	e.app.WaitBackground()
	me := e.do("GET", "/api/acct/me", nil, bearer(tok)).body
	if me["realName"].(map[string]any)["status"] != "已核验" || me["account"].(map[string]any)["status"] != "冻结" {
		t.Fatalf("有欠费不应解冻: %v", me)
	}
	e.event("b2", "bill.bill.paid", "OD9", map[string]any{"orderId": "OD9", "userId": uid, "payStatus": "欠费已补付", "channelPaid": 400, "depositDeducted": 0,
		"paidAt": e.clk.Now().Format(time.RFC3339), "degraded": true})
	if s := e.do("GET", "/api/acct/me", nil, bearer(tok)).body["account"].(map[string]any)["status"]; s != "正常" {
		t.Fatalf("补付后应自动解冻: %v", s)
	}
}

// R-48：注销待处理项与注销
func TestCancelFlow(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.login("13812345678")
	e.event("r1", "ride.order.created", "OD1", map[string]any{"orderId": "OD1", "userId": uid})
	e.event("b1", "bill.arrears.created", "OD0", map[string]any{"orderId": "OD0", "userId": uid, "amount": 1100, "userArrearsTotal": 1100, "cause": "扣款失败", "createdAt": e.clk.Now().Format(time.RFC3339), "degraded": false})
	e.event("b2", "bill.bill.issued", "OD2", map[string]any{"orderId": "OD2", "userId": uid, "billedMinutes": 12, "payable": 450, "collectBy": "一键支付",
		"payDeadline": e.clk.Now().Add(24 * time.Hour).Format(time.RFC3339), "degraded": false})
	e.event("d1", "deposit.status.changed", uid, map[string]any{"userId": uid, "mode": "押金", "status": "已缴", "amountPaid": 19900, "balance": 19900,
		"creditProvider": nil, "rideable": true, "changedAt": e.clk.Now().Format(time.RFC3339)})
	c := e.do("GET", "/api/acct/account/cancel-check", nil, bearer(tok))
	if c.body["canCancel"] != false {
		t.Fatalf("有待处理项不能注销: %v", c.body)
	}
	codes := map[string]map[string]any{}
	for _, b := range c.body["blockers"].([]any) {
		m := b.(map[string]any)
		codes[m["code"].(string)] = m
	}
	if codes["HAS_RIDING"]["orderId"] != "OD1" || codes["HAS_ARREARS"]["amount"] != float64(1100) || codes["UNPAID_ORDER"]["orderId"] != "OD2" || codes["DEPOSIT_NOT_REFUNDED"] == nil {
		t.Fatalf("待处理项不对: %v", codes)
	}
	if codes["HAS_ARREARS"]["next"].(map[string]any)["action"] != "payArrears" {
		t.Fatalf("欠费下一步应为补付: %v", codes["HAS_ARREARS"])
	}
	r := e.do("POST", "/api/acct/account/cancel", map[string]any{}, idem(bearer(tok), "c1"))
	if r.status != 409 || r.str("code") != "CANCEL_BLOCKED" || len(r.body["blockers"].([]any)) != 4 {
		t.Fatalf("应 CANCEL_BLOCKED: %d %v", r.status, r.body)
	}
	// 逐项处理
	e.event("r2", "ride.order.ended", "OD1", map[string]any{"orderId": "OD1", "userId": uid})
	e.event("b3", "bill.arrears.cleared", uid, map[string]any{"userId": uid, "clearedAt": e.clk.Now().Format(time.RFC3339)})
	e.event("b4", "bill.bill.paid", "OD2", map[string]any{"orderId": "OD2", "userId": uid, "payStatus": "已支付", "channelPaid": 450, "depositDeducted": 0, "paidAt": e.clk.Now().Format(time.RFC3339), "degraded": false})
	e.event("d2", "deposit.status.changed", uid, map[string]any{"userId": uid, "mode": "押金", "status": "退押中", "amountPaid": 19900, "balance": 19900, "rideable": false, "changedAt": e.clk.Now().Add(time.Second).Format(time.RFC3339)})
	c = e.do("GET", "/api/acct/account/cancel-check", nil, bearer(tok))
	bl := c.body["blockers"].([]any)
	if len(bl) != 1 || bl[0].(map[string]any)["code"] != "DEPOSIT_REFUNDING" {
		t.Fatalf("应只剩退押中: %v", bl)
	}
	e.event("d3", "deposit.refunded", uid, map[string]any{"requestId": "RQ1", "userId": uid, "amount": 19900, "mode": "原路", "refundedAt": e.clk.Now().Add(2 * time.Second).Format(time.RFC3339)})
	c = e.do("GET", "/api/acct/account/cancel-check", nil, bearer(tok))
	if c.body["canCancel"] != true {
		t.Fatalf("处理完应可注销: %v", c.body)
	}
	r = e.do("POST", "/api/acct/account/cancel", map[string]any{"reason": "不用了"}, idem(bearer(tok), "c2"))
	if r.status != 200 || r.body["ok"] != true || r.str("cancelledAt") == "" {
		t.Fatalf("注销失败: %d %v", r.status, r.body)
	}
	if ev := e.outbox("acct.account.cancelled"); len(ev) != 1 {
		t.Fatalf("应发账户已注销: %v", ev)
	}
	if r := e.do("GET", "/api/acct/me", nil, bearer(tok)); r.status != 409 || r.str("code") != "ACCOUNT_CANCELLED" {
		t.Fatalf("注销后 me 应 ACCOUNT_CANCELLED: %d %v", r.status, r.body)
	}
	if r := e.do("GET", "/api/acct/notices", nil, bearer(tok)); r.status != 401 {
		t.Fatalf("注销后令牌应作废: %d", r.status)
	}
	// 已注销的手机号按新用户注册
	a := e.do("POST", "/api/acct/authorize", map[string]any{"channel": "微信", "phone": "13812345678"}, nil)
	if a.body["isNew"] != true || a.body["user"].(map[string]any)["userId"] == uid {
		t.Fatalf("已注销手机号应按新用户注册: %v", a.body)
	}
}

// R-09 BR-22：通知
func TestNotices(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.login("13812345678")
	now := e.clk.Now().Format(time.RFC3339)
	e.event("n1", "bill.bill.issued", "OD1", map[string]any{"orderId": "OD1", "userId": uid, "billedMinutes": 12, "payable": 450, "collectBy": "一键支付", "payDeadline": e.clk.Now().Add(24 * time.Hour).Format(time.RFC3339)})
	e.clk.Add(time.Minute)
	e.event("n2", "bill.bill.issued", "OD2", map[string]any{"orderId": "OD2", "userId": uid, "billedMinutes": 8, "payable": 300, "collectBy": "免押自动扣款", "payDeadline": nil})
	e.event("n3", "bill.bill.paid", "OD2", map[string]any{"orderId": "OD2", "userId": uid, "payStatus": "已支付", "channelPaid": 300, "depositDeducted": 0, "paidAt": now})
	e.clk.Add(time.Minute)
	e.event("n4", "deposit.deducted", uid, map[string]any{"userId": uid, "orderId": "OD1", "amount": 450, "balanceAfter": 19450, "deductedAt": now})
	e.event("n5", "bill.bill.refunded", "OD2", map[string]any{"orderId": "OD2", "userId": uid, "amount": 100, "kind": "部分退款", "refundedAt": now})
	e.event("n6", "deposit.refunded", uid, map[string]any{"requestId": "R1", "userId": uid, "amount": 19450, "mode": "原路", "refundedAt": now})
	e.event("n7", "ride.order.over_limit_reminded", "OD3", map[string]any{"orderId": "OD3", "userId": uid, "limitMinutes": 30, "limitAmount": 1500, "remindedAt": now})
	e.event("n8", "bill.arrears.created", "OD4", map[string]any{"orderId": "OD4", "userId": uid, "amount": 200, "userArrearsTotal": 200, "cause": "扣款失败", "createdAt": now})
	e.event("n8", "bill.arrears.created", "OD4", map[string]any{"orderId": "OD4", "userId": uid, "amount": 200, "userArrearsTotal": 200, "cause": "扣款失败", "createdAt": now})
	r := e.do("GET", "/api/acct/notices", nil, bearer(tok))
	if r.status != 200 || len(r.list) != 7 {
		t.Fatalf("应有 7 条通知（同一事件只通知一次）: %d %d %v", r.status, len(r.list), r.list)
	}
	kinds := map[string]bool{}
	for _, n := range r.list {
		m := n.(map[string]any)
		kinds[m["kind"].(string)] = true
		if m["noticeId"] == "" || m["read"] != false || m["title"] == "" || m["text"] == "" {
			t.Fatalf("通知字段不全: %v", m)
		}
	}
	for _, k := range []string{"账单", "押金抵扣", "退款", "押金", "骑行", "欠费"} {
		if !kinds[k] {
			t.Errorf("缺 %s 类通知", k)
		}
	}
	first := r.list[0].(map[string]any)
	last := r.list[len(r.list)-1].(map[string]any)
	if first["at"].(string) < last["at"].(string) {
		t.Fatal("应新到旧")
	}
	if last["orderId"] != "OD1" || !strings.Contains(last["text"].(string), "¥4.5") {
		t.Fatalf("最早一条应为 OD1 账单: %v", last)
	}
	m := e.do("POST", "/api/acct/notices/read", map[string]any{"ids": []string{first["noticeId"].(string)}}, bearer(tok))
	if m.body["marked"] != float64(1) {
		t.Fatalf("标已读: %v", m.body)
	}
	if u := e.do("GET", "/api/acct/notices?unreadOnly=true", nil, bearer(tok)); len(u.list) != 6 {
		t.Fatalf("未读应 6: %d", len(u.list))
	}
	if m := e.do("POST", "/api/acct/notices/read", nil, bearer(tok)); m.body["marked"] != float64(6) {
		t.Fatalf("全部已读: %v", m.body)
	}
	if me := e.do("GET", "/api/acct/me", nil, bearer(tok)); me.body["unreadNotices"] != float64(0) {
		t.Fatalf("未读数应为 0: %v", me.body["unreadNotices"])
	}
}

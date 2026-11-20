package core

import (
	"testing"
)

// trace: 任务列表 10 账户处理；cs.yaml unfreezeUser、assistRefundDeposit、assistCancelAccount；R-14 R-17 R-48
func TestUnfreeze(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100712", "", "账户", "账户被冻结了")
	path := "/api/cs/admin/users/U100712/unfreeze"
	e.expect(e.do("POST", path, staff(), map[string]any{"reason": "短"}, ""), 400, "REASON_REQUIRED")
	e.expect(e.do("POST", path, adminTok("AC0401", "运维", "order.view"), map[string]any{"reason": "无权解冻账户"}, ""), 403, "FORBIDDEN")
	e.expect(e.do("POST", path, adminTokWith("AC0201", "OP01", false, "account.unfreeze"), map[string]any{"reason": "运营商不能解冻"}, ""), 403, "SCOPE_DENIED")
	other := e.open("U100484", "", "我的", "别人的工单")
	e.expect(e.do("POST", path, staff(), map[string]any{"reason": "关联了别人的工单", "ticketId": other}, ""), 400, "INVALID")
	r := e.do("POST", path, staff(), map[string]any{"reason": "用户已线下结清，核实后解冻", "ticketId": id}, "uf-1")
	e.expect(r, 200, "")
	if r.body["ok"] != true || r.body["userId"] != "U100712" || r.body["accountStatus"] != "正常" {
		t.Fatalf("%s", r.raw)
	}
	calls := e.world.CallsTo("/internal/acct/users/U100712/unfreeze")
	if len(calls) != 1 || calls[0].Body["ticketId"] != id || calls[0].Body["reason"] != "用户已线下结清，核实后解冻" {
		t.Fatalf("%+v", calls)
	}
	a := e.do("GET", "/api/cs/admin/tickets/"+id, staff(), nil, "")
	acts := a.body["actions"].([]any)
	if len(acts) != 1 || mustMap(t, acts[0])["type"] != "人工解冻" {
		t.Fatalf("%s", a.raw)
	}
	// 幂等重放
	r2 := e.do("POST", path, staff(), map[string]any{"reason": "用户已线下结清，核实后解冻", "ticketId": id}, "uf-1")
	if r2.status != 200 || len(e.world.CallsTo("/internal/acct/users/U100712/unfreeze")) != 1 {
		t.Fatalf("%s", r2.raw)
	}
	// 未冻结
	e.expect(e.do("POST", path, staff(), map[string]any{"reason": "再解冻一次试试"}, "uf-2"), 409, "CONFLICT")
	e.expect(e.do("POST", "/api/cs/admin/users/U999999/unfreeze", staff(), map[string]any{"reason": "不存在的用户"}, ""), 404, "NOT_FOUND")
	au := e.outbox("cs.audit.recorded")
	if len(au) != 0 { // 人工解冻由 acct 执行并记留痕，cs 不重复记
		t.Fatalf("%v", au)
	}
}

func TestAssistRefundDeposit(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/cs/admin/users/U100004/assist-refund-deposit", staff(), map[string]any{"reason": "用户电话申请退押"}, "")
	e.expect(r, 200, "")
	ref := mustMap(t, r.body["refund"])
	if r.body["userId"] != "U100004" || ref["amount"] != 9900.0 || ref["appliedAt"] == nil || ref["expectedAt"] == nil || r.body["text"] == "" {
		t.Fatalf("%s", r.raw)
	}
	b := e.do("POST", "/api/cs/admin/users/U100123/assist-refund-deposit", staff(), map[string]any{"reason": "用户电话申请退押"}, "")
	e.expect(b, 409, "REFUND_BLOCKED")
	if bl, _ := b.body["blockers"].([]any); len(bl) != 1 || mustMap(t, bl[0])["code"] != "HAS_RIDING" {
		t.Fatalf("应附 blockers：%s", b.raw)
	}
	e.expect(e.do("POST", "/api/cs/admin/users/U100484/assist-refund-deposit", staff(), map[string]any{"reason": "用户电话申请退押"}, ""), 409, "NOT_DEPOSIT_USER")
	e.expect(e.do("POST", "/api/cs/admin/users/U100004/assist-refund-deposit", adminTok("AC0401", "运维", "order.view"), map[string]any{"reason": "无权协助退押"}, ""), 403, "FORBIDDEN")
	au := e.outbox("cs.audit.recorded")
	if len(au) != 0 { // 协助退押由 deposit 执行并记留痕
		t.Fatalf("%v", au)
	}
}

func TestAssistCancel(t *testing.T) {
	e := newEnv(t)
	b := e.do("POST", "/api/cs/admin/users/U100279/assist-cancel", staff(), map[string]any{"reason": "用户要求注销账户"}, "")
	e.expect(b, 409, "CANCEL_BLOCKED")
	if bl, _ := b.body["blockers"].([]any); len(bl) != 1 || mustMap(t, bl[0])["code"] != "HAS_ARREARS" {
		t.Fatalf("%s", b.raw)
	}
	e.expect(e.do("POST", "/api/cs/admin/users/U100598/assist-cancel", staff(), map[string]any{"reason": "用户要求注销账户"}, ""), 409, "ALREADY_DONE")
	r := e.do("POST", "/api/cs/admin/users/U100484/assist-cancel", staff(), map[string]any{"reason": "用户要求注销账户"}, "")
	e.expect(r, 200, "")
	if r.body["ok"] != true || r.body["userId"] != "U100484" || r.body["cancelledAt"] == nil {
		t.Fatalf("%s", r.raw)
	}
	au := e.outbox("cs.audit.recorded")
	if len(au) != 0 { // 协助注销由 acct 执行并记留痕
		t.Fatalf("%v", au)
	}
}

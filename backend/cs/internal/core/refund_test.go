package core

import (
	"context"
	"testing"
)

// trace: 任务列表 7 退款确认；cs.yaml confirmRefund、approveOverLimitRefund、rejectRefund；R-10 BR-06
func TestRefundNeedsHumanConfirm(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.open("U100484", "OD2026090110600", "我的订单", "我明明停在站点里，为什么还收了调度费？")
	// 客服智能体写提议（原型 TK202609010031：退调度费 ¥3.0）
	if _, err := e.app.AgentProposeRefund(ctx, id, RefundProposalInput{Amount: 300, Kind: "部分退款", Basis: "站点桩离线，定位漂移可能导致误判站外"}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.world.CallsTo("/internal/bill/refunds")); n != 0 {
		t.Fatalf("没有人确认不得发退款命令，已发 %d 次", n)
	}
	a := e.do("GET", "/api/cs/admin/tickets/"+id, staff(), nil, "")
	p := mustMap(t, a.body["refundProposal"])
	if p["amount"] != 300.0 || p["decision"] != "待确认" || p["proposedBy"] != "AC0904" || p["overLimit"] != false || p["decidedBy"] != nil {
		t.Fatalf("提议不对：%v", p)
	}
	u := e.do("GET", "/api/cs/tickets/"+id, userTok("U100484"), nil, "")
	if b := mustMap(t, u.body["refundProposal"]); b["amount"] != 300.0 || b["kind"] != "部分退款" || b["decision"] != "待确认" || b["basis"] != nil {
		t.Fatalf("用户只看到摘要：%v", b)
	}

	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/confirm", staff(), map[string]any{}, ""), 400, "REASON_REQUIRED")
	r := e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/confirm", staff(), map[string]any{"reason": "核实桩离线，退调度费"}, "c-1")
	e.expect(r, 200, "")
	if r.body["pendingSupervisor"] != false {
		t.Fatalf("%s", r.raw)
	}
	ref := mustMap(t, r.body["refund"])
	if ref["status"] != "成功" || ref["amount"] != 300.0 || ref["paymentId"] == "" {
		t.Fatalf("%v", ref)
	}
	tk := mustMap(t, r.body["ticket"])
	acts := tk["actions"].([]any)
	if tk["status"] != "已解决" || tk["closedAt"] == nil || len(acts) != 1 || mustMap(t, acts[0])["type"] != "退款" || mustMap(t, acts[0])["paymentId"] != ref["paymentId"] {
		t.Fatalf("%v", tk)
	}
	if rp := mustMap(t, tk["refundProposal"]); rp["decision"] != "已确认" || rp["decidedBy"] != "AC0301" {
		t.Fatalf("%v", rp)
	}
	calls := e.world.CallsTo("/internal/bill/refunds")
	if len(calls) != 1 {
		t.Fatalf("应调 bill 一次，得到 %d", len(calls))
	}
	c := calls[0]
	actor := c.Body["actor"].(map[string]any)
	if c.Body["orderId"] != "OD2026090110600" || c.Body["amount"] != 300.0 || c.Body["ticketId"] != id || actor["id"] != "AC0301" || actor["kind"] != "人" || c.IdemKey == "" {
		t.Fatalf("退款命令不对：%+v", c)
	}
	// 同一幂等键重放第一次的结果，不再调 bill
	r2 := e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/confirm", staff(), map[string]any{"reason": "核实桩离线，退调度费"}, "c-1")
	if r2.status != 200 || string(r2.raw) != string(r.raw) || len(e.world.CallsTo("/internal/bill/refunds")) != 1 {
		t.Fatalf("幂等回放不对：%s", r2.raw)
	}
	// 换个键再确认：工单已解决
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/confirm", staff(), map[string]any{"reason": "核实桩离线，退调度费"}, "c-2"), 409, "ALREADY_DONE")
	// 留痕与事件
	au := e.outbox("cs.audit.recorded")
	if len(au) != 1 || payload(au[0])["action"] != "确认退款" {
		t.Fatalf("%v", au)
	}
	var changes []string
	for _, ev := range e.outbox("cs.ticket.changed") {
		changes = append(changes, payload(ev)["change"].(string))
	}
	if changes[len(changes)-2] != "退款提议" || changes[len(changes)-1] != "已退款" {
		t.Fatalf("%v", changes)
	}
}

func TestRefundRequiresPermission(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费")
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/confirm", adminTok("AC0401", "运维", "order.view"), map[string]any{"amount": 100, "reason": "退一点钱"}, ""), 403, "FORBIDDEN")
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/approve-over-limit", staff(), map[string]any{"reason": "超额确认"}, ""), 403, "FORBIDDEN")
}

func TestManualRefundEntryByStaff(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费")
	path := "/api/cs/admin/tickets/" + id + "/refund/confirm"
	e.expect(e.do("POST", path, staff(), map[string]any{"reason": "没有提议也没金额"}, ""), 400, "INVALID")
	e.expect(e.do("POST", path, staff(), map[string]any{"amount": 0, "reason": "金额为零"}, ""), 400, "INVALID")
	e.expect(e.do("POST", path, staff(), map[string]any{"amount": 931, "reason": "超过渠道已收"}, ""), 400, "INVALID")
	if len(e.world.CallsTo("/internal/bill/refunds")) != 0 {
		t.Fatal("校验不过不应调 bill")
	}
	r := e.do("POST", path, staff(), map[string]any{"amount": 930, "reason": "客服核实后全额退款"}, "")
	e.expect(r, 200, "")
	p := mustMap(t, mustMap(t, r.body["ticket"])["refundProposal"])
	if p["proposedBy"] != "AC0301" || p["kind"] != "全额退款" || p["amount"] != 930.0 || p["basis"] != "客服核实后全额退款" {
		t.Fatalf("客服手工录入的提议不对：%v", p)
	}
}

func TestRefundWithoutOrderIsInvalid(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100004", "", "我的", "怎么开发票")
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/confirm", staff(), map[string]any{"amount": 100, "reason": "没有订单的退款"}, ""), 400, "INVALID")
}

// 原型 TK202612010032：提议退 ¥21.0，超过客服授权额度 ¥20.0
func TestOverLimitGoesToSupervisor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.open("U101601", "OD2026113010347", "我的订单", "桩位都满了我在桩旁锁好车，结果骑了 48 分钟")
	if _, err := e.app.AgentProposeRefund(ctx, id, RefundProposalInput{Amount: 2100, Kind: "部分退款", Basis: "车辆提前停在站点内，锁车报告延迟"}); err != nil {
		t.Fatal(err)
	}
	a := e.do("GET", "/api/cs/admin/tickets/"+id, staff(), nil, "")
	if mustMap(t, a.body["refundProposal"])["overLimit"] != true {
		t.Fatal("应标记超出额度")
	}
	r := e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/confirm", staff(), map[string]any{"reason": "核实锁车报告延迟"}, "")
	e.expect(r, 200, "")
	tk := mustMap(t, r.body["ticket"])
	if r.body["pendingSupervisor"] != true || r.body["refund"] != nil || tk["status"] != "待主管确认" || mustMap(t, tk["refundProposal"])["decision"] != "待主管确认" {
		t.Fatalf("%s", r.raw)
	}
	if len(e.world.CallsTo("/internal/bill/refunds")) != 0 {
		t.Fatal("超额退款主管确认前不得退款")
	}
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/close", staff(), map[string]any{"reason": "想直接结案"}, ""), 409, "CONFLICT")
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/reject", staff(), map[string]any{"reason": "不同意退款"}, ""), 409, "OVER_LIMIT_NEEDS_SUPERVISOR")
	// 待办：主管看到 overLimit
	td := e.do("GET", "/api/cs/admin/todos", supervisor(), nil, "")
	found := false
	for _, x := range td.list {
		if m := x.(map[string]any); m["key"] == "overLimit" && m["count"] == 1.0 && m["menu"] == "退款处理" {
			found = true
		}
	}
	if !found {
		t.Fatalf("主管待办应有超额退款：%s", td.raw)
	}
	r = e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/approve-over-limit", supervisor(), map[string]any{"reason": "核实无误同意退款"}, "")
	e.expect(r, 200, "")
	tk = mustMap(t, r.body["ticket"])
	acts := tk["actions"].([]any)
	if mustMap(t, r.body["refund"])["status"] != "成功" || tk["status"] != "已解决" || mustMap(t, acts[0])["type"] != "超额退款确认" || mustMap(t, acts[0])["by"] != "AC0302" {
		t.Fatalf("%s", r.raw)
	}
	if calls := e.world.CallsTo("/internal/bill/refunds"); len(calls) != 1 || calls[0].Body["amount"] != 2100.0 {
		t.Fatalf("%+v", calls)
	}
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/approve-over-limit", supervisor(), map[string]any{"reason": "再确认一次"}, ""), 409, "CONFLICT")
	au := e.outbox("cs.audit.recorded")
	if len(au) != 2 || payload(au[1])["action"] != "确认超额退款" {
		t.Fatalf("%v", au)
	}
}

func TestSupervisorConfirmsOverLimitDirectly(t *testing.T) {
	e := newEnv(t)
	id := e.open("U101601", "OD2026113010347", "我的订单", "多收了")
	r := e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/confirm", supervisor(), map[string]any{"amount": 2100, "reason": "主管直接确认"}, "")
	e.expect(r, 200, "")
	if r.body["pendingSupervisor"] != false || mustMap(t, r.body["refund"])["status"] != "成功" {
		t.Fatalf("%s", r.raw)
	}
}

func TestRejectRefund(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费")
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/reject", staff(), map[string]any{"reason": "没有提议可驳回"}, ""), 409, "CONFLICT")
	if _, err := e.app.AgentProposeRefund(ctx, id, RefundProposalInput{Amount: 300, Kind: "部分退款", Basis: "定位漂移"}); err != nil {
		t.Fatal(err)
	}
	r := e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/reject", staff(), map[string]any{"reason": "车端与桩均判定站外，调度费不退"}, "")
	e.expect(r, 200, "")
	if r.body["status"] != "已解决" || mustMap(t, r.body["refundProposal"])["decision"] != "已驳回" {
		t.Fatalf("%s", r.raw)
	}
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/reject", staff(), map[string]any{"reason": "再驳回一次"}, ""), 409, "CONFLICT")
	if len(e.world.CallsTo("/internal/bill/refunds")) != 0 {
		t.Fatal("驳回不应退款")
	}
}

// trace: 任务列表 8 原路失败收集收款账户；R-11；原型 TK202611290031
func TestRefundChannelFailedAwaitsPayee(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.open("U100484", "OD2026112714351", "我的订单", "开锁后车没电，骑不动，这单要退。")
	if _, err := e.app.AgentProposeRefund(ctx, id, RefundProposalInput{Amount: 300, Kind: "全额退款", Basis: "车辆电池故障"}); err != nil {
		t.Fatal(err)
	}
	r := e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/confirm", staff(), map[string]any{"reason": "运营商确认电池故障"}, "")
	e.expect(r, 200, "")
	ref := mustMap(t, r.body["refund"])
	tk := mustMap(t, r.body["ticket"])
	pid := ref["paymentId"].(string)
	if ref["status"] != "失败" || tk["status"] != "处理中" || tk["awaitingPayee"] != true || tk["refundFailedPaymentId"] != pid {
		t.Fatalf("%s", r.raw)
	}
	u := e.do("GET", "/api/cs/tickets/"+id, userTok("U100484"), nil, "")
	if u.body["awaitingPayee"] != true || u.body["refundFailedPaymentId"] != pid {
		t.Fatalf("用户对话应显示收款账户表单：%s", u.raw)
	}
	// 用户向 pay 提交收款账户后
	ev := map[string]any{"eventId": "pay-ev-1", "type": "pay.payee.submitted", "version": 1, "occurredAt": "2026-12-01T12:00:00+08:00", "key": "BZ1",
		"actor":   map[string]any{"kind": "用户", "id": "U100484"},
		"payload": map[string]any{"paymentId": pid, "bizNo": "BZ1", "account": "骑行收入户", "userId": "U100484", "orderId": "OD2026112714351", "submittedAt": "2026-12-01T12:00:00+08:00"}}
	if err := e.app.HandleEvent(ctx, mustJSON(ev)); err != nil {
		t.Fatal(err)
	}
	u = e.do("GET", "/api/cs/tickets/"+id, userTok("U100484"), nil, "")
	if u.body["awaitingPayee"] != false || u.body["refundFailedPaymentId"] != nil {
		t.Fatalf("提交后不再等待收款账户：%s", u.raw)
	}
	// 财务人工转账完成
	done := map[string]any{"eventId": "pay-ev-2", "type": "pay.refund.succeeded", "version": 1, "occurredAt": "2026-12-01T15:00:00+08:00", "key": "BZ2",
		"actor": map[string]any{"kind": "人", "id": "AC0501"},
		"payload": map[string]any{"paymentId": "PY-MANUAL-1", "bizNo": "BZ2", "type": "人工转账退款", "account": "骑行收入户", "userId": "U100484", "orderId": "OD2026112714351",
			"amount": 300, "channel": "银行转账", "relatedPaymentId": pid, "completedAt": "2026-12-01T15:00:00+08:00"}}
	for i := 0; i < 2; i++ { // 重复送达只处理一次
		if err := e.app.HandleEvent(ctx, mustJSON(done)); err != nil {
			t.Fatal(err)
		}
	}
	a := e.do("GET", "/api/cs/admin/tickets/"+id, staff(), nil, "")
	acts := a.body["actions"].([]any)
	if a.body["status"] != "已解决" || len(acts) != 2 || mustMap(t, acts[1])["type"] != "人工转账" || mustMap(t, acts[1])["paymentId"] != "PY-MANUAL-1" || mustMap(t, acts[1])["by"] != "AC0501" {
		t.Fatalf("%s", a.raw)
	}
}

func TestRefundUnknownResolvedByEvent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.world.Bills["OD2026090110600"].RefundMode = "unknown"
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费")
	r := e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/confirm", staff(), map[string]any{"amount": 300, "reason": "退调度费给用户"}, "")
	e.expect(r, 200, "")
	pid := mustMap(t, r.body["refund"])["paymentId"].(string)
	if mustMap(t, r.body["refund"])["status"] != "结果未知" || mustMap(t, r.body["ticket"])["status"] != "处理中" {
		t.Fatalf("%s", r.raw)
	}
	ok := map[string]any{"eventId": "pay-ev-9", "type": "pay.refund.succeeded", "version": 1, "occurredAt": "2026-12-01T12:00:00+08:00", "key": "BZ9",
		"actor": map[string]any{"kind": "系统", "id": "pay"},
		"payload": map[string]any{"paymentId": pid, "bizNo": "BZ9", "type": "退款", "account": "骑行收入户", "userId": "U100484", "orderId": "OD2026090110600",
			"amount": 300, "channel": "微信支付", "relatedPaymentId": "PY-ORIG", "completedAt": "2026-12-01T12:00:00+08:00"}}
	if err := e.app.HandleEvent(ctx, mustJSON(ok)); err != nil {
		t.Fatal(err)
	}
	a := e.do("GET", "/api/cs/admin/tickets/"+id, staff(), nil, "")
	if a.body["status"] != "已解决" {
		t.Fatalf("%s", a.raw)
	}
}

func TestRefundUnknownThenFailedEvent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.world.Bills["OD2026090110600"].RefundMode = "unknown"
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费")
	r := e.do("POST", "/api/cs/admin/tickets/"+id+"/refund/confirm", staff(), map[string]any{"amount": 300, "reason": "退调度费给用户"}, "")
	pid := mustMap(t, r.body["refund"])["paymentId"].(string)
	fail := map[string]any{"eventId": "pay-ev-10", "type": "pay.refund.failed", "version": 1, "occurredAt": "2026-12-01T12:00:00+08:00", "key": "BZ10",
		"actor": map[string]any{"kind": "系统", "id": "pay"},
		"payload": map[string]any{"paymentId": pid, "bizNo": "BZ10", "type": "退款", "account": "骑行收入户", "userId": "U100484", "orderId": "OD2026090110600",
			"amount": 300, "channel": "微信支付", "relatedPaymentId": "PY-ORIG", "failReason": "原支付账户已注销", "failedAt": "2026-12-01T12:00:00+08:00"}}
	if err := e.app.HandleEvent(ctx, mustJSON(fail)); err != nil {
		t.Fatal(err)
	}
	u := e.do("GET", "/api/cs/tickets/"+id, userTok("U100484"), nil, "")
	if u.body["awaitingPayee"] != true || u.body["refundFailedPaymentId"] != pid || u.body["status"] != "处理中" {
		t.Fatalf("%s", u.raw)
	}
}

// trace: rule.params.changed 客服退款授权额度
func TestRefundLimitFromRuleParams(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费")
	ev := func(eid string, limit int, at string) []byte {
		return mustJSON(map[string]any{"eventId": eid, "type": "rule.params.changed", "version": 1, "occurredAt": at, "key": "params",
			"actor": map[string]any{"kind": "人", "id": "AC0101"}, "payload": map[string]any{"csRefundLimit": limit, "changedAt": at, "payWindowHours": 24}})
	}
	if err := e.app.HandleEvent(ctx, ev("r1", 3000, "2026-12-01T10:00:00+08:00")); err != nil {
		t.Fatal(err)
	}
	if err := e.app.HandleEvent(ctx, ev("r0", 1000, "2026-11-01T10:00:00+08:00")); err != nil { // 晚到的旧值不覆盖
		t.Fatal(err)
	}
	a := e.do("GET", "/api/cs/admin/tickets/"+id, staff(), nil, "")
	if a.body["refundLimit"] != 3000.0 {
		t.Fatalf("额度应为 3000，得到 %v", a.body["refundLimit"])
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM inbox`).Scan(&n)
	if n != 2 {
		t.Fatalf("inbox 应记 2 条，得到 %d", n)
	}
}

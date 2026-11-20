package app

import (
	"strings"
	"testing"
	"time"

	"bike/deposit/internal/clients/pay/payfake"
)

func orderHead(order, uid string) map[string]any {
	return map[string]any{"orderId": order, "userId": uid, "vehicleId": "VH000004", "plateNo": "001", "brandId": "BR01", "operatorId": "OP01", "channel": "微信",
		"pricingVersionId": "PR0002", "requestAt": "2026-12-01T11:00:00+08:00", "degraded": nil}
}

func (e *env) depositUser(uid string) {
	e.t.Helper()
	e.verified(uid)
	mustStatus(e.t, e.user("POST", "/api/deposit/me/payment", uid, "pay-"+uid, nil), 200)
}

// trace: 任务 8 · R-17 BR-23 待处理项检查
func TestRefundBlockers(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.depositUser("U100004")
	e.event("ride.order.created", "OD1", orderHead("OD1", "U100004"))
	e.event("bill.bill.issued", "OD0", billIssued("OD0", "U100004", 450, "一键支付"))
	e.event("bill.bill.issued", "OD5", billIssued("OD5", "U100004", 1100, "一键支付"))
	e.event("bill.arrears.created", "OD5", map[string]any{"orderId": "OD5", "userId": "U100004", "amount": 1100, "userArrearsTotal": 1100, "cause": "押金不足", "createdAt": e.now.Format(time.RFC3339), "degraded": false})

	pg := e.user("GET", "/api/deposit/me", "U100004", "", nil)
	mustStatus(t, pg, 200)
	bl := pg.body["blockers"].([]any)
	if pg.body["canRefund"] != false || len(bl) != 3 {
		t.Fatalf("应有三项待处理：%s", pg.raw)
	}
	codes := []string{}
	for _, b := range bl {
		codes = append(codes, b.(map[string]any)["code"].(string))
	}
	if strings.Join(codes, ",") != "HAS_RIDING,HAS_ARREARS,UNPAID_ORDER" {
		t.Fatalf("待处理项顺序或内容不对：%v", codes)
	}
	if num(bl[1].(map[string]any)["amount"]) != 1100 || bl[0].(map[string]any)["orderId"] != "OD1" {
		t.Fatalf("待处理项字段不对：%s", pg.raw)
	}
	r := e.user("POST", "/api/deposit/me/refund", "U100004", "r1", nil)
	mustCode(t, r, 409, "REFUND_BLOCKED")
	if len(r.body["blockers"].([]any)) != 3 {
		t.Fatalf("拒绝时列出待处理项：%s", r.raw)
	}
	if e.countOf("deposit.refund.requested") != 0 {
		t.Fatal("被拒不应发事件")
	}
	// 处理完：还车、付清、欠费结清
	e.event("ride.order.ended", "OD1", orderHead("OD1", "U100004"))
	e.event("bill.bill.paid", "OD0", map[string]any{"orderId": "OD0", "userId": "U100004", "payStatus": "已支付", "channelPaid": 450, "depositDeducted": 0, "paidAt": e.now.Format(time.RFC3339), "degraded": false})
	e.event("bill.arrears.cleared", "U100004", map[string]any{"userId": "U100004", "clearedAt": e.now.Format(time.RFC3339)})
	pg = e.user("GET", "/api/deposit/me", "U100004", "", nil)
	if pg.body["canRefund"] != true || len(pg.body["blockers"].([]any)) != 0 || num(pg.body["refundAmount"]) != 19900 {
		t.Fatalf("处理完应能退押：%s", pg.raw)
	}
	if pg.body["expectedAtIfApplyNow"] != "2026-12-04T18:00:00+08:00" {
		t.Fatalf("预计到账 3 个工作日：%v", pg.body["expectedAtIfApplyNow"])
	}
}

// trace: 任务 8 · BR-24 申请即发事件，收到"已阻止开单"回执才退款；R-18 退押记账
func TestRefundFlowOriginalRoute(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.depositUser("U100004")
	r := e.user("POST", "/api/deposit/me/refund", "U100004", "r1", nil)
	mustStatus(t, r, 200)
	rf := r.body["refund"].(map[string]any)
	if num(rf["amount"]) != 19900 || rf["expectedAt"] != "2026-12-04T18:00:00+08:00" || rf["stoppedAt"] != nil || rf["paymentId"] != nil {
		t.Fatalf("退押申请不对：%s", r.raw)
	}
	if r.body["text"] != "押金 ¥199.0 将原路退回，预计 12月4日 前到账。退押后需重新缴押或开通免押才能骑行。" {
		t.Fatalf("提示语不对：%v", r.body["text"])
	}
	req := e.lastOf("deposit.refund.requested")
	if num(req["amount"]) != 19900 || req["requestId"] == "" {
		t.Fatalf("已申请退押事件不对：%v", req)
	}
	if st := e.lastOf("deposit.status.changed"); st["status"] != "退押中" || st["rideable"] != false {
		t.Fatalf("状态应为退押中、不可骑：%v", st)
	}
	if n := len(e.pay.Calls()); n != 1 {
		t.Fatalf("未收到回执前不应调 pay 退押，实际调用 %d 次", n)
	}
	// 退押中不能再抵扣
	if d := e.deduct("d1", "U100004", "OD1", 100); d.body["failCode"] != "NO_BALANCE" {
		t.Fatalf("退押中不能抵扣：%s", d.raw)
	}
	// 重复申请
	mustCode(t, e.user("POST", "/api/deposit/me/refund", "U100004", "r2", nil), 409, "NOT_DEPOSIT_USER")

	reqID := req["requestId"].(string)
	e.event("ride.admission.blocked", "U100004", map[string]any{"userId": "U100004", "requestId": reqID, "blockedAt": e.now.Format(time.RFC3339), "ridingOrderId": nil})
	calls := e.pay.Calls()
	if len(calls) != 2 || calls[1].Path != "/internal/pay/deposit-refunds" || calls[1].Body["bizNo"] != reqID || num(calls[1].Body["amount"]) != 19900 {
		t.Fatalf("收到回执后应调 pay 退押：%+v", calls)
	}
	if ids := calls[1].Body["originalPaymentIds"].([]any); len(ids) != 1 {
		t.Fatalf("原路退回应带原缴纳流水：%v", ids)
	}
	// 替身默认同步成功 → 已退
	st := e.user("GET", "/api/deposit/me/state", "U100004", "", nil)
	if st.body["status"] != "已退" || num(st.body["balance"]) != 0 {
		t.Fatalf("应已退：%s", st.raw)
	}
	rf = st.body["refund"].(map[string]any)
	if rf["stoppedAt"] == nil || rf["paymentId"] == nil {
		t.Fatalf("退押申请应记回执时刻与流水：%s", st.raw)
	}
	done := e.lastOf("deposit.refunded")
	if done["mode"] != "原路" || num(done["amount"]) != 19900 || done["requestId"] != reqID {
		t.Fatalf("已退押事件不对：%v", done)
	}
	au := e.lastOf("deposit.audit.recorded")
	if au["action"] != "退还押金" || au["targetType"] != "user" {
		t.Fatalf("退押要留痕：%v", au)
	}
	// 回执重投不重复退款
	e.event("ride.admission.blocked", "U100004", map[string]any{"userId": "U100004", "requestId": reqID, "blockedAt": e.now.Format(time.RFC3339), "ridingOrderId": nil})
	if len(e.pay.Calls()) != 2 {
		t.Fatal("不应重复退款")
	}
	// 专户恒等式
	sm := e.do("GET", "/api/deposit/admin/summary", map[string]string{"Authorization": "Bearer " + adminToken("AC0201", nil, "deposit.view")}, nil)
	l := sm.body["ledger"].(map[string]any)
	if num(l["paidIn"]) != 19900 || num(l["refunded"]) != 19900 || num(l["balance"]) != 0 || num(sm.body["userBalanceSum"]) != 0 {
		t.Fatalf("专户汇总不对：%s", sm.raw)
	}
	// 已退后可再缴
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U100004", "again", nil), 200)
}

// trace: BR-24 回执显示仍有骑行中订单 → 拒绝退押
func TestRefundRejectedByRidingReceipt(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.depositUser("U1")
	mustStatus(t, e.user("POST", "/api/deposit/me/refund", "U1", "r1", nil), 200)
	reqID := e.lastOf("deposit.refund.requested")["requestId"].(string)
	e.event("ride.admission.blocked", "U1", map[string]any{"userId": "U1", "requestId": reqID, "blockedAt": e.now.Format(time.RFC3339), "ridingOrderId": "OD7"})
	if len(e.pay.Calls()) != 1 {
		t.Fatal("有骑行中订单不应退款")
	}
	st := e.user("GET", "/api/deposit/me/state", "U1", "", nil)
	if st.body["status"] != "已缴" || st.body["refund"] != nil {
		t.Fatalf("拒绝后应恢复为已缴：%s", st.raw)
	}
	if ev := e.lastOf("deposit.status.changed"); ev["status"] != "已缴" || ev["rideable"] != true {
		t.Fatalf("恢复后应发状态事件：%v", ev)
	}
}

// trace: 任务 9 · R-11 原路失败转人工转账
func TestRefundFailedThenManual(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.depositUser("U100479")
	mustStatus(t, e.user("POST", "/api/deposit/me/refund", "U100479", "r1", nil), 200)
	reqID := e.lastOf("deposit.refund.requested")["requestId"].(string)
	e.pay.Next(payfake.Outcome{Result: "结果未知"})
	e.event("ride.admission.blocked", "U100479", map[string]any{"userId": "U100479", "requestId": reqID, "blockedAt": e.now.Format(time.RFC3339), "ridingOrderId": nil})
	st := e.user("GET", "/api/deposit/me/state", "U100479", "", nil)
	refundPID := st.body["refund"].(map[string]any)["paymentId"].(string)
	if st.body["status"] != "退押中" || refundPID == "" {
		t.Fatalf("受理未到账应仍退押中：%s", st.raw)
	}
	sm := e.do("GET", "/api/deposit/admin/summary", map[string]string{"Authorization": "Bearer " + adminToken("AC0201", nil, "deposit.view")}, nil)
	if num(sm.body["ledger"].(map[string]any)["refundingAmount"]) != 19900 {
		t.Fatalf("退押处理中金额：%s", sm.raw)
	}
	e.event("pay.refund.failed", reqID, map[string]any{"paymentId": refundPID, "bizNo": reqID, "type": "退押", "account": "押金专户", "userId": "U100479", "orderId": nil,
		"amount": 19900, "channel": "微信支付", "relatedPaymentId": nil, "failReason": "原支付账户已注销", "failedAt": e.now.Format(time.RFC3339)})
	pg := e.user("GET", "/api/deposit/me", "U100479", "", nil)
	if pg.body["awaitingPayee"] != true || pg.body["refundFailedPaymentId"] != refundPID {
		t.Fatalf("原路失败应等收款账户：%s", pg.raw)
	}
	e.event("pay.payee.submitted", reqID, map[string]any{"paymentId": refundPID, "bizNo": reqID, "account": "押金专户", "userId": "U100479", "orderId": nil, "submittedAt": e.now.Format(time.RFC3339)})
	pg = e.user("GET", "/api/deposit/me", "U100479", "", nil)
	if pg.body["awaitingPayee"] != false {
		t.Fatalf("提交收款账户后不再等待：%s", pg.raw)
	}
	e.event("pay.refund.succeeded", reqID, map[string]any{"paymentId": "PY-MAN-1", "bizNo": reqID, "type": "人工转账退押", "account": "押金专户", "userId": "U100479", "orderId": nil,
		"amount": 19900, "channel": "银行转账", "relatedPaymentId": refundPID, "completedAt": e.now.Format(time.RFC3339)})
	st = e.user("GET", "/api/deposit/me/state", "U100479", "", nil)
	if st.body["status"] != "已退" || st.body["refund"].(map[string]any)["manualPaymentId"] != "PY-MAN-1" {
		t.Fatalf("人工转账后应已退：%s", st.raw)
	}
	if ev := e.lastOf("deposit.refunded"); ev["mode"] != "人工转账" {
		t.Fatalf("已退押事件方式应为人工转账：%v", ev)
	}
	sm = e.do("GET", "/api/deposit/admin/summary", map[string]string{"Authorization": "Bearer " + adminToken("AC0201", nil, "deposit.view")}, nil)
	l := sm.body["ledger"].(map[string]any)
	if num(l["manualRefunded"]) != 19900 || num(l["refunded"]) != 0 || num(l["balance"]) != 0 || num(l["refundingAmount"]) != 0 {
		t.Fatalf("人工转账退押记账不对：%s", sm.raw)
	}
	f := e.do("GET", "/api/deposit/admin/flows?type=人工转账退押", map[string]string{"Authorization": "Bearer " + adminToken("AC0201", nil, "deposit.view")}, nil)
	rows := f.body["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["relatedPaymentId"] != refundPID {
		t.Fatalf("人工转账流水应指向失败的原路退押：%s", f.raw)
	}
}

// trace: pay 不可用时退押命令由定时任务补发
func TestRefundRetryWhenPayDown(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.depositUser("U1")
	mustStatus(t, e.user("POST", "/api/deposit/me/refund", "U1", "r1", nil), 200)
	reqID := e.lastOf("deposit.refund.requested")["requestId"].(string)
	e.pay.FailAll = true
	e.event("ride.admission.blocked", "U1", map[string]any{"userId": "U1", "requestId": reqID, "blockedAt": e.now.Format(time.RFC3339), "ridingOrderId": nil})
	st := e.user("GET", "/api/deposit/me/state", "U1", "", nil)
	if st.body["status"] != "退押中" {
		t.Fatalf("pay 不可用时保持退押中：%s", st.raw)
	}
	e.pay.FailAll = false
	e.app.RetryPending(t.Context())
	st = e.user("GET", "/api/deposit/me/state", "U1", "", nil)
	if st.body["status"] != "已退" {
		t.Fatalf("补发后应已退：%s", st.raw)
	}
}

// trace: 任务 10 · BR-25 按缴纳时金额退还（押金调价后不补差不退差）
func TestRefundAtPaidAmount(t *testing.T) {
	e := newEnv(t)
	e.rule("PR0001", time.Date(2026, 7, 1, 0, 0, 0, 0, loc), 9900, 550)
	e.depositUser("U100004")
	e.rule("PR0002", e.now.Add(-time.Minute), 19900, 550)
	pg := e.user("GET", "/api/deposit/me", "U100004", "", nil)
	if num(pg.body["rule"].(map[string]any)["depositAmount"]) != 19900 || num(pg.body["refundAmount"]) != 9900 || num(pg.body["deposit"].(map[string]any)["amountPaid"]) != 9900 {
		t.Fatalf("应按缴纳时金额：%s", pg.raw)
	}
	r := e.user("POST", "/api/deposit/me/refund", "U100004", "r1", nil)
	if num(r.body["refund"].(map[string]any)["amount"]) != 9900 {
		t.Fatalf("退 99 元：%s", r.raw)
	}
}

// trace: rule.params.changed 改退押时限
func TestRefundWorkdaysFromParams(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.event("rule.params.changed", "params", map[string]any{"depositRefundWorkdays": 5, "changedAt": e.now.Format(time.RFC3339)})
	e.depositUser("U1")
	pg := e.user("GET", "/api/deposit/me", "U1", "", nil)
	if num(pg.body["rule"].(map[string]any)["refundWorkdays"]) != 5 || pg.body["expectedAtIfApplyNow"] != "2026-12-08T18:00:00+08:00" {
		t.Fatalf("退押时限应取平台参数：%s", pg.raw)
	}
}

// trace: 任务 8 · R-10 客服协助退押（cs 调用），规则与自助相同
func TestAssistRefund(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.depositUser("U101097")
	body := map[string]any{"userId": "U101097", "reason": "用户来电要求退押", "actor": map[string]any{"kind": "人", "id": "AC0301", "name": "苏＊＊"}, "ticketId": "TK202612010001"}
	if r := e.internal("/internal/deposit/assist-refunds", "bill", "bill-key", "a1", body); r.status != 403 {
		t.Fatalf("只有 cs 能调：%s", r.raw)
	}
	mustCode(t, e.internal("/internal/deposit/assist-refunds", "cs", "cs-key", "a0", map[string]any{"userId": "U101097", "reason": "退", "actor": map[string]any{"kind": "人", "id": "AC0301"}}), 400, "REASON_REQUIRED")

	e.event("ride.order.created", "OD1", orderHead("OD1", "U101097"))
	r := e.internal("/internal/deposit/assist-refunds", "cs", "cs-key", "a1", body)
	mustStatus(t, r, 200)
	if r.body["result"] != "失败" || r.body["failCode"] != "REFUND_BLOCKED" || len(r.body["blockers"].([]any)) != 1 || !strings.Contains(r.body["failReason"].(string), "正在骑行") {
		t.Fatalf("有待处理项应失败：%s", r.raw)
	}
	e.event("ride.order.unlock_failed", "OD1", orderHead("OD1", "U101097"))
	r = e.internal("/internal/deposit/assist-refunds", "cs", "cs-key", "a2", body)
	if r.body["result"] != "成功" || r.body["refund"] == nil || r.body["text"] == "" {
		t.Fatalf("应受理：%s", r.raw)
	}
	var by map[string]any
	_ = testPool.QueryRow(t.Context(), `SELECT requested_by FROM refund_requests`).Scan(&by)
	if by["id"] != "AC0301" {
		t.Fatalf("应记客服为申请人：%v", by)
	}
	if ev := e.outbox(); ev[len(ev)-1]["actor"].(map[string]any)["id"] != "AC0301" && ev[len(ev)-2]["actor"].(map[string]any)["id"] != "AC0301" {
		t.Fatal("事件操作人应为客服")
	}
	r = e.internal("/internal/deposit/assist-refunds", "cs", "cs-key", "a3", body)
	if r.body["result"] != "失败" || r.body["failCode"] != "NOT_DEPOSIT_USER" {
		t.Fatalf("已在退押中：%s", r.raw)
	}
}

// 退押时有处理中的抵扣，视为待支付订单
func TestRefundBlockedByPendingDeduction(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.depositUser("U1")
	e.pay.Next(payfake.Outcome{Result: "结果未知"})
	_ = e.deduct("d1", "U1", "OD1", 300)
	r := e.user("POST", "/api/deposit/me/refund", "U1", "r1", nil)
	mustCode(t, r, 409, "REFUND_BLOCKED")
	if b := r.body["blockers"].([]any)[0].(map[string]any); b["code"] != "UNPAID_ORDER" || b["orderId"] != "OD1" {
		t.Fatalf("处理中的抵扣应为待支付项：%s", r.raw)
	}
}

package app

import (
	"net/url"
	"testing"

	"bike/bill/internal/clients/pay"
)

func urlq(s string) string { return url.QueryEscape(s) }

func (e *env) paidCreditBill(order, user string, minutes int64, inStation bool) {
	e.depositMode(user, "免押", nil, nil)
	e.must(e.endOrder(endOpt{order: order, user: user, minutes: minutes, inStation: inStation}))
}

func csHdr(key string) map[string]string {
	return map[string]string{"X-Caller": "cs", "Idempotency-Key": key}
}

var csActor = map[string]any{"kind": "人", "id": "AC0301", "name": "客服甲"}

// R-10 BR-06：部分退款原路退回，更新已退款与订单实收，发"已退款"
func TestRefund_Partial(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.paidCreditBill("OD1", "U100484", 18, false) // 900+500
	cmd := map[string]any{"orderId": "OD1", "amount": 500, "reason": "站外调度费有误，退还调度费", "ticketId": "TK1", "actor": csActor}
	st, m, _ := e.call("POST", "/internal/bill/refunds", "cs-key", cmd, csHdr("r1"))
	if st != 200 || m["result"] != "成功" || m["refunded"].(float64) != 500 || m["refundStatus"] != "部分退款" {
		t.Fatalf("%d %v", st, m)
	}
	b := e.bill("OD1")
	if b.Refunded != 500 || b.amounts().NetReceived() != 900 {
		t.Fatalf("%+v", b)
	}
	ev := e.events("bill.bill.refunded")
	if len(ev) != 1 || ev[0]["kind"] != "部分退款" || ev[0]["netReceived"].(float64) != 900 || ev[0]["ticketId"] != "TK1" || ev[0]["receivable"].(float64) != 1400 {
		t.Fatalf("%v", ev)
	}
	if au := e.events("bill.audit.recorded"); len(au) != 1 || au[0]["action"] != "执行退款" {
		t.Fatalf("%v", au)
	}
	// 同一幂等键重复到达：返回首次结果，不再调 pay
	st2, m2, _ := e.call("POST", "/internal/bill/refunds", "cs-key", cmd, csHdr("r1"))
	if st2 != 200 || m2["refunded"].(float64) != 500 || e.pay.count("/internal/pay/refunds") != 1 {
		t.Fatal("幂等键应返回首次结果")
	}
	// pay 的退款成功事件随后到达，不重复累计
	e.must(e.deliver("pay.refund.succeeded", map[string]any{"paymentId": m["paymentId"], "type": "退款", "userId": "U100484", "orderId": "OD1", "amount": 500}))
	if e.bill("OD1").Refunded != 500 {
		t.Fatal("退款不应重复累计")
	}
	// 超过可退金额
	st, m, _ = e.call("POST", "/internal/bill/refunds", "cs-key", map[string]any{"orderId": "OD1", "amount": 1000, "reason": "再退一次试试", "actor": csActor}, csHdr("r2"))
	if st != 200 || m["result"] != "失败" || m["failCode"] != "EXCEED_REFUNDABLE" {
		t.Fatalf("%d %v", st, m)
	}
	// 全额退完
	st, m, _ = e.call("POST", "/internal/bill/refunds", "cs-key", map[string]any{"orderId": "OD1", "amount": 900, "reason": "车辆故障全额退款", "actor": csActor}, csHdr("r3"))
	if st != 200 || m["refundStatus"] != "全额退款" {
		t.Fatalf("%d %v", st, m)
	}
	_, f, _ := e.call("GET", "/internal/bill/orders/OD1", "cs-key", nil, map[string]string{"X-Caller": "cs"})
	if f["refundable"].(float64) != 0 || f["refundStatus"] != "全额退款" {
		t.Fatalf("%v", f)
	}
}

func TestRefund_Validation(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.paidCreditBill("OD1", "U1", 10, true)
	ok := map[string]any{"orderId": "OD1", "amount": 100, "reason": "用户投诉多扣", "actor": csActor}
	if st, m, _ := e.call("POST", "/internal/bill/refunds", "cs-key", ok, map[string]string{"X-Caller": "cs"}); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("没带幂等键应 400：%d", st)
	}
	if st, _, _ := e.call("POST", "/internal/bill/refunds", "wrong", ok, csHdr("a")); st != 401 {
		t.Fatalf("凭证不对应 401：%d", st)
	}
	if st, _, _ := e.call("POST", "/internal/bill/refunds", "cs-key", ok, map[string]string{"X-Caller": "ride", "Idempotency-Key": "a"}); st != 401 {
		t.Fatal("不在调用方名单应 401")
	}
	if st, m, _ := e.call("POST", "/internal/bill/refunds", "cs-key", map[string]any{"orderId": "OD1", "amount": 100, "reason": "退", "actor": csActor}, csHdr("b")); st != 400 || m["code"] != "REASON_REQUIRED" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m, _ := e.call("POST", "/internal/bill/refunds", "cs-key", map[string]any{"orderId": "OD1", "amount": 0, "reason": "金额为零的退款", "actor": csActor}, csHdr("c")); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("%d %v", st, m)
	}
	if st, _, _ := e.call("POST", "/internal/bill/refunds", "cs-key", map[string]any{"orderId": "ODX", "amount": 100, "reason": "没有这个订单", "actor": csActor}, csHdr("d")); st != 404 {
		t.Fatal(st)
	}
}

// 押金抵扣部分不在线退：没有渠道收款
func TestRefund_NothingToRefund(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "押金", ptr("已缴"), ptr(int64(19900)))
	e.dep.balance = 19900
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 5, inStation: true}))
	e.clock = e.clock.Add(25 * 3600e9)
	e.must(e.a.RunDeadlines(t.Context()))
	_, m, _ := e.call("POST", "/internal/bill/refunds", "cs-key", map[string]any{"orderId": "OD1", "amount": 100, "reason": "押金抵扣的单退款", "actor": csActor}, csHdr("x"))
	if m["result"] != "失败" || m["failCode"] != "NOTHING_TO_REFUND" {
		t.Fatalf("%v", m)
	}
}

// R-11：原路失败待人工转账；人工转账完成后退款状态恢复
func TestRefund_ChannelFailedThenManual(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.paidCreditBill("OD1", "U1", 10, true) // 500
	fr := "原支付账户已注销"
	e.pay.set("/internal/pay/refunds", pay.Result{Result: "失败", FailCode: ptr("CHANNEL_FAILED"), FailReason: &fr, PaymentID: "PY-RF"})
	_, m, _ := e.call("POST", "/internal/bill/refunds", "cs-key", map[string]any{"orderId": "OD1", "amount": 500, "reason": "全额退款给用户", "actor": csActor, "ticketId": "TK9"}, csHdr("x"))
	if m["result"] != "失败" || m["failCode"] != "CHANNEL_FAILED" || m["awaitingPayee"] != true || m["refundStatus"] != "原路失败·待人工转账" {
		t.Fatalf("%v", m)
	}
	_, ob, _ := e.call("GET", "/api/bill/me/orders/OD1", e.userToken("U1"), nil, nil)
	if ob["refundFailedPaymentId"] != "PY-RF" || ob["statusText"] != "退款处理中" {
		t.Fatalf("%v", ob)
	}
	e.must(e.deliver("pay.refund.succeeded", map[string]any{"paymentId": "PY-MAN", "type": "人工转账退款", "userId": "U1", "orderId": "OD1", "amount": 500, "relatedPaymentId": "PY-RF"}))
	b := e.bill("OD1")
	if b.Refunded != 500 || b.RefundFailedPaymentID != nil || b.refundStatus() != "全额退款" {
		t.Fatalf("%+v", b)
	}
	ev := e.events("bill.bill.refunded")
	if len(ev) != 1 || ev[0]["kind"] != "全额退款" || ev[0]["ticketId"] != "TK9" {
		t.Fatalf("%v", ev)
	}
}

// 退款结果未知：可退金额扣除处理中的退款，最终以退款事件为准
func TestRefund_Unknown(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.paidCreditBill("OD1", "U1", 10, true) // 500
	e.pay.set("/internal/pay/refunds", pay.Result{Result: "结果未知", PaymentID: "PY-R1"})
	_, m, _ := e.call("POST", "/internal/bill/refunds", "cs-key", map[string]any{"orderId": "OD1", "amount": 300, "reason": "部分退款给用户", "actor": csActor}, csHdr("x"))
	if m["result"] != "结果未知" {
		t.Fatalf("%v", m)
	}
	_, f, _ := e.call("GET", "/internal/bill/orders/OD1", "cs-key", nil, map[string]string{"X-Caller": "cs"})
	if f["refundable"].(float64) != 200 {
		t.Fatalf("处理中的退款应占用可退金额：%v", f)
	}
	e.must(e.deliver("pay.refund.failed", map[string]any{"paymentId": "PY-R1", "type": "退款", "userId": "U1", "orderId": "OD1", "amount": 300, "failReason": "原支付账户已注销"}))
	_, f, _ = e.call("GET", "/internal/bill/orders/OD1", "cs-key", nil, map[string]string{"X-Caller": "cs"})
	if f["refundable"].(float64) != 500 || f["refundStatus"] != "原路失败·待人工转账" {
		t.Fatalf("%v", f)
	}
}

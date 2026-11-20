package app

import (
	"testing"
	"time"

	"bike/deposit/internal/clients/pay/payfake"
)

func (e *env) deduct(idem, uid, order string, amount int64) resp {
	return e.internal("/internal/deposit/deductions", "bill", "bill-key", idem, map[string]any{"userId": uid, "orderId": order, "amount": amount})
}

// trace: 任务 7 · BR-22 余额不够抵多少算多少
func TestDeductPartial(t *testing.T) {
	e := newEnv(t)
	e.rule("PR0001", time.Date(2026, 7, 1, 0, 0, 0, 0, loc), 9900, 550)
	e.verified("U100054")
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U100054", "p1", nil), 200)

	r := e.deduct("d1", "U100054", "OD1", 4500)
	mustStatus(t, r, 200)
	if r.body["result"] != "成功" || num(r.body["deducted"]) != 4500 || num(r.body["shortfall"]) != 0 || num(r.body["balanceAfter"]) != 5400 || r.body["status"] != "押金不足" {
		t.Fatalf("第一次抵扣不对：%s", r.raw)
	}
	r = e.deduct("d2", "U100054", "OD2", 8000)
	if r.body["result"] != "成功" || num(r.body["deducted"]) != 5400 || num(r.body["shortfall"]) != 2600 || num(r.body["balanceAfter"]) != 0 {
		t.Fatalf("余额不够应抵多少算多少：%s", r.raw)
	}
	calls := e.pay.Calls()
	last := calls[len(calls)-1]
	if last.Path != "/internal/pay/deposit-transfers" || last.Body["orderId"] != "OD2" || num(last.Body["amount"]) != 5400 {
		t.Fatalf("应调 pay 专户划转实际抵扣额：%+v", last)
	}
	ev := e.lastOf("deposit.deducted")
	if ev["orderId"] != "OD2" || num(ev["amount"]) != 5400 || num(ev["balanceAfter"]) != 0 {
		t.Fatalf("押金已抵扣事件不对：%v", ev)
	}
	if e.countOf("deposit.audit.recorded") < 2 {
		t.Fatal("押金抵扣要留痕")
	}
	au := e.lastOf("deposit.audit.recorded")
	if au["action"] != "抵扣押金" || au["targetType"] != "order" || au["targetId"] != "OD2" {
		t.Fatalf("留痕不对：%v", au)
	}
	// 余额为 0
	r = e.deduct("d3", "U100054", "OD3", 100)
	if r.body["result"] != "失败" || r.body["failCode"] != "NO_BALANCE" || num(r.body["shortfall"]) != 100 {
		t.Fatalf("余额为 0 应失败 NO_BALANCE：%s", r.raw)
	}
	// 幂等：同键返回首次结果
	r2 := e.deduct("d2", "U100054", "OD2", 8000)
	if num(r2.body["deducted"]) != 5400 || r2.body["paymentId"] != r.body["paymentId"] && r2.body["paymentId"] == nil {
		t.Fatalf("同键应返回首次结果：%s", r2.raw)
	}
	if len(e.pay.Calls()) != len(calls) {
		t.Fatal("同键重复不应再划转")
	}
}

// trace: BR-22 只抵押金用户；免押、未开通不抵
func TestDeductNotDepositUser(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	r := e.deduct("d1", "U999", "OD1", 100)
	mustStatus(t, r, 200)
	if r.body["result"] != "失败" || r.body["failCode"] != "NOT_DEPOSIT_USER" || num(r.body["deducted"]) != 0 {
		t.Fatalf("非押金用户应失败：%s", r.raw)
	}
	mustCode(t, e.deduct("d2", "U1", "OD1", 0), 400, "INVALID")
	mustCode(t, e.deduct("d3", "", "OD1", 10), 400, "INVALID")
}

// trace: BR-22 只抵本人的订单
func TestDeductOtherUsersOrder(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U1")
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U1", "p1", nil), 200)
	e.event("bill.bill.issued", "OD9", billIssued("OD9", "U2", 500, "一键支付"))
	mustCode(t, e.deduct("d1", "U1", "OD9", 500), 400, "INVALID")
}

// trace: BR-22 专户划转失败不动押金
func TestDeductTransferFailed(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U1")
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U1", "p1", nil), 200)
	e.pay.Next(payfake.Outcome{Result: "失败", FailCode: "DECLINED", FailReason: "专户余额异常"})
	r := e.deduct("d1", "U1", "OD1", 500)
	if r.body["result"] != "失败" || r.body["failCode"] != "TRANSFER_FAILED" || num(r.body["deducted"]) != 0 || num(r.body["balanceAfter"]) != 19900 {
		t.Fatalf("划转失败应不动押金：%s", r.raw)
	}
	st := e.user("GET", "/api/deposit/me/state", "U1", "", nil)
	if num(st.body["balance"]) != 19900 || st.body["status"] != "已缴" {
		t.Fatalf("余额应恢复：%s", st.raw)
	}
}

// trace: BR-22 划转结果未知，以 pay 事件为准
func TestDeductUnknownThenEvent(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U1")
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U1", "p1", nil), 200)
	e.pay.Next(payfake.Outcome{Result: "结果未知"})
	r := e.deduct("d1", "U1", "OD1", 500)
	if r.body["result"] != "结果未知" {
		t.Fatalf("应为结果未知：%s", r.raw)
	}
	if e.countOf("deposit.deducted") != 0 {
		t.Fatal("结果未知时不发押金已抵扣")
	}
	biz := e.pay.Calls()[1].Body["bizNo"].(string)
	e.event("pay.payment.succeeded", biz, map[string]any{"paymentId": "PY-T-1", "bizNo": biz, "type": "押金抵扣", "account": "押金专户", "userId": "U1", "orderId": "OD1",
		"amount": 500, "channel": "押金专户划转", "completedAt": e.now.Format(time.RFC3339), "channelTradeNo": "ZH1"})
	ev := e.lastOf("deposit.deducted")
	if num(ev["amount"]) != 500 || num(ev["balanceAfter"]) != 19400 {
		t.Fatalf("事件不对：%v", ev)
	}
	// 同键重试拿到成功
	r = e.deduct("d1", "U1", "OD1", 500)
	if r.body["result"] != "成功" || r.body["paymentId"] != "PY-T-1" {
		t.Fatalf("同键重试应拿到最终结果：%s", r.raw)
	}
}

func billIssued(order, uid string, payable int64, collectBy string) map[string]any {
	return map[string]any{"orderId": order, "userId": uid, "brandId": "BR01", "operatorId": "OP01", "pricingVersionId": "PR0002", "billedMinutes": 10,
		"rideFee": payable, "dispatchFee": 0, "receivable": payable, "monthCardDeduct": 0, "monthCardId": nil, "couponDeduct": 0, "payable": payable,
		"collectBy": collectBy, "payDeadline": nil, "billPushedAt": "2026-12-01T12:00:00+08:00", "orderEndAt": "2026-12-01T12:00:00+08:00", "degraded": false}
}

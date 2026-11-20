package app

import (
	"testing"
	"time"

	"bike/deposit/internal/clients/pay/payfake"
)

// trace: 任务 5 · R-15 R-18 BR-21 押金缴纳
func TestPayDepositSuccess(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U100087")
	st := e.user("GET", "/api/deposit/me/state", "U100087", "", nil)
	mustStatus(t, st, 200)
	if st.body["mode"] != "未开通" {
		t.Fatalf("新用户应为未开通：%s", st.raw)
	}

	r := e.user("POST", "/api/deposit/me/payment", "U100087", "k1", nil)
	mustStatus(t, r, 200)
	if num(r.body["amount"]) != 19900 || r.body["paymentId"] == "" {
		t.Fatalf("金额取现行规则 19900：%s", r.raw)
	}
	d := r.body["deposit"].(map[string]any)
	if d["mode"] != "押金" || d["status"] != "已缴" || num(d["amountPaid"]) != 19900 || num(d["balance"]) != 19900 || d["paymentId"] != r.body["paymentId"] {
		t.Fatalf("押金形态不对：%s", r.raw)
	}
	calls := e.pay.Calls()
	if len(calls) != 1 || calls[0].Path != "/internal/pay/deposit-charges" || calls[0].Body["type"] != "押金缴纳" || calls[0].Body["channel"] != "微信支付" || calls[0].Caller != "deposit" {
		t.Fatalf("应调 pay 押金收款：%+v", calls)
	}
	ev := e.lastOf("deposit.status.changed")
	if ev["mode"] != "押金" || ev["status"] != "已缴" || ev["rideable"] != true || num(ev["balance"]) != 19900 {
		t.Fatalf("状态事件不对：%v", ev)
	}
	// 台账有一笔缴纳
	f := e.do("GET", "/api/deposit/admin/flows", map[string]string{"Authorization": "Bearer " + adminToken("AC0201", nil, "deposit.view")}, nil)
	mustStatus(t, f, 200)
	rows := f.body["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["type"] != "押金缴纳" || rows[0].(map[string]any)["direction"] != "收" || rows[0].(map[string]any)["status"] != "成功" {
		t.Fatalf("台账不对：%s", f.raw)
	}
}

// trace: R-15 未实名或冻结的不能缴押金；已缴不能重复
func TestPayDepositRejected(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	mustCode(t, e.user("POST", "/api/deposit/me/payment", "U100127", "k1", nil), 409, "NEED_ONBOARDING")
	e.verified("U100712")
	e.event("acct.account.frozen", "U100712", map[string]any{"userId": "U100712", "reason": "欠费追缴", "frozenAt": e.now.Format(time.RFC3339), "unfreezeHow": "结清欠费后自动解冻"})
	mustCode(t, e.user("POST", "/api/deposit/me/payment", "U100712", "k1", nil), 409, "ACCOUNT_FROZEN")
	e.event("acct.account.unfrozen", "U100712", map[string]any{"userId": "U100712", "mode": "自动解冻", "unfrozenAt": e.now.Format(time.RFC3339)})
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U100712", "k2", nil), 200)
	mustCode(t, e.user("POST", "/api/deposit/me/payment", "U100712", "k3", nil), 409, "ALREADY_DONE")
	if len(e.pay.Calls()) != 1 {
		t.Fatal("被拒的请求不应调 pay")
	}
}

// trace: 没有现行规则时不收钱
func TestPayDepositNoRule(t *testing.T) {
	e := newEnv(t)
	e.verified("U1")
	mustCode(t, e.user("POST", "/api/deposit/me/payment", "U1", "k1", nil), 503, "SERVER_BUSY")
	// 待生效版本不算现行
	e.rule("PR0004", e.now.Add(24*time.Hour), 29900, 600)
	mustCode(t, e.user("POST", "/api/deposit/me/payment", "U1", "k2", nil), 503, "SERVER_BUSY")
}

// trace: 渠道拒绝 → PAY_DECLINED，不动押金
func TestPayDepositDeclined(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U1")
	e.pay.Next(payfake.Outcome{Result: "失败", FailCode: "DECLINED", FailReason: "用户支付账户余额不足"})
	r := e.user("POST", "/api/deposit/me/payment", "U1", "k1", nil)
	mustCode(t, r, 409, "PAY_DECLINED")
	if r.body["charged"] != false {
		t.Fatal("用户侧失败必有 charged")
	}
	st := e.user("GET", "/api/deposit/me/state", "U1", "", nil)
	if st.body["mode"] != "未开通" {
		t.Fatal("失败不应开通押金")
	}
	// 换键重试可以成功
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U1", "k2", nil), 200)
}

// trace: 收款结果未知 → CONFLICT；之后以 pay 事件为准
func TestPayDepositUnknownThenEvent(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U1")
	e.pay.Next(payfake.Outcome{Result: "结果未知"})
	mustCode(t, e.user("POST", "/api/deposit/me/payment", "U1", "k1", nil), 409, "CONFLICT")
	// 结果未知期间换键再缴：拒绝，避免重复收款
	mustCode(t, e.user("POST", "/api/deposit/me/payment", "U1", "k2", nil), 409, "CONFLICT")
	call := e.pay.Calls()[0]
	e.event("pay.payment.succeeded", call.Body["bizNo"].(string), map[string]any{"paymentId": "PY-OK-1", "bizNo": call.Body["bizNo"], "type": "押金缴纳", "account": "押金专户",
		"userId": "U1", "orderId": nil, "amount": 19900, "channel": "微信支付", "completedAt": e.now.Format(time.RFC3339), "channelTradeNo": "4200"})
	st := e.user("GET", "/api/deposit/me/state", "U1", "", nil)
	if st.body["mode"] != "押金" || st.body["status"] != "已缴" || st.body["paymentId"] != "PY-OK-1" {
		t.Fatalf("收到收款成功事件后应已缴：%s", st.raw)
	}
	// 同一键重试返回成功结果
	r := e.user("POST", "/api/deposit/me/payment", "U1", "k1", nil)
	mustStatus(t, r, 200)
	if r.body["paymentId"] != "PY-OK-1" {
		t.Fatalf("同键重试应取到最终结果：%s", r.raw)
	}
}

// trace: 结果未知后 pay 发收款失败事件
func TestPayDepositUnknownThenFailed(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U1")
	e.pay.Next(payfake.Outcome{Result: "结果未知"})
	mustCode(t, e.user("POST", "/api/deposit/me/payment", "U1", "k1", nil), 409, "CONFLICT")
	biz := e.pay.Calls()[0].Body["bizNo"].(string)
	e.event("pay.payment.failed", biz, map[string]any{"paymentId": "PY-F-1", "bizNo": biz, "type": "押金缴纳", "account": "押金专户", "userId": "U1", "orderId": nil,
		"amount": 19900, "channel": "微信支付", "failReason": "用户取消", "failedAt": e.now.Format(time.RFC3339)})
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U1", "k2", nil), 200)
}

// trace: 任务 10 · BR-25 按缴纳时金额；补缴到缴纳时金额，不补差
func TestTopupToAmountPaid(t *testing.T) {
	e := newEnv(t)
	e.rule("PR0001", time.Date(2026, 7, 1, 0, 0, 0, 0, loc), 9900, 550)
	e.verified("U100249")
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U100249", "k1", nil), 200)
	mustCode(t, e.user("POST", "/api/deposit/me/topup", "U100249", "t0", nil), 409, "NOTHING_TO_PAY")
	// 调价到 199
	e.rule("PR0002", e.now.Add(-time.Hour), 19900, 550)
	// 抵扣 30 元
	r := e.internal("/internal/deposit/deductions", "bill", "bill-key", "d1", map[string]any{"userId": "U100249", "orderId": "OD2026091813663", "amount": 3000})
	mustStatus(t, r, 200)
	if r.body["result"] != "成功" || num(r.body["balanceAfter"]) != 6900 || r.body["status"] != "押金不足" {
		t.Fatalf("抵扣结果不对：%s", r.raw)
	}
	tp := e.user("POST", "/api/deposit/me/topup", "U100249", "t1", nil)
	mustStatus(t, tp, 200)
	if num(tp.body["amount"]) != 3000 {
		t.Fatalf("补缴应为缴纳时金额与余额之差 3000，不补调价差：%s", tp.raw)
	}
	d := tp.body["deposit"].(map[string]any)
	if d["status"] != "已缴" || num(d["balance"]) != 9900 || num(d["amountPaid"]) != 9900 || len(d["topupPaymentIds"].([]any)) != 1 || len(d["deductionPaymentIds"].([]any)) != 1 {
		t.Fatalf("补缴后状态不对：%s", tp.raw)
	}
	if c := e.pay.Calls(); c[len(c)-1].Body["type"] != "押金补缴" {
		t.Fatal("补缴应以押金补缴类型调 pay")
	}
}

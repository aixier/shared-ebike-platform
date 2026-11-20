package app

// 页面接口：退款失败与人工转账、流水查询、待办、骑行收入户汇总、鉴权。
// trace: R-11 R-12 R-18 R-25 R-47；pay.yaml /api/pay/*

import (
	"context"
	"testing"
	"time"

	"bike/pay/internal/channel"
)

// failRefund 给 U100484 造一笔原路失败的退款，返回失败退款流水号与订单号
func failRefund(t *testing.T) (string, string) {
	t.Helper()
	order := newBiz("OD")
	// 收款时让该用户的渠道行为为成功，退款时恢复为失败
	for _, m := range tMocks {
		m.Cfg.Users["U100484"] = channel.BehaveOK
	}
	payOrder(t, "U100484", order, 300, "微信支付")
	for _, m := range tMocks {
		m.Cfg.Users["U100484"] = channel.BehaveFail
	}
	r := internal(t, "bill", "/internal/pay/refunds", newKey(), map[string]any{"userId": "U100484", "orderId": order, "amount": 300, "ticketId": "TK202611290031"})
	if r.Body["result"] != "失败" || r.Body["failCode"] != "CHANNEL_FAILED" {
		t.Fatalf("%s", r.Raw)
	}
	return r.Body["paymentId"].(string), order
}

func findFailure(t *testing.T, id string) map[string]any {
	r := do(t, "GET", "/api/pay/admin/refund-failures", finance(), nil, nil)
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	for _, x := range r.List {
		if x["paymentId"] == id {
			return x
		}
	}
	return nil
}

// TC-PAY-API-01：原路失败 → 退款失败事件；用户提交本人账户（户名核对）→ 待转账；财务录入人工转账 → 退款成功与留痕
func TestRefundFailureToManualTransfer(t *testing.T) {
	setClock(time.Date(2026, 12, 1, 14, 0, 0, 0, bj))
	id, order := failRefund(t)
	e := lastEvent(t, "pay.refund.failed", order)
	if e["payload"].(map[string]any)["failReason"] != "原支付账户已注销，渠道拒绝原路退回" {
		t.Fatalf("%v", e)
	}
	f := findFailure(t, id)
	if f == nil || f["status"] != "待用户填写收款账户" || f["ticketId"] != "TK202611290031" {
		t.Fatalf("%v", f)
	}
	// 财务在用户提交前转账：缺收款账户
	if x := do(t, "POST", "/api/pay/admin/refund-failures/"+id+"/transfer", finance(), nil, map[string]any{"reason": "原路失败人工退回"}); x.Status != 400 || x.Body["code"] != "INVALID" {
		t.Fatalf("%d %s", x.Status, x.Raw)
	}
	path := "/api/pay/me/payments/" + id + "/payee"
	if x := do(t, "POST", path, userToken("U200001"), nil, map[string]any{"name": "张三", "bank": "示例银行", "account": "6214123412347908"}); x.Status != 404 {
		t.Fatalf("别人的退款应 404：%d", x.Status)
	}
	if x := do(t, "POST", path, userToken("U100484"), nil, map[string]any{"name": "薛大明", "bank": "示例银行", "account": "6214123412347908"}); x.Status != 400 || x.Body["code"] != "PAYEE_NAME_MISMATCH" {
		t.Fatalf("%d %s", x.Status, x.Raw)
	}
	if x := do(t, "POST", path, userToken("U100484"), nil, map[string]any{"name": "薛小明", "bank": "示例银行", "account": "62141"}); x.Status != 400 || x.Body["code"] != "INVALID" {
		t.Fatalf("%d %s", x.Status, x.Raw)
	}
	x := do(t, "POST", path, userToken("U100484"), map[string]string{"Idempotency-Key": "pk1"}, map[string]any{"name": "薛小明", "bank": "示例农商银行", "account": "6214 1234 1234 7908"})
	if x.Status != 200 || x.Body["ok"] != true {
		t.Fatalf("%d %s", x.Status, x.Raw)
	}
	pe := x.Body["payee"].(map[string]any)
	if pe["name"] != "薛＊＊" || pe["account"] != "6214 **** **** 7908" {
		t.Fatalf("应脱敏：%v", pe)
	}
	sub := lastEvent(t, "pay.payee.submitted", order)
	if _, has := sub["payload"].(map[string]any)["name"]; has {
		t.Fatal("事件载荷不得含账户信息")
	}
	if f := findFailure(t, id); f["status"] != "待转账" || f["payeeSubmitted"] == nil {
		t.Fatalf("%v", f)
	}
	tp := "/api/pay/admin/refund-failures/" + id + "/transfer"
	if y := do(t, "POST", tp, finance(), nil, map[string]any{"reason": "转"}); y.Status != 400 || y.Body["code"] != "REASON_REQUIRED" {
		t.Fatalf("%d %s", y.Status, y.Raw)
	}
	y := do(t, "POST", tp, finance(), map[string]string{"Idempotency-Key": "tk1"}, map[string]any{"reason": "原路退回失败，银行转账退回", "tradeNo": "HX20261201001"})
	if y.Status != 200 || y.Body["type"] != "人工转账退款" || y.Body["channel"] != "银行转账" || y.Body["relatedPaymentId"] != id ||
		y.Body["operatorAccountId"] != "AC0301" || y.Body["channelTradeNo"] != "HX20261201001" || y.Body["status"] != "成功" {
		t.Fatalf("%d %s", y.Status, y.Raw)
	}
	if y.Body["payee"].(map[string]any)["account"] != "6214 **** **** 7908" {
		t.Fatalf("%v", y.Body["payee"])
	}
	y2 := do(t, "POST", tp, finance(), map[string]string{"Idempotency-Key": "tk1"}, map[string]any{"reason": "原路退回失败，银行转账退回"})
	if y2.Status != 200 || y2.Body["paymentId"] != y.Body["paymentId"] {
		t.Fatalf("同键重放应返回首次结果：%s", y2.Raw)
	}
	if z := do(t, "POST", tp, finance(), nil, map[string]any{"reason": "再转一次试试"}); z.Status != 409 {
		t.Fatalf("已转账应 409：%d", z.Status)
	}
	if findFailure(t, id) != nil {
		t.Fatal("已人工转账的不应再在清单里")
	}
	ev := eventsOf(t, order)
	if ev[len(ev)-1] != "pay.refund.succeeded" {
		t.Fatalf("%v", ev)
	}
	a := lastEvent(t, "pay.audit.recorded", "payment:"+y.Body["paymentId"].(string))
	if a["payload"].(map[string]any)["action"] != "人工转账" || a["actor"].(map[string]any)["id"] != "AC0301" {
		t.Fatalf("%v", a)
	}
	// 订单退款累计：失败的原路退款已占额度，不能再退
	if r := internal(t, "bill", "/internal/pay/refunds", newKey(), map[string]any{"userId": "U100484", "orderId": order, "amount": 1}); r.Status != 409 || r.Body["code"] != "CONFLICT" {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
}

// TC-PAY-API-02：鉴权：未登录、无权限、运营商数据范围、只读
func TestAdminAuth(t *testing.T) {
	if r := do(t, "GET", "/api/pay/admin/recon", "", nil, nil); r.Status != 401 || r.Body["code"] != "ADMIN_NOT_LOGGED_IN" {
		t.Fatalf("%d", r.Status)
	}
	if r := do(t, "GET", "/api/pay/admin/recon", adminToken("AC1", []string{"order.view"}, nil, false), nil, nil); r.Status != 403 || r.Body["code"] != "FORBIDDEN" {
		t.Fatalf("%d", r.Status)
	}
	if r := do(t, "GET", "/api/pay/admin/orders/OD1/payments", adminToken("AC2", []string{"settlement.manage"}, "OP01", false), nil, nil); r.Status != 403 || r.Body["code"] != "SCOPE_DENIED" {
		t.Fatalf("%d", r.Status)
	}
	if r := do(t, "POST", "/api/pay/admin/refund-failures/PY1/transfer", adminToken("AC3", []string{"refund.manualTransfer"}, nil, true), nil, map[string]any{"reason": "试一下只读"}); r.Status != 403 || r.Body["code"] != "READONLY" {
		t.Fatalf("%d", r.Status)
	}
	if r := do(t, "GET", "/api/pay/me/payments?orderId=OD1", "", nil, nil); r.Status != 401 || r.Body["code"] != "NOT_LOGGED_IN" || r.Body["charged"] != false {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	if r := do(t, "GET", "/api/pay/me/payments", userToken("U1"), nil, nil); r.Status != 400 {
		t.Fatalf("缺订单号应 400：%d", r.Status)
	}
}

// TC-PAY-API-03：按订单号、按用户编号查流水；本人只看本人
func TestPaymentQueries(t *testing.T) {
	order := newBiz("OD")
	pid := payOrder(t, "U200002", order, 650, "支付宝")
	r := do(t, "GET", "/api/pay/me/payments?orderId="+order, userToken("U200002"), nil, nil)
	if r.Status != 200 || len(r.List) != 1 || r.List[0]["paymentId"] != pid || r.List[0]["channel"] != "支付宝" {
		t.Fatalf("%s", r.Raw)
	}
	if _, has := r.List[0]["account"]; has {
		t.Fatal("用户视图不含账户字段")
	}
	if r := do(t, "GET", "/api/pay/me/payments?orderId="+order, userToken("U200001"), nil, nil); len(r.List) != 0 {
		t.Fatal("非本人订单应为空")
	}
	a := do(t, "GET", "/api/pay/admin/orders/"+order+"/payments", finance(), nil, nil)
	if len(a.List) != 1 || a.List[0]["account"] != "骑行收入户" || a.List[0]["direction"] != "收" {
		t.Fatalf("%s", a.Raw)
	}
	u := do(t, "GET", "/api/pay/admin/users/U200002/payments?pageSize=2&type=订单支付", finance(), nil, nil)
	if u.Status != 200 || u.Body["pageSize"].(float64) != 2 || len(u.Body["rows"].([]any)) == 0 || u.Body["total"].(float64) < 1 {
		t.Fatalf("%s", u.Raw)
	}
	for _, x := range u.Body["rows"].([]any) {
		if x.(map[string]any)["type"] != "订单支付" {
			t.Fatal("类型筛选无效")
		}
	}
}

// TC-PAY-API-04：骑行收入户汇总 = 收款 + 月卡款 − 退款
func TestIncomeSummary(t *testing.T) {
	before := do(t, "GET", "/api/pay/admin/income-summary", finance(), nil, nil).Body
	order := newBiz("OD")
	payOrder(t, "U200001", order, 1000, "微信支付")
	internal(t, "bill", "/internal/pay/refunds", newKey(), map[string]any{"userId": "U200001", "orderId": order, "amount": 200})
	after := do(t, "GET", "/api/pay/admin/income-summary", finance(), nil, nil).Body
	d := func(k string) float64 { return after[k].(float64) - before[k].(float64) }
	if d("received") != 1000 || d("refunded") != 200 || d("rideIncomeNet") != 800 || after["asOf"] == nil {
		t.Fatalf("%v → %v", before, after)
	}
	if after["rideIncomeNet"].(float64) != after["received"].(float64)+after["monthCardIncome"].(float64)-after["refunded"].(float64) {
		t.Fatal("净额恒等式不成立")
	}
}

// TC-PAY-API-05：待办只返回有权限且大于 0 的项
func TestTodos(t *testing.T) {
	failRefund(t)
	r := do(t, "GET", "/api/pay/admin/todos", finance(), nil, nil)
	found := false
	for _, x := range r.List {
		if x["key"] == "refundFail" && x["count"].(float64) > 0 && x["menu"] == "欠费与退款失败" {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s", r.Raw)
	}
	r2 := do(t, "GET", "/api/pay/admin/todos", adminToken("AC9", []string{"order.view"}, nil, false), nil, nil)
	if r2.Status != 200 || len(r2.List) != 0 {
		t.Fatalf("无权限应为空：%s", r2.Raw)
	}
	_ = context.Background()
}

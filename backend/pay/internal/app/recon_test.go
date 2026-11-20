package app

// 每日收单对账端到端：模拟渠道账单（当日、跨日、未到账）→ 次日 02:10 出具 → 财务处理差异。
// trace: R-25、R-18、R-47；SCHEMA 4.5；pay.yaml /api/pay/admin/recon*

import (
	"context"
	"testing"
	"time"
)

func at(h, m int) time.Time { return time.Date(2031, 3, 1, h, m, 0, 0, bj) }

// TC-PAY-RECON-10：对账口径与差异项（订单收款、退款、月卡、押金抵扣、人工转账、跨日、未到账）
func TestDailyReconEndToEnd(t *testing.T) {
	setClock(at(10, 0))
	order := newBiz("OD")
	payOrder(t, "U200001", order, 800, "微信支付")
	setClock(at(11, 0))
	if r := internal(t, "bill", "/internal/pay/refunds", newKey(), map[string]any{"userId": "U200001", "orderId": order, "amount": 200}); r.Body["result"] != "成功" {
		t.Fatalf("%s", r.Raw)
	}
	setClock(at(12, 0))
	card := newBiz("MC")
	oneTap(t, map[string]any{"bizNo": card, "type": "月卡购买", "userId": "U200001", "cardId": card, "amount": 3000, "channel": "微信支付"})
	setClock(at(13, 0))
	payOrder(t, "UNOSET", newBiz("OD"), 300, "微信支付")
	setClock(at(14, 0))
	internal(t, "deposit", "/internal/pay/deposit-transfers", newKey(), map[string]any{"bizNo": newBiz("DD"), "userId": "U200001", "orderId": newBiz("OD"), "amount": 500})
	setClock(at(15, 0))
	fid, _ := failRefund(t)
	do(t, "POST", "/api/pay/me/payments/"+fid+"/payee", userToken("U100484"), nil, map[string]any{"name": "薛小明", "bank": "示例银行", "account": "6214123412347908"})
	if y := do(t, "POST", "/api/pay/admin/refund-failures/"+fid+"/transfer", finance(), nil, map[string]any{"reason": "原路失败人工退回"}); y.Status != 200 {
		t.Fatalf("%s", y.Raw)
	}
	setClock(at(23, 59))
	payOrder(t, "UNEXT", newBiz("OD"), 450, "支付宝")

	// 02:10 前：当日未出
	setClock(time.Date(2031, 3, 2, 1, 0, 0, 0, bj))
	if err := tApp.GenerateDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	g := do(t, "GET", "/api/pay/admin/recon/2031-03-01", finance(), nil, nil)
	if g.Body["status"] != "当日未出" || g.Body["generatedAt"] != nil {
		t.Fatalf("02:10 前应为当日未出：%s", g.Raw)
	}

	setClock(time.Date(2031, 3, 2, 3, 0, 0, 0, bj))
	if err := tApp.GenerateDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := do(t, "GET", "/api/pay/admin/recon/RC20310301", finance(), nil, nil)
	b := r.Body
	want := map[string]float64{"orderPayments": 1850, "depositDeducted": 500, "refundsOriginal": 200, "refundsManual": 300,
		"orderNetReceived": 1850, "monthCardIncome": 3000, "expected": 4350, "diff": -450}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("%s：得到 %v，应为 %v", k, b[k], v)
		}
	}
	ca := b["channelActual"].(map[string]any)
	if ca["wechat"] != 3900.0 || ca["alipay"] != 0.0 || ca["total"] != 3900.0 {
		t.Errorf("渠道实到 %v", ca)
	}
	items := b["diffItems"].([]any)
	types := []string{}
	for _, it := range items {
		types = append(types, it.(map[string]any)["type"].(string))
	}
	if len(items) != 3 || types[0] != "渠道未到账" || types[1] != "人工转账退款不经收单渠道" || types[2] != "跨日到账" {
		t.Fatalf("差异项 %v", types)
	}
	if b["status"] != "差异待处理" || b["generatedAt"] == nil || b["reconId"] != "RC20310301" {
		t.Fatalf("%v %v", b["status"], b["generatedAt"])
	}
	if len(b["payments"].([]any)) < 7 {
		t.Fatalf("详情应含当日相关流水：%d", len(b["payments"].([]any)))
	}
	e := lastEvent(t, "pay.recon.diff_found", "2031-03-01")
	if p := e["payload"].(map[string]any); p["diff"] != -450.0 || p["itemCount"] != 3.0 || p["reconId"] != "RC20310301" {
		t.Fatalf("%v", p)
	}

	// 列表：新到旧，次日为当日未出且含跨日 +450
	l := do(t, "GET", "/api/pay/admin/recon?from=2031-03-01&to=2031-03-02", finance(), nil, nil)
	rows := l.Body["rows"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["date"] != "2031-03-02" || rows[0].(map[string]any)["status"] != "当日未出" {
		t.Fatalf("%s", l.Raw)
	}
	if d2 := rows[0].(map[string]any); d2["diff"] != 450.0 || d2["channelActual"].(map[string]any)["alipay"] != 450.0 {
		t.Fatalf("次日 %v", d2)
	}
	if tt := l.Body["totals"].(map[string]any); tt["diff"] != 0.0 || l.Body["formula"] == "" {
		t.Fatalf("合计 %v", tt)
	}
	if x := do(t, "GET", "/api/pay/admin/recon?status=差异待处理&from=2031-03-01&to=2031-03-02", finance(), nil, nil); len(x.Body["rows"].([]any)) != 1 {
		t.Fatalf("状态筛选 %s", x.Raw)
	}

	// 处理差异
	h := func(id string, idx string, note string) resp {
		return do(t, "POST", "/api/pay/admin/recon/"+id+"/diff-items/"+idx+"/handle", finance(), nil, map[string]any{"note": note})
	}
	if x := h("RC20310302", "0", "向渠道核实"); x.Status != 409 || x.Body["code"] != "CONFLICT" {
		t.Fatalf("当日未出不能处理：%d %s", x.Status, x.Raw)
	}
	if x := h("RC20310301", "9", "向渠道核实"); x.Status != 404 {
		t.Fatalf("%d", x.Status)
	}
	if x := h("RC20310301", "1", "向渠道核实"); x.Status != 409 || x.Body["code"] != "ALREADY_DONE" {
		t.Fatalf("自动核销的项：%d %s", x.Status, x.Raw)
	}
	if x := h("RC20310301", "0", "核实"); x.Status != 400 || x.Body["code"] != "REASON_REQUIRED" {
		t.Fatalf("%d", x.Status)
	}
	x := h("RC20310301", "0", "已向支付宝核实，次日补入账")
	if x.Status != 200 || x.Body["status"] != "差异已处理" {
		t.Fatalf("%d %s", x.Status, x.Raw)
	}
	it0 := x.Body["diffItems"].([]any)[0].(map[string]any)
	if it0["status"] != "已处理" || it0["handledBy"] != "AC0301" || it0["note"] != "已向支付宝核实，次日补入账" {
		t.Fatalf("%v", it0)
	}
	pid := it0["paymentId"].(string)
	a := lastEvent(t, "pay.audit.recorded", "payment:"+pid)
	if a["payload"].(map[string]any)["action"] != "处理对账差异" {
		t.Fatalf("%v", a)
	}
	if x := h("RC20310301", "0", "再处理一次"); x.Body["code"] != "ALREADY_DONE" {
		t.Fatalf("%s", x.Raw)
	}
	if x := do(t, "GET", "/api/pay/admin/recon/2020-01-01", finance(), nil, nil); x.Status != 404 {
		t.Fatalf("%d", x.Status)
	}
	// 出具后冻结：再跑不重出
	if err := tApp.GenerateDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	tApp.Pool.QueryRow(context.Background(), `SELECT count(*) FROM recon_reports WHERE date = '2031-03-01'`).Scan(&n)
	if n != 1 {
		t.Fatalf("对账单应只有一份：%d", n)
	}
}

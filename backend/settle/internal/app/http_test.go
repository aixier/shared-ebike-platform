package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestHTTPAuthScopeAndFlow(t *testing.T) {
	a, clk := newApp(t, "2026-11-01T00:30:00+08:00")
	seedScenario(t, a)
	h := a.Handler()

	if r := call(t, h, "GET", "/healthz", nil, nil, ""); r.code != 200 || !strings.Contains(string(r.body), `"ok":true`) {
		t.Fatalf("healthz: %d %s", r.code, r.body)
	}
	if r := call(t, h, "GET", "/api/settle/finance/settlements", nil, nil, ""); r.code != 401 || r.errCode(t) != "ADMIN_NOT_LOGGED_IN" {
		t.Errorf("未登录: %d %s", r.code, r.body)
	}
	if r := call(t, h, "GET", "/api/settle/finance/settlements", &cs, nil, ""); r.code != 403 || r.errCode(t) != "FORBIDDEN" {
		t.Errorf("无权限: %d %s", r.code, r.body)
	}

	// 手动出单（幂等：同键重复只执行一次）
	r := call(t, h, "POST", "/api/settle/finance/settlements/generate", &finance, map[string]string{"month": "2026-10"}, "gen-1")
	if r.code != 200 {
		t.Fatalf("出单: %d %s", r.code, r.body)
	}
	var gen []SettlementView
	r.json(t, &gen)
	r2 := call(t, h, "POST", "/api/settle/finance/settlements/generate", &finance, map[string]string{"month": "2026-10"}, "gen-1")
	if r2.code != 200 || !bytes.Equal(r.body, r2.body) {
		i := 0
		for i < len(r.body) && i < len(r2.body) && r.body[i] == r2.body[i] {
			i++
		}
		t.Errorf("同一幂等键应返回第一次结果: %d len %d/%d diff@%d %q", r2.code, len(r.body), len(r2.body), i, r2.body[max(0, i-20):min(len(r2.body), i+20)])
	}
	if r3 := call(t, h, "POST", "/api/settle/finance/settlements/generate", &finance, map[string]string{"month": "2026-10"}, "gen-2"); r3.code != 409 || r3.errCode(t) != "ALREADY_GENERATED" {
		t.Errorf("新键应 ALREADY_GENERATED: %d %s", r3.code, r3.body)
	}
	if r4 := call(t, h, "POST", "/api/settle/finance/settlements/generate", &finance, map[string]string{"month": "2026-11"}, ""); r4.code != 409 || r4.errCode(t) != "MONTH_NOT_ENDED" || !strings.Contains(string(r4.body), "2026-12-01 起可生成") {
		t.Errorf("MONTH_NOT_ENDED: %d %s", r4.code, r4.body)
	}

	// 列表与详情
	var page Page[SettlementView]
	r = call(t, h, "GET", "/api/settle/finance/settlements?month=2026-10&pageSize=1", &finance, nil, "")
	r.json(t, &page)
	if page.Total != 2 || len(page.Rows) != 1 || page.Rows[0].OperatorID != "OP01" || strings.Contains(string(r.body), `"details"`) {
		t.Errorf("列表: %s", r.body)
	}
	if r := call(t, h, "GET", "/api/settle/finance/settlements?month=2026-1", &finance, nil, ""); r.code != 400 {
		t.Errorf("月份格式应 400: %d", r.code)
	}
	r = call(t, h, "GET", "/api/settle/finance/settlements/ST-M-202610-OP01", &finance, nil, "")
	if r.code != 200 || !strings.Contains(string(r.body), `"ratio":0.58`) || !strings.Contains(string(r.body), `"details":[`) {
		t.Errorf("详情: %d %s", r.code, r.body)
	}
	if r := call(t, h, "GET", "/api/settle/finance/settlements/ST-X", &finance, nil, ""); r.code != 404 {
		t.Errorf("应 404: %d", r.code)
	}

	// 财务导出 CSV
	r = call(t, h, "GET", "/api/settle/finance/settlements/ST-M-202610-OP01/export", &finance, nil, "")
	if r.code != 200 || !bytes.HasPrefix(r.body, []byte{0xEF, 0xBB, 0xBF}) || r.header.Get("X-Total-Count") != "3" || !strings.Contains(string(r.body), "4.65") ||
		!strings.Contains(r.header.Get("Content-Disposition"), "ST-M-202610-OP01.csv") {
		t.Errorf("导出: %d %v %s", r.code, r.header, r.body)
	}

	// 运营商：只能看本家
	if r := call(t, h, "GET", "/api/settle/operator/settlements/ST-M-202610-OP02", &operator, nil, ""); r.code != 403 || r.errCode(t) != "SCOPE_DENIED" {
		t.Errorf("他家结算单: %d %s", r.code, r.body)
	}
	r = call(t, h, "GET", "/api/settle/operator/settlements", &operator, nil, "")
	page = Page[SettlementView]{}
	r.json(t, &page)
	if page.Total != 1 || page.Rows[0].SettlementID != "ST-M-202610-OP01" {
		t.Errorf("运营商列表: %s", r.body)
	}
	r = call(t, h, "GET", "/api/settle/operator/settlements/ST-M-202610-OP01/export", &operator, nil, "")
	if r.code != 200 || !strings.Contains(string(r.body), "出单时已退款") {
		t.Errorf("运营商导出: %d %s", r.code, r.body)
	}
	if r := call(t, h, "GET", "/api/settle/operator/settlements/current", &operator, nil, ""); r.code != 200 || !strings.Contains(string(r.body), `"status":"未出单"`) {
		t.Errorf("本期预览: %d %s", r.code, r.body)
	}
	if r := call(t, h, "GET", "/api/settle/operator/settlements", &finance, nil, ""); r.code != 403 {
		t.Errorf("财务无 settlement.view.own: %d", r.code)
	}

	// 运营商订单明细：不含用户字段、筛选与汇总
	r = call(t, h, "GET", "/api/settle/operator/orders?pageSize=2", &operator, nil, "")
	var op OperatorOrderPage
	r.json(t, &op)
	if r.code != 200 || op.Total != 5 || len(op.Rows) != 2 || op.Summary.Count != 5 || op.Summary.ReceivableSum != 750+125+1000+600+300 || op.Rows[0].OrderID != "OD2026110500005" {
		t.Errorf("订单明细: %s", r.body)
	}
	if strings.Contains(string(r.body), "userId") || strings.Contains(string(r.body), "U100004") {
		t.Errorf("不得含用户信息: %s", r.body)
	}
	r = call(t, h, "GET", "/api/settle/operator/orders?date=2026-10-31&q=QH000004", &operator, nil, "")
	op = OperatorOrderPage{}
	r.json(t, &op)
	if op.Total != 1 || op.Rows[0].OrderID != "OD2026103100004" || !op.Rows[0].LateReport {
		t.Errorf("按日与车号: %s", r.body)
	}
	r = call(t, h, "GET", "/api/settle/operator/orders?brandId=BR02&sortKey=receivable&sortDir=asc", &operator, nil, "")
	op = OperatorOrderPage{}
	r.json(t, &op)
	if op.Total != 2 || *op.Rows[0].Receivable != 125 {
		t.Errorf("品牌与排序: %s", r.body)
	}
	if r := call(t, h, "GET", "/api/settle/operator/orders?sortKey=userId", &operator, nil, ""); r.code != 400 {
		t.Errorf("非法排序应 400: %d", r.code)
	}
	if r := call(t, h, "GET", "/api/settle/operator/orders/OD2026101800006", &operator, nil, ""); r.code != 403 || r.errCode(t) != "SCOPE_DENIED" {
		t.Errorf("他家订单: %d %s", r.code, r.body)
	}
	r = call(t, h, "GET", "/api/settle/operator/orders/export?date=2026-10-02", &operator, nil, "")
	if r.code != 200 || r.header.Get("X-Total-Count") != "1" || !strings.Contains(r.header.Get("Content-Disposition"), "2026-10-02.csv") {
		t.Errorf("订单导出: %d %v", r.code, r.header)
	}

	// 按订单号查结算归属
	r = call(t, h, "GET", "/api/settle/order-settlements?orderIds=OD2026100200001,OD2026101800006,OD-NONE", &operator, nil, "")
	var oss []OrderSettlementView
	r.json(t, &oss)
	if len(oss) != 1 || oss[0].OrderID != "OD2026100200001" || oss[0].Settlement.SettlementID != "ST-M-202610-OP01" {
		t.Errorf("批量结算归属（运营商只见本家）: %s", r.body)
	}
	if r := call(t, h, "GET", "/api/settle/orders/OD-NONE/settlement", &finance, nil, ""); r.code != 404 {
		t.Errorf("未收到的订单应 404: %d", r.code)
	}

	// 异议：提交、只读账号、待办
	body := map[string]any{"settlementId": "ST-M-202610-OP01", "orderIds": []string{"OD2026101000002"}, "field": "时长", "claim": "车端记录骑行 8 分钟，平台少计"}
	if r := call(t, h, "POST", "/api/settle/operator/disputes", &exitedOp, body, ""); r.code != 403 || r.errCode(t) != "READONLY" {
		t.Errorf("只读账号: %d %s", r.code, r.body)
	}
	if r := call(t, h, "POST", "/api/settle/operator/disputes", &op2, body, ""); r.code != 403 || r.errCode(t) != "SCOPE_DENIED" {
		t.Errorf("他家结算单: %d %s", r.code, r.body)
	}
	price := map[string]any{"settlementId": "ST-M-202610-OP01", "orderIds": []string{"OD2026101000002"}, "field": "价格", "claim": "价格应当按旧价计算"}
	if r := call(t, h, "POST", "/api/settle/operator/disputes", &operator, price, ""); r.code != 409 || r.errCode(t) != "PRICE_NOT_DISPUTABLE" {
		t.Errorf("价格: %d %s", r.code, r.body)
	}
	r = call(t, h, "POST", "/api/settle/operator/disputes", &operator, body, "ds-1")
	var dv DisputeView
	r.json(t, &dv)
	if r.code != 200 || dv.DisputeID != "DS202611001" {
		t.Fatalf("提交异议: %d %s", r.code, r.body)
	}
	if r := call(t, h, "POST", "/api/settle/operator/disputes", &operator, body, "ds-1"); !strings.Contains(string(r.body), "DS202611001") {
		t.Errorf("幂等重放: %s", r.body)
	}
	r = call(t, h, "GET", "/api/settle/todos", &finance, nil, "")
	if !strings.Contains(string(r.body), `"key":"settlements","label":"待审核结算单","count":2`) || !strings.Contains(string(r.body), `"key":"disputes","label":"待核实异议","count":1`) {
		t.Errorf("财务待办: %s", r.body)
	}
	r = call(t, h, "GET", "/api/settle/todos", &operator, nil, "")
	if !strings.Contains(string(r.body), `"key":"myDisputes"`) || strings.Contains(string(r.body), `"key":"settlements"`) {
		t.Errorf("运营商待办: %s", r.body)
	}
	r = call(t, h, "GET", "/api/settle/finance/disputes/DS202611001", &finance, nil, "")
	if r.code != 200 || !strings.Contains(string(r.body), `"orders":[`) || strings.Contains(string(r.body), "U100004") {
		t.Errorf("异议详情: %d %s", r.code, r.body)
	}
	clk.Set("2026-11-02T10:00:00+08:00")
	r = call(t, h, "POST", "/api/settle/finance/disputes/DS202611001/decide", &finance, map[string]any{"decision": "采纳", "corrections": []map[string]any{{"orderId": "OD2026101000002", "minutes": 8}}, "reason": "车端轨迹支持运营商主张"}, "dec-1")
	if r.code != 200 || !strings.Contains(string(r.body), `"status":"已采纳"`) {
		t.Errorf("处理异议: %d %s", r.code, r.body)
	}
	r = call(t, h, "GET", "/api/settle/operator/disputes", &operator, nil, "")
	if !strings.Contains(string(r.body), `"total":1`) {
		t.Errorf("运营商异议列表: %s", r.body)
	}

	// 人工调整 → 审核 → 发送 → 到账
	if r := call(t, h, "POST", "/api/settle/finance/settlements/ST-M-202610-OP01/adjustments", &finance, map[string]any{"brandId": "BR01", "feeDelta": 120, "reason": "补计线下核对差额"}, "adj-1"); r.code != 200 || !strings.Contains(string(r.body), `"payableFee":1258`) {
		t.Errorf("人工调整: %d %s", r.code, r.body)
	}
	if r := call(t, h, "POST", "/api/settle/finance/settlements/ST-M-202610-OP01/adjustments", &finance, map[string]any{"brandId": "BR01", "feeDelta": 120, "reason": "补计线下核对差额"}, "adj-1"); !strings.Contains(string(r.body), `"payableFee":1258`) {
		t.Errorf("同键不重复录入: %s", r.body)
	}
	if r := call(t, h, "POST", "/api/settle/finance/settlements/ST-M-202610-OP01/adjustments", &finance, map[string]any{"brandId": "BR01", "feeDelta": 10, "reason": "短"}, ""); r.code != 400 || r.errCode(t) != "REASON_REQUIRED" {
		t.Errorf("事由: %d %s", r.code, r.body)
	}
	for _, step := range []struct{ path, want string }{{"review", "已审核"}, {"send", "已发送"}} {
		if r := call(t, h, "POST", "/api/settle/finance/settlements/ST-M-202610-OP01/"+step.path, &finance, map[string]any{}, ""); r.code != 200 || !strings.Contains(string(r.body), step.want) {
			t.Errorf("%s: %d %s", step.path, r.code, r.body)
		}
	}
	if r := call(t, h, "POST", "/api/settle/finance/settlements/ST-M-202610-OP01/payment", &finance, map[string]any{"bankTradeNo": "x"}, ""); r.code != 400 {
		t.Errorf("流水号: %d %s", r.code, r.body)
	}
	if r := call(t, h, "POST", "/api/settle/finance/settlements/ST-M-202610-OP01/payment", &finance, map[string]any{"bankTradeNo": "HX20261105000001", "paidAt": "2026-11-05T16:00:00+08:00"}, ""); r.code != 200 || !strings.Contains(string(r.body), "已到账") {
		t.Errorf("到账: %d %s", r.code, r.body)
	}
	if r := call(t, h, "POST", "/api/settle/finance/settlements/ST-M-202610-OP01/payment", &finance, map[string]any{"bankTradeNo": "HX20261105000001"}, ""); r.code != 409 || r.errCode(t) != "SETTLEMENT_STATE" {
		t.Errorf("重复到账: %d %s", r.code, r.body)
	}
}

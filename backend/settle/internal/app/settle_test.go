package app

import (
	"context"
	"strings"
	"testing"

	"bike/settle/internal/calc"
)

func seedScenario(t *testing.T, a *App) {
	t.Helper()
	feedBrands(t, a)
	feedVersions(t, a)
	// OP01
	feed(t, a, "ride.order.ended", ended("OD2026100200001", "BR01", "OP01", "PR0001", "2026-10-02T08:00:00+08:00", "2026-10-02T08:18:00+08:00", "2026-10-02T08:18:01+08:00", 18))
	feed(t, a, "bill.bill.issued", issued("OD2026100200001", "BR01", "OP01", "PR0001", 18, 750, 300))
	feed(t, a, "ride.order.ended", ended("OD2026101000002", "BR02", "OP01", "PR0001", "2026-10-10T08:00:00+08:00", "2026-10-10T08:05:00+08:00", "2026-10-10T08:05:01+08:00", 5))
	feed(t, a, "bill.bill.issued", issued("OD2026101000002", "BR02", "OP01", "PR0001", 5, 125, 0))
	// 账单先于订单结束事件到达（跨流不保序）
	feed(t, a, "bill.bill.issued", issued("OD2026102000003", "BR02", "OP01", "PR0002", 20, 1000, 0))
	feed(t, a, "ride.order.ended", ended("OD2026102000003", "BR02", "OP01", "PR0002", "2026-10-20T08:00:00+08:00", "2026-10-20T08:20:00+08:00", "2026-10-20T08:20:01+08:00", 20))
	// 10-31 结束、11-01 09:00 才收到补报
	feed(t, a, "ride.order.ended", ended("OD2026103100004", "BR01", "OP01", "PR0002", "2026-10-31T23:00:00+08:00", "2026-10-31T23:30:00+08:00", "2026-11-01T09:00:00+08:00", 30))
	feed(t, a, "bill.bill.issued", issued("OD2026103100004", "BR01", "OP01", "PR0002", 30, 600, 0))
	feed(t, a, "ride.order.ended", ended("OD2026110500005", "BR01", "OP01", "PR0002", "2026-11-05T08:00:00+08:00", "2026-11-05T08:10:00+08:00", "2026-11-05T08:10:01+08:00", 10))
	feed(t, a, "bill.bill.issued", issued("OD2026110500005", "BR01", "OP01", "PR0002", 10, 300, 0))
	// OP02
	feed(t, a, "ride.order.ended", ended("OD2026101800006", "BR03", "OP02", "PR0002", "2026-10-18T20:00:00+08:00", "2026-10-18T20:13:00+08:00", "2026-10-18T20:13:01+08:00", 13))
	feed(t, a, "bill.bill.issued", issued("OD2026101800006", "BR03", "OP02", "PR0002", 13, 650, 0))
	feed(t, a, "bill.card.purchased", map[string]any{"cardId": "MC0001", "userId": "U100004", "price": 2500, "paidAt": "2026-10-01T09:00:00+08:00", "startAt": "2026-10-01T09:00:00+08:00", "endAt": "2026-10-31T09:00:00+08:00"})
}

func TestSettlementLifecycle(t *testing.T) {
	ctx := context.Background()
	a, clk := newApp(t, "2026-10-31T12:00:00+08:00")
	seedScenario(t, a)
	fin := Actor{Kind: "人", ID: "AC0201", Name: "吕＊＊"}

	// 结算月未结束不能出单
	if _, err := a.Generate(ctx, "2026-10", "财务手动", fin); err == nil || err.(*ApiError).Code != "MONTH_NOT_ENDED" {
		t.Fatalf("应 MONTH_NOT_ENDED: %v", err)
	}
	if _, err := a.Generate(ctx, "2026-13", "财务手动", fin); err == nil || err.(*ApiError).Code != "INVALID" {
		t.Fatalf("应 INVALID: %v", err)
	}

	clk.Set("2026-11-01T00:30:00+08:00")
	got, err := a.Generate(ctx, "2026-10", "财务手动", fin)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SettlementID != "ST-M-202610-OP01" || got[1].SettlementID != "ST-M-202610-OP02" {
		t.Fatalf("应出 2 张: %+v", got)
	}
	s1 := got[0]
	if s1.Status != "待审核" || s1.DueDate != "2026-11-06" || s1.Totals.OrderCount != 3 || s1.Totals.DetailFee != 465+73+600 || s1.Totals.PayableFee != 1138 || s1.DetailCount != 3 || s1.Details != nil {
		t.Errorf("OP01 结算单: %+v", s1)
	}
	if s1.OperatorName != "青禾出行" || len(s1.Brands) != 2 || s1.Brands[1].BrandName != "木棉" || len(s1.Brands[1].RatioBreakdown) != 2 {
		t.Errorf("品牌汇总: %+v", s1.Brands)
	}
	if s1.MonthCard.PlatformIncome != 2500 || s1.MonthCard.PlatformCardOrders != 1 || s1.Brands[0].MonthCardShare != 2500 || s1.Brands[0].MonthCardOrderCount != 1 {
		t.Errorf("月卡分摊: %+v %+v", s1.MonthCard, s1.Brands[0])
	}
	if _, err := a.Generate(ctx, "2026-10", "自动出单", SystemActor); err == nil || err.(*ApiError).Code != "ALREADY_GENERATED" {
		t.Fatalf("应 ALREADY_GENERATED: %v", err)
	}
	if n := outboxTypes(t)["settle.settlement.generated"]; n != 2 {
		t.Errorf("generated 事件 %d 条", n)
	}

	// 出单后：A 部分退款；明细冻结不变
	clk.Set("2026-11-03T10:00:00+08:00")
	feed(t, a, "bill.bill.refunded", map[string]any{"orderId": "OD2026100200001", "userId": "U100004", "brandId": "BR01", "operatorId": "OP01", "amount": 250,
		"refundedTotal": 250, "netReceived": 200, "receivable": 750, "kind": "部分退款", "refundedAt": "2026-11-03T10:00:00+08:00", "ticketId": nil})
	d1, err := a.GetSettlement(ctx, "", "ST-M-202610-OP01", true)
	if err != nil {
		t.Fatal(err)
	}
	if (*d1.Details)[0].Base != 750 || (*d1.Details)[0].Fee != 465 {
		t.Errorf("明细应冻结: %+v", (*d1.Details)[0])
	}

	// 本期预览：11 月明细 + 待进单调整项（退款、补报）
	clk.Set("2026-11-10T12:00:00+08:00")
	pv, err := a.Preview(ctx, "OP01")
	if err != nil {
		t.Fatal(err)
	}
	if pv.Status != "未出单" || pv.Month != "2026-11" || pv.DetailCount != 1 || len(pv.Adjustments) != 2 || pv.PreviewNote == nil {
		t.Fatalf("预览: %+v", pv)
	}
	if pv.Adjustments[0].Type != calc.TypeRefund || pv.Adjustments[0].FeeDelta != 310-465 || pv.Adjustments[1].Type != calc.TypeLate || pv.Adjustments[1].FeeDelta != 372 {
		t.Errorf("预览调整项: %+v", pv.Adjustments)
	}
	if _, err := a.Preview(ctx, "OP99"); err == nil || err.(*ApiError).Code != "NOT_FOUND" {
		t.Errorf("未知运营商应 NOT_FOUND: %v", err)
	}
	// 预览不落库
	var n int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM adjustments`).Scan(&n)
	if n != 0 {
		t.Errorf("预览不应写调整项")
	}

	// 异议：OP02 对 H 提时长异议；价格不受理；他家不能提
	opActor := Actor{Kind: "人", ID: "AC0102"}
	if _, err := a.SubmitDispute(ctx, "OP02", "ST-M-202610-OP02", []string{"OD2026101800006"}, "价格", "价格太低了吧啊啊", opActor); err == nil || err.(*ApiError).Code != "PRICE_NOT_DISPUTABLE" {
		t.Errorf("价格应不受理: %v", err)
	}
	if _, err := a.SubmitDispute(ctx, "OP01", "ST-M-202610-OP02", []string{"OD2026101800006"}, "时长", "车端时钟偏差实际 19 分钟", opActor); err == nil || err.(*ApiError).Code != "SCOPE_DENIED" {
		t.Errorf("他家结算单应 SCOPE_DENIED: %v", err)
	}
	if _, err := a.SubmitDispute(ctx, "OP02", "ST-M-202610-OP02", []string{"OD2026100200001"}, "时长", "车端时钟偏差实际 19 分钟", opActor); err == nil || err.(*ApiError).Code != "INVALID" {
		t.Errorf("不在结算单的订单应 INVALID: %v", err)
	}
	if _, err := a.SubmitDispute(ctx, "OP02", "ST-M-202610-OP02", []string{"OD2026101800006"}, "时长", "太短", opActor); err == nil || err.(*ApiError).Code != "INVALID" {
		t.Errorf("理由过短应 INVALID: %v", err)
	}
	ds, err := a.SubmitDispute(ctx, "OP02", "ST-M-202610-OP02", []string{"OD2026101800006"}, "时长", "车端时钟偏差导致锁车时刻上报偏早，实际骑行 19 分钟", opActor)
	if err != nil {
		t.Fatal(err)
	}
	if ds.DisputeID != "DS202611001" || ds.Status != "待核实" || ds.PlatformFact != "OD2026101800006 平台记录 13 分钟、2.6 公里" || ds.AgentComparison != "" || ds.AgentSuggestion != nil {
		t.Errorf("异议: %+v", ds)
	}
	// 里程异议不能采纳
	dsKm, _ := a.SubmitDispute(ctx, "OP02", "ST-M-202610-OP02", []string{"OD2026101800006"}, "里程", "我方车端里程为 3.2 公里", opActor)
	if _, err := a.DecideDispute(ctx, "", dsKm.DisputeID, "采纳", []CorrectionInput{{OrderID: "OD2026101800006", Minutes: 19}}, "里程有差异", fin); err == nil || err.(*ApiError).Code != "INVALID" {
		t.Errorf("里程采纳应 INVALID: %v", err)
	}
	if _, err := a.DecideDispute(ctx, "", dsKm.DisputeID, "无需调整", nil, "短", fin); err == nil || err.(*ApiError).Code != "REASON_REQUIRED" {
		t.Errorf("事由过短应 REASON_REQUIRED: %v", err)
	}
	if _, err := a.DecideDispute(ctx, "", dsKm.DisputeID, "无需调整", nil, "里程不参与计价", fin); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DecideDispute(ctx, "", ds.DisputeID, "采纳", nil, "轨迹与运营商一致", fin); err == nil || err.(*ApiError).Code != "INVALID" {
		t.Errorf("采纳缺分钟数应 INVALID: %v", err)
	}
	clk.Set("2026-11-10T14:02:00+08:00")
	dd, err := a.DecideDispute(ctx, "", ds.DisputeID, "采纳", []CorrectionInput{{OrderID: "OD2026101800006", Minutes: 19}}, "轨迹与运营商主张一致", fin)
	if err != nil {
		t.Fatal(err)
	}
	if dd.Status != "已采纳" || len(dd.Corrections) != 1 || dd.Corrections[0].BaseDelta != 300 || dd.AdjustmentID != nil {
		t.Errorf("采纳结果: %+v", dd)
	}
	if _, err := a.DecideDispute(ctx, "", ds.DisputeID, "驳回", nil, "再处理一次", fin); err == nil || err.(*ApiError).Code != "ALREADY_DONE" {
		t.Errorf("重复处理应 ALREADY_DONE: %v", err)
	}

	// 10 月 OP01：审核、发送、登记到账
	if _, err := a.Send(ctx, "", "ST-M-202610-OP01", fin); err == nil || err.(*ApiError).Code != "SETTLEMENT_STATE" {
		t.Errorf("未审核不能发送: %v", err)
	}
	if v, err := a.Review(ctx, "", "ST-M-202610-OP01", "", fin); err != nil || v.Status != "已审核" || v.ReviewedBy == nil {
		t.Fatalf("审核: %+v %v", v, err)
	}
	if _, err := a.Review(ctx, "", "ST-M-202610-OP01", "", fin); err == nil || err.(*ApiError).Code != "SETTLEMENT_STATE" {
		t.Errorf("重复审核应 SETTLEMENT_STATE: %v", err)
	}
	if _, err := a.AddAdjustment(ctx, "", "ST-M-202610-OP01", "BR01", 100, nil, "补发奖励", fin); err == nil || err.(*ApiError).Code != "SETTLEMENT_STATE" {
		t.Errorf("已审核不能录入调整项: %v", err)
	}
	if _, err := a.Send(ctx, "", "ST-M-202610-OP01", fin); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RegisterPayment(ctx, "", "ST-M-202610-OP01", "hx-1", nil, fin); err == nil || err.(*ApiError).Code != "INVALID" {
		t.Errorf("流水号格式应 INVALID: %v", err)
	}
	clk.Set("2026-11-07T10:00:00+08:00")
	pay, err := a.RegisterPayment(ctx, "", "ST-M-202610-OP01", "HX2026110500001", nil, fin)
	if err != nil || pay.Status != "已到账" || pay.BankTradeNo == nil || pay.InvoiceStatus == nil || pay.Overdue {
		t.Fatalf("到账: %+v %v", pay, err)
	}
	// OP02 未到账且已过 6 日 → overdue
	o2, _ := a.GetSettlement(ctx, "", "ST-M-202610-OP02", false)
	if !o2.Overdue {
		t.Errorf("OP02 应逾期")
	}

	// 11 月出单：退款、补报、异议核实进调整项
	clk.Set("2026-12-01T00:30:00+08:00")
	if _, err := a.AutoSettle(ctx); err != nil {
		t.Fatal(err)
	}
	s11, err := a.GetSettlement(ctx, "", "ST-M-202611-OP01", true)
	if err != nil {
		t.Fatal(err)
	}
	if s11.DetailCount != 1 || len(s11.Adjustments) != 2 {
		t.Fatalf("11 月 OP01: %+v", s11)
	}
	refund, late := s11.Adjustments[0], s11.Adjustments[1]
	if refund.AdjustmentID != "ADJ-202611-OP01-01" || refund.Type != "出单后退款" || refund.Items[0].PreviousBase != 750 || refund.Items[0].NewBase != 500 || refund.FeeDelta != -155 || refund.ConfirmedBy != nil || refund.CreatedBy.Kind != "系统" {
		t.Errorf("退款调整项: %+v", refund)
	}
	if late.Type != "晚到补报" || late.Items[0].NewBase != 600 || late.FeeDelta != 372 || !strings.Contains(late.Reason, "10 月 31 日") {
		t.Errorf("补报调整项: %+v", late)
	}
	if s11.Totals.PayableFee != 186+(-155)+372 {
		t.Errorf("11 月合计: %+v", s11.Totals)
	}
	s11b, _ := a.GetSettlement(ctx, "", "ST-M-202611-OP02", false)
	if len(s11b.Adjustments) != 1 || s11b.Adjustments[0].Type != "异议核实" || s11b.Adjustments[0].FeeDelta != 180 || deref(s11b.Adjustments[0].DisputeID) != ds.DisputeID {
		t.Errorf("异议核实调整项: %+v", s11b.Adjustments)
	}
	dv, _ := a.GetDispute(ctx, "", ds.DisputeID)
	if deref(dv.AdjustmentID) != "ADJ-202611-OP02-01" || dv.Orders == nil || len(*dv.Orders) != 1 {
		t.Errorf("异议应指向调整项: %+v", dv)
	}

	// 未确认调整项不能审核；确认后可审核；人工调整录入即确认
	if _, err := a.Review(ctx, "", "ST-M-202611-OP01", "", fin); err == nil || err.(*ApiError).Code != "ADJUSTMENT_UNCONFIRMED" {
		t.Errorf("应 ADJUSTMENT_UNCONFIRMED: %v", err)
	}
	for _, adj := range s11.Adjustments {
		if _, err := a.ConfirmAdjustment(ctx, "", "ST-M-202611-OP01", adj.AdjustmentID, "", fin); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.ConfirmAdjustment(ctx, "", "ST-M-202611-OP01", refund.AdjustmentID, "", fin); err == nil || err.(*ApiError).Code != "ALREADY_DONE" {
		t.Errorf("重复确认应 ALREADY_DONE: %v", err)
	}
	if _, err := a.ConfirmAdjustment(ctx, "", "ST-M-202611-OP01", "ADJ-X", "", fin); err == nil || err.(*ApiError).Code != "NOT_FOUND" {
		t.Errorf("应 NOT_FOUND: %v", err)
	}
	if _, err := a.AddAdjustment(ctx, "", "ST-M-202611-OP01", "BR03", 100, nil, "品牌不对哦", fin); err == nil || err.(*ApiError).Code != "INVALID" {
		t.Errorf("他家品牌应 INVALID: %v", err)
	}
	if _, err := a.AddAdjustment(ctx, "", "ST-M-202611-OP01", "BR01", 0, nil, "零元调整", fin); err == nil || err.(*ApiError).Code != "INVALID" {
		t.Errorf("0 应 INVALID: %v", err)
	}
	if _, err := a.AddAdjustment(ctx, "", "ST-M-202611-OP01", "BR01", 100, nil, "短", fin); err == nil || err.(*ApiError).Code != "REASON_REQUIRED" {
		t.Errorf("应 REASON_REQUIRED: %v", err)
	}
	man, err := a.AddAdjustment(ctx, "", "ST-M-202611-OP01", "BR01", -50, []string{"OD2026110500005"}, "线下核对少计一单调度", fin)
	if err != nil {
		t.Fatal(err)
	}
	last := man.Adjustments[len(man.Adjustments)-1]
	if last.AdjustmentID != "ADJ-202611-OP01-03" || last.Type != "人工调整" || last.ConfirmedBy == nil || man.Totals.PayableFee != 186-155+372-50 {
		t.Errorf("人工调整: %+v %+v", last, man.Totals)
	}
	if v, err := a.Review(ctx, "", "ST-M-202611-OP01", "核对无误", fin); err != nil || v.Status != "已审核" {
		t.Fatalf("审核: %v", err)
	}

	// 结算归属指针与说明
	osD, _ := a.OrderSettlement(ctx, "", "OD2026103100004")
	if osD.Settlement == nil || osD.Settlement.Kind != "调整项" || osD.Settlement.SettlementID != "ST-M-202611-OP01" || deref(osD.SettleMonth) != "2026-10" {
		t.Errorf("补报单指针: %+v", osD)
	}
	osA, _ := a.OrderSettlement(ctx, "", "OD2026100200001")
	if osA.Settlement.Kind != "明细" || osA.Settlement.SettlementID != "ST-M-202610-OP01" || !strings.Contains(deref(osA.SettlementNote), "ADJ-202611-OP01-01") {
		t.Errorf("退款单指针不变、写差额去向: %+v", osA)
	}
	osH, _ := a.OrderSettlement(ctx, "", "OD2026101800006")
	if len(osH.DisputeIDs) != 2 {
		t.Errorf("关联异议: %+v", osH)
	}
	if _, err := a.OrderSettlement(ctx, "OP01", "OD2026101800006"); err == nil || err.(*ApiError).Code != "SCOPE_DENIED" {
		t.Errorf("他家订单应 SCOPE_DENIED: %v", err)
	}

	// 不变量：每单所有结算记录的服务费累加 = 按最终基数复算
	rows, _ := testPool.Query(ctx, `SELECT order_id, sum(fee) FROM (
		SELECT order_id, fee FROM settlement_lines UNION ALL SELECT order_id, fee_delta FROM adjustment_items) x GROUP BY order_id`)
	sums := map[string]int64{}
	for rows.Next() {
		var id string
		var s int64
		_ = rows.Scan(&id, &s)
		sums[id] = s
	}
	rows.Close()
	want := map[string]int64{"OD2026100200001": 310, "OD2026101000002": 73, "OD2026102000003": 600, "OD2026103100004": 372, "OD2026110500005": 186, "OD2026101800006": 570}
	for id, w := range want {
		if sums[id] != w {
			t.Errorf("%s 累计服务费 %d，期望 %d", id, sums[id], w)
		}
	}
	// 每单恰好进一次明细
	var dup int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM (SELECT order_id FROM settlement_lines GROUP BY order_id HAVING count(*) > 1) x`).Scan(&dup)
	if dup != 0 {
		t.Errorf("有订单重复进明细")
	}
	types := outboxTypes(t)
	if types["settle.settlement.paid"] != 1 || types["settle.dispute.decided"] != 2 || types["settle.settlement.adjusted"] < 6 || types["settle.audit.recorded"] < 10 {
		t.Errorf("事件: %v", types)
	}
}

func TestOperatorExitedNote(t *testing.T) {
	ctx := context.Background()
	a, clk := newApp(t, "2026-11-20T00:00:00+08:00")
	seedScenario(t, a)
	feed(t, a, "fleet.operator.exited", map[string]any{"operatorId": "OP02", "brandIds": []string{"BR03"}, "exitedAt": "2026-11-20T00:00:00+08:00", "reason": "合同到期退出"})
	clk.Set("2026-12-01T01:00:00+08:00")
	got, err := a.AutoSettle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "2026-11" {
		t.Fatalf("从未出过单时只出上个月: %v", got)
	}
	s, err := a.GetSettlement(ctx, "", "ST-M-202611-OP02", false)
	if err != nil {
		t.Fatal(err)
	}
	if deref(s.Note) != "运营商已退出，本期为最后一期结算" {
		t.Errorf("note = %v", s.Note)
	}
	// 再跑一次不重复出单
	got2, _ := a.AutoSettle(ctx)
	if len(got2) != 0 {
		t.Errorf("不应重复: %v", got2)
	}
}

func TestAutoSettleCatchUpAndTiming(t *testing.T) {
	ctx := context.Background()
	a, clk := newApp(t, "2026-11-01T00:20:00+08:00")
	seedScenario(t, a)
	// 00:30 前不出上个月
	got, err := a.AutoSettle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var cnt int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM settle_months WHERE month = '2026-10'`).Scan(&cnt)
	if cnt != 0 {
		t.Errorf("00:30 前不应出 10 月: %v", got)
	}
	// 从未出过单时只补上一个已到时刻的月（9 月，空月也记为已出单）
	if len(got) != 1 || got[0] != "2026-09" {
		t.Fatalf("00:20 时应只出 9 月: %v", got)
	}
	// 跳过一个月后补出
	clk.Set("2026-12-01T00:31:00+08:00")
	got, err = a.AutoSettle(ctx)
	if err != nil || len(got) != 2 || got[0] != "2026-10" || got[1] != "2026-11" {
		t.Fatalf("补出: %v %v", got, err)
	}
}

func TestProjectionDedupAndNoUserID(t *testing.T) {
	ctx := context.Background()
	a, _ := newApp(t, "2026-11-01T00:00:00+08:00")
	env := Envelope{EventID: "dup-1", Type: "bill.card.purchased", Version: 1, Payload: []byte(`{"cardId":"MC1","userId":"U100004","price":2500,"paidAt":"2026-10-01T09:00:00+08:00"}`)}
	for i := 0; i < 3; i++ {
		if err := a.HandleEvent(ctx, env); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM card_purchases`).Scan(&n)
	if n != 1 {
		t.Errorf("重复送达应只处理一次: %d", n)
	}
	// 任何表都没有用户编号列
	var cols int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND column_name ILIKE '%user%'`).Scan(&cols)
	if cols != 0 {
		t.Errorf("不应有用户字段列: %d", cols)
	}
	// 无法解析的消息
	if err := a.HandleMessage(ctx, []byte("not json")); err == nil {
		t.Error("应报解析失败")
	}
	// 版本不可改：重复发布同号版本不覆盖
	feed(t, a, "rule.version.published", map[string]any{"versionId": "PR0001", "effectiveAt": "2026-07-01T00:00:00+08:00", "ride": map[string]any{"startFee": 150, "startMinutes": 3, "perMinute": 40}, "dispatchFee": 300, "monthCard": map[string]any{"allocation": "按骑行单数"}, "serviceFee": map[string]any{"basis": "订单应收", "ratios": map[string]any{"BR01": 0.62}}})
	feed(t, a, "rule.version.published", map[string]any{"versionId": "PR0001", "effectiveAt": "2026-07-01T00:00:00+08:00", "ride": map[string]any{"startFee": 999, "startMinutes": 3, "perMinute": 40}, "dispatchFee": 300, "monthCard": map[string]any{"allocation": "按骑行单数"}, "serviceFee": map[string]any{"basis": "订单应收", "ratios": map[string]any{"BR01": 0.9}}})
	vs, _ := loadVersions(ctx, testPool)
	if vs["PR0001"].StartFee != 150 || vs["PR0001"].Ratios["BR01"] != 620000 {
		t.Errorf("版本被覆盖: %+v", vs["PR0001"])
	}
	feed(t, a, "rule.version.revoked", map[string]any{"versionId": "PR0001", "revokedAt": "2026-06-30T00:00:00+08:00"})
	vs, _ = loadVersions(ctx, testPool)
	if _, ok := vs["PR0001"]; ok {
		t.Error("已撤销版本不参与取值")
	}
}

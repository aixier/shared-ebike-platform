package app

import (
	"context"
	"testing"
)

// 对账智能体的工具：读结算明细、读订单事实、写调整项草稿（金额由代码算、待财务确认）、写异议比对结论。
func TestReconTools(t *testing.T) {
	ctx := context.Background()
	a, clk := newApp(t, "2026-11-01T00:30:00+08:00")
	seedScenario(t, a)
	if _, err := a.Generate(ctx, "2026-10", "自动出单", SystemActor); err != nil {
		t.Fatal(err)
	}
	tools := ReconTools{App: a}
	agent := Actor{Kind: "智能体", ID: "AC0902", Name: "对账智能体"}

	s, err := tools.ReadSettlement(ctx, "ST-M-202610-OP01")
	if err != nil || s.Details == nil || len(*s.Details) != 3 {
		t.Fatalf("读结算明细: %v", err)
	}
	facts, err := tools.ReadOrderFacts(ctx, []string{"OD2026100200001", "OD2026103100004"})
	if err != nil || len(facts) != 2 || facts[1].ReportReceivedAt == nil {
		t.Fatalf("读订单事实: %+v %v", facts, err)
	}

	// 出单后到达的补报与退款：智能体起草，代码复算金额
	clk.Set("2026-11-03T10:00:00+08:00")
	feed(t, a, "bill.bill.refunded", map[string]any{"orderId": "OD2026100200001", "brandId": "BR01", "operatorId": "OP01", "amount": 250,
		"refundedTotal": 250, "netReceived": 200, "receivable": 750, "kind": "部分退款", "refundedAt": "2026-11-03T10:00:00+08:00"})
	if _, err := tools.WriteAdjustmentDraft(ctx, AdjustmentDraftInput{SettlementID: "ST-M-202610-OP01", OrderIDs: []string{"OD2026101000002"}, Reason: "核对无差额的单", Agent: agent}); err == nil {
		t.Error("没有差额应报错")
	}
	if _, err := tools.WriteAdjustmentDraft(ctx, AdjustmentDraftInput{SettlementID: "ST-M-202610-OP01", OrderIDs: []string{"OD2026100200001"}, Reason: "人写的", Agent: Actor{Kind: "人", ID: "AC0201"}}); err == nil {
		t.Error("生成人须为智能体")
	}
	ids, err := tools.WriteAdjustmentDraft(ctx, AdjustmentDraftInput{SettlementID: "ST-M-202610-OP01", OrderIDs: []string{"OD2026100200001", "OD2026103100004"},
		Reason: "出单后到达的退款与补报，按事实更正", Agent: agent})
	if err != nil || len(ids) != 2 {
		t.Fatalf("写草稿: %v %v", ids, err)
	}
	v, _ := a.GetSettlement(ctx, "", "ST-M-202610-OP01", false)
	if len(v.Adjustments) != 2 || v.Adjustments[0].FeeDelta != 310-465 || v.Adjustments[1].FeeDelta != 372 || v.Adjustments[0].ConfirmedBy != nil || v.Adjustments[0].CreatedBy.Kind != "智能体" {
		t.Fatalf("草稿: %+v", v.Adjustments)
	}
	if _, err := a.Review(ctx, "", "ST-M-202610-OP01", "", Actor{Kind: "人", ID: "AC0201"}); err == nil || err.(*ApiError).Code != "ADJUSTMENT_UNCONFIRMED" {
		t.Errorf("草稿未确认不能审核: %v", err)
	}
	// 草稿已改了这两单的结算状态：下一期不会再重复调整
	clk.Set("2026-12-01T00:30:00+08:00")
	if _, err := a.Generate(ctx, "2026-11", "自动出单", SystemActor); err != nil {
		t.Fatal(err)
	}
	s11, _ := a.GetSettlement(ctx, "", "ST-M-202611-OP01", false)
	if len(s11.Adjustments) != 0 {
		t.Errorf("已起草的差额不应重复进下一期: %+v", s11.Adjustments)
	}

	// 异议比对结论
	ds, err := a.SubmitDispute(ctx, "OP01", "ST-M-202610-OP01", []string{"OD2026101000002"}, "时长", "车端记录骑行 8 分钟，平台少计", Actor{Kind: "人", ID: "AC0101"})
	if err != nil {
		t.Fatal(err)
	}
	sug := "驳回：平台计时与锁车报告一致"
	if err := tools.WriteDisputeComparison(ctx, ds.DisputeID, "锁车报告时刻与平台结束时刻一致", &sug, agent); err != nil {
		t.Fatal(err)
	}
	d, _ := a.GetDispute(ctx, "", ds.DisputeID)
	if d.AgentComparison == "" || deref(d.AgentSuggestion) != sug || d.Status != "待核实" {
		t.Errorf("比对结论: %+v", d)
	}
	if err := tools.WriteDisputeComparison(ctx, "DS-NONE", "x", nil, agent); err == nil {
		t.Error("不存在的异议应报错")
	}
}

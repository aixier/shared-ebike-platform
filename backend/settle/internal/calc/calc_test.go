package calc

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustRatio(t *testing.T, s string) Ratio {
	t.Helper()
	r, err := ParseRatio(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParseRatio(t *testing.T) {
	cases := map[string]Ratio{"0.62": 620000, "0.6": 600000, "1": 1000000, "0": 0, "0.555555": 555555, "0.58": 580000}
	for s, want := range cases {
		got, err := ParseRatio(s)
		if err != nil || got != want {
			t.Errorf("ParseRatio(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, bad := range []string{"", "-0.1", "1.2", "abc", "0.1234567", "0.62.1"} {
		if _, err := ParseRatio(bad); err == nil {
			t.Errorf("ParseRatio(%q) 应报错", bad)
		}
	}
	if s := Ratio(620000).String(); s != "0.62" {
		t.Errorf("String = %q", s)
	}
	if s := Ratio(1000000).String(); s != "1" {
		t.Errorf("String = %q", s)
	}
	if s := Ratio(0).String(); s != "0" {
		t.Errorf("String = %q", s)
	}
}

func TestFeeRoundHalfUp(t *testing.T) {
	cases := []struct {
		base  int64
		ratio string
		want  int64
	}{
		{750, "0.62", 465},   // 4.65
		{1, "0.5", 1},        // 0.5 分 → 1
		{3, "0.5", 2},        // 1.5 → 2
		{125, "0.58", 73},    // 72.5 → 73
		{124, "0.58", 72},    // 71.92 → 72
		{0, "0.62", 0},       // 0 元订单
		{-500, "0.62", -310}, // 负基数差额对称
		{-1, "0.5", -1},      // −0.5 → −1（远离零）
		{1400, "0.62", 868},
		{650, "0.6", 390},
	}
	for _, c := range cases {
		if got := Fee(c.base, mustRatio(t, c.ratio)); got != c.want {
			t.Errorf("Fee(%d,%s) = %d, want %d", c.base, c.ratio, got, c.want)
		}
	}
}

func TestMonthHelpers(t *testing.T) {
	if m := MonthOf(mustTime(t, "2026-10-31T23:59:59+08:00")); m != "2026-10" {
		t.Errorf("MonthOf = %s", m)
	}
	// UTC 时刻按东八区归月
	if m := MonthOf(mustTime(t, "2026-10-31T16:00:00Z")); m != "2026-11" {
		t.Errorf("MonthOf utc = %s", m)
	}
	if n := NextMonth("2026-12"); n != "2027-01" {
		t.Errorf("NextMonth = %s", n)
	}
	if n := PrevMonth("2027-01"); n != "2026-12" {
		t.Errorf("PrevMonth = %s", n)
	}
	if d := DueDate("2026-11"); d != "2026-12-06" {
		t.Errorf("DueDate = %s", d)
	}
	s, err := MonthStart("2026-12")
	if err != nil || !s.Equal(mustTime(t, "2026-12-01T00:00:00+08:00")) {
		t.Errorf("MonthStart = %v %v", s, err)
	}
	for _, bad := range []string{"2026-13", "2026-1", "202611", ""} {
		if ValidMonth(bad) {
			t.Errorf("ValidMonth(%q) 应为 false", bad)
		}
	}
	if !ValidMonth("2026-11") {
		t.Error("ValidMonth(2026-11)")
	}
}

func TestRideFee(t *testing.T) {
	v := Version{StartFee: 150, StartMinutes: 3, PerMinute: 50, DispatchFee: 500}
	if got := RideFee(v, 19); got != 950 {
		t.Errorf("RideFee 19 = %d", got)
	}
	if got := RideFee(v, 2); got != 150 {
		t.Errorf("RideFee 2 = %d", got)
	}
}

func TestBaseAt(t *testing.T) {
	o := Order{OrderID: "OD1", Receivable: 1400, Payable: 800, HasBill: true}
	t0 := mustTime(t, "2026-11-01T00:30:00+08:00")
	t1 := mustTime(t, "2026-11-05T10:00:00+08:00")
	t2 := mustTime(t, "2026-12-01T00:30:00+08:00")
	refunds := []Refund{{OrderID: "OD1", RefundedTotal: 500, NetReceived: 300, Kind: "部分退款", At: t1}}
	// 应收口径：出单时无退款
	if b := BaseAt(o, BasisReceivable, refunds, nil, t0); b != 1400 {
		t.Errorf("base t0 = %d", b)
	}
	// 部分退款按退款后金额
	if b := BaseAt(o, BasisReceivable, refunds, nil, t2); b != 900 {
		t.Errorf("base t2 = %d", b)
	}
	// 全额退款不计
	full := append(refunds, Refund{OrderID: "OD1", RefundedTotal: 800, NetReceived: 0, Kind: "全额退款", At: t1.Add(time.Hour)})
	if b := BaseAt(o, BasisReceivable, full, nil, t2); b != 0 {
		t.Errorf("full base = %d", b)
	}
	// 异议更正加到基数上，只计已处理时刻之前的
	corr := []Correction{{OrderID: "OD1", BaseDelta: 300, At: t1}}
	if b := BaseAt(o, BasisReceivable, nil, corr, t0); b != 1400 {
		t.Errorf("corr before = %d", b)
	}
	if b := BaseAt(o, BasisReceivable, nil, corr, t2); b != 1700 {
		t.Errorf("corr after = %d", b)
	}
	// 0 元订单
	if b := BaseAt(Order{HasBill: true}, BasisReceivable, nil, corr, t2); b != 0 {
		t.Errorf("zero = %d", b)
	}
	// 实收口径：未退款取应付，退款后取退款后实收
	if b := BaseAt(o, BasisNetReceived, nil, nil, t0); b != 800 {
		t.Errorf("net t0 = %d", b)
	}
	if b := BaseAt(o, BasisNetReceived, refunds, nil, t2); b != 300 {
		t.Errorf("net t2 = %d", b)
	}
	if r := RefundedAt(refunds, t2); r != 500 {
		t.Errorf("RefundedAt = %d", r)
	}
	if r := RefundedAt(refunds, t0); r != 0 {
		t.Errorf("RefundedAt t0 = %d", r)
	}
}

func TestAllocateLargestRemainder(t *testing.T) {
	got := Allocate(1000, map[string]int{"BR01": 1, "BR02": 1, "BR03": 1})
	if got["BR01"]+got["BR02"]+got["BR03"] != 1000 {
		t.Fatalf("合计不等: %v", got)
	}
	if got["BR01"] != 334 || got["BR02"] != 333 || got["BR03"] != 333 {
		t.Errorf("余数按品牌编号先后: %v", got)
	}
	z := Allocate(500, map[string]int{})
	if len(z) != 0 {
		t.Errorf("无单数 = %v", z)
	}
	z2 := Allocate(0, map[string]int{"BR01": 3})
	if z2["BR01"] != 0 {
		t.Errorf("无收入 = %v", z2)
	}
}

// 构造一个小场景：明细、晚到补报、出单后退款、异议核实、比例分组
func TestBuildScenario(t *testing.T) {
	v1 := Version{ID: "PR0001", Basis: BasisReceivable, Ratios: map[string]Ratio{"BR01": 620000, "BR02": 580000}}
	v2 := Version{ID: "PR0002", Basis: BasisReceivable, Ratios: map[string]Ratio{"BR01": 620000, "BR02": 600000}}
	vs := map[string]Version{"PR0001": v1, "PR0002": v2}
	gen10 := mustTime(t, "2026-11-01T00:30:00+08:00")
	orders := []Order{
		{OrderID: "A", BrandID: "BR01", PricingVersionID: "PR0001", RequestAt: mustTime(t, "2026-10-02T08:00:00+08:00"), EndAt: mustTime(t, "2026-10-02T08:20:00+08:00"), ReportReceivedAt: mustTime(t, "2026-10-02T08:20:01+08:00"), HasBill: true, Receivable: 750},
		{OrderID: "B", BrandID: "BR02", PricingVersionID: "PR0001", RequestAt: mustTime(t, "2026-10-10T08:00:00+08:00"), EndAt: mustTime(t, "2026-10-10T08:20:00+08:00"), ReportReceivedAt: mustTime(t, "2026-10-10T08:20:01+08:00"), HasBill: true, Receivable: 125, Degraded: true},
		{OrderID: "C", BrandID: "BR02", PricingVersionID: "PR0002", RequestAt: mustTime(t, "2026-10-20T08:00:00+08:00"), EndAt: mustTime(t, "2026-10-20T08:20:00+08:00"), ReportReceivedAt: mustTime(t, "2026-10-20T08:20:01+08:00"), HasBill: true, Receivable: 1000},
		// 10-31 结束、出单后才收到补报
		{OrderID: "D", BrandID: "BR01", PricingVersionID: "PR0002", RequestAt: mustTime(t, "2026-10-31T23:00:00+08:00"), EndAt: mustTime(t, "2026-10-31T23:30:00+08:00"), ReportReceivedAt: mustTime(t, "2026-11-01T09:00:00+08:00"), HasBill: true, Receivable: 600},
		// 0 元订单
		{OrderID: "E", BrandID: "BR01", PricingVersionID: "PR0001", RequestAt: mustTime(t, "2026-10-05T08:00:00+08:00"), EndAt: mustTime(t, "2026-10-05T08:01:00+08:00"), ReportReceivedAt: mustTime(t, "2026-10-05T08:01:01+08:00"), HasBill: true, Receivable: 0},
		// 还没收到账单的不进明细
		{OrderID: "F", BrandID: "BR01", PricingVersionID: "PR0001", RequestAt: mustTime(t, "2026-10-06T08:00:00+08:00"), EndAt: mustTime(t, "2026-10-06T08:10:00+08:00"), ReportReceivedAt: mustTime(t, "2026-10-06T08:10:01+08:00")},
		// 11 月的单不进 10 月
		{OrderID: "G", BrandID: "BR01", PricingVersionID: "PR0002", RequestAt: mustTime(t, "2026-11-01T00:10:00+08:00"), EndAt: mustTime(t, "2026-11-01T00:20:00+08:00"), ReportReceivedAt: mustTime(t, "2026-11-01T00:20:01+08:00"), HasBill: true, Receivable: 300},
	}
	for i := range orders {
		orders[i].OperatorID = "OP01"
	}
	in := Input{Month: "2026-10", At: gen10, Orders: orders, Versions: vs, Refunds: map[string][]Refund{}, Corrections: map[string][]Correction{}, Settled: map[string]Settled{}}
	lines, adjs := Build(in)
	if len(adjs) != 0 {
		t.Fatalf("首期不应有调整项: %+v", adjs)
	}
	ids := []string{}
	for _, l := range lines {
		ids = append(ids, l.OrderID)
	}
	if len(lines) != 4 || ids[0] != "A" || ids[1] != "E" || ids[2] != "B" || ids[3] != "C" {
		t.Fatalf("明细应为 A E B C（按结束时刻）: %v", ids)
	}
	if lines[0].Fee != 465 || lines[2].Fee != 73 || lines[3].Fee != 600 {
		t.Errorf("fee: %+v", lines)
	}
	if lines[2].Remark != "降级单" || lines[1].Remark != "0 元订单" || lines[0].Remark != "" {
		t.Errorf("remark: %q %q", lines[2].Remark, lines[1].Remark)
	}
	tot, brands := Summarize(lines, nil)
	if tot.OrderCount != 4 || tot.DetailFee != 465+73+600 || tot.PayableFee != tot.DetailFee {
		t.Errorf("totals: %+v", tot)
	}
	if len(brands) != 2 || brands[1].BrandID != "BR02" || len(brands[1].RatioBreakdown) != 2 {
		t.Fatalf("BR02 应按 0.58 与 0.6 分两组: %+v", brands)
	}
	if brands[1].RatioBreakdown[0].Ratio != 580000 || brands[1].RatioBreakdown[0].PricingVersionIDs[0] != "PR0001" {
		t.Errorf("分组升序: %+v", brands[1].RatioBreakdown)
	}

	settled := map[string]Settled{}
	Apply(settled, "ST-M-202610-OP01", lines, nil, gen10)

	// 11 月：A 部分退款，C 异议采纳 +3 分钟，D 晚到补报
	gen11 := mustTime(t, "2026-12-01T00:30:00+08:00")
	in2 := Input{Month: "2026-11", At: gen11, Orders: orders, Versions: vs,
		Refunds:     map[string][]Refund{"A": {{OrderID: "A", RefundedTotal: 250, Kind: "部分退款", At: mustTime(t, "2026-11-03T10:00:00+08:00")}}},
		Corrections: map[string][]Correction{"C": {{OrderID: "C", DisputeID: "DS202611001", BaseDelta: 150, At: mustTime(t, "2026-11-10T10:00:00+08:00")}}},
		Settled:     settled}
	lines2, adjs2 := Build(in2)
	if len(lines2) != 1 || lines2[0].OrderID != "G" {
		t.Fatalf("11 月明细只有 G: %+v", lines2)
	}
	if len(adjs2) != 3 {
		t.Fatalf("应有 3 组调整项: %+v", adjs2)
	}
	// 按首次出现的订单（开锁时刻）排序：A 退款(10-02)、C 异议(10-20)、D 补报(10-31)
	if adjs2[0].Type != TypeRefund || adjs2[0].Items[0].PreviousBase != 750 || adjs2[0].Items[0].NewBase != 500 || adjs2[0].FeeDelta != 310-465 {
		t.Errorf("退款调整: %+v", adjs2[0])
	}
	if adjs2[1].Type != TypeDispute || adjs2[1].DisputeID != "DS202611001" || adjs2[1].FeeDelta != 690-600 || adjs2[1].BaseDelta != 150 {
		t.Errorf("异议调整: %+v", adjs2[1])
	}
	if adjs2[2].Type != TypeLate || adjs2[2].Items[0].PreviousBase != 0 || adjs2[2].Items[0].NewBase != 600 || adjs2[2].FeeDelta != 372 {
		t.Errorf("补报调整: %+v", adjs2[2])
	}
	tot2, _ := Summarize(lines2, adjs2)
	if tot2.AdjustmentFee != (310-465)+(690-600)+372 || tot2.PayableFee != tot2.DetailFee+tot2.AdjustmentFee {
		t.Errorf("totals2: %+v", tot2)
	}
	Apply(settled, "ST-M-202611-OP01", lines2, adjs2, gen11)
	// 不变量：每单所有记录的服务费累加 = 按最终基数复算
	for _, o := range orders {
		s, ok := settled[o.OrderID]
		if !ok {
			continue
		}
		v := vs[o.PricingVersionID]
		final := BaseAt(o, v.Basis, in2.Refunds[o.OrderID], in2.Corrections[o.OrderID], gen11)
		if s.Fee != Fee(final, v.Ratios[o.BrandID]) {
			t.Errorf("%s 累计服务费 %d ≠ 复算 %d", o.OrderID, s.Fee, Fee(final, v.Ratios[o.BrandID]))
		}
	}
	// 再出一次（无新事实）不应产生调整项
	in3 := in2
	in3.Month, in3.At = "2026-12", mustTime(t, "2027-01-01T00:30:00+08:00")
	_, adjs3 := Build(in3)
	if len(adjs3) != 0 {
		t.Errorf("无新事实不应有调整项: %+v", adjs3)
	}
}

func TestBuildSkipsUnknownVersion(t *testing.T) {
	o := Order{OrderID: "X", OperatorID: "OP01", BrandID: "BR01", PricingVersionID: "PR0009", EndAt: mustTime(t, "2026-10-02T08:20:00+08:00"), ReportReceivedAt: mustTime(t, "2026-10-02T08:20:00+08:00"), HasBill: true, Receivable: 100}
	lines, _ := Build(Input{Month: "2026-10", At: mustTime(t, "2026-11-01T00:30:00+08:00"), Orders: []Order{o}, Versions: map[string]Version{}})
	if len(lines) != 0 {
		t.Errorf("未收到规则版本的订单不出单: %+v", lines)
	}
}

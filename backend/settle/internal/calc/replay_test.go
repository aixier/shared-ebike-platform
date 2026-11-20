package calc

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// 原型样例回放：用 orders/payments/pricing 的事实逐期出单，与 settlements.json 的 9 张结算单逐单比对。
// 数据由原型 mockdata 转成整数分（见 testdata/prototype.json 的 source）。

type fxVersion struct {
	VersionID    string            `json:"versionId"`
	EffectiveAt  time.Time         `json:"effectiveAt"`
	Basis        string            `json:"basis"`
	Ratios       map[string]string `json:"ratios"`
	StartFee     int64             `json:"startFee"`
	StartMinutes int               `json:"startMinutes"`
	PerMinute    int64             `json:"perMinute"`
	DispatchFee  int64             `json:"dispatchFee"`
	Allocation   string            `json:"allocation"`
}

type fxOrder struct {
	OrderID          string    `json:"orderId"`
	BrandID          string    `json:"brandId"`
	OperatorID       string    `json:"operatorId"`
	PricingVersionID string    `json:"pricingVersionId"`
	RequestAt        time.Time `json:"requestAt"`
	StartAt          time.Time `json:"startAt"`
	EndAt            time.Time `json:"endAt"`
	ReportReceivedAt time.Time `json:"reportReceivedAt"`
	BilledMinutes    int       `json:"billedMinutes"`
	UnlockResult     string    `json:"unlockResult"`
	Degraded         bool      `json:"degraded"`
	Receivable       int64     `json:"receivable"`
	DispatchFee      int64     `json:"dispatchFee"`
	Payable          int64     `json:"payable"`
	MonthCardDeduct  int64     `json:"monthCardDeduct"`
}

type fxRefund struct {
	OrderID       string    `json:"orderId"`
	RefundedTotal int64     `json:"refundedTotal"`
	NetReceived   int64     `json:"netReceived"`
	Kind          string    `json:"kind"`
	RefundedAt    time.Time `json:"refundedAt"`
}

type fxExpected struct {
	SettlementID string    `json:"settlementId"`
	OperatorID   string    `json:"operatorId"`
	Month        string    `json:"month"`
	GeneratedAt  time.Time `json:"generatedAt"`
	Totals       Totals    `json:"totals"`
	MonthCard    struct {
		PlatformIncome     int64 `json:"platformIncome"`
		PlatformCardOrders int   `json:"platformCardOrders"`
	} `json:"monthCard"`
	Brands []struct {
		BrandID             string `json:"brandId"`
		OrderCount          int    `json:"orderCount"`
		ReceivableSum       int64  `json:"receivableSum"`
		BaseSum             int64  `json:"baseSum"`
		DetailFee           int64  `json:"detailFee"`
		AdjustmentFee       int64  `json:"adjustmentFee"`
		MonthCardOrderCount int    `json:"monthCardOrderCount"`
		MonthCardShare      int64  `json:"monthCardShare"`
		RatioBreakdown      []struct {
			Ratio             string   `json:"ratio"`
			PricingVersionIDs []string `json:"pricingVersionIds"`
			OrderCount        int      `json:"orderCount"`
			BaseSum           int64    `json:"baseSum"`
			Fee               int64    `json:"fee"`
		} `json:"ratioBreakdown"`
	} `json:"brands"`
	Details []struct {
		OrderID              string  `json:"orderId"`
		BrandID              string  `json:"brandId"`
		Receivable           int64   `json:"receivable"`
		RefundedAtGeneration int64   `json:"refundedAtGeneration"`
		Base                 int64   `json:"base"`
		Ratio                string  `json:"ratio"`
		Fee                  int64   `json:"fee"`
		Remark               *string `json:"remark"`
	} `json:"details"`
	Adjustments []struct {
		AdjustmentID string `json:"adjustmentId"`
		Type         string `json:"type"`
		BrandID      string `json:"brandId"`
		BaseDelta    int64  `json:"baseDelta"`
		FeeDelta     int64  `json:"feeDelta"`
		Items        []struct {
			OrderID      string `json:"orderId"`
			PreviousBase int64  `json:"previousBase"`
			NewBase      int64  `json:"newBase"`
			BaseDelta    int64  `json:"baseDelta"`
			Ratio        string `json:"ratio"`
			FeeDelta     int64  `json:"feeDelta"`
		} `json:"items"`
	} `json:"adjustments"`
}

type fixture struct {
	Versions []fxVersion `json:"versions"`
	Orders   []fxOrder   `json:"orders"`
	Refunds  []fxRefund  `json:"refunds"`
	Cards    []struct {
		Price  int64     `json:"price"`
		PaidAt time.Time `json:"paidAt"`
	} `json:"cards"`
	Corrections []struct {
		DisputeID string    `json:"disputeId"`
		OrderID   string    `json:"orderId"`
		BaseDelta int64     `json:"baseDelta"`
		DecidedAt time.Time `json:"decidedAt"`
	} `json:"corrections"`
	Expected []fxExpected `json:"expected"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	b, err := os.ReadFile("testdata/prototype.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestReplayPrototypeSettlements(t *testing.T) {
	f := loadFixture(t)
	vs := map[string]Version{}
	for _, v := range f.Versions {
		rs := map[string]Ratio{}
		for k, s := range v.Ratios {
			rs[k] = mustRatio(t, s)
		}
		vs[v.VersionID] = Version{ID: v.VersionID, EffectiveAt: v.EffectiveAt, Basis: v.Basis, Ratios: rs, StartFee: v.StartFee, StartMinutes: v.StartMinutes, PerMinute: v.PerMinute, DispatchFee: v.DispatchFee, Allocation: v.Allocation}
	}
	byOp := map[string][]Order{}
	all := []Order{}
	for _, o := range f.Orders {
		x := Order{OrderID: o.OrderID, BrandID: o.BrandID, OperatorID: o.OperatorID, PricingVersionID: o.PricingVersionID, RequestAt: o.RequestAt, StartAt: o.StartAt, EndAt: o.EndAt, ReportReceivedAt: o.ReportReceivedAt, BilledMinutes: o.BilledMinutes, UnlockResult: o.UnlockResult, Degraded: o.Degraded, HasBill: true, Receivable: o.Receivable, DispatchFee: o.DispatchFee, Payable: o.Payable, MonthCardDeduct: o.MonthCardDeduct}
		byOp[o.OperatorID] = append(byOp[o.OperatorID], x)
		all = append(all, x)
	}
	refunds := map[string][]Refund{}
	for _, r := range f.Refunds {
		refunds[r.OrderID] = append(refunds[r.OrderID], Refund{OrderID: r.OrderID, RefundedTotal: r.RefundedTotal, NetReceived: r.NetReceived, Kind: r.Kind, At: r.RefundedAt})
	}
	corr := map[string][]Correction{}
	for _, c := range f.Corrections {
		corr[c.OrderID] = append(corr[c.OrderID], Correction{OrderID: c.OrderID, DisputeID: c.DisputeID, BaseDelta: c.BaseDelta, At: c.DecidedAt})
	}
	settled := map[string]Settled{}
	exp := append([]fxExpected(nil), f.Expected...)
	sort.SliceStable(exp, func(i, j int) bool { return exp[i].GeneratedAt.Before(exp[j].GeneratedAt) })
	checked := 0
	for _, e := range exp {
		in := Input{Month: e.Month, At: e.GeneratedAt, Orders: byOp[e.OperatorID], Versions: vs, Refunds: refunds, Corrections: corr, Settled: settled}
		lines, adjs := Build(in)
		if len(lines) != len(e.Details) {
			t.Fatalf("%s 明细条数 %d，期望 %d", e.SettlementID, len(lines), len(e.Details))
		}
		for i, d := range e.Details {
			l := lines[i]
			remark := ""
			if d.Remark != nil {
				// "用户未付（按应收计）"依赖支付状态，settle 未订阅支付结果事件，不出此备注
				remark = strings.TrimSuffix(strings.TrimSuffix(*d.Remark, "用户未付（按应收计）"), "；")
			}
			if l.OrderID != d.OrderID || l.Base != d.Base || l.Fee != d.Fee || l.Ratio != mustRatio(t, d.Ratio) || l.Receivable != d.Receivable || l.RefundedAtGeneration != d.RefundedAtGeneration || l.Remark != remark {
				t.Errorf("%s 第 %d 行 %+v，期望 %+v", e.SettlementID, i, l, d)
			}
		}
		if len(adjs) != len(e.Adjustments) {
			t.Fatalf("%s 调整项 %d 组，期望 %d：%+v", e.SettlementID, len(adjs), len(e.Adjustments), adjs)
		}
		for i, ea := range e.Adjustments {
			a := adjs[i]
			if a.Type != ea.Type || a.BrandID != ea.BrandID || a.FeeDelta != ea.FeeDelta || a.BaseDelta != ea.BaseDelta || len(a.Items) != len(ea.Items) {
				t.Errorf("%s 调整项 %s：%+v", e.SettlementID, ea.AdjustmentID, a)
				continue
			}
			for k, it := range ea.Items {
				g := a.Items[k]
				if g.OrderID != it.OrderID || g.PreviousBase != it.PreviousBase || g.NewBase != it.NewBase || g.BaseDelta != it.BaseDelta || g.FeeDelta != it.FeeDelta || g.Ratio != mustRatio(t, it.Ratio) {
					t.Errorf("%s %s 第 %d 项 %+v，期望 %+v", e.SettlementID, ea.AdjustmentID, k, g, it)
				}
			}
		}
		tot, brands := Summarize(lines, adjs)
		if tot != e.Totals {
			t.Errorf("%s 合计 %+v，期望 %+v", e.SettlementID, tot, e.Totals)
		}
		// 月卡分摊：当月全平台月卡款按使用月卡的单数分到品牌
		var income int64
		for _, c := range f.Cards {
			if MonthOf(c.PaidAt) == e.Month {
				income += c.Price
			}
		}
		counts := MonthCardCounts(all, e.Month, e.GeneratedAt)
		shares := Allocate(income, counts)
		if income != e.MonthCard.PlatformIncome || SumCounts(counts) != e.MonthCard.PlatformCardOrders {
			t.Errorf("%s 月卡 %d/%d，期望 %+v", e.SettlementID, income, SumCounts(counts), e.MonthCard)
		}
		if len(brands) != len(e.Brands) {
			t.Fatalf("%s 品牌数 %d，期望 %d", e.SettlementID, len(brands), len(e.Brands))
		}
		for i, eb := range e.Brands {
			b := brands[i]
			if b.BrandID != eb.BrandID || b.OrderCount != eb.OrderCount || b.ReceivableSum != eb.ReceivableSum || b.BaseSum != eb.BaseSum || b.DetailFee != eb.DetailFee || b.AdjustmentFee != eb.AdjustmentFee {
				t.Errorf("%s 品牌 %+v，期望 %+v", e.SettlementID, b, eb)
			}
			if counts[b.BrandID] != eb.MonthCardOrderCount || shares[b.BrandID] != eb.MonthCardShare {
				t.Errorf("%s %s 月卡 %d/%d，期望 %d/%d", e.SettlementID, b.BrandID, counts[b.BrandID], shares[b.BrandID], eb.MonthCardOrderCount, eb.MonthCardShare)
			}
			if len(b.RatioBreakdown) != len(eb.RatioBreakdown) {
				t.Errorf("%s %s 比例分组 %+v", e.SettlementID, b.BrandID, b.RatioBreakdown)
				continue
			}
			for k, g := range eb.RatioBreakdown {
				x := b.RatioBreakdown[k]
				if x.Ratio != mustRatio(t, g.Ratio) || x.OrderCount != g.OrderCount || x.BaseSum != g.BaseSum || x.Fee != g.Fee || len(x.PricingVersionIDs) != len(g.PricingVersionIDs) {
					t.Errorf("%s %s 分组 %+v，期望 %+v", e.SettlementID, b.BrandID, x, g)
				}
			}
		}
		Apply(settled, e.SettlementID, lines, adjs, e.GeneratedAt)
		checked++
	}
	if checked != 9 {
		t.Fatalf("应回放 9 张结算单，实际 %d", checked)
	}
	// 不变量：任一订单所有结算记录的服务费累加 = 按最终基数复算
	end := exp[len(exp)-1].GeneratedAt
	for _, o := range all {
		s, ok := settled[o.OrderID]
		if !ok {
			continue
		}
		v := vs[o.PricingVersionID]
		want := Fee(BaseAt(o, v.Basis, refunds[o.OrderID], corr[o.OrderID], end), v.Ratios[o.BrandID])
		if s.Fee != want {
			t.Errorf("%s 累计 %d ≠ 复算 %d", o.OrderID, s.Fee, want)
		}
	}
}

// 异议 DS202611003：PR0002 下核实为 19 分钟，基数更正 = 新应收 − 原应收 = 9.5 − 6.5
func TestDisputeCorrectionFromMinutes(t *testing.T) {
	v := Version{StartFee: 150, StartMinutes: 3, PerMinute: 50, DispatchFee: 500}
	o := Order{Receivable: 650, DispatchFee: 0}
	if d := CorrectionDelta(v, o, 19); d != 300 {
		t.Errorf("baseDelta = %d", d)
	}
	o2 := Order{Receivable: 1150, DispatchFee: 500} // 站外还车：调度费不变
	if d := CorrectionDelta(v, o2, 10); d != 150+350+500-1150 {
		t.Errorf("baseDelta = %d", d)
	}
}

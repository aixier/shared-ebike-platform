package app

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"
)

// 原型样例经事件投影、落库、出单的全链路回放：9 张结算单的合计、品牌汇总、调整项、月卡分摊与 settlements.json 一致。
// 样例数据与 internal/calc 的回放共用（../calc/testdata/prototype.json，已转成整数分）。

func TestReplayPrototypeThroughDB(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../calc/testdata/prototype.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Versions []map[string]any `json:"versions"`
		Orders   []struct {
			OrderID, BrandID, OperatorID, VehicleID, PricingVersionID, UnlockResult string
			RequestAt, StartAt, EndAt, ReportReceivedAt                             string
			BilledMinutes                                                           int
			DistanceKm                                                              *float64
			Degraded, LateReport                                                    bool
			Receivable, DispatchFee, Payable, MonthCardDeduct                       int64
		} `json:"orders"`
		Refunds     []map[string]any `json:"refunds"`
		Cards       []map[string]any `json:"cards"`
		Corrections []struct {
			DisputeID, OrderID, DecidedAt string
			BaseDelta                     int64
		} `json:"corrections"`
		Expected []struct {
			SettlementID, OperatorID, Month, GeneratedAt string
			Totals                                       map[string]int64
			Brands                                       []struct {
				BrandID        string
				DetailFee      int64
				AdjustmentFee  int64
				MonthCardShare int64
			}
			Adjustments []struct {
				AdjustmentID, Type string
				FeeDelta           int64
			}
		} `json:"expected"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	a, clk := newApp(t, "2026-12-01T12:00:00+08:00")
	for _, v := range f.Versions {
		feed(t, a, "rule.version.published", map[string]any{"versionId": v["versionId"], "effectiveAt": v["effectiveAt"],
			"ride":        map[string]any{"startFee": v["startFee"], "startMinutes": v["startMinutes"], "perMinute": v["perMinute"]},
			"dispatchFee": v["dispatchFee"], "monthCard": map[string]any{"allocation": v["allocation"]},
			"serviceFee": map[string]any{"basis": v["basis"], "ratios": v["ratios"]}})
	}
	seenBrand := map[string]bool{}
	for _, o := range f.Orders {
		if !seenBrand[o.BrandID] {
			seenBrand[o.BrandID] = true
			feed(t, a, "fleet.brand.registered", map[string]any{"brandId": o.BrandID, "operatorId": o.OperatorID, "name": o.BrandID, "operatorShortName": o.OperatorID})
		}
		feed(t, a, "ride.order.ended", map[string]any{"orderId": o.OrderID, "userId": "U1", "vehicleId": o.VehicleID, "brandId": o.BrandID, "operatorId": o.OperatorID,
			"pricingVersionId": o.PricingVersionID, "requestAt": o.RequestAt, "unlockResult": o.UnlockResult, "startAt": o.StartAt, "endAt": o.EndAt,
			"distanceKm": o.DistanceKm, "reportReceivedAt": o.ReportReceivedAt, "lateReport": o.LateReport, "degraded": map[bool]any{true: map[string]any{"eventId": nil}, false: nil}[o.Degraded]})
		feed(t, a, "bill.bill.issued", map[string]any{"orderId": o.OrderID, "brandId": o.BrandID, "operatorId": o.OperatorID, "pricingVersionId": o.PricingVersionID,
			"billedMinutes": o.BilledMinutes, "dispatchFee": o.DispatchFee, "receivable": o.Receivable, "monthCardDeduct": o.MonthCardDeduct, "payable": o.Payable})
	}
	for _, r := range f.Refunds {
		feed(t, a, "bill.bill.refunded", r)
	}
	for _, c := range f.Cards {
		feed(t, a, "bill.card.purchased", c)
	}
	for _, c := range f.Corrections {
		if _, err := testPool.Exec(ctx, `INSERT INTO corrections(dispute_id, order_id, minutes, base_delta, decided_at) VALUES ($1,$2,0,$3,$4)`, c.DisputeID, c.OrderID, c.BaseDelta, c.DecidedAt); err != nil {
			t.Fatal(err)
		}
	}
	months := map[string]string{}
	for _, e := range f.Expected {
		months[e.Month] = e.GeneratedAt
	}
	keys := []string{}
	for m := range months {
		keys = append(keys, m)
	}
	sort.Strings(keys)
	for _, m := range keys {
		clk.Set(months[m])
		if _, err := a.Generate(ctx, m, "自动出单", SystemActor); err != nil {
			t.Fatal(err)
		}
	}
	clk.t = time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	for _, e := range f.Expected {
		v, err := a.GetSettlement(ctx, "", e.SettlementID, true)
		if err != nil {
			t.Fatalf("%s: %v", e.SettlementID, err)
		}
		got := map[string]int64{"orderCount": int64(v.Totals.OrderCount), "receivableSum": v.Totals.ReceivableSum, "baseSum": v.Totals.BaseSum,
			"detailFee": v.Totals.DetailFee, "adjustmentFee": v.Totals.AdjustmentFee, "payableFee": v.Totals.PayableFee}
		for k, w := range e.Totals {
			if got[k] != w {
				t.Errorf("%s %s = %d，期望 %d", e.SettlementID, k, got[k], w)
			}
		}
		if len(v.Brands) != len(e.Brands) {
			t.Errorf("%s 品牌数 %d", e.SettlementID, len(v.Brands))
			continue
		}
		for i, b := range e.Brands {
			g := v.Brands[i]
			if g.BrandID != b.BrandID || g.DetailFee != b.DetailFee || g.AdjustmentFee != b.AdjustmentFee || g.MonthCardShare != b.MonthCardShare {
				t.Errorf("%s 品牌 %+v，期望 %+v", e.SettlementID, g, b)
			}
		}
		if len(v.Adjustments) != len(e.Adjustments) {
			t.Errorf("%s 调整项 %d 条", e.SettlementID, len(v.Adjustments))
			continue
		}
		for i, ad := range e.Adjustments {
			g := v.Adjustments[i]
			if g.AdjustmentID != ad.AdjustmentID || g.Type != ad.Type || g.FeeDelta != ad.FeeDelta {
				t.Errorf("%s 调整项 %s/%s/%d，期望 %+v", e.SettlementID, g.AdjustmentID, g.Type, g.FeeDelta, ad)
			}
		}
	}
}

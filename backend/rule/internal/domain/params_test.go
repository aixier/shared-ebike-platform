package domain

import (
	"encoding/json"
	"testing"
)

// trace: 6.9 任务 9

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

func TestParseParamsPatch_合法改动(t *testing.T) {
	p, errs := ParseParamsPatch(raw(t, `{"items":{"payWindowHours":48,"csRefundLimit":3000,"pauseThreshold":{"availability":0.75},"retentionYears":{"orders":6}}}`)["items"])
	if len(errs) > 0 {
		t.Fatalf("不应有错误：%s", fieldsOf(errs))
	}
	cur := ParamItems{
		PayWindowHours: i64(24), CsRefundLimit: i64(2000),
		PauseThreshold: &PauseThreshold{UnlockSuccessRate: f64(0.9), Availability: f64(0.8), SustainMinutes: i64(120)},
		RetentionYears: &RetentionYears{Orders: i64(5), Payments: i64(10)},
		NearbyRadiusM:  i64(500),
	}
	got := MergeParams(cur, p)
	if *got.PayWindowHours != 48 || *got.CsRefundLimit != 3000 || *got.NearbyRadiusM != 500 {
		t.Errorf("顶层合并不对：%+v", got)
	}
	if *got.PauseThreshold.Availability != 0.75 || *got.PauseThreshold.UnlockSuccessRate != 0.9 || *got.PauseThreshold.SustainMinutes != 120 {
		t.Errorf("嵌套项应只改传入的字段：%+v", got.PauseThreshold)
	}
	if *got.RetentionYears.Orders != 6 || *got.RetentionYears.Payments != 10 {
		t.Errorf("保存期限合并不对：%+v", got.RetentionYears)
	}
	if *cur.PayWindowHours != 24 || *cur.PauseThreshold.Availability != 0.8 {
		t.Error("合并不得改动旧值")
	}
	before, after := DiffParams(cur, got)
	if len(before) != 4 || len(after) != 4 {
		t.Errorf("应只列出改动的 4 项，实得 before=%v after=%v", before, after)
	}
	if _, ok := after["nearbyRadiusM"]; ok {
		t.Error("未改动的项不应出现在留痕里")
	}
}

func TestParseParamsPatch_逐项报错(t *testing.T) {
	_, errs := ParseParamsPatch(raw(t, `{"items":{"payWindowHours":0,"csRefundLimit":-1,"nearbyRadiusM":1.5,"availabilityTarget":1.2,"pauseThreshold":{"unlockSuccessRate":0.123456},"foo":1}}`)["items"])
	for _, f := range []string{"items.payWindowHours", "items.csRefundLimit", "items.nearbyRadiusM", "items.availabilityTarget", "items.pauseThreshold.unlockSuccessRate", "items.foo"} {
		if !hasField(errs, f) {
			t.Errorf("缺少 %s 的错误；实得 %s", f, fieldsOf(errs))
		}
	}
	_, errs = ParseParamsPatch(raw(t, `{"items":{}}`)["items"])
	if !hasField(errs, "items") {
		t.Error("一项都没改应报错")
	}
	_, errs = ParseParamsPatch(nil)
	if !hasField(errs, "items") {
		t.Error("缺 items 应报错")
	}
}

func TestParamItems_未设置的项输出为null(t *testing.T) {
	b, _ := json.Marshal(ParamItems{PayWindowHours: i64(24)})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if v, ok := m["backfillWindowHours"]; !ok || v != nil {
		t.Errorf("未设置的项应为 null：%s", b)
	}
	if m["payWindowHours"].(float64) != 24 {
		t.Errorf("已设置的项应输出：%s", b)
	}
}

func TestParams_新参数范围与缺省值(t *testing.T) {
	_, errs := ParseParamsPatch(raw(t, `{"items":{"deferredVerifyDays":31,"legalAge":9}}`)["items"])
	if len(errs) != 2 {
		t.Errorf("超范围应逐项报错：%s", fieldsOf(errs))
	}
	p, errs := ParseParamsPatch(raw(t, `{"items":{"deferredVerifyDays":5,"legalAge":18}}`)["items"])
	if len(errs) > 0 {
		t.Fatal(fieldsOf(errs))
	}
	d := ParamItems{}.WithDefaults()
	if *d.DeferredVerifyDays != 3 || *d.LegalAge != 16 || d.PayWindowHours != nil {
		t.Errorf("缺省值只补两项：%+v", d)
	}
	got := MergeParams(d, p)
	if *got.DeferredVerifyDays != 5 || *got.LegalAge != 18 || *d.LegalAge != 16 {
		t.Errorf("合并不对：%+v", got)
	}
	if again := got.WithDefaults(); *again.LegalAge != 18 {
		t.Error("已录入的值不应被缺省值覆盖")
	}
}

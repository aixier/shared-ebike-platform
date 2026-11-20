package domain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// trace: R-19 R-20 R-21 R-22 BR-15 任务 5 6 8

func raw(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewBufferString(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func sp(s string) *string { return &s }

// baseContent 取原型 PR0003 的内容（金额已换成分）。
func baseContent() Content {
	one := int64(1)
	return Content{
		Ride:        RideRule{VehicleType: sp("电单车"), StartFee: 150, StartMinutes: 3, PerMinute: 50, BillingUnit: sp("按分钟计，不足 1 分钟按 1 分钟")},
		DispatchFee: 500,
		MonthCard:   MonthCardRule{Price: 2500, Days: 30, PerOrderCap: 300, AppliesTo: sp("骑行费（不含调度费）"), Allocation: "按骑行单数"},
		Coupons: []CouponTemplate{
			{TemplateID: "CPT01", Name: "新用户骑行券", Amount: 200, Audience: sp("新注册并完成实名的用户"), ValidDays: 7, Issue: sp("注册后自动发放"), PerUser: &one},
		},
		Deposit:    DepositRule{Amount: 19900},
		CreditFree: CreditFreeRule{Threshold: 550, Providers: []string{"微信支付分", "芝麻信用"}},
		ServiceFee: ServiceFeeRule{Basis: "订单应收", Ratios: map[string]float64{"BR01": 0.62, "BR02": 0.6, "BR03": 0.61}},
		RiskLimits: RiskLimits{PerUserOpenDegradedOrders: 1, Normal: DegradeLimit{120, 6000}, Tight: DegradeLimit{30, 1500}, HourlyDegradedOrders: 200, DegradeDurationAlertMinutes: 30},
	}
}

func fieldsOf(errs []FieldError) string {
	var b []string
	for _, e := range errs {
		b = append(b, e.Field+":"+e.Message)
	}
	return strings.Join(b, " | ")
}

func hasField(errs []FieldError, field string) bool {
	for _, e := range errs {
		if e.Field == field {
			return true
		}
	}
	return false
}

func TestCheckReason(t *testing.T) {
	if _, ok := CheckReason(raw(t, `{"reason":"调价"}`)); ok {
		t.Error("不足 4 个字应拒绝")
	}
	if _, ok := CheckReason(raw(t, `{}`)); ok {
		t.Error("缺事由应拒绝")
	}
	if _, ok := CheckReason(raw(t, `{"reason":"   调价   "}`)); ok {
		t.Error("去掉空白后不足 4 个字应拒绝")
	}
	if r, ok := CheckReason(raw(t, `{"reason":" 政府调价 "}`)); !ok || r != "政府调价" {
		t.Errorf("4 个字应通过并去空白，实得 %q %v", r, ok)
	}
}

func TestParseCreate_只传改动项其余沿用(t *testing.T) {
	in, errs := ParseCreate(raw(t, `{"name":"2027年调价","reason":"政府调价通知","ride":{"perMinute":60},"serviceFee":{"ratios":{"BR02":0.65}},"riskLimits":{"tight":{"maxMinutes":20}}}`))
	if len(errs) > 0 {
		t.Fatalf("不应有错误：%s", fieldsOf(errs))
	}
	base := baseContent()
	got, errs := in.Apply(&base)
	if len(errs) > 0 {
		t.Fatalf("不应有错误：%s", fieldsOf(errs))
	}
	if got.Ride.PerMinute != 60 || got.Ride.StartFee != 150 || got.Ride.StartMinutes != 3 || *got.Ride.VehicleType != "电单车" {
		t.Errorf("骑行单价合并不对：%+v", got.Ride)
	}
	if got.ServiceFee.Ratios["BR02"] != 0.65 || got.ServiceFee.Ratios["BR01"] != 0.62 || got.ServiceFee.Basis != "订单应收" {
		t.Errorf("服务费合并不对：%+v", got.ServiceFee)
	}
	if got.RiskLimits.Tight.MaxMinutes != 20 || got.RiskLimits.Tight.MaxAmount != 1500 || got.RiskLimits.Normal.MaxMinutes != 120 {
		t.Errorf("限额合并不对：%+v", got.RiskLimits)
	}
	if got.Deposit.Amount != 19900 || got.MonthCard.Price != 2500 || len(got.Coupons) != 1 {
		t.Errorf("未改项应沿用：%+v", got)
	}
	// 旧版本不可被改动
	if base.ServiceFee.Ratios["BR02"] != 0.6 || base.Ride.PerMinute != 50 {
		t.Error("合并不得改动前一版本的内容")
	}
	if in.Name != "2027年调价" || in.Note != nil {
		t.Errorf("名称或说明不对：%q %v", in.Name, in.Note)
	}
}

func TestParseCreate_基数口径与月卡分配是配置项(t *testing.T) {
	in, errs := ParseCreate(raw(t, `{"name":"口径调整","reason":"按Q3答复调整","serviceFee":{"basis":"订单实收"},"monthCard":{"allocation":"按骑行时长"}}`))
	if len(errs) > 0 {
		t.Fatalf("不应有错误：%s", fieldsOf(errs))
	}
	base := baseContent()
	got, _ := in.Apply(&base)
	if got.ServiceFee.Basis != "订单实收" || got.MonthCard.Allocation != "按骑行时长" {
		t.Errorf("口径或分配规则未生效：%+v %+v", got.ServiceFee, got.MonthCard)
	}
	_, errs = ParseCreate(raw(t, `{"name":"x","reason":"按Q3答复调整","serviceFee":{"basis":"订单净额"},"monthCard":{"allocation":"  "}}`))
	if !hasField(errs, "serviceFee.basis") || !hasField(errs, "monthCard.allocation") {
		t.Errorf("口径取值与空分配规则应报错：%s", fieldsOf(errs))
	}
}

func TestParseCreate_取值范围与小数位逐项报错(t *testing.T) {
	_, errs := ParseCreate(raw(t, `{
		"name":"",
		"reason":"政府调价通知",
		"ride":{"startFee":155,"startMinutes":1.5,"perMinute":510},
		"dispatchFee":"5元",
		"monthCard":{"price":50001,"days":0,"perOrderCap":-1},
		"deposit":{"amount":100001},
		"creditFree":{"threshold":300},
		"serviceFee":{"ratios":{"BR01":0.12345,"BR02":1.2,"XX9":0.5}},
		"riskLimits":{"perUserOpenDegradedOrders":6,"normal":{"maxMinutes":0,"maxAmount":100001},"hourlyDegradedOrders":0,"degradeDurationAlertMinutes":1441}
	}`))
	want := []string{
		"name", "ride.startFee", "ride.startMinutes", "ride.perMinute", "dispatchFee",
		"monthCard.price", "monthCard.days", "monthCard.perOrderCap", "deposit.amount", "creditFree.threshold",
		"serviceFee.ratios.BR01", "serviceFee.ratios.BR02", "serviceFee.ratios.XX9",
		"riskLimits.perUserOpenDegradedOrders", "riskLimits.normal.maxMinutes", "riskLimits.normal.maxAmount",
		"riskLimits.hourlyDegradedOrders", "riskLimits.degradeDurationAlertMinutes",
	}
	for _, f := range want {
		if !hasField(errs, f) {
			t.Errorf("缺少 %s 的错误；实得 %s", f, fieldsOf(errs))
		}
	}
	for _, e := range errs {
		if e.Message == "" {
			t.Errorf("%s 的错误说明为空", e.Field)
		}
	}
}

func TestParseCreate_错误说明说人话(t *testing.T) {
	_, errs := ParseCreate(raw(t, `{"name":"a","reason":"政府调价通知","ride":{"startFee":155,"perMinute":600}}`))
	msg := fieldsOf(errs)
	if !strings.Contains(msg, "10 分的整数倍") || !strings.Contains(msg, "0～500 分") {
		t.Errorf("说明不清楚：%s", msg)
	}
}

func TestParseCreate_券模板整体替换与逐行校验(t *testing.T) {
	in, errs := ParseCreate(raw(t, `{"name":"新增元旦券","reason":"元旦活动安排","coupons":[
		{"templateId":"CPT01","name":"新用户骑行券","amount":200,"validDays":7,"perUser":1},
		{"templateId":"CPT05","name":"元旦骑行券","amount":200,"validDays":3,"audience":"全部实名用户","issue":"2027-01-01 00:00 自动发放","perUser":1}
	]}`))
	if len(errs) > 0 {
		t.Fatalf("不应有错误：%s", fieldsOf(errs))
	}
	base := baseContent()
	got, _ := in.Apply(&base)
	if len(got.Coupons) != 2 || got.Coupons[1].TemplateID != "CPT05" || *got.Coupons[1].Audience != "全部实名用户" {
		t.Errorf("券模板应整体替换：%+v", got.Coupons)
	}
	if got.Coupons[0].Audience != nil {
		t.Error("整体替换时未传的字段应为空，不从旧模板继承")
	}

	_, errs = ParseCreate(raw(t, `{"name":"x","reason":"元旦活动安排","coupons":[
		{"templateId":"CPT01","name":"a","amount":200,"validDays":7},
		{"templateId":"CPT01","name":"b","amount":5001,"validDays":91,"perUser":0},
		{"name":"c","amount":100,"validDays":3}
	]}`))
	for _, f := range []string{"coupons[1].templateId", "coupons[1].amount", "coupons[1].validDays", "coupons[1].perUser", "coupons[2].templateId"} {
		if !hasField(errs, f) {
			t.Errorf("缺少 %s 的错误；实得 %s", f, fieldsOf(errs))
		}
	}
	for _, e := range errs {
		if strings.HasPrefix(e.Field, "coupons[1]") && (e.Row == nil || *e.Row != 2) {
			t.Errorf("%s 应标第 2 行", e.Field)
		}
	}
}

func TestApply_没有前一版本时须填写完整(t *testing.T) {
	in, errs := ParseCreate(raw(t, `{"name":"首版","reason":"首版计价规则","ride":{"startFee":150}}`))
	if len(errs) > 0 {
		t.Fatalf("解析不应有错误：%s", fieldsOf(errs))
	}
	_, errs = in.Apply(nil)
	for _, f := range []string{"ride.startMinutes", "ride.perMinute", "dispatchFee", "monthCard.price", "monthCard.allocation", "deposit.amount", "creditFree.threshold", "serviceFee.basis", "riskLimits.tight.maxAmount"} {
		if !hasField(errs, f) {
			t.Errorf("缺少 %s 的必填错误；实得 %s", f, fieldsOf(errs))
		}
	}

	full := `{"name":"首版","reason":"首版计价规则",
		"ride":{"startFee":150,"startMinutes":3,"perMinute":40},
		"dispatchFee":300,
		"monthCard":{"price":2000,"days":30,"perOrderCap":300,"allocation":"按骑行单数"},
		"deposit":{"amount":9900},
		"creditFree":{"threshold":550},
		"serviceFee":{"basis":"订单应收","ratios":{"BR01":0.62}},
		"riskLimits":{"perUserOpenDegradedOrders":1,"normal":{"maxMinutes":120,"maxAmount":6000},"tight":{"maxMinutes":45,"maxAmount":2000},"hourlyDegradedOrders":300,"degradeDurationAlertMinutes":30}}`
	in, errs = ParseCreate(raw(t, full))
	if len(errs) > 0 {
		t.Fatalf("解析不应有错误：%s", fieldsOf(errs))
	}
	got, errs := in.Apply(nil)
	if len(errs) > 0 {
		t.Fatalf("填写完整不应报错：%s", fieldsOf(errs))
	}
	if got.Coupons == nil || len(got.Coupons) != 0 {
		t.Error("未给券模板时应为空数组")
	}
	if got.ServiceFee.Ratios["BR01"] != 0.62 {
		t.Error("比例应写入")
	}
}

func TestResolveEffectiveAt(t *testing.T) {
	now := ts("2026-11-19T10:00:00.700+08:00")
	got, fe := ResolveEffectiveAt(nil, now)
	if fe != nil || !got.Equal(ts("2026-11-19T10:00:00+08:00")) {
		t.Errorf("缺省应为当前时刻（到秒）：%v %v", got, fe)
	}
	got, fe = ResolveEffectiveAt(sp("2026-11-19T09:59:30+08:00"), now)
	if fe != nil || !got.Equal(ts("2026-11-19T10:00:00+08:00")) {
		t.Errorf("一分钟内的时钟偏差按立即生效：%v %v", got, fe)
	}
	if _, fe = ResolveEffectiveAt(sp("2026-11-19T09:58:00+08:00"), now); fe == nil || fe.Field != "effectiveAt" {
		t.Error("早于现在应报错")
	}
	if _, fe = ResolveEffectiveAt(sp("2026-12-01 00:00:00"), now); fe == nil {
		t.Error("不带时区的格式应报错")
	}
	got, fe = ResolveEffectiveAt(sp("2027-01-01T00:00:00+08:00"), now)
	if fe != nil || !got.Equal(ts("2027-01-01T00:00:00+08:00")) {
		t.Errorf("未来时刻应原样接受：%v %v", got, fe)
	}
	_ = time.Second
}

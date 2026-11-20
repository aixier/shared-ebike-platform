package billing

import (
	"testing"
	"time"
)

func TestCouponExpireAt(t *testing.T) {
	// 第 N 天 23:59:59：7 天券 11-10 发放，到 11-16 23:59:59
	got := CouponExpireAt(tm("2026-11-10 09:30:00"), 7, cst)
	if !got.Equal(tm("2026-11-16 23:59:59")) {
		t.Fatalf("得到 %v", got)
	}
	// 跨零点：UTC 时刻落在北京时间次日
	utc := time.Date(2026, 11, 10, 17, 0, 0, 0, time.UTC) // 北京 11-11 01:00
	if g := CouponExpireAt(utc, 1, cst); !g.Equal(tm("2026-11-11 23:59:59")) {
		t.Fatalf("应按本地日期算，得到 %v", g.In(cst))
	}
}

func TestClaimWindow(t *testing.T) {
	claim := CouponTemplate{TemplateID: "CPT02", Issue: "用户领券中心领取（每周五 17:00 开放）"}
	auto := CouponTemplate{TemplateID: "CPT01", Issue: "注册后自动发放"}
	// 2026-11-20 是周五
	cases := []struct {
		at   string
		open bool
	}{
		{"2026-11-20 16:59:59", false}, {"2026-11-20 17:00:00", true}, {"2026-11-21 08:00:00", true},
		{"2026-11-22 23:59:59", true}, {"2026-11-23 00:00:00", false}, {"2026-11-18 12:00:00", false},
	}
	for _, c := range cases {
		w := ClaimWindowAt(claim, tm(c.at), cst)
		if w.Open != c.open {
			t.Errorf("%s open=%v，期望 %v", c.at, w.Open, c.open)
		}
		if !w.Open && w.Why != "每周五 17:00 开放领取" {
			t.Errorf("%s why=%s", c.at, w.Why)
		}
	}
	w := ClaimWindowAt(claim, tm("2026-11-22 10:00:00"), cst)
	if !w.Start.Equal(tm("2026-11-20 17:00:00")) {
		t.Errorf("本期起点应为周五 17:00，得到 %v", w.Start.In(cst))
	}
	if w := ClaimWindowAt(auto, tm("2026-11-21 10:00:00"), cst); w.Open || w.Why != "该券由系统自动发放" {
		t.Errorf("自动发放券不可领：%+v", w)
	}
}

func TestCareTemplate(t *testing.T) {
	ts := []CouponTemplate{
		{TemplateID: "CPT01", Issue: "注册后自动发放"},
		{TemplateID: "CPT04", Issue: "开锁失败后自动发放"},
	}
	if c := CareTemplate(ts); c == nil || c.TemplateID != "CPT04" {
		t.Fatalf("得到 %+v", c)
	}
	if CareTemplate(ts[:1]) != nil {
		t.Fatal("没有关怀券模板时应为 nil")
	}
}

func TestMonthCardSpan(t *testing.T) {
	now := tm("2026-11-20 10:00:00")
	// 没有未到期的卡：立即生效
	s, e := MonthCardSpan(now, nil, 30)
	if !s.Equal(now) || !e.Equal(now.AddDate(0, 0, 30)) {
		t.Fatalf("得到 %v %v", s, e)
	}
	// 有未到期的卡：顺延到最晚结束时刻
	ends := []time.Time{tm("2026-11-25 00:00:00"), tm("2026-12-25 00:00:00"), tm("2026-11-01 00:00:00")}
	s, e = MonthCardSpan(now, ends, 30)
	if !s.Equal(tm("2026-12-25 00:00:00")) || !e.Equal(tm("2027-01-24 00:00:00")) {
		t.Fatalf("应顺延，得到 %v %v", s, e)
	}
}

func TestMonthCardStatus(t *testing.T) {
	s, e := tm("2026-11-01 00:00:00"), tm("2026-12-01 00:00:00")
	if MonthCardStatus(s, e, tm("2026-10-31 00:00:00")) != "未生效" ||
		MonthCardStatus(s, e, tm("2026-11-01 00:00:00")) != "生效中" ||
		MonthCardStatus(s, e, tm("2026-12-01 00:00:00")) != "已过期" {
		t.Fatal("月卡状态不对")
	}
}

func TestCouponStatus(t *testing.T) {
	exp := tm("2026-11-16 23:59:59")
	if CouponStatus(false, exp, tm("2026-11-16 23:59:59")) != "未使用" ||
		CouponStatus(false, exp, tm("2026-11-17 00:00:00")) != "已过期" ||
		CouponStatus(true, exp, tm("2026-11-17 00:00:00")) != "已使用" {
		t.Fatal("券状态不对")
	}
}

// 券模板发放类别（第二轮：注册券、渠道券、系统券、定时券）
func TestKindOf(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	cases := []struct {
		issue, audience, want string
	}{
		{"注册后自动发放", "", KindRegister},
		{"注册后自动发放", "微信新用户", KindChannel},
		{"注册后自动发放", "支付宝新用户", KindChannel},
		{"开锁失败后自动发放", "", KindCare},
		{"用户在券中心领取（每周五 17:00 开放）", "", KindClaim},
		{"系统一次性发放", "全部用户", KindSystem},
		{"2027-01-01 00:00 自动发放", "", KindScheduled},
		{"2027-01-01 自动发放", "", KindScheduled},
		{"人工发放", "", KindOther},
	}
	for _, c := range cases {
		if got := KindOf(CouponTemplate{Issue: c.issue, Audience: c.audience}, loc); got != c.want {
			t.Errorf("%q/%q 应为 %s，得到 %s", c.issue, c.audience, c.want, got)
		}
	}
	if ch := ChannelOf(CouponTemplate{Audience: "支付宝新用户"}); ch != "支付宝" {
		t.Fatal(ch)
	}
	at, ok := ScheduledAt(CouponTemplate{Issue: "2027-01-01 08:30 自动发放"}, loc)
	if !ok || !at.Equal(time.Date(2027, 1, 1, 8, 30, 0, 0, loc)) {
		t.Fatalf("%v %v", at, ok)
	}
	at, ok = ScheduledAt(CouponTemplate{Issue: "2027-01-01 自动发放"}, loc)
	if !ok || !at.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("只写日期按当天零点：%v %v", at, ok)
	}
	if _, ok := ScheduledAt(CouponTemplate{Issue: "注册后自动发放"}, loc); ok {
		t.Fatal("没有日期不是定时券")
	}
}

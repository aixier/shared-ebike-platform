package seed

import (
	"testing"
	"time"

	"bike/rule/internal/domain"
)

// trace: 原型/mockdata/SCHEMA.md 5.7、第 3 节、第 7 节 任务 12

func TestLoad_锚点当天原样载入(t *testing.T) {
	now := time.Date(2026, 12, 1, 12, 0, 0, 0, cst)
	s, err := Load(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Versions) != 4 {
		t.Fatalf("应有 4 个版本，实得 %d", len(s.Versions))
	}
	pr1 := s.Versions[0]
	if pr1.VersionID != "PR0001" || pr1.Seq != 1 || pr1.Content.Ride.StartFee != 150 || pr1.Content.Ride.PerMinute != 40 || pr1.Content.DispatchFee != 300 || pr1.Content.Deposit.Amount != 9900 {
		t.Errorf("PR0001 金额换算不对：%+v", pr1.Content)
	}
	if !pr1.EffectiveAt.Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, cst)) {
		t.Errorf("PR0001 生效时刻不对：%v", pr1.EffectiveAt)
	}
	if pr1.CreatedBy.ID != "AC0001" || pr1.CreatedBy.Kind != "人" {
		t.Errorf("录入人不对：%+v", pr1.CreatedBy)
	}
	want := map[string]string{"PR0001": domain.StatusExpired, "PR0002": domain.StatusExpired, "PR0003": domain.StatusActive, "PR0004": domain.StatusPending}
	for _, v := range domain.Chain(s.Versions, now) {
		if v.Status != want[v.VersionID] {
			t.Errorf("%s 状态应为 %s，实得 %s", v.VersionID, want[v.VersionID], v.Status)
		}
	}
	pr3 := s.Versions[2]
	if pr3.Content.RiskLimits.Tight.MaxAmount != 1500 || pr3.Content.RiskLimits.Tight.MaxMinutes != 30 || pr3.Content.ServiceFee.Ratios["BR03"] != 0.61 || pr3.Content.ServiceFee.Basis != "订单应收" {
		t.Errorf("PR0003 要点不对：%+v", pr3.Content.RiskLimits)
	}
	if s.Versions[3].Content.CreditFree.Threshold != 600 || len(s.Versions[3].Content.Coupons) != 5 {
		t.Error("PR0004 要点不对")
	}
	// BR-14 样例：OD2026101412805 于 10-14 23:51 开锁，按 PR0001
	if v := domain.At(s.Versions, time.Date(2026, 10, 14, 23, 51, 0, 0, cst)); v == nil || v.VersionID != "PR0001" {
		t.Error("BR-14 样例应取 PR0001")
	}
	p := s.Params
	if *p.Items.PayWindowHours != 24 || *p.Items.CsRefundLimit != 2000 || *p.Items.PauseThreshold.UnlockSuccessRate != 0.9 || *p.Items.RetentionYears.Payments != 10 || *p.Items.TrackRetentionDays != 180 {
		t.Errorf("平台参数不对：%+v", p.Items)
	}
	if p.UpdatedBy.ID != "AC0404" {
		t.Error("参数修改人不对")
	}
}

func TestLoad_按今天平移(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 30, 0, 0, cst) // 比锚点早 12 天
	s, err := Load(now)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Versions[2].EffectiveAt.Equal(time.Date(2026, 11, 19, 0, 0, 0, 0, cst)) {
		t.Errorf("PR0003 应平移到今天零点：%v", s.Versions[2].EffectiveAt)
	}
	if !s.Versions[2].CreatedAt.Equal(time.Date(2026, 11, 12, 16, 40, 18, 0, cst)) {
		t.Errorf("录入时刻也应平移：%v", s.Versions[2].CreatedAt)
	}
	// 平移后"现在"相对版本的关系与锚点一致：PR0003 生效中
	for _, v := range domain.Chain(s.Versions, now) {
		if v.VersionID == "PR0003" && v.Status != domain.StatusActive {
			t.Errorf("平移后 PR0003 应生效中，实得 %s", v.Status)
		}
	}
}

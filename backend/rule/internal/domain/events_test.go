package domain

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// trace: contracts/events/rule.md 任务 10 13

func keysOf(m map[string]any) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPublishedPayload_字段与契约一致(t *testing.T) {
	v := Version{VersionID: "PR0005", Seq: 5, Name: "调价", EffectiveAt: ts("2027-02-01T00:00:00+08:00"), Content: baseContent()}
	prev := "PR0004"
	m := roundTrip(t, PublishedPayload(v, &prev))
	want := "coupons,creditFree,deposit,dispatchFee,effectiveAt,monthCard,name,previousVersionId,ride,riskLimits,serviceFee,versionId"
	if keysOf(m) != want {
		t.Errorf("载荷字段应为 %s，实得 %s", want, keysOf(m))
	}
	if keysOf(m["ride"].(map[string]any)) != "perMinute,startFee,startMinutes,vehicleType" {
		t.Errorf("ride 字段不对：%v", m["ride"])
	}
	if keysOf(m["monthCard"].(map[string]any)) != "allocation,days,perOrderCap,price" {
		t.Errorf("monthCard 字段不对：%v", m["monthCard"])
	}
	c := m["coupons"].([]any)[0].(map[string]any)
	if keysOf(c) != "amount,audience,issue,name,perUser,templateId,validDays" {
		t.Errorf("券模板字段不对：%v", c)
	}
	if keysOf(m["creditFree"].(map[string]any)) != "providers,threshold" || keysOf(m["serviceFee"].(map[string]any)) != "basis,ratios" {
		t.Error("免押或服务费字段不对")
	}
	rl := m["riskLimits"].(map[string]any)
	if keysOf(rl) != "degradeDurationAlertMinutes,hourlyDegradedOrders,normal,perUserOpenDegradedOrders,tight" || keysOf(rl["tight"].(map[string]any)) != "maxAmount,maxMinutes" {
		t.Errorf("限额字段不对：%v", rl)
	}
	if m["effectiveAt"] != "2027-02-01T00:00:00+08:00" || m["previousVersionId"] != "PR0004" || m["dispatchFee"].(float64) != 500 {
		t.Errorf("值不对：%v", m)
	}

	m = roundTrip(t, PublishedPayload(v, nil))
	if vv, ok := m["previousVersionId"]; !ok || vv != nil {
		t.Error("没有上一版本时 previousVersionId 应为 null 而不是省略")
	}
}

func TestRevokedAndParamsPayload(t *testing.T) {
	m := roundTrip(t, RevokedPayload("PR0004", ts("2026-11-30T10:00:00+08:00")))
	if keysOf(m) != "revokedAt,versionId" || m["revokedAt"] != "2026-11-30T10:00:00+08:00" {
		t.Errorf("撤销载荷不对：%v", m)
	}
	m = roundTrip(t, ParamsChangedPayload(ParamItems{PayWindowHours: i64(24)}, ts("2026-11-30T10:00:00+08:00")))
	want := "acceptance,arrearsFreezeDays,availabilityTarget,backfillWindowHours,billPushTargetSec,changedAt,csRefundLimit,deferredVerifyDays,depositRefundWorkdays,legalAge,nearbyRadiusM,pauseThreshold,payWindowHours,retentionYears,trackRetentionDays,unlockP90TargetSec,unlockReportTimeoutSec"
	if keysOf(m) != want {
		t.Errorf("参数变更载荷字段应为 %s，实得 %s", want, keysOf(m))
	}
	if m["changedAt"] != "2026-11-30T10:00:00+08:00" || m["payWindowHours"].(float64) != 24 || m["nearbyRadiusM"] != nil {
		t.Errorf("值不对：%v", m)
	}
}

func TestContentDiff_只列改动的部分(t *testing.T) {
	a := baseContent()
	b := cloneContent(a)
	b.Ride.PerMinute = 60
	b.ServiceFee.Ratios["BR02"] = 0.65
	before, after := ContentDiff(&a, b)
	if keysOf(after) != "ride,serviceFee" || keysOf(before) != "ride,serviceFee" {
		t.Errorf("改动部分应为 ride,serviceFee：%v", after)
	}
	before, after = ContentDiff(nil, b)
	if before != nil || len(after) != 8 {
		t.Errorf("首版应列出全部内容：%v %v", before, after)
	}
}

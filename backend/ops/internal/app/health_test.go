package app

import (
	"fmt"
	"testing"
	"time"
)

// buildHistory 2026-12-01 08:00～10:59：青禾每小时 6 单全成功；木棉每小时 6 成 1 败（85.71%，低于 90% 门槛，仿 SCHEMA 5.12 木棉自 04:00 起低于 90%）；
// 简途 09:00～09:30 运营商通道不可达（可用率 50%，低于 80%）。每小时末重算一次指标。
func buildHistory(e *env) {
	n := 0
	for hour := 8; hour <= 10; hour++ {
		for i := 0; i < 7; i++ {
			for _, b := range []string{"BR01", "BR02", "BR03"} {
				n++
				id := fmt.Sprintf("OD20261201%05d", n)
				req := fmt.Sprintf("2026-12-01 %02d:%02d:00", hour, i*5)
				start := fmt.Sprintf("2026-12-01 %02d:%02d:03", hour, i*5)
				h := e.head(id, fmt.Sprintf("U3%05d", n), b, req, nil)
				e.publish("ride.order.created", h)
				if b == "BR02" && i == 6 {
					e.publish("ride.order.unlock_failed", merge(h, "reason", "车辆无响应", "failedAt", e.ts(start)))
					continue
				}
				e.publish("ride.order.unlocked", merge(h, "startAt", e.ts(start)))
			}
		}
		if hour == 9 {
			e.publish("opgw.operator.unreachable", map[string]any{"operatorId": "OP03", "brandIds": []string{"BR03"}, "since": e.ts("2026-12-01 09:00:00"), "lastError": "连接超时"})
			e.publish("opgw.operator.recovered", map[string]any{"operatorId": "OP03", "brandIds": []string{"BR03"}, "recoveredAt": e.ts("2026-12-01 09:30:00")})
		}
		e.setNow(fmt.Sprintf("2026-12-01 %02d:59:59", hour))
		if err := e.a.RefreshMetrics(e.ctx()); err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestBrandMetricsAndMonitor(t *testing.T) {
	e := newEnv(t, "2026-12-01 08:00:00")
	e.seedRules()
	e.seedBrands()
	// 骑行入口可用性：两次采样一次可用
	e.si.healthy = true
	if err := e.a.ProbeEntry(e.ctx()); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Minute)
	e.si.healthy = false
	_ = e.a.ProbeEntry(e.ctx())
	// 一张账单：结束后 4 秒推送
	e.publish("bill.bill.issued", map[string]any{"orderId": "OD2026120199999", "receivable": 200, "collectBy": "免押自动扣款",
		"billPushedAt": e.ts("2026-12-01 08:10:04"), "orderEndAt": e.ts("2026-12-01 08:10:00"), "degraded": false})
	buildHistory(e)
	e.setNow("2026-12-01 10:50:00")
	if err := e.a.RefreshMetrics(e.ctx()); err != nil {
		t.Fatal(err)
	}

	m := e.do("GET", "/api/ops/monitor?hours=6&days=2", e.duty(), nil, "")
	if m.status != 200 {
		t.Fatalf("监控 %d %s", m.status, m.raw)
	}
	pts := map[string]map[string]any{}
	for _, x := range m.body["hourly"].([]any) {
		p := x.(map[string]any)
		pts[p["brandId"].(string)+"@"+p["at"].(string)] = p
	}
	h9 := e.ts("2026-12-01 09:00:00")
	if p := pts["BR01@"+h9]; p["unlockSuccessRate"] != float64(1) || p["availability"] != float64(1) || p["paused"] != false {
		t.Fatalf("青禾 %v", p)
	}
	if p := pts["BR02@"+h9]; p["unlockSuccessRate"] != 0.8571 || p["paused"] != true {
		t.Fatalf("木棉低于门槛 %v", p)
	}
	if p := pts["BR03@"+h9]; p["availability"] != 0.5 || p["paused"] != true || p["unlockSuccessRate"] != float64(1) {
		t.Fatalf("简途可用率 %v", p)
	}
	if p := pts["BR03@"+e.ts("2026-12-01 10:00:00")]; p["availability"] != float64(1) || p["paused"] != false {
		t.Fatalf("简途恢复 %v", p)
	}
	if len(m.body["brands"].([]any)) != 3 {
		t.Fatalf("有指标的品牌 %v", m.body["brands"])
	}
	today := m.body["today"].(map[string]any)
	if today["requests"] != float64(63) || today["unlockFailed"] != float64(3) || today["unlockSuccessRate"] != 0.9524 || today["riding"] != float64(60) {
		t.Fatalf("今日 %v", today)
	}
	th := m.body["thresholds"].(map[string]any)
	if th["pause"].(map[string]any)["unlockSuccessRate"] != 0.9 || th["availabilityTarget"] != 0.995 || th["billPushTargetSec"] != float64(10) {
		t.Fatalf("门槛 %v", th)
	}
	pd := m.body["platformDaily"].([]any)
	if len(pd) != 1 {
		t.Fatalf("平台日统计 %v", pd)
	}
	p0 := pd[0].(map[string]any)
	if p0["date"] != "2026-12-01" || p0["entryAvailability"] != 0.5 || p0["unlockP90Sec"] != float64(3) || p0["billPushP90Sec"] != float64(4) {
		t.Fatalf("平台日统计 %v", p0)
	}
	todayDaily := map[string]map[string]any{}
	for _, x := range m.body["daily"].([]any) {
		p := x.(map[string]any)
		if p["date"] == "2026-12-01" {
			todayDaily[p["brandId"].(string)] = p
		}
	}
	if len(todayDaily) != 3 || todayDaily["BR02"]["unlockSuccessRate"] != 0.8571 || todayDaily["BR03"]["availability"] != 0.9538 {
		t.Fatalf("日序列 %v", todayDaily)
	}
	if m.body["autoPauseByAgent"] != false || m.body["degrade"].(map[string]any)["degraded"] != false {
		t.Fatalf("开关与降级 %s", m.raw)
	}

	// 主管部门精简版：只有开锁成功率
	g := e.do("GET", "/api/ops/monitor/unlock-rate?hours=6", e.gov(), nil, "")
	if g.status != 200 || g.body["limited"] != true || g.body["degrade"] != nil {
		t.Fatalf("精简版 %s", g.raw)
	}
	if _, ok := g.body["hourly"].([]any)[0].(map[string]any)["availability"]; ok {
		t.Fatal("精简版不含可用率")
	}
	if r := e.do("GET", "/api/ops/monitor", e.gov(), nil, ""); r.status != 403 {
		t.Fatal("主管部门不能看完整版")
	}
	// 运营商只见本家
	op := e.token("AC0302", []string{"access.view.own"}, "OP02", false)
	r := e.do("GET", "/api/ops/brands/unlock-rate", op, nil, "")
	if len(r.arr) != 1 || r.arr[0].(map[string]any)["brandId"] != "BR02" || r.arr[0].(map[string]any)["unlockSuccessRate"] != 0.8571 {
		t.Fatalf("运营商本家 %s", r.raw)
	}
	if r := e.do("GET", "/api/ops/brands/unlock-rate?brandIds=BR01,BR02", op, nil, ""); r.status != 403 || r.code() != "SCOPE_DENIED" {
		t.Fatalf("越界 %d %s", r.status, r.raw)
	}
	all := e.do("GET", "/api/ops/brands/unlock-rate?brandIds=BR01,BR09", e.gov(), nil, "")
	if len(all.arr) != 2 || all.arr[1].(map[string]any)["unlockSuccessRate"] != nil {
		t.Fatalf("没有指标为 null %s", all.raw)
	}
}

// 智能体未接入：品牌指标持续低于门槛只告警，不提建议、不暂停；告一次。
func TestBrandBelowOnlyAlerts(t *testing.T) {
	e := newEnv(t, "2026-12-01 08:00:00")
	e.seedRules()
	e.seedBrands()
	buildHistory(e)
	e.setNow("2026-12-01 09:30:00")
	_ = e.a.CheckBrandHealth(e.ctx())
	var n int
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FROM alerts WHERE type = '品牌指标低于门槛'`).Scan(&n)
	if n != 0 {
		t.Fatal("持续未满 120 分钟不告警")
	}
	e.setNow("2026-12-01 10:50:00")
	_ = e.a.RefreshMetrics(e.ctx())
	if err := e.a.CheckBrandHealth(e.ctx()); err != nil {
		t.Fatal(err)
	}
	_ = e.a.CheckBrandHealth(e.ctx())
	al := e.do("GET", "/api/ops/alerts?level=警告", e.duty(), nil, "")
	var below []map[string]any
	for _, x := range al.body["rows"].([]any) {
		if x.(map[string]any)["type"] == "品牌指标低于门槛" {
			below = append(below, x.(map[string]any))
		}
	}
	if len(below) != 1 || below[0]["brandId"] != "BR02" {
		t.Fatalf("木棉告警一次 %v", below)
	}
	if len(e.si.calls) != 0 {
		t.Fatalf("不得自动暂停 %v", e.si.calls)
	}
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FROM suspend_suggestions`).Scan(&n)
	if n != 0 {
		t.Fatal("没有智能体不产生建议")
	}
	// 运维读健康：持续分钟由代码算
	h, _, err := e.a.Agent().ReadHealth(e.ctx(), "BR02")
	if err != nil || !h.Below || h.SustainMinutes != 170 || h.LowMetric != "开锁成功率" {
		t.Fatalf("健康 %+v %v", h, err)
	}
}

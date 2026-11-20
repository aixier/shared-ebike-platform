package app

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestEnterExitDegradeManually(t *testing.T) {
	e := newEnv(t, "2026-11-27 21:06:44")
	e.seedRules()
	duty := e.duty()

	if r := e.do("POST", "/api/ops/degrade/exit", duty, nil, ""); r.status != 409 || r.code() != "DEGRADE_NOT_ACTIVE" {
		t.Fatalf("不在降级时退出 %d %s", r.status, r.raw)
	}
	if r := e.do("POST", "/api/ops/degrade/enter", duty, map[string]any{}, ""); r.status != 400 || r.code() != "INVALID" {
		t.Fatalf("没给预设也没给原因 %d %s", r.status, r.raw)
	}
	if r := e.do("POST", "/api/ops/degrade/enter", duty, map[string]any{"preset": "实名核验不可用", "reason": "短"}, ""); r.code() != "REASON_REQUIRED" {
		t.Fatalf("事由不足 4 字 %s", r.raw)
	}
	if r := e.do("POST", "/api/ops/degrade/enter", duty, map[string]any{"cause": "x", "components": []string{"邮件服务"}}, ""); r.code() != "INVALID" {
		t.Fatalf("故障组件不在枚举 %s", r.raw)
	}

	r := e.do("POST", "/api/ops/degrade/enter", duty, map[string]any{"preset": "实名核验不可用", "reason": "第三方核验接口连续超时"}, "")
	if r.status != 200 {
		t.Fatalf("进入 %d %s", r.status, r.raw)
	}
	ev := r.body
	if ev["eventId"] != "OPS-DG-20261127-01" || ev["status"] != "进行中" || ev["limitsVersionId"] != "PR0002" || ev["enteredBy"] != "AC0401" {
		t.Fatalf("降级事件 %v", ev)
	}
	if ev["cause"] != "实名核验服务不可用（第三方核验接口连续超时）" {
		t.Fatalf("预设原因 %v", ev["cause"])
	}
	tl := ev["timeline"].([]any)
	if len(tl) != 1 || tl[0].(map[string]any)["action"] != "降级进入" || tl[0].(map[string]any)["byKind"] != "人" {
		t.Fatalf("时间线 %v", tl)
	}
	// 已在降级中：返回当前事件，不新建
	r2 := e.do("POST", "/api/ops/degrade/enter", duty, map[string]any{"preset": "支付通道故障"}, "")
	if r2.body["eventId"] != "OPS-DG-20261127-01" {
		t.Fatalf("重复进入 %s", r2.raw)
	}
	types := e.outboxTypes()
	if count(types, "ops.degrade.entered") != 1 || count(types, "ops.alert.raised") != 1 || count(types, "ops.audit.recorded") != 1 {
		t.Fatalf("发件表 %v", types)
	}
	entered := e.outboxPayloads("ops.degrade.entered")[0]
	if entered["_key"] != "degrade" || entered["limitsVersionId"] != "PR0002" {
		t.Fatalf("降级已进入 %v", entered)
	}
	comps := entered["components"].([]any)
	if len(comps) != 1 || comps[0] != "实名核验" {
		t.Fatalf("事件里的故障组件按 events/ops.md 取值 %v", comps)
	}
	audit := e.outboxPayloads("ops.audit.recorded")[0]
	if audit["action"] != "进入降级" || audit["targetType"] != "degrade" || audit["targetId"] != "OPS-DG-20261127-01" || audit["reason"] != "第三方核验接口连续超时" {
		t.Fatalf("留痕 %v", audit)
	}
	alert := e.outboxPayloads("ops.alert.raised")[0]
	if alert["level"] != "严重" || alert["type"] != "降级进入" || alert["degradeEventId"] != "OPS-DG-20261127-01" {
		t.Fatalf("告警 %v", alert)
	}

	pub := e.do("GET", "/api/ops/degrade/public", "", nil, "")
	if pub.status != 200 || pub.body["active"] != true || pub.body["eventId"] != "OPS-DG-20261127-01" {
		t.Fatalf("公开状态 %s", pub.raw)
	}

	e.setNow("2026-11-27 21:47:19")
	x := e.do("POST", "/api/ops/degrade/exit", duty, map[string]any{"reason": "核验服务已恢复"}, "")
	if x.status != 200 || x.body["status"] != "已退出" || x.body["durationMinutes"] != float64(41) || x.body["exitedBy"] != "AC0401" {
		t.Fatalf("退出 %s", x.raw)
	}
	exited := e.outboxPayloads("ops.degrade.exited")
	if len(exited) != 1 || exited[0]["durationMinutes"] != float64(41) {
		t.Fatalf("降级已退出 %v", exited)
	}
	if p := e.do("GET", "/api/ops/degrade/public", "", nil, ""); p.body["active"] != false || p.body["eventId"] != nil {
		t.Fatalf("退出后公开状态 %s", p.raw)
	}
	st := e.do("GET", "/api/ops/degrade/status", duty, nil, "")
	if st.body["degraded"] != false || st.body["lastDegradeEventId"] != "OPS-DG-20261127-01" || st.body["event"] != nil || st.body["minutes"] != float64(0) {
		t.Fatalf("退出后状态 %s", st.raw)
	}
	// 再次进入：同日序号递增
	if r := e.do("POST", "/api/ops/degrade/enter", duty, map[string]any{"preset": "授权不可用"}, ""); r.body["eventId"] != "OPS-DG-20261127-02" {
		t.Fatalf("编号 %s", r.raw)
	}
	list := e.do("GET", "/api/ops/degrade/events", duty, nil, "")
	if len(list.arr) != 2 || list.arr[0].(map[string]any)["eventId"] != "OPS-DG-20261127-02" {
		t.Fatalf("事件列表新到旧 %s", list.raw)
	}
	if r := e.do("GET", "/api/ops/degrade/events/OPS-DG-NOPE", duty, nil, ""); r.status != 404 {
		t.Fatalf("不存在的事件 %d", r.status)
	}
}

// 2026-11-12 样例：08:31 值班人收紧；停止、放宽；没有值班人操作不发收紧与停止。
func TestTightenStopRelax(t *testing.T) {
	e := newEnv(t, "2026-11-12 07:58:12")
	e.seedRules()
	duty := e.duty()
	if r := e.do("POST", "/api/ops/degrade/tighten", duty, map[string]any{"reason": "降级已超过三十分钟"}, ""); r.code() != "DEGRADE_NOT_ACTIVE" {
		t.Fatalf("不在降级时收紧 %s", r.raw)
	}
	e.do("POST", "/api/ops/degrade/enter", duty, map[string]any{"cause": "支付通道故障（支付宝扣款接口返回系统繁忙）", "components": []string{"支付宝收款"}}, "")
	if c := count(e.outboxTypes(), "ops.degrade.tightened") + count(e.outboxTypes(), "ops.degrade.stopped"); c != 0 {
		t.Fatal("没有值班人操作，不发收紧与停止")
	}
	if r := e.do("POST", "/api/ops/degrade/tighten", duty, map[string]any{}, ""); r.code() != "REASON_REQUIRED" {
		t.Fatalf("收紧要事由 %s", r.raw)
	}
	e.setNow("2026-11-12 08:31:05")
	r := e.do("POST", "/api/ops/degrade/tighten", duty, map[string]any{"reason": "降级已超过 30 分钟"}, "")
	if r.status != 200 || r.body["tightened"] != true || r.body["stopped"] != false || r.body["minutes"] != float64(32) {
		t.Fatalf("收紧 %s", r.raw)
	}
	r = e.do("POST", "/api/ops/degrade/stop", duty, map[string]any{"reason": "限额接近，停止开新单"}, "")
	if r.body["stopped"] != true || r.body["tightened"] != true {
		t.Fatalf("停止 %s", r.raw)
	}
	r = e.do("POST", "/api/ops/degrade/relax", duty, map[string]any{"reason": "支付宝已恢复大部分"}, "")
	if r.body["stopped"] != false || r.body["tightened"] != false {
		t.Fatalf("放宽 %s", r.raw)
	}
	tg := e.outboxPayloads("ops.degrade.tightened")
	if len(tg) != 1 || tg[0]["eventId"] != "OPS-DG-20261112-01" || tg[0]["reason"] != "降级已超过 30 分钟" || tg[0]["_key"] != "degrade" {
		t.Fatalf("已收紧 %v", tg)
	}
	if len(e.outboxPayloads("ops.degrade.stopped")) != 1 {
		t.Fatal("已停止")
	}
	rl := e.outboxPayloads("ops.degrade.relaxed")
	if len(rl) != 1 || rl[0]["to"] != "正常" {
		t.Fatalf("已放宽 %v", rl)
	}
	ev := r.body["event"].(map[string]any)
	var actions []string
	for _, x := range ev["timeline"].([]any) {
		actions = append(actions, x.(map[string]any)["action"].(string))
	}
	if len(actions) != 4 || actions[1] != "降级收紧" || actions[2] != "降级停止" || actions[3] != "降级放宽" {
		t.Fatalf("时间线 %v", actions)
	}
	if p := e.do("GET", "/api/ops/degrade/public", "", nil, ""); p.body["tightened"] != false {
		t.Fatalf("公开状态 %s", p.raw)
	}
	// 退出时收紧与停止一并清除
	e.do("POST", "/api/ops/degrade/tighten", duty, map[string]any{"reason": "再次收紧一下"}, "")
	e.do("POST", "/api/ops/degrade/exit", duty, nil, "")
	var tightened bool
	_ = testPool.QueryRow(e.ctx(), `SELECT tightened FROM degrade_events WHERE event_id = 'OPS-DG-20261112-01'`).Scan(&tightened)
	if tightened {
		t.Fatal("退出后不再收紧")
	}
}

// 智能体未接入：收到依赖熔断只告警并通知值班人，不自动进入降级；进入时把熔断记为"检测到故障"，并收编此前的降级单。
func TestDependencyTripOnlyAlerts(t *testing.T) {
	e := newEnv(t, "2026-10-18 19:38:26")
	e.seedRules()
	e.publish("ride.dependency.state_changed", map[string]any{"component": "支付", "state": "熔断", "at": e.nowTS(), "instance": "ride-1"})
	var n int
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FROM degrade_events`).Scan(&n)
	if n != 0 {
		t.Fatal("没有运行智能体时不得自动宣布降级")
	}
	al := e.do("GET", "/api/ops/alerts", e.duty(), nil, "")
	rows := al.body["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("告警 %s", al.raw)
	}
	a0 := rows[0].(map[string]any)
	if a0["level"] != "严重" || a0["type"] != "依赖熔断" || len(a0["notified"].([]any)) != 2 {
		t.Fatalf("熔断告警并通知值班人 %v", a0)
	}
	// ride 已自主开出降级单（尚无事件编号）
	e.publish("ride.order.created", e.head("OD2026101813001", "U100001", "BR01", "2026-10-18 19:39:00", degraded(nil, "老用户")))
	e.setNow("2026-10-18 19:41:26")
	r := e.do("POST", "/api/ops/degrade/enter", e.duty(), map[string]any{"preset": "支付通道故障"}, "")
	tl := r.body["timeline"].([]any)
	if tl[0].(map[string]any)["action"] != "检测到故障" || tl[0].(map[string]any)["byKind"] != "系统" {
		t.Fatalf("检测到故障 %v", tl)
	}
	if r.body["orderCount"] != float64(1) {
		t.Fatalf("宣布前的降级单并入事件 %s", r.raw)
	}
	// 值班人确认进入告警 → 时间线"值班人确认"
	var alertID string
	_ = testPool.QueryRow(e.ctx(), `SELECT alert_id FROM alerts WHERE type = '降级进入'`).Scan(&alertID)
	if a := e.do("POST", "/api/ops/alerts/"+alertID+"/ack", e.duty(), nil, ""); a.status != 200 {
		t.Fatalf("确认 %s", a.raw)
	}
	d := e.do("GET", "/api/ops/degrade/events/"+r.body["eventId"].(string), e.duty(), nil, "")
	last := d.body["timeline"].([]any)
	if last[len(last)-1].(map[string]any)["action"] != "值班人确认" {
		t.Fatalf("值班人确认 %v", last)
	}
	e.publish("ride.dependency.state_changed", map[string]any{"component": "支付", "state": "恢复", "at": e.nowTS(), "instance": "ride-1"})
	var still int
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FROM degrade_events WHERE status = '进行中'`).Scan(&still)
	if still != 1 {
		t.Fatal("依赖恢复只告警，由值班人手动退出")
	}
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FROM alerts WHERE type = '依赖恢复' AND level = '提示'`).Scan(&n)
	if n != 1 {
		t.Fatal("依赖恢复提示")
	}
}

// 限额使用量：近 1 小时单量、累计单数、骑行中、累计金额（应收 + 骑行中估算）、超限单数；接近或达到限额提醒。
func TestUsageAndLimitAlerts(t *testing.T) {
	e := newEnv(t, "2026-11-30 10:00:00")
	e.seedRules()
	// 把 PR0002 的近 1 小时上限调小，便于触发（另发一版，10-16 生效）
	e.publish("rule.version.published", map[string]any{"versionId": "PR0099", "effectiveAt": e.ts("2026-10-16 00:00:00"),
		"ride": map[string]any{"startFee": 150, "startMinutes": 3, "perMinute": 50},
		"riskLimits": map[string]any{"perUserOpenDegradedOrders": 1, "normal": map[string]any{"maxMinutes": 120, "maxAmount": 6000},
			"tight": map[string]any{"maxMinutes": 45, "maxAmount": 2000}, "hourlyDegradedOrders": 5, "degradeDurationAlertMinutes": 30}})
	duty := e.duty()
	e.do("POST", "/api/ops/degrade/enter", duty, map[string]any{"preset": "支付通道故障"}, "")
	for i, u := range []string{"U1", "U2", "U3", "U4"} {
		id := "OD202611300000" + string(rune('1'+i))
		h := e.head(id, u, "BR01", "2026-11-30 09:5"+string(rune('0'+i))+":00", degraded(nil, "老用户"))
		h["pricingVersionId"] = "PR0099"
		e.publish("ride.order.created", h)
	}
	types := e.outboxTypes()
	_ = types
	var near, reach int
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FILTER (WHERE type='接近限额'), count(*) FILTER (WHERE type='达到限额') FROM alerts`).Scan(&near, &reach)
	if near != 1 || reach != 0 {
		t.Fatalf("4/5 应提醒接近限额：near=%d reach=%d", near, reach)
	}
	// 两笔开锁：一笔骑行中 13 分钟（估算 150 + 10×50 = 650），一笔已出账应收 450
	e.publish("ride.order.unlocked", merge(e.head("OD2026113000001", "U1", "BR01", "2026-11-30 09:50:00", degraded(nil, "老用户")), "startAt", e.ts("2026-11-30 09:47:00"), "pricingVersionId", "PR0099"))
	e.publish("ride.order.unlocked", merge(e.head("OD2026113000002", "U2", "BR01", "2026-11-30 09:51:00", degraded(nil, "老用户")), "startAt", e.ts("2026-11-30 09:51:30")))
	e.publish("ride.order.ended", merge(e.head("OD2026113000002", "U2", "BR01", "2026-11-30 09:51:00", degraded(nil, "老用户")), "startAt", e.ts("2026-11-30 09:51:30"), "endAt", e.ts("2026-11-30 09:58:00")))
	e.publish("bill.bill.issued", map[string]any{"orderId": "OD2026113000002", "userId": "U2", "brandId": "BR01", "operatorId": "OP01", "receivable": 450,
		"collectBy": "免押自动扣款", "billPushedAt": e.ts("2026-11-30 09:58:05"), "orderEndAt": e.ts("2026-11-30 09:58:00"), "degraded": true})
	e.publish("ride.order.over_limit_reminded", map[string]any{"orderId": "OD2026113000001", "userId": "U1", "limitMinutes": 120, "limitAmount": 6000, "remindedAt": e.nowTS()})
	st := e.do("GET", "/api/ops/degrade/status", duty, nil, "")
	u := st.body["usage"].(map[string]any)
	if u["lastHourOrders"] != float64(4) || u["hourlyLimit"] != float64(5) || u["orders"] != float64(4) || u["riding"] != float64(1) ||
		u["amountSoFar"] != float64(450+650) || u["overLimitRiding"] != float64(1) {
		t.Fatalf("限额使用量 %v", u)
	}
	if st.body["limitsVersionId"] != "PR0099" {
		t.Fatalf("限额版本 %v", st.body["limitsVersionId"])
	}
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FROM alerts WHERE type = '降级单超限' AND degrade_event_id IS NOT NULL`).Scan(&near)
	if near != 1 {
		t.Fatal("降级单超限通知值班人")
	}
	e.publish("ride.order.created", e.head("OD2026113000009", "U9", "BR01", "2026-11-30 09:59:30", degraded(nil, "先骑后验新用户")))
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FROM alerts WHERE type = '达到限额' AND level = '严重'`).Scan(&reach)
	if reach != 1 {
		t.Fatal("达到限额提醒")
	}
	// 降级持续 30 分钟提醒（定时任务）
	e.advance(31 * time.Minute)
	if err := e.a.CheckLimits(e.ctx()); err != nil {
		t.Fatal(err)
	}
	if err := e.a.CheckLimits(e.ctx()); err != nil {
		t.Fatal(err)
	}
	var dur int
	_ = testPool.QueryRow(e.ctx(), `SELECT count(*) FROM alerts WHERE type = '降级持续超阈值'`).Scan(&dur)
	if dur != 1 {
		t.Fatalf("降级持续超阈值只提醒一次，得 %d", dur)
	}
	var maxHourly, maxUser int
	_ = testPool.QueryRow(e.ctx(), `SELECT max_hourly_orders, max_user_open_orders FROM degrade_events`).Scan(&maxHourly, &maxUser)
	if maxHourly != 5 || maxUser != 1 {
		t.Fatalf("期间最大值 %d %d", maxHourly, maxUser)
	}
	// 仍未收紧：提醒不等于执行
	if len(e.outboxPayloads("ops.degrade.tightened")) != 0 {
		t.Fatal("达到限额只提醒，不自动收紧")
	}
}

func TestUnifiedComponents(t *testing.T) {
	e := newEnv(t, "2026-11-27 14:00:00")
	e.seedRules()
	duty := e.duty()
	r := e.do("POST", "/api/ops/degrade/enter", duty, map[string]any{"cause": "多组件故障", "components": []string{"数据库", "计费", "支付宝收款", "微信支付收款", "微信授权"}}, "")
	if r.status != 200 {
		t.Fatalf("%s", r.raw)
	}
	want := []any{"数据库", "计费", "支付通道", "授权"}
	got := r.body["components"].([]any)
	if len(got) != len(want) {
		t.Fatalf("接口返回统一取值并去重 %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("接口返回统一取值 %v", got)
		}
	}
	ev := e.outboxPayloads("ops.degrade.entered")[0]["components"].([]any)
	for i := range want {
		if ev[i] != want[i] {
			t.Fatalf("事件取统一集合 %v", ev)
		}
	}
	e.do("POST", "/api/ops/degrade/exit", duty, map[string]any{"reason": "全部恢复正常"}, "")
	r = e.do("POST", "/api/ops/degrade/enter", duty, map[string]any{"preset": "授权不可用"}, "")
	if c := r.body["components"].([]any); len(c) != 1 || c[0] != "授权" {
		t.Fatalf("预设用统一取值 %v", c)
	}
}

func TestMigrationFoldsLegacyComponents(t *testing.T) {
	e := newEnv(t, "2026-11-27 14:00:00")
	sqlText, err := os.ReadFile("../../migrations/00002_components_unified.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(strings.Split(string(sqlText), "-- +goose Down")[0], "-- +goose Up")[1]
	_, err = testPool.Exec(e.ctx(), `INSERT INTO degrade_events (event_id, cause, components, entered_at, entered_by, status, limits_version_id, limits)
		VALUES ('OPS-DG-OLD', 'x', ARRAY['微信支付收款','支付宝收款','实名核验','微信授权'], now(), 'AC1', '进行中', 'PR1', '{}')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(e.ctx(), up); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := testPool.QueryRow(e.ctx(), `SELECT components FROM degrade_events WHERE event_id = 'OPS-DG-OLD'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "支付通道,实名核验,授权" {
		t.Fatalf("旧名折算 %v", got)
	}
}

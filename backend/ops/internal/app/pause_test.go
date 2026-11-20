package app

import (
	"fmt"
	"testing"
)

var agent = Actor{Kind: "智能体", ID: "AC0901", Name: "运行智能体"}

func TestManualPauseResume(t *testing.T) {
	e := newEnv(t, "2026-11-30 08:00:00")
	e.seedRules()
	e.seedBrands()
	duty := e.duty()
	if r := e.do("POST", "/api/ops/brands/BR02/pause", duty, map[string]any{"reason": "短"}, ""); r.code() != "REASON_REQUIRED" {
		t.Fatalf("%s", r.raw)
	}
	r := e.do("POST", "/api/ops/brands/BR02/pause", duty, map[string]any{"reason": "运营商反馈通信模组故障"}, "p-1")
	if r.status != 200 || r.body["id"] != "OPS-PS-20261130-01" || r.body["metric"] != "人工" || r.body["resumedAt"] != nil || r.body["by"] != "AC0401" {
		t.Fatalf("暂停 %s", r.raw)
	}
	if e.si.brands["BR02"] != "暂停可租" {
		t.Fatal("调 fleet 暂停")
	}
	if r := e.do("POST", "/api/ops/brands/BR02/pause", duty, map[string]any{"reason": "运营商反馈通信模组故障"}, ""); r.status != 409 || r.code() != "CONFLICT" {
		t.Fatalf("已暂停再暂停 %s", r.raw)
	}
	if r := e.do("POST", "/api/ops/brands/BR77/pause", duty, map[string]any{"reason": "不存在的品牌"}, ""); r.status != 404 {
		t.Fatalf("不存在 %s", r.raw)
	}
	e.si.failFleet = true
	if r := e.do("POST", "/api/ops/brands/BR01/pause", duty, map[string]any{"reason": "fleet 故障时"}, ""); r.code() != "SERVER_BUSY" {
		t.Fatalf("fleet 不可用 %s", r.raw)
	}
	e.si.failFleet = false
	audits := e.outboxPayloads("ops.audit.recorded")
	if len(audits) != 1 || audits[0]["action"] != "暂停可租" || audits[0]["targetId"] != "BR02" {
		t.Fatalf("留痕 %v", audits)
	}
	e.setNow("2026-11-30 12:40:00")
	x := e.do("POST", "/api/ops/brands/BR02/resume", duty, map[string]any{"reason": "固件已修复"}, "")
	if x.status != 200 || x.body["id"] != "OPS-PS-20261130-01" || x.body["resumedAt"] != e.ts("2026-11-30 12:40:00") {
		t.Fatalf("恢复 %s", x.raw)
	}
	if r := e.do("POST", "/api/ops/brands/BR02/resume", duty, map[string]any{"reason": "再恢复一次"}, ""); r.code() != "CONFLICT" {
		t.Fatalf("未暂停时恢复 %s", r.raw)
	}
	// 运营商只看本家暂停记录
	e.do("POST", "/api/ops/brands/BR01/pause", duty, map[string]any{"reason": "青禾车辆检修中"}, "")
	op := e.token("AC0302", []string{"access.view.own"}, "OP02", false)
	ps := e.do("GET", "/api/ops/pauses", op, nil, "")
	if len(ps.arr) != 1 || ps.arr[0].(map[string]any)["brandId"] != "BR02" {
		t.Fatalf("运营商范围 %s", ps.raw)
	}
	if all := e.do("GET", "/api/ops/pauses", duty, nil, ""); len(all.arr) != 2 || all.arr[0].(map[string]any)["brandId"] != "BR01" {
		t.Fatalf("新到旧 %s", all.raw)
	}
	var types []string
	for _, a := range e.outboxPayloads("ops.alert.raised") {
		types = append(types, a["type"].(string))
	}
	if count(types, "暂停可租") != 2 || count(types, "恢复可租") != 1 {
		t.Fatalf("告警 %v", types)
	}
}

func TestSuggestionsAndAgentTools(t *testing.T) {
	e := newEnv(t, "2026-12-01 08:00:00")
	e.seedRules()
	e.seedBrands()
	buildHistory(e)
	e.setNow("2026-12-01 10:50:00")
	_ = e.a.RefreshMetrics(e.ctx())
	tools := e.a.Agent()
	duty := e.duty()

	if _, err := tools.ProposeSuspend(e.ctx(), Actor{Kind: "人", ID: "AC0401"}, "BR02", "人不能用智能体工具"); err == nil {
		t.Fatal("只有智能体能用工具")
	}
	if _, err := tools.ProposeSuspend(e.ctx(), agent, "BR01", "青禾开锁成功率正常"); err == nil {
		t.Fatal("指标没有持续低于门槛时拒绝")
	}
	sg, err := tools.ProposeSuspend(e.ctx(), agent, "BR02", "仅木棉低于门槛，同时段其他品牌正常，判断为品牌自身问题")
	if err != nil {
		t.Fatal(err)
	}
	if sg.ID != "OPS-SG-20261201-01" || sg.Status != "待确认" || sg.Metric != "开锁成功率" || sg.Value != 0.8571 || sg.Threshold != 0.9 || sg.SustainMinutes != 170 {
		t.Fatalf("建议的数值由代码填入 %+v", sg)
	}
	if _, err := tools.ProposeSuspend(e.ctx(), agent, "BR02", "重复提出一次"); err == nil {
		t.Fatal("已有待确认建议")
	}
	ev := e.outboxPayloads("ops.brand.suspend_suggested")
	if len(ev) != 1 || ev[0]["_key"] != "BR02" || ev[0]["suggestionId"] != sg.ID {
		t.Fatalf("暂停可租建议事件 %v", ev)
	}
	if len(e.si.calls) != 0 {
		t.Fatal("开关关闭时只建议不暂停")
	}
	list := e.do("GET", "/api/ops/suspend-suggestions?status=待确认", duty, nil, "")
	if len(list.arr) != 1 {
		t.Fatalf("建议列表 %s", list.raw)
	}
	if td := e.do("GET", "/api/ops/todos", duty, nil, ""); len(td.arr) != 2 { // 未确认告警 + 待确认建议
		t.Fatalf("待办 %s", td.raw)
	}
	// 驳回
	d := e.do("POST", "/api/ops/suspend-suggestions/"+sg.ID+"/dismiss", duty, map[string]any{"reason": "运营商已在处理，先观察"}, "")
	if d.body["status"] != "已驳回" || d.body["decidedBy"] != "AC0401" {
		t.Fatalf("驳回 %s", d.raw)
	}
	if r := e.do("POST", "/api/ops/suspend-suggestions/"+sg.ID+"/confirm", duty, map[string]any{"reason": "改主意了要暂停"}, ""); r.code() != "ALREADY_DONE" {
		t.Fatalf("已驳回不能确认 %s", r.raw)
	}
	if r := e.do("POST", "/api/ops/suspend-suggestions/OPS-SG-X/confirm", duty, map[string]any{"reason": "不存在的建议"}, ""); r.status != 404 {
		t.Fatalf("%s", r.raw)
	}
	// 再提、确认
	sg2, err := tools.ProposeSuspend(e.ctx(), agent, "BR02", "仍仅木棉低于门槛，判断为品牌自身问题")
	if err != nil {
		t.Fatal(err)
	}
	c := e.do("POST", "/api/ops/suspend-suggestions/"+sg2.ID+"/confirm", duty, map[string]any{"reason": "确认暂停木棉"}, "")
	if c.status != 200 || c.body["status"] != "已确认" || c.body["pauseId"] != "OPS-PS-20261201-01" {
		t.Fatalf("确认 %s", c.raw)
	}
	if e.si.brands["BR02"] != "暂停可租" {
		t.Fatal("确认后调 fleet 暂停")
	}
	ps := e.do("GET", "/api/ops/pauses", duty, nil, "")
	p0 := ps.arr[0].(map[string]any)
	if p0["metric"] != "开锁成功率" || p0["value"] != 0.8571 || p0["threshold"] != 0.9 || p0["sustainMinutes"] != float64(170) {
		t.Fatalf("暂停记录 %v", p0)
	}
	// 暂停期间没有订单：开锁成功率为空，不据此自动恢复
	e.setNow("2026-12-01 12:30:00")
	_ = e.a.RefreshMetrics(e.ctx())
	_ = e.a.CheckBrandHealth(e.ctx())
	if e.si.brands["BR02"] != "暂停可租" {
		t.Fatal("没有开锁数据时不自动恢复")
	}
	// 例外条款：因开锁成功率暂停的品牌，指标回升后也不自动恢复，只提示人工恢复
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("OD2026120199%03d", i)
		req := fmt.Sprintf("2026-12-01 12:%02d:00", i*5)
		h := e.head(id, fmt.Sprintf("U9%05d", i), "BR02", req, nil)
		e.publish("ride.order.created", h)
		e.publish("ride.order.unlocked", merge(h, "startAt", e.ts(fmt.Sprintf("2026-12-01 12:%02d:03", i*5))))
	}
	e.setNow("2026-12-01 12:59:00")
	_ = e.a.RefreshMetrics(e.ctx())
	for i := 0; i < 2; i++ {
		if err := e.a.CheckBrandHealth(e.ctx()); err != nil {
			t.Fatal(err)
		}
	}
	if e.si.brands["BR02"] != "暂停可租" {
		t.Fatal("因开锁成功率暂停的品牌不得自动恢复")
	}
	ps2 := e.do("GET", "/api/ops/pauses", duty, nil, "")
	if p := ps2.arr[0].(map[string]any); p["autoResume"] != false || p["resumedAt"] != nil {
		t.Fatalf("autoResume 应为 false %v", p)
	}
	var hint int
	for _, a := range e.outboxPayloads("ops.alert.raised") {
		if a["type"] == "品牌指标已恢复" {
			hint++
		}
	}
	if hint != 1 {
		t.Fatalf("指标回升应提示人工恢复且只提示一次，得 %d", hint)
	}
	e.do("POST", "/api/ops/brands/BR02/resume", duty, map[string]any{"reason": "确认已恢复，人工恢复"}, "")
	if e.si.brands["BR02"] != "已接入" {
		t.Fatal("人工恢复")
	}
}

func TestSuggestEventAutoResumeFlag(t *testing.T) {
	e := newEnv(t, "2026-12-01 08:00:00")
	e.seedRules()
	e.seedBrands()
	buildHistory(e)
	e.setNow("2026-12-01 10:50:00")
	_ = e.a.RefreshMetrics(e.ctx())
	if _, err := e.a.Agent().ProposeSuspend(e.ctx(), agent, "BR02", "仅木棉低于门槛，判断为品牌自身问题"); err != nil {
		t.Fatal(err)
	}
	ev := e.outboxPayloads("ops.brand.suspend_suggested")
	if len(ev) != 1 || ev[0]["autoResume"] != false {
		t.Fatalf("开锁成功率建议的 autoResume 固定 false：%v", ev)
	}
}

func TestCallerActorOnlyPersonOrSystem(t *testing.T) {
	e := newEnv(t, "2026-12-01 08:00:00")
	e.seedRules()
	e.seedBrands()
	duty := e.duty()
	e.do("POST", "/api/ops/brands/BR02/pause", duty, map[string]any{"reason": "运营商反馈通信模组故障"}, "")
	e.do("POST", "/api/ops/brands/BR02/resume", duty, map[string]any{"reason": "固件已修复"}, "")
	// 运行智能体自动暂停：调 fleet 的 actor 只能是 人 或 系统
	e.do("PUT", "/api/ops/auto-suspend", duty, map[string]any{"on": true, "reason": "夜间无人值守试行"}, "")
	e.publish("opgw.operator.unreachable", map[string]any{"operatorId": "OP03", "brandIds": []string{"BR03"}, "since": e.ts("2026-12-01 08:00:00"), "lastError": "连接超时"})
	for _, at := range []string{"2026-12-01 08:59:59", "2026-12-01 09:59:59", "2026-12-01 10:30:00"} {
		e.setNow(at)
		_ = e.a.RefreshMetrics(e.ctx())
	}
	if _, err := e.a.Agent().ProposeSuspend(e.ctx(), agent, "BR03", "仅简途运营商通道不可达，平台与其他运营商正常"); err != nil {
		t.Fatal(err)
	}
	want := []string{"pause:人", "resume:人", "pause:系统"}
	if len(e.si.actors) != 3 || e.si.actors[0] != want[0] || e.si.actors[1] != want[1] || e.si.actors[2] != want[2] {
		t.Fatalf("actor.kind %v，期望 %v", e.si.actors, want)
	}
	// 事件信封里仍如实记录智能体
	if sg := e.outboxPayloads("ops.audit.recorded"); sg[len(sg)-1]["_actor"].(Actor).Kind != "智能体" {
		t.Fatalf("留痕里的操作人 %v", sg[len(sg)-1]["_actor"])
	}
}

func TestAutoSuspendSwitchAndAutoResume(t *testing.T) {
	e := newEnv(t, "2026-12-01 08:00:00")
	e.seedRules()
	e.seedBrands()
	duty := e.duty()
	if r := e.do("PUT", "/api/ops/auto-suspend", duty, map[string]any{"on": true}, ""); r.code() != "REASON_REQUIRED" {
		t.Fatalf("%s", r.raw)
	}
	if r := e.do("PUT", "/api/ops/auto-suspend", duty, map[string]any{"reason": "缺开关值"}, ""); r.code() != "INVALID" {
		t.Fatalf("%s", r.raw)
	}
	r := e.do("PUT", "/api/ops/auto-suspend", duty, map[string]any{"on": true, "reason": "夜间无人值守试行"}, "")
	if r.status != 200 || r.body["autoPauseByAgent"] != true {
		t.Fatalf("开关 %s", r.raw)
	}
	a := e.outboxPayloads("ops.audit.recorded")
	if len(a) != 1 || a[0]["before"].(map[string]any)["autoPauseByAgent"] != false || a[0]["after"].(map[string]any)["autoPauseByAgent"] != true {
		t.Fatalf("开关留痕 %v", a)
	}
	// 简途通道不可达 3 小时（可用率 0），运行智能体判定品牌自身问题 → 开关开启直接暂停
	e.publish("opgw.operator.unreachable", map[string]any{"operatorId": "OP03", "brandIds": []string{"BR03"}, "since": e.ts("2026-12-01 08:00:00"), "lastError": "连接超时"})
	for _, at := range []string{"2026-12-01 08:59:59", "2026-12-01 09:59:59", "2026-12-01 10:30:00"} {
		e.setNow(at)
		_ = e.a.RefreshMetrics(e.ctx())
	}
	sg, err := e.a.Agent().ProposeSuspend(e.ctx(), agent, "BR03", "仅简途运营商通道不可达，平台与其他运营商正常")
	if err != nil {
		t.Fatal(err)
	}
	if sg.Status != "已确认" || sg.Metric != "可用率" || sg.PauseID == nil || sg.DecidedBy == nil || *sg.DecidedBy != "AC0901" {
		t.Fatalf("自动暂停 %+v", sg)
	}
	if e.si.brands["BR03"] != "暂停可租" {
		t.Fatal("自动暂停调 fleet")
	}
	// 通道恢复，新的一小时指标回到门槛以上 → 自动恢复可租
	e.publish("opgw.operator.recovered", map[string]any{"operatorId": "OP03", "brandIds": []string{"BR03"}, "recoveredAt": e.ts("2026-12-01 10:40:00")})
	e.setNow("2026-12-01 11:59:00")
	_ = e.a.RefreshMetrics(e.ctx())
	if err := e.a.CheckBrandHealth(e.ctx()); err != nil {
		t.Fatal(err)
	}
	if e.si.brands["BR03"] != "已接入" {
		t.Fatal("可用率恢复后调 fleet 恢复可租")
	}
	ps := e.do("GET", "/api/ops/pauses", duty, nil, "")
	if ps.arr[0].(map[string]any)["resumedAt"] == nil {
		t.Fatalf("补记恢复时刻 %s", ps.raw)
	}
	// 智能体宣布降级：同一状态机，操作人为智能体
	ev, err := e.a.Agent().DeclareDegrade(e.ctx(), agent, EnterRequest{Preset: "支付通道故障"})
	if err != nil || ev.EnteredBy != "AC0901" || ev.Timeline[0].ByKind != "智能体" {
		t.Fatalf("智能体宣布 %+v %v", ev, err)
	}
	if _, err := e.a.Agent().DeclareDegrade(e.ctx(), Actor{Kind: "人", ID: "x"}, EnterRequest{Preset: "支付通道故障"}); err == nil {
		t.Fatal("人不能用智能体工具")
	}
	if _, err := e.a.Agent().DeclareRecovery(e.ctx(), agent, "支付通道恢复正常"); err != nil {
		t.Fatal(err)
	}
}

func TestAlertsListAndAck(t *testing.T) {
	e := newEnv(t, "2026-12-01 08:00:00")
	e.seedRules()
	duty := e.duty()
	e.publish("ride.dependency.state_changed", map[string]any{"component": "支付", "state": "熔断", "at": e.nowTS(), "instance": "ride-1"})
	e.advance(60e9)
	e.publish("opgw.operator.unreachable", map[string]any{"operatorId": "OP02", "brandIds": []string{"BR02"}, "since": e.nowTS(), "lastError": "超时"})
	e.advance(60e9)
	e.publish("ride.dependency.state_changed", map[string]any{"component": "支付", "state": "恢复", "at": e.nowTS(), "instance": "ride-1"})
	all := e.do("GET", "/api/ops/alerts", duty, nil, "")
	rows := all.body["rows"].([]any)
	if all.body["total"] != float64(3) || rows[0].(map[string]any)["type"] != "依赖恢复" {
		t.Fatalf("新到旧 %s", all.raw)
	}
	byLevel := e.do("GET", "/api/ops/alerts?sortKey=level&sortDir=desc&pageSize=1&page=1", duty, nil, "")
	if byLevel.body["rows"].([]any)[0].(map[string]any)["level"] != "严重" || len(byLevel.body["rows"].([]any)) != 1 || byLevel.body["pageSize"] != float64(1) {
		t.Fatalf("按级别 %s", byLevel.raw)
	}
	id := rows[1].(map[string]any)["alertId"].(string)
	ack := e.do("POST", "/api/ops/alerts/"+id+"/ack", duty, nil, "")
	if ack.body["ackBy"] != "AC0401" || ack.body["ackAt"] == nil {
		t.Fatalf("确认 %s", ack.raw)
	}
	if r := e.do("POST", "/api/ops/alerts/"+id+"/ack", duty, nil, ""); r.code() != "ALREADY_DONE" {
		t.Fatalf("重复确认 %s", r.raw)
	}
	if r := e.do("POST", "/api/ops/alerts/OPS-AL-X/ack", duty, nil, ""); r.status != 404 {
		t.Fatalf("%s", r.raw)
	}
	un := e.do("GET", "/api/ops/alerts?acked=false", duty, nil, "")
	if un.body["total"] != float64(2) {
		t.Fatalf("未确认 %s", un.raw)
	}
	if r := e.do("GET", "/api/ops/alerts?level=紧急", duty, nil, ""); r.code() != "INVALID" {
		t.Fatalf("级别枚举 %s", r.raw)
	}
	td := e.do("GET", "/api/ops/todos", duty, nil, "")
	if len(td.arr) != 1 || td.arr[0].(map[string]any)["count"] != float64(2) {
		t.Fatalf("待办 %s", td.raw)
	}
	// 只有闭合权限的人看不到告警待办
	cl := e.token("AC0403", []string{"degrade.closure.view"}, nil, false)
	if td := e.do("GET", "/api/ops/todos", cl, nil, ""); len(td.arr) != 0 {
		t.Fatalf("按权限 %s", td.raw)
	}
}

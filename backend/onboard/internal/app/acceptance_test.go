package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

// trace: 任务 10、11 · R-40 R-41 BR-37

func listVehicle(e *testEnv, vid, plate, code string) {
	e.event("fleet.vehicle.listed", Actor{Kind: "人", ID: "AC0114"}, map[string]any{"vehicleId": vid, "brandId": "BR05", "operatorId": "OP04", "plateNo": plate,
		"govCode": code, "model": "市民 S1", "pileTagType": "RFID", "pileTag": "E28011" + vid, "codeStatus": "待启用", "codeEnableAt": nil, "listedAt": time.Now()})
}

// realRide 模拟用户实骑一辆真车：开锁、等开锁结果、还车、等锁车报告。
func (e *testEnv) realRide(plate string) string {
	e.t.Helper()
	st, m, raw := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/real-rides/rides", map[string]any{"plateNo": plate, "simUserId": "SBX-U001"})
	if st != 200 || m["unlockReply"] != "已受理" {
		e.t.Fatalf("发起真车骑行: %d %s", st, raw)
	}
	id := m["orderId"].(string)
	e.waitOrder(id, "开锁结果")
	if st, _, raw := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/real-rides/rides/"+id+"/end", map[string]any{}); st != 200 {
		e.t.Fatalf("还车: %d %s", st, raw)
	}
	e.waitOrder(id, "锁车")
	return id
}

func (e *testEnv) waitOrder(orderID, typ string) {
	e.t.Helper()
	if e.app.waitFor(context.Background(), orderID, typ, time.Now().Add(3*time.Second)) == nil {
		e.t.Fatalf("订单 %s 没等到 %s", orderID, typ)
	}
}

func TestRealRidesAndAcceptance(t *testing.T) {
	e, op := prepRun(t)
	_, _ = testPool.Exec(context.Background(), `UPDATE params SET real_vehicles=2, real_orders=3`)
	listVehicle(e, "VH001151", "SM000001", "SF27239795")
	listVehicle(e, "VH001152", "SM000002", "SF27239796")

	// 不变量：符合性测试未全部通过，不能做真车联调、不能提交
	if st, m, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/real-rides", nil); st != 409 || m["code"] != "ACCEPTANCE_NOT_READY" {
		t.Fatalf("未测试就提交: %d %v", st, m)
	}
	if run := e.startRun(op); run.Summary.Failed != 0 {
		t.Fatalf("前置测试应全部通过: %v", failedIDs(run))
	}
	if st, m, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/real-rides/rides", map[string]any{"plateNo": "SBX10001", "simUserId": "SBX-U001"}); st != 409 || m["code"] != "CONFLICT" {
		t.Fatalf("真车联调只能用清单里的车: %d %v", st, m)
	}
	e.realRide("SM000001")
	e.realRide("SM000002")
	st, m, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/real-rides", nil)
	if st != 409 || m["code"] != "ACCEPTANCE_NOT_READY" || !strings.Contains(m["message"].(string), "真车联调订单 2 单，参数要求 3 单") || m["realOrders"].(float64) != 2 {
		t.Fatalf("单数不够: %d %v", st, m)
	}
	e.realRide("SM000001")
	_, _, raw := e.call(techOP04, "GET", "/api/onboard/integrations/BR05/real-rides/rides", nil)
	if strings.Count(string(raw), `"lockReported":true`) != 3 {
		t.Fatalf("真车骑行记录: %s", raw)
	}

	st, _, raw = e.call(techOP04, "POST", "/api/onboard/integrations/BR05/real-rides", nil)
	if st != 200 {
		t.Fatalf("提交真车联调: %d %s", st, raw)
	}
	var in Integration
	mustJSON(t, raw, &in)
	ac := in.Acceptance
	if ac == nil || *ac.ReportID != "IN-BR05-AR01" || ac.Result != "待确认" || ac.RealVehiclesRidden != 2 || ac.RealOrders != 3 || ac.MatchedOrders != 3 ||
		ac.LockReportsMatched != 3 || !ac.TimeLimitOk || *ac.ConformanceRunID != "IN-BR05-R001" || *ac.SubmittedBy != "AC0114" || ac.Summary != nil {
		t.Fatalf("验收报告不对: %+v", ac)
	}
	if !contains(outboxTypes(t), "onboard.acceptance.submitted") {
		t.Fatal("应发验收报告已提交事件")
	}
	if st, m, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/real-rides", nil); st != 409 || m["code"] != "CONFLICT" {
		t.Fatalf("重复提交: %d %v", st, m)
	}

	// 待办
	_, _, raw = e.call(leadOps, "GET", "/api/onboard/todos", nil)
	if !strings.Contains(string(raw), `"count":1`) || !strings.Contains(string(raw), `"key":"acceptance"`) {
		t.Fatalf("待办: %s", raw)
	}

	// 接入智能体写摘要（工具后端），结论不变
	if err := e.app.AgentWriteReportSummary(context.Background(), "IN-BR05-AR01", "符合性测试 18 项全部通过；真车 2 辆、3 单全部匹配。"); err != nil {
		t.Fatal(err)
	}

	// 人确认点：只有接入负责人能确认
	if st, m, _ := e.call(techOP04, "POST", "/api/onboard/acceptance/IN-BR05/confirm", map[string]any{"reason": "我自己确认上线"}); st != 403 || m["code"] != "FORBIDDEN" {
		t.Fatalf("运营商不能确认: %d %v", st, m)
	}
	e.fleet.down = true
	if st, m, _ := e.call(leadOps, "POST", "/api/onboard/acceptance/IN-BR05/confirm", map[string]any{"reason": "验收报告核对无误"}); st != 500 || m["code"] != "SERVER_BUSY" {
		t.Fatalf("fleet 不可用: %d %v", st, m)
	}
	if e.integ(techOP04, "BR05").Acceptance.Result != "待确认" {
		t.Fatal("fleet 未上线时验收不得确认")
	}
	e.fleet.down = false
	st, _, raw = e.call(leadOps, "POST", "/api/onboard/acceptance/IN-BR05/confirm", map[string]any{"reason": "验收报告核对无误"})
	if st != 200 {
		t.Fatalf("确认: %d %s", st, raw)
	}
	mustJSON(t, raw, &in)
	if in.Status != "已接入" || in.Acceptance.Result != "通过" || *in.Acceptance.ConfirmedBy != "AC0403" || *in.Acceptance.Summary == "" {
		t.Fatalf("确认后: %+v %+v", in.Status, in.Acceptance)
	}
	if e.fleet.calls[len(e.fleet.calls)-1] != "/internal/fleet/brands/BR05/go-live" {
		t.Fatalf("应调 fleet 上线: %v", e.fleet.calls)
	}
	if !contains(outboxTypes(t), "onboard.acceptance.confirmed") || !contains(auditActions(t), "确认验收") || !contains(auditActions(t), "上线品牌") {
		t.Fatal("应发验收已确认事件并留痕")
	}
	if st, m, _ := e.call(leadOps, "POST", "/api/onboard/acceptance/IN-BR05/confirm", map[string]any{"reason": "再次确认验收"}); st != 409 || m["code"] != "ACCEPTANCE_NOT_READY" {
		t.Fatalf("重复确认: %d %v", st, m)
	}
	// fleet 随后发来的状态变更不重复记历史
	e.event("fleet.brand.access_changed", Actor{Kind: "人", ID: "AC0403"}, map[string]any{"brandId": "BR05", "from": "联调中", "to": "已接入", "changedAt": time.Now(), "reason": "验收通过"})
	if h := e.integ(techOP04, "BR05").StatusHistory; len(h) != 3 {
		t.Fatalf("状态历史应为 未接入→联调中→已接入: %+v", h)
	}
	if err := e.app.AgentWriteReportSummary(context.Background(), "IN-BR05-AR01", "确认后再改摘要"); err == nil {
		t.Fatal("确认后不能再改摘要")
	}

	// 运维列表
	_, _, raw = e.call(dutyOps, "GET", "/api/onboard/acceptance", nil)
	var rows []AcceptanceRow
	mustJSON(t, raw, &rows)
	if len(rows) != 1 || rows[0].ElapsedDays == nil || *rows[0].ElapsedDays != 0 || rows[0].LatestRun == nil {
		t.Fatalf("接入验收列表: %s", raw)
	}
	_, _, raw = e.call(leadOps, "GET", "/api/onboard/todos", nil)
	if string(raw) != "[]\n" {
		t.Fatalf("确认后没有待办: %s", raw)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// trace: 任务 12 的工具后端——只能给失败用例写建议
func TestAgentSuggestion(t *testing.T) {
	e, op := prepRun(t)
	op.normalDelay = 1200 * time.Millisecond
	run := e.startRun(op)
	ctx := context.Background()
	if err := e.app.AgentWriteSuggestion(ctx, run.RunID, "M01-01", "这一项不该有建议"); err == nil {
		t.Fatal("通过的用例不能写建议")
	}
	text := "规范允许先回\"已受理\"，车辆确认后再报结果；见接入规范第 3.2 节参考报文。"
	if err := e.app.AgentWriteSuggestion(ctx, run.RunID, "M03-01", text); err != nil {
		t.Fatal(err)
	}
	d, err := e.app.AgentRun(ctx, "", "BR05")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range d.Cases {
		if c.CaseID == "M03-01" {
			if *c.Suggestion != text || *c.SuggestedBy != "AC0903" || c.Facts["violations"] == nil {
				t.Fatalf("建议或事实不对: %+v", c)
			}
		}
	}
	if a := lastAudit(t); a["actor"].(map[string]any)["kind"] != "智能体" {
		t.Fatalf("智能体写入应以智能体身份留痕: %v", a)
	}
	_, _, raw := e.call(techOP04, "GET", "/api/onboard/runs", nil)
	if !strings.Contains(string(raw), `"suggestedBy":"AC0903"`) {
		t.Fatalf("页面应看到建议: %s", raw)
	}
}

package fleet

import (
	"testing"
)

var actorOnboard = map[string]any{"kind": "人", "id": "AC0403", "name": "毛＊＊"}
var actorAgent = map[string]any{"kind": "智能体", "id": "AC0901", "name": "运行智能体"}

func TestTransitionTable(t *testing.T) {
	// 迁移只有：未接入→联调中→已接入⇄暂停可租；任一状态→已退出
	all := []string{"未接入", "联调中", "已接入", "暂停可租", "已退出"}
	allowed := map[[2]string]bool{
		{"未接入", "联调中"}: true, {"联调中", "已接入"}: true, {"已接入", "暂停可租"}: true, {"暂停可租", "已接入"}: true,
		{"未接入", "已退出"}: true, {"联调中", "已退出"}: true, {"已接入", "已退出"}: true, {"暂停可租", "已退出"}: true,
	}
	for _, f := range all {
		for _, to := range all {
			if got := canTransit(f, to); got != allowed[[2]string{f, to}] {
				t.Errorf("%s→%s 期望 %v 得到 %v", f, to, allowed[[2]string{f, to}], got)
			}
		}
	}
}

func TestStartIntegration(t *testing.T) {
	reset(t)
	r := internalCall(t, "onboard", "/internal/fleet/brands/BR06/start-integration", "s1", map[string]any{"actor": actorOnboard})
	if r.status != 200 || r.body["accessStatus"] != "联调中" || r.body["statusSince"] != "2026-12-01T12:00:00+08:00" || r.body["brandId"] != "BR06" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	// 已在联调中、已接入的重复调用直接返回当前状态
	r = internalCall(t, "onboard", "/internal/fleet/brands/BR06/start-integration", "s2", map[string]any{"actor": actorOnboard})
	if r.status != 200 || r.body["accessStatus"] != "联调中" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	r = internalCall(t, "onboard", "/internal/fleet/brands/BR01/start-integration", "s3", map[string]any{"actor": actorOnboard})
	if r.status != 200 || r.body["accessStatus"] != "已接入" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if countType(outboxTypes(t), "fleet.brand.access_changed") != 1 {
		t.Fatal("只有真正迁移时发事件")
	}
	wantCode(t, internalCall(t, "onboard", "/internal/fleet/brands/BR04/start-integration", "s4", map[string]any{"actor": actorOnboard}), 409, "CONFLICT")
	wantCode(t, internalCall(t, "onboard", "/internal/fleet/brands/BR99/start-integration", "s5", map[string]any{"actor": actorOnboard}), 404, "NOT_FOUND")
	// 历史追加
	b := call(t, "GET", "/api/fleet/brands/BR06", govTok(t), nil, nil)
	h := b.body["statusHistory"].([]any)
	last := h[len(h)-1].(map[string]any)
	if len(h) != 2 || last["status"] != "联调中" || last["by"].(map[string]any)["id"] != "AC0403" {
		t.Fatalf("状态历史: %v", h)
	}
}

func TestGoLive(t *testing.T) {
	reset(t)
	body := map[string]any{"actor": actorOnboard, "reason": "验收通过，上线可租"}
	wantCode(t, internalCall(t, "onboard", "/internal/fleet/brands/BR05/go-live", "g0", map[string]any{"actor": actorOnboard, "reason": "过"}), 400, "REASON_REQUIRED")
	r := internalCall(t, "onboard", "/internal/fleet/brands/BR05/go-live", "g1", body)
	if r.status != 200 || r.body["enabledCodes"].(float64) != 1 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if r.body["brand"].(map[string]any)["accessStatus"] != "已接入" {
		t.Fatalf("应为已接入: %s", r.raw)
	}
	v := call(t, "GET", "/api/fleet/vehicles/VH001151", govTok(t), nil, nil)
	if v.body["codeStatus"] != "已启用" || v.body["rentable"] != true || v.body["status"] != "可用" {
		t.Fatalf("待启用的码全部启用，车辆可租: %s", v.raw)
	}
	types := outboxTypes(t)
	if countType(types, "fleet.brand.access_changed") != 1 || countType(types, "fleet.code.status_changed") != 1 {
		t.Fatalf("事件: %v", types)
	}
	ac := outboxEvents(t, "fleet.brand.access_changed")[0]["payload"].(map[string]any)
	if ac["from"] != "联调中" || ac["to"] != "已接入" || ac["reason"] != "验收通过，上线可租" || ac["changedAt"] != "2026-12-01T12:00:00+08:00" {
		t.Fatalf("状态变更载荷: %v", ac)
	}
	cs := outboxEvents(t, "fleet.code.status_changed")[0]["payload"].(map[string]any)
	if cs["from"] != "待启用" || cs["to"] != "已启用" || cs["govCode"] != "SF27239795" {
		t.Fatalf("码状态变更载荷: %v", cs)
	}
	wantCode(t, internalCall(t, "onboard", "/internal/fleet/brands/BR05/go-live", "g2", body), 409, "ALREADY_DONE")
	wantCode(t, internalCall(t, "onboard", "/internal/fleet/brands/BR06/go-live", "g3", body), 409, "CONFLICT")
	wantCode(t, internalCall(t, "onboard", "/internal/fleet/brands/BR02/go-live", "g4", body), 409, "CONFLICT")
	wantCode(t, internalCall(t, "onboard", "/internal/fleet/brands/BR99/go-live", "g5", body), 404, "NOT_FOUND")
	wantCode(t, internalCall(t, "onboard", "/internal/fleet/brands/BR05/go-live", "g6", map[string]any{"actor": map[string]any{"kind": "外星人", "id": "x"}, "reason": "验收通过上线"}), 400, "INVALID")
}

func TestPauseResume(t *testing.T) {
	reset(t)
	body := map[string]any{"actor": actorAgent, "metric": "开锁成功率", "value": 0.861, "threshold": 0.9, "sustainMinutes": 130, "reason": "开锁成功率持续低于门槛"}
	r := internalCall(t, "ops", "/internal/fleet/brands/BR03/pause", "p1", body)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	p := r.body["pause"].(map[string]any)
	if r.body["brand"].(map[string]any)["accessStatus"] != "暂停可租" || p["metric"] != "开锁成功率" || p["value"].(float64) != 0.861 || p["sustainMinutes"].(float64) != 130 || p["resumedAt"] != nil || p["note"] != "开锁成功率持续低于门槛" {
		t.Fatalf("暂停结果: %s", r.raw)
	}
	if p["by"].(map[string]any)["kind"] != "智能体" {
		t.Fatalf("执行人: %v", p["by"])
	}
	v := call(t, "GET", "/api/fleet/vehicles/VH000707", govTok(t), nil, nil)
	if v.body["rentable"] != false {
		t.Fatal("暂停可租后不可租")
	}
	wantCode(t, internalCall(t, "ops", "/internal/fleet/brands/BR03/pause", "p2", body), 409, "ALREADY_DONE")
	wantCode(t, internalCall(t, "ops", "/internal/fleet/brands/BR05/pause", "p3", body), 409, "CONFLICT")
	bad := map[string]any{"actor": actorAgent, "metric": "心情", "reason": "开锁成功率持续低于门槛"}
	wantCode(t, internalCall(t, "ops", "/internal/fleet/brands/BR01/pause", "p4", bad), 400, "INVALID")

	rb := map[string]any{"actor": map[string]any{"kind": "人", "id": "AC0401"}, "reason": "指标恢复，回到已接入"}
	r = internalCall(t, "ops", "/internal/fleet/brands/BR03/resume", "r1", rb)
	if r.status != 200 || r.body["brand"].(map[string]any)["accessStatus"] != "已接入" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	p = r.body["pause"].(map[string]any)
	if p["resumedAt"] != "2026-12-01T12:00:00+08:00" || p["resumeNote"] != "指标恢复，回到已接入" || p["resumedBy"].(map[string]any)["id"] != "AC0401" {
		t.Fatalf("恢复记录: %v", p)
	}
	v = call(t, "GET", "/api/fleet/vehicles/VH000707", govTok(t), nil, nil)
	if v.body["rentable"] != true {
		t.Fatal("恢复后可租")
	}
	wantCode(t, internalCall(t, "ops", "/internal/fleet/brands/BR03/resume", "r2", rb), 409, "CONFLICT")
	// 原型样例：木棉当前暂停中，恢复时关闭 OPS-PS-20261130-01
	r = internalCall(t, "ops", "/internal/fleet/brands/BR02/resume", "r3", rb)
	if r.status != 200 || r.body["pause"].(map[string]any)["pauseId"] != "OPS-PS-20261130-01" {
		t.Fatalf("木棉恢复: %s", r.raw)
	}
	actions := map[string]int{}
	for _, a := range outboxEvents(t, "fleet.audit.recorded") {
		actions[a["payload"].(map[string]any)["action"].(string)]++
	}
	if actions["暂停可租"] != 1 || actions["恢复可租"] != 2 {
		t.Fatalf("留痕: %v", actions)
	}
}

func TestOperatorExit(t *testing.T) {
	reset(t)
	tok := govTok(t)
	wantCode(t, call(t, "POST", "/api/fleet/operators/OP01/exit", tok, map[string]any{"reason": "退"}, nil), 400, "REASON_REQUIRED")
	wantCode(t, call(t, "POST", "/api/fleet/operators/OP01/exit", op1Tok(t), map[string]any{"reason": "按线下决定退出"}, nil), 403, "FORBIDDEN")
	wantCode(t, call(t, "POST", "/api/fleet/operators/OP99/exit", tok, map[string]any{"reason": "按线下决定退出"}, nil), 404, "NOT_FOUND")
	wantCode(t, call(t, "POST", "/api/fleet/operators/OP03/exit", tok, map[string]any{"reason": "按线下决定退出"}, nil), 409, "ALREADY_DONE")

	r := call(t, "POST", "/api/fleet/operators/OP01/exit", tok, map[string]any{"reason": "按主管部门线下决定退出"}, map[string]string{"Idempotency-Key": "ex1"})
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	// OP01 6 辆车全部有效码 → 6 个失效
	if r.body["invalidatedCodes"].(float64) != 6 {
		t.Fatalf("失效码数: %s", r.raw)
	}
	op := r.body["operator"].(map[string]any)
	if op["status"] != "已退出" || op["exitedAt"] != "2026-12-01T12:00:00+08:00" {
		t.Fatalf("运营商: %v", op)
	}
	bs := call(t, "GET", "/api/fleet/brands?operatorId=OP01", tok, nil, nil)
	for _, b := range bs.arr {
		if b.(map[string]any)["accessStatus"] != "已退出" {
			t.Fatalf("品牌全部已退出: %s", bs.raw)
		}
	}
	// 骑行中的车码失效但状态仍为骑行中（在途订单照常结束）
	v := call(t, "GET", "/api/fleet/vehicles/VH000110", tok, nil, nil)
	if v.body["codeStatus"] != "已失效" || v.body["status"] != "骑行中" {
		t.Fatalf("骑行中的车: %s", v.raw)
	}
	types := outboxTypes(t)
	if countType(types, "fleet.operator.exited") != 1 || countType(types, "fleet.brand.access_changed") != 2 || countType(types, "fleet.code.status_changed") != 6 {
		t.Fatalf("事件: %v", types)
	}
	ex := outboxEvents(t, "fleet.operator.exited")[0]
	p := ex["payload"].(map[string]any)
	ids := p["brandIds"].([]any)
	if ex["key"] != "OP01" || len(ids) != 2 || p["reason"] != "按主管部门线下决定退出" || p["exitedAt"] != "2026-12-01T12:00:00+08:00" {
		t.Fatalf("运营商已退出载荷: %v", ex)
	}
	audit := outboxEvents(t, "fleet.audit.recorded")
	found := false
	for _, a := range audit {
		ap := a["payload"].(map[string]any)
		if ap["action"] == "设置运营商退出" && ap["targetType"] == "operator" && ap["targetId"] == "OP01" && a["key"] == "operator:OP01" {
			found = true
		}
	}
	if !found {
		t.Fatal("应留痕运营商退出设置")
	}
	// 已退出品牌不能再迁移
	wantCode(t, internalCall(t, "ops", "/internal/fleet/brands/BR01/pause", "x1", map[string]any{"actor": actorAgent, "metric": "人工", "reason": "人工暂停可租"}), 409, "CONFLICT")
}

func TestAccessOverview(t *testing.T) {
	reset(t)
	r := call(t, "GET", "/api/fleet/access/overview", govTok(t), nil, nil)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	sum := r.body["summary"].([]any)
	if len(sum) != 5 {
		t.Fatalf("五种接入状态各一行: %v", sum)
	}
	want := map[string][2]float64{"未接入": {1, 0}, "联调中": {1, 1}, "已接入": {2, 7}, "暂停可租": {1, 1}, "已退出": {1, 1}}
	order := []string{"未接入", "联调中", "已接入", "暂停可租", "已退出"}
	for i, x := range sum {
		m := x.(map[string]any)
		if m["status"] != order[i] {
			t.Fatalf("顺序: %v", sum)
		}
		w := want[m["status"].(string)]
		if m["brandCount"].(float64) != w[0] || m["vehicleCount"].(float64) != w[1] {
			t.Errorf("%s 期望品牌 %v 车 %v，得到 %v", m["status"], w[0], w[1], m)
		}
	}
	brands := r.body["brands"].([]any)
	if len(brands) != 6 {
		t.Fatalf("品牌数: %d", len(brands))
	}
	b1 := brands[0].(map[string]any)
	if b1["rentable"].(float64) != 2 || b1["operator"].(map[string]any)["operatorId"] != "OP01" {
		t.Fatalf("青禾可租 2 辆: %v", b1)
	}
	bs := b1["byStatus"].(map[string]any)
	if bs["可用"].(float64) != 2 || bs["低电量"].(float64) != 1 || bs["骑行中"].(float64) != 1 || bs["离线"].(float64) != 1 {
		t.Fatalf("按车辆状态计数: %v", bs)
	}
	if b1["byCodeStatus"].(map[string]any)["已启用"].(float64) != 5 {
		t.Fatalf("按码状态计数: %v", b1["byCodeStatus"])
	}
	b2 := brands[1].(map[string]any)
	if b2["pause"] == nil || b2["rentable"].(float64) != 0 || len(b2["statusHistory"].([]any)) != 5 {
		t.Fatalf("木棉: %v", b2)
	}
	// 运营商账号只返回本运营商
	r = call(t, "GET", "/api/fleet/access/overview", op1Tok(t), nil, nil)
	if len(r.body["brands"].([]any)) != 2 {
		t.Fatalf("运营商只看本家: %s", r.raw)
	}
	s := r.body["summary"].([]any)
	if s[2].(map[string]any)["brandCount"].(float64) != 1 || s[3].(map[string]any)["brandCount"].(float64) != 1 {
		t.Fatalf("运营商汇总: %v", s)
	}
}

// 第二轮：运营商退出后载荷含 readonlyUntil；名下车辆保留且码已失效、不可租；运营商范围写操作拒绝 READONLY（令牌未标只读也拒）。
func TestOperatorExitSecondRound(t *testing.T) {
	reset(t)
	tok := govTok(t)
	r := call(t, "POST", "/api/fleet/operators/OP01/exit", tok, map[string]any{"reason": "按主管部门线下决定退出"}, map[string]string{"Idempotency-Key": "ex2"})
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	p := outboxEvents(t, "fleet.operator.exited")[0]["payload"].(map[string]any)
	if v, has := p["readonlyUntil"]; !has || v != nil {
		t.Fatalf("默认不设截止，readonlyUntil 应存在且为 null: %v", p)
	}
	// 车辆不删除，码已失效，不再计入可租
	list := call(t, "GET", "/api/fleet/operator/vehicles?operatorId=OP01", tok, nil, nil)
	if list.body["total"].(float64) != 6 || list.body["stats"].(map[string]any)["invalid"].(float64) != 6 {
		t.Fatalf("退出后车辆保留且码全部失效: %s", list.raw)
	}
	for _, row := range list.body["rows"].([]any) {
		if row.(map[string]any)["rentable"] != false {
			t.Fatalf("退出运营商的车不可租: %v", row)
		}
	}
	ov := call(t, "GET", "/api/fleet/access/overview", tok, nil, nil)
	for _, b := range ov.body["brands"].([]any) {
		bm := b.(map[string]any)
		if bm["operatorId"] == "OP01" && bm["rentable"].(float64) != 0 {
			t.Fatalf("总览可租数应为 0: %v", bm)
		}
	}
	// 运营商账号令牌没标只读，但运营商已退出：写操作一律 READONLY
	wantCode(t, call(t, "PATCH", "/api/fleet/operator/vehicles/VH000001", op1Tok(t), map[string]any{"model": "新车型"}, nil), 403, "READONLY")
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/VH000001/remove", op1Tok(t), map[string]any{"reason": "按线下决定移出"}, nil), 403, "READONLY")
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/upload", op1Tok(t), map[string]any{"rows": []map[string]any{{"brandId": "BR01", "plateNo": "QH999999", "model": "m", "pileTag": "x"}}}, nil), 403, "READONLY")
	// 只读：读不受影响
	if call(t, "GET", "/api/fleet/operator/vehicles", op1Tok(t), nil, nil).status != 200 {
		t.Fatal("退出后仍可查看")
	}
}

func TestOperatorExitReadonlyUntil(t *testing.T) {
	reset(t)
	testSvc.SetReadonlyDays(30)
	defer testSvc.SetReadonlyDays(0)
	call(t, "POST", "/api/fleet/operators/OP01/exit", govTok(t), map[string]any{"reason": "按主管部门线下决定退出"}, nil)
	p := outboxEvents(t, "fleet.operator.exited")[0]["payload"].(map[string]any)
	if p["readonlyUntil"] != "2026-12-31T12:00:00+08:00" {
		t.Fatalf("readonlyUntil: %v", p["readonlyUntil"])
	}
}

// 所有管理端写接口：令牌标只读一律 READONLY（先于业务校验）。
func TestReadonlyTokenRejectsAllAdminWrites(t *testing.T) {
	reset(t)
	tok := token(t, tokOpt{perms: []string{"access.setExit", "access.view", "vehicle.edit.own", "vehicle.view.own"}, scope: nil, readonly: true})
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/fleet/operator/vehicles/upload"},
		{"PATCH", "/api/fleet/operator/vehicles/VH000001"},
		{"POST", "/api/fleet/operator/vehicles/VH000001/remove"},
		{"POST", "/api/fleet/operators/OP01/exit"},
		{"POST", "/api/fleet/operators"},
		{"POST", "/api/fleet/brands"},
		{"POST", "/api/fleet/providers"},
		{"PUT", "/api/fleet/providers/PV01"},
	} {
		r := call(t, c.method, c.path, tok, map[string]any{}, nil)
		wantCode(t, r, 403, "READONLY")
	}
}

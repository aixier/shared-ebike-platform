package app

import (
	"context"
	"testing"
)

// 示范市范围内约 100m×110m 的围栏（未闭合，交给服务闭合）
var newFence = [][]float64{{104.05, 30.58}, {104.051, 30.58}, {104.051, 30.581}, {104.05, 30.581}}

func createBody(extra map[string]any) map[string]any {
	b := map[string]any{"name": "测试站点", "district": "高新区", "fence": newFence, "reason": "新增站点测试"}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func TestHealthz(t *testing.T) {
	h := newHarness(t)
	r := h.req("GET", "/healthz", nil)
	mustStatus(t, r, 200)
	if r.Body["ok"] != true {
		t.Fatalf("healthz 应返回 ok:true：%s", r.Raw)
	}
}

func TestAdminAuth(t *testing.T) {
	h := newHarness(t)
	mustCode(t, h.req("GET", "/api/site/admin/stations", nil), 401, "ADMIN_NOT_LOGGED_IN")
	mustCode(t, h.req("GET", "/api/site/admin/stations", nil, "Authorization", "Bearer 乱写的"), 401, "ADMIN_NOT_LOGGED_IN")
	mustCode(t, h.admin("GET", "/api/site/admin/stations", nil, "order.view"), 403, "FORBIDDEN")
	mustStatus(t, h.admin("GET", "/api/site/admin/stations", nil, "ops.monitor"), 200) // 列出的权限点有其一即可
	mustStatus(t, h.admin("GET", "/api/site/admin/stations/all", nil), 200)            // 登录即可
	mustCode(t, h.admin("POST", "/api/site/admin/stations", createBody(nil), "pile.edit"), 403, "FORBIDDEN")
	ro := tokenWith(map[string]any{"readonly": true}, "station.edit")
	mustCode(t, h.req("POST", "/api/site/admin/stations", createBody(nil), "Authorization", "Bearer "+ro), 403, "READONLY")
	sc := tokenWith(map[string]any{"scope": "OP01"}, "station.edit")
	mustCode(t, h.req("POST", "/api/site/admin/stations", createBody(nil), "Authorization", "Bearer "+sc), 403, "SCOPE_DENIED")
}

func TestCreateStation(t *testing.T) {
	h := newHarness(t)
	r := h.admin("POST", "/api/site/admin/stations", createBody(nil), "station.edit")
	mustStatus(t, r, 200)
	b := r.Body
	if b["stationId"] != "ST0001" || b["status"] != "启用" || b["type"] != "无桩" || b["capacity"] != float64(10) || b["pileCount"] != float64(0) {
		t.Fatalf("新站点字段错误：%s", r.Raw)
	}
	fence := b["fence"].([]any)
	if len(fence) != 5 {
		t.Fatalf("围栏应自动闭合为 5 个点：%v", fence)
	}
	if by, _ := b["updatedBy"].(map[string]any); by["id"] != "AC0001" || by["kind"] != "人" {
		t.Fatalf("调整人错误：%v", b["updatedBy"])
	}
	if b["createdAt"] != "2026-12-01T12:00:00+08:00" {
		t.Fatalf("创建时刻应为服务器时间 RFC 3339：%v", b["createdAt"])
	}
	ev := h.outboxEvents("site.station.changed")
	if len(ev) != 1 || ev[0]["key"] != "ST0001" || payload(ev[0])["status"] != "启用" || payload(ev[0])["type"] != "无桩" {
		t.Fatalf("站点变更事件错误：%v", ev)
	}
	au := h.outboxEvents("site.audit.recorded")
	if len(au) != 1 {
		t.Fatalf("应发 1 条留痕：%v", au)
	}
	ap := payload(au[0])
	if ap["action"] != "新建站点" || ap["targetType"] != "station" || ap["targetId"] != "ST0001" || ap["reason"] != "新增站点测试" || au[0]["key"] != "station:ST0001" {
		t.Fatalf("留痕内容错误：%v", au[0])
	}
	if a := au[0]["actor"].(map[string]any); a["id"] != "AC0001" {
		t.Fatalf("留痕操作人错误：%v", a)
	}
}

func TestCreateStationValidation(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name  string
		extra map[string]any
		code  string
	}{
		{"缺事由", map[string]any{"reason": "短"}, "REASON_REQUIRED"},
		{"缺名称", map[string]any{"name": "  "}, "INVALID"},
		{"区不对", map[string]any{"district": "火星区"}, "INVALID"},
		{"类型不对", map[string]any{"type": "半桩"}, "INVALID"},
		{"容量为 0", map[string]any{"capacity": 0}, "INVALID"},
		{"两个顶点", map[string]any{"fence": [][]float64{{104.05, 30.58}, {104.051, 30.58}}}, "INVALID"},
		{"出本市", map[string]any{"fence": [][]float64{{103.5, 30.58}, {104.051, 30.58}, {104.051, 30.581}}}, "INVALID"},
		{"面积过小", map[string]any{"fence": [][]float64{{104.05, 30.58}, {104.05001, 30.58}, {104.05001, 30.58001}}}, "INVALID"},
		{"顶点格式", map[string]any{"fence": [][]float64{{104.05}, {104.051, 30.58}, {104.051, 30.581}}}, "INVALID"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustCode(t, h.admin("POST", "/api/site/admin/stations", createBody(c.extra), "station.edit"), 400, c.code)
		})
	}
	mustCode(t, h.admin("POST", "/api/site/admin/stations", "{坏", "station.edit"), 400, "INVALID")
	if n := countRows(t, `SELECT count(*) FROM stations`); n != 0 {
		t.Fatalf("校验失败不应落库：%d", n)
	}
	if n := countRows(t, `SELECT count(*) FROM outbox`); n != 0 {
		t.Fatalf("校验失败不应发事件：%d", n)
	}
}

func TestIdempotency(t *testing.T) {
	h := newHarness(t)
	tok := "Bearer " + token("station.edit")
	r1 := h.req("POST", "/api/site/admin/stations", createBody(nil), "Authorization", tok, "Idempotency-Key", "k-1")
	r2 := h.req("POST", "/api/site/admin/stations", createBody(map[string]any{"name": "另一个"}), "Authorization", tok, "Idempotency-Key", "k-1")
	mustStatus(t, r1, 200)
	mustStatus(t, r2, 200)
	if r1.Raw != r2.Raw {
		t.Fatalf("同一幂等键应返回第一次的结果：\n%s\n%s", r1.Raw, r2.Raw)
	}
	r3 := h.req("POST", "/api/site/admin/stations", createBody(nil), "Authorization", tok, "Idempotency-Key", "k-2")
	if r3.Body["stationId"] != "ST0002" {
		t.Fatalf("不同幂等键应新建：%s", r3.Raw)
	}
	if n := countRows(t, `SELECT count(*) FROM stations`); n != 2 {
		t.Fatalf("应只有 2 个站点：%d", n)
	}
	// 失败结果同样重放
	e1 := h.req("POST", "/api/site/admin/stations", createBody(map[string]any{"reason": "短"}), "Authorization", tok, "Idempotency-Key", "k-3")
	e2 := h.req("POST", "/api/site/admin/stations", createBody(nil), "Authorization", tok, "Idempotency-Key", "k-3")
	if e1.Status != 400 || e2.Status != 400 {
		t.Fatalf("失败结果应重放：%d %d", e1.Status, e2.Status)
	}
}

// 原型样例：ST0001 桩满（fullNow）、ST0002 无桩、ST0004 在线桩全占用（任务 14）
func TestListStationsWithProtoSample(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	r := h.admin("GET", "/api/site/admin/stations", nil, "station.edit")
	mustStatus(t, r, 200)
	if r.Body["total"] != float64(4) || r.Body["page"] != float64(1) || r.Body["pageSize"] != float64(20) {
		t.Fatalf("分页信息错误：%s", r.Raw)
	}
	if reg := r.Body["region"].(map[string]any); reg["city"] != "示范市" {
		t.Fatalf("应带区域：%v", reg)
	}
	byID := map[string]map[string]any{}
	for _, x := range r.Body["rows"].([]any) {
		m := x.(map[string]any)
		byID[m["stationId"].(string)] = m
	}
	st1 := byID["ST0001"]
	if st1["fullNow"] != true || st1["freePiles"] != float64(0) || st1["pileCount"] != float64(11) || st1["parkedVehicles"] != float64(11) || len(st1["pileIds"].([]any)) != 11 {
		t.Fatalf("ST0001 统计错误：%v", st1)
	}
	if byID["ST0002"]["type"] != "无桩" || byID["ST0002"]["fullNow"] != false || byID["ST0002"]["pileCount"] != float64(0) {
		t.Fatalf("ST0002 应为无桩、不满：%v", byID["ST0002"])
	}
	if byID["ST0004"]["fullNow"] != true || byID["ST0004"]["faultPiles"] != float64(1) {
		t.Fatalf("ST0004 应为桩满且 1 个不在线：%v", byID["ST0004"])
	}
	st3 := byID["ST0003"]
	if st3["freePiles"] != float64(5) || st3["onlinePiles"] != float64(9) || st3["faultPiles"] != float64(1) || st3["fullNow"] != false {
		t.Fatalf("ST0003 统计错误：%v", st3)
	}

	// 过滤、排序、分页
	r = h.admin("GET", "/api/site/admin/stations?type=有桩&district=西城区", nil, "station.edit")
	if r.Body["total"] != float64(2) {
		t.Fatalf("按类型与区过滤错误：%s", r.Raw)
	}
	r = h.admin("GET", "/api/site/admin/stations?q=市民中心", nil, "station.edit")
	if r.Body["total"] != float64(1) || r.Body["rows"].([]any)[0].(map[string]any)["stationId"] != "ST0003" {
		t.Fatalf("按名称搜索错误：%s", r.Raw)
	}
	r = h.admin("GET", "/api/site/admin/stations?sortKey=freePiles&sortDir=desc&pageSize=1&page=1", nil, "station.edit")
	rows := r.Body["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["stationId"] != "ST0003" || r.Body["total"] != float64(4) {
		t.Fatalf("排序分页错误：%s", r.Raw)
	}
	mustCode(t, h.admin("GET", "/api/site/admin/stations?sortKey=xx", nil, "station.edit"), 400, "INVALID")
	mustCode(t, h.admin("GET", "/api/site/admin/stations?pageSize=500", nil, "station.edit"), 400, "INVALID")
	mustCode(t, h.admin("GET", "/api/site/admin/stations?type=半桩", nil, "station.edit"), 400, "INVALID")
}

func TestGetStationAndPiles(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	r := h.admin("GET", "/api/site/admin/stations/ST0001", nil)
	mustStatus(t, r, 200)
	piles := r.Body["piles"].([]any)
	if len(piles) != 11 {
		t.Fatalf("ST0001 应有 11 个桩：%d", len(piles))
	}
	p1 := piles[0].(map[string]any)
	if p1["pileId"] != "PL00001" || p1["occupiedVehicleId"] != "VH000036" || p1["occupiedPlateNo"] == nil || p1["stationName"] != "文昌街老城区商业街" {
		t.Fatalf("桩字段错误：%v", p1)
	}
	mustCode(t, h.admin("GET", "/api/site/admin/stations/ST9999", nil), 404, "NOT_FOUND")

	r = h.admin("GET", "/api/site/admin/piles?stationId=ST0003&status=在线", nil, "pile.edit")
	if r.Body["total"] != float64(9) {
		t.Fatalf("按站点与状态过滤桩错误：%s", r.Raw)
	}
	r = h.admin("GET", "/api/site/admin/piles?sortKey=no&sortDir=desc&pageSize=3", nil, "ops.monitor")
	if r.Body["total"] != float64(28) || len(r.Body["rows"].([]any)) != 3 || r.Body["rows"].([]any)[0].(map[string]any)["no"] != float64(11) {
		t.Fatalf("桩排序分页错误：%s", r.Raw)
	}
	mustCode(t, h.admin("GET", "/api/site/admin/piles?status=坏了", nil, "pile.edit"), 400, "INVALID")
	mustCode(t, h.admin("GET", "/api/site/admin/piles", nil, "order.view"), 403, "FORBIDDEN")
}

func TestUpdateStation(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	r := h.admin("PATCH", "/api/site/admin/stations/ST0002", map[string]any{"name": "桂溪路口", "capacity": 20, "reason": "现场复核调整"}, "station.edit")
	mustStatus(t, r, 200)
	if r.Body["name"] != "桂溪路口" || r.Body["capacity"] != float64(20) || r.Body["updatedAt"] != "2026-12-01T12:00:00+08:00" {
		t.Fatalf("调整结果错误：%s", r.Raw)
	}
	au := h.outboxEvents("site.audit.recorded")
	ap := payload(au[len(au)-1])
	before, after := ap["before"].(map[string]any), ap["after"].(map[string]any)
	if before["name"] != "桂溪路与建设大道路口" || after["capacity"] != float64(20) || before["fence"] != nil {
		t.Fatalf("留痕应只含改动字段的前后值：%v", ap)
	}
	if len(h.outboxEvents("site.station.changed")) != 1 {
		t.Fatal("调整应发站点变更事件")
	}

	// 调整围栏：新围栏须覆盖全部桩
	mustCode(t, h.admin("PATCH", "/api/site/admin/stations/ST0001", map[string]any{"fence": newFence, "reason": "移动围栏测试"}, "station.edit"), 400, "INVALID")
	// 有桩站点不能改为无桩
	mustCode(t, h.admin("PATCH", "/api/site/admin/stations/ST0001", map[string]any{"type": "无桩", "reason": "改类型测试"}, "station.edit"), 400, "INVALID")
	// 无桩站点的围栏可以移动
	r = h.admin("PATCH", "/api/site/admin/stations/ST0002", map[string]any{"fence": newFence, "reason": "移动围栏测试"}, "station.edit")
	mustStatus(t, r, 200)
	if c := r.Body["center"].([]any); c[0] != 104.0505 || c[1] != 30.5805 {
		t.Fatalf("中心点应随围栏更新：%v", c)
	}
	mustCode(t, h.admin("PATCH", "/api/site/admin/stations/ST9999", map[string]any{"name": "x", "reason": "不存在测试"}, "station.edit"), 404, "NOT_FOUND")
	mustCode(t, h.admin("PATCH", "/api/site/admin/stations/ST0002", map[string]any{"name": "x"}, "station.edit"), 400, "REASON_REQUIRED")
}

func TestRemoveStation(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	ctx := context.Background()
	reason := map[string]any{"reason": "站点撤销测试"}

	r := h.admin("POST", "/api/site/admin/stations/ST0001/remove", reason, "station.edit")
	mustCode(t, r, 409, "STATION_IN_USE")
	if r.Body["piles"] != float64(11) {
		t.Fatalf("应附桩数：%s", r.Raw)
	}

	// ST0002 无桩：有进行中骑行时不能删
	if err := h.app.HandleEvent(ctx, rideUnlocked("OD2026120111001", "VH000004", "ST0002")); err != nil {
		t.Fatal(err)
	}
	r = h.admin("POST", "/api/site/admin/stations/ST0002/remove", reason, "station.edit")
	mustCode(t, r, 409, "STATION_IN_USE")
	if r.Body["riding"] != float64(1) {
		t.Fatalf("应附进行中骑行数：%s", r.Raw)
	}
	// 在点还到 ST0002：骑行结束，但停着车
	if err := h.app.HandleEvent(ctx, rideEnded("OD2026120111001", "VH000004", "ST0002", "ST0002", true)); err != nil {
		t.Fatal(err)
	}
	r = h.admin("POST", "/api/site/admin/stations/ST0002/remove", reason, "station.edit")
	mustCode(t, r, 409, "STATION_IN_USE")
	if r.Body["parked"] != float64(1) {
		t.Fatalf("应附停放车辆数：%s", r.Raw)
	}
	// 车被骑走
	if err := h.app.HandleEvent(ctx, rideUnlocked("OD2026120111002", "VH000004", "ST0002")); err != nil {
		t.Fatal(err)
	}
	if err := h.app.HandleEvent(ctx, rideEnded("OD2026120111002", "VH000004", "ST0002", nil, false)); err != nil {
		t.Fatal(err)
	}
	r = h.admin("POST", "/api/site/admin/stations/ST0002/remove", reason, "station.edit")
	mustStatus(t, r, 200)
	if r.Body["ok"] != true || r.Body["stationId"] != "ST0002" {
		t.Fatalf("删除结果错误：%s", r.Raw)
	}
	mustCode(t, h.admin("POST", "/api/site/admin/stations/ST0002/remove", reason, "station.edit"), 409, "ALREADY_DONE")
	mustCode(t, h.admin("POST", "/api/site/admin/stations/ST9999/remove", reason, "station.edit"), 404, "NOT_FOUND")
	mustCode(t, h.admin("POST", "/api/site/admin/stations/ST0003/remove", map[string]any{}, "station.edit"), 400, "REASON_REQUIRED")

	ev := h.outboxEvents("site.station.changed")
	if len(ev) != 1 || payload(ev[0])["status"] != "停用" {
		t.Fatalf("删除应发站点变更（停用）：%v", ev)
	}
	// 停用后保留记录，但不在地图与默认列表里
	d := h.admin("GET", "/api/site/admin/stations/ST0002", nil)
	if d.Body["status"] != "停用" || d.Body["removedAt"] == nil || d.Body["removedBy"] == nil {
		t.Fatalf("停用站点应保留记录：%s", d.Raw)
	}
	if h.admin("GET", "/api/site/admin/stations", nil, "station.edit").Body["total"] != float64(3) {
		t.Fatal("默认列表不应含停用站点")
	}
	if h.admin("GET", "/api/site/admin/stations?includeRemoved=true", nil, "station.edit").Body["total"] != float64(4) {
		t.Fatal("includeRemoved=true 应含停用站点")
	}
	if len(h.admin("GET", "/api/site/admin/stations/all", nil).Body["stations"].([]any)) != 3 {
		t.Fatal("管理端地图不应含停用站点")
	}
	if len(h.req("GET", "/api/site/map", nil).Body["stations"].([]any)) != 3 {
		t.Fatal("扫码页地图不应含停用站点")
	}
}

func TestCreatePile(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	// ST0002 无桩站点，中心点在围栏内
	in := map[string]any{"stationId": "ST0002", "location": []float64{104.046362, 30.579147}, "reason": "新装桩位测试"}
	r := h.admin("POST", "/api/site/admin/piles", in, "pile.edit")
	mustStatus(t, r, 200)
	if r.Body["pileId"] != "PL00029" || r.Body["no"] != float64(1) || r.Body["status"] != "在线" || r.Body["stationName"] != "桂溪路与建设大道路口" {
		t.Fatalf("新桩字段错误：%s", r.Raw)
	}
	r = h.admin("POST", "/api/site/admin/piles", in, "pile.edit")
	if r.Body["no"] != float64(2) {
		t.Fatalf("桩位号应顺延：%s", r.Raw)
	}
	st := h.admin("GET", "/api/site/admin/stations/ST0002", nil)
	if st.Body["type"] != "有桩" || st.Body["pileCount"] != float64(2) {
		t.Fatalf("无桩站点登记桩后应变为有桩：%s", st.Raw)
	}
	ps := h.outboxEvents("site.pile.status_changed")
	if len(ps) != 2 || payload(ps[0])["status"] != "在线" || payload(ps[0])["no"] != float64(1) || ps[0]["key"] != "PL00029" {
		t.Fatalf("登记桩应发桩状态变更：%v", ps)
	}
	sc := h.outboxEvents("site.station.changed")
	if len(sc) != 1 || payload(sc[0])["type"] != "有桩" {
		t.Fatalf("站点类型变化应发 1 次站点变更：%v", sc)
	}
	au := h.outboxEvents("site.audit.recorded")
	if payload(au[0])["action"] != "登记桩" || payload(au[0])["targetType"] != "pile" {
		t.Fatalf("留痕错误：%v", au[0])
	}

	mustCode(t, h.admin("POST", "/api/site/admin/piles", map[string]any{"stationId": "ST0002", "location": []float64{104.06, 30.59}, "reason": "围栏外测试"}, "pile.edit"), 400, "INVALID")
	mustCode(t, h.admin("POST", "/api/site/admin/piles", map[string]any{"stationId": "ST9999", "location": []float64{104.06, 30.59}, "reason": "站点不存在"}, "pile.edit"), 404, "NOT_FOUND")
	mustCode(t, h.admin("POST", "/api/site/admin/piles", map[string]any{"stationId": "ST0002", "location": []float64{104.06}, "reason": "坐标错误测试"}, "pile.edit"), 400, "INVALID")
	mustCode(t, h.admin("POST", "/api/site/admin/piles", map[string]any{"stationId": "ST0002", "location": []float64{104.046362, 30.579147}}, "pile.edit"), 400, "REASON_REQUIRED")
}

func TestSetPileStatus(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	r := h.admin("POST", "/api/site/admin/piles/PL00013/status", map[string]any{"status": "故障", "reason": "地锁卡滞报修"}, "pile.edit")
	mustStatus(t, r, 200)
	if r.Body["status"] != "故障" || r.Body["faultNote"] != "地锁卡滞报修" {
		t.Fatalf("故障说明缺省应取事由：%s", r.Raw)
	}
	ev := h.outboxEvents("site.pile.status_changed")
	if len(ev) != 1 || payload(ev[0])["status"] != "故障" || payload(ev[0])["changedAt"] != "2026-12-01T12:00:00+08:00" {
		t.Fatalf("应发桩状态变更：%v", ev)
	}
	au := payload(h.outboxEvents("site.audit.recorded")[0])
	if au["action"] != "设置桩状态" || au["before"].(map[string]any)["status"] != "在线" || au["after"].(map[string]any)["status"] != "故障" {
		t.Fatalf("留痕错误：%v", au)
	}
	r = h.admin("POST", "/api/site/admin/piles/PL00013/status", map[string]any{"status": "在线", "reason": "已修复上线"}, "pile.edit")
	if r.Body["status"] != "在线" || r.Body["faultNote"] != nil {
		t.Fatalf("恢复在线应清除说明：%s", r.Raw)
	}
	mustCode(t, h.admin("POST", "/api/site/admin/piles/PL00013/status", map[string]any{"status": "报废", "reason": "状态非法测试"}, "pile.edit"), 400, "INVALID")
	mustCode(t, h.admin("POST", "/api/site/admin/piles/PL99999/status", map[string]any{"status": "在线", "reason": "不存在测试"}, "pile.edit"), 404, "NOT_FOUND")
}

func TestStationMap(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	r := h.req("GET", "/api/site/map", nil) // 不需要登录
	mustStatus(t, r, 200)
	if len(r.Body["stations"].([]any)) != 4 || len(r.Body["piles"].([]any)) != 28 {
		t.Fatalf("地图数据数量错误：%s", r.Raw[:200])
	}
	for _, x := range r.Body["stations"].([]any) {
		m := x.(map[string]any)
		if m["stationId"] == "ST0003" && (m["freePiles"] != float64(5) || m["pileCount"] != float64(10) || m["district"] != "西城区") {
			t.Fatalf("ST0003 地图字段错误：%v", m)
		}
	}
	occupied := 0
	for _, x := range r.Body["piles"].([]any) {
		if x.(map[string]any)["occupied"] == true {
			occupied++
		}
	}
	if occupied != 21 {
		t.Fatalf("在位桩数应为 21：%d", occupied)
	}
}

package fleet

import (
	"context"
	"regexp"
	"testing"
)

func TestListVehicles(t *testing.T) {
	reset(t)
	r := call(t, "GET", "/api/fleet/operator/vehicles", op1Tok(t), nil, nil)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if r.body["total"].(float64) != 6 || len(r.body["rows"].([]any)) != 6 {
		t.Fatalf("OP01 有 6 辆车: %s", r.raw)
	}
	st := r.body["stats"].(map[string]any)
	if st["total"].(float64) != 6 || st["enabled"].(float64) != 6 || st["pending"].(float64) != 0 || st["invalid"].(float64) != 0 {
		t.Fatalf("统计不对: %v", st)
	}
	// 筛选与分页不改变统计
	r = call(t, "GET", "/api/fleet/operator/vehicles?brandId=BR02&page=1&pageSize=1", op1Tok(t), nil, nil)
	if r.body["total"].(float64) != 1 || r.body["stats"].(map[string]any)["total"].(float64) != 6 || r.body["pageSize"].(float64) != 1 {
		t.Fatalf("筛选后: %s", r.raw)
	}
	r = call(t, "GET", "/api/fleet/operator/vehicles?status=低电量", op1Tok(t), nil, nil)
	if r.body["total"].(float64) != 1 || r.body["rows"].([]any)[0].(map[string]any)["vehicleId"] != "VH000015" {
		t.Fatalf("按状态: %s", r.raw)
	}
	// q：包含匹配、不分大小写，覆盖车号、政府码、车辆编号、车辆标识
	for q, want := range map[string]string{"qh464": "VH000004", "86020226": "VH000004", "vh000110": "VH000110", "f425cc4a1483": "VH000001"} {
		r = call(t, "GET", "/api/fleet/operator/vehicles?q="+q, op1Tok(t), nil, nil)
		rows := r.body["rows"].([]any)
		if len(rows) != 1 || rows[0].(map[string]any)["vehicleId"] != want {
			t.Errorf("q=%s 应找到 %s: %s", q, want, r.raw)
		}
	}
	// 分页与排序
	r = call(t, "GET", "/api/fleet/operator/vehicles?pageSize=2&page=2&sortKey=vehicleId&sortDir=desc", op1Tok(t), nil, nil)
	rows := r.body["rows"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["vehicleId"] != "VH000016" {
		t.Fatalf("分页排序: %s", r.raw)
	}
	wantCode(t, call(t, "GET", "/api/fleet/operator/vehicles?sortKey=xx", op1Tok(t), nil, nil), 400, "INVALID")
	wantCode(t, call(t, "GET", "/api/fleet/operator/vehicles?page=0", op1Tok(t), nil, nil), 400, "INVALID")

	// 运营商账号指定他家
	wantCode(t, call(t, "GET", "/api/fleet/operator/vehicles?operatorId=OP02", op1Tok(t), nil, nil), 403, "SCOPE_DENIED")
	// 平台账号看全部或指定
	r = call(t, "GET", "/api/fleet/operator/vehicles", govTok(t), nil, nil)
	st = r.body["stats"].(map[string]any)
	if st["total"].(float64) != 10 || st["pending"].(float64) != 1 || st["invalid"].(float64) != 1 {
		t.Fatalf("平台全量统计: %v", st)
	}
	r = call(t, "GET", "/api/fleet/operator/vehicles?operatorId=OP02", govTok(t), nil, nil)
	if r.body["total"].(float64) != 2 {
		t.Fatalf("平台指定运营商: %s", r.raw)
	}
	r = call(t, "GET", "/api/fleet/operator/vehicles?codeStatus=待启用", govTok(t), nil, nil)
	if r.body["total"].(float64) != 1 {
		t.Fatalf("按码状态: %s", r.raw)
	}
}

func uploadRow(brand, plate, tag string) map[string]any {
	return map[string]any{"brandId": brand, "plateNo": plate, "model": "青禾 Q3 电单车", "pileTag": tag}
}

func TestUploadVehicleList(t *testing.T) {
	reset(t)
	rows := []any{
		uploadRow("BR01", "qh900001", "e28011aaaaaaaaaaaaaaaa01"),
		uploadRow("BR02", "MM900002", "E28011AAAAAAAAAAAAAAAA02"),
	}
	r := call(t, "POST", "/api/fleet/operator/vehicles/upload", op1Tok(t), map[string]any{"rows": rows}, map[string]string{"Idempotency-Key": "up-1"})
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if r.body["added"].(float64) != 2 {
		t.Fatalf("应入库 2 辆: %s", r.raw)
	}
	vs := r.body["vehicles"].([]any)
	gc := regexp.MustCompile(`^SF\d{8}$`)
	vid := regexp.MustCompile(`^VH\d{6}$`)
	for _, x := range vs {
		v := x.(map[string]any)
		if !gc.MatchString(v["govCode"].(string)) || !vid.MatchString(v["vehicleId"].(string)) {
			t.Fatalf("编号格式不对: %v", v)
		}
		if v["codeStatus"] != "已启用" || v["pileTagType"] != "RFID" || v["operatorId"] != "OP01" {
			t.Fatalf("已接入或暂停可租品牌的码立即启用: %v", v)
		}
	}
	v0 := vs[0].(map[string]any)
	if v0["plateNo"] != "QH900001" || v0["pileTag"] != "E28011AAAAAAAAAAAAAAAA01" || v0["vehicleId"] != "VH001152" {
		t.Fatalf("应规范成大写、续号发车辆编号: %v", v0)
	}
	if v0["rentable"] != true {
		t.Fatalf("已接入品牌的新车可租: %v", v0)
	}
	types := outboxTypes(t)
	if countType(types, "fleet.vehicle.listed") != 2 || countType(types, "fleet.audit.recorded") < 2 {
		t.Fatalf("事件不对: %v", types)
	}
	ev := outboxEvents(t, "fleet.vehicle.listed")[0]
	p := ev["payload"].(map[string]any)
	for _, k := range []string{"vehicleId", "brandId", "operatorId", "plateNo", "govCode", "model", "pileTagType", "pileTag", "codeStatus", "codeEnableAt", "listedAt"} {
		if _, ok := p[k]; !ok {
			t.Fatalf("车辆已入清单载荷缺 %s: %v", k, p)
		}
	}
	if ev["key"] != p["vehicleId"] {
		t.Fatal("key 应为 vehicleId")
	}
	audits := outboxEvents(t, "fleet.audit.recorded")
	actions := map[string]bool{}
	for _, a := range audits {
		actions[a["payload"].(map[string]any)["action"].(string)] = true
	}
	if !actions["提交车辆清单"] || !actions["发放政府码"] {
		t.Fatalf("应留痕提交车辆清单与发放政府码: %v", actions)
	}

	// 幂等：同一个键重复到达不重复入库
	r2 := call(t, "POST", "/api/fleet/operator/vehicles/upload", op1Tok(t), map[string]any{"rows": rows}, map[string]string{"Idempotency-Key": "up-1"})
	if r2.status != 200 || string(r2.raw) != string(r.raw) {
		t.Fatalf("幂等: %d %s", r2.status, r2.raw)
	}
}

func TestUploadPendingBrand(t *testing.T) {
	reset(t)
	tok := token(t, tokOpt{sub: "AC0104", perms: []string{"vehicle.edit.own"}, scope: "OP04"})
	r := call(t, "POST", "/api/fleet/operator/vehicles/upload", tok, map[string]any{"rows": []any{uploadRow("BR05", "GJ900001", "E28011BBBBBBBBBBBBBBBB01")}}, nil)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	v := r.body["vehicles"].([]any)[0].(map[string]any)
	if v["codeStatus"] != "待启用" || v["status"] != "码未启用" || v["rentable"] != false {
		t.Fatalf("联调中品牌的码为待启用: %v", v)
	}
	if v["codeEnableAt"] != "2026-12-08T09:00:00+08:00" {
		t.Fatalf("待启用的计划启用时刻沿用该品牌的计划: %v", v["codeEnableAt"])
	}
}

func TestUploadValidation(t *testing.T) {
	reset(t)
	rows := []any{
		uploadRow("BR03", "CL900001", "E28011CCCCCCCCCCCCCCCC01"),                                                                          // 1 他家品牌
		uploadRow("BR01", "MM900002", "E28011CCCCCCCCCCCCCCCC02"),                                                                          // 2 车号前缀不对
		uploadRow("BR01", "QH12", "E28011CCCCCCCCCCCCCCCC03"),                                                                              // 3 车号格式
		uploadRow("BR01", "QH900004", "XYZ"),                                                                                               // 4 标识格式
		uploadRow("BR01", "QH900005", "E28011F425CC4A1483995C39"),                                                                          // 5 标识与在用车重复
		uploadRow("BR01", "QH900006", "E28011CCCCCCCCCCCCCCCC06"),                                                                          // 6 正确
		uploadRow("BR01", "QH900006", "E28011CCCCCCCCCCCCCCCC07"),                                                                          // 7 批内车号重复
		map[string]any{"brandId": "BR01", "plateNo": "QH900008", "model": " ", "pileTag": "E28011CCCCCCCCCCCCCCCC08"},                      // 8 车型空
		map[string]any{"brandId": "BR01", "plateNo": "QH900009", "model": "x", "pileTag": "E28011CCCCCCCCCCCCCCCC09", "pileTagType": "红外"}, // 9 识别方式不对
	}
	r := call(t, "POST", "/api/fleet/operator/vehicles/upload", op1Tok(t), map[string]any{"rows": rows}, nil)
	wantCode(t, r, 400, "VEHICLE_LIST_INVALID")
	errs := r.body["errors"].([]any)
	bad := map[int]string{}
	for _, e := range errs {
		m := e.(map[string]any)
		bad[int(m["row"].(float64))] = m["field"].(string)
	}
	want := map[int]string{1: "brandId", 2: "plateNo", 3: "plateNo", 4: "pileTag", 5: "pileTag", 7: "plateNo", 8: "model", 9: "pileTagType"}
	for row, f := range want {
		if bad[row] != f {
			t.Errorf("第 %d 行应报 %s 错，得到 %q", row, f, bad[row])
		}
	}
	if _, ok := bad[6]; ok {
		t.Error("第 6 行正确，不应报错")
	}
	// 整批拒绝：一辆都没入库
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM vehicles WHERE plate_no='QH900006'`).Scan(&n)
	if n != 0 {
		t.Fatal("有错时整批不入库")
	}
	if len(outboxTypes(t)) != 0 {
		t.Fatal("有错时不发事件")
	}
	// 未接入、已退出品牌不能提交
	tok5 := token(t, tokOpt{sub: "AC0105", perms: []string{"vehicle.edit.own"}, scope: "OP05"})
	r = call(t, "POST", "/api/fleet/operator/vehicles/upload", tok5, map[string]any{"rows": []any{uploadRow("BR06", "TM900001", "E28011CCCCCCCCCCCCCCCC10")}}, nil)
	wantCode(t, r, 400, "VEHICLE_LIST_INVALID")
	// 空清单
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/upload", op1Tok(t), map[string]any{"rows": []any{}}, nil), 400, "INVALID")
	// 平台账号（无数据范围）不能替运营商上传
	plat := token(t, tokOpt{perms: []string{"vehicle.edit.own"}})
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/upload", plat, map[string]any{"rows": []any{uploadRow("BR01", "QH900011", "E28011CCCCCCCCCCCCCCCC11")}}, nil), 403, "FORBIDDEN")
	// 已退出运营商只读
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/upload", op3Tok(t), map[string]any{"rows": []any{uploadRow("BR04", "JT900001", "E28011CCCCCCCCCCCCCCCC12")}}, nil), 403, "READONLY")
}

func TestUploadUpdatesExisting(t *testing.T) {
	// 清单只做新增与更新：已在清单内的本家车号按更新处理
	reset(t)
	rows := []any{map[string]any{"brandId": "BR01", "plateNo": "QH464022", "model": "青禾 Q5 电单车", "pileTag": "E28011C79479F55A38EC7779"}}
	r := call(t, "POST", "/api/fleet/operator/vehicles/upload", op1Tok(t), map[string]any{"rows": rows}, nil)
	if r.status != 200 || r.body["added"].(float64) != 0 || r.body["updated"].(float64) != 1 {
		t.Fatalf("应按更新处理: %d %s", r.status, r.raw)
	}
	v := r.body["vehicles"].([]any)[0].(map[string]any)
	if v["vehicleId"] != "VH000004" || v["govCode"] != "SF86020226" || v["model"] != "青禾 Q5 电单车" {
		t.Fatalf("更新不发新码、不换编号: %v", v)
	}
	// 他家车号算重复
	rows = []any{uploadRow("BR01", "CL987739", "E28011DDDDDDDDDDDDDDDD01")}
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/upload", op1Tok(t), map[string]any{"rows": rows}, nil), 400, "VEHICLE_LIST_INVALID")
}

func TestInternalSubmitVehicleList(t *testing.T) {
	reset(t)
	body := map[string]any{"operatorId": "OP02", "actor": map[string]any{"kind": "系统", "id": "opgw"},
		"rows": []any{map[string]any{"brandId": "BR03", "plateNo": "CL900001", "model": "驰路 C5 电单车", "pileTag": "E28011EEEEEEEEEEEEEEEE01"}}}
	r := internalCall(t, "opgw", "/internal/fleet/vehicle-lists", "il-1", body)
	if r.status != 200 || r.body["added"].(float64) != 1 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	body["operatorId"] = "OP99"
	wantCode(t, internalCall(t, "opgw", "/internal/fleet/vehicle-lists", "il-2", body), 404, "NOT_FOUND")
	body["operatorId"] = "OP03"
	body["rows"] = []any{uploadRow("BR04", "JT900001", "E28011EEEEEEEEEEEEEEEE02")}
	wantCode(t, internalCall(t, "opgw", "/internal/fleet/vehicle-lists", "il-3", body), 403, "READONLY")
	body["operatorId"] = "OP02"
	body["rows"] = []any{uploadRow("BR01", "QH900001", "E28011EEEEEEEEEEEEEEEE03")}
	wantCode(t, internalCall(t, "opgw", "/internal/fleet/vehicle-lists", "il-4", body), 400, "VEHICLE_LIST_INVALID")
	delete(body, "actor")
	wantCode(t, internalCall(t, "opgw", "/internal/fleet/vehicle-lists", "il-5", body), 400, "INVALID")
	// 只有登记的调用方能调
	r = call(t, "POST", "/internal/fleet/vehicle-lists", "k-ops", body, map[string]string{"X-Caller": "ride", "Idempotency-Key": "x"})
	if r.status != 401 {
		t.Fatalf("未登记调用方应 401: %d", r.status)
	}
}

func TestUpdateVehicle(t *testing.T) {
	reset(t)
	h := map[string]string{"Idempotency-Key": "u1"}
	r := call(t, "PATCH", "/api/fleet/operator/vehicles/VH000004", op1Tok(t), map[string]any{"model": "青禾 Q5", "pileTag": "e28011ffffffffffffffff01"}, h)
	if r.status != 200 || r.body["model"] != "青禾 Q5" || r.body["pileTag"] != "E28011FFFFFFFFFFFFFFFF01" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if countType(outboxTypes(t), "fleet.vehicle.listed") != 1 {
		t.Fatal("信息更新应发车辆已入清单")
	}
	a := outboxEvents(t, "fleet.audit.recorded")[0]["payload"].(map[string]any)
	if a["before"].(map[string]any)["model"] != "青禾 Q3 电单车" || a["after"].(map[string]any)["model"] != "青禾 Q5" {
		t.Fatalf("留痕应带改动前后: %v", a)
	}
	wantCode(t, call(t, "PATCH", "/api/fleet/operator/vehicles/VH000001", op1Tok(t), map[string]any{"pileTag": "E28011FFFFFFFFFFFFFFFF01"}, nil), 400, "INVALID")
	wantCode(t, call(t, "PATCH", "/api/fleet/operator/vehicles/VH000001", op1Tok(t), map[string]any{"pileTag": "123"}, nil), 400, "INVALID")
	wantCode(t, call(t, "PATCH", "/api/fleet/operator/vehicles/VH000001", op1Tok(t), map[string]any{"model": ""}, nil), 400, "INVALID")
	wantCode(t, call(t, "PATCH", "/api/fleet/operator/vehicles/VH000001", op1Tok(t), map[string]any{}, nil), 400, "INVALID")
	wantCode(t, call(t, "PATCH", "/api/fleet/operator/vehicles/VH000707", op1Tok(t), map[string]any{"model": "x"}, nil), 403, "SCOPE_DENIED")
	wantCode(t, call(t, "PATCH", "/api/fleet/operator/vehicles/VH999999", op1Tok(t), map[string]any{"model": "x"}, nil), 404, "NOT_FOUND")
	// 换识别方式：蓝牙标识为 12 位十六进制
	r = call(t, "PATCH", "/api/fleet/operator/vehicles/VH000001", op1Tok(t), map[string]any{"pileTagType": "蓝牙", "pileTag": "a1:b2:c3:d4:e5:f6"}, nil)
	if r.status != 200 || r.body["pileTag"] != "A1B2C3D4E5F6" || r.body["pileTagType"] != "蓝牙" {
		t.Fatalf("蓝牙标识: %d %s", r.status, r.raw)
	}
}

func TestRemoveVehicle(t *testing.T) {
	reset(t)
	tok := op1Tok(t)
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/VH000004/remove", tok, map[string]any{"reason": "报废"}, nil), 400, "REASON_REQUIRED")
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/VH000110/remove", tok, map[string]any{"reason": "车辆报废处理"}, nil), 409, "CONFLICT")
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/VH000707/remove", tok, map[string]any{"reason": "车辆报废处理"}, nil), 403, "SCOPE_DENIED")
	r := call(t, "POST", "/api/fleet/operator/vehicles/VH000004/remove", tok, map[string]any{"reason": "车辆报废处理"}, map[string]string{"Idempotency-Key": "rm1"})
	if r.status != 200 || r.body["codeStatus"] != "已失效" || r.body["status"] != "码已失效" || r.body["codeInvalidAt"] != "2026-12-01T12:00:00+08:00" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	types := outboxTypes(t)
	if countType(types, "fleet.vehicle.removed") != 1 || countType(types, "fleet.code.status_changed") != 1 || countType(types, "fleet.audit.recorded") != 1 {
		t.Fatalf("事件: %v", types)
	}
	p := outboxEvents(t, "fleet.vehicle.removed")[0]["payload"].(map[string]any)
	if p["govCode"] != "SF86020226" || p["removedAt"] != "2026-12-01T12:00:00+08:00" || p["operatorId"] != "OP01" {
		t.Fatalf("车辆已移除载荷: %v", p)
	}
	c := outboxEvents(t, "fleet.code.status_changed")[0]["payload"].(map[string]any)
	if c["from"] != "已启用" || c["to"] != "已失效" {
		t.Fatalf("码状态变更载荷: %v", c)
	}
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/VH000004/remove", tok, map[string]any{"reason": "车辆报废处理"}, nil), 409, "ALREADY_DONE")
	// 移出后车辆记录保留，政府码可查，车不再计入清单
	r = call(t, "GET", "/api/fleet/gov-codes/SF86020226", govTok(t), nil, nil)
	if r.status != 200 {
		t.Fatal("车辆记录保留")
	}
	r = call(t, "GET", "/api/fleet/brands/BR01", govTok(t), nil, nil)
	if r.body["vehicleCount"].(float64) != 4 {
		t.Fatalf("移出后清单内车辆数减一: %v", r.body["vehicleCount"])
	}
	// 移出车的标识可被新车使用，车号不能复用
	rows := []any{uploadRow("BR01", "QH900020", "E28011C79479F55A38EC7779")}
	if r := call(t, "POST", "/api/fleet/operator/vehicles/upload", tok, map[string]any{"rows": rows}, nil); r.status != 200 {
		t.Fatalf("移出车的标识可复用: %d %s", r.status, r.raw)
	}
	rows = []any{uploadRow("BR01", "QH464022", "E28011C79479F55A38EC7700")}
	wantCode(t, call(t, "POST", "/api/fleet/operator/vehicles/upload", tok, map[string]any{"rows": rows}, nil), 400, "VEHICLE_LIST_INVALID")
}

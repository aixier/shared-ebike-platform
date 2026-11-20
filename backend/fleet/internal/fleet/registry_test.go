package fleet

import (
	"context"
	"testing"
)

func TestSeedEmitsBrandRegistered(t *testing.T) {
	ctx := context.Background()
	reset(t)
	_, _ = testPool.Exec(ctx, `TRUNCATE providers, operators, brands, brand_status_history, vehicles, pauses, counters, outbox RESTART IDENTITY CASCADE`)
	if _, err := testSvc.LoadSeed(ctx, mustReadSeed(t)); err != nil {
		t.Fatal(err)
	}
	evs := outboxEvents(t, "fleet.brand.registered")
	if len(evs) != 6 {
		t.Fatalf("登记 6 个品牌应发 6 条品牌已登记，得到 %d", len(evs))
	}
	var p map[string]any
	for _, e := range evs {
		if e["key"] == "BR05" {
			p = e["payload"].(map[string]any)
		}
	}
	want := map[string]any{"brandId": "BR05", "operatorId": "OP04", "name": "市民骑行", "displayName": "【市民骑行】电单车",
		"operatorName": "示范市公共交通集团有限公司", "operatorShortName": "公交集团车队", "operatorType": "政府自有车队",
		"vehicleType": "电单车", "color": "#0F766E", "accessStatus": "联调中"}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("品牌已登记载荷 %s=%v，期望 %v", k, p[k], v)
		}
	}
	if n := countType(outboxTypes(t), "fleet.vehicle.listed"); n != 10 {
		t.Fatalf("登记的 10 辆车应各发一条车辆已入清单，得到 %d", n)
	}
}

func TestListOperators(t *testing.T) {
	reset(t)
	r := call(t, "GET", "/api/fleet/operators", govTok(t), nil, nil)
	if r.status != 200 || len(r.arr) != 5 {
		t.Fatalf("平台账号应看到 5 家运营商: %d %s", r.status, r.raw)
	}
	op2 := r.arr[1].(map[string]any)
	if op2["operatorId"] != "OP02" || op2["accessVia"] != "车联网服务商代接" {
		t.Fatalf("OP02 不对: %v", op2)
	}
	prov := op2["provider"].(map[string]any)
	if prov["providerId"] != "SP01" || prov["name"] != "联泰车联网科技有限公司" {
		t.Fatalf("代接服务商不对: %v", prov)
	}
	if r.arr[0].(map[string]any)["provider"] != nil {
		t.Fatal("自行接入的 provider 应为 null")
	}
	ids := r.arr[0].(map[string]any)["brandIds"].([]any)
	if len(ids) != 2 || ids[0] != "BR01" || ids[1] != "BR02" {
		t.Fatalf("OP01 旗下品牌应为 BR01、BR02: %v", ids)
	}

	r = call(t, "GET", "/api/fleet/operators?status=已退出", govTok(t), nil, nil)
	if len(r.arr) != 1 || r.arr[0].(map[string]any)["operatorId"] != "OP03" {
		t.Fatalf("按状态筛选: %s", r.raw)
	}
	// 运营商账号只看本家
	r = call(t, "GET", "/api/fleet/operators", op1Tok(t), nil, nil)
	if len(r.arr) != 1 || r.arr[0].(map[string]any)["operatorId"] != "OP01" {
		t.Fatalf("运营商账号只看本家: %s", r.raw)
	}
}

func TestGetOperator(t *testing.T) {
	reset(t)
	r := call(t, "GET", "/api/fleet/operators/OP04", govTok(t), nil, nil)
	if r.status != 200 || r.body["type"] != "政府自有车队" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	wantCode(t, call(t, "GET", "/api/fleet/operators/OP99", govTok(t), nil, nil), 404, "NOT_FOUND")
	wantCode(t, call(t, "GET", "/api/fleet/operators/OP02", op1Tok(t), nil, nil), 403, "SCOPE_DENIED")
	wantCode(t, call(t, "GET", "/api/fleet/operators/OP01", op1Tok(t), nil, nil), 200, "")
}

func TestListBrands(t *testing.T) {
	reset(t)
	r := call(t, "GET", "/api/fleet/brands", govTok(t), nil, nil)
	if len(r.arr) != 6 {
		t.Fatalf("6 个品牌: %s", r.raw)
	}
	b1 := r.arr[0].(map[string]any)
	if b1["brandId"] != "BR01" || b1["operatorName"] != "青禾出行" || b1["accessStatus"] != "已接入" || b1["vehicleCount"].(float64) != 5 {
		t.Fatalf("BR01 不对: %v", b1)
	}
	if b1["statusSince"] != "2026-07-20T16:30:00+08:00" {
		t.Fatalf("时刻应为 RFC 3339 带时区: %v", b1["statusSince"])
	}
	r = call(t, "GET", "/api/fleet/brands?accessStatus=暂停可租", govTok(t), nil, nil)
	if len(r.arr) != 1 || r.arr[0].(map[string]any)["brandId"] != "BR02" {
		t.Fatalf("按状态筛选: %s", r.raw)
	}
	r = call(t, "GET", "/api/fleet/brands?operatorId=OP01", govTok(t), nil, nil)
	if len(r.arr) != 2 {
		t.Fatalf("按运营商筛选: %s", r.raw)
	}
	r = call(t, "GET", "/api/fleet/brands", op1Tok(t), nil, nil)
	if len(r.arr) != 2 {
		t.Fatalf("运营商账号只看本家品牌: %s", r.raw)
	}
	r = call(t, "GET", "/api/fleet/brands?operatorId=OP02", op1Tok(t), nil, nil)
	if len(r.arr) != 0 {
		t.Fatalf("运营商账号查他家品牌应为空: %s", r.raw)
	}
}

func TestGetBrand(t *testing.T) {
	reset(t)
	r := call(t, "GET", "/api/fleet/brands/BR02", govTok(t), nil, nil)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	hist := r.body["statusHistory"].([]any)
	if len(hist) != 5 || hist[4].(map[string]any)["status"] != "暂停可租" {
		t.Fatalf("木棉的状态历史应有 5 条，末条暂停可租: %v", hist)
	}
	p := r.body["pause"].(map[string]any)
	if p["pauseId"] != "OPS-PS-20261130-01" || p["resumedAt"] != nil || p["metric"] != "开锁成功率" || p["value"].(float64) != 0.861 {
		t.Fatalf("当前暂停记录不对: %v", p)
	}
	r = call(t, "GET", "/api/fleet/brands/BR01", govTok(t), nil, nil)
	if r.body["pause"] != nil {
		t.Fatal("没有暂停时 pause 为 null")
	}
	wantCode(t, call(t, "GET", "/api/fleet/brands/BR03", op1Tok(t), nil, nil), 403, "SCOPE_DENIED")
	wantCode(t, call(t, "GET", "/api/fleet/brands/BR99", govTok(t), nil, nil), 404, "NOT_FOUND")
}

func TestGetVehicleAndGovCode(t *testing.T) {
	reset(t)
	r := call(t, "GET", "/api/fleet/gov-codes/SF86020226", govTok(t), nil, nil)
	if r.status != 200 || r.body["vehicleId"] != "VH000004" || r.body["operatorId"] != "OP01" || r.body["brandName"] != "青禾" || r.body["plateNo"] != "QH464022" {
		t.Fatalf("码对得上运营商、品牌、车号: %s", r.raw)
	}
	if r.body["rentable"] != true || r.body["status"] != "可用" || r.body["codeStatus"] != "已启用" {
		t.Fatalf("VH000004 应可租: %s", r.raw)
	}
	// 已失效的码仍可查到
	r = call(t, "GET", "/api/fleet/gov-codes/SF71009215", govTok(t), nil, nil)
	if r.status != 200 || r.body["codeStatus"] != "已失效" || r.body["status"] != "码已失效" || r.body["rentable"] != false {
		t.Fatalf("已失效码: %s", r.raw)
	}
	if r.body["codeInvalidAt"] != "2026-11-20T10:00:00+08:00" {
		t.Fatalf("失效时刻: %v", r.body["codeInvalidAt"])
	}
	wantCode(t, call(t, "GET", "/api/fleet/gov-codes/SF00000000", govTok(t), nil, nil), 404, "CODE_NOT_FOUND")
	wantCode(t, call(t, "GET", "/api/fleet/gov-codes/abc", govTok(t), nil, nil), 404, "CODE_NOT_FOUND")
	wantCode(t, call(t, "GET", "/api/fleet/gov-codes/SF22048694", op1Tok(t), nil, nil), 403, "SCOPE_DENIED")

	r = call(t, "GET", "/api/fleet/vehicles/VH001151", govTok(t), nil, nil)
	if r.body["codeStatus"] != "待启用" || r.body["status"] != "码未启用" || r.body["codeEnableAt"] != "2026-12-08T09:00:00+08:00" {
		t.Fatalf("待启用车: %s", r.raw)
	}
	r = call(t, "GET", "/api/fleet/vehicles/VH000481", govTok(t), nil, nil)
	if r.body["rentable"] != false || r.body["status"] != "可用" {
		t.Fatalf("暂停可租品牌的车不可租: %s", r.raw)
	}
	for _, k := range []string{"battery", "location", "lastReportAt"} {
		if _, has := r.body[k]; has {
			t.Fatalf("车辆不应带 %s: %s", k, r.raw)
		}
	}
	wantCode(t, call(t, "GET", "/api/fleet/vehicles/VH999999", govTok(t), nil, nil), 404, "NOT_FOUND")
	wantCode(t, call(t, "GET", "/api/fleet/vehicles/VH000707", op1Tok(t), nil, nil), 403, "SCOPE_DENIED")
}

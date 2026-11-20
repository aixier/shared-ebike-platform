package fleet

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 内部接口的操作人 kind 四种取值都应被接受（types.go 的 valid() 与契约一致）；
// 同时把"请求体不是 UTF-8"这种客户端编码问题单独说清楚，而不是误报成"操作人缺失或格式不对"。

func TestInternalActorKindsAccepted(t *testing.T) {
	for _, kind := range []string{"人", "智能体", "系统", "用户"} {
		reset(t)
		actor := map[string]any{"kind": kind, "id": "AC0900", "name": "测试"}
		// 每个内部命令都要接受这个 kind；状态不对时返回业务错误（409），但绝不能是 INVALID
		cases := []struct {
			path string
			body map[string]any
		}{
			{"/internal/fleet/brands/BR06/start-integration", map[string]any{"actor": actor}},
			{"/internal/fleet/brands/BR05/go-live", map[string]any{"actor": actor, "reason": "测试上线"}},
			{"/internal/fleet/brands/BR01/pause", map[string]any{"actor": actor, "metric": "人工", "reason": "测试暂停可租"}},
			{"/internal/fleet/brands/BR02/resume", map[string]any{"actor": actor, "reason": "测试恢复可租"}},
		}
		for i, c := range cases {
			caller := map[int]string{0: "onboard", 1: "onboard", 2: "ops", 3: "ops"}[i]
			r := internalCall(t, caller, c.path, "kind-"+kind+"-"+string(rune('a'+i)), c.body)
			if r.status != 200 {
				t.Errorf("kind=%s %s 应成功，得到 %d %s", kind, c.path, r.status, r.raw)
			}
		}
		r := internalCall(t, "opgw", "/internal/fleet/vehicle-lists", "kind-"+kind+"-vl", map[string]any{
			"operatorId": "OP01", "actor": actor,
			"rows": []map[string]any{{"brandId": "BR01", "plateNo": "QH90001", "model": "测试型", "pileTag": "AABBCC01", "pileTagType": "RFID"}},
		})
		if r.status == 400 && r.body["code"] == "INVALID" && strings.Contains(r.body["message"].(string), "操作人") {
			t.Errorf("kind=%s 车辆清单被当成操作人无效: %s", kind, r.raw)
		}
	}
}

// 非 UTF-8 的请求体（Windows 命令行直传中文被转成本地代码页）：说明原因，不再说"操作人缺失"。
func TestNonUTF8BodyExplained(t *testing.T) {
	reset(t)
	gbk := []byte(`{"actor":{"kind":"`)
	gbk = append(gbk, 0xCF, 0xB5, 0xCD, 0xB3) // "系统" 的 GBK 字节
	gbk = append(gbk, []byte(`","id":"AC0900"},"reason":"`)...)
	gbk = append(gbk, 0xB2, 0xE2, 0xCA, 0xD4, 0xC9, 0xCF, 0xCF, 0xDF) // "测试上线"
	gbk = append(gbk, []byte(`"}`)...)
	req := httptest.NewRequest("POST", "/internal/fleet/brands/BR05/go-live", bytes.NewReader(gbk))
	req.Header.Set("Authorization", "Bearer k-onboard")
	req.Header.Set("X-Caller", "onboard")
	req.Header.Set("Idempotency-Key", "gbk-1")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testHandler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "UTF-8") {
		t.Fatalf("非 UTF-8 请求体应 400 并提示 UTF-8，得到 %d %s", w.Code, w.Body.String())
	}
}

func regTok(t *testing.T) string { return govTok(t) }

func post(t *testing.T, path, key string, body any) resp {
	t.Helper()
	return call(t, "POST", path, regTok(t), body, map[string]string{"Idempotency-Key": key})
}

func TestRegisterProviderAndCoverage(t *testing.T) {
	reset(t)
	// 没权限点
	wantCode(t, call(t, "POST", "/api/fleet/providers", op1Tok(t), map[string]any{"name": "某服务商", "reason": "测试登记"}, nil), 403, "FORBIDDEN")
	// 缺事由
	wantCode(t, post(t, "/api/fleet/providers", "p0", map[string]any{"name": "某服务商"}), 400, "REASON_REQUIRED")
	// 缺名称
	wantCode(t, post(t, "/api/fleet/providers", "p0b", map[string]any{"reason": "测试登记"}), 400, "INVALID")

	r := post(t, "/api/fleet/providers", "p1", map[string]any{
		"name": "新联车联网科技有限公司", "contact": map[string]any{"name": "钱＊", "role": "对接人", "phone": "137****0001"},
		"operatorIds": []string{"OP04"}, "reason": "测试登记服务商"})
	if r.status != 200 || r.body["providerId"] != "SP02" {
		t.Fatalf("应顺延发放 SP02: %d %s", r.status, r.raw)
	}
	if ids, _ := r.body["operatorIds"].([]any); len(ids) != 1 || ids[0] != "OP04" {
		t.Fatalf("覆盖应为 OP04: %s", r.raw)
	}
	op := call(t, "GET", "/api/fleet/operators/OP04", govTok(t), nil, nil)
	if op.body["accessVia"] != "车联网服务商代接" || op.body["provider"].(map[string]any)["providerId"] != "SP02" {
		t.Fatalf("OP04 应变为代接: %s", op.raw)
	}
	if n := countType(outboxTypes(t), "fleet.operator.registered"); n != 1 {
		t.Fatalf("覆盖变动应发 1 条运营商已登记，得到 %d", n)
	}
	au := outboxEvents(t, "fleet.audit.recorded")
	if len(au) != 1 || au[0]["key"] != "provider:SP02" || au[0]["payload"].(map[string]any)["action"] != "登记服务商" {
		t.Fatalf("留痕不对: %v", au)
	}
	// 同键重放不重复
	r2 := post(t, "/api/fleet/providers", "p1", map[string]any{"name": "新联车联网科技有限公司", "reason": "测试登记服务商"})
	if string(r2.raw) != string(r.raw) || countType(outboxTypes(t), "fleet.audit.recorded") != 1 {
		t.Fatalf("同键应返回第一次结果且不重复留痕: %s", r2.raw)
	}
	// 覆盖已被别家覆盖的运营商、已退出的运营商、不存在的运营商
	wantCode(t, post(t, "/api/fleet/providers", "p2", map[string]any{"name": "丙", "operatorIds": []string{"OP02"}, "reason": "测试登记服务商"}), 409, "CONFLICT")
	wantCode(t, post(t, "/api/fleet/providers", "p3", map[string]any{"name": "丁", "operatorIds": []string{"OP03"}, "reason": "测试登记服务商"}), 409, "CONFLICT")
	wantCode(t, post(t, "/api/fleet/providers", "p4", map[string]any{"name": "戊", "operatorIds": []string{"OP99"}, "reason": "测试登记服务商"}), 404, "NOT_FOUND")
	// 指定已有编号
	wantCode(t, post(t, "/api/fleet/providers", "p5", map[string]any{"providerId": "SP01", "name": "重复", "reason": "测试登记服务商"}), 409, "ALREADY_DONE")
	// 失败的请求不留下半截数据
	if call(t, "GET", "/api/fleet/providers", govTok(t), nil, nil); len(call(t, "GET", "/api/fleet/providers", govTok(t), nil, nil).arr) != 2 {
		t.Fatal("应只有 SP01、SP02")
	}

	// 更新：换覆盖（去掉 OP04，加上 OP05），OP04 改回自行接入
	u := call(t, "PUT", "/api/fleet/providers/SP02", govTok(t), map[string]any{
		"name": "新联车联网（改名）", "operatorIds": []string{"OP05"}, "reason": "测试更新覆盖"}, map[string]string{"Idempotency-Key": "u1"})
	if u.status != 200 || u.body["name"] != "新联车联网（改名）" {
		t.Fatalf("更新失败: %d %s", u.status, u.raw)
	}
	op4 := call(t, "GET", "/api/fleet/operators/OP04", govTok(t), nil, nil)
	op5 := call(t, "GET", "/api/fleet/operators/OP05", govTok(t), nil, nil)
	if op4.body["accessVia"] != "自行接入" || op4.body["provider"] != nil || op5.body["accessVia"] != "车联网服务商代接" {
		t.Fatalf("覆盖替换不对: OP04=%s OP05=%s", op4.raw, op5.raw)
	}
	if n := countType(outboxTypes(t), "fleet.operator.registered"); n != 3 {
		t.Fatalf("OP04 登记 + OP04 回退 + OP05 变更共 3 条，得到 %d", n)
	}
	wantCode(t, call(t, "PUT", "/api/fleet/providers/SP77", govTok(t), map[string]any{"name": "无", "reason": "测试更新覆盖"}, nil), 404, "NOT_FOUND")

	// 查询
	d := call(t, "GET", "/api/fleet/providers/SP01", govTok(t), nil, nil)
	if d.status != 200 || d.body["operatorIds"].([]any)[0] != "OP02" {
		t.Fatalf("SP01 详情应覆盖 OP02: %s", d.raw)
	}
	wantCode(t, call(t, "GET", "/api/fleet/providers/SP77", govTok(t), nil, nil), 404, "NOT_FOUND")
	wantCode(t, call(t, "GET", "/api/fleet/providers", op1Tok(t), nil, nil), 403, "FORBIDDEN")
}

func TestRegisterOperator(t *testing.T) {
	reset(t)
	base := map[string]any{"name": "远行出行科技有限公司", "shortName": "远行出行", "type": "运营商", "creditCode": "91510100MA6X000001",
		"contacts":          []map[string]any{{"name": "孙＊", "role": "业务负责人", "phone": "136****0001"}},
		"settlementAccount": map[string]any{"bank": "示范市农商银行", "accountName": "远行出行科技有限公司", "account": "6230 **** 0001"},
		"reason":            "测试登记运营商"}
	wantCode(t, call(t, "POST", "/api/fleet/operators", op1Tok(t), base, nil), 403, "FORBIDDEN")
	r := post(t, "/api/fleet/operators", "o1", base)
	if r.status != 200 || r.body["operatorId"] != "OP06" || r.body["status"] != "在营" || r.body["accessVia"] != "自行接入" || r.body["joinedAt"] == nil {
		t.Fatalf("登记运营商: %d %s", r.status, r.raw)
	}
	if ev := outboxEvents(t, "fleet.operator.registered"); len(ev) != 1 || ev[0]["key"] != "OP06" {
		t.Fatalf("应发 1 条运营商已登记: %v", ev)
	}
	au := outboxEvents(t, "fleet.audit.recorded")
	if len(au) != 1 || au[0]["key"] != "operator:OP06" {
		t.Fatalf("留痕不对: %v", au)
	}
	// 同键重放
	if r2 := post(t, "/api/fleet/operators", "o1", base); string(r2.raw) != string(r.raw) {
		t.Fatalf("同键重放应一致: %s", r2.raw)
	}
	if len(call(t, "GET", "/api/fleet/operators", govTok(t), nil, nil).arr) != 6 {
		t.Fatal("同键重放不应多登记")
	}
	// 代接
	b2 := map[string]any{"name": "代接运营商有限公司", "shortName": "代接运营", "type": "运营商", "providerId": "SP01", "reason": "测试登记运营商"}
	r = post(t, "/api/fleet/operators", "o2", b2)
	if r.status != 200 || r.body["accessVia"] != "车联网服务商代接" || r.body["provider"].(map[string]any)["providerId"] != "SP01" {
		t.Fatalf("代接登记: %d %s", r.status, r.raw)
	}
	// 各类错误
	bad := func(key string, mut func(m map[string]any), status int, code string) {
		m := map[string]any{"name": "甲方运营", "shortName": "甲运", "type": "运营商", "reason": "测试登记运营商"}
		mut(m)
		wantCode(t, post(t, "/api/fleet/operators", key, m), status, code)
	}
	bad("e1", func(m map[string]any) { delete(m, "reason") }, 400, "REASON_REQUIRED")
	bad("e2", func(m map[string]any) { m["type"] = "别的" }, 400, "INVALID")
	bad("e3", func(m map[string]any) { delete(m, "shortName") }, 400, "INVALID")
	bad("e4", func(m map[string]any) { m["providerId"] = "SP88" }, 404, "NOT_FOUND")
	bad("e5", func(m map[string]any) { m["operatorId"] = "OP01" }, 409, "ALREADY_DONE")
	bad("e6", func(m map[string]any) { m["operatorId"] = "X1" }, 400, "INVALID")
	// 指定未用的编号
	bad("e7", func(m map[string]any) { m["operatorId"] = "OP20" }, 200, "")
	if r := post(t, "/api/fleet/operators", "e8", map[string]any{"name": "后续运营", "shortName": "后续", "type": "政府自有车队", "reason": "测试登记运营商"}); r.body["operatorId"] != "OP21" {
		t.Fatalf("发号应接在最大编号之后: %s", r.raw)
	}
}

func TestRegisterBrand(t *testing.T) {
	reset(t)
	base := map[string]any{"name": "远行", "operatorId": "OP01", "color": "#123456", "platePrefix": "YX", "reason": "测试登记品牌"}
	wantCode(t, call(t, "POST", "/api/fleet/brands", op1Tok(t), base, nil), 403, "FORBIDDEN")
	r := post(t, "/api/fleet/brands", "b1", base)
	if r.status != 200 || r.body["brandId"] != "BR07" || r.body["accessStatus"] != "未接入" || r.body["operatorName"] != "青禾出行" ||
		r.body["platePrefix"] != "YX" || r.body["vehicleType"] != "电单车" || r.body["displayName"] != "【远行】电单车" {
		t.Fatalf("登记品牌: %d %s", r.status, r.raw)
	}
	if h, _ := r.body["statusHistory"].([]any); len(h) != 1 || h[0].(map[string]any)["status"] != "未接入" {
		t.Fatalf("状态历史应有一条登记: %s", r.raw)
	}
	ev := outboxEvents(t, "fleet.brand.registered")
	if len(ev) != 1 || ev[0]["key"] != "BR07" {
		t.Fatalf("应发品牌已登记: %v", ev)
	}
	p := ev[0]["payload"].(map[string]any)
	if p["operatorId"] != "OP01" || p["accessStatus"] != "未接入" || p["platePrefix"] != "YX" || p["providerId"] != nil {
		t.Fatalf("载荷不对: %v", p)
	}
	if au := outboxEvents(t, "fleet.audit.recorded"); len(au) != 1 || au[0]["key"] != "brand:BR07" {
		t.Fatalf("留痕不对: %v", au)
	}
	// 登记完可以直接开始联调
	if s := internalCall(t, "onboard", "/internal/fleet/brands/BR07/start-integration", "sb7", map[string]any{"actor": map[string]any{"kind": "系统", "id": "AC0900"}}); s.status != 200 || s.body["accessStatus"] != "联调中" {
		t.Fatalf("新品牌开始联调: %d %s", s.status, s.raw)
	}
	// 沿用运营商的代接服务商
	r = post(t, "/api/fleet/brands", "b2", map[string]any{"name": "驰云", "operatorId": "OP02", "displayName": "【驰云】骑行", "reason": "测试登记品牌"})
	if r.status != 200 || r.body["providerId"] != "SP01" || r.body["displayName"] != "【驰云】骑行" {
		t.Fatalf("应沿用 SP01: %d %s", r.status, r.raw)
	}
	// 显式给服务商
	if sp := post(t, "/api/fleet/providers", "bp", map[string]any{"name": "另一服务商", "reason": "测试登记服务商"}); sp.status != 200 {
		t.Fatal(string(sp.raw))
	}
	r = post(t, "/api/fleet/brands", "b3", map[string]any{"name": "云行", "operatorId": "OP01", "providerId": "SP02", "reason": "测试登记品牌"})
	if r.status != 200 || r.body["providerId"] != "SP02" {
		t.Fatalf("显式服务商: %d %s", r.status, r.raw)
	}
	if g := call(t, "GET", "/api/fleet/brands/BR09", govTok(t), nil, nil); g.status != 200 || g.body["providerId"] != "SP02" {
		t.Fatalf("详情应带 providerId: %s", g.raw)
	}
	// 错误
	bad := func(key string, mut func(m map[string]any), status int, code string) {
		m := map[string]any{"name": "乙品牌", "operatorId": "OP01", "reason": "测试登记品牌"}
		mut(m)
		wantCode(t, post(t, "/api/fleet/brands", key, m), status, code)
	}
	bad("x1", func(m map[string]any) { delete(m, "reason") }, 400, "REASON_REQUIRED")
	bad("x2", func(m map[string]any) { delete(m, "name") }, 400, "INVALID")
	bad("x3", func(m map[string]any) { m["operatorId"] = "OP77" }, 404, "NOT_FOUND")
	bad("x4", func(m map[string]any) { m["operatorId"] = "OP03" }, 409, "CONFLICT") // 已退出
	bad("x5", func(m map[string]any) { m["providerId"] = "SP66" }, 404, "NOT_FOUND")
	bad("x6", func(m map[string]any) { m["brandId"] = "BR01" }, 409, "ALREADY_DONE")
	bad("x7", func(m map[string]any) { m["platePrefix"] = "q" }, 400, "INVALID")
	bad("x8", func(m map[string]any) { m["brandId"] = "B1" }, 400, "INVALID")
	// 运营商旗下品牌列表里出现新品牌
	op := call(t, "GET", "/api/fleet/operators/OP01", govTok(t), nil, nil)
	if ids := op.body["brandIds"].([]any); len(ids) != 4 {
		t.Fatalf("OP01 旗下应有 4 个品牌: %v", ids)
	}
}

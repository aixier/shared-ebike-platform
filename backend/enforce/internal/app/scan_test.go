package app

import (
	"net/http"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/healthz", "", nil)
	if r.status != 200 || r.json(t)["ok"] != true {
		t.Fatalf("healthz 不对：%d %s", r.status, r.raw)
	}
}

func TestNotLoggedIn(t *testing.T) {
	e := newEnv(t)
	for _, p := range []struct{ m, path string }{
		{"POST", "/api/enforce/scan"}, {"GET", "/api/enforce/my/evidence"}, {"GET", "/api/enforce/evidence"},
		{"GET", "/api/enforce/evidence-exports"}, {"PUT", "/api/enforce/my/evidence/EV1"},
	} {
		r := e.do(p.m, p.path, "", map[string]any{"code": "x"})
		if r.status != 401 || r.code(t) != "ADMIN_NOT_LOGGED_IN" || r.json(t)["message"] == "" {
			t.Fatalf("%s %s 未登录应 401：%d %s", p.m, p.path, r.status, r.raw)
		}
	}
	r := e.do("POST", "/api/enforce/scan", "garbage", map[string]any{"code": "x"})
	if r.status != 401 {
		t.Fatalf("坏令牌应 401：%d", r.status)
	}
}

func TestScanResults(t *testing.T) {
	e := newEnv(t)
	e.seedPrototype()
	tok := e.officer("AC0503")

	cases := []struct {
		code, result, textHas string
		isGov, capture        bool
	}{
		{"SF86020226", "有效政府码", "政府码有效，属于青禾出行（青禾），接入状态：已接入", true, false},
		{"https://gov.example/v?c=sf27239795&x=1", "码未启用", "政府码尚未启用，属于公交集团车队（市民骑行），接入状态：联调中", true, true},
		{"SF71009215", "码已失效", "政府码已失效，简途共享已退出，可拍照留证", true, true},
		{"随便什么二维码", "无政府码", "未识别到政府码，请拍照取证", false, true},
		{"SF99999999", "无政府码", "未识别到政府码", false, true},
	}
	for _, c := range cases {
		r := e.do("POST", "/api/enforce/scan", tok, map[string]any{"code": c.code})
		if r.status != 200 {
			t.Fatalf("%s：%d %s", c.code, r.status, r.raw)
		}
		m := r.json(t)
		if m["scanResult"] != c.result || m["isGovCode"] != c.isGov || m["needCapture"] != c.capture {
			t.Fatalf("%s：结果不对 %s", c.code, r.raw)
		}
		if !strings.Contains(m["text"].(string), c.textHas) {
			t.Fatalf("%s：文案不对 %s", c.code, m["text"])
		}
		if !c.isGov && m["vehicle"] != nil {
			t.Fatalf("%s：无政府码时 vehicle 应为 null", c.code)
		}
	}

	r := e.do("POST", "/api/enforce/scan", tok, map[string]any{"code": "SF86020226"})
	v := r.json(t)["vehicle"].(map[string]any)
	want := map[string]any{
		"vehicleId": "VH000004", "govCode": "SF86020226", "plateNo": "QH464022", "model": "青禾 Q3 电单车",
		"codeStatus": "已启用", "brandId": "BR01", "brandName": "青禾", "operatorId": "OP01",
		"operatorName": "青禾出行科技有限公司", "operatorShortName": "青禾出行", "operatorStatus": "在营",
		"accessStatus": "已接入", "codeIssuedAt": "2026-07-08T14:20:11+08:00", "codeEnableAt": nil, "codeInvalidAt": nil,
	}
	for k, w := range want {
		if v[k] != w {
			t.Fatalf("vehicle.%s：got %v want %v", k, v[k], w)
		}
	}
	r = e.do("POST", "/api/enforce/scan", tok, map[string]any{"code": "SF27239795"})
	if r.json(t)["vehicle"].(map[string]any)["codeEnableAt"] != "2026-12-08T09:00:00+08:00" {
		t.Fatal("计划启用时刻不对")
	}

	// 每次扫码记一条核查记录
	if n := e.count("SELECT count(*) FROM scan_records WHERE officer_id='AC0503'"); n != 7 {
		t.Fatalf("核查记录应 7 条，得到 %d", n)
	}
}

func TestScanRequiresOfficer(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/enforce/scan", e.supervisor(), map[string]any{"code": "SF86020226"})
	if r.status != 403 || r.code(t) != "FORBIDDEN" || !strings.Contains(r.json(t)["message"].(string), "主管部门管理员") {
		t.Fatalf("非执法人员应 403：%d %s", r.status, r.raw)
	}
}

func TestScanValidation(t *testing.T) {
	e := newEnv(t)
	tok := e.officer("AC0503")
	r := e.do("POST", "/api/enforce/scan", tok, map[string]any{})
	if r.status != http.StatusBadRequest || r.code(t) != "INVALID" {
		t.Fatalf("缺 code 应 400：%d %s", r.status, r.raw)
	}
}

func TestScanIdempotent(t *testing.T) {
	e := newEnv(t)
	e.seedPrototype()
	tok := e.officer("AC0503")
	r1 := e.do("POST", "/api/enforce/scan", tok, map[string]any{"code": "SF86020226"}, "Idempotency-Key", "k-1")
	// 中间车码状态变了，同一个键仍返回第一次的结果
	e.seed(codeChanged("e20", "VH000004", "BR01", "SF86020226", "已启用", "已失效", "2026-12-01T11:00:00+08:00"))
	r2 := e.do("POST", "/api/enforce/scan", tok, map[string]any{"code": "SF86020226"}, "Idempotency-Key", "k-1")
	if string(r1.raw) != string(r2.raw) || r2.status != 200 {
		t.Fatalf("幂等键重复应返回第一次结果：\n%s\n%s", r1.raw, r2.raw)
	}
	if n := e.count("SELECT count(*) FROM scan_records"); n != 1 {
		t.Fatalf("幂等键重复不应再记核查：%d", n)
	}
	// 另一个执法人员用同一个键互不影响
	r3 := e.do("POST", "/api/enforce/scan", e.officer("AC0504"), map[string]any{"code": "SF86020226"}, "Idempotency-Key", "k-1")
	if r3.json(t)["scanResult"] != "码已失效" {
		t.Fatalf("幂等键应按账号隔离：%s", r3.raw)
	}
}

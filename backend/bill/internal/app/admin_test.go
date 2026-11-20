package app

import (
	"testing"
	"time"

	"bike/bill/internal/clients/pay"
)

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	st, m, _ := e.call("GET", "/healthz", "", nil, nil)
	if st != 200 || m["ok"] != true {
		t.Fatalf("%d %v", st, m)
	}
}

func TestAuthErrors(t *testing.T) {
	e := newEnv(t)
	if st, m, _ := e.call("GET", "/api/bill/me/summary", "", nil, nil); st != 401 || m["code"] != "NOT_LOGGED_IN" || m["charged"] != false {
		t.Fatalf("%d %v", st, m)
	}
	if st, m, _ := e.call("GET", "/api/bill/admin/arrears", e.userToken("U1"), nil, nil); st != 401 || m["code"] != "ADMIN_NOT_LOGGED_IN" {
		t.Fatalf("用户令牌不能当管理令牌：%d %v", st, m)
	}
	if st, m, _ := e.call("GET", "/api/bill/admin/arrears", e.adminToken([]string{"order.view"}, nil), nil, nil); st != 403 || m["code"] != "FORBIDDEN" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m, _ := e.call("GET", "/api/bill/admin/arrears", e.adminToken([]string{"arrears.view"}, "OP01"), nil, nil); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("运营商账号不能看用户欠费：%d %v", st, m)
	}
	if st, _, _ := e.call("GET", "/api/bill/me/orders?orderIds=", e.userToken("U1"), nil, nil); st != 400 {
		t.Fatal(st)
	}
}

// 财务欠费清单、待办计数、按支付状态查账单、客服看用户
func TestAdminViews(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U100036", "免押", nil, nil)
	e.depositMode("U100279", "免押", nil, nil)
	e.pay.set("/internal/pay/credit-charges", pay.Result{Result: "失败", FailCode: ptr("DECLINED")})
	e.clock = at("2026-11-01 08:00:00")
	e.must(e.endOrder(endOpt{order: "OD1", user: "U100036", minutes: 10, inStation: true}))
	e.clock = at("2026-11-20 08:00:00")
	e.must(e.endOrder(endOpt{order: "OD2", user: "U100279", minutes: 5, inStation: true, degraded: true}))
	e.must(e.endOrder(endOpt{order: "OD3", user: "U100279", minutes: 4, inStation: true}))
	e.pay.set("/internal/pay/credit-charges", pay.Result{Result: "成功"})
	e.must(e.endOrder(endOpt{order: "OD4", user: "U100279", minutes: 4, inStation: true}))
	e.clock = at("2026-12-01 12:00:00")

	fin := e.adminToken([]string{"arrears.view"}, nil)
	st, m, _ := e.call("GET", "/api/bill/admin/arrears", fin, nil, nil)
	if st != 200 || m["total"].(float64) != 2 {
		t.Fatalf("%d %v", st, m)
	}
	rows := m["rows"].([]any)
	first := rows[0].(map[string]any)
	if first["userId"] != "U100036" || first["days"].(float64) != 30 || first["arrears"].(float64) != 500 {
		t.Fatalf("按欠费天数从多到少：%v", rows)
	}
	second := rows[1].(map[string]any)
	if second["orderCount"].(float64) != 2 || second["degradedOrders"].(float64) != 1 || second["arrears"].(float64) != 450 {
		t.Fatalf("%v", second)
	}
	if tot := m["totals"].(map[string]any); tot["users"].(float64) != 2 || tot["amount"].(float64) != 950 {
		t.Fatalf("%v", tot)
	}
	_, m, _ = e.call("GET", "/api/bill/admin/arrears?sortKey=arrears&sortDir=asc&pageSize=1", fin, nil, nil)
	if r := m["rows"].([]any); len(r) != 1 || r[0].(map[string]any)["userId"] != "U100279" {
		t.Fatalf("%v", m)
	}

	_, _, raw := e.call("GET", "/api/bill/admin/todos", fin, nil, nil)
	if !contains(string(raw), `"count":2`) {
		t.Fatalf("%s", raw)
	}
	_, _, raw = e.call("GET", "/api/bill/admin/todos", e.adminToken([]string{"order.view"}, nil), nil, nil)
	if string(raw) != "[]\n" {
		t.Fatalf("无权限时不返回待办：%s", raw)
	}

	ov := e.adminToken([]string{"order.view"}, nil)
	_, m, _ = e.call("GET", "/api/bill/admin/bills?payStatus=欠费", ov, nil, nil)
	if m["total"].(float64) != 3 {
		t.Fatalf("%v", m)
	}
	_, m, _ = e.call("GET", "/api/bill/admin/bills?from="+urlq("2026-11-10T00:00:00+08:00"), ov, nil, nil)
	if m["total"].(float64) != 3 {
		t.Fatalf("%v", m)
	}
	// 运营商账号只看本运营商
	_, m, _ = e.call("GET", "/api/bill/admin/bills", e.adminToken([]string{"order.view"}, "OP02"), nil, nil)
	if m["total"].(float64) != 0 {
		t.Fatalf("%v", m)
	}
	_, m, _ = e.call("GET", "/api/bill/admin/orders?orderIds=OD1,OD4,ODX", e.adminToken([]string{"order.view"}, "OP01"), nil, nil)
	if len(m["rows"].([]any)) != 2 || len(m["missing"].([]any)) != 1 || m["rows"].([]any)[0].(map[string]any)["userId"] != "U100036" {
		t.Fatalf("%v", m)
	}
	if st, _, _ := e.call("GET", "/api/bill/admin/orders/OD1", e.adminToken([]string{"order.view"}, "OP02"), nil, nil); st != 403 {
		t.Fatal(st)
	}
	if st, _, _ := e.call("GET", "/api/bill/admin/orders/OD1", ov, nil, nil); st != 200 {
		t.Fatal(st)
	}

	cs := e.adminToken([]string{"user.view"}, nil)
	_, m, _ = e.call("GET", "/api/bill/admin/users/U100279/summary", cs, nil, nil)
	if m["arrears"].(float64) != 450 {
		t.Fatalf("%v", m)
	}
	_, m, _ = e.call("GET", "/api/bill/admin/users/U100279/bills?payStatus=欠费", cs, nil, nil)
	if m["total"].(float64) != 2 {
		t.Fatalf("%v", m)
	}
	if st, _, _ := e.call("GET", "/api/bill/admin/users/U999999/summary", cs, nil, nil); st != 404 {
		t.Fatal(st)
	}
	if st, _, _ := e.call("GET", "/api/bill/admin/users/U100279/month-cards", cs, nil, nil); st != 200 {
		t.Fatal(st)
	}
	if st, _, _ := e.call("GET", "/api/bill/admin/users/U100279/coupons", cs, nil, nil); st != 200 {
		t.Fatal(st)
	}

	// 用户批量查：只返回本人的
	_, m, _ = e.call("GET", "/api/bill/me/orders?orderIds=OD2,OD3,OD1", e.userToken("U100279"), nil, nil)
	if len(m["rows"].([]any)) != 2 || m["missing"].([]any)[0] != "OD1" {
		t.Fatalf("%v", m)
	}
	_ = time.Second
}

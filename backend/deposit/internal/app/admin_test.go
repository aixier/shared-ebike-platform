package app

import (
	"testing"
	"time"
)

func (e *env) admin(path string, perms ...string) resp {
	return e.do("GET", path, map[string]string{"Authorization": "Bearer " + adminToken("AC0201", nil, perms...)}, nil)
}

// trace: 任务 11 · R-18 BR-21 专户台账与财务汇总、流水
func TestSummaryAndFlows(t *testing.T) {
	e := newEnv(t)
	e.rule("PR0001", time.Date(2026, 7, 1, 0, 0, 0, 0, loc), 9900, 550)
	e.depositUser("U100004") // 99
	e.depositUser("U100249") // 99
	e.now = e.now.Add(time.Minute)
	e.depositUser("U100054") // 99
	e.now = e.now.Add(time.Minute)
	mustStatus(t, e.deduct("d1", "U100249", "OD1", 2000), 200)
	mustStatus(t, e.user("POST", "/api/deposit/me/topup", "U100249", "t1", nil), 200)
	mustStatus(t, e.deduct("d2", "U100054", "OD2", 1500), 200)
	mustStatus(t, e.user("POST", "/api/deposit/me/refund", "U100004", "r1", nil), 200)

	sm := e.admin("/api/deposit/admin/summary", "deposit.view")
	mustStatus(t, sm, 200)
	l := sm.body["ledger"].(map[string]any)
	if num(l["paidIn"]) != 29700 || num(l["toppedUp"]) != 2000 || num(l["deducted"]) != 3500 || num(l["refunded"]) != 0 {
		t.Fatalf("汇总不对：%s", sm.raw)
	}
	if num(l["balance"]) != 29700+2000-3500 || num(sm.body["userBalanceSum"]) != num(l["balance"]) {
		t.Fatalf("专户余额 = 缴纳+补缴−抵扣−已退，且与用户余额之和一致：%s", sm.raw)
	}
	u := sm.body["users"].(map[string]any)
	if num(u["已缴"]) != 1 || num(u["押金不足"]) != 1 || num(u["退押中"]) != 1 || num(u["已退"]) != 0 {
		t.Fatalf("各状态用户数不对：%s", sm.raw)
	}
	if sm.body["note"] == nil {
		t.Fatal("应带说明")
	}

	f := e.admin("/api/deposit/admin/flows?pageSize=2", "deposit.view")
	mustStatus(t, f, 200)
	if num(f.body["total"]) != 6 || len(f.body["rows"].([]any)) != 2 || num(f.body["page"]) != 1 || num(f.body["pageSize"]) != 2 {
		t.Fatalf("分页不对：%s", f.raw)
	}
	first := f.body["rows"].([]any)[0].(map[string]any)
	if first["createdAt"].(string) < f.body["rows"].([]any)[1].(map[string]any)["createdAt"].(string) {
		t.Fatal("默认按创建时间新到旧")
	}
	f = e.admin("/api/deposit/admin/flows?type=押金抵扣", "deposit.view")
	rows := f.body["rows"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["direction"] != "转" || rows[0].(map[string]any)["channel"] != "押金专户划转" || rows[0].(map[string]any)["orderId"] == nil {
		t.Fatalf("抵扣流水不对：%s", f.raw)
	}
	f = e.admin("/api/deposit/admin/flows?userId=U100249", "deposit.view")
	if num(f.body["total"]) != 3 {
		t.Fatalf("按用户筛选：%s", f.raw)
	}
	from := e.now.Add(-30 * time.Second).Format(time.RFC3339)
	f = e.admin("/api/deposit/admin/flows?from="+urlq(from), "deposit.view")
	if num(f.body["total"]) != 3 {
		t.Fatalf("按时间筛选：%s", f.raw)
	}
	mustCode(t, e.admin("/api/deposit/admin/flows?type=乱写", "deposit.view"), 400, "INVALID")
}

// trace: R-10 客服查用户押金
func TestAdminUserDeposit(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	mustCode(t, e.admin("/api/deposit/admin/users/U404", "user.view"), 404, "NOT_FOUND")
	mustCode(t, e.admin("/api/deposit/admin/users/U404", "deposit.view"), 403, "FORBIDDEN")
	e.verified("U100087")
	r := e.admin("/api/deposit/admin/users/U100087", "user.view")
	mustStatus(t, r, 200)
	if r.body["deposit"].(map[string]any)["mode"] != "未开通" || r.body["canRefund"] != false {
		t.Fatalf("实名用户未开通：%s", r.raw)
	}
	e.depositUser("U100004")
	e.event("bill.bill.issued", "OD1", billIssued("OD1", "U100004", 450, "一键支付"))
	r = e.admin("/api/deposit/admin/users/U100004", "user.view")
	if r.body["deposit"].(map[string]any)["status"] != "已缴" || r.body["canRefund"] != false || len(r.body["refundBlockers"].([]any)) != 1 || r.body["previousDeposit"] != nil {
		t.Fatalf("押金用户有待支付：%s", r.raw)
	}
}

func urlq(s string) string {
	out := ""
	for _, c := range s {
		if c == '+' {
			out += "%2B"
		} else {
			out += string(c)
		}
	}
	return out
}

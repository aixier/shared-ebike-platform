package app

import (
	"testing"

	"bike/deposit/internal/credit"
)

// trace: 任务 6 · R-16 信用免押
func TestOpenCreditFree(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	mustCode(t, e.user("POST", "/api/deposit/me/credit-free", "U100087", "c0", nil), 409, "NEED_ONBOARDING")
	e.verified("U100087")
	r := e.user("POST", "/api/deposit/me/credit-free", "U100087", "c1", map[string]any{"provider": "芝麻信用"})
	mustStatus(t, r, 200)
	if num(r.body["score"]) != 700 || num(r.body["threshold"]) != 550 {
		t.Fatalf("分值与门槛不对：%s", r.raw)
	}
	d := r.body["deposit"].(map[string]any)
	if d["mode"] != "免押" || d["provider"] != "芝麻信用" || d["creditOpenedAt"] == nil {
		t.Fatalf("免押形态不对：%s", r.raw)
	}
	ev := e.lastOf("deposit.status.changed")
	if ev["mode"] != "免押" || ev["rideable"] != true || ev["creditProvider"] != "芝麻信用" || ev["status"] != nil {
		t.Fatalf("状态事件不对：%v", ev)
	}
	mustCode(t, e.user("POST", "/api/deposit/me/credit-free", "U100087", "c2", nil), 409, "ALREADY_DONE")
	// 免押用户不能再缴押金
	mustCode(t, e.user("POST", "/api/deposit/me/payment", "U100087", "p1", nil), 409, "ALREADY_DONE")
}

// trace: R-16 信用分未达门槛
func TestCreditScoreLow(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U100127")
	r := e.user("POST", "/api/deposit/me/credit-free", "U100127", "c1", nil)
	mustCode(t, r, 409, "CREDIT_SCORE_LOW")
	if num(r.body["score"]) != 500 || num(r.body["threshold"]) != 550 || num(r.body["depositAmount"]) != 19900 {
		t.Fatalf("应带分值、门槛与押金金额：%s", r.raw)
	}
	if r.body["next"].(map[string]any)["action"] != "payDeposit" {
		t.Fatal("下一步应为缴押金")
	}
}

// trace: R-16 押金用户改开免押须先退押；退押中的旧押金成为 previousDeposit
func TestCreditFreeNeedsRefundFirst(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U100004")
	mustStatus(t, e.user("POST", "/api/deposit/me/payment", "U100004", "p1", nil), 200)
	r := e.user("POST", "/api/deposit/me/credit-free", "U100004", "c1", nil)
	mustCode(t, r, 409, "REFUND_BLOCKED")
	if r.body["next"].(map[string]any)["action"] != "refundDeposit" {
		t.Fatalf("下一步应为申请退押：%s", r.raw)
	}
	mustStatus(t, e.user("POST", "/api/deposit/me/refund", "U100004", "r1", nil), 200)
	// 退押中可以开通免押，旧押金进 previousDeposit
	mustStatus(t, e.user("POST", "/api/deposit/me/credit-free", "U100004", "c2", nil), 200)
	pg := e.user("GET", "/api/deposit/me", "U100004", "", nil)
	mustStatus(t, pg, 200)
	prev := pg.body["previousDeposit"].(map[string]any)
	if pg.body["mode"] != "免押" || prev["status"] != "退押中" || num(prev["amountPaid"]) != 19900 || prev["refund"] == nil {
		t.Fatalf("旧押金不对：%s", pg.raw)
	}
}

// trace: 信用分查询渠道不可用
func TestCreditUnavailable(t *testing.T) {
	e := newEnv(t)
	e.baseline()
	e.verified("U1")
	e.app.Credit = credit.Unavailable{}
	mustCode(t, e.user("POST", "/api/deposit/me/credit-free", "U1", "c1", nil), 503, "SERVER_BUSY")
	mustCode(t, e.user("POST", "/api/deposit/me/credit-free", "U1", "c2", map[string]any{"provider": "花呗"}), 400, "INVALID")
}

package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"bike/bill/internal/clients/pay"
)

// R-02：购买月卡；有生效卡时顺延为未生效；月卡用于后续订单抵扣
func TestMonthCard_BuyAndExtend(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.realname("U1", "已核验")
	e.depositMode("U1", "免押", nil, nil)
	st, m, _ := e.call("POST", "/api/bill/me/month-cards", e.userToken("U1"), nil, map[string]string{"Idempotency-Key": "c1"})
	if st != 200 || m["status"] != "生效中" || m["price"].(float64) != 2500 || m["perOrderCap"].(float64) != 300 || m["pricingVersionId"] != "PR0002" {
		t.Fatalf("%d %v", st, m)
	}
	first := m["cardId"].(string)
	if e.pay.calls[0].Body["type"] != "月卡购买" || e.pay.calls[0].Body["cardId"] != first || e.pay.calls[0].Body["bizNo"] != first {
		t.Fatalf("%v", e.pay.calls[0].Body)
	}
	cp := e.events("bill.card.purchased")
	if len(cp) != 1 || cp[0]["cardId"] != first || cp[0]["price"].(float64) != 2500 {
		t.Fatalf("%v", cp)
	}
	// 同一幂等键不重复购买
	if st, m2, _ := e.call("POST", "/api/bill/me/month-cards", e.userToken("U1"), nil, map[string]string{"Idempotency-Key": "c1"}); st != 200 || m2["cardId"] != first {
		t.Fatal("幂等键应返回首次结果")
	}
	st, m, _ = e.call("POST", "/api/bill/me/month-cards", e.userToken("U1"), nil, map[string]string{"Idempotency-Key": "c2"})
	if st != 200 || m["status"] != "未生效" || m["startAt"] != "2026-12-31T12:00:00+08:00" || m["endAt"] != "2027-01-30T12:00:00+08:00" {
		t.Fatalf("应顺延：%v", m)
	}
	_, page, _ := e.call("GET", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil)
	if cards := page["cards"].([]any); len(cards) != 2 || cards[0].(map[string]any)["status"] != "未生效" {
		t.Fatalf("%v", page)
	}
	if page["rule"].(map[string]any)["text"] != "30 天内每单骑行费最多抵 ¥3.0（不抵调度费）" {
		t.Fatalf("%v", page["rule"])
	}
	// 后续订单用上月卡
	e.clock = e.clock.Add(time.Hour)
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 10, inStation: true}))
	if b := e.bill("OD1"); b.MonthCardDeduct != 300 || str(b.MonthCardID) != first {
		t.Fatalf("%+v", b)
	}
	_, page, _ = e.call("GET", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil)
	used := page["cards"].([]any)[1].(map[string]any)["usedOrderIds"].([]any)
	if len(used) != 1 || used[0] != "OD1" {
		t.Fatalf("%v", used)
	}
	_, s, _ := e.call("GET", "/api/bill/me/summary", e.userToken("U1"), nil, nil)
	if s["monthCard"].(map[string]any)["cardId"] != first {
		t.Fatalf("%v", s)
	}
}

func TestMonthCard_Rejections(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	if st, m, _ := e.call("POST", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil); st != 409 || m["code"] != "NEED_ONBOARDING" {
		t.Fatalf("未实名：%d %v", st, m)
	}
	e.realname("U1", "已核验")
	e.must(e.deliver("acct.account.frozen", map[string]any{"userId": "U1", "reason": "欠费追缴"}))
	if st, m, _ := e.call("POST", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil); st != 409 || m["code"] != "ACCOUNT_FROZEN" || m["next"] == nil {
		t.Fatalf("冻结：%d %v", st, m)
	}
	e.must(e.deliver("acct.account.unfrozen", map[string]any{"userId": "U1", "mode": "自动解冻"}))
	fr := "用户支付账户余额不足"
	e.pay.set("/internal/pay/one-tap-payments", pay.Result{Result: "失败", FailCode: ptr("DECLINED"), FailReason: &fr})
	if st, m, _ := e.call("POST", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil); st != 409 || m["code"] != "PAY_DECLINED" {
		t.Fatalf("%d %v", st, m)
	}
	// 结果未知：保持支付中，再买返回 CONFLICT；收款成功事件到达后生效
	e.pay.set("/internal/pay/one-tap-payments", pay.Result{Result: "结果未知"})
	if st, m, _ := e.call("POST", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil); st != 409 || m["code"] != "CONFLICT" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m, _ := e.call("POST", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil); st != 409 || m["code"] != "CONFLICT" {
		t.Fatalf("%d %v", st, m)
	}
	var cardID string
	e.must(testPool.QueryRow(context.Background(), `SELECT card_id FROM month_cards WHERE state='支付中'`).Scan(&cardID))
	e.must(e.deliver("pay.payment.succeeded", map[string]any{"paymentId": "PY-C", "bizNo": cardID, "type": "月卡购买", "userId": "U1", "orderId": nil, "amount": 2500, "completedAt": e.clock}))
	_, page, _ := e.call("GET", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil)
	if cards := page["cards"].([]any); len(cards) != 1 || cards[0].(map[string]any)["paymentId"] != "PY-C" {
		t.Fatalf("%v", page)
	}
	if len(e.events("bill.card.purchased")) != 1 {
		t.Fatal("月卡已购买事件")
	}
}

// 月卡支付中超时：重查任务用同一幂等键取最新结果
func TestMonthCard_Retry(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.realname("U1", "已核验")
	e.a.Pay = downPay{}
	if st, _, _ := e.call("POST", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil); st != 409 {
		t.Fatal(st)
	}
	e.a.Pay = pay.New(e.payURL(), "bill-key")
	e.clock = e.clock.Add(time.Minute)
	e.must(e.a.RetryCards(context.Background()))
	var st string
	e.must(testPool.QueryRow(context.Background(), `SELECT state FROM month_cards`).Scan(&st))
	if st != cardPaid {
		t.Fatalf("重查后应已支付：%s", st)
	}
}

// 领券：周五 17:00 至周日开放，每期每人一张；未登录可看但不可领
func TestCoupons_Claim(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.clock = at("2026-12-02 10:00:00") // 周三
	_, _, raw := e.call("GET", "/api/bill/coupon-templates", "", nil, nil)
	if !contains(string(raw), "请先授权登录") {
		t.Fatalf("%s", raw)
	}
	if st, m, _ := e.call("POST", "/api/bill/me/coupons", e.userToken("U1"), map[string]any{"templateId": "CPT02"}, nil); st != 409 || m["code"] != "COUPON_NOT_OPEN" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m, _ := e.call("POST", "/api/bill/me/coupons", e.userToken("U1"), map[string]any{"templateId": "CPT01"}, nil); st != 409 || m["code"] != "COUPON_NOT_OPEN" {
		t.Fatalf("系统发放券不可领：%d %v", st, m)
	}
	if st, _, _ := e.call("POST", "/api/bill/me/coupons", e.userToken("U1"), map[string]any{"templateId": "CPT99"}, nil); st != 404 {
		t.Fatal(st)
	}
	e.clock = at("2026-12-04 18:00:00") // 周五
	st, m, _ := e.call("POST", "/api/bill/me/coupons", e.userToken("U1"), map[string]any{"templateId": "CPT02"}, nil)
	if st != 200 || m["obtainedBy"] != "用户领取" || m["expireAt"] != "2026-12-06T23:59:59+08:00" || m["amount"].(float64) != 200 {
		t.Fatalf("%d %v", st, m)
	}
	if st, m, _ := e.call("POST", "/api/bill/me/coupons", e.userToken("U1"), map[string]any{"templateId": "CPT02"}, nil); st != 409 || m["code"] != "COUPON_LIMIT" {
		t.Fatalf("%d %v", st, m)
	}
	_, _, raw = e.call("GET", "/api/bill/coupon-templates", e.userToken("U1"), nil, nil)
	if !contains(string(raw), "本期已领取") {
		t.Fatalf("%s", raw)
	}
	// 下一期可再领
	e.clock = at("2026-12-11 17:30:00")
	if st, _, _ := e.call("POST", "/api/bill/me/coupons", e.userToken("U1"), map[string]any{"templateId": "CPT02"}, nil); st != 200 {
		t.Fatal(st)
	}
	// 我的券：未使用在前；过期状态按时刻算
	e.clock = at("2026-12-12 10:00:00")
	_, _, raw = e.call("GET", "/api/bill/me/coupons", e.userToken("U1"), nil, nil)
	if !contains(string(raw), "已过期") || !contains(string(raw), "未使用") {
		t.Fatalf("%s", raw)
	}
	_, _, raw = e.call("GET", "/api/bill/me/coupons?status=已过期", e.userToken("U1"), nil, nil)
	if contains(string(raw), "未使用") {
		t.Fatalf("%s", raw)
	}
}

// 当天首次开锁失败发关怀券
func TestCareCoupon(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	fail := func(order string) {
		e.must(e.deliver("ride.order.unlock_failed", map[string]any{"orderId": order, "userId": "U1", "pricingVersionId": "PR0002", "reason": "电量不足", "failedAt": e.clock}))
	}
	fail("OD1")
	fail("OD2")
	var n int
	e.must(testPool.QueryRow(context.Background(), `SELECT count(*) FROM coupons WHERE user_id='U1' AND template_id='CPT04'`).Scan(&n))
	if n != 1 {
		t.Fatalf("当天只发一张，得到 %d", n)
	}
	e.clock = e.clock.Add(24 * time.Hour)
	fail("OD3")
	e.must(testPool.QueryRow(context.Background(), `SELECT count(*) FROM coupons WHERE user_id='U1' AND template_id='CPT04'`).Scan(&n))
	if n != 2 {
		t.Fatalf("第二天再发，得到 %d", n)
	}
	_, s, _ := e.call("GET", "/api/bill/me/summary", e.userToken("U1"), nil, nil)
	if s["usableCouponCount"].(float64) != 2 {
		t.Fatalf("%v", s)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

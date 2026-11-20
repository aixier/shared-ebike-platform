package app

import (
	"context"
	"testing"
	"time"

	"bike/bill/internal/clients/pay"
)

func (e *env) addCard(id, user string, start, end time.Time, cap int64) {
	_, err := testPool.Exec(context.Background(), `INSERT INTO month_cards (card_id, user_id, pricing_version_id, price, per_order_cap, start_at, end_at, state, paid_at, created_at)
		VALUES ($1,$2,'PR0002',2500,$3,$4,$5,'已支付',$4,$4)`, id, user, cap, start, end)
	e.must(err)
}

func (e *env) addCoupon(id, user, name string, amount int64, issued, expire time.Time) {
	_, err := testPool.Exec(context.Background(), `INSERT INTO coupons (coupon_id, user_id, template_id, name, amount, issued_at, expire_at, obtained_by)
		VALUES ($1,$2,'CPT03',$3,$4,$5,$6,'系统发放')`, id, user, name, amount, issued, expire)
	e.must(err)
}

// SCHEMA 4.1 算例端到端：免押用户 18 分钟站外还车，月卡 3 元 + 绿色出行券 3 元，自动扣款 8 元
func TestIssue_CreditFreeAutoCharge(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U100341", "免押", nil, nil)
	e.addCard("MC1", "U100341", at("2026-11-20 00:00:00"), at("2026-12-20 00:00:00"), 300)
	e.addCoupon("CP1", "U100341", "绿色出行券", 300, at("2026-11-25 00:00:00"), at("2026-12-08 23:59:59"))
	d := 230
	e.must(e.endOrder(endOpt{order: "OD2026120100001", user: "U100341", minutes: 18, inStation: false, dist: &d}))

	b := e.bill("OD2026120100001")
	if b.RideFee != 900 || b.DispatchFee != 500 || b.Receivable != 1400 || b.MonthCardDeduct != 300 || b.CouponDeduct != 300 || b.Payable != 800 {
		t.Fatalf("计价不对：%+v", b)
	}
	if b.PayStatus != stPaid || b.ChannelPaid != 800 || str(b.PayMethod) != byCredit || b.pending() {
		t.Fatalf("应已自动扣款：%+v", b)
	}
	if e.pay.count("/internal/pay/credit-charges") != 1 {
		t.Fatal("应扣款一次")
	}
	iss := e.events("bill.bill.issued")
	if len(iss) != 1 || iss[0]["collectBy"] != byCredit || iss[0]["payable"].(float64) != 800 || iss[0]["monthCardId"] != "MC1" {
		t.Fatalf("账单已出事件：%v", iss)
	}
	paid := e.events("bill.bill.paid")
	if len(paid) != 1 || paid[0]["payStatus"] != stPaid || paid[0]["channelPaid"].(float64) != 800 {
		t.Fatalf("已付清事件：%v", paid)
	}
	// 券被用掉
	var used *string
	e.must(testPool.QueryRow(context.Background(), `SELECT used_order_id FROM coupons WHERE coupon_id='CP1'`).Scan(&used))
	if used == nil || *used != "OD2026120100001" {
		t.Fatal("券应记为已用")
	}
	// 用户端费用部分
	st, m, _ := e.call("GET", "/api/bill/me/orders/OD2026120100001", e.userToken("U100341"), nil, nil)
	if st != 200 {
		t.Fatalf("%d %v", st, m)
	}
	bl := m["bill"].(map[string]any)
	if bl["headline"] != "已自动扣款 ¥8.0" || m["statusText"] != "已支付" || len(bl["lines"].([]any)) != 5 || bl["couponName"] != "绿色出行券" {
		t.Fatalf("账单明细：%v", m)
	}
	notes := bl["notes"].([]any)
	if len(notes) != 1 || notes[0] != "站点外还车，含调度费 ¥5.0（距最近站点 230 米）" {
		t.Fatalf("notes=%v", notes)
	}
	if _, ok := m["userId"]; ok {
		t.Fatal("用户端不返回 userId")
	}
	// 别人看不到
	if st, _, _ := e.call("GET", "/api/bill/me/orders/OD2026120100001", e.userToken("U100004"), nil, nil); st != 404 {
		t.Fatalf("别人的订单应 404，得到 %d", st)
	}
}

// 同一订单结束事件重复到达不重复出账、不重复扣款
func TestIssue_Idempotent(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "免押", nil, nil)
	o := endOpt{order: "OD1", user: "U1", minutes: 10, inStation: true}
	e.must(e.endOrder(o))
	e.must(e.endOrder(o)) // 不同事件编号、同一订单
	if e.pay.count("/internal/pay/credit-charges") != 1 || len(e.events("bill.bill.issued")) != 1 {
		t.Fatal("重复的订单结束不应重复出账或扣款")
	}
	// 同一事件编号重复投递
	e.must(e.deliverID("same", "ride.order.ended", map[string]any{"orderId": "OD2", "userId": "U1", "brandId": "BR01", "operatorId": "OP01", "channel": "微信",
		"pricingVersionId": "PR0002", "unlockResult": "成功", "startAt": e.clock.Add(-5 * time.Minute), "endAt": e.clock, "durationSec": 300, "inStation": true}))
	e.must(e.deliverID("same", "ride.order.ended", map[string]any{"orderId": "OD3", "userId": "U1"}))
	var n int
	e.must(testPool.QueryRow(context.Background(), `SELECT count(*) FROM bills`).Scan(&n))
	if n != 2 {
		t.Fatalf("应只有 2 张账单，得到 %d", n)
	}
}

// 规则版本尚未到达：返回错误等待重投，到达后再处理
func TestIssue_WaitsForVersion(t *testing.T) {
	e := newEnv(t)
	e.depositMode("U1", "押金", ptr("已缴"), ptr(int64(19900)))
	if err := e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 5, inStation: true, version: "PR0009"}); err == nil {
		t.Fatal("版本未到时应返回错误让总线重投")
	}
	var n int
	e.must(testPool.QueryRow(context.Background(), `SELECT count(*) FROM inbox WHERE type='ride.order.ended'`).Scan(&n))
	if n != 0 {
		t.Fatal("失败的事件不应记入已处理")
	}
}

// BR-03：超时后才报开锁成功，整单 0 元，无需支付
func TestIssue_ZeroUnlock(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "免押", nil, nil)
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 3, inStation: false, unlock: "超时后成功"}))
	b := e.bill("OD1")
	if b.Payable != 0 || b.BilledMinutes != 0 || b.PayStatus != stNone || b.CollectBy != byNone {
		t.Fatalf("%+v", b)
	}
	if e.pay.count("/internal/pay/credit-charges") != 0 {
		t.Fatal("0 元不扣款")
	}
	if p := e.events("bill.bill.paid"); len(p) != 1 || p[0]["payStatus"] != stNone {
		t.Fatalf("0 元账单也发已付清：%v", p)
	}
	_, m, _ := e.call("GET", "/api/bill/me/orders/OD1", e.userToken("U1"), nil, nil)
	if m["statusText"] != "超时未开锁 · 0 元" || m["bill"].(map[string]any)["headline"] != "本单无需支付" {
		t.Fatalf("%v", m)
	}
}

// 免押扣款失败转欠费；一键补付全部欠费后结清
func TestCreditFailed_ArrearsThenPayAll(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U100036", "免押", nil, nil)
	fr := "用户支付账户余额不足"
	e.pay.set("/internal/pay/credit-charges", pay.Result{Result: "失败", FailCode: ptr("DECLINED"), FailReason: &fr})
	e.must(e.endOrder(endOpt{order: "OD1", user: "U100036", minutes: 10, inStation: true}))
	e.clock = e.clock.Add(time.Hour)
	e.must(e.endOrder(endOpt{order: "OD2", user: "U100036", minutes: 4, inStation: true}))
	b := e.bill("OD1")
	if b.PayStatus != stArrears || str(b.ArrearsCause) != causeChargeFailed {
		t.Fatalf("%+v", b)
	}
	ac := e.events("bill.arrears.created")
	if len(ac) != 2 || ac[1]["userArrearsTotal"].(float64) != 500+200 || ac[0]["cause"] != causeChargeFailed {
		t.Fatalf("欠费产生事件：%v", ac)
	}
	_, s, _ := e.call("GET", "/api/bill/me/summary", e.userToken("U100036"), nil, nil)
	if s["arrears"].(float64) != 700 || len(s["arrearsOrderIds"].([]any)) != 2 {
		t.Fatalf("摘要：%v", s)
	}
	e.pay.set("/internal/pay/one-tap-payments", pay.Result{Result: "成功"})
	st, m, _ := e.call("POST", "/api/bill/me/arrears/pay", e.userToken("U100036"), nil, map[string]string{"Idempotency-Key": "k1"})
	if st != 200 || m["paid"].(float64) != 700 || len(m["bills"].([]any)) != 2 {
		t.Fatalf("%d %v", st, m)
	}
	if e.bill("OD1").PayStatus != stArrearsPaid || e.bill("OD1").ArrearsPaidAt == nil {
		t.Fatal("应为欠费已补付")
	}
	if c := e.events("bill.arrears.cleared"); len(c) != 1 {
		t.Fatalf("应发一次欠费结清：%v", c)
	}
	sum := m["summary"].(map[string]any)
	if sum["arrears"].(float64) != 0 {
		t.Fatal("补付后欠费为 0")
	}
	// 同一幂等键重复到达返回首次结果，不重复付款
	n := e.pay.count("/internal/pay/one-tap-payments")
	st2, m2, _ := e.call("POST", "/api/bill/me/arrears/pay", e.userToken("U100036"), nil, map[string]string{"Idempotency-Key": "k1"})
	if st2 != 200 || m2["paid"].(float64) != 700 || e.pay.count("/internal/pay/one-tap-payments") != n {
		t.Fatal("幂等键应返回首次结果")
	}
	// 没有欠费了
	if st, m, _ := e.call("POST", "/api/bill/me/arrears/pay", e.userToken("U100036"), nil, nil); st != 409 || m["code"] != "NOTHING_TO_PAY" {
		t.Fatalf("%d %v", st, m)
	}
}

// 扣款结果未知：账单保持处理中，不能重复发起；收款成功事件到达后付清
func TestCreditUnknown_ThenSucceededEvent(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "免押", nil, nil)
	e.pay.set("/internal/pay/credit-charges", pay.Result{Result: "结果未知", PaymentID: "PY-X", Status: "处理中"})
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 10, inStation: true}))
	b := e.bill("OD1")
	if b.PayStatus != stPending || str(b.PendingPaymentID) != "PY-X" {
		t.Fatalf("%+v", b)
	}
	_, m, _ := e.call("GET", "/api/bill/me/orders/OD1", e.userToken("U1"), nil, nil)
	if m["bill"].(map[string]any)["canPay"] != false {
		t.Fatal("结果未知时不能支付")
	}
	if st, m, _ := e.call("POST", "/api/bill/me/orders/OD1/pay", e.userToken("U1"), nil, nil); st != 409 || m["code"] != "CONFLICT" {
		t.Fatalf("%d %v", st, m)
	}
	// 结果未知事件不改变什么
	e.must(e.deliver("pay.payment.unknown", map[string]any{"paymentId": "PY-X", "bizNo": b.BillID, "type": "订单扣款", "userId": "U1", "orderId": "OD1", "amount": 500}))
	e.must(e.deliver("pay.payment.succeeded", map[string]any{"paymentId": "PY-X", "bizNo": b.BillID, "type": "订单扣款", "userId": "U1", "orderId": "OD1", "amount": 500}))
	// 重复的成功事件不重复记账
	e.must(e.deliver("pay.payment.succeeded", map[string]any{"paymentId": "PY-X", "bizNo": b.BillID, "type": "订单扣款", "userId": "U1", "orderId": "OD1", "amount": 500}))
	b = e.bill("OD1")
	if b.PayStatus != stPaid || b.ChannelPaid != 500 || b.pending() {
		t.Fatalf("%+v", b)
	}
}

// 扣款调用失败：留待重试任务用同一幂等键补扣
func TestCreditRetry(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "免押", nil, nil)
	real := e.a.Pay
	e.a.Pay = downPay{}
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 10, inStation: true}))
	if b := e.bill("OD1"); b.PayStatus != stPending || str(b.PendingKind) != pendCredit {
		t.Fatalf("%+v", b)
	}
	e.a.Pay = real
	e.clock = e.clock.Add(time.Minute)
	_, _ = testPool.Exec(context.Background(), `UPDATE bills SET updated_at = $1`, e.clock.Add(-time.Minute))
	e.must(e.a.RetryCredit(context.Background()))
	if b := e.bill("OD1"); b.PayStatus != stPaid {
		t.Fatalf("重试后应已扣款：%+v", b)
	}
	if c := e.pay.calls[0]; c.Idem != "credit-"+e.bill("OD1").BillID {
		t.Fatalf("幂等键：%s", c.Idem)
	}
}

type downPay struct{}

func (downPay) ChargeCreditFree(context.Context, string, pay.CreditChargeCommand) (pay.Result, error) {
	return pay.Result{}, context.DeadlineExceeded
}
func (downPay) StartOneTap(context.Context, string, pay.OneTapPaymentCommand) (pay.Result, error) {
	return pay.Result{}, context.DeadlineExceeded
}
func (downPay) Refund(context.Context, string, pay.RefundCommand) (pay.Result, error) {
	return pay.Result{}, context.DeadlineExceeded
}

// 押金用户推送一键支付；期限内付清
func TestOneTap_PayWithinDeadline(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U101256", "押金", ptr("已缴"), ptr(int64(19900)))
	e.must(e.endOrder(endOpt{order: "OD2026113010514", user: "U101256", minutes: 26, inStation: true}))
	b := e.bill("OD2026113010514")
	if b.Payable != 1300 || b.PayStatus != stPending || b.CollectBy != byOneTap || b.PayDeadline == nil || !b.PayDeadline.Equal(e.clock.Add(24*time.Hour)) {
		t.Fatalf("%+v", b)
	}
	if e.pay.count("/internal/pay/credit-charges") != 0 {
		t.Fatal("押金用户不自动扣款")
	}
	_, s, _ := e.call("GET", "/api/bill/me/summary", e.userToken("U101256"), nil, nil)
	if ids := s["unpaidOrderIds"].([]any); len(ids) != 1 {
		t.Fatalf("待支付订单号：%v", s)
	}
	_, m, _ := e.call("GET", "/api/bill/me/orders/OD2026113010514", e.userToken("U101256"), nil, nil)
	bl := m["bill"].(map[string]any)
	if bl["headline"] != "应付 ¥13.0" || bl["canPay"] != true || bl["payDeadline"] != "2026-12-02T12:00:00+08:00" {
		t.Fatalf("%v", bl)
	}
	st, r, _ := e.call("POST", "/api/bill/me/orders/OD2026113010514/pay", e.userToken("U101256"), nil, map[string]string{"Idempotency-Key": "p1"})
	if st != 200 || r["amount"].(float64) != 1300 {
		t.Fatalf("%d %v", st, r)
	}
	if b := e.bill("OD2026113010514"); b.PayStatus != stPaid || str(b.PayMethod) != byOneTap || b.ChannelPaid != 1300 {
		t.Fatalf("%+v", b)
	}
	if e.pay.calls[0].Body["type"] != "订单支付" {
		t.Fatalf("类型：%v", e.pay.calls[0].Body)
	}
	if st, m, _ := e.call("POST", "/api/bill/me/orders/OD2026113010514/pay", e.userToken("U101256"), nil, nil); st != 409 || m["code"] != "NOTHING_TO_PAY" {
		t.Fatalf("%d %v", st, m)
	}
}

// 一键支付被拒
func TestOneTap_Declined(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "押金", ptr("已缴"), ptr(int64(19900)))
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 10, inStation: true}))
	fr := "用户支付账户余额不足"
	e.pay.set("/internal/pay/one-tap-payments", pay.Result{Result: "失败", FailCode: ptr("DECLINED"), FailReason: &fr})
	st, m, _ := e.call("POST", "/api/bill/me/orders/OD1/pay", e.userToken("U1"), nil, nil)
	if st != 409 || m["code"] != "PAY_DECLINED" || m["message"] != "支付没有成功：用户支付账户余额不足" || m["charged"] != false {
		t.Fatalf("%d %v", st, m)
	}
	if b := e.bill("OD1"); b.PayStatus != stPending || b.pending() {
		t.Fatalf("%+v", b)
	}
}

// BR-22：期限届满，押金不足抵扣部分、余下转欠费；事后留痕
func TestDeadline_DepositPartial(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U100054", "押金", ptr("已缴"), ptr(int64(600)))
	e.dep.balance = 600
	e.must(e.endOrder(endOpt{order: "OD1", user: "U100054", minutes: 26, inStation: false})) // 1300 + 500
	e.must(e.a.RunDeadlines(context.Background()))
	if e.dep.calls != 0 {
		t.Fatal("期限未到不抵扣")
	}
	e.clock = e.clock.Add(24*time.Hour + time.Second)
	e.must(e.a.RunDeadlines(context.Background()))
	b := e.bill("OD1")
	if b.DepositDeducted != 600 || b.PayStatus != stArrears || str(b.ArrearsCause) != causeDepositShort || b.unpaid() != 1200 || str(b.PayMethod) != methodDeposit {
		t.Fatalf("%+v", b)
	}
	if b.Payable != b.ChannelPaid+b.DepositDeducted+b.unpaid() {
		t.Fatal("应付恒等式不成立")
	}
	au := e.events("bill.audit.recorded")
	if len(au) != 1 || au[0]["action"] != "抵扣押金" {
		t.Fatalf("留痕：%v", au)
	}
	// deposit 的"押金已抵扣"事件随后到达，不重复记
	e.must(e.deliver("deposit.deducted", map[string]any{"userId": "U100054", "orderId": "OD1", "amount": 600, "balanceAfter": 0}))
	if b := e.bill("OD1"); b.DepositDeducted != 600 {
		t.Fatal("押金抵扣不应重复记")
	}
	e.must(e.a.RunDeadlines(context.Background()))
	if e.dep.calls != 1 {
		t.Fatal("已转欠费的单不再抵扣")
	}
}

// BR-22：押金足额抵扣，状态为押金抵扣
func TestDeadline_DepositFull(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U101891", "押金", ptr("已缴"), ptr(int64(19900)))
	e.dep.balance = 19900
	e.must(e.endOrder(endOpt{order: "OD1", user: "U101891", minutes: 29, inStation: true, version: "PR0001"})) // 150 + 26*40 = 1190
	e.clock = e.clock.Add(25 * time.Hour)
	e.must(e.a.RunDeadlines(context.Background()))
	b := e.bill("OD1")
	if b.Payable != 1190 || b.DepositDeducted != 1190 || b.PayStatus != stDeposit {
		t.Fatalf("%+v", b)
	}
	p := e.events("bill.bill.paid")
	if len(p) != 1 || p[0]["payStatus"] != stDeposit || p[0]["depositDeducted"].(float64) != 1190 {
		t.Fatalf("%v", p)
	}
	_, m, _ := e.call("GET", "/api/bill/me/orders/OD1", e.userToken("U101891"), nil, nil)
	if m["statusText"] != "已从押金抵扣" || m["bill"].(map[string]any)["headline"] != "已从押金抵扣 ¥11.9" {
		t.Fatalf("%v", m)
	}
}

// 期限届满且不是押金用户（如先骑后验新用户）：直接转欠费
func TestDeadline_NoDeposit(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "未开通", nil, nil)
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 5, inStation: true}))
	e.clock = e.clock.Add(25 * time.Hour)
	e.must(e.a.RunDeadlines(context.Background()))
	if b := e.bill("OD1"); b.PayStatus != stArrears || e.dep.calls != 0 || str(b.ArrearsCause) != causeExpired {
		t.Fatalf("%+v", b)
	}
	if ac := e.events("bill.arrears.created"); len(ac) != 1 || ac[0]["cause"] != "期限届满未付" {
		t.Fatalf("欠费产生事件：%v", ac)
	}
}

// 押金抵扣结果未知：保持处理中，下一轮同一幂等键重试
func TestDeadline_DeductUnknownRetried(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "押金", ptr("已缴"), ptr(int64(19900)))
	e.dep.balance = 19900
	e.dep.result = "结果未知"
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 5, inStation: true}))
	e.clock = e.clock.Add(25 * time.Hour)
	e.must(e.a.RunDeadlines(context.Background()))
	if b := e.bill("OD1"); b.PayStatus != stPending || str(b.PendingKind) != pendDeduct {
		t.Fatalf("%+v", b)
	}
	if st, _, _ := e.call("POST", "/api/bill/me/orders/OD1/pay", e.userToken("U1"), nil, nil); st != 409 {
		t.Fatal("抵扣处理中不能支付")
	}
	e.dep.result = "成功"
	e.must(e.a.RunDeadlines(context.Background()))
	if b := e.bill("OD1"); b.PayStatus != stDeposit || b.DepositDeducted != 250 {
		t.Fatalf("%+v", b)
	}
}

// R-14：最早欠费超过参数天数发一次欠费超期；结清后再欠重新计
func TestOverdue(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "免押", nil, nil)
	e.pay.set("/internal/pay/credit-charges", pay.Result{Result: "失败", FailCode: ptr("DECLINED")})
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 5, inStation: true}))
	e.clock = e.clock.Add(29 * 24 * time.Hour)
	e.must(e.a.RunOverdue(context.Background()))
	if len(e.events("bill.arrears.overdue")) != 0 {
		t.Fatal("未满 30 天不发")
	}
	e.clock = e.clock.Add(2 * 24 * time.Hour)
	e.must(e.a.RunOverdue(context.Background()))
	e.must(e.a.RunOverdue(context.Background()))
	ov := e.events("bill.arrears.overdue")
	if len(ov) != 1 || ov[0]["days"].(float64) != 31 || ov[0]["userArrearsTotal"].(float64) != 250 {
		t.Fatalf("%v", ov)
	}
	// 结清后删除本轮记录
	e.pay.set("/internal/pay/one-tap-payments", pay.Result{Result: "成功"})
	if st, _, _ := e.call("POST", "/api/bill/me/arrears/pay", e.userToken("U1"), nil, nil); st != 200 {
		t.Fatal(st)
	}
	var n int
	e.must(testPool.QueryRow(context.Background(), `SELECT count(*) FROM arrears_overdue`).Scan(&n))
	if n != 0 {
		t.Fatal("结清后应结束本轮超期记录")
	}
}

// BR-34：先骑后验核验不通过，降级期间的待支付费用转欠费追缴；之后补到的降级单直接转欠费
func TestPursueAfterRealnameFailed(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U100026", "未开通", nil, nil)
	e.must(e.endOrder(endOpt{order: "OD1", user: "U100026", minutes: 12, inStation: true, degraded: true}))
	e.must(e.endOrder(endOpt{order: "OD2", user: "U100026", minutes: 6, inStation: true}))
	if b := e.bill("OD1"); b.PayStatus != stPending || !b.Degraded || b.CollectBy != byOneTap {
		t.Fatalf("降级单按常态方式收款：%+v", b)
	}
	e.realname("U100026", "未通过")
	if b := e.bill("OD1"); b.PayStatus != stArrears || str(b.ArrearsCause) != causePursue {
		t.Fatalf("%+v", b)
	}
	if b := e.bill("OD2"); b.PayStatus != stPending {
		t.Fatal("非降级单不在追缴范围")
	}
	e.must(e.endOrder(endOpt{order: "OD3", user: "U100026", minutes: 6, inStation: true, degraded: true}))
	if b := e.bill("OD3"); b.PayStatus != stArrears || str(b.ArrearsCause) != causePursue {
		t.Fatalf("%+v", b)
	}
	ac := e.events("bill.arrears.created")
	if len(ac) != 2 || ac[0]["degraded"] != true {
		t.Fatalf("%v", ac)
	}
}

// 骑行中实时估算：按开锁时刻版本与当前生效月卡
func TestEstimate(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.addCard("MC1", "U1", at("2026-11-20 00:00:00"), at("2026-12-20 00:00:00"), 300)
	start := e.clock.Add(-(17*time.Minute + 30*time.Second)).Format(time.RFC3339)
	st, m, _ := e.call("GET", "/api/bill/me/orders/OD9/estimate?pricingVersionId=PR0002&startAt="+urlq(start), e.userToken("U1"), nil, nil)
	if st != 200 || m["liveBilledMinutes"].(float64) != 18 || m["liveRideFee"].(float64) != 900 || m["liveMonthCardDeduct"].(float64) != 300 || m["liveEstimate"].(float64) != 600 {
		t.Fatalf("%d %v", st, m)
	}
	if st, _, _ := e.call("GET", "/api/bill/me/orders/OD9/estimate?pricingVersionId=PR0099&startAt="+urlq(start), e.userToken("U1"), nil, nil); st != 404 {
		t.Fatal(st)
	}
	if st, _, _ := e.call("GET", "/api/bill/me/orders/OD9/estimate", e.userToken("U1"), nil, nil); st != 400 {
		t.Fatal(st)
	}
}

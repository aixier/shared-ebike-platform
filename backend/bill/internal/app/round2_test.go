package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"bike/bill/internal/clients/pay"
)

// 第二轮变更：一键支付两步化、注册与批量发券、开锁状态、READONLY、UTF-8、退出运营商。

const intentPath = "/internal/pay/one-tap-payments" // fakePay 把新旧发起支付的路径记在同一个键下

func params(s string) json.RawMessage { return json.RawMessage(s) }

// 第一步拿调起参数，重复调用同一份；支付成功事件到达后才付清
func TestPayIntent_TwoStep(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "押金", ptr("已缴"), ptr(int64(19900)))
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 10, inStation: true}))
	e.pay.set(intentPath, pay.Result{Result: "结果未知", PaymentID: "PY-I1", Status: "待支付", PayParams: params(`{"prepayId":"wx123","nonceStr":"abc"}`)})

	st, m, _ := e.call("POST", "/api/bill/me/orders/OD1/pay-intent", e.userToken("U1"), nil, map[string]string{"Idempotency-Key": "i1"})
	if st != 200 || m["paid"] != false || m["paymentId"] != "PY-I1" || m["amount"].(float64) != 500 || m["orderId"] != "OD1" {
		t.Fatalf("%d %v", st, m)
	}
	pp, _ := m["payParams"].(map[string]any)
	if pp["prepayId"] != "wx123" {
		t.Fatalf("调起参数要原样透传：%v", m["payParams"])
	}
	if bl := m["bill"].(map[string]any)["bill"].(map[string]any); bl["payStatus"] != stPending || bl["canPay"] != true {
		t.Fatalf("发起后账单仍是待支付且还能再取调起参数：%v", bl)
	}
	b := e.bill("OD1")
	if b.PayStatus != stPending || !b.hasIntent() {
		t.Fatalf("%+v", b)
	}
	// 重复调用（不带幂等键）返回同一份，不再调 pay
	n := e.pay.count(intentPath)
	st, m2, _ := e.call("POST", "/api/bill/me/orders/OD1/pay-intent", e.userToken("U1"), nil, nil)
	if st != 200 || m2["paymentId"] != "PY-I1" || e.pay.count(intentPath) != n {
		t.Fatalf("%d %v 调用次数 %d→%d", st, m2, n, e.pay.count(intentPath))
	}
	// 旧路径同一处理
	if st, m3, _ := e.call("POST", "/api/bill/me/orders/OD1/pay", e.userToken("U1"), nil, nil); st != 200 || m3["paymentId"] != "PY-I1" {
		t.Fatalf("旧路径：%d %v", st, m3)
	}
	// 支付成功事件到达才付清
	e.must(e.deliver("pay.payment.succeeded", map[string]any{"paymentId": "PY-I1", "bizNo": b.BillID, "type": "订单支付", "userId": "U1", "orderId": "OD1", "amount": 500, "completedAt": e.clock}))
	b = e.bill("OD1")
	if b.PayStatus != stPaid || b.ChannelPaid != 500 || b.pending() || str(b.PayMethod) != byOneTap {
		t.Fatalf("%+v", b)
	}
	if p := e.events("bill.bill.paid"); len(p) != 1 {
		t.Fatalf("%v", p)
	}
	if st, m, _ := e.call("POST", "/api/bill/me/orders/OD1/pay-intent", e.userToken("U1"), nil, nil); st != 409 || m["code"] != "NOTHING_TO_PAY" {
		t.Fatalf("%d %v", st, m)
	}
}

// 支付失败事件放开该单；调起参数过期的任务也放开
func TestPayIntent_FailedAndExpired(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "押金", ptr("已缴"), ptr(int64(19900)))
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 10, inStation: true}))
	e.pay.set(intentPath, pay.Result{Result: "结果未知", PaymentID: "PY-I1", PayParams: params(`{"a":1}`)})
	if st, _, _ := e.call("POST", "/api/bill/me/orders/OD1/pay-intent", e.userToken("U1"), nil, nil); st != 200 {
		t.Fatal(st)
	}
	b := e.bill("OD1")
	e.must(e.deliver("pay.payment.failed", map[string]any{"paymentId": "PY-I1", "bizNo": b.BillID, "type": "订单支付", "userId": "U1", "orderId": "OD1", "amount": 500, "failReason": "用户取消"}))
	if b := e.bill("OD1"); b.pending() || b.PayStatus != stPending {
		t.Fatalf("支付失败应放开：%+v", b)
	}
	// 再发起一次，这次不处理，过期后由任务放开
	e.pay.set(intentPath, pay.Result{Result: "结果未知", PaymentID: "PY-I2", PayParams: params(`{"a":2}`)})
	e.clock = e.clock.Add(time.Minute) // 新的幂等键由随机 attempt 产生
	if st, m, _ := e.call("POST", "/api/bill/me/orders/OD1/pay-intent", e.userToken("U1"), nil, nil); st != 200 || m["paymentId"] != "PY-I2" {
		t.Fatalf("%d %v", st, m)
	}
	e.must(e.a.ReleaseStaleIntents(context.Background()))
	if !e.bill("OD1").hasIntent() {
		t.Fatal("未过期不放开")
	}
	e.clock = e.clock.Add(intentTTL + time.Second)
	e.must(e.a.ReleaseStaleIntents(context.Background()))
	if b := e.bill("OD1"); b.pending() {
		t.Fatalf("过期应放开：%+v", b)
	}
	// 放开后迟到的成功事件照常入账
	e.must(e.deliver("pay.payment.succeeded", map[string]any{"paymentId": "PY-I2", "bizNo": b.BillID, "type": "订单支付", "userId": "U1", "orderId": "OD1", "amount": 500}))
	if b := e.bill("OD1"); b.PayStatus != stPaid {
		t.Fatalf("%+v", b)
	}
}

// 欠费补付两步：每单一份调起参数
func TestArrearsPayIntent(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "免押", nil, nil)
	e.pay.set("/internal/pay/credit-charges", pay.Result{Result: "失败", FailCode: ptr("DECLINED")})
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 10, inStation: true}))
	e.must(e.endOrder(endOpt{order: "OD2", user: "U1", minutes: 5, inStation: true}))
	e.pay.set(intentPath, pay.Result{Result: "结果未知", PayParams: params(`{"prepayId":"p"}`)})
	st, m, _ := e.call("POST", "/api/bill/me/arrears/pay-intent", e.userToken("U1"), nil, map[string]string{"Idempotency-Key": "a1"})
	if st != 200 || m["paid"].(float64) != 0 || len(m["bills"].([]any)) != 0 || len(m["intents"].([]any)) != 2 {
		t.Fatalf("%d %v", st, m)
	}
	for _, it := range m["intents"].([]any) {
		if it.(map[string]any)["payParams"] == nil {
			t.Fatalf("每单应带调起参数：%v", it)
		}
	}
	if m["summary"].(map[string]any)["arrears"].(float64) != float64(e.bill("OD1").unpaid()+e.bill("OD2").unpaid()) || e.bill("OD2").unpaid() != 250 {
		t.Fatalf("尚未付，欠费不变：%v", m["summary"])
	}
	// 两单的支付成功事件到达后结清
	for i, id := range []string{"OD1", "OD2"} {
		b := e.bill(id)
		e.must(e.deliver("pay.payment.succeeded", map[string]any{"paymentId": b.BillID + "-pay", "bizNo": b.BillID, "type": "欠费补付", "userId": "U1", "orderId": id, "amount": b.unpaid()}))
		if e.bill(id).PayStatus != stArrearsPaid {
			t.Fatalf("第 %d 单：%+v", i, e.bill(id))
		}
	}
	if c := e.events("bill.arrears.cleared"); len(c) != 1 {
		t.Fatalf("%v", c)
	}
}

// 月卡购买两步：先待支付，款到才生效
func TestMonthCard_TwoStep(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.realname("U1", "已核验")
	e.pay.set(intentPath, pay.Result{Result: "结果未知", PaymentID: "PY-C1", PayParams: params(`{"prepayId":"card"}`)})
	st, m, _ := e.call("POST", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil)
	if st != 200 || m["payState"] != "待支付" || m["status"] != "未生效" || m["payParams"].(map[string]any)["prepayId"] != "card" {
		t.Fatalf("%d %v", st, m)
	}
	_, page, _ := e.call("GET", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil)
	if len(page["cards"].([]any)) != 0 {
		t.Fatalf("款未到不应出现在我的月卡：%v", page)
	}
	// 再买返回同一张待支付卡
	st, m2, _ := e.call("POST", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil)
	if st != 200 || m2["cardId"] != m["cardId"] {
		t.Fatalf("%d %v", st, m2)
	}
	if len(e.events("bill.card.purchased")) != 0 {
		t.Fatal("款未到不发月卡已购买")
	}
	e.must(e.deliver("pay.payment.succeeded", map[string]any{"paymentId": "PY-C1", "bizNo": m["cardId"], "type": "月卡购买", "userId": "U1", "amount": 2500, "completedAt": e.clock}))
	_, page, _ = e.call("GET", "/api/bill/me/month-cards", e.userToken("U1"), nil, nil)
	cards := page["cards"].([]any)
	if len(cards) != 1 || cards[0].(map[string]any)["payState"] != "已支付" || cards[0].(map[string]any)["payParams"] != nil {
		t.Fatalf("%v", page)
	}
	if len(e.events("bill.card.purchased")) != 1 {
		t.Fatal("月卡已购买事件")
	}
}

// 模拟渠道直接返回成功：paid 为 true、payParams 为空，账单已付清
func TestPayIntent_MockImmediate(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "押金", ptr("已缴"), ptr(int64(19900)))
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 10, inStation: true}))
	st, m, _ := e.call("POST", "/api/bill/me/orders/OD1/pay-intent", e.userToken("U1"), nil, nil)
	if st != 200 || m["paid"] != true || m["payParams"] != nil || e.bill("OD1").PayStatus != stPaid {
		t.Fatalf("%d %v", st, m)
	}
}

/* ---------------- 发券 ---------------- */

func (e *env) publishCoupons(id string, effective string, tpls []map[string]any) {
	e.must(e.deliver("rule.version.published", map[string]any{
		"versionId": id, "name": id, "effectiveAt": at(effective), "previousVersionId": nil,
		"ride":        map[string]any{"vehicleType": "电单车", "startFee": 150, "startMinutes": 3, "perMinute": 50},
		"dispatchFee": 500, "monthCard": map[string]any{"price": 2500, "days": 30, "perOrderCap": 300, "allocation": "按骑行单数"},
		"coupons": tpls, "deposit": map[string]any{"amount": 19900},
	}))
}

func (e *env) couponsOf(user string) map[string]int {
	rows, err := testPool.Query(context.Background(), `SELECT template_id, count(*) FROM coupons WHERE user_id=$1 GROUP BY template_id`, user)
	e.must(err)
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		e.must(rows.Scan(&id, &n))
		out[id] = n
	}
	return out
}

func (e *env) register(user, channel string) {
	e.must(e.deliver("acct.user.registered", map[string]any{"userId": user, "channel": channel, "registeredAt": e.clock, "duringDegrade": false}))
}

func TestRegistrationCoupons_ByChannel(t *testing.T) {
	e := newEnv(t)
	e.publishCoupons("PR0003", "2026-10-15 00:00:00", []map[string]any{
		{"templateId": "CPT01", "name": "新用户体验券", "amount": 200, "validDays": 7, "issue": "注册后自动发放", "perUser": 1},
		{"templateId": "CPT05", "name": "微信新人券", "amount": 300, "audience": "微信新用户", "validDays": 7, "issue": "注册后自动发放", "perUser": 2},
		{"templateId": "CPT06", "name": "支付宝新人券", "amount": 300, "audience": "支付宝新用户", "validDays": 7, "issue": "注册后自动发放", "perUser": 1},
		{"templateId": "CPT02", "name": "周末骑行券", "amount": 200, "validDays": 3, "issue": "用户领券中心领取（每周五 17:00 开放）", "perUser": 1},
	})
	e.register("U1", "微信")
	e.register("U2", "支付宝")
	if got := e.couponsOf("U1"); len(got) != 2 || got["CPT01"] != 1 || got["CPT05"] != 2 {
		t.Fatalf("微信用户：%v", got)
	}
	if got := e.couponsOf("U2"); len(got) != 2 || got["CPT01"] != 1 || got["CPT06"] != 1 {
		t.Fatalf("支付宝用户：%v", got)
	}
	// 重复事件不重复发；事件重投（新 eventId）也不重复发
	e.register("U1", "微信")
	if got := e.couponsOf("U1"); got["CPT01"] != 1 || got["CPT05"] != 2 {
		t.Fatalf("不应重复发：%v", got)
	}
	p, _ := getProfile(context.Background(), testPool, "U1")
	if str(p.Channel) != "微信" {
		t.Fatalf("渠道应记入投影：%+v", p)
	}
	// 发的券到期日按版本模板的有效天数
	_, cs, raw := e.call("GET", "/api/bill/me/coupons", e.userToken("U2"), nil, nil)
	_ = cs
	var list []map[string]any
	_ = json.Unmarshal(raw, &list)
	if len(list) != 2 || list[0]["obtainedBy"] != "系统发放" || list[0]["status"] != "未使用" {
		t.Fatalf("%s", raw)
	}
	// 券模板接口带发放类别
	_, _, raw = e.call("GET", "/api/bill/coupon-templates", "", nil, nil)
	var ts []map[string]any
	_ = json.Unmarshal(raw, &ts)
	kinds := map[string]string{}
	for _, x := range ts {
		kinds[x["templateId"].(string)] = x["kind"].(string)
	}
	if kinds["CPT01"] != "注册券" || kinds["CPT05"] != "渠道券" || kinds["CPT02"] != "领取券" {
		t.Fatalf("%v", kinds)
	}
}

// 规则版本晚于注册到达：定时任务补发
func TestRegistrationCoupons_RuleArrivesLater(t *testing.T) {
	e := newEnv(t)
	e.register("U1", "微信")
	if got := e.couponsOf("U1"); len(got) != 0 {
		t.Fatalf("没有规则时不能发：%v", got)
	}
	e.publishCoupons("PR0003", "2026-10-15 00:00:00", []map[string]any{
		{"templateId": "CPT01", "name": "新用户体验券", "amount": 200, "validDays": 7, "issue": "注册后自动发放", "perUser": 1},
	})
	e.must(e.a.RunCouponBatches(context.Background()))
	e.must(e.a.RunCouponBatches(context.Background()))
	if got := e.couponsOf("U1"); got["CPT01"] != 1 {
		t.Fatalf("%v", got)
	}
}

// 系统券（版本生效后一次性）与定时券（到点一次性）：每个模板只发一批，已注销与到点前的不发
func TestCouponBatches_SystemAndScheduled(t *testing.T) {
	e := newEnv(t)
	e.publishCoupons("PR0003", "2026-10-15 00:00:00", []map[string]any{
		{"templateId": "CPT07", "name": "系统赠券", "amount": 100, "validDays": 7, "issue": "系统一次性发放", "perUser": 1},
		{"templateId": "CPT08", "name": "元旦赠券", "amount": 500, "validDays": 7, "issue": "2027-01-01 00:00 自动发放", "perUser": 1},
	})
	e.depositMode("U1", "免押", nil, nil)
	e.depositMode("U2", "免押", nil, nil)
	e.depositMode("U3", "免押", nil, nil)
	e.must(e.deliver("acct.account.cancelled", map[string]any{"userId": "U3"}))

	e.must(e.a.RunCouponBatches(context.Background()))
	if got := e.couponsOf("U1"); got["CPT07"] != 1 || got["CPT08"] != 0 {
		t.Fatalf("系统券到点即发，定时券未到点：%v", got)
	}
	if got := e.couponsOf("U3"); len(got) != 0 {
		t.Fatalf("已注销不发：%v", got)
	}
	e.must(e.a.RunCouponBatches(context.Background()))
	if got := e.couponsOf("U2"); got["CPT07"] != 1 {
		t.Fatalf("不重复发：%v", got)
	}
	e.depositMode("U4", "免押", nil, nil) // 批次之后才出现的用户拿不到"一次性"券
	e.clock = at("2027-01-01 00:00:30")
	e.must(e.a.RunCouponBatches(context.Background()))
	e.must(e.a.RunCouponBatches(context.Background()))
	if got := e.couponsOf("U1"); got["CPT07"] != 1 || got["CPT08"] != 1 {
		t.Fatalf("到点发定时券：%v", got)
	}
	if got := e.couponsOf("U4"); got["CPT07"] != 0 || got["CPT08"] != 1 {
		t.Fatalf("系统券一次性，定时券到点时在库的都发：%v", got)
	}
	var issued int
	e.must(testPool.QueryRow(context.Background(), `SELECT issued FROM coupon_batches WHERE template_id='CPT08'`).Scan(&issued))
	if issued != 3 {
		t.Fatalf("定时券发了 %d 张", issued)
	}
}

/* ---------------- ride 开锁状态 ---------------- */

func TestUnlockFailed_NoBill(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "免押", nil, nil)
	e.must(e.deliver("ride.order.unlock_failed", map[string]any{"orderId": "OD1", "userId": "U1", "pricingVersionId": "PR0002", "reason": "车辆无响应", "failedAt": e.clock}))
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 5, inStation: true}))
	var n int
	e.must(testPool.QueryRow(context.Background(), `SELECT count(*) FROM bills WHERE order_id='OD1'`).Scan(&n))
	if n != 0 || e.pay.count("/internal/pay/credit-charges") != 0 {
		t.Fatalf("开锁失败的订单不收费，账单 %d 张", n)
	}
	var reason *string
	e.must(testPool.QueryRow(context.Background(), `SELECT reason FROM order_unlock_states WHERE order_id='OD1'`).Scan(&reason))
	if str(reason) != "车辆无响应" {
		t.Fatalf("%v", reason)
	}
}

// 超时后才报开锁成功：先有开锁失败，再有已开锁与结束，按 0 元出账
func TestUnlockLate_ZeroBill(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "免押", nil, nil)
	e.must(e.deliver("ride.order.unlock_failed", map[string]any{"orderId": "OD1", "userId": "U1", "pricingVersionId": "PR0002", "reason": "车辆无响应", "failedAt": e.clock}))
	e.must(e.deliver("ride.order.unlocked", map[string]any{"orderId": "OD1", "userId": "U1", "startAt": e.clock}))
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 8, inStation: true, unlock: "超时后成功"}))
	if b := e.bill("OD1"); b.Payable != 0 || !b.ZeroUnlock || b.PayStatus != stNone {
		t.Fatalf("%+v", b)
	}
	// 乱序：已开锁之后迟到的失败事件不改回失败
	e.must(e.deliver("ride.order.unlock_failed", map[string]any{"orderId": "OD2", "userId": "U1", "pricingVersionId": "PR0002", "failedAt": e.clock}))
	e.must(e.deliver("ride.order.unlocked", map[string]any{"orderId": "OD3", "userId": "U1", "startAt": e.clock}))
	e.must(e.deliver("ride.order.unlock_failed", map[string]any{"orderId": "OD3", "userId": "U1", "pricingVersionId": "PR0002", "failedAt": e.clock}))
	e.must(e.endOrder(endOpt{order: "OD3", user: "U1", minutes: 8, inStation: true}))
	if b := e.bill("OD3"); b.Payable == 0 {
		t.Fatalf("已开锁的订单应照常计费：%+v", b)
	}
}

func TestPendingManual_StillBilled(t *testing.T) {
	e := newEnv(t)
	e.seedRules()
	e.depositMode("U1", "押金", ptr("已缴"), ptr(int64(19900)))
	e.must(e.deliver("ride.order.pending_manual", map[string]any{"orderId": "OD1", "userId": "U1", "missing": []string{"锁车事实"}}))
	if st, _ := unlockStateOf(context.Background(), testPool, "OD1"); st != ulManual {
		t.Fatalf("%s", st)
	}
	e.must(e.deliver("ride.order.unlocked", map[string]any{"orderId": "OD1", "userId": "U1", "startAt": e.clock}))
	if st, _ := unlockStateOf(context.Background(), testPool, "OD1"); st != ulOK {
		t.Fatalf("%s", st)
	}
	e.must(e.deliver("ride.order.pending_manual", map[string]any{"orderId": "OD1", "userId": "U1"}))
	if st, _ := unlockStateOf(context.Background(), testPool, "OD1"); st != ulOK {
		t.Fatalf("已开锁不降级为待人工：%s", st)
	}
	e.must(e.endOrder(endOpt{order: "OD1", user: "U1", minutes: 5, inStation: true}))
	if e.bill("OD1").Payable == 0 {
		t.Fatal("补到结束事实后照常出账")
	}
}

/* ---------------- 只读与退出 ---------------- */

func (e *env) adminTokenFull(perms []string, scope any, readonly bool) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "bike-iam", "sub": "AC0902", "name": "财务", "role": "财务", "client": "管理端",
		"perms": perms, "scope": scope, "readonly": readonly, "exp": time.Now().Add(time.Hour).Unix()})
	tok.Header["kid"] = "a1"
	s, _ := tok.SignedString(e.admPriv)
	return s
}

func TestAdminWrite_Readonly(t *testing.T) {
	e := newEnv(t)
	// 管理端写（POST、PUT、PATCH、DELETE）：只读令牌一律 403 READONLY；读不受影响
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		st, body, _ := e.call(m, "/api/bill/admin/anything", e.adminTokenFull([]string{"arrears.view"}, nil, true), map[string]any{"x": 1}, nil)
		if st != 403 || body["code"] != "READONLY" {
			t.Fatalf("%s：%d %v", m, st, body)
		}
	}
	if st, _, _ := e.call("GET", "/api/bill/admin/arrears", e.adminTokenFull([]string{"arrears.view"}, nil, true), nil, nil); st != 200 {
		t.Fatalf("只读令牌仍可读：%d", st)
	}
	// 运营商退出后，该 scope 的写操作拒绝，别的运营商不受影响；令牌里的 readonly 何时刷新都不依赖
	e.must(e.deliver("fleet.operator.exited", map[string]any{"operatorId": "OP01", "brandIds": []string{"BR01"}, "exitedAt": e.clock, "readonlyUntil": e.clock.AddDate(0, 3, 0), "reason": "线下结果"}))
	st, body, _ := e.call("POST", "/api/bill/admin/anything", e.adminTokenFull([]string{"arrears.view"}, "OP01", false), map[string]any{"x": 1}, nil)
	if st != 403 || body["code"] != "READONLY" {
		t.Fatalf("已退出运营商：%d %v", st, body)
	}
	if st, body, _ := e.call("POST", "/api/bill/admin/anything", e.adminTokenFull([]string{"arrears.view"}, "OP02", false), map[string]any{"x": 1}, nil); st == 403 {
		t.Fatalf("别的运营商不受影响：%d %v", st, body)
	}
	if st, _, _ := e.call("POST", "/api/bill/admin/anything", "", nil, nil); st != 401 {
		t.Fatalf("没令牌：%d", st)
	}
	// 重复的退出事件幂等
	e.must(e.deliver("fleet.operator.exited", map[string]any{"operatorId": "OP01", "exitedAt": e.clock}))
}

/* ---------------- UTF-8 ---------------- */

func rawPost(t *testing.T, url string, hdr map[string]string, body []byte) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func TestBody_NotUTF8(t *testing.T) {
	e := newEnv(t)
	gbk := []byte("{\"orderId\":\"OD1\",\"reason\":\"\xd6\xd0\xce\xc4\xca\xc2\xd3\xc9\"}") // GBK 的"中文事由"
	st, m := rawPost(t, e.srv.URL+"/internal/bill/refunds", map[string]string{"Authorization": "Bearer cs-key", "X-Caller": "cs", "Idempotency-Key": "k"}, gbk)
	if st != 400 || m["code"] != "INVALID" || m["message"] != "请求体不是 UTF-8 编码" {
		t.Fatalf("%d %v", st, m)
	}
	st, m = rawPost(t, e.srv.URL+"/api/bill/me/coupons", map[string]string{"Authorization": "Bearer " + e.userToken("U1")}, gbk)
	if st != 400 || m["code"] != "INVALID" || m["message"] != "请求体不是 UTF-8 编码" {
		t.Fatalf("用户端：%d %v", st, m)
	}
	// UTF-8 正常通过（到业务校验才报别的错）
	st, m = rawPost(t, e.srv.URL+"/internal/bill/refunds", map[string]string{"Authorization": "Bearer cs-key", "X-Caller": "cs", "Idempotency-Key": "k2"},
		[]byte(`{"orderId":"OD1","amount":100,"reason":"中文事由说明","actor":{"kind":"人","id":"AC1"}}`))
	if st != 404 || m["code"] != "NOT_FOUND" {
		t.Fatalf("%d %v", st, m)
	}
}

/* ---------------- 配置 ---------------- */

func TestConfig_EventRetention(t *testing.T) {
	base := map[string]string{"DB_DSN": "x"}
	get := func(extra map[string]string) func(string) string {
		return func(k string) string {
			if v, ok := extra[k]; ok {
				return v
			}
			return base[k]
		}
	}
	c, err := LoadConfig(get(nil))
	if err != nil || c.EventRetentionDays != 30 || c.EventRetention() != 30*24*time.Hour {
		t.Fatalf("默认 30 天：%+v %v", c, err)
	}
	c, err = LoadConfig(get(map[string]string{"EVENT_RETENTION_DAYS": "7"}))
	if err != nil || c.EventRetentionDays != 7 {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := LoadConfig(get(map[string]string{"EVENT_RETENTION_DAYS": "abc"})); err == nil {
		t.Fatal("非法值应报错")
	}
	if _, err := LoadConfig(get(map[string]string{"EVENT_RETENTION_DAYS": "0"})); err == nil {
		t.Fatal("0 应报错")
	}
}

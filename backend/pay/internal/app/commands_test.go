package app

// 模块间命令的契约测试与单元测试。
// trace: R-09 R-18 BR-06 R-11 R-15 R-17 BR-22 R-34；pay.yaml /internal/pay/*；events/pay.md

import (
	"context"
	"fmt"
	"testing"
	"time"

	"bike/pay/internal/channel"
)

var bizSeq int

func newBiz(prefix string) string {
	bizSeq++
	return fmt.Sprintf("%s%d%03d", prefix, time.Now().UnixNano()%1e9, bizSeq)
}

// completeCallbacks 让所有待支付的一键支付立刻到回调时刻并执行模拟渠道回调
func completeCallbacks(t *testing.T) {
	t.Helper()
	if _, err := tApp.Pool.Exec(context.Background(), `UPDATE pay_intents SET callback_at = $1 WHERE state = 'awaiting' AND callback_at IS NOT NULL`, now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := tApp.RunMockCallbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// oneTap 两步一键支付走完：第一步拿调起参数，回调后用同一幂等键取最终结果
func oneTap(t *testing.T, body map[string]any) resp {
	t.Helper()
	key := newKey()
	r := internal(t, "bill", "/internal/pay/payments", key, body)
	if r.Status == 200 && r.Body["result"] == "结果未知" && r.Body["payParams"] != nil {
		completeCallbacks(t)
		r = internal(t, "bill", "/internal/pay/payments", key, body)
	}
	return r
}

// payOrder 一键支付"订单支付"，返回流水号
func payOrder(t *testing.T, user, order string, amount int64, ch string) string {
	t.Helper()
	r := oneTap(t, map[string]any{
		"bizNo": newBiz("BL"), "type": "订单支付", "userId": user, "orderId": order, "amount": amount, "channel": ch})
	if r.Status != 200 || r.Body["result"] != "成功" {
		t.Fatalf("收款应成功：%d %s", r.Status, r.Raw)
	}
	return r.Body["paymentId"].(string)
}

// CT-PAY-01：健康检查
func TestHealthz(t *testing.T) {
	r := do(t, "GET", "/healthz", "", nil, nil)
	if r.Status != 200 || r.Body["ok"] != true {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
}

// CT-PAY-02：服务凭证与幂等键
func TestInternalAuth(t *testing.T) {
	body := map[string]any{"bizNo": "B1", "userId": "U1", "orderId": "OD1", "amount": 100, "channel": "微信支付"}
	if r := do(t, "POST", "/internal/pay/credit-charges", "bill-key", map[string]string{"X-Caller": "ops", "Idempotency-Key": "x"}, body); r.Status != 401 {
		t.Fatalf("凭证不符应 401：%d", r.Status)
	}
	if r := do(t, "POST", "/internal/pay/credit-charges", "bill-key", map[string]string{"X-Caller": "bill"}, body); r.Status != 400 || r.Body["code"] != "INVALID" {
		t.Fatalf("缺幂等键应 400：%d %s", r.Status, r.Raw)
	}
	if r := internal(t, "bill", "/internal/pay/credit-charges", newKey(), map[string]any{"bizNo": "B1", "amount": 0}); r.Status != 400 {
		t.Fatalf("参数不全应 400：%d", r.Status)
	}
}

// TC-PAY-CMD-01：免押扣款成功；同键重放同结果；同业务单号再扣返回 ALREADY_PAID；事件载荷含流水头
func TestCreditChargeSuccess(t *testing.T) {
	setClock(time.Date(2026, 12, 1, 12, 0, 0, 0, bj))
	biz, key := newBiz("BL"), newKey()
	body := map[string]any{"bizNo": biz, "userId": "U200001", "orderId": "OD2026120100001", "amount": 800, "channel": "微信支付"}
	r := internal(t, "bill", "/internal/pay/credit-charges", key, body)
	if r.Status != 200 || r.Body["result"] != "成功" || r.Body["account"] != "骑行收入户" || r.Body["type"] != "订单扣款" || r.Body["completedAt"] == nil {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	pid := r.Body["paymentId"].(string)
	r2 := internal(t, "bill", "/internal/pay/credit-charges", key, body)
	if r2.Body["paymentId"] != pid || r2.Body["result"] != "成功" {
		t.Fatalf("同键重放应返回首次结果：%s", r2.Raw)
	}
	r3 := internal(t, "bill", "/internal/pay/credit-charges", newKey(), body)
	if r3.Body["result"] != "失败" || r3.Body["failCode"] != "ALREADY_PAID" || r3.Body["paymentId"] != pid {
		t.Fatalf("同业务单号只成功一次：%s", r3.Raw)
	}
	if ev := eventsOf(t, biz); len(ev) != 1 || ev[0] != "pay.payment.succeeded" {
		t.Fatalf("事件 %v", ev)
	}
	e := lastEvent(t, "pay.payment.succeeded", biz)
	p := e["payload"].(map[string]any)
	for _, k := range []string{"paymentId", "bizNo", "type", "account", "userId", "orderId", "amount", "channel", "completedAt", "channelTradeNo"} {
		if _, ok := p[k]; !ok {
			t.Errorf("载荷缺 %s", k)
		}
	}
	if e["version"].(float64) != 1 || e["key"] != biz || p["channelTradeNo"] == nil {
		t.Fatalf("信封 %v", e)
	}
}

// TC-PAY-CMD-02：渠道拒绝 → 失败带原因，没有动钱；可用新键重新发起
func TestCreditChargeDeclined(t *testing.T) {
	biz := newBiz("BL")
	body := map[string]any{"bizNo": biz, "userId": "UFAIL", "orderId": "OD2026120100002", "amount": 300, "channel": "支付宝"}
	r := internal(t, "bill", "/internal/pay/credit-charges", newKey(), body)
	if r.Body["result"] != "失败" || r.Body["failCode"] != "DECLINED" || r.Body["failReason"] != "用户支付账户余额不足" {
		t.Fatalf("%s", r.Raw)
	}
	e := lastEvent(t, "pay.payment.failed", biz)
	if e["payload"].(map[string]any)["failReason"] != "用户支付账户余额不足" {
		t.Fatalf("%v", e)
	}
	r2 := internal(t, "bill", "/internal/pay/credit-charges", newKey(), body)
	if r2.Body["paymentId"] == r.Body["paymentId"] {
		t.Fatal("失败后重新发起应是新流水")
	}
}

// TC-PAY-CMD-03：结果未知 → 发"结果未知"；同业务单号不重复发起；查询补齐后发成功；同键再取得到最新结果
func TestUnknownResolvedByQuery(t *testing.T) {
	biz, key := newBiz("BL"), newKey()
	body := map[string]any{"bizNo": biz, "userId": "UUNK", "orderId": "OD2026120100003", "amount": 450, "channel": "微信支付"}
	r := internal(t, "bill", "/internal/pay/credit-charges", key, body)
	if r.Body["result"] != "结果未知" || r.Body["status"] != "处理中" {
		t.Fatalf("%s", r.Raw)
	}
	pid := r.Body["paymentId"]
	r2 := internal(t, "bill", "/internal/pay/credit-charges", newKey(), body)
	if r2.Body["result"] != "结果未知" || r2.Body["paymentId"] != pid {
		t.Fatalf("处理中不应重复发起：%s", r2.Raw)
	}
	tMocks[channel.WeChat].Cfg.UnknownResolve = channel.BehaveOK
	setClock(now().Add(time.Minute))
	if _, err := tApp.ResolvePending(context.Background(), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	r3 := internal(t, "bill", "/internal/pay/credit-charges", key, body)
	if r3.Body["result"] != "成功" {
		t.Fatalf("同键再调应取到最新结果：%s", r3.Raw)
	}
	if ev := eventsOf(t, biz); len(ev) != 2 || ev[0] != "pay.payment.unknown" || ev[1] != "pay.payment.succeeded" {
		t.Fatalf("事件 %v", ev)
	}
}

// TC-PAY-CMD-04：仍未知的由运维确认；不在未知状态的返回 NOT_PENDING；事由不足 4 字拒绝
func TestOpsConfirm(t *testing.T) {
	tMocks[channel.Alipay].Cfg.UnknownResolve = channel.BehaveUnknown
	defer func() { tMocks[channel.Alipay].Cfg.UnknownResolve = channel.BehaveOK }()
	biz := newBiz("BL")
	r := internal(t, "bill", "/internal/pay/credit-charges", newKey(), map[string]any{"bizNo": biz, "userId": "UUNK", "orderId": "OD2026120100004", "amount": 500, "channel": "支付宝"})
	pid := r.Body["paymentId"].(string)
	setClock(now().Add(time.Minute))
	tApp.ResolvePending(context.Background(), 30*time.Second)
	if ev := eventsOf(t, biz); len(ev) != 1 {
		t.Fatalf("仍未知不应再发事件：%v", ev)
	}
	path := "/internal/pay/payments/" + pid + "/confirm"
	actor := map[string]any{"kind": "人", "id": "AC0501", "name": "运维甲"}
	if x := internal(t, "ops", path, newKey(), map[string]any{"result": "已收款", "reason": "核实", "actor": actor}); x.Status != 400 || x.Body["code"] != "REASON_REQUIRED" {
		t.Fatalf("%d %s", x.Status, x.Raw)
	}
	if x := internal(t, "ops", "/internal/pay/payments/PY0/confirm", newKey(), map[string]any{"result": "已收款", "reason": "渠道后台核实已到账", "actor": actor}); x.Status != 404 {
		t.Fatalf("%d", x.Status)
	}
	x := internal(t, "ops", path, newKey(), map[string]any{"result": "已收款", "reason": "渠道后台核实已到账", "actor": actor})
	if x.Body["result"] != "成功" {
		t.Fatalf("%s", x.Raw)
	}
	e := lastEvent(t, "pay.payment.succeeded", biz)
	if e["actor"].(map[string]any)["id"] != "AC0501" {
		t.Fatalf("事件操作人应为运维：%v", e["actor"])
	}
	y := internal(t, "ops", path, newKey(), map[string]any{"result": "未收款", "reason": "再次确认的结果", "actor": actor})
	if y.Body["result"] != "失败" || y.Body["failCode"] != "NOT_PENDING" {
		t.Fatalf("%s", y.Raw)
	}
}

// TC-PAY-CMD-05：一键支付的类型与必填项；月卡购买进骑行收入户
func TestOneTap(t *testing.T) {
	if r := internal(t, "bill", "/internal/pay/one-tap-payments", newKey(), map[string]any{"bizNo": "MC1", "type": "月卡购买", "userId": "U200001", "amount": 3000, "channel": "微信支付"}); r.Status != 400 {
		t.Fatalf("月卡购买缺 cardId 应 400：%d", r.Status)
	}
	if r := internal(t, "bill", "/internal/pay/one-tap-payments", newKey(), map[string]any{"bizNo": "X", "type": "退款", "userId": "U1", "orderId": "O", "amount": 1, "channel": "微信支付"}); r.Status != 400 {
		t.Fatalf("非法类型应 400：%d", r.Status)
	}
	card := newBiz("MC")
	r := oneTap(t, map[string]any{"bizNo": card, "type": "月卡购买", "userId": "U200001", "cardId": card, "amount": 3000, "channel": "微信支付"})
	if r.Body["result"] != "成功" || r.Body["type"] != "月卡购买" || r.Body["account"] != "骑行收入户" {
		t.Fatalf("%s", r.Raw)
	}
}

// TC-PAY-CMD-06：退款原路、部分退、累计不超过已收；没有收款的订单 404
func TestRefundLimits(t *testing.T) {
	order := newBiz("OD")
	pid := payOrder(t, "U200002", order, 800, "微信支付")
	refund := func(amount int64) resp {
		return internal(t, "bill", "/internal/pay/refunds", newKey(), map[string]any{"userId": "U200002", "orderId": order, "amount": amount,
			"ticketId": "TK1", "actor": map[string]any{"kind": "人", "id": "AC0301"}, "reason": "退调度费"})
	}
	r := refund(300)
	if r.Body["result"] != "成功" || r.Body["type"] != "退款" {
		t.Fatalf("%s", r.Raw)
	}
	e := lastEvent(t, "pay.refund.succeeded", order)
	if e["payload"].(map[string]any)["relatedPaymentId"] != pid {
		t.Fatalf("退款应指向原支付：%v", e)
	}
	if x := refund(600); x.Body["result"] != "失败" || x.Body["failCode"] != "EXCEED_RECEIVED" {
		t.Fatalf("超过已收应失败：%s", x.Raw)
	}
	if x := refund(500); x.Body["result"] != "成功" {
		t.Fatalf("%s", x.Raw)
	}
	if x := refund(1); x.Status != 409 || x.Body["code"] != "CONFLICT" {
		t.Fatalf("已全额退款再退应 409 CONFLICT：%d %s", x.Status, x.Raw)
	}
	if x := internal(t, "bill", "/internal/pay/refunds", newKey(), map[string]any{"userId": "U200002", "orderId": "OD-NONE", "amount": 1}); x.Status != 404 {
		t.Fatalf("%d", x.Status)
	}
}

// TC-PAY-CMD-07：押金收款进押金专户；退押先受理（处理中），查询到账后发退款成功；同一退押申请只退一次
func TestDepositFlow(t *testing.T) {
	r := internal(t, "deposit", "/internal/pay/deposit-charges", newKey(), map[string]any{"bizNo": newBiz("DP"), "type": "押金缴纳", "userId": "U200001", "amount": 9900, "channel": "微信支付"})
	if r.Body["result"] != "成功" || r.Body["account"] != "押金专户" {
		t.Fatalf("%s", r.Raw)
	}
	orig := r.Body["paymentId"].(string)
	biz := newBiz("RF")
	body := map[string]any{"bizNo": biz, "userId": "U200001", "amount": 9900, "originalPaymentIds": []string{orig}, "actor": map[string]any{"kind": "用户", "id": "U200001"}}
	x := internal(t, "deposit", "/internal/pay/deposit-refunds", newKey(), body)
	if x.Body["result"] != "结果未知" || x.Body["status"] != "处理中" || x.Body["type"] != "退押" {
		t.Fatalf("%s", x.Raw)
	}
	if len(eventsOf(t, biz)) != 0 {
		t.Fatal("退押处理中不发事件")
	}
	setClock(now().Add(time.Minute))
	tApp.ResolvePending(context.Background(), 30*time.Second)
	y := internal(t, "deposit", "/internal/pay/deposit-refunds", newKey(), body)
	if y.Body["result"] != "成功" || y.Body["paymentId"] != x.Body["paymentId"] {
		t.Fatalf("同一退押申请应返回同一流水的最新结果：%s", y.Raw)
	}
	if ev := eventsOf(t, biz); len(ev) != 1 || ev[0] != "pay.refund.succeeded" {
		t.Fatalf("%v", ev)
	}
	if z := internal(t, "deposit", "/internal/pay/deposit-refunds", newKey(), map[string]any{"bizNo": newBiz("RF"), "userId": "U200002", "amount": 9900, "originalPaymentIds": []string{orig}}); z.Status != 404 {
		t.Fatalf("别人的押金流水应 404：%d", z.Status)
	}
	if z := internal(t, "deposit", "/internal/pay/deposit-refunds", newKey(), map[string]any{"bizNo": newBiz("RF"), "userId": "U200001", "amount": 20000, "originalPaymentIds": []string{orig}}); z.Body["failCode"] != "EXCEED_RECEIVED" {
		t.Fatalf("超过原押金应失败：%s", z.Raw)
	}
}

// TC-PAY-CMD-08：专户划转记一笔方向为"转"的押金抵扣，同一抵扣记录只划一次
func TestDepositTransfer(t *testing.T) {
	biz := newBiz("DD")
	body := map[string]any{"bizNo": biz, "userId": "U200001", "orderId": "OD2026120100009", "amount": 450}
	r := internal(t, "deposit", "/internal/pay/deposit-transfers", newKey(), body)
	if r.Body["result"] != "成功" || r.Body["type"] != "押金抵扣" || r.Body["account"] != "押金专户" {
		t.Fatalf("%s", r.Raw)
	}
	p, _ := tApp.getPayment(context.Background(), tApp.Pool, r.Body["paymentId"].(string))
	if p.Direction != "转" || p.Channel != "押金专户划转" || p.ChannelTradeNo == nil {
		t.Fatalf("%+v", p)
	}
	if x := internal(t, "deposit", "/internal/pay/deposit-transfers", newKey(), body); x.Body["failCode"] != "ALREADY_PAID" {
		t.Fatalf("%s", x.Raw)
	}
	if ev := eventsOf(t, biz); len(ev) != 1 || ev[0] != "pay.payment.succeeded" {
		t.Fatalf("%v", ev)
	}
}

// TC-PAY-CMD-09：发件表的事件发到 JetStream，消息编号为 eventId
func TestPublishOutbox(t *testing.T) {
	biz := newBiz("DD")
	internal(t, "deposit", "/internal/pay/deposit-transfers", newKey(), map[string]any{"bizNo": biz, "userId": "U200001", "orderId": "OD2026120100010", "amount": 100})
	ctx := context.Background()
	for {
		n, err := tApp.PublishPending(ctx, tJS)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	var left int
	tApp.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&left)
	if left != 0 {
		t.Fatalf("仍有 %d 条未发", left)
	}
	e := lastEvent(t, "pay.payment.succeeded", biz)
	st, _ := tJS.Stream(ctx, "EVT_PAY")
	msg, err := st.GetLastMsgForSubject(ctx, "pay.payment.succeeded")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Nats-Msg-Id") != e["eventId"] {
		t.Fatalf("消息编号应为 eventId：%s vs %v", msg.Header.Get("Nats-Msg-Id"), e["eventId"])
	}
}

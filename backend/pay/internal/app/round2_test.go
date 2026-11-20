package app

// 第二轮变更：一键支付两步化、退款 409、退款执行留痕、UTF-8、READONLY、保留期配置。
// trace: 第二轮变更单 pay 节与全体 1、2、3；pay.yaml startOneTapPayment、refundOrderPayment

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func oneTapBody(user, biz, order string, amount int64, ch string) map[string]any {
	return map[string]any{"bizNo": biz, "type": "订单支付", "userId": user, "orderId": order, "amount": amount, "channel": ch}
}

// TC-PAY-R2-01：一键支付第一步只返回调起参数，不成功、不发事件；同键与同业务单号重放返回同一笔与同一参数；回调后成功并发事件
func TestOneTapTwoStep(t *testing.T) {
	setClock(time.Date(2026, 12, 3, 9, 0, 0, 0, bj))
	biz, order, key := newBiz("BL"), newBiz("OD"), newKey()
	body := oneTapBody("U200001", biz, order, 700, "微信支付")
	r := internal(t, "bill", "/internal/pay/payments", key, body)
	if r.Status != 200 || r.Body["result"] != "结果未知" || r.Body["status"] != "处理中" || r.Body["completedAt"] != nil {
		t.Fatalf("第一步不应成功：%d %s", r.Status, r.Raw)
	}
	pid := r.Body["paymentId"].(string)
	pp, _ := r.Body["payParams"].(map[string]any)
	if pp == nil || pp["paymentId"] != pid || pp["channel"] != "微信支付" || pp["mode"] != "mock" || pp["expiresAt"] != "2026-12-03T09:15:00+08:00" {
		t.Fatalf("payParams：%s", r.Raw)
	}
	if ps, _ := pp["params"].(map[string]any); ps == nil || ps["prepayId"] == nil {
		t.Fatalf("params：%v", pp)
	}
	if ev := eventsOf(t, biz); len(ev) != 0 {
		t.Fatalf("待支付不发事件：%v", ev)
	}
	// 同键重放、同业务单号换键：同一笔同一参数，不重复下单
	for _, k := range []string{key, newKey()} {
		x := internal(t, "bill", "/internal/pay/payments", k, body)
		if x.Body["paymentId"] != pid || x.Body["payParams"] == nil {
			t.Fatalf("应返回同一笔待支付：%s", x.Raw)
		}
	}
	// 回调延迟未到：不动
	if n, err := tApp.RunMockCallbacks(context.Background()); err != nil || n != 0 {
		t.Fatalf("延迟未到不应回调：%d %v", n, err)
	}
	setClock(now().Add(4 * time.Second)) // 超过 PAY_MOCK_CALLBACK_DELAY（测试里 3s）
	if _, err := tApp.RunMockCallbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	x := internal(t, "bill", "/internal/pay/payments", key, body)
	if x.Body["result"] != "成功" || x.Body["paymentId"] != pid || x.Body["payParams"] != nil || x.Body["completedAt"] == nil {
		t.Fatalf("回调后同键应取到成功：%s", x.Raw)
	}
	if ev := eventsOf(t, biz); len(ev) != 1 || ev[0] != "pay.payment.succeeded" {
		t.Fatalf("事件 %v", ev)
	}
	e := lastEvent(t, "pay.payment.succeeded", biz)
	if e["payload"].(map[string]any)["channelTradeNo"] == nil {
		t.Fatalf("%v", e)
	}
	// 已成功的业务单号再发起：ALREADY_PAID
	y := internal(t, "bill", "/internal/pay/payments", newKey(), body)
	if y.Body["result"] != "失败" || y.Body["failCode"] != "ALREADY_PAID" {
		t.Fatalf("%s", y.Raw)
	}
	// 旧路径兼容，行为相同
	z := internal(t, "bill", "/internal/pay/one-tap-payments", newKey(), oneTapBody("U200001", newBiz("BL"), newBiz("OD"), 100, "支付宝"))
	if z.Body["result"] != "结果未知" || z.Body["payParams"] == nil {
		t.Fatalf("%s", z.Raw)
	}
}

// TC-PAY-R2-02：渠道回调为失败 → 收款失败事件带原因；失败后可重新发起新流水
func TestOneTapCallbackFailed(t *testing.T) {
	biz := newBiz("BL")
	body := oneTapBody("UFAIL", biz, newBiz("OD"), 300, "支付宝")
	r := oneTap(t, body)
	if r.Body["result"] != "失败" || r.Body["failCode"] != "DECLINED" || r.Body["failReason"] != "用户支付账户余额不足" {
		t.Fatalf("%s", r.Raw)
	}
	if ev := eventsOf(t, biz); len(ev) != 1 || ev[0] != "pay.payment.failed" {
		t.Fatalf("%v", ev)
	}
	r2 := internal(t, "bill", "/internal/pay/payments", newKey(), body)
	if r2.Body["paymentId"] == r.Body["paymentId"] || r2.Body["payParams"] == nil {
		t.Fatalf("失败后重新发起应是新流水并给新参数：%s", r2.Raw)
	}
}

// TC-PAY-R2-03：调起参数到期仍未支付记失败；行为为"未知"的用户不回调也不按超时关闭
func TestOneTapExpire(t *testing.T) {
	setClock(time.Date(2026, 12, 4, 9, 0, 0, 0, bj))
	biz := newBiz("BL")
	r := internal(t, "bill", "/internal/pay/payments", newKey(), oneTapBody("U200002", biz, newBiz("OD"), 200, "微信支付"))
	bizU := newBiz("BL")
	u := internal(t, "bill", "/internal/pay/payments", newKey(), oneTapBody("UUNK", bizU, newBiz("OD"), 200, "微信支付"))
	if r.Body["payParams"] == nil || u.Body["payParams"] == nil {
		t.Fatalf("%s %s", r.Raw, u.Raw)
	}
	// 用户一直没付：渠道侧到期仍未出结果（模拟为"未知"且不按行为补齐）
	if _, err := tApp.Pool.Exec(context.Background(), `UPDATE pay_intents SET callback_at = $1 WHERE payment_id = $2`, now().Add(time.Hour), r.Body["paymentId"]); err != nil {
		t.Fatal(err)
	}
	if _, err := tApp.Pool.Exec(context.Background(), `UPDATE mock_channel_trades SET behavior = '未知' WHERE out_no = $1`, r.Body["paymentId"]); err != nil {
		t.Fatal(err)
	}
	tMocks["微信支付"].Cfg.UnknownResolve = "未知"
	defer func() { tMocks["微信支付"].Cfg.UnknownResolve = "成功" }()
	setClock(now().Add(16 * time.Minute))
	if _, err := tApp.ExpireIntents(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := lastEvent(t, "pay.payment.failed", biz)
	if e["payload"].(map[string]any)["failReason"] != "用户未在时限内完成支付" {
		t.Fatalf("%v", e)
	}
	if ev := eventsOf(t, bizU); len(ev) != 0 {
		t.Fatalf("行为未知的不应被超时关闭：%v", ev)
	}
	x := internal(t, "bill", "/internal/pay/payments", newKey(), oneTapBody("U200002", biz, "OD-X", 200, "微信支付"))
	if x.Body["paymentId"] == r.Body["paymentId"] {
		t.Fatal("到期失败后应可重新发起")
	}
}

// TC-PAY-R2-04：已全额退过再退 409 CONFLICT；未退完超额仍是 200 失败 EXCEED_RECEIVED；退款执行留痕带操作人
func TestRefundConflictAndAudit(t *testing.T) {
	order := newBiz("OD")
	payOrder(t, "U200002", order, 500, "微信支付")
	refund := func(amount int64, actor any) resp {
		return internal(t, "bill", "/internal/pay/refunds", newKey(), map[string]any{"userId": "U200002", "orderId": order, "amount": amount, "actor": actor, "reason": "退调度费"})
	}
	if x := refund(600, nil); x.Status != 200 || x.Body["failCode"] != "EXCEED_RECEIVED" {
		t.Fatalf("%d %s", x.Status, x.Raw)
	}
	r := refund(200, map[string]any{"kind": "人", "id": "AC0301", "name": "财务甲"})
	if r.Body["result"] != "成功" {
		t.Fatalf("%s", r.Raw)
	}
	a := lastEvent(t, "pay.audit.recorded", "payment:"+r.Body["paymentId"].(string))
	p := a["payload"].(map[string]any)
	if p["action"] != "执行退款" || p["targetType"] != "payment" || p["reason"] != "退调度费" || a["actor"].(map[string]any)["id"] != "AC0301" {
		t.Fatalf("%v", a)
	}
	r2 := refund(300, nil)
	if r2.Body["result"] != "成功" {
		t.Fatalf("%s", r2.Raw)
	}
	a2 := lastEvent(t, "pay.audit.recorded", "payment:"+r2.Body["paymentId"].(string))
	if a2["actor"].(map[string]any)["kind"] != "系统" {
		t.Fatalf("无 actor 时留痕操作人应为系统：%v", a2)
	}
	x := refund(1, nil)
	if x.Status != 409 || x.Body["code"] != "CONFLICT" {
		t.Fatalf("%d %s", x.Status, x.Raw)
	}
	var n int
	tApp.Pool.QueryRow(context.Background(), `SELECT count(*) FROM payments WHERE order_id=$1 AND type='退款'`, order).Scan(&n)
	if n != 2 {
		t.Fatalf("409 不应产生流水：%d", n)
	}
}

// TC-PAY-R2-05：非 UTF-8 请求体 400 INVALID
func TestNonUTF8Body(t *testing.T) {
	gbk := []byte(`{"bizNo":"B1","userId":"U1","orderId":"OD1","amount":100,"channel":"`)
	gbk = append(gbk, 0xCE, 0xA2, 0xD0, 0xC5, 0xD6, 0xA7, 0xB8, 0xB6) // GBK 的“微信支付”
	gbk = append(gbk, []byte(`"}`)...)
	cases := []struct {
		path, bearer string
		hdr          map[string]string
	}{
		{"/internal/pay/credit-charges", "bill-key", map[string]string{"X-Caller": "bill", "Idempotency-Key": newKey()}},
		{"/api/pay/admin/refund-failures/PY1/transfer", finance(), nil},
		{"/api/pay/me/payments/PY1/payee", userToken("U200001"), nil},
	}
	for _, c := range cases {
		req, _ := http.NewRequest("POST", tSrv.URL+c.path, bytes.NewReader(gbk))
		req.Header.Set("Authorization", "Bearer "+c.bearer)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 400 || !strings.Contains(string(raw), `"INVALID"`) || !strings.Contains(string(raw), "请求体不是 UTF-8 编码") {
			t.Fatalf("%s：%d %s", c.path, res.StatusCode, raw)
		}
	}
}

// TC-PAY-R2-06：管理端全部写接口对只读令牌一律 403 READONLY；有运营商范围的令牌 SCOPE_DENIED
func TestAdminWritesReadonly(t *testing.T) {
	ro := adminToken("AC9", []string{"recon.manage", "refund.manualTransfer"}, nil, true)
	for _, path := range []string{
		"/api/pay/admin/recon/RC20261201/diff-items/0/handle",
		"/api/pay/admin/refund-failures/PY1/transfer",
	} {
		r := do(t, "POST", path, ro, nil, map[string]any{"note": "试试只读写", "reason": "试试只读写"})
		if r.Status != 403 || r.Body["code"] != "READONLY" {
			t.Fatalf("%s：%d %s", path, r.Status, r.Raw)
		}
	}
	// 只读令牌读接口不受影响
	if r := do(t, "GET", "/api/pay/admin/recon", ro, nil, nil); r.Status != 200 {
		t.Fatalf("只读可读：%d %s", r.Status, r.Raw)
	}
	sc := adminToken("AC8", []string{"recon.manage", "refund.manualTransfer"}, "OP01", true)
	if r := do(t, "POST", "/api/pay/admin/refund-failures/PY1/transfer", sc, nil, map[string]any{"reason": "试试范围"}); r.Status != 403 || r.Body["code"] != "SCOPE_DENIED" {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
}

// TC-PAY-R2-07：事件保留期读 EVENT_RETENTION_DAYS（默认 30），旧变量 PAY_STREAM_MAX_AGE 兜底
func TestRetentionConfig(t *testing.T) {
	t.Setenv("DB_DSN", "postgres://x")
	t.Setenv("EVENT_RETENTION_DAYS", "")
	t.Setenv("PAY_STREAM_MAX_AGE", "")
	c, err := ConfigFromEnv()
	if err != nil || c.StreamMaxAge != 30*24*time.Hour || c.MockCallbackDelay != 3*time.Second || c.IntentTTL != 15*time.Minute {
		t.Fatalf("默认：%+v %v", c, err)
	}
	t.Setenv("PAY_STREAM_MAX_AGE", "48h")
	if c, err = ConfigFromEnv(); err != nil || c.StreamMaxAge != 48*time.Hour {
		t.Fatalf("旧变量兜底：%v %v", c.StreamMaxAge, err)
	}
	t.Setenv("EVENT_RETENTION_DAYS", "7")
	if c, err = ConfigFromEnv(); err != nil || c.StreamMaxAge != 7*24*time.Hour {
		t.Fatalf("新变量优先：%v %v", c.StreamMaxAge, err)
	}
	t.Setenv("EVENT_RETENTION_DAYS", "0")
	if _, err = ConfigFromEnv(); err == nil {
		t.Fatal("非法天数应报错")
	}
	t.Setenv("EVENT_RETENTION_DAYS", "")
	t.Setenv("PAY_MOCK_CALLBACK_DELAY", "500ms")
	t.Setenv("PAY_INTENT_TTL", "2m")
	if c, err = ConfigFromEnv(); err != nil || c.MockCallbackDelay != 500*time.Millisecond || c.IntentTTL != 2*time.Minute {
		t.Fatalf("%+v %v", c, err)
	}
}

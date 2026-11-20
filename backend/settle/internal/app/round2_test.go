package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// 管理端写接口收到 readonly=true 的令牌一律 403 READONLY。
func TestReadonlyTokenRejectedOnEveryWrite(t *testing.T) {
	a, _ := newApp(t, "2026-11-01T00:30:00+08:00")
	seedScenario(t, a)
	h := a.Handler()
	ro := tokenSpec{sub: "AC0299", role: "财务", readonly: true, perms: []string{"settlement.manage", "settlement.adjust", "dispute.decide", "dispute.submit.own"}}
	body := map[string]any{"month": "2026-10"}
	for _, p := range []string{
		"/api/settle/finance/settlements/generate",
		"/api/settle/finance/settlements/ST-M-202610-OP01/adjustments",
		"/api/settle/finance/settlements/ST-M-202610-OP01/adjustments/ADJ-1/confirm",
		"/api/settle/finance/settlements/ST-M-202610-OP01/review",
		"/api/settle/finance/settlements/ST-M-202610-OP01/send",
		"/api/settle/finance/settlements/ST-M-202610-OP01/payment",
		"/api/settle/finance/disputes/DS1/decide",
		"/api/settle/operator/disputes",
	} {
		if r := call(t, h, "POST", p, &ro, body, ""); r.code != 403 || r.errCode(t) != "READONLY" {
			t.Errorf("%s: %d %s", p, r.code, r.body)
		}
	}
	// 读接口不受影响
	if r := call(t, h, "GET", "/api/settle/finance/settlements", &ro, nil, ""); r.code != 200 {
		t.Errorf("只读令牌读列表: %d %s", r.code, r.body)
	}
}

// 收到 fleet.operator.exited 之后，对该运营商 scope 的写操作拒绝 READONLY（令牌里 readonly 仍为 false）；
// 其他运营商与财务侧不受影响，只读保留。
func TestOperatorExitedEventBlocksScopedWrites(t *testing.T) {
	a, _ := newApp(t, "2026-11-01T00:30:00+08:00")
	seedScenario(t, a)
	h := a.Handler()
	if r := call(t, h, "POST", "/api/settle/finance/settlements/generate", &finance, map[string]string{"month": "2026-10"}, ""); r.code != 200 {
		t.Fatalf("出单: %d %s", r.code, r.body)
	}
	body := map[string]any{"settlementId": "ST-M-202610-OP01", "orderIds": []string{"OD2026101000002"}, "field": "时长", "claim": "车端记录骑行 8 分钟，平台少计"}
	if r := call(t, h, "POST", "/api/settle/operator/disputes", &operator, body, ""); r.code != 200 {
		t.Fatalf("退出前可提交: %d %s", r.code, r.body)
	}
	feed(t, a, "fleet.operator.exited", map[string]any{"operatorId": "OP01", "exitedAt": "2026-11-02T00:00:00+08:00", "readonlyUntil": "2027-02-02", "reason": "主动退出"})
	if r := call(t, h, "POST", "/api/settle/operator/disputes", &operator, body, "k2"); r.code != 403 || r.errCode(t) != "READONLY" {
		t.Errorf("退出后提交应 READONLY: %d %s", r.code, r.body)
	}
	for _, p := range []string{"/api/settle/operator/settlements", "/api/settle/operator/orders", "/api/settle/operator/disputes"} {
		if r := call(t, h, "GET", p, &operator, nil, ""); r.code != 200 {
			t.Errorf("退出后只读保留 %s: %d %s", p, r.code, r.body)
		}
	}
	// 他家照常；财务的结算单流程与异议处理不受影响
	other := map[string]any{"settlementId": "ST-M-202610-OP02", "orderIds": []string{"OD2026101800006"}, "field": "里程", "claim": "车端里程记录比平台多一公里"}
	if r := call(t, h, "POST", "/api/settle/operator/disputes", &op2, other, ""); r.code != 200 {
		t.Errorf("他家不受影响: %d %s", r.code, r.body)
	}
	if r := call(t, h, "POST", "/api/settle/finance/settlements/ST-M-202610-OP01/review", &finance, map[string]any{}, ""); r.code != 200 {
		t.Errorf("财务审核不受影响: %d %s", r.code, r.body)
	}
}

func TestNonUTF8BodyRejected(t *testing.T) {
	a, _ := newApp(t, "2026-11-01T00:30:00+08:00")
	h := a.Handler()
	req := httptest.NewRequest("POST", "/api/settle/finance/settlements/generate", strings.NewReader("{\"month\":\"2026-1\xff0\"}"))
	req.Header.Set("Authorization", "Bearer "+token(finance))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"INVALID"`) || !strings.Contains(w.Body.String(), "请求体不是 UTF-8 编码") {
		t.Errorf("非 UTF-8: %d %s", w.Code, w.Body.String())
	}
}

func unlockState(t *testing.T, oid string) (string, *string) {
	t.Helper()
	var st string
	var reason *string
	if err := testPool.QueryRow(context.Background(), `SELECT state, reason FROM order_unlock WHERE order_id = $1`, oid).Scan(&st, &reason); err != nil {
		t.Fatalf("开锁状态 %s: %v", oid, err)
	}
	return st, reason
}

// 开锁相关事件与收款事件进投影；开锁失败不计入结算；迟到的失败事件不降级已开锁/已结束的订单。
func TestUnlockAndPaidFacts(t *testing.T) {
	ctx := context.Background()
	a, clk := newApp(t, "2026-10-31T12:00:00+08:00")
	seedScenario(t, a)

	feed(t, a, "ride.order.unlock_failed", map[string]any{"orderId": "OD-FAIL1", "userId": "U1", "operatorId": "OP01", "brandId": "BR01", "reason": "车辆无响应", "failedAt": "2026-10-05T08:00:00+08:00"})
	if st, r := unlockState(t, "OD-FAIL1"); st != "开锁失败" || r == nil || *r != "车辆无响应" {
		t.Errorf("开锁失败: %s %v", st, r)
	}
	feed(t, a, "ride.order.pending_manual", map[string]any{"orderId": "OD-PEND1", "userId": "U1", "missing": []string{"锁车事实"}})
	if st, _ := unlockState(t, "OD-PEND1"); st != "待人工处理" {
		t.Errorf("待人工处理: %s", st)
	}
	feed(t, a, "ride.order.unlocked", map[string]any{"orderId": "OD-OK1", "userId": "U1", "startAt": "2026-10-05T09:00:00+08:00"})
	feed(t, a, "ride.order.unlock_failed", map[string]any{"orderId": "OD-OK1", "reason": "车辆故障"}) // 迟到，不降级
	if st, _ := unlockState(t, "OD-OK1"); st != "已开锁" {
		t.Errorf("已开锁不应被降级: %s", st)
	}
	// 先失败后成功结束：以结束事实为准
	feed(t, a, "ride.order.unlock_failed", map[string]any{"orderId": "OD2026110500005", "reason": "车辆无响应"})
	feed(t, a, "ride.order.ended", ended("OD2026110500005", "BR01", "OP01", "PR0002", "2026-11-05T08:00:00+08:00", "2026-11-05T08:10:00+08:00", "2026-11-05T08:10:01+08:00", 10))
	if st, _ := unlockState(t, "OD2026110500005"); st != "已结束" {
		t.Errorf("结束后收口: %s", st)
	}

	feed(t, a, "bill.bill.paid", map[string]any{"orderId": "OD2026101000002", "userId": "U1", "payStatus": "已支付", "channelPaid": 125, "depositDeducted": 0, "paidAt": "2026-10-10T08:06:00+08:00", "degraded": false})
	var ps string
	if err := testPool.QueryRow(ctx, `SELECT pay_status FROM order_pay WHERE order_id = 'OD2026101000002'`).Scan(&ps); err != nil || ps != "已支付" {
		t.Errorf("收款结果: %q %v", ps, err)
	}

	// 开锁失败不计入结算：把 OD2026101000002 标为开锁失败，出单只剩 2 单
	clk.Set("2026-11-01T00:30:00+08:00")
	fin := Actor{Kind: "人", ID: "AC0201", Name: "吕＊＊"}
	if _, err := testPool.Exec(ctx, `INSERT INTO order_unlock(order_id, state, reason) VALUES ('OD2026101000002','开锁失败','车辆无响应')
		ON CONFLICT (order_id) DO UPDATE SET state='开锁失败'`); err != nil {
		t.Fatal(err)
	}
	got, err := a.Generate(ctx, "2026-10", "财务手动", fin)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Totals.OrderCount != 2 || got[0].Totals.DetailFee != 465+600 {
		t.Errorf("开锁失败的订单不应计入: %+v", got[0].Totals)
	}
}

func TestSubscriptionsCoverRoundTwo(t *testing.T) {
	has := func(stream, subj string) bool {
		for _, s := range Subscriptions[stream] {
			if s == subj {
				return true
			}
		}
		return false
	}
	for _, s := range []string{"ride.order.unlocked", "ride.order.unlock_failed", "ride.order.pending_manual"} {
		if !has("EVT_RIDE", s) {
			t.Errorf("缺订阅 %s", s)
		}
	}
	if !has("EVT_BILL", "bill.bill.paid") {
		t.Error("缺订阅 bill.bill.paid")
	}
}

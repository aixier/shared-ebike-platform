package core

import (
	"context"

	"bike/ride/internal/rules"
	"sync"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/healthz", "", nil)
	if r.status != 200 || r.body["ok"] != true {
		t.Fatalf("healthz：%d %s", r.status, r.raw)
	}
}

func TestScan(t *testing.T) {
	e := newEnv(t)
	// 可租车、老用户免押：check.ok，价格文案取现行版本
	r := e.do("POST", "/api/ride/scan", "U100341", map[string]any{"code": "https://bike.gov.cn/c?code=SF86020226"})
	if r.status != 200 {
		t.Fatalf("扫码：%d %s", r.status, r.raw)
	}
	check := r.body["check"].(map[string]any)
	price := r.body["price"].(map[string]any)
	veh := r.body["vehicle"].(map[string]any)
	if check["ok"] != true || price["text"] != "起步 ¥1.5（含 3 分钟），之后每分钟 ¥0.5" || veh["displayName"] != "【青禾】电单车" || veh["vehicleId"] != "VH000004" {
		t.Fatalf("扫码结果不对：%s", r.raw)
	}
	if r.body["degrade"].(map[string]any)["active"] != false {
		t.Fatal("常态不降级")
	}
	// 未登录也能扫码，原因放在 check
	r = e.do("POST", "/api/ride/scan", "", map[string]any{"code": "SF86020226"})
	if r.status != 200 || r.body["check"].(map[string]any)["code"] != "NOT_LOGGED_IN" {
		t.Fatalf("未登录扫码：%s", r.raw)
	}
	// 车辆编号也可以
	r = e.do("POST", "/api/ride/scan", "U100341", map[string]any{"code": "VH000004"})
	if r.status != 200 {
		t.Fatal("车辆编号扫码")
	}
	// 码已失效、暂停可租、码未启用：check 失败并给附近车；暂停可租只给其他品牌
	cases := map[string]string{"SF71009215": "CODE_INVALID", "SF50000481": "BRAND_PAUSED", "SF27239795": "CODE_NOT_ENABLED"}
	for code, want := range cases {
		r = e.do("POST", "/api/ride/scan", "U100341", map[string]any{"code": code})
		c := r.body["check"].(map[string]any)
		if r.status != 200 || c["ok"] != false || c["code"] != want || c["charged"] != false || c["next"] == nil {
			t.Fatalf("%s 应为 %s：%s", code, want, r.raw)
		}
		near := r.body["nearby"].([]any)
		if len(near) == 0 {
			t.Fatalf("%s 应给附近车：%s", code, r.raw)
		}
		for _, n := range near {
			m := n.(map[string]any)
			if want == "BRAND_PAUSED" && m["brandId"] == "BR02" {
				t.Fatal("暂停可租只给其他品牌")
			}
			if m["vehicleId"] == "VH001061" || m["vehicleId"] == "VH001151" || m["vehicleId"] == "VH000481" {
				t.Fatalf("不可租的车不应出现在附近车：%v", m)
			}
		}
	}
	r = e.do("POST", "/api/ride/scan", "U100341", map[string]any{"code": "hello"})
	if r.status != 404 || r.body["code"] != "CODE_NOT_FOUND" {
		t.Fatalf("未识别：%d %s", r.status, r.raw)
	}
	// 用户侧各种不能开锁
	for uid, want := range map[string]string{"U100036": "HAS_ARREARS", "U100712": "ACCOUNT_FROZEN", "U100127": "NEED_ONBOARDING", "U100087": "DEPOSIT_REQUIRED",
		"U100054": "DEPOSIT_INSUFFICIENT", "U101097": "DEPOSIT_REFUNDING", "U100598": "ACCOUNT_CANCELLED", "U100026": "NEED_ONBOARDING"} {
		r = e.do("POST", "/api/ride/scan", uid, map[string]any{"code": "SF86020226"})
		if c := r.body["check"].(map[string]any); c["code"] != want {
			t.Errorf("%s 应为 %s：%s", uid, want, r.raw)
		}
	}
}

func TestNearby(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/api/ride/nearby?vehicleId=VH000004", "", nil)
	var rows []any
	if r.status == 200 {
		// 返回数组
		rows = decodeArr(t, r.raw)
	}
	if r.status != 200 || len(rows) != 1 || rows[0].(map[string]any)["vehicleId"] != "VH000005" {
		t.Fatalf("附近车只应有 VH000005：%d %s", r.status, r.raw)
	}
	r = e.do("GET", "/api/ride/nearby?lng=104.0005&lat=30.0005&radiusM=1000", "", nil)
	if rows = decodeArr(t, r.raw); len(rows) != 2 {
		t.Fatalf("以点为中心应 2 辆：%s", r.raw)
	}
	r = e.do("GET", "/api/ride/nearby", "", nil)
	if r.status != 400 || r.body["code"] != "INVALID" {
		t.Fatalf("缺位置：%s", r.raw)
	}
}

func TestUnlockSuccessAndOrderFacts(t *testing.T) {
	e := newEnv(t)
	start := time.Now()
	r := e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020226"}, "Idempotency-Key", "k-1")
	if r.status != 200 || r.body["status"] != "骑行中" || r.body["unlockResult"] != "成功" || r.body["startAt"] == nil || r.body["elapsedSec"] == nil {
		t.Fatalf("开锁：%d %s", r.status, r.raw)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("开锁应在 5 秒内返回")
	}
	id := r.body["orderId"].(string)
	if len(id) != 15 || id[:2] != "OD" {
		t.Fatalf("订单号格式：%s", id)
	}
	if _, ok := r.body["userId"]; ok {
		t.Fatal("用户端不返回 userId")
	}
	// 同一幂等键重复请求：返回同一单，不产生第二单
	r2 := e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020226"}, "Idempotency-Key", "k-1")
	if r2.status != 200 || r2.body["orderId"] != id {
		t.Fatalf("幂等重复：%s", r2.raw)
	}
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM orders`).Scan(&n)
	if n != 1 {
		t.Fatalf("应只有一单，得 %d", n)
	}
	// 已有骑行中
	r3 := e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020227"})
	if r3.status != 409 || r3.body["code"] != "HAS_RIDING" || r3.body["orderId"] != id {
		t.Fatalf("已有骑行中：%s", r3.raw)
	}
	// 别人扫同一辆车：快照判可开，但一车一单 → 车辆被占用
	r4 := e.do("POST", "/api/ride/unlock", "U100004", map[string]any{"code": "SF86020226"})
	if r4.status != 409 || r4.body["code"] != "UNLOCK_OCCUPIED" || r4.body["charged"] != false {
		t.Fatalf("一车一单：%s", r4.raw)
	}
	// 事件：订单已创建、已开锁
	cr := e.outbox("ride.order.created")
	un := e.outbox("ride.order.unlocked")
	if len(un) != 1 || un[0]["orderId"] != id || un[0]["pricingVersionId"] != "PR0003" || un[0]["degraded"] != nil {
		t.Fatalf("已开锁事件：%v", un)
	}
	if v, ok := un[0]["startPileId"]; !ok || v != nil {
		t.Fatalf("startPileId 应存在且为 null：%v", un[0])
	}
	if len(cr) != 2 || cr[0]["userId"] != "U100341" || cr[0]["channel"] != "微信" {
		t.Fatalf("订单已创建事件：%v", cr)
	}
	// 当前骑行中
	cur := e.do("GET", "/api/ride/current", "U100341", nil)
	if cur.status != 200 || cur.body["orderId"] != id || cur.body["price"] == nil {
		t.Fatalf("当前骑行：%s", cur.raw)
	}
	if c := e.do("GET", "/api/ride/current", "U100004", nil); c.status != 204 {
		t.Fatalf("没有骑行中应 204：%d", c.status)
	}
	if c := e.do("GET", "/api/ride/current", "", nil); c.status != 401 || c.body["code"] != "NOT_LOGGED_IN" {
		t.Fatalf("未登录：%s", c.raw)
	}
}

func TestUnlockFailures(t *testing.T) {
	e := newEnv(t)
	// 运营商报告电量不足：开锁失败不计费，给附近车
	e.og.set(func(f *fakeOpgw) { f.unlock, f.reason = "失败", "电量不足" })
	r := e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020226"})
	if r.status != 409 || r.body["code"] != "UNLOCK_LOW_BATTERY" || r.body["charged"] != false || r.body["orderId"] == nil {
		t.Fatalf("电量不足：%s", r.raw)
	}
	if r.body["message"] != "车辆电量不足，未扣费。附近还有 1 辆可用车" {
		t.Fatalf("文案：%v", r.body["message"])
	}
	failID := r.body["orderId"].(string)
	o := e.order(failID)
	if o.Status != StFailed || *o.UnlockFailReason != "电量不足" || o.StartAt != nil || o.FailNotifiedAt == nil {
		t.Fatalf("失败单：%+v", o)
	}
	if fl := e.outbox("ride.order.unlock_failed"); len(fl) != 1 || fl[0]["reason"] != "电量不足" {
		t.Fatalf("开锁失败事件：%v", fl)
	}
	// 运营商同步拒绝
	e.og.set(func(f *fakeOpgw) { f.unlock, f.reason = "拒绝", "车辆故障" })
	r = e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020226"})
	if r.status != 409 || r.body["code"] != "UNLOCK_FAULT" {
		t.Fatalf("拒绝：%s", r.raw)
	}
	// 运营商同步拒绝并报「车辆无响应」：原因原样保留，不折成车辆故障
	e.og.set(func(f *fakeOpgw) { f.unlock, f.reason = "拒绝", "车辆无响应" })
	r = e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020226"})
	if r.status != 409 || r.body["code"] != "UNLOCK_NO_RESPONSE" {
		t.Fatalf("拒绝·无响应：%s", r.raw)
	}
	if o := e.order(r.body["orderId"].(string)); o.UnlockFailReason == nil || *o.UnlockFailReason != "车辆无响应" {
		t.Fatalf("原因应为车辆无响应：%+v", o.UnlockFailReason)
	}
	// 时限内未报告：车辆无响应
	e.og.set(func(f *fakeOpgw) { f.unlock = "无报告" })
	r = e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020226"})
	if r.status != 409 || r.body["code"] != "UNLOCK_NO_RESPONSE" {
		t.Fatalf("无响应：%s", r.raw)
	}
	noResp := r.body["orderId"].(string)
	// 超时后才报成功：立即请求结束，0 元（BR-03）
	late := &RideEvent{OperatorEventID: "LATE-1", OrderID: &noResp, PlateNo: "QH0004", BrandID: "BR01", OperatorID: "OP01", Kind: "开锁结果", ReceivedAt: time.Now()}
	late.Unlock = &struct {
		Result string    `json:"result"`
		Reason *string   `json:"reason"`
		At     time.Time `json:"at"`
	}{"成功", nil, time.Now()}
	res := e.do("POST", "/internal/ride/ride-events", "internal:opgw", late)
	if res.status != 200 || res.body["accepted"] != true || res.body["orderId"] != noResp {
		t.Fatalf("送达晚到报告：%s", res.raw)
	}
	o = e.order(noResp)
	if o.Status != StEnded || *o.UnlockResult != UnlockLate || *o.EndMethod != EndLateUnlock || *o.DurationSec != 0 {
		t.Fatalf("超时后成功：%+v", o)
	}
	eventually(t, 2*time.Second, func() bool {
		for _, c := range e.og.callList() {
			if c == "/internal/opgw/end-ride|"+noResp+"|超时后才报开锁成功" {
				return true
			}
		}
		return false
	})
	ended := e.outbox("ride.order.ended")
	if len(ended) != 1 || ended[0]["unlockResult"] != "超时后成功" {
		t.Fatalf("已结束事件：%v", ended)
	}
	// 重复送达：同样结果、不重复生效
	res = e.do("POST", "/internal/ride/ride-events", "internal:opgw", late)
	if res.status != 200 || res.body["orderId"] != noResp || len(e.outbox("ride.order.ended")) != 1 {
		t.Fatalf("重复送达：%s", res.raw)
	}
	// 失败之后换车成功：失败单记 afterFailOrderId
	e.og.set(func(f *fakeOpgw) { f.unlock = "成功" })
	ok := e.unlockOK("U100341", "SF86020227")
	found := false
	rows, _ := testPool.Query(context.Background(), `SELECT doc->>'afterFailOrderId' FROM orders WHERE status='开锁失败'`)
	for rows.Next() {
		var s *string
		_ = rows.Scan(&s)
		if s != nil && *s == ok {
			found = true
		}
	}
	rows.Close()
	if !found {
		t.Fatal("失败单应记下之后换车成功的订单")
	}
}

func TestUnlockAdmissionOrder(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		uid, code, want string
		status          int
	}{
		{"", "SF86020226", "NOT_LOGGED_IN", 401},
		{"", "SF71009215", "CODE_INVALID", 409}, // 车辆判定先于未登录
		{"U100036", "SF86020226", "HAS_ARREARS", 409},
		{"U100054", "SF86020226", "DEPOSIT_INSUFFICIENT", 409},
		{"U100598", "SF86020226", "ACCOUNT_CANCELLED", 409},
		{"U100341", "SF27239795", "CODE_NOT_ENABLED", 409},
		{"U100341", "nope", "CODE_NOT_FOUND", 404},
	}
	for _, c := range cases {
		r := e.do("POST", "/api/ride/unlock", c.uid, map[string]any{"code": c.code})
		if r.status != c.status || r.body["code"] != c.want || r.body["message"] == nil {
			t.Errorf("%s/%s：期望 %d %s，得 %d %s", c.uid, c.code, c.status, c.want, r.status, r.raw)
		}
	}
	r := e.do("POST", "/api/ride/unlock", "U100036", map[string]any{"code": "SF86020226"})
	if next := r.body["next"].(map[string]any); next["action"] != "payArrears" || r.body["amount"] != float64(1100) {
		t.Fatalf("欠费下一步：%s", r.raw)
	}
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM orders`).Scan(&n)
	if n != 0 {
		t.Fatal("准入不通过不建单")
	}
}

func TestUnlockConcurrentSameVehicle(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	codes := make([]string, 2)
	for i, uid := range []string{"U100341", "U100004"} {
		wg.Add(1)
		go func(i int, uid string) {
			defer wg.Done()
			r := e.do("POST", "/api/ride/unlock", uid, map[string]any{"code": "SF86020226"})
			if r.status == 200 {
				codes[i] = "OK"
			} else {
				codes[i] = r.body["code"].(string)
			}
		}(i, uid)
	}
	wg.Wait()
	okN := 0
	for _, c := range codes {
		if c == "OK" {
			okN++
		} else if c != "UNLOCK_OCCUPIED" {
			t.Fatalf("另一单应为车辆被占用：%v", codes)
		}
	}
	if okN != 1 {
		t.Fatalf("同一辆车同时只能一单成功：%v", codes)
	}
}

func decodeArr(t *testing.T, raw []byte) []any {
	t.Helper()
	var a []any
	if err := jsonUnmarshal(raw, &a); err != nil {
		t.Fatalf("不是数组：%s", raw)
	}
	return a
}

func TestSnapshotPileOf(t *testing.T) {
	s := &Snapshot{piles: map[string]*rules.Pile{"PL1": {PileID: "PL1", OccupiedBy: "V1"}, "PL2": {PileID: "PL2"}}}
	if p := s.pileOf("V1"); p == nil || *p != "PL1" {
		t.Fatalf("pileOf V1：%v", p)
	}
	if s.pileOf("V9") != nil || s.pileOf("") != nil {
		t.Fatal("无桩应为 nil")
	}
}

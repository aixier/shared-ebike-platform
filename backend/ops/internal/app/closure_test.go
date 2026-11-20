package app

import (
	"fmt"
	"testing"
	"time"
)

// SCHEMA 5.12 第三次降级 OPS-DG-20261127-01：实名核验不可用，15 单；
// 2 单待人工（OD2026112714209 缺锁车事实、OD2026112714304 扣款结果未知）；
// 12 名先骑后验新用户中 2 通过、2 未通过、8 待核验；收款 已收款 11、未收款 1、已转欠费 2、结果未知 1。
func seed1127(t *testing.T) (*env, string) {
	e := newEnv(t, "2026-11-27 21:06:44")
	e.seedRules()
	e.seedBrands()
	r := e.do("POST", "/api/ops/degrade/enter", e.duty(), map[string]any{"preset": "实名核验不可用"}, "")
	eid := r.body["eventId"].(string)
	if eid != "OPS-DG-20261127-01" {
		t.Fatalf("编号 %s", eid)
	}
	type spec struct {
		id, user, admission string
	}
	var orders []spec
	for i := 1; i <= 15; i++ {
		adm := "先骑后验新用户"
		user := fmt.Sprintf("U2%05d", i)
		switch {
		case i == 13:
			adm = "押金状态未知的老用户"
		case i >= 14:
			adm = "老用户"
		}
		id := fmt.Sprintf("OD20261127142%02d", i)
		if i == 9 {
			id = "OD2026112714209"
		}
		if i == 15 {
			id = "OD2026112714304"
		}
		orders = append(orders, spec{id, user, adm})
	}
	for i, o := range orders {
		req := fmt.Sprintf("2026-11-27 21:%02d:00", 8+i)
		h := e.head(o.id, o.user, "BR01", req, degraded(eid, o.admission))
		e.publish("ride.order.created", h)
		e.publish("ride.order.unlocked", merge(h, "startAt", e.ts(req)))
		if o.id == "OD2026112714209" {
			continue // 一直没有锁车报告
		}
		end := fmt.Sprintf("2026-11-27 21:%02d:30", 25+i)
		e.publish("ride.order.ended", merge(h, "startAt", e.ts(req), "endAt", e.ts(end)))
		e.publish("bill.bill.issued", map[string]any{"orderId": o.id, "userId": o.user, "brandId": "BR01", "operatorId": "OP01", "receivable": 250,
			"collectBy": "免押自动扣款", "billPushedAt": e.ts(end), "orderEndAt": e.ts(end), "degraded": true})
		switch {
		case o.id == "OD2026112714304":
			// 扣款结果未知（退出后由 pay 发）
		case i == 1 || i == 3:
			e.publish("bill.arrears.created", map[string]any{"orderId": o.id, "userId": o.user, "amount": 250, "userArrearsTotal": 250, "cause": "扣款失败", "createdAt": e.ts(end), "degraded": true})
		default:
			e.publish("pay.payment.succeeded", map[string]any{"paymentId": "PY" + o.id, "bizNo": o.id, "type": "订单扣款", "account": "骑行收入户", "userId": o.user,
				"orderId": o.id, "amount": 250, "channel": "微信支付", "completedAt": e.ts(end), "channelTradeNo": "T"})
		}
	}
	// 先骑后验补验：2 通过、2 未通过，其余待核验
	for i, st := range []string{"已核验", "已核验", "未通过", "未通过", "待核验"} {
		e.publish("acct.realname.changed", map[string]any{"userId": fmt.Sprintf("U2%05d", i+1), "status": st, "afterDegrade": true})
	}
	e.setNow("2026-11-27 21:47:19")
	if x := e.do("POST", "/api/ops/degrade/exit", e.duty(), nil, ""); x.status != 200 {
		t.Fatalf("退出 %s", x.raw)
	}
	e.publish("ride.order.pending_manual", merge(e.head("OD2026112714209", "U200009", "BR01", "2026-11-27 21:16:00", degraded(eid, "先骑后验新用户")),
		"missing", []string{"锁车事实"}, "userClaimedEndAt", e.ts("2026-11-27 21:40:00")))
	e.publish("pay.payment.unknown", map[string]any{"paymentId": "PY14304", "bizNo": "OD2026112714304", "type": "订单扣款", "account": "骑行收入户", "userId": "U200015",
		"orderId": "OD2026112714304", "amount": 250, "channel": "微信支付", "since": e.nowTS()})
	e.si.rideOrds["OD2026112714209"] = "待人工处理"
	e.si.payments["PY14304"] = "处理中"
	return e, eid
}

func TestClosureFromSample1127(t *testing.T) {
	e, eid := seed1127(t)
	duty := e.duty()
	d := e.do("GET", "/api/ops/degrade/events/"+eid, duty, nil, "")
	ev := d.body
	if ev["orderCount"] != float64(15) || ev["receivableSum"] != float64(14*250) {
		t.Fatalf("单数与应收 %v %v", ev["orderCount"], ev["receivableSum"])
	}
	adm := ev["admissionCounts"].(map[string]any)
	if adm["先骑后验新用户"] != float64(12) || adm["押金状态未知的老用户"] != float64(1) || adm["老用户"] != float64(2) {
		t.Fatalf("准入分类 %v", adm)
	}
	cl := ev["closure"].(map[string]any)
	if cl["status"] != "部分待人工" || cl["closed"] != float64(13) {
		t.Fatalf("闭合汇总 %v", cl)
	}
	pend := cl["pendingOrderIds"].([]any)
	if len(pend) != 2 || pend[0] != "OD2026112714209" || pend[1] != "OD2026112714304" {
		t.Fatalf("未闭合 %v", pend)
	}
	pr := cl["paymentResults"].(map[string]any)
	if pr["已收款"] != float64(11) || pr["已转欠费"] != float64(2) || pr["未收款"] != float64(1) || pr["结果未知"] != float64(1) {
		t.Fatalf("收款结果 %v", pr)
	}
	nv := cl["newUserVerification"].(map[string]any)
	if nv["通过"] != float64(2) || nv["未通过"] != float64(2) || nv["待核验"] != float64(8) {
		t.Fatalf("新用户补验 %v", nv)
	}
	if len(d.body["orders"].([]any)) != 15 {
		t.Fatal("逐单闭合项")
	}

	// 结果未知未超时：待闭合；超过 30 分钟：待人工
	list := e.do("GET", "/api/ops/closures", duty, nil, "")
	byID := map[string]map[string]any{}
	for _, x := range list.arr {
		m := x.(map[string]any)
		byID[m["orderId"].(string)] = m
	}
	if len(list.arr) != 2 || byID["OD2026112714209"]["status"] != "待人工" || byID["OD2026112714304"]["status"] != "待闭合" {
		t.Fatalf("默认列出未闭合 %s", list.raw)
	}
	m := byID["OD2026112714209"]["missing"].([]any)
	if len(m) != 2 || m[0] != "锁车事实" || m[1] != "收款结果" || byID["OD2026112714209"]["unlockResult"] != true {
		t.Fatalf("缺项 %v", byID["OD2026112714209"])
	}
	e.advance(31 * time.Minute)
	if err := e.a.SweepClosures(e.ctx()); err != nil {
		t.Fatal(err)
	}
	manual := e.do("GET", "/api/ops/closures?status=待人工", duty, nil, "")
	if len(manual.arr) != 2 {
		t.Fatalf("超时后两单待人工 %s", manual.raw)
	}
	td := e.do("GET", "/api/ops/todos", duty, nil, "")
	found := false
	for _, x := range td.arr {
		if x.(map[string]any)["key"] == "closure" && x.(map[string]any)["count"] == float64(2) && x.(map[string]any)["menu"] == "恢复闭合" {
			found = true
		}
	}
	if !found {
		t.Fatalf("待办 %s", td.raw)
	}
	d = e.do("GET", "/api/ops/degrade/events/"+eid, duty, nil, "")
	if acts := timelineActions(d.body); count(acts, "部分闭合") != 1 || count(acts, "全部闭合") != 0 {
		t.Fatalf("部分闭合记一次 %v", acts)
	}

	// 补录锁车事实：错误分支
	h := "/api/ops/closures/OD2026112714209/handle"
	if r := e.do("POST", h, duty, map[string]any{"action": "补录锁车事实", "endAt": e.ts("2026-11-27 21:40:00"), "reason": "短"}, ""); r.code() != "REASON_REQUIRED" {
		t.Fatalf("%s", r.raw)
	}
	if r := e.do("POST", h, duty, map[string]any{"action": "补录锁车事实", "endAt": e.ts("2026-11-27 21:00:00"), "reason": "用户来电确认还车"}, ""); r.code() != "INVALID" {
		t.Fatalf("锁车早于开锁 %s", r.raw)
	}
	if r := e.do("POST", h, duty, map[string]any{"action": "乱填", "reason": "用户来电确认还车"}, ""); r.code() != "INVALID" {
		t.Fatalf("未知动作 %s", r.raw)
	}
	if r := e.do("POST", h, duty, map[string]any{"action": "确认收款结果", "result": "已收款", "reason": "用户来电确认还车"}, ""); r.code() != "CONFLICT" {
		t.Fatalf("没有结果未知的扣款 %s", r.raw)
	}
	if r := e.do("POST", "/api/ops/closures/OD2099/handle", duty, map[string]any{"action": "补录锁车事实", "endAt": e.nowTS(), "reason": "用户来电确认还车"}, ""); r.status != 404 {
		t.Fatalf("不存在的单 %s", r.raw)
	}
	r := e.do("POST", h, duty, map[string]any{"action": "补录锁车事实", "endAt": e.ts("2026-11-27 21:40:00"), "stationId": "ST0001", "reason": "用户来电确认 21:40 还车"}, "h-1")
	if r.status != 200 || r.body["lockFact"] != true || r.body["status"] != "待人工" && r.body["status"] != "待闭合" {
		t.Fatalf("补录 %s", r.raw)
	}
	if r.body["status"] != "待闭合" {
		t.Fatalf("补录后等待收款 %s", r.raw)
	}
	if count(e.si.calls, "POST /internal/ride/orders/OD2026112714209/lock-fact") != 1 {
		t.Fatalf("调 ride 补录 %v", e.si.calls)
	}
	// 同一幂等键重放不再调 ride
	e.do("POST", h, duty, map[string]any{"action": "补录锁车事实", "endAt": e.ts("2026-11-27 21:40:00"), "reason": "用户来电确认 21:40 还车"}, "h-1")
	if count(e.si.calls, "POST /internal/ride/orders/OD2026112714209/lock-fact") != 1 {
		t.Fatal("幂等重放不重复调用 ride")
	}
	if r := e.do("POST", h, duty, map[string]any{"action": "补录锁车事实", "endAt": e.ts("2026-11-27 21:40:00"), "reason": "再补一次锁车"}, ""); r.code() != "CONFLICT" {
		t.Fatalf("已有锁车事实 %s", r.raw)
	}
	// ride 结束订单 → bill 出账收款
	hd := e.head("OD2026112714209", "U200009", "BR01", "2026-11-27 21:16:00", degraded(eid, "先骑后验新用户"))
	e.publish("ride.order.ended", merge(hd, "startAt", e.ts("2026-11-27 21:16:00"), "endAt", e.ts("2026-11-27 21:40:00")))
	e.publish("bill.bill.issued", map[string]any{"orderId": "OD2026112714209", "receivable": 1350, "collectBy": "一键支付", "billPushedAt": e.nowTS(), "orderEndAt": e.ts("2026-11-27 21:40:00"), "degraded": true})

	// 确认收款结果
	p := e.do("POST", "/api/ops/closures/OD2026112714304/handle", duty, map[string]any{"action": "确认收款结果", "result": "已收款", "reason": "向微信核实已扣款"}, "")
	if p.status != 200 || p.body["status"] != "已闭合" || p.body["payment"] != "已收款" || p.body["closedAt"] == nil {
		t.Fatalf("确认收款 %s", p.raw)
	}
	if e.si.payments["PY14304"] != "成功" {
		t.Fatal("调 pay 落定")
	}
	d = e.do("GET", "/api/ops/degrade/events/"+eid, duty, nil, "")
	cl = d.body["closure"].(map[string]any)
	if cl["status"] != "已全部闭合" || cl["closed"] != float64(15) || count(timelineActions(d.body), "全部闭合") != 1 {
		t.Fatalf("全部闭合 %v %v", cl, timelineActions(d.body))
	}
	if r := e.do("POST", "/api/ops/closures/OD2026112714304/handle", duty, map[string]any{"action": "确认收款结果", "result": "已收款", "reason": "重复确认一下"}, ""); r.code() != "CONFLICT" {
		t.Fatalf("已闭合 %s", r.raw)
	}
	audits := e.outboxPayloads("ops.audit.recorded")
	n := 0
	for _, a := range audits {
		if a["action"] == "闭合订单" && a["targetType"] == "order" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("恢复闭合处理留痕 %d", n)
	}
	if left := e.do("GET", "/api/ops/closures", duty, nil, ""); len(left.arr) != 0 {
		t.Fatalf("没有未闭合 %s", left.raw)
	}
}

func TestClosureUnlockFailedAndPayUnknownToArrears(t *testing.T) {
	e := newEnv(t, "2026-11-12 08:00:00")
	e.seedRules()
	r := e.do("POST", "/api/ops/degrade/enter", e.duty(), map[string]any{"preset": "支付通道故障"}, "")
	eid := r.body["eventId"].(string)
	h := e.head("OD2026111208001", "U1", "BR01", "2026-11-12 08:00:00", degraded(eid, "老用户"))
	e.publish("ride.order.created", h)
	e.publish("ride.order.unlock_failed", merge(h, "reason", "车辆无响应", "failedAt", e.ts("2026-11-12 08:00:30")))
	list := e.do("GET", "/api/ops/closures?eventId="+eid, e.duty(), nil, "")
	it := list.arr[0].(map[string]any)
	if it["status"] != "已闭合" || it["note"] != "开锁失败，不计费" {
		t.Fatalf("开锁失败不计费即闭合 %v", it)
	}
	// 结果未知 → 人工确认未收款 → 已转欠费
	h2 := e.head("OD2026111208002", "U2", "BR01", "2026-11-12 08:01:00", degraded(eid, "老用户"))
	e.publish("ride.order.created", h2)
	e.publish("ride.order.unlocked", merge(h2, "startAt", e.ts("2026-11-12 08:01:05")))
	e.publish("ride.order.ended", merge(h2, "startAt", e.ts("2026-11-12 08:01:05"), "endAt", e.ts("2026-11-12 08:20:00")))
	e.publish("pay.payment.unknown", map[string]any{"paymentId": "PYX", "type": "订单扣款", "orderId": "OD2026111208002", "since": e.nowTS()})
	e.si.payments["PYX"] = "处理中"
	r = e.do("POST", "/api/ops/closures/OD2026111208002/handle", e.duty(), map[string]any{"action": "确认收款结果", "result": "未收款", "reason": "渠道核实未扣款"}, "")
	if r.body["payment"] != "已转欠费" || r.body["status"] != "已闭合" {
		t.Fatalf("未收款转欠费 %s", r.raw)
	}
	// 非降级单不在闭合范围
	e.publish("ride.order.created", e.head("OD2026111208003", "U3", "BR01", "2026-11-12 08:02:00", nil))
	if r := e.do("POST", "/api/ops/closures/OD2026111208003/handle", e.duty(), map[string]any{"action": "补录锁车事实", "endAt": e.nowTS(), "reason": "测试非降级单"}, ""); r.status != 404 {
		t.Fatalf("非降级单 %s", r.raw)
	}
	// pay 返回 NOT_PENDING → CONFLICT
	h4 := e.head("OD2026111208004", "U4", "BR01", "2026-11-12 08:03:00", degraded(eid, "老用户"))
	e.publish("ride.order.created", h4)
	e.publish("pay.payment.unknown", map[string]any{"paymentId": "PYY", "type": "订单扣款", "orderId": "OD2026111208004", "since": e.nowTS()})
	e.si.payments["PYY"] = "成功"
	if r := e.do("POST", "/api/ops/closures/OD2026111208004/handle", e.duty(), map[string]any{"action": "确认收款结果", "result": "已收款", "reason": "渠道核实已扣款"}, ""); r.code() != "CONFLICT" {
		t.Fatalf("NOT_PENDING %s", r.raw)
	}
}

func timelineActions(ev map[string]any) []string {
	var out []string
	for _, x := range ev["timeline"].([]any) {
		out = append(out, x.(map[string]any)["action"].(string))
	}
	return out
}

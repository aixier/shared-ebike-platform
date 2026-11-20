package app

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// VH000004 的车辆标识（来自原型 vehicles.json）
const tagVH4 = "E28011C79479F55A38EC7779"

func vehicleTag(t *testing.T, id string) string {
	t.Helper()
	var tag string
	if err := testPool.QueryRow(context.Background(), `SELECT tag FROM vehicle_tags WHERE vehicle_id=$1`, id).Scan(&tag); err != nil {
		t.Fatal(err)
	}
	return tag
}

func occMsg(dev, msgID, ts string, slots ...map[string]any) map[string]any {
	return map[string]any{"msgId": msgID, "deviceId": dev, "ts": ts, "slots": slots}
}

func slot(no int, present bool, tag string) map[string]any {
	m := map[string]any{"slotNo": no, "present": present, "tagType": "RFID"}
	if tag != "" {
		m["tag"] = tag
	}
	return m
}

const nowTS = "2026-12-01T12:00:00+08:00"

func (h *harness) device(kind, key string, body any) resp {
	return h.req("POST", "/device/v1/"+kind, body, "X-Device-Key", key)
}

func TestOccupancyEnterLeaveDedupe(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	p := freeSlot(t, "ST0003")
	r := h.device("occupancy", "dv3-secret", occMsg("DV0003", "m-1", "2026-12-01T11:59:58+08:00", slot(p.No, true, tagVH4)))
	mustStatus(t, r, 200)
	if r.Body["received"] != true || r.Body["duplicate"] != false || r.Body["recognized"] != true {
		t.Fatalf("回执错误：%s", r.Raw)
	}
	ev := h.outboxEvents("site.pile.occupancy")
	if len(ev) != 1 {
		t.Fatalf("应发 1 条桩在位：%v", ev)
	}
	pl := payload(ev[0])
	if pl["pileId"] != p.ID || pl["stationId"] != "ST0003" || pl["vehicleId"] != "VH000004" || pl["present"] != true ||
		pl["at"] != "2026-12-01T11:59:58+08:00" || pl["receivedAt"] != nowTS || ev[0]["key"] != p.ID {
		t.Fatalf("桩在位载荷错误：%v", pl)
	}
	if st := pileState(t, p.ID); !st.Occupied || !strEq(st.VehicleID, "VH000004") {
		t.Fatalf("桩应记为在位：%+v", st)
	}
	// 同一 msgId 重复到达只生效一次
	r = h.device("occupancy", "dv3-secret", occMsg("DV0003", "m-1", nowTS, slot(p.No, false, "")))
	if r.Body["duplicate"] != true || len(h.outboxEvents("site.pile.occupancy")) != 1 {
		t.Fatalf("重复消息不应再次生效：%s", r.Raw)
	}
	// 同样的状态换个 msgId 再报：状态没变，不发事件
	h.device("occupancy", "dv3-secret", occMsg("DV0003", "m-2", nowTS, slot(p.No, true, tagVH4)))
	if len(h.outboxEvents("site.pile.occupancy")) != 1 {
		t.Fatal("状态未变不应发事件")
	}
	// 离位（标识可空）
	h.device("occupancy", "dv3-secret", occMsg("DV0003", "m-3", nowTS, slot(p.No, false, "")))
	ev = h.outboxEvents("site.pile.occupancy")
	if len(ev) != 2 || payload(ev[1])["present"] != false || payload(ev[1])["vehicleId"] != "VH000004" {
		t.Fatalf("离位事件错误：%v", ev)
	}
	if st := pileState(t, p.ID); st.Occupied || st.VehicleID != nil {
		t.Fatalf("离位后桩应空闲：%+v", st)
	}
}

func TestUnrecognizedTag(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	p := freeSlot(t, "ST0003")
	r := h.device("occupancy", "dv3-secret", occMsg("DV0003", "u-1", nowTS, slot(p.No, true, "FFFF0000")))
	mustStatus(t, r, 200)
	if r.Body["recognized"] != false {
		t.Fatalf("未知标识应回 recognized=false：%s", r.Raw)
	}
	if len(h.outboxEvents("site.pile.occupancy")) != 0 {
		t.Fatal("识别不出的标识不应发在位事件")
	}
	if n := countRows(t, `SELECT count(*) FROM pile_unrecognized WHERE pile_id=$1 AND tag='FFFF0000'`, p.ID); n != 1 {
		t.Fatalf("应记录未识别标识：%d", n)
	}
	// 桩位被占着，不算空闲
	st := h.admin("GET", "/api/site/admin/stations/ST0003", nil)
	if st.Body["freePiles"] != float64(4) {
		t.Fatalf("被未知车辆占用的桩位不应算空闲：%v", st.Body["freePiles"])
	}
	// 离位也不发事件
	h.device("occupancy", "dv3-secret", occMsg("DV0003", "u-2", nowTS, slot(p.No, false, "")))
	if len(h.outboxEvents("site.pile.occupancy")) != 0 {
		t.Fatal("未知车辆离位不应发事件")
	}
}

func TestOneVehiclePerSlotAndOneSlotPerVehicle(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	a := freeSlot(t, "ST0003")
	tag707 := vehicleTag(t, "VH000707")
	h.device("occupancy", "dv3-secret", occMsg("DV0003", "r-1", nowTS, slot(a.No, true, tagVH4)))
	// 同一桩位报告换了一辆车：原车离位、新车入位
	h.device("occupancy", "dv3-secret", occMsg("DV0003", "r-2", nowTS, slot(a.No, true, tag707)))
	ev := h.outboxEvents("site.pile.occupancy")
	if len(ev) != 3 || payload(ev[1])["vehicleId"] != "VH000004" || payload(ev[1])["present"] != false ||
		payload(ev[2])["vehicleId"] != "VH000707" || payload(ev[2])["present"] != true {
		t.Fatalf("换车应先离位后入位：%v", ev)
	}
	// VH000707 出现在另一个桩位：原桩位离位
	b := freeSlot(t, "ST0003")
	h.device("occupancy", "dv3-secret", occMsg("DV0003", "r-3", nowTS, slot(b.No, true, tag707)))
	ev = h.outboxEvents("site.pile.occupancy")
	if len(ev) != 5 || payload(ev[3])["pileId"] != a.ID || payload(ev[3])["present"] != false || payload(ev[4])["pileId"] != b.ID {
		t.Fatalf("一辆车只在一个桩位：%v", ev)
	}
	if st := pileState(t, a.ID); st.Occupied {
		t.Fatal("原桩位应清空")
	}
}

func TestSnapshotAlignsOnlyChanges(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	// ST0001 的 11 个桩都有车；全量报告 1 号位空、其余照旧
	var slots []map[string]any
	ps := h.admin("GET", "/api/site/admin/stations/ST0001", nil).Body["piles"].([]any)
	for _, x := range ps {
		m := x.(map[string]any)
		no := int(m["no"].(float64))
		if no == 1 {
			slots = append(slots, slot(no, false, ""))
			continue
		}
		slots = append(slots, slot(no, true, vehicleTag(t, m["occupiedVehicleId"].(string))))
	}
	r := h.device("snapshot", "dv1-secret", occMsg("DV0001", "s-1", nowTS, slots...))
	mustStatus(t, r, 200)
	ev := h.outboxEvents("site.pile.occupancy")
	if len(ev) != 1 || payload(ev[0])["pileId"] != "PL00001" || payload(ev[0])["present"] != false || payload(ev[0])["vehicleId"] != "VH000036" {
		t.Fatalf("全量只对变化的桩位发事件：%v", ev)
	}
	if st := h.admin("GET", "/api/site/admin/stations/ST0001", nil); st.Body["fullNow"] != false || st.Body["freePiles"] != float64(1) {
		t.Fatalf("空出一个桩位后不再桩满：%v", st.Body)
	}
}

func TestClockSkewUsesReceivedTime(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	p := freeSlot(t, "ST0003")
	h.device("occupancy", "dv3-secret", occMsg("DV0003", "c-1", "2026-11-30T08:00:00+08:00", slot(p.No, true, tagVH4)))
	ev := h.outboxEvents("site.pile.occupancy")
	if payload(ev[0])["at"] != nowTS {
		t.Fatalf("设备时钟偏差过大应取收到时刻：%v", payload(ev[0]))
	}
	// 毫秒时间戳也接受
	q := freeSlot(t, "ST0003")
	h.device("occupancy", "dv3-secret", map[string]any{"msgId": "c-2", "deviceId": "DV0003", "ts": protoNow.Add(-3 * time.Second).UnixMilli(), "slots": []any{slot(q.No, true, vehicleTag(t, "VH000707"))}})
	ev = h.outboxEvents("site.pile.occupancy")
	if payload(ev[1])["at"] != "2026-12-01T11:59:57+08:00" {
		t.Fatalf("毫秒时间戳解析错误：%v", payload(ev[1]))
	}
}

func TestHeartbeatStatus(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	// ST0003 里不在线的那个桩
	var off pileRow
	for _, x := range h.admin("GET", "/api/site/admin/piles?stationId=ST0003", nil, "pile.edit").Body["rows"].([]any) {
		m := x.(map[string]any)
		if m["status"] != "在线" {
			off = pileState(t, m["pileId"].(string))
		}
	}
	if off.ID == "" {
		t.Fatal("样例里 ST0003 应有一个不在线的桩")
	}
	hb := func(id string, slots ...map[string]any) resp {
		return h.device("heartbeat", "dv3-secret", map[string]any{"msgId": id, "deviceId": "DV0003", "ts": nowTS, "slots": slots, "fw": "1.0", "rssi": -60})
	}
	r := hb("h-1", map[string]any{"slotNo": off.No, "status": "正常"})
	mustStatus(t, r, 200)
	st := pileState(t, off.ID)
	if off.ManualFault {
		if st.Status != "故障" {
			t.Fatal("人工标为故障的桩不应因心跳自动恢复")
		}
		// 人工改回在线后，心跳才能管它
		h.admin("POST", "/api/site/admin/piles/"+off.ID+"/status", map[string]any{"status": "离线", "reason": "改为离线测试"}, "pile.edit")
		hb("h-1b", map[string]any{"slotNo": off.No, "status": "正常"})
		st = pileState(t, off.ID)
	}
	if st.Status != "在线" || st.LastHB == nil || !st.LastHB.Equal(protoNow) {
		t.Fatalf("心跳正常应恢复在线并记心跳时刻：%+v", st)
	}
	// 自检故障
	hb("h-2", map[string]any{"slotNo": off.No, "status": "故障"})
	if st := pileState(t, off.ID); st.Status != "故障" || !strEq(st.FaultNote, "设备自检故障") || st.ManualFault {
		t.Fatalf("自检故障应转故障：%+v", st)
	}
	hb("h-3", map[string]any{"slotNo": off.No, "status": "正常"})
	if pileState(t, off.ID).Status != "在线" {
		t.Fatal("自检恢复后应回到在线")
	}
	// 人工标故障后，心跳正常也不恢复
	h.admin("POST", "/api/site/admin/piles/"+off.ID+"/status", map[string]any{"status": "故障", "reason": "人工报修测试"}, "pile.edit")
	hb("h-4", map[string]any{"slotNo": off.No, "status": "正常"})
	if pileState(t, off.ID).Status != "故障" {
		t.Fatal("人工故障不应被心跳恢复")
	}
	mustCode(t, hb("h-5", map[string]any{"slotNo": off.No, "status": "坏了"}), 400, "INVALID")
}

func TestOfflineScan(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	online := countRows(t, `SELECT count(*) FROM piles WHERE status='在线'`)
	// ST0003 的控制器 4 分钟里一直在报心跳
	h.clock.Add(4 * time.Minute)
	h.device("heartbeat", "dv3-secret", map[string]any{"msgId": "o-1", "deviceId": "DV0003", "ts": "2026-12-01T12:04:00+08:00", "slots": []any{}})
	n, err := h.app.ScanOffline(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if k := countRows(t, `SELECT count(*) FROM piles WHERE station_id='ST0003' AND fault_note='心跳超时'`); k != 0 {
		t.Fatalf("有心跳的控制器下的桩不应离线：%d", k)
	}
	if n == 0 || n != countRows(t, `SELECT count(*) FROM piles WHERE status='离线' AND fault_note='心跳超时'`) || n >= online+1 {
		t.Fatalf("心跳超时判离线数量错误：%d", n)
	}
	ev := h.outboxEvents("site.pile.status_changed")
	if len(ev) < n || payload(ev[len(ev)-1])["status"] != "离线" {
		t.Fatalf("离线应发桩状态变更：%d", len(ev))
	}
	if p := pileState(t, "PL00001"); p.Status != "离线" || !strEq(p.FaultNote, "心跳超时") {
		t.Fatalf("PL00001 应因心跳超时离线：%+v", p)
	}
	// 再扫一次不重复
	if n2, _ := h.app.ScanOffline(context.Background()); n2 != 0 {
		t.Fatalf("已离线的不应重复处理：%d", n2)
	}
}

func TestDeviceHTTPAuthAndValidation(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	body := occMsg("DV0003", "a-1", nowTS, slot(1, false, ""))
	mustCode(t, h.device("occupancy", "wrong", body), 401, "DEVICE_UNAUTHORIZED")
	mustCode(t, h.device("occupancy", "", body), 401, "DEVICE_UNAUTHORIZED")
	mustCode(t, h.device("occupancy", "dv1-secret", body), 401, "DEVICE_UNAUTHORIZED") // 别的控制器的密钥
	mustCode(t, h.device("occupancy", "x", occMsg("DV7777", "a-1", nowTS)), 401, "DEVICE_UNAUTHORIZED")
	mustCode(t, h.device("occupancy", "dv3-secret", map[string]any{"deviceId": "DV0003", "ts": nowTS}), 400, "INVALID")
	mustCode(t, h.device("occupancy", "dv3-secret", "不是json"), 400, "INVALID")
	// 未登记的桩位只忽略并在回执里列出
	r := h.device("occupancy", "dv3-secret", occMsg("DV0003", "a-2", nowTS, slot(99, true, tagVH4)))
	mustStatus(t, r, 200)
	if u := r.Body["unknownSlots"].([]any); len(u) != 1 || u[0] != float64(99) {
		t.Fatalf("应列出未登记桩位：%s", r.Raw)
	}
	// 厂商设备也可用厂商密钥走标准入口
	mustStatus(t, h.device("heartbeat", "acme-vendor-key", map[string]any{"msgId": "a-3", "deviceId": "DV0101", "ts": nowTS, "slots": []any{}}), 200)
}

func TestHTTPAccessDisabled(t *testing.T) {
	h := newHarness(t, func(c *configT) { c.AccessHTTP = false })
	r := h.device("occupancy", "dv3-secret", occMsg("DV0003", "x", nowTS))
	if r.Status != 404 && r.Status != 405 {
		t.Fatalf("未启用 HTTP 接入时设备入口不应存在：%d", r.Status)
	}
}

func TestSinglePileDevice(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	p := pileState(t, "PL00012")
	if p.Occupied {
		h.device("occupancy", "dv9001-secret", occMsg("DV9001", "sp-0", nowTS, slot(1, false, "")))
	}
	h.device("occupancy", "dv9001-secret", occMsg("DV9001", "sp-1", nowTS, slot(1, true, tagVH4)))
	if st := pileState(t, "PL00012"); !strEq(st.VehicleID, "VH000004") {
		t.Fatalf("单桩直连设备的 1 号桩位应对应其桩：%+v", st)
	}
}

func TestVendorHTTPAdapter(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	// ST0004 有车在位的第一个桩位先报离位
	var bay int
	if err := testPool.QueryRow(context.Background(), `SELECT no FROM piles WHERE station_id='ST0004' AND occupied ORDER BY no LIMIT 1`).Scan(&bay); err != nil {
		t.Fatal(err)
	}
	body := `{"ctrl":"DV0101","seq":1,"time":%d,"type":"leave","bays":[{"bay":%d}]}`
	r := h.req("POST", "/device/v1/vendor/acme", fmt.Sprintf(body, protoNow.UnixMilli(), bay), "X-Device-Key", "acme-vendor-key")
	mustStatus(t, r, 200)
	ev := h.outboxEvents("site.pile.occupancy")
	if len(ev) != 1 || payload(ev[0])["stationId"] != "ST0004" || payload(ev[0])["present"] != false {
		t.Fatalf("厂商报文应转成标准消息并发事件：%v", ev)
	}
	park := fmt.Sprintf(`{"ctrl":"DV0101","seq":2,"time":%d,"type":"park","bays":[{"bay":%d,"card":"%s"}]}`, protoNow.UnixMilli(), bay, tagVH4)
	mustStatus(t, h.req("POST", "/device/v1/vendor/acme", park, "X-Device-Key", "dv101-secret"), 200) // 控制器自己的密钥也行
	ev = h.outboxEvents("site.pile.occupancy")
	if len(ev) != 2 || payload(ev[1])["vehicleId"] != "VH000004" {
		t.Fatalf("厂商入位应发事件：%v", ev)
	}
	mustCode(t, h.req("POST", "/device/v1/vendor/acme", park, "X-Device-Key", "wrong"), 401, "DEVICE_UNAUTHORIZED")
	mustCode(t, h.req("POST", "/device/v1/vendor/nobody", park, "X-Device-Key", "x"), 404, "NOT_FOUND")
	mustCode(t, h.req("POST", "/device/v1/vendor/acme", `{"ctrl":"DV0101","type":"dance"}`, "X-Device-Key", "acme-vendor-key"), 400, "INVALID")
	// 标准控制器不能冒充厂商
	mustCode(t, h.req("POST", "/device/v1/vendor/acme", `{"ctrl":"DV0003","seq":3,"time":1,"type":"beat","bays":[]}`, "X-Device-Key", "dv3-secret"), 401, "DEVICE_UNAUTHORIZED")
}

func TestLegacyPresence(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	p := freeSlot(t, "ST0003")
	in := map[string]any{"pileId": p.ID, "pileTag": tagVH4, "pileTagType": "RFID", "event": "入位", "at": nowTS, "seq": "L-1"}
	r := h.req("POST", "/device/v1/pile-presence", in, "X-Device-Key", "dv3-secret")
	mustStatus(t, r, 200)
	if r.Body["received"] != true || r.Body["recognized"] != true || r.Body["duplicate"] != false {
		t.Fatalf("回执错误：%s", r.Raw)
	}
	r = h.req("POST", "/device/v1/pile-presence", in, "X-Device-Key", "dv3-secret")
	if r.Body["duplicate"] != true || len(h.outboxEvents("site.pile.occupancy")) != 1 {
		t.Fatalf("同一 seq 只生效一次：%s", r.Raw)
	}
	other := map[string]any{"pileId": p.ID, "pileTag": vehicleTag(t, "VH000707"), "event": "入位", "at": nowTS, "seq": "L-2"}
	mustCode(t, h.req("POST", "/device/v1/pile-presence", other, "X-Device-Key", "dv3-secret"), 409, "CONFLICT")
	leave := map[string]any{"pileId": p.ID, "pileTag": tagVH4, "event": "离位", "at": nowTS, "seq": "L-3"}
	mustStatus(t, h.req("POST", "/device/v1/pile-presence", leave, "X-Device-Key", "dv3-secret"), 200)
	if ev := h.outboxEvents("site.pile.occupancy"); len(ev) != 2 || payload(ev[1])["present"] != false {
		t.Fatalf("离位应发事件：%v", ev)
	}
	unk := map[string]any{"pileId": p.ID, "pileTag": "ABCDEF", "event": "入位", "at": nowTS, "seq": "L-4"}
	if r := h.req("POST", "/device/v1/pile-presence", unk, "X-Device-Key", "dv3-secret"); r.Body["recognized"] != false {
		t.Fatalf("未知标识 recognized 应为 false：%s", r.Raw)
	}
	mustCode(t, h.req("POST", "/device/v1/pile-presence", in, "X-Device-Key", "dv1-secret"), 401, "DEVICE_UNAUTHORIZED")
	in["pileId"] = "PL99999"
	mustCode(t, h.req("POST", "/device/v1/pile-presence", in, "X-Device-Key", "dv3-secret"), 404, "NOT_FOUND")
	mustCode(t, h.req("POST", "/device/v1/pile-presence", map[string]any{"pileId": p.ID, "event": "路过", "at": nowTS, "seq": "x"}, "X-Device-Key", "dv3-secret"), 400, "INVALID")
}

func TestLegacyHeartbeat(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	p := freeSlot(t, "ST0003")
	r := h.req("POST", "/device/v1/pile-heartbeat", map[string]any{"pileId": p.ID, "at": nowTS, "selfCheck": "正常", "occupiedPileTag": tagVH4}, "X-Device-Key", "dv3-secret")
	mustStatus(t, r, 200)
	if r.Body["received"] != true || r.Body["serverTime"] != nowTS {
		t.Fatalf("回执错误：%s", r.Raw)
	}
	if ev := h.outboxEvents("site.pile.occupancy"); len(ev) != 1 || payload(ev[0])["vehicleId"] != "VH000004" {
		t.Fatalf("心跳带在位标识应对齐并发事件：%v", ev)
	}
	// 不带 occupiedPileTag 不动在位状态
	h.req("POST", "/device/v1/pile-heartbeat", map[string]any{"pileId": p.ID, "at": nowTS, "selfCheck": "故障", "faultNote": "读卡器无响应"}, "X-Device-Key", "dv3-secret")
	st := pileState(t, p.ID)
	if !st.Occupied || st.Status != "故障" || !strEq(st.FaultNote, "读卡器无响应") {
		t.Fatalf("心跳自检故障错误：%+v", st)
	}
	// occupiedPileTag 为 null 表示无车
	h.req("POST", "/device/v1/pile-heartbeat", map[string]any{"pileId": p.ID, "at": nowTS, "occupiedPileTag": nil}, "X-Device-Key", "dv3-secret")
	if st := pileState(t, p.ID); st.Occupied || st.Status != "在线" {
		t.Fatalf("对齐为无车并恢复在线：%+v", st)
	}
	mustCode(t, h.req("POST", "/device/v1/pile-heartbeat", map[string]any{"pileId": p.ID, "at": "昨天"}, "X-Device-Key", "dv3-secret"), 400, "INVALID")
}

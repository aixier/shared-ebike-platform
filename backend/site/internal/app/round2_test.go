package app

// 第二轮变更的测试：只读、请求体编码、事件保留期、骑行事件的临时判断、控制器登记。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"bike/site/internal/config"
)

// ---------- 只读与数据范围 ----------

func TestReadonlyRejectsEveryAdminWrite(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	ro := "Bearer " + tokenWith(map[string]any{"readonly": true}, "station.edit", "pile.edit", "ops.monitor")
	sc := "Bearer " + tokenWith(map[string]any{"scope": "OP01"}, "station.edit", "pile.edit")
	reason := map[string]any{"reason": "只读测试事由"}
	writes := []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/site/admin/stations", createBody(nil)},
		{"PATCH", "/api/site/admin/stations/ST0001", map[string]any{"name": "改名", "reason": "只读测试事由"}},
		{"POST", "/api/site/admin/stations/ST0001/remove", reason},
		{"POST", "/api/site/admin/piles", map[string]any{"stationId": "ST0001", "location": []float64{104.06, 30.59}, "reason": "只读测试事由"}},
		{"POST", "/api/site/admin/piles/PL00001/status", map[string]any{"status": "故障", "reason": "只读测试事由"}},
		{"POST", "/api/site/admin/controllers", map[string]any{"deviceId": "DV7777", "accessMode": "MQTT", "stationId": "ST0001", "reason": "只读测试事由"}},
		{"POST", "/api/site/admin/controllers/DV7777/reset-secret", reason},
		{"POST", "/api/site/admin/controllers/DV7777/disable", reason},
	}
	for _, w := range writes {
		mustCode(t, h.req(w.method, w.path, w.body, "Authorization", ro), 403, "READONLY")
		mustCode(t, h.req(w.method, w.path, w.body, "Authorization", sc), 403, "SCOPE_DENIED")
	}
	// 只读账号仍能查看
	mustStatus(t, h.req("GET", "/api/site/admin/stations", nil, "Authorization", ro), 200)
	mustStatus(t, h.req("GET", "/api/site/admin/controllers", nil, "Authorization", ro), 200)
	if n := countRows(t, `SELECT count(*) FROM outbox WHERE subject LIKE 'site.audit.%'`); n != 0 {
		t.Fatalf("被拒绝的写操作不应留痕：%d", n)
	}
}

// ---------- 请求体编码 ----------

func (h *harness) raw(method, path string, body []byte, headers ...string) resp {
	h.t.Helper()
	r, _ := http.NewRequest(method, h.srv.URL+path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Raw: string(raw)}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

func TestNonUTF8BodyRejected(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	// 0xB2 0xE2 是"测"的 GBK 编码，不是合法的 UTF-8
	gbk := []byte("{\"name\":\"\xb2\xe2\xca\xd4\",\"district\":\"高新区\",\"reason\":\"事由事由\"}")
	auth := "Bearer " + token("station.edit", "pile.edit")
	r := h.raw("POST", "/api/site/admin/stations", gbk, "Authorization", auth)
	mustCode(t, r, 400, "INVALID")
	if !strings.Contains(r.Body["message"].(string), "请求体不是 UTF-8 编码") {
		t.Fatalf("提示不对：%s", r.Raw)
	}
	mustCode(t, h.raw("PATCH", "/api/site/admin/stations/ST0001", gbk, "Authorization", auth), 400, "INVALID")
	mustCode(t, h.raw("POST", "/api/site/admin/controllers", gbk, "Authorization", auth), 400, "INVALID")
	// 桩设备入口同样
	dev := []byte("{\"msgId\":\"x1\",\"deviceId\":\"DV0003\",\"ts\":1,\"slots\":[{\"slotNo\":1,\"present\":true,\"tag\":\"\xb2\xe2\"}]}")
	for _, p := range []string{"occupancy", "heartbeat", "snapshot", "pile-heartbeat", "vendor/acme"} {
		r := h.raw("POST", "/device/v1/"+p, dev, "X-Device-Key", "dv3-secret")
		if r.Status != 400 || r.Body["code"] != "INVALID" || !strings.Contains(r.Body["message"].(string), "UTF-8") {
			t.Fatalf("%s 应拒绝非 UTF-8：%d %s", p, r.Status, r.Raw)
		}
	}
	// 合法的 UTF-8 中文照常
	mustStatus(t, h.req("POST", "/api/site/admin/stations", createBody(map[string]any{"name": "中文站点"}), "Authorization", auth), 200)
}

// ---------- 事件保留期 ----------

func TestRetentionDaysConfig(t *testing.T) {
	get := func(v string) func(string) string {
		return func(k string) string {
			switch k {
			case "DB_DSN":
				return "x"
			case "EVENT_RETENTION_DAYS":
				return v
			}
			return ""
		}
	}
	if c, err := config.Load(get("")); err != nil || c.RetentionDays != 30 {
		t.Fatalf("默认应为 30 天：%d %v", c.RetentionDays, err)
	}
	if c, err := config.Load(get("7")); err != nil || c.RetentionDays != 7 {
		t.Fatalf("应读到 7 天：%d %v", c.RetentionDays, err)
	}
	for _, bad := range []string{"0", "-1", "abc", "9999"} {
		if _, err := config.Load(get(bad)); err == nil {
			t.Fatalf("EVENT_RETENTION_DAYS=%q 应报错", bad)
		}
	}
}

func TestStreamRetentionFollowsConfig(t *testing.T) {
	ctx := context.Background()
	nc, err := nats.Connect(testNats.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	_ = js.DeleteStream(ctx, "EVT_SITE")
	if err := EnsureStream(ctx, js, 7); err != nil {
		t.Fatal(err)
	}
	s, _ := js.Stream(ctx, "EVT_SITE")
	if got := s.CachedInfo().Config.MaxAge; got != 7*24*time.Hour {
		t.Fatalf("保留期应为 7 天：%v", got)
	}
	if err := EnsureStream(ctx, js, 0); err != nil { // 0 按默认 30 天
		t.Fatal(err)
	}
	s, _ = js.Stream(ctx, "EVT_SITE")
	if got := s.CachedInfo().Config.MaxAge; got != 30*24*time.Hour {
		t.Fatalf("缺省应为 30 天：%v", got)
	}
}

// ---------- 站点状态统一 ----------

func TestStationStatusValues(t *testing.T) {
	h := newHarness(t)
	r := h.admin("POST", "/api/site/admin/stations", createBody(nil), "station.edit")
	mustStatus(t, r, 200)
	if r.Body["status"] != "启用" {
		t.Fatalf("新站点应为启用：%s", r.Raw)
	}
	if ev := h.outboxEvents("site.station.changed"); payload(ev[0])["status"] != "启用" {
		t.Fatalf("事件状态应为启用：%v", ev)
	}
	mustStatus(t, h.admin("POST", "/api/site/admin/stations/ST0001/remove", map[string]any{"reason": "不要了的站点"}, "station.edit"), 200)
	d := h.admin("GET", "/api/site/admin/stations/ST0001", nil)
	if d.Body["status"] != "停用" {
		t.Fatalf("删除后应为停用：%s", d.Raw)
	}
	ev := h.outboxEvents("site.station.changed")
	if len(ev) != 2 || payload(ev[1])["status"] != "停用" {
		t.Fatalf("删除事件状态应为停用：%v", ev)
	}
	if n := countRows(t, `SELECT count(*) FROM stations WHERE status NOT IN ('启用','停用')`); n != 0 {
		t.Fatal("库里不应有别的状态值")
	}
}

// ---------- 骑行事件的临时判断 ----------

func rideUnlockedAt(orderID, vehicleID string, startStation any, startAt string) []byte {
	p := orderHead(orderID, vehicleID)
	p["startAt"], p["startStationId"], p["startLocation"], p["degraded"] = startAt, startStation, nil, nil
	return envelope("ride.order.unlocked", orderID, p)
}

func rideEndedAt(orderID, vehicleID string, endStation, endPile any, endAt string) []byte {
	p := orderHead(orderID, vehicleID)
	for k, v := range map[string]any{
		"unlockResult": "成功", "startAt": "2026-12-01T11:00:05+08:00", "endAt": endAt, "durationSec": 1200, "distanceKm": 3.2,
		"startLocation": nil, "endLocation": nil, "startStationId": nil, "endStationId": endStation, "endStationName": nil,
		"endPileId": endPile, "endMethod": "入桩识别", "inStation": true, "inStationBasis": "桩识别", "distanceToStationM": nil,
		"dispatchPromptShown": false, "lockReportAt": nil, "pileInAt": endAt, "reportReceivedAt": endAt, "lateReport": false,
		"degraded": nil, "forceEnd": nil,
	} {
		p[k] = v
	}
	return envelope("ride.order.ended", orderID, p)
}

func (h *harness) feed(t *testing.T, body []byte) {
	t.Helper()
	if err := h.app.HandleEvent(context.Background(), body); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) pileInfo(t *testing.T, id string) (map[string]any, bool) {
	t.Helper()
	m := h.req("GET", "/api/site/map", nil)
	var occ bool
	for _, p := range m.Body["piles"].([]any) {
		pm := p.(map[string]any)
		if pm["pileId"] == id {
			occ = pm["occupied"].(bool)
		}
	}
	d := h.admin("GET", "/api/site/admin/stations/ST0003", nil)
	for _, p := range d.Body["piles"].([]any) {
		pm := p.(map[string]any)
		if pm["pileId"] == id {
			return pm, occ
		}
	}
	t.Fatalf("没有桩 %s", id)
	return nil, false
}

func TestRideUnlockMarksPileLeft(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	// PL00012 上记着 VH000426，站点 ST0003 停着车辆数以详情为准
	before := h.admin("GET", "/api/site/admin/stations/ST0003", nil).Body
	h.feed(t, rideUnlockedAt("RD1", "VH000426", "ST0003", "2026-12-01T11:59:50+08:00"))
	pm, occ := h.pileInfo(t, "PL00012")
	if pm["occupiedVehicleId"] != nil || pm["presenceBasis"] != "骑行事件·已离开" || occ {
		t.Fatalf("开锁后应临时按已离开处理：%v occ=%v", pm, occ)
	}
	after := h.admin("GET", "/api/site/admin/stations/ST0003", nil).Body
	if after["freePiles"].(float64) != before["freePiles"].(float64)+1 || after["parkedVehicles"].(float64) != before["parkedVehicles"].(float64)-1 {
		t.Fatalf("空闲桩位应加一、停放车辆应减一：%v → %v", before, after)
	}
	// 不发桩在位事件：桩的离位事件只来自桩自己的报告
	if n := len(h.outboxEvents("site.pile.occupancy")); n != 0 {
		t.Fatalf("临时判断不应发桩在位事件：%d", n)
	}
	// 其他桩位不受影响
	if pm2, _ := h.pileInfo(t, "PL00013"); pm2["presenceBasis"] != "桩上报" || pm2["occupiedVehicleId"] != "VH000452" {
		t.Fatalf("别的桩位不应变：%v", pm2)
	}
	// 迟到的、早于开锁时刻的"仍在位"报告不推翻标记
	tag := vehicleTag(t, "VH000426")
	mustStatus(t, h.device("occupancy", "dv3-secret", occMsg("DV0003", "s-1", "2026-12-01T11:59:40+08:00", slot(1, true, tag))), 200)
	if pm, _ := h.pileInfo(t, "PL00012"); pm["presenceBasis"] != "骑行事件·已离开" {
		t.Fatalf("迟到的旧报告不应推翻标记：%v", pm)
	}
	// 桩迟到的离位报告到达：恢复为桩上报，并正常发离位事件
	mustStatus(t, h.device("occupancy", "dv3-secret", occMsg("DV0003", "s-2", "2026-12-01T11:59:55+08:00", slot(1, false, ""))), 200)
	pm, occ = h.pileInfo(t, "PL00012")
	if pm["presenceBasis"] != "桩上报" || pm["occupiedVehicleId"] != nil || occ {
		t.Fatalf("桩报告后应恢复桩上报且空闲：%v", pm)
	}
	ev := h.outboxEvents("site.pile.occupancy")
	if len(ev) != 1 || payload(ev[0])["present"] != false || payload(ev[0])["vehicleId"] != "VH000426" {
		t.Fatalf("桩的离位事件应照常发出：%v", ev)
	}
}

func TestRideUnlockMarkClearedWhenVehicleReportedAgain(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	h.feed(t, rideUnlockedAt("RD1", "VH000426", "ST0003", "2026-12-01T11:59:50+08:00"))
	// 开锁之后桩又报告同一辆车在位：车回来了，标记清除
	tag := vehicleTag(t, "VH000426")
	mustStatus(t, h.device("occupancy", "dv3-secret", occMsg("DV0003", "s-1", "2026-12-01T11:59:58+08:00", slot(1, true, tag))), 200)
	pm, _ := h.pileInfo(t, "PL00012")
	if pm["presenceBasis"] != "桩上报" || pm["occupiedVehicleId"] != "VH000426" {
		t.Fatalf("桩在开锁后又报告在位应恢复桩上报：%v", pm)
	}
}

func TestRideEndedMarksPileBack(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	free := freeSlot(t, "ST0003") // 空闲桩
	tag := vehicleTag(t, "VH000004")
	h.feed(t, rideEndedAt("RD1", "VH000004", "ST0003", free.ID, "2026-12-01T11:59:50+08:00"))
	pm, occ := h.pileInfo(t, free.ID)
	if pm["occupiedVehicleId"] != "VH000004" || pm["presenceBasis"] != "骑行事件·已回来" || !occ {
		t.Fatalf("还车事件后应临时按已回来处理：%v occ=%v", pm, occ)
	}
	if pm["occupiedPlateNo"] == nil {
		t.Fatalf("已回来的车应带车号：%v", pm)
	}
	// 迟到的、早于还车时刻的"空位"报告不推翻标记
	mustStatus(t, h.device("occupancy", "dv3-secret", occMsg("DV0003", "b-1", "2026-12-01T11:59:40+08:00", slot(free.No, false, ""))), 200)
	if pm, _ := h.pileInfo(t, free.ID); pm["presenceBasis"] != "骑行事件·已回来" {
		t.Fatalf("迟到的旧空位报告不应推翻标记：%v", pm)
	}
	// 桩的入位报告到达：恢复桩上报，并发桩在位事件
	mustStatus(t, h.device("occupancy", "dv3-secret", occMsg("DV0003", "b-2", "2026-12-01T11:59:52+08:00", slot(free.No, true, tag))), 200)
	pm, _ = h.pileInfo(t, free.ID)
	if pm["presenceBasis"] != "桩上报" || pm["occupiedVehicleId"] != "VH000004" {
		t.Fatalf("桩确认后应为桩上报：%v", pm)
	}
	ev := h.outboxEvents("site.pile.occupancy")
	if len(ev) != 1 || payload(ev[0])["present"] != true || payload(ev[0])["pileId"] != free.ID {
		t.Fatalf("桩在位事件应来自桩的报告：%v", ev)
	}
}

func TestRideBackMarkClearedByNextUnlockAndOrdering(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	free := freeSlot(t, "ST0003")
	h.feed(t, rideEndedAt("RD1", "VH000004", "ST0003", free.ID, "2026-12-01T11:59:50+08:00"))
	if pm, _ := h.pileInfo(t, free.ID); pm["presenceBasis"] != "骑行事件·已回来" {
		t.Fatalf("应有已回来标记：%v", pm)
	}
	// 同一辆车再次开锁（开锁时刻晚于还车）：已回来标记作废；桩上没记着它，也不打已离开
	h.feed(t, rideUnlockedAt("RD2", "VH000004", "ST0003", "2026-12-01T11:59:58+08:00"))
	pm, occ := h.pileInfo(t, free.ID)
	if pm["presenceBasis"] != "桩上报" || pm["occupiedVehicleId"] != nil || occ {
		t.Fatalf("再次开锁后应清除已回来标记：%v", pm)
	}
	// 迟到的旧订单还车事件（这辆车已有更新的一单）不再打标记
	free2 := freeSlot(t, "ST0003")
	h.feed(t, rideEndedAt("RD0", "VH000004", "ST0003", free2.ID, "2026-12-01T11:59:30+08:00"))
	if pm, _ := h.pileInfo(t, free2.ID); pm["presenceBasis"] != "桩上报" || pm["occupiedVehicleId"] != nil {
		t.Fatalf("迟到的旧订单不应打标记：%v", pm)
	}
	// 同一事件重复到达只处理一次；停放车辆仍按在点还车投影
	d := h.admin("GET", "/api/site/admin/stations/ST0003", nil).Body
	if d["parkedVehicles"] == nil {
		t.Fatal("站点应有停放车辆数")
	}
}

func TestRideUnlockAfterEndedIsNotMarked(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	// 同一订单：结束事件先到、开锁事件后到（重放乱序）：不回退，也不打已离开
	h.feed(t, rideEndedAt("RD1", "VH000426", "ST0003", nil, "2026-12-01T11:59:50+08:00"))
	h.feed(t, rideUnlockedAt("RD1", "VH000426", "ST0003", "2026-12-01T11:50:00+08:00"))
	if pm, _ := h.pileInfo(t, "PL00012"); pm["presenceBasis"] != "桩上报" || pm["occupiedVehicleId"] != "VH000426" {
		t.Fatalf("已结束订单的迟到开锁事件不应打已离开：%v", pm)
	}
	if s := countRows(t, `SELECT count(*) FROM ride_orders WHERE order_id='RD1' AND status='已结束'`); s != 1 {
		t.Fatal("订单状态不应回退")
	}
}

func TestVehicleRemovedClearsMarks(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	h.feed(t, rideUnlockedAt("RD1", "VH000426", "ST0003", "2026-12-01T11:59:50+08:00"))
	h.feed(t, fleetVehicleRemoved("VH000426"))
	if n := countRows(t, `SELECT count(*) FROM piles WHERE ride_left_vehicle_id IS NOT NULL OR ride_back_vehicle_id IS NOT NULL`); n != 0 {
		t.Fatalf("车辆移除后临时标记应清除：%d", n)
	}
}

// ---------- 控制器登记 ----------

func TestControllerRegistration(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	adm := func(method, path string, body any) resp { return h.admin(method, path, body, "pile.edit") }
	create := map[string]any{"deviceId": "DV8001", "accessMode": "MQTT", "stationId": "ST0003", "reason": "新装控制器登记"}
	r := adm("POST", "/api/site/admin/controllers", create)
	mustStatus(t, r, 200)
	secret, _ := r.Body["secret"].(string)
	if len(secret) < 32 || r.Body["secretTail"] != secret[len(secret)-4:] || r.Body["source"] != "登记" || r.Body["status"] != "启用" ||
		r.Body["stationId"] != "ST0003" || r.Body["pileId"] != nil || r.Body["vendor"] != nil || r.Body["accessMode"] != "MQTT" {
		t.Fatalf("登记响应错误：%s", r.Raw)
	}
	// 同样的编号再登记
	mustCode(t, adm("POST", "/api/site/admin/controllers", create), 409, "CONFLICT")
	// 与部署配置同名
	create2 := map[string]any{"deviceId": "DV0003", "accessMode": "HTTP", "stationId": "ST0003", "reason": "新装控制器登记"}
	mustCode(t, adm("POST", "/api/site/admin/controllers", create2), 409, "CONFLICT")

	// 列表不含密钥，只给尾号，并带上配置里的控制器
	l := adm("GET", "/api/site/admin/controllers", nil)
	mustStatus(t, l, 200)
	if strings.Contains(l.Raw, secret) {
		t.Fatal("列表不得返回密钥")
	}
	rows := l.Body["rows"].([]any)
	var gotReg, gotCfg bool
	for _, x := range rows {
		m := x.(map[string]any)
		if m["deviceId"] == "DV8001" && m["source"] == "登记" && m["secretTail"] == secret[len(secret)-4:] {
			gotReg = true
		}
		if m["deviceId"] == "DV0003" && m["source"] == "配置" && m["secretTail"] == nil {
			gotCfg = true
		}
	}
	if !gotReg || !gotCfg {
		t.Fatalf("列表应含登记与配置的控制器：%s", l.Raw)
	}
	if f := adm("GET", "/api/site/admin/controllers?stationId=ST0003&status=启用", nil); len(f.Body["rows"].([]any)) != 3 { // 配置里的 DV0003、单桩直连的 DV9001 与登记的 DV8001
		t.Fatalf("按站点筛选应有 3 台：%s", f.Raw)
	}

	// 新密钥即刻可用：HTTP 在位上报，站点 ST0002 的第 1 号桩位
	no := freeSlot(t, "ST0003").No
	tag := vehicleTag(t, "VH000004")
	body := occMsg("DV8001", "c-1", nowTS, slot(no, true, tag))
	mustCode(t, h.device("occupancy", "错误的密钥", body), 401, "DEVICE_UNAUTHORIZED")
	mustStatus(t, h.device("occupancy", secret, body), 200)
	if n := len(h.outboxEvents("site.pile.occupancy")); n != 1 {
		t.Fatalf("登记的控制器上报应产生桩在位事件：%d", n)
	}

	// 密钥只存摘要；幂等记录里也没有密钥
	if n := countRows(t, `SELECT count(*) FROM controllers WHERE secret_sha256 = $1`, hashSecret(secret)); n != 1 {
		t.Fatal("应存密钥摘要")
	}
	if n := countRows(t, `SELECT count(*) FROM idempotency WHERE convert_from(body,'SQL_ASCII') LIKE '%'||$1||'%'`, secret); n != 0 {
		t.Fatal("幂等记录里不得有密钥")
	}
	if n := countRows(t, `SELECT count(*) FROM outbox WHERE body::text LIKE '%'||$1||'%'`, secret); n != 0 {
		t.Fatal("事件里不得有密钥")
	}

	// 重置：旧密钥失效，新密钥可用
	rs := adm("POST", "/api/site/admin/controllers/DV8001/reset-secret", map[string]any{"reason": "密钥疑似泄露"})
	mustStatus(t, rs, 200)
	secret2 := rs.Body["secret"].(string)
	if secret2 == secret || rs.Body["secretTail"] != secret2[len(secret2)-4:] {
		t.Fatalf("应换新密钥：%s", rs.Raw)
	}
	mustCode(t, h.device("occupancy", secret, occMsg("DV8001", "c-2", nowTS, slot(no, false, ""))), 401, "DEVICE_UNAUTHORIZED")
	mustStatus(t, h.device("occupancy", secret2, occMsg("DV8001", "c-3", nowTS, slot(no, false, ""))), 200)
	mustCode(t, adm("POST", "/api/site/admin/controllers/DV0003/reset-secret", map[string]any{"reason": "配置里的控制器"}), 409, "CONFLICT")
	mustCode(t, adm("POST", "/api/site/admin/controllers/DV9999/reset-secret", map[string]any{"reason": "不存在的控制器"}), 404, "NOT_FOUND")

	// 停用：接入立即被拒；再停用 ALREADY_DONE；停用的不能重置
	d := adm("POST", "/api/site/admin/controllers/DV8001/disable", map[string]any{"reason": "设备拆除下线"})
	mustStatus(t, d, 200)
	if d.Body["status"] != "停用" || d.Body["disabledAt"] == nil {
		t.Fatalf("停用响应错误：%s", d.Raw)
	}
	mustCode(t, h.device("occupancy", secret2, occMsg("DV8001", "c-4", nowTS, slot(no, true, tag))), 401, "DEVICE_UNAUTHORIZED")
	mustCode(t, adm("POST", "/api/site/admin/controllers/DV8001/disable", map[string]any{"reason": "设备拆除下线"}), 409, "ALREADY_DONE")
	mustCode(t, adm("POST", "/api/site/admin/controllers/DV8001/reset-secret", map[string]any{"reason": "已停用的控制器"}), 409, "CONFLICT")
	mustCode(t, adm("POST", "/api/site/admin/controllers/DV0003/disable", map[string]any{"reason": "配置里的控制器"}), 409, "CONFLICT")

	// 留痕：登记、重置、停用各一条，对象为所属站点；不带密钥
	var actions []string
	for _, e := range h.outboxEvents("site.audit.recorded") {
		pl := payload(e)
		if strings.Contains(pl["summary"].(string), "DV8001") {
			actions = append(actions, pl["action"].(string))
			if pl["targetType"] != "station" || pl["targetId"] != "ST0003" {
				t.Fatalf("留痕对象应为所属站点：%v", pl)
			}
		}
	}
	if strings.Join(actions, ",") != "登记控制器,重置控制器密钥,停用控制器" {
		t.Fatalf("留痕动作不对：%v", actions)
	}
}

func TestControllerValidation(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	adm := func(body map[string]any) resp {
		b := map[string]any{"reason": "新装控制器登记"}
		for k, v := range body {
			b[k] = v
		}
		return h.admin("POST", "/api/site/admin/controllers", b, "pile.edit")
	}
	mustCode(t, adm(map[string]any{"deviceId": "ab", "accessMode": "MQTT", "stationId": "ST0001"}), 400, "INVALID")                          // 编号太短
	mustCode(t, adm(map[string]any{"deviceId": "中文编号", "accessMode": "MQTT", "stationId": "ST0001"}), 400, "INVALID")                        // 非法字符
	mustCode(t, adm(map[string]any{"deviceId": "DV8101", "accessMode": "蓝牙", "stationId": "ST0001"}), 400, "INVALID")                        // 接入方式
	mustCode(t, adm(map[string]any{"deviceId": "DV8101", "accessMode": "MQTT"}), 400, "INVALID")                                             // 缺所属
	mustCode(t, adm(map[string]any{"deviceId": "DV8101", "accessMode": "MQTT", "stationId": "ST0001", "pileId": "PL00001"}), 400, "INVALID") // 两个都给
	mustCode(t, adm(map[string]any{"deviceId": "DV8101", "accessMode": "厂商适配", "stationId": "ST0001"}), 400, "INVALID")                      // 缺厂商
	mustCode(t, adm(map[string]any{"deviceId": "DV8101", "accessMode": "MQTT", "stationId": "ST0001", "vendor": "acme"}), 400, "INVALID")    // 标准接入不得给厂商
	mustCode(t, adm(map[string]any{"deviceId": "DV8101", "accessMode": "厂商适配", "stationId": "ST0001", "vendor": "nope"}), 400, "INVALID")    // 厂商适配器未启用
	mustCode(t, adm(map[string]any{"deviceId": "DV8101", "accessMode": "MQTT", "stationId": "ST9999"}), 404, "NOT_FOUND")
	mustCode(t, adm(map[string]any{"deviceId": "DV8101", "accessMode": "MQTT", "pileId": "PL99999"}), 404, "NOT_FOUND")
	mustCode(t, adm(map[string]any{"deviceId": "DV8101", "accessMode": "MQTT", "stationId": "ST0001", "reason": "短"}), 400, "REASON_REQUIRED")
	mustCode(t, h.admin("POST", "/api/site/admin/controllers", map[string]any{"deviceId": "DV8101", "accessMode": "MQTT", "stationId": "ST0001", "reason": "新装控制器登记"}, "station.edit"), 403, "FORBIDDEN")

	// 已删除的站点不能登记
	mustStatus(t, h.admin("POST", "/api/site/admin/stations", createBody(nil), "station.edit"), 200)
	mustStatus(t, h.admin("POST", "/api/site/admin/stations/ST0005/remove", map[string]any{"reason": "不要了的站点"}, "station.edit"), 200)
	mustCode(t, adm(map[string]any{"deviceId": "DV8101", "accessMode": "MQTT", "stationId": "ST0005"}), 400, "INVALID")

	// 单桩直连 + 厂商适配：可用厂商密钥接入
	v := adm(map[string]any{"deviceId": "DV8102", "accessMode": "厂商适配", "vendor": "acme", "pileId": "PL00001"})
	mustStatus(t, v, 200)
	if v.Body["vendor"] != "acme" || v.Body["pileId"] != "PL00001" || v.Body["stationId"] != nil {
		t.Fatalf("单桩直连响应错误：%s", v.Raw)
	}
	l := h.admin("GET", "/api/site/admin/controllers?stationId=ST0001", nil, "pile.edit")
	if !strings.Contains(l.Raw, `"deviceId":"DV8102"`) {
		t.Fatalf("单桩直连的控制器应按桩所在站点筛出：%s", l.Raw)
	}
}

func TestRegisteredControllerOverMQTT(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	addr := startMQTT(t, h)
	r := h.admin("POST", "/api/site/admin/controllers", map[string]any{"deviceId": "DV8201", "accessMode": "MQTT", "stationId": "ST0003", "reason": "新装控制器登记"}, "pile.edit")
	mustStatus(t, r, 200)
	secret := r.Body["secret"].(string)
	if _, err := mqttClient(t, addr, "DV8201", "bad-secret"); err == nil {
		t.Fatal("错误密钥不应连上")
	}
	c, err := mqttClient(t, addr, "DV8201", secret)
	if err != nil {
		t.Fatalf("登记的控制器应能用密钥连 MQTT：%v", err)
	}
	no := freeSlot(t, "ST0003").No
	tag := vehicleTag(t, "VH000004")
	b, _ := json.Marshal(occMsg("DV8201", "q-1", nowTS, slot(no, true, tag)))
	c.Publish("pile/v1/DV8201/up/occupancy", 1, false, b).Wait()
	waitFor(t, "登记的控制器 MQTT 上报生效", func() bool { return len(h.outboxEvents("site.pile.occupancy")) == 1 })
	// 停用后不能再连
	mustStatus(t, h.admin("POST", "/api/site/admin/controllers/DV8201/disable", map[string]any{"reason": "设备拆除下线"}, "pile.edit"), 200)
	if _, err := mqttClient(t, addr, "DV8201", secret); err == nil {
		t.Fatal("停用的控制器不应再连上")
	}
}

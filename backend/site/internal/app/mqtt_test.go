package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mochi-mqtt/server/v2/packets"
)

func startMQTT(t *testing.T, h *harness) string {
	t.Helper()
	if err := h.app.StartMQTT(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.app.StopMQTT)
	addr := h.app.MQTTAddr()
	if host, _, _ := net.SplitHostPort(addr); host != "127.0.0.1" {
		t.Fatalf("MQTT 只能监听回环地址：%s", addr)
	}
	return addr
}

func mqttClient(t *testing.T, addr, user, pass string) (paho.Client, error) {
	t.Helper()
	opts := paho.NewClientOptions().AddBroker("tcp://" + addr).SetClientID(user + "-" + fmt.Sprint(time.Now().UnixNano())).
		SetUsername(user).SetPassword(pass).SetAutoReconnect(false).SetConnectTimeout(3 * time.Second)
	c := paho.NewClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(5 * time.Second) {
		return nil, fmt.Errorf("连接超时")
	}
	if tok.Error() != nil {
		return nil, tok.Error()
	}
	t.Cleanup(func() { c.Disconnect(50) })
	return c, nil
}

// waitFor 轮询直到条件成立
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func TestMQTTOccupancyAndSnapshotQuery(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	addr := startMQTT(t, h)
	c, err := mqttClient(t, addr, "DV0003", "dv3-secret")
	if err != nil {
		t.Fatal(err)
	}
	// 连上后平台下发 down/query 要求全量
	got := make(chan string, 4)
	st := c.Subscribe("pile/v1/DV0003/down/#", 1, func(_ paho.Client, m paho.Message) { got <- m.Topic() })
	st.Wait()
	if code := st.(*paho.SubscribeToken).Result()["pile/v1/DV0003/down/#"]; code != 1 {
		t.Fatalf("订阅自己的下行主题应获准：%d", code)
	}
	select {
	case topic := <-got:
		if topic != "pile/v1/DV0003/down/query" {
			t.Fatalf("下行主题错误：%s", topic)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("连接后应收到全量查询")
	}

	p := freeSlot(t, "ST0003")
	b, _ := json.Marshal(occMsg("DV0003", "mq-1", nowTS, slot(p.No, true, tagVH4)))
	c.Publish("pile/v1/DV0003/up/occupancy", 1, false, b).Wait()
	waitFor(t, "MQTT 在位事件", func() bool { return len(h.outboxEvents("site.pile.occupancy")) == 1 })
	if payload(h.outboxEvents("site.pile.occupancy")[0])["vehicleId"] != "VH000004" {
		t.Fatal("MQTT 在位应识别出 VH000004")
	}
	// 心跳 QoS 0
	hb, _ := json.Marshal(map[string]any{"msgId": "mq-2", "deviceId": "DV0003", "ts": nowTS, "slots": []any{map[string]any{"slotNo": p.No, "status": "故障"}}})
	c.Publish("pile/v1/DV0003/up/heartbeat", 0, false, hb).Wait()
	waitFor(t, "MQTT 心跳", func() bool { return pileState(t, p.ID).Status == "故障" })
}

func TestMQTTAuthAndACL(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	addr := startMQTT(t, h)
	if _, err := mqttClient(t, addr, "DV0003", "wrong"); err == nil {
		t.Fatal("密码错误应拒绝连接")
	}
	if _, err := mqttClient(t, addr, "DV7777", "x"); err == nil {
		t.Fatal("未登记控制器应拒绝连接")
	}
	c, err := mqttClient(t, addr, "DV0003", "dv3-secret")
	if err != nil {
		t.Fatal(err)
	}
	// 订阅别人的主题、发布到自己的下行主题：拒绝
	st := c.Subscribe("pile/v1/DV0001/down/#", 1, nil)
	st.Wait()
	if code := st.(*paho.SubscribeToken).Result()["pile/v1/DV0001/down/#"]; code != 0x80 {
		t.Fatalf("订阅别人的主题应被拒绝：%d", code)
	}
	st = c.Subscribe("pile/v1/+/up/#", 1, nil)
	st.Wait()
	if code := st.(*paho.SubscribeToken).Result()["pile/v1/+/up/#"]; code != 0x80 {
		t.Fatalf("通配订阅应被拒绝：%d", code)
	}
	// 冒充别的控制器发布：被丢弃（服务端可能因此断开该连接）
	b, _ := json.Marshal(occMsg("DV0001", "acl-1", nowTS, slot(1, false, "")))
	c.Publish("pile/v1/DV0001/up/occupancy", 1, false, b).WaitTimeout(time.Second)
	// 自己主题下报文 deviceId 填别人：也丢弃
	c.Publish("pile/v1/DV0003/up/occupancy", 1, false, b).WaitTimeout(time.Second)
	time.Sleep(300 * time.Millisecond)
	if len(h.outboxEvents("site.pile.occupancy")) != 0 || pileState(t, "PL00001").VehicleID == nil {
		t.Fatal("越权发布不应生效")
	}
}

// rawConnect 用原始套接字连接并登记遗嘱，返回连接（关闭即异常断线，触发遗嘱）
func rawConnect(t *testing.T, addr, user, pass, willTopic string) (net.Conn, byte) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	pk := packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Connect}, ProtocolVersion: 4, Connect: packets.ConnectParams{
		ProtocolName: []byte("MQTT"), Clean: true, Keepalive: 30, ClientIdentifier: "raw-" + user,
		UsernameFlag: true, Username: []byte(user), PasswordFlag: true, Password: []byte(pass),
		WillFlag: true, WillTopic: willTopic, WillPayload: []byte(`{}`), WillQos: 1,
	}}
	var buf bytes.Buffer
	if err := pk.ConnectEncode(&buf); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	ack := make([]byte, 4)
	if _, err := io.ReadFull(conn, ack); err != nil {
		return conn, 0xff
	}
	return conn, ack[3]
}

func TestMQTTWillMarksOffline(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	addr := startMQTT(t, h)
	// 遗嘱主题不是自己的：拒绝连接
	conn, code := rawConnect(t, addr, "DV0003", "dv3-secret", "pile/v1/DV0001/up/offline")
	conn.Close()
	if code == 0 {
		t.Fatal("遗嘱主题越权应拒绝连接")
	}
	conn, code = rawConnect(t, addr, "DV0003", "dv3-secret", "pile/v1/DV0003/up/offline")
	if code != 0 {
		t.Fatalf("合法连接被拒：%d", code)
	}
	conn.Close() // 异常断线
	waitFor(t, "遗嘱判离线", func() bool {
		return countRows(t, `SELECT count(*) FROM piles WHERE station_id='ST0003' AND status='离线' AND fault_note='控制器断线'`) > 0
	})
	if n := countRows(t, `SELECT count(*) FROM piles WHERE station_id='ST0003' AND status='在线'`); n != 0 {
		t.Fatalf("控制器断线后其下在线桩应全部离线：%d", n)
	}
	if n := countRows(t, `SELECT count(*) FROM piles WHERE station_id='ST0001' AND status='离线'`); n != 0 {
		t.Fatal("别的站点不受影响")
	}
}

func TestMQTTVendorAdapter(t *testing.T) {
	h := newHarness(t)
	seedProto(t)
	addr := startMQTT(t, h)
	c, err := mqttClient(t, addr, "DV0101", "acme-vendor-key") // 厂商密钥
	if err != nil {
		t.Fatal(err)
	}
	var bay int
	_ = testPool.QueryRow(t.Context(), `SELECT no FROM piles WHERE station_id='ST0004' AND occupied ORDER BY no LIMIT 1`).Scan(&bay)
	msg := fmt.Sprintf(`{"ctrl":"DV0101","seq":11,"time":%d,"type":"leave","bays":[{"bay":%d}]}`, protoNow.UnixMilli(), bay)
	c.Publish("vendor/acme/DV0101/evt", 1, false, []byte(msg)).Wait()
	waitFor(t, "厂商 MQTT 转译", func() bool { return len(h.outboxEvents("site.pile.occupancy")) == 1 })
	// 厂商设备不能用标准主题
	b, _ := json.Marshal(occMsg("DV0101", "v-2", nowTS, slot(bay, true, tagVH4)))
	c.Publish("pile/v1/DV0101/up/occupancy", 1, false, b).WaitTimeout(time.Second)
	time.Sleep(300 * time.Millisecond)
	if len(h.outboxEvents("site.pile.occupancy")) != 1 {
		t.Fatal("厂商设备发标准主题应被拒绝")
	}
}

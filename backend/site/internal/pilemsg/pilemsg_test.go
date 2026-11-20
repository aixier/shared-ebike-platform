package pilemsg

import (
	"testing"
	"time"
)

func TestParseOccupancyRFC3339(t *testing.T) {
	m, err := Parse(KindOccupancy, []byte(`{"msgId":"m1","deviceId":"DV0001","ts":"2026-12-01T12:00:00+08:00","slots":[{"slotNo":1,"present":true,"tag":"E28011C79479F55A38EC7779","tagType":"RFID"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindOccupancy || m.DeviceID != "DV0001" || m.MsgID != "m1" || len(m.Slots) != 1 {
		t.Fatalf("解析错误: %+v", m)
	}
	if !m.Slots[0].Present || m.Slots[0].Tag == "" || m.Slots[0].TagType != "RFID" {
		t.Fatalf("桩位解析错误: %+v", m.Slots[0])
	}
	want := time.Date(2026, 12, 1, 4, 0, 0, 0, time.UTC)
	if !m.TS.Equal(want) {
		t.Fatalf("时刻错误: %v", m.TS)
	}
}

func TestParseMillis(t *testing.T) {
	m, err := Parse(KindHeartbeat, []byte(`{"msgId":"h1","deviceId":"DV0001","ts":1795996800000,"slots":[{"slotNo":1,"status":"正常"}],"fw":"1.2","rssi":-70}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.TS.UnixMilli() != 1795996800000 || m.Slots[0].Status != "正常" {
		t.Fatalf("解析错误: %+v", m)
	}
}

func TestTagTypeDefaultAndInvalid(t *testing.T) {
	m, err := Parse(KindSnapshot, []byte(`{"msgId":"s1","deviceId":"D","ts":"2026-12-01T12:00:00+08:00","slots":[{"slotNo":1,"present":true,"tag":"X"},{"slotNo":2,"present":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Slots[0].TagType != "RFID" {
		t.Fatalf("缺省识别方式应为 RFID: %q", m.Slots[0].TagType)
	}
	bad := []string{
		`{"deviceId":"D","ts":"2026-12-01T12:00:00+08:00","slots":[]}`,                                                                 // 缺 msgId
		`{"msgId":"a","ts":"2026-12-01T12:00:00+08:00","slots":[]}`,                                                                    // 缺 deviceId
		`{"msgId":"a","deviceId":"D","slots":[]}`,                                                                                      // 缺 ts
		`{"msgId":"a","deviceId":"D","ts":"昨天","slots":[]}`,                                                                            // ts 格式错
		`{"msgId":"a","deviceId":"D","ts":"2026-12-01T12:00:00+08:00","slots":[{"slotNo":0,"present":false}]}`,                         // 桩位号非法
		`{"msgId":"a","deviceId":"D","ts":"2026-12-01T12:00:00+08:00","slots":[{"slotNo":1,"present":true,"tagType":"RFID"}]}`,         // 在位无标识
		`{"msgId":"a","deviceId":"D","ts":"2026-12-01T12:00:00+08:00","slots":[{"slotNo":1,"present":true,"tag":"X","tagType":"指纹"}]}`, // 识别方式非法
		`not json`,
	}
	for i, b := range bad {
		if _, err := Parse(KindOccupancy, []byte(b)); err == nil {
			t.Errorf("第 %d 个非法报文未被拒绝", i)
		}
	}
	if _, err := Parse(KindHeartbeat, []byte(`{"msgId":"a","deviceId":"D","ts":"2026-12-01T12:00:00+08:00","slots":[{"slotNo":1,"status":"坏了"}]}`)); err == nil {
		t.Error("心跳状态非法应拒绝")
	}
}

func TestKindFromTopic(t *testing.T) {
	cases := map[string][2]string{
		"pile/v1/DV0001/up/occupancy": {"DV0001", "occupancy"},
		"pile/v1/DV0001/up/heartbeat": {"DV0001", "heartbeat"},
		"pile/v1/DV0001/up/snapshot":  {"DV0001", "snapshot"},
		"pile/v1/DV0001/up/offline":   {"DV0001", "offline"},
	}
	for topic, want := range cases {
		dev, kind, ok := ParseTopic(topic)
		if !ok || dev != want[0] || string(kind) != want[1] {
			t.Errorf("%s 解析为 %s %s %v", topic, dev, kind, ok)
		}
	}
	for _, topic := range []string{"pile/v1/DV0001/down/query", "pile/v2/D/up/occupancy", "pile/v1/D/up/xxx", "vendor/acme/x"} {
		if _, _, ok := ParseTopic(topic); ok {
			t.Errorf("%s 不应识别为上行主题", topic)
		}
	}
}

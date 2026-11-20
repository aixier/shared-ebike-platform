package acme

import (
	"testing"

	"bike/site/internal/pilemsg"
)

func TestPark(t *testing.T) {
	a := New()
	ms, err := a.TranslateMQTT("vendor/acme/DV0101/evt", []byte(`{"ctrl":"DV0101","seq":7,"time":1795996800000,"type":"park","bays":[{"bay":2,"card":"E28011C79479F55A38EC7779"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("应译出 1 条: %d", len(ms))
	}
	m := ms[0]
	if m.Kind != pilemsg.KindOccupancy || m.DeviceID != "DV0101" || m.MsgID != "acme-7" {
		t.Fatalf("译文错误: %+v", m)
	}
	if len(m.Slots) != 1 || m.Slots[0].SlotNo != 2 || !m.Slots[0].Present || m.Slots[0].TagType != "RFID" {
		t.Fatalf("桩位错误: %+v", m.Slots)
	}
}

func TestLeaveBeatAll(t *testing.T) {
	a := New()
	ms, _ := a.TranslateHTTP([]byte(`{"ctrl":"DV0101","seq":8,"time":1795996800000,"type":"leave","bays":[{"bay":2}]}`))
	if ms[0].Kind != pilemsg.KindOccupancy || ms[0].Slots[0].Present {
		t.Fatalf("离位译文错误: %+v", ms[0])
	}
	ms, _ = a.TranslateHTTP([]byte(`{"ctrl":"DV0101","seq":9,"time":1795996800000,"type":"beat","bays":[{"bay":1,"st":0},{"bay":2,"st":1}]}`))
	if ms[0].Kind != pilemsg.KindHeartbeat || ms[0].Slots[0].Status != "正常" || ms[0].Slots[1].Status != "故障" {
		t.Fatalf("心跳译文错误: %+v", ms[0])
	}
	ms, _ = a.TranslateHTTP([]byte(`{"ctrl":"DV0101","seq":10,"time":1795996800000,"type":"all","bays":[{"bay":1,"card":"AA"},{"bay":2}]}`))
	if ms[0].Kind != pilemsg.KindSnapshot || !ms[0].Slots[0].Present || ms[0].Slots[1].Present {
		t.Fatalf("全量译文错误: %+v", ms[0])
	}
}

func TestReject(t *testing.T) {
	a := New()
	if _, err := a.TranslateMQTT("vendor/acme/DV0102/evt", []byte(`{"ctrl":"DV0101","seq":1,"time":1,"type":"park","bays":[]}`)); err == nil {
		t.Error("主题与报文控制器不一致应拒绝")
	}
	if _, err := a.TranslateHTTP([]byte(`{"ctrl":"DV0101","seq":1,"time":1,"type":"dance","bays":[]}`)); err == nil {
		t.Error("未知类型应拒绝")
	}
	if _, err := a.TranslateHTTP([]byte(`{`)); err == nil {
		t.Error("坏报文应拒绝")
	}
}

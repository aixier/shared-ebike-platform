// Package acme 示例适配器：虚构厂商"acme"的私有报文转标准桩消息。
//
// 厂商报文（MQTT 主题 vendor/acme/{ctrl}/evt，或 HTTP 回调 POST /device/v1/vendor/acme）：
//
//	{"ctrl":"DV0101","seq":7,"time":1795996800000,"type":"park","bays":[{"bay":2,"card":"E280...","st":0}]}
//
// type：park 入位、leave 离位、beat 心跳（st 0 正常 1 故障）、all 全量（card 非空即在位）。
// 该厂商只用 RFID 识别。
package acme

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"bike/site/internal/pilemsg"
)

// Vendor 厂商名，即 MQTT 命名空间 vendor/acme/# 与配置里的适配器名
const Vendor = "acme"

type bay struct {
	Bay  int    `json:"bay"`
	Card string `json:"card"`
	St   int    `json:"st"`
}

type report struct {
	Ctrl string `json:"ctrl"`
	Seq  int64  `json:"seq"`
	Time int64  `json:"time"`
	Type string `json:"type"`
	Bays []bay  `json:"bays"`
}

// Adapter acme 适配器
type Adapter struct{}

// New 新建
func New() *Adapter { return &Adapter{} }

// Vendor 厂商名
func (*Adapter) Vendor() string { return Vendor }

// TranslateMQTT 主题形如 vendor/acme/{ctrl}/evt
func (a *Adapter) TranslateMQTT(topic string, payload []byte) ([]pilemsg.Message, error) {
	p := strings.Split(topic, "/")
	if len(p) != 4 || p[0] != "vendor" || p[1] != Vendor || p[3] != "evt" {
		return nil, fmt.Errorf("acme 不认识的主题 %s", topic)
	}
	ms, err := a.TranslateHTTP(payload)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if m.DeviceID != p[2] {
			return nil, fmt.Errorf("主题里的控制器 %s 与报文里的 %s 不一致", p[2], m.DeviceID)
		}
	}
	return ms, nil
}

// TranslateHTTP 译厂商报文
func (*Adapter) TranslateHTTP(body []byte) ([]pilemsg.Message, error) {
	var r report
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("acme 报文不是合法 JSON: %w", err)
	}
	if r.Ctrl == "" {
		return nil, fmt.Errorf("acme 报文缺 ctrl")
	}
	m := pilemsg.Message{MsgID: fmt.Sprintf("acme-%d", r.Seq), DeviceID: r.Ctrl, TS: time.UnixMilli(r.Time)}
	switch r.Type {
	case "park", "leave", "all":
		m.Kind = pilemsg.KindOccupancy
		if r.Type == "all" {
			m.Kind = pilemsg.KindSnapshot
		}
		for _, b := range r.Bays {
			present := b.Card != ""
			if r.Type == "leave" {
				present = false
			}
			m.Slots = append(m.Slots, pilemsg.Slot{SlotNo: b.Bay, Present: present, Tag: b.Card, TagType: "RFID"})
		}
	case "beat":
		m.Kind = pilemsg.KindHeartbeat
		for _, b := range r.Bays {
			st := "正常"
			if b.St != 0 {
				st = "故障"
			}
			m.Slots = append(m.Slots, pilemsg.Slot{SlotNo: b.Bay, Status: st})
		}
	default:
		return nil, fmt.Errorf("acme 报文类型 %q 不认识", r.Type)
	}
	if err := pilemsg.Validate(&m); err != nil {
		return nil, err
	}
	return []pilemsg.Message{m}, nil
}

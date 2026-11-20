// Package pilemsg 标准桩消息（桩接入规范第 2、3 节）：结构、解析与格式校验。
// 只做格式，不做业务判断；MQTT、HTTP 与厂商适配器都产出这里的 Message。
package pilemsg

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind 消息类别
type Kind string

const (
	KindOccupancy Kind = "occupancy"
	KindHeartbeat Kind = "heartbeat"
	KindSnapshot  Kind = "snapshot"
	KindOffline   Kind = "offline" // 遗嘱消息
)

// 识别方式（同 fleet 车辆清单的 pileTagType）
var TagTypes = map[string]bool{"RFID": true, "蓝牙": true, "视觉车牌": true, "其他": true}

// Slot 一个桩位的状态
type Slot struct {
	SlotNo  int    `json:"slotNo"`
	Present bool   `json:"present"`
	Tag     string `json:"tag,omitempty"`
	TagType string `json:"tagType,omitempty"`
	Status  string `json:"status,omitempty"` // 心跳：正常｜故障
	Note    string `json:"note,omitempty"`   // 心跳故障说明（扩展字段，可空）
}

// Message 标准桩消息
type Message struct {
	Kind     Kind
	MsgID    string
	DeviceID string
	TS       time.Time
	Slots    []Slot
	FW       string
	RSSI     *int
}

type wire struct {
	MsgID    string          `json:"msgId"`
	DeviceID string          `json:"deviceId"`
	TS       json.RawMessage `json:"ts"`
	Slots    []Slot          `json:"slots"`
	FW       string          `json:"fw"`
	RSSI     *int            `json:"rssi"`
}

// ParseTS 解析设备时刻：RFC 3339 字符串或毫秒时间戳数字
func ParseTS(raw json.RawMessage) (time.Time, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return time.Time{}, errors.New("缺少 ts")
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return time.Time{}, err
		}
		t, err := time.Parse(time.RFC3339Nano, str)
		if err != nil {
			return time.Time{}, fmt.Errorf("ts 不是 RFC 3339: %w", err)
		}
		return t, nil
	}
	var ms int64
	if err := json.Unmarshal(raw, &ms); err != nil {
		return time.Time{}, fmt.Errorf("ts 既不是 RFC 3339 也不是毫秒时间戳")
	}
	return time.UnixMilli(ms), nil
}

// Parse 解析并校验一条标准桩消息
func Parse(kind Kind, body []byte) (Message, error) {
	var w wire
	if err := json.Unmarshal(body, &w); err != nil {
		return Message{}, fmt.Errorf("报文不是合法 JSON: %w", err)
	}
	m := Message{Kind: kind, MsgID: w.MsgID, DeviceID: w.DeviceID, Slots: w.Slots, FW: w.FW, RSSI: w.RSSI}
	if m.MsgID == "" {
		return m, errors.New("缺少 msgId")
	}
	if len(m.MsgID) > 64 {
		return m, errors.New("msgId 超过 64 个字符")
	}
	if m.DeviceID == "" {
		return m, errors.New("缺少 deviceId")
	}
	ts, err := ParseTS(w.TS)
	if err != nil {
		return m, err
	}
	m.TS = ts
	return m, Validate(&m)
}

// Validate 校验桩位字段并补缺省识别方式（RFID）
func Validate(m *Message) error {
	for i := range m.Slots {
		s := &m.Slots[i]
		if s.SlotNo < 1 {
			return fmt.Errorf("第 %d 个桩位的 slotNo 非法", i+1)
		}
		switch m.Kind {
		case KindHeartbeat:
			if s.Status != "正常" && s.Status != "故障" {
				return fmt.Errorf("桩位 %d 的 status 只能是正常或故障", s.SlotNo)
			}
		case KindOccupancy, KindSnapshot:
			if s.Present && s.Tag == "" {
				return fmt.Errorf("桩位 %d 在位但没有车辆标识", s.SlotNo)
			}
			if s.TagType == "" {
				s.TagType = "RFID"
			}
			if !TagTypes[s.TagType] {
				return fmt.Errorf("桩位 %d 的识别方式 %q 不在 RFID、蓝牙、视觉车牌、其他之内", s.SlotNo, s.TagType)
			}
		}
	}
	return nil
}

// ParseTopic 解析上行主题 pile/v1/{deviceId}/up/{kind}
func ParseTopic(topic string) (deviceID string, kind Kind, ok bool) {
	p := strings.Split(topic, "/")
	if len(p) != 5 || p[0] != "pile" || p[1] != "v1" || p[3] != "up" || p[2] == "" {
		return "", "", false
	}
	switch Kind(p[4]) {
	case KindOccupancy, KindHeartbeat, KindSnapshot, KindOffline:
		return p[2], Kind(p[4]), true
	}
	return "", "", false
}

package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"

	"bike/site/internal/pilemsg"
)

// broker 内嵌的 MQTT 服务（桩接入规范第 3 节）
type broker struct {
	srv *mqtt.Server
	tcp *listeners.TCP
}

// StartMQTT 启动内嵌 MQTT 服务并订阅上行主题与已启用厂商的命名空间
func (a *App) StartMQTT() error {
	srv := mqtt.New(&mqtt.Options{InlineClient: true, Logger: a.log.With(slog.String("part", "mqtt"))})
	if err := srv.AddHook(&deviceHook{app: a}, nil); err != nil {
		return err
	}
	lc := listeners.Config{ID: "pile", Address: a.cfg.MQTTAddr}
	if a.cfg.MQTTTLSCert != "" {
		cert, err := tls.LoadX509KeyPair(a.cfg.MQTTTLSCert, a.cfg.MQTTTLSKey)
		if err != nil {
			return fmt.Errorf("MQTT 证书: %w", err)
		}
		lc.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	tcp := listeners.NewTCP(lc)
	if err := srv.AddListener(tcp); err != nil {
		return err
	}
	if err := srv.Subscribe("pile/v1/+/up/+", 1, a.onStandard); err != nil {
		return err
	}
	id := 2
	for vendor := range a.adapters {
		if err := srv.Subscribe("vendor/"+vendor+"/#", id, a.onVendor(vendor)); err != nil {
			return err
		}
		id++
	}
	go func() {
		if err := srv.Serve(); err != nil {
			a.log.Error("MQTT 服务异常", "err", err)
		}
	}()
	a.broker = &broker{srv: srv, tcp: tcp}
	return nil
}

// MQTTAddr 实际监听地址（测试用随机端口时取它）
func (a *App) MQTTAddr() string {
	if a.broker == nil {
		return ""
	}
	return a.broker.tcp.Address()
}

// StopMQTT 关闭
func (a *App) StopMQTT() {
	if a.broker != nil {
		_ = a.broker.srv.Close()
		a.broker = nil
	}
}

// RequestSnapshot 下发 pile/v1/{deviceId}/down/query，要求控制器上报全量
func (a *App) RequestSnapshot(deviceID string) error {
	if a.broker == nil {
		return fmt.Errorf("MQTT 未启用")
	}
	body, _ := json.Marshal(map[string]any{"ts": a.fmtTime(a.now())})
	return a.broker.srv.Publish("pile/v1/"+deviceID+"/down/query", body, false, 1)
}

func (a *App) mqttCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// onStandard 标准主题的上行消息
func (a *App) onStandard(cl *mqtt.Client, _ packets.Subscription, pk packets.Packet) {
	devID, kind, ok := pilemsg.ParseTopic(pk.TopicName)
	if !ok {
		return
	}
	var m pilemsg.Message
	if kind == pilemsg.KindOffline {
		m = pilemsg.Message{Kind: kind, DeviceID: devID}
	} else {
		var err error
		if m, err = pilemsg.Parse(kind, pk.Payload); err != nil {
			a.log.Warn("桩消息格式错误，已丢弃", "topic", pk.TopicName, "err", err)
			return
		}
		if m.DeviceID != devID {
			a.log.Warn("报文 deviceId 与主题不一致，已丢弃", "topic", pk.TopicName, "deviceId", m.DeviceID)
			return
		}
	}
	ctx, cancel := a.mqttCtx()
	defer cancel()
	if _, err := a.Ingest(ctx, m); err != nil {
		a.log.Error("桩消息处理失败", "topic", pk.TopicName, "err", err)
	}
}

// onVendor 厂商命名空间的消息，交给该厂商的适配器转译
func (a *App) onVendor(vendor string) mqtt.InlineSubFn {
	return func(cl *mqtt.Client, _ packets.Subscription, pk packets.Packet) {
		ms, err := a.adapters[vendor].TranslateMQTT(pk.TopicName, pk.Payload)
		if err != nil {
			a.log.Warn("厂商报文无法转译，已丢弃", "vendor", vendor, "topic", pk.TopicName, "err", err)
			return
		}
		ctx, cancel := a.mqttCtx()
		defer cancel()
		for _, m := range ms {
			if dev, ok := a.device(m.DeviceID); !ok || dev.Vendor != vendor {
				a.log.Warn("厂商报文里的控制器未登记在该厂商名下", "vendor", vendor, "deviceId", m.DeviceID)
				continue
			}
			if _, err := a.Ingest(ctx, m); err != nil {
				a.log.Error("桩消息处理失败", "vendor", vendor, "err", err)
			}
		}
	}
}

// deviceHook 设备鉴权与主题权限：用户名为 deviceId、密码为控制器密钥（厂商设备也可用厂商密钥）；
// 标准设备只能发布 pile/v1/{deviceId}/up/*、订阅 pile/v1/{deviceId}/down/*；
// 厂商设备只能在 vendor/{厂商}/{deviceId}/ 下收发。
type deviceHook struct {
	mqtt.HookBase
	app *App
}

func (h *deviceHook) ID() string { return "site-device-auth" }

func (h *deviceHook) Provides(b byte) bool {
	return bytes.Contains([]byte{mqtt.OnConnectAuthenticate, mqtt.OnACLCheck, mqtt.OnSessionEstablished}, []byte{b})
}

// prefix 设备可用的主题前缀
func (h *deviceHook) prefix(deviceID string) (string, bool) {
	dev, ok := h.app.device(deviceID)
	if !ok {
		return "", false
	}
	if dev.Vendor != "" {
		if _, enabled := h.app.adapters[dev.Vendor]; !enabled {
			return "", false
		}
		return "vendor/" + dev.Vendor + "/" + deviceID + "/", true
	}
	return "pile/v1/" + deviceID + "/", true
}

func (h *deviceHook) OnConnectAuthenticate(cl *mqtt.Client, pk packets.Packet) bool {
	user := string(pk.Connect.Username)
	dev, ok := h.app.device(user)
	if !ok || !h.app.deviceKeyOK(dev, string(pk.Connect.Password)) {
		h.app.log.Warn("MQTT 设备鉴权失败", "username", user, "remote", cl.Net.Remote)
		return false
	}
	pre, ok := h.prefix(user)
	if !ok {
		return false
	}
	// 遗嘱消息不经过发布权限检查，连接时就要求它落在自己的主题下
	if pk.Connect.WillFlag && !strings.HasPrefix(pk.Connect.WillTopic, pre) {
		h.app.log.Warn("遗嘱主题越权", "username", user, "willTopic", pk.Connect.WillTopic)
		return false
	}
	return true
}

func (h *deviceHook) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	if cl.Net.Inline {
		return true
	}
	user := string(cl.Properties.Username)
	pre, ok := h.prefix(user)
	if !ok || !strings.HasPrefix(topic, pre) {
		return false
	}
	if strings.HasPrefix(pre, "vendor/") {
		return true
	}
	rest := strings.TrimPrefix(topic, pre)
	if write {
		return strings.HasPrefix(rest, "up/")
	}
	return strings.HasPrefix(rest, "down/")
}

// OnSessionEstablished 控制器连上后要求它报一次全量（厂商设备由适配器自己的协议决定，不下发）
func (h *deviceHook) OnSessionEstablished(cl *mqtt.Client, _ packets.Packet) {
	user := string(cl.Properties.Username)
	if dev, ok := h.app.device(user); ok && dev.Vendor == "" && !cl.Net.Inline {
		go func() {
			time.Sleep(200 * time.Millisecond) // 等设备完成订阅
			_ = h.app.RequestSnapshot(user)
		}()
	}
}

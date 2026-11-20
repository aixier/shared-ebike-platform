// Package config 读环境变量（变量名见 contracts/wire.md 第 5 节与本服务 README）。
package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bike/site/internal/geo"
	_ "time/tzdata" // 时区数据打进可执行文件，不依赖部署机
)

// Region 本市范围
type Region struct {
	City        string       `json:"city"`
	Center      geo.Point    `json:"center"`
	Bounds      [2]geo.Point `json:"bounds"`
	CoordSystem string       `json:"coordSystem"`
}

// Device 一台站级控制器（或单桩直连设备）
type Device struct {
	DeviceID  string `json:"deviceId"`
	StationID string `json:"stationId"` // 站级控制器：slotNo 对应该站点的桩位号
	PileID    string `json:"pileId"`    // 单桩直连：只有 1 号桩位，对应该桩
	Vendor    string `json:"vendor"`    // 空为按标准消息接入；否则为厂商适配器名
	Secret    string `json:"secret"`    // MQTT 密码与 HTTP 设备密钥；厂商设备可用厂商密钥代替

	// 以下只用于管理端登记的控制器（不来自 PILE_DEVICES）
	SecretSHA256 string `json:"-"` // 密钥的 SHA-256 十六进制；登记的控制器只存摘要
	Registered   bool   `json:"-"`
}

// Config 本服务配置
type Config struct {
	ListenHost     string
	Port           int
	DBDSN          string
	NatsURL        string
	AdminKeys      map[string]ed25519.PublicKey
	ServiceKey     string
	AllowedCallers map[string]string
	TZ             *time.Location

	Region         Region
	Districts      []string
	FenceMinAreaM2 float64

	MQTTAddr    string
	MQTTTLSCert string
	MQTTTLSKey  string
	AccessMQTT  bool
	AccessHTTP  bool
	Adapters    []string
	Devices     map[string]Device
	VendorKeys  map[string]string

	OfflineAfter time.Duration
	ClockSkew    time.Duration
	OfflineScan  string // 离线扫描的 cron 表达式

	RetentionDays int // 事件流保留天数 EVENT_RETENTION_DAYS，默认 30
}

// ListenAddr 监听地址 LISTEN_HOST:PORT
func (c Config) ListenAddr() string { return fmt.Sprintf("%s:%d", c.ListenHost, c.Port) }

// 示范市的缺省区域与区（与原型 stations.json 一致）；正式部署由本市配置覆盖
var defaultRegion = Region{City: "示范市", Center: geo.Point{104.066, 30.596}, Bounds: [2]geo.Point{{104.03, 30.57}, {104.102, 30.622}}, CoordSystem: "GCJ-02"}

const defaultDistricts = "东城区,西城区,滨河区,高新区"

func or(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return strings.TrimSpace(v)
}

func list(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func kv(v string) (map[string]string, error) {
	out := map[string]string{}
	for _, s := range list(v) {
		k, val, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%q 不是 键=值", s)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(val)
	}
	return out, nil
}

func secs(get func(string) string, name string, def int) (time.Duration, error) {
	n, err := strconv.Atoi(or(get(name), strconv.Itoa(def)))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s 应为正整数秒", name)
	}
	return time.Duration(n) * time.Second, nil
}

// Load 从环境变量读配置
func Load(get func(string) string) (Config, error) {
	c := Config{
		ListenHost:  or(get("LISTEN_HOST"), "127.0.0.1"),
		DBDSN:       strings.TrimSpace(get("DB_DSN")),
		NatsURL:     or(get("NATS_URL"), "nats://127.0.0.1:4222"),
		ServiceKey:  get("SERVICE_KEY"),
		MQTTAddr:    or(get("MQTT_ADDR"), "127.0.0.1:1883"),
		MQTTTLSCert: get("MQTT_TLS_CERT"),
		MQTTTLSKey:  get("MQTT_TLS_KEY"),
		Region:      defaultRegion,
		Districts:   list(or(get("SITE_DISTRICTS"), defaultDistricts)),
		Adapters:    list(get("PILE_ADAPTERS")),
		AdminKeys:   map[string]ed25519.PublicKey{},
		Devices:     map[string]Device{},
		OfflineScan: or(get("PILE_OFFLINE_SCAN"), "@every 30s"),
	}
	if c.DBDSN == "" {
		return c, errors.New("缺少 DB_DSN")
	}
	port, err := strconv.Atoi(or(get("PORT"), "18306"))
	if err != nil {
		return c, errors.New("PORT 应为整数")
	}
	c.Port = port
	tz, err := time.LoadLocation(or(get("TZ_NAME"), "Asia/Shanghai"))
	if err != nil {
		return c, fmt.Errorf("TZ_NAME: %w", err)
	}
	c.TZ = tz

	keys, err := kv(get("ADMIN_JWT_PUBKEYS"))
	if err != nil {
		return c, fmt.Errorf("ADMIN_JWT_PUBKEYS: %w", err)
	}
	for kid, b64 := range keys {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return c, fmt.Errorf("ADMIN_JWT_PUBKEYS 中 %s 不是 Ed25519 公钥的 base64", kid)
		}
		c.AdminKeys[kid] = ed25519.PublicKey(raw)
	}
	if c.AllowedCallers, err = kv(get("ALLOWED_CALLERS")); err != nil {
		return c, fmt.Errorf("ALLOWED_CALLERS: %w", err)
	}
	if c.VendorKeys, err = kv(get("PILE_VENDOR_KEYS")); err != nil {
		return c, fmt.Errorf("PILE_VENDOR_KEYS: %w", err)
	}

	for _, a := range list(or(get("PILE_ACCESS"), "mqtt,http")) {
		switch strings.ToLower(a) {
		case "mqtt":
			c.AccessMQTT = true
		case "http":
			c.AccessHTTP = true
		default:
			return c, fmt.Errorf("PILE_ACCESS 里的 %q 不是 mqtt 或 http", a)
		}
	}

	if v := strings.TrimSpace(get("SITE_REGION")); v != "" {
		if err := json.Unmarshal([]byte(v), &c.Region); err != nil {
			return c, fmt.Errorf("SITE_REGION: %w", err)
		}
	}
	if v := get("SITE_FENCE_MIN_AREA_M2"); v != "" {
		if c.FenceMinAreaM2, err = strconv.ParseFloat(v, 64); err != nil {
			return c, errors.New("SITE_FENCE_MIN_AREA_M2 应为数字")
		}
	} else {
		c.FenceMinAreaM2 = 20
	}
	c.RetentionDays = 30
	if v := strings.TrimSpace(get("EVENT_RETENTION_DAYS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 3650 {
			return c, errors.New("EVENT_RETENTION_DAYS 应为 1～3650 的整数天数")
		}
		c.RetentionDays = n
	}
	if c.OfflineAfter, err = secs(get, "PILE_OFFLINE_AFTER_SEC", 180); err != nil {
		return c, err
	}
	if c.ClockSkew, err = secs(get, "PILE_CLOCK_SKEW_SEC", 300); err != nil {
		return c, err
	}

	if v := strings.TrimSpace(get("PILE_DEVICES")); v != "" {
		var ds []Device
		if err := json.Unmarshal([]byte(v), &ds); err != nil {
			return c, fmt.Errorf("PILE_DEVICES: %w", err)
		}
		for _, d := range ds {
			switch {
			case d.DeviceID == "":
				return c, errors.New("PILE_DEVICES: 缺 deviceId")
			case (d.StationID == "") == (d.PileID == ""):
				return c, fmt.Errorf("PILE_DEVICES: %s 须且只须给 stationId 或 pileId 之一", d.DeviceID)
			case d.Secret == "" && d.Vendor == "":
				return c, fmt.Errorf("PILE_DEVICES: %s 缺 secret", d.DeviceID)
			}
			if _, dup := c.Devices[d.DeviceID]; dup {
				return c, fmt.Errorf("PILE_DEVICES: %s 重复", d.DeviceID)
			}
			c.Devices[d.DeviceID] = d
		}
	}
	return c, nil
}

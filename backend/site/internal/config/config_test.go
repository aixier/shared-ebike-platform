package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_DSN": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr() != "127.0.0.1:18306" {
		t.Fatalf("默认监听地址应为 127.0.0.1:18306: %s", c.ListenAddr())
	}
	if c.MQTTAddr != "127.0.0.1:1883" {
		t.Fatalf("默认 MQTT 地址应只监听回环: %s", c.MQTTAddr)
	}
	if !c.AccessMQTT || !c.AccessHTTP || len(c.Adapters) != 0 {
		t.Fatalf("默认接入方式应为 MQTT+HTTP、无适配器: %+v", c)
	}
	if c.FenceMinAreaM2 != 20 || c.OfflineAfter != 180*time.Second || c.ClockSkew != 300*time.Second {
		t.Fatalf("默认参数错误: %+v", c)
	}
	if len(c.Districts) != 4 || c.Region.City == "" {
		t.Fatalf("默认区域错误: %+v", c.Region)
	}
}

func TestRequiresDSN(t *testing.T) {
	if _, err := Load(env(map[string]string{})); err == nil {
		t.Fatal("缺 DB_DSN 应报错")
	}
}

func TestParse(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	c, err := Load(env(map[string]string{
		"DB_DSN":            "postgres://x",
		"LISTEN_HOST":       "0.0.0.0",
		"PORT":              "28306",
		"ADMIN_JWT_PUBKEYS": "k1=" + base64.StdEncoding.EncodeToString(pub),
		"ALLOWED_CALLERS":   "ride=abc,ops=def",
		"PILE_ACCESS":       "mqtt",
		"PILE_ADAPTERS":     "acme",
		"PILE_DEVICES":      `[{"deviceId":"DV0001","stationId":"ST0001","secret":"s1"},{"deviceId":"DV0101","stationId":"ST0003","vendor":"acme","secret":"s2"},{"deviceId":"DV9001","pileId":"PL00005","secret":"s3"}]`,
		"PILE_VENDOR_KEYS":  "acme=vk",
		"SITE_REGION":       `{"city":"测试市","center":[1,2],"bounds":[[0,0],[3,3]],"coordSystem":"GCJ-02"}`,
		"SITE_DISTRICTS":    "甲区,乙区",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr() != "0.0.0.0:28306" || len(c.AdminKeys) != 1 || c.AllowedCallers["ops"] != "def" {
		t.Fatalf("解析错误: %+v", c)
	}
	if c.AccessHTTP || !c.AccessMQTT || c.Adapters[0] != "acme" {
		t.Fatalf("接入方式解析错误")
	}
	if len(c.Devices) != 3 || c.Devices["DV0101"].Vendor != "acme" || c.Devices["DV9001"].PileID != "PL00005" || c.VendorKeys["acme"] != "vk" {
		t.Fatalf("控制器解析错误: %+v", c.Devices)
	}
	if c.Region.City != "测试市" || c.Region.Bounds[1][0] != 3 || len(c.Districts) != 2 {
		t.Fatalf("区域解析错误: %+v", c.Region)
	}
}

func TestBadDevices(t *testing.T) {
	bad := []string{
		`[{"deviceId":"D","secret":"s"}]`,                                                                         // 既无站点也无桩
		`[{"deviceId":"D","stationId":"ST0001","pileId":"PL00001","secret":"s"}]`,                                 // 两者都有
		`[{"deviceId":"D","stationId":"ST0001"}]`,                                                                 // 无密钥且无厂商
		`[{"deviceId":"D","stationId":"ST0001","secret":"s"},{"deviceId":"D","stationId":"ST0002","secret":"t"}]`, // 重复
		`not json`,
	}
	for i, b := range bad {
		if _, err := Load(env(map[string]string{"DB_DSN": "x", "PILE_DEVICES": b})); err == nil {
			t.Errorf("第 %d 个非法控制器配置未被拒绝", i)
		}
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "x", "ADMIN_JWT_PUBKEYS": "k1=@@"})); err == nil {
		t.Error("非法公钥应报错")
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "x", "PILE_ACCESS": "carrier-pigeon"})); err == nil {
		t.Error("未知接入方式应报错")
	}
}

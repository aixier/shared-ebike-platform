package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

// trace: contracts/wire.md 第 5 节 任务 4

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoad_缺省值(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_DSN": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "127.0.0.1:18307" {
		t.Errorf("缺省只监听本机回环 18307，实得 %s", c.Addr())
	}
	if c.Seed != "" || c.NatsURL != "" || c.EventRetention != 30*24*time.Hour {
		t.Errorf("缺省值不对：%+v", c)
	}
}

func TestLoad_完整配置(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	c, err := Load(env(map[string]string{
		"PORT": "28307", "LISTEN_HOST": "0.0.0.0", "DB_DSN": "postgres://x", "NATS_URL": "nats://127.0.0.1:4222",
		"ADMIN_JWT_PUBKEYS": "k1=" + base64.StdEncoding.EncodeToString(pub), "ALLOWED_CALLERS": "bill=s1",
		"RULE_SEED": "prototype", "EVENT_RETENTION_DAYS": "30",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "0.0.0.0:28307" || c.NatsURL == "" || len(c.AdminKeys) != 1 || c.Callers["bill"] != "s1" || c.Seed != "prototype" || c.EventRetention != 30*24*time.Hour {
		t.Errorf("配置解析不对：%+v", c)
	}
}

func TestLoad_错误(t *testing.T) {
	cases := []map[string]string{
		{},
		{"DB_DSN": "x", "PORT": "abc"},
		{"DB_DSN": "x", "ADMIN_JWT_PUBKEYS": "k1=bad"},
		{"DB_DSN": "x", "ALLOWED_CALLERS": "bill"},
		{"DB_DSN": "x", "RULE_SEED": "demo"},
		{"DB_DSN": "x", "EVENT_RETENTION_DAYS": "-1"},
	}
	for i, m := range cases {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("第 %d 组应报错", i)
		}
	}
}

func TestLoad_事件保留期(t *testing.T) {
	for v, want := range map[string]time.Duration{"7": 7 * 24 * time.Hour, "0": 0} {
		c, err := Load(env(map[string]string{"DB_DSN": "x", "EVENT_RETENTION_DAYS": v}))
		if err != nil || c.EventRetention != want {
			t.Errorf("EVENT_RETENTION_DAYS=%s 实得 %v %v", v, c.EventRetention, err)
		}
	}
}

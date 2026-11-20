package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestFromEnvDefaults(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	c, err := FromEnv(env(map[string]string{
		"DB_DSN":            "postgres://x",
		"ADMIN_JWT_KID":     "k1",
		"ADMIN_JWT_PRIVKEY": base64.StdEncoding.EncodeToString(priv.Seed()),
		"ALLOWED_CALLERS":   "ride=abc, fleet=def",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "127.0.0.1:18315" {
		t.Fatalf("默认监听地址应为 127.0.0.1:18315，实际 %s", c.Addr())
	}
	if c.TokenTTL != 30*time.Minute || c.AllowedCallers["fleet"] != "def" || c.Location.String() != "Asia/Shanghai" {
		t.Fatalf("配置不对: %+v", c)
	}
}

func TestFromEnvErrors(t *testing.T) {
	if _, err := FromEnv(env(map[string]string{})); err == nil {
		t.Fatal("缺必填项应报错")
	}
	if _, err := FromEnv(env(map[string]string{"ALLOWED_CALLERS": "ride"})); err == nil {
		t.Fatal("调用方格式错应报错")
	}
}

func baseEnv() map[string]string {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	return map[string]string{"DB_DSN": "postgres://x", "ADMIN_JWT_KID": "k1", "ADMIN_JWT_PRIVKEY": base64.StdEncoding.EncodeToString(priv.Seed())}
}

func TestEventRetention(t *testing.T) {
	m := baseEnv()
	c, err := FromEnv(env(m))
	if err != nil || c.StreamMaxAge != 30*24*time.Hour {
		t.Fatalf("默认 30 天: %v %v", err, c.StreamMaxAge)
	}
	m["STREAM_MAX_AGE"] = "48h"
	if c, _ = FromEnv(env(m)); c.StreamMaxAge != 48*time.Hour {
		t.Fatalf("旧变量兜底: %v", c.StreamMaxAge)
	}
	m["EVENT_RETENTION_DAYS"] = "7"
	if c, _ = FromEnv(env(m)); c.StreamMaxAge != 7*24*time.Hour {
		t.Fatalf("新变量优先: %v", c.StreamMaxAge)
	}
	m["EVENT_RETENTION_DAYS"] = "abc"
	if _, err = FromEnv(env(m)); err == nil {
		t.Fatal("非法天数应报错")
	}
}

func TestTokenTTLCapped(t *testing.T) {
	m := baseEnv()
	m["ADMIN_TOKEN_TTL"] = "8h"
	c, err := FromEnv(env(m))
	if err != nil || c.TokenTTL != 30*time.Minute {
		t.Fatalf("超过 30 分钟应按 30 分钟: %v %v", err, c.TokenTTL)
	}
	m["ADMIN_TOKEN_TTL"] = "10m"
	if c, _ = FromEnv(env(m)); c.TokenTTL != 10*time.Minute {
		t.Fatalf("%v", c.TokenTTL)
	}
}

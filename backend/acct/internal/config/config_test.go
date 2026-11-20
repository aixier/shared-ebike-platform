package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaultsAndKeys(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, priv2, _ := ed25519.GenerateKey(rand.Reader)
	oldPub := priv2.Public().(ed25519.PublicKey)
	c, err := Load(env(map[string]string{
		"DB_DSN":             "postgres://x",
		"USER_JWT_PRIVKEY":   base64.StdEncoding.EncodeToString(priv.Seed()),
		"USER_JWT_KID":       "u2",
		"USER_JWT_PUBKEYS":   "u1=" + base64.StdEncoding.EncodeToString(oldPub),
		"ALLOWED_CALLERS":    "cs=k1, pay=k2",
		"ACCT_EXTERNAL_MODE": "mock",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "127.0.0.1:18301" {
		t.Errorf("默认只监听本机回环: %s", c.Addr())
	}
	if !c.UserPubKeys["u2"].Equal(pub) || c.UserPubKeys["u1"] == nil {
		t.Errorf("自己的公钥应并入验签公钥集合")
	}
	if c.AllowedCallers["cs"] != "k1" || c.AllowedCallers["pay"] != "k2" {
		t.Errorf("调用方表解析不对: %v", c.AllowedCallers)
	}
	if c.TokenTTL != 168*time.Hour || c.DeferredDays != 7 || c.LegalAge != 16 {
		t.Errorf("默认参数不对: %v %d %d", c.TokenTTL, c.DeferredDays, c.LegalAge)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(env(map[string]string{})); err == nil {
		t.Error("缺 DB_DSN 应报错")
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	base := map[string]string{"DB_DSN": "x", "USER_JWT_PRIVKEY": base64.StdEncoding.EncodeToString(priv.Seed()), "USER_JWT_KID": "k"}
	base["ACCT_EXTERNAL_MODE"] = "other"
	if _, err := Load(env(base)); err == nil {
		t.Error("外部模式非法应报错")
	}
	delete(base, "ACCT_EXTERNAL_MODE")
	delete(base, "USER_JWT_KID")
	if _, err := Load(env(base)); err == nil {
		t.Error("缺 USER_JWT_KID 应报错")
	}
}

func TestLoadEventRetention(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	base := func(extra map[string]string) func(string) string {
		m := map[string]string{"DB_DSN": "x", "USER_JWT_PRIVKEY": base64.StdEncoding.EncodeToString(priv.Seed()), "USER_JWT_KID": "k"}
		for k, v := range extra {
			m[k] = v
		}
		return env(m)
	}
	cases := []struct {
		name  string
		extra map[string]string
		want  time.Duration
	}{
		{"默认 30 天", nil, 30 * 24 * time.Hour},
		{"EVENT_RETENTION_DAYS", map[string]string{"EVENT_RETENTION_DAYS": "7"}, 7 * 24 * time.Hour},
		{"旧变量兜底", map[string]string{"ACCT_EVENT_RETENTION_HOURS": "48"}, 48 * time.Hour},
		{"新变量优先于旧变量", map[string]string{"EVENT_RETENTION_DAYS": "3", "ACCT_EVENT_RETENTION_HOURS": "48"}, 72 * time.Hour},
	}
	for _, c := range cases {
		cfg, err := Load(base(c.extra))
		if err != nil || cfg.EventRetention != c.want {
			t.Errorf("%s: %v %v want %v", c.name, cfg, err, c.want)
		}
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		if _, err := Load(base(map[string]string{"EVENT_RETENTION_DAYS": bad})); err == nil {
			t.Errorf("EVENT_RETENTION_DAYS=%q 应报错", bad)
		}
	}
}

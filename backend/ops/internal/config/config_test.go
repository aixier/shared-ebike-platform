package config

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_DSN": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr() != "127.0.0.1:18313" {
		t.Fatalf("默认只监听本机回环 18313，得 %s", c.ListenAddr())
	}
	if c.LimitNearRatio != 0.8 || c.PaymentUnknownTimeoutMinutes != 30 || c.Timezone != "Asia/Shanghai" {
		t.Fatalf("默认参数不对：%+v", c)
	}
	if len(c.OnDuty) != 0 {
		t.Fatal("值班人默认为空")
	}
}

func TestOverride(t *testing.T) {
	c, err := Load(env(map[string]string{
		"DB_DSN": "d", "PORT": "28313", "LISTEN_HOST": "0.0.0.0",
		"OPS_ONDUTY_ACCOUNTS": "AC0101, AC0102", "OPS_LIMIT_NEAR_RATIO": "0.9",
		"FLEET_URL": "http://127.0.0.1:18305/",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr() != "0.0.0.0:28313" || len(c.OnDuty) != 2 || c.OnDuty[1] != "AC0102" || c.LimitNearRatio != 0.9 || c.FleetURL != "http://127.0.0.1:18305" {
		t.Fatalf("覆盖不对：%+v", c)
	}
}

func TestRequired(t *testing.T) {
	if _, err := Load(env(map[string]string{})); err == nil {
		t.Fatal("缺 DB_DSN 应报错")
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "d", "OPS_LIMIT_NEAR_RATIO": "abc"})); err == nil {
		t.Fatal("数值参数不合法应报错")
	}
}

func TestEventRetention(t *testing.T) {
	c, _ := Load(env(map[string]string{"DB_DSN": "d"}))
	if c.EventRetentionHours != 30*24 {
		t.Fatalf("默认 30 天，得 %d 小时", c.EventRetentionHours)
	}
	c, _ = Load(env(map[string]string{"DB_DSN": "d", "NATS_STREAM_MAX_AGE_HOURS": "48"}))
	if c.EventRetentionHours != 48 {
		t.Fatalf("旧变量兜底，得 %d", c.EventRetentionHours)
	}
	c, _ = Load(env(map[string]string{"DB_DSN": "d", "NATS_STREAM_MAX_AGE_HOURS": "48", "EVENT_RETENTION_DAYS": "7"}))
	if c.EventRetentionHours != 7*24 {
		t.Fatalf("新变量优先，得 %d", c.EventRetentionHours)
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "d", "EVENT_RETENTION_DAYS": "0"})); err == nil {
		t.Fatal("非正整数应报错")
	}
}

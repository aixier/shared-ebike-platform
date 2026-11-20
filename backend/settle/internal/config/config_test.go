package config

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_DSN": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "127.0.0.1:18311" || !c.AutoSettle || c.EventRetentionDays != 30 {
		t.Errorf("默认值 %+v", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_DSN": "d", "PORT": "1234", "LISTEN_HOST": "0.0.0.0", "SETTLE_AUTO_GENERATE": "false", "SETTLE_EVENT_RETENTION_DAYS": "30"}))
	if err != nil || c.Addr() != "0.0.0.0:1234" || c.AutoSettle || c.EventRetentionDays != 30 {
		t.Errorf("%+v %v", c, err)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(env(map[string]string{})); err == nil {
		t.Error("缺 DB_DSN 应报错")
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "d", "PORT": "x"})); err == nil {
		t.Error("PORT 非数字应报错")
	}
}

func TestRetentionVars(t *testing.T) {
	c, _ := Load(env(map[string]string{"DB_DSN": "d"}))
	if c.EventRetentionDays != 30 {
		t.Errorf("默认应为 30: %d", c.EventRetentionDays)
	}
	c, err := Load(env(map[string]string{"DB_DSN": "d", "EVENT_RETENTION_DAYS": "45", "SETTLE_EVENT_RETENTION_DAYS": "400"}))
	if err != nil || c.EventRetentionDays != 45 {
		t.Errorf("统一变量优先: %+v %v", c, err)
	}
	c, err = Load(env(map[string]string{"DB_DSN": "d", "SETTLE_EVENT_RETENTION_DAYS": "400"}))
	if err != nil || c.EventRetentionDays != 400 {
		t.Errorf("旧变量兜底: %+v %v", c, err)
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "d", "EVENT_RETENTION_DAYS": "0"})); err == nil {
		t.Error("非正整数应报错")
	}
}

package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	env := map[string]string{"DB_DSN": "postgres://x", "SPEC_REPLY_LIMIT_MS": "800", "CONFORMANCE_GRACE_SEC": "5"}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:18304" {
		t.Fatalf("默认只监听本机回环地址: %s", c.Addr)
	}
	if c.App.ReplyLimit != 800*time.Millisecond || c.App.Grace != 5*time.Second || c.App.CallTimeout != 3*time.Second || c.App.AgentAccountID != "AC0903" {
		t.Fatalf("参数不对: %+v", c.App)
	}
	env["LISTEN_HOST"], env["PORT"] = "0.0.0.0", "9000"
	if c, _ := Load(func(k string) string { return env[k] }); c.Addr != "0.0.0.0:9000" {
		t.Fatalf("生产由部署配置显式给监听地址: %s", c.Addr)
	}
	if _, err := Load(func(string) string { return "" }); err == nil {
		t.Fatal("缺 DB_DSN 应报错")
	}
	env["SPEC_REPLY_LIMIT_MS"] = "abc"
	if _, err := Load(func(k string) string { return env[k] }); err == nil {
		t.Fatal("非数字应报错")
	}
}

// 第二轮：事件保留期读 EVENT_RETENTION_DAYS（默认 30），旧变量 EVENT_MAX_AGE_HOURS 作兜底。
func TestEventRetention(t *testing.T) {
	env := map[string]string{"DB_DSN": "postgres://x"}
	get := func() time.Duration {
		c, err := Load(func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		return c.App.EventMaxAge
	}
	if got := get(); got != 30*24*time.Hour {
		t.Fatalf("默认 30 天: %v", got)
	}
	env["EVENT_MAX_AGE_HOURS"] = "48"
	if got := get(); got != 48*time.Hour {
		t.Fatalf("旧变量兜底: %v", got)
	}
	env["EVENT_RETENTION_DAYS"] = "7"
	if got := get(); got != 7*24*time.Hour {
		t.Fatalf("新变量优先: %v", got)
	}
	env["EVENT_RETENTION_DAYS"] = "0"
	if _, err := Load(func(k string) string { return env[k] }); err == nil {
		t.Fatal("非正整数应报错")
	}
}

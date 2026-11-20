// Package config 从环境变量读配置（contracts/wire.md 第 5 节，专有变量见 README）。
package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"bike/onboard/internal/app"
)

// Config 进程配置。
type Config struct {
	Addr            string
	DBDSN           string
	NATSURL         string
	AdminJWTPubKeys string
	ServiceKey      string
	AllowedCallers  string
	FleetURL        string
	App             app.Config
}

// Load 读环境变量。getenv 便于测试注入。
func Load(getenv func(string) string) (*Config, error) {
	get := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	c := &Config{
		Addr:            get("LISTEN_HOST", "127.0.0.1") + ":" + get("PORT", "18304"),
		DBDSN:           getenv("DB_DSN"),
		NATSURL:         getenv("NATS_URL"),
		AdminJWTPubKeys: getenv("ADMIN_JWT_PUBKEYS"),
		ServiceKey:      getenv("SERVICE_KEY"),
		AllowedCallers:  getenv("ALLOWED_CALLERS"),
		FleetURL:        get("FLEET_URL", "http://127.0.0.1:18305"),
		App:             app.DefaultConfig(),
	}
	if c.DBDSN == "" {
		return nil, errors.New("缺少环境变量 DB_DSN")
	}
	c.App.AgentAccountID = get("ONBOARD_AGENT_ACCOUNT_ID", c.App.AgentAccountID)
	c.App.SandboxBaseURL = get("SANDBOX_BASE_URL", c.App.SandboxBaseURL)
	for _, d := range []struct {
		key  string
		unit time.Duration
		dst  *time.Duration
	}{
		{"SPEC_REPLY_LIMIT_MS", time.Millisecond, &c.App.ReplyLimit},
		{"SPEC_CALL_TIMEOUT_MS", time.Millisecond, &c.App.CallTimeout},
		{"SPEC_CLOCK_SKEW_SEC", time.Second, &c.App.ClockSkew},
		{"CONFORMANCE_GRACE_SEC", time.Second, &c.App.Grace},
		{"CONFORMANCE_LOCK_WAIT_SEC", time.Second, &c.App.LockWait},
		{"EVENT_MAX_AGE_HOURS", time.Hour, &c.App.EventMaxAge}, // 旧变量，兜底保留一个版本；EVENT_RETENTION_DAYS 优先
		{"EVENT_RETENTION_DAYS", 24 * time.Hour, &c.App.EventMaxAge},
	} {
		if v := getenv(d.key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("环境变量 %s 应为正整数", d.key)
			}
			*d.dst = time.Duration(n) * d.unit
		}
	}
	return c, nil
}

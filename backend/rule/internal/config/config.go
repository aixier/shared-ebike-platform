// Package config 从环境变量读配置；变量名见 contracts/wire.md 第 5 节与本模块 README。
package config

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"bike/rule/internal/auth"
)

// Config 本服务的全部配置。
type Config struct {
	Port           int
	ListenHost     string
	DBDSN          string
	NatsURL        string
	AdminKeys      map[string]ed25519.PublicKey
	Callers        auth.Callers
	Seed           string        // 空或 prototype：库为空时载入原型样例
	EventRetention time.Duration // 事件流按时间保留的期限；默认 30 天，EVENT_RETENTION_DAYS=0 为不限
}

// Addr 监听地址 LISTEN_HOST:PORT。
func (c Config) Addr() string { return net.JoinHostPort(c.ListenHost, strconv.Itoa(c.Port)) }

// Load 读配置；getenv 一般传 os.Getenv。
func Load(getenv func(string) string) (Config, error) {
	c := Config{Port: 18307, ListenHost: "127.0.0.1", EventRetention: 30 * 24 * time.Hour}
	if v := getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return c, fmt.Errorf("PORT 不是合法端口：%q", v)
		}
		c.Port = p
	}
	if v := getenv("LISTEN_HOST"); v != "" {
		c.ListenHost = v
	}
	c.DBDSN = getenv("DB_DSN")
	if c.DBDSN == "" {
		return c, errors.New("缺少 DB_DSN")
	}
	c.NatsURL = getenv("NATS_URL")
	keys, err := auth.ParsePubKeys(getenv("ADMIN_JWT_PUBKEYS"))
	if err != nil {
		return c, fmt.Errorf("ADMIN_JWT_PUBKEYS：%w", err)
	}
	c.AdminKeys = keys
	callers, err := auth.ParseCallers(getenv("ALLOWED_CALLERS"))
	if err != nil {
		return c, fmt.Errorf("ALLOWED_CALLERS：%w", err)
	}
	c.Callers = callers
	switch v := getenv("RULE_SEED"); v {
	case "", "prototype":
		c.Seed = v
	default:
		return c, fmt.Errorf("RULE_SEED 只能为空或 prototype：%q", v)
	}
	if v := getenv("EVENT_RETENTION_DAYS"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 0 {
			return c, fmt.Errorf("EVENT_RETENTION_DAYS 应为非负整数：%q", v)
		}
		c.EventRetention = time.Duration(d) * 24 * time.Hour
	}
	return c, nil
}

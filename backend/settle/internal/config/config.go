// Package config 从环境变量读取本服务配置（变量名见 contracts/wire.md 第 5 节）。
package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// Config 是 settle 的全部配置。
type Config struct {
	Port               int
	ListenHost         string
	DBDSN              string
	NatsURL            string
	AdminJWTPubKeys    string
	EventRetentionDays int // 事件流 EVT_SETTLE 保留天数：EVENT_RETENTION_DAYS，兜底旧变量 SETTLE_EVENT_RETENTION_DAYS，默认 30
	AutoSettle         bool
}

// Addr 监听地址 LISTEN_HOST:PORT。
func (c Config) Addr() string { return c.ListenHost + ":" + strconv.Itoa(c.Port) }

// Load 读取环境变量；getenv 为空时用 os.Getenv。
func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := Config{Port: 18311, ListenHost: "127.0.0.1", EventRetentionDays: 30, AutoSettle: true}
	if v := strings.TrimSpace(getenv("PORT")); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return c, errors.New("PORT 不是有效端口")
		}
		c.Port = p
	}
	if v := strings.TrimSpace(getenv("LISTEN_HOST")); v != "" {
		c.ListenHost = v
	}
	c.DBDSN = strings.TrimSpace(getenv("DB_DSN"))
	if c.DBDSN == "" {
		return c, errors.New("缺少 DB_DSN")
	}
	c.NatsURL = strings.TrimSpace(getenv("NATS_URL"))
	c.AdminJWTPubKeys = strings.TrimSpace(getenv("ADMIN_JWT_PUBKEYS"))
	// 统一变量优先；原 SETTLE_EVENT_RETENTION_DAYS 保留一个版本作兜底
	for _, name := range []string{"EVENT_RETENTION_DAYS", "SETTLE_EVENT_RETENTION_DAYS"} {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			d, err := strconv.Atoi(v)
			if err != nil || d <= 0 {
				return c, errors.New(name + " 应为正整数")
			}
			c.EventRetentionDays = d
			break
		}
	}
	if v := strings.TrimSpace(getenv("SETTLE_AUTO_GENERATE")); v != "" {
		c.AutoSettle = v != "false" && v != "0"
	}
	return c, nil
}

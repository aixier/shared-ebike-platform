// Package config 读环境变量（变量名见 contracts/wire.md 第 5 节与本服务 README）。
package config

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strconv"

	"bike/fleet/internal/auth"
)

type Config struct {
	ListenHost         string
	Port               int
	DBDSN              string
	NatsURL            string
	AdminKeys          map[string]ed25519.PublicKey
	Callers            auth.Callers
	EventRetentionDays int
	ExitReadonlyDays   int    // 运营商退出后只读保留天数，0 为不设截止
	SeedFile           string // 登记数据文件（运营商、品牌、车联网服务商，可含车辆），可空
}

// Addr 返回监听地址 LISTEN_HOST:PORT。
func (c *Config) Addr() string { return fmt.Sprintf("%s:%d", c.ListenHost, c.Port) }

// Load 从 getenv 读取配置。
func Load(getenv func(string) string) (*Config, error) {
	c := &Config{
		ListenHost:         or(getenv("LISTEN_HOST"), "127.0.0.1"),
		DBDSN:              getenv("DB_DSN"),
		NatsURL:            or(getenv("NATS_URL"), "nats://127.0.0.1:4222"),
		SeedFile:           getenv("FLEET_SEED_FILE"),
		Port:               18305,
		EventRetentionDays: 30,
	}
	if p := getenv("PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("PORT 不是数字: %w", err)
		}
		c.Port = n
	}
	if d := getenv("EVENT_RETENTION_DAYS"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 {
			return nil, errors.New("EVENT_RETENTION_DAYS 应为正整数")
		}
		c.EventRetentionDays = n
	}
	if d := getenv("EXIT_READONLY_DAYS"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 {
			return nil, errors.New("EXIT_READONLY_DAYS 应为非负整数")
		}
		c.ExitReadonlyDays = n
	}
	if c.DBDSN == "" {
		return nil, errors.New("缺少 DB_DSN")
	}
	var err error
	if c.AdminKeys, err = auth.ParseKeys(getenv("ADMIN_JWT_PUBKEYS")); err != nil {
		return nil, fmt.Errorf("ADMIN_JWT_PUBKEYS: %w", err)
	}
	if c.Callers, err = auth.ParseCallers(getenv("ALLOWED_CALLERS")); err != nil {
		return nil, fmt.Errorf("ALLOWED_CALLERS: %w", err)
	}
	return c, nil
}

func or(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

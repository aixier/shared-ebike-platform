package app

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"bike/bill/internal/auth"
)

// Config 全部来自环境变量（contracts/wire.md 第 5 节）
type Config struct {
	Port       string
	ListenHost string
	DBDSN      string
	NatsURL    string
	UserKeys   map[string]ed25519.PublicKey
	AdminKeys  map[string]ed25519.PublicKey
	ServiceKey string
	Callers    auth.Callers
	PayURL     string
	DepositURL string
	// EventRetentionDays 本服务事件流的保留天数（EVENT_RETENTION_DAYS，默认 30）
	EventRetentionDays int
}

// LoadConfig 读环境变量
func LoadConfig(get func(string) string) (Config, error) {
	if get == nil {
		get = os.Getenv
	}
	c := Config{
		Port:       get("PORT"),
		ListenHost: get("LISTEN_HOST"),
		DBDSN:      get("DB_DSN"),
		NatsURL:    get("NATS_URL"),
		ServiceKey: get("SERVICE_KEY"),
		PayURL:     get("PAY_URL"),
		DepositURL: get("DEPOSIT_URL"),
	}
	if c.Port == "" {
		c.Port = "18308"
	}
	if c.ListenHost == "" {
		c.ListenHost = "127.0.0.1"
	}
	if c.DBDSN == "" {
		return c, fmt.Errorf("缺少 DB_DSN")
	}
	c.EventRetentionDays = 30
	if v := get("EVENT_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 1 {
			return c, fmt.Errorf("EVENT_RETENTION_DAYS 应为不小于 1 的整数：%q", v)
		}
		c.EventRetentionDays = n
	}
	var err error
	if c.UserKeys, err = auth.ParseKeys(get("USER_JWT_PUBKEYS")); err != nil {
		return c, fmt.Errorf("USER_JWT_PUBKEYS：%w", err)
	}
	if c.AdminKeys, err = auth.ParseKeys(get("ADMIN_JWT_PUBKEYS")); err != nil {
		return c, fmt.Errorf("ADMIN_JWT_PUBKEYS：%w", err)
	}
	if c.Callers, err = auth.ParseCallers(get("ALLOWED_CALLERS")); err != nil {
		return c, err
	}
	return c, nil
}

// Addr 监听地址 LISTEN_HOST:PORT
func (c Config) Addr() string { return c.ListenHost + ":" + c.Port }

// EventRetention 事件流保留期
func (c Config) EventRetention() time.Duration {
	return time.Duration(c.EventRetentionDays) * 24 * time.Hour
}

// Package config 从环境变量读取配置。变量清单见 backend/cs/README.md「运行」与 contracts/wire.md 第 5 节。
package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"bike/cs/internal/auth"
)

// Config 是本服务的全部配置。
type Config struct {
	Port       string
	ListenHost string
	DBDSN      string
	NatsURL    string

	UserKeys  auth.Keys
	AdminKeys auth.Keys

	ServiceKey string
	Callers    auth.Callers

	RideURL    string
	BillURL    string
	AcctURL    string
	DepositURL string

	// EventRetentionDays 是事件流 EVT_CS 的保留天数（EVENT_RETENTION_DAYS，默认 30）。
	EventRetentionDays int

	// DefaultRefundLimit 是收到 rule 平台参数前使用的客服退款授权额度（分）。
	DefaultRefundLimit int64
}

// Addr 是监听地址 LISTEN_HOST:PORT。
func (c *Config) Addr() string { return net.JoinHostPort(c.ListenHost, c.Port) }

// Load 读取配置；getenv 一般为 os.Getenv。
func Load(getenv func(string) string) (*Config, error) {
	or := func(k, d string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return d
	}
	c := &Config{
		Port:       or("PORT", "18312"),
		ListenHost: or("LISTEN_HOST", "127.0.0.1"),
		DBDSN:      getenv("DB_DSN"),
		NatsURL:    getenv("NATS_URL"),
		ServiceKey: getenv("SERVICE_KEY"),
		RideURL:    getenv("RIDE_URL"),
		BillURL:    getenv("BILL_URL"),
		AcctURL:    getenv("ACCT_URL"),
		DepositURL: getenv("DEPOSIT_URL"),
	}
	if c.DBDSN == "" {
		return nil, errors.New("缺少环境变量 DB_DSN")
	}
	var err error
	if c.UserKeys, err = auth.ParseKeys(getenv("USER_JWT_PUBKEYS")); err != nil {
		return nil, fmt.Errorf("USER_JWT_PUBKEYS：%w", err)
	}
	if c.AdminKeys, err = auth.ParseKeys(getenv("ADMIN_JWT_PUBKEYS")); err != nil {
		return nil, fmt.Errorf("ADMIN_JWT_PUBKEYS：%w", err)
	}
	if c.Callers, err = auth.ParseCallers(getenv("ALLOWED_CALLERS")); err != nil {
		return nil, fmt.Errorf("ALLOWED_CALLERS：%w", err)
	}
	c.DefaultRefundLimit, err = strconv.ParseInt(or("CS_DEFAULT_REFUND_LIMIT", "2000"), 10, 64)
	if err != nil || c.DefaultRefundLimit < 0 {
		return nil, errors.New("CS_DEFAULT_REFUND_LIMIT 须为非负整数（分）")
	}
	c.EventRetentionDays = 30
	if d := getenv("EVENT_RETENTION_DAYS"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 1 {
			return nil, errors.New("EVENT_RETENTION_DAYS 须为正整数")
		}
		c.EventRetentionDays = n
	}
	return c, nil
}

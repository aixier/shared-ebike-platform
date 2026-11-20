// Package config 从环境变量读配置（contracts/wire.md 第 5 节；本模块专有的见 README）。
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Config 本服务配置。
type Config struct {
	ListenHost      string
	Port            string
	DBDSN           string
	NatsURL         string
	AdminJWTPubkeys string
	ServiceKey      string
	AllowedCallers  string
	FleetURL        string
	RideURL         string
	PayURL          string

	// 本模块专有
	OnDuty                       []string // OPS_ONDUTY_ACCOUNTS：值班人账号编号，告警通知对象
	LimitNearRatio               float64  // OPS_LIMIT_NEAR_RATIO：近 1 小时降级单量达到上限的该比例时提醒"接近限额"
	PaymentUnknownTimeoutMinutes int      // OPS_PAYMENT_UNKNOWN_TIMEOUT_MINUTES：收款结果未知超过该分钟数进人工处理
	Timezone                     string   // OPS_TIMEZONE：本市时区，用于按日统计与编号日期
	EventRetentionHours          int      // EVENT_RETENTION_DAYS（默认 30 天）折算的小时数；旧变量 NATS_STREAM_MAX_AGE_HOURS 作兜底
}

// ListenAddr 监听地址 LISTEN_HOST:PORT。
func (c Config) ListenAddr() string { return c.ListenHost + ":" + c.Port }

// Load 读配置；getenv 一般传 os.Getenv。
func Load(getenv func(string) string) (Config, error) {
	get := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	c := Config{
		ListenHost:      get("LISTEN_HOST", "127.0.0.1"),
		Port:            get("PORT", "18313"),
		DBDSN:           get("DB_DSN", ""),
		NatsURL:         get("NATS_URL", ""),
		AdminJWTPubkeys: get("ADMIN_JWT_PUBKEYS", ""),
		ServiceKey:      get("SERVICE_KEY", ""),
		AllowedCallers:  get("ALLOWED_CALLERS", ""),
		FleetURL:        strings.TrimRight(get("FLEET_URL", ""), "/"),
		RideURL:         strings.TrimRight(get("RIDE_URL", ""), "/"),
		PayURL:          strings.TrimRight(get("PAY_URL", ""), "/"),
		Timezone:        get("OPS_TIMEZONE", "Asia/Shanghai"),
	}
	if c.DBDSN == "" {
		return c, errors.New("缺少环境变量 DB_DSN")
	}
	for _, a := range strings.Split(get("OPS_ONDUTY_ACCOUNTS", ""), ",") {
		if a = strings.TrimSpace(a); a != "" {
			c.OnDuty = append(c.OnDuty, a)
		}
	}
	r, err := strconv.ParseFloat(get("OPS_LIMIT_NEAR_RATIO", "0.8"), 64)
	if err != nil || r <= 0 || r > 1 {
		return c, fmt.Errorf("OPS_LIMIT_NEAR_RATIO 应为 0～1 的小数")
	}
	c.LimitNearRatio = r
	m, err := strconv.Atoi(get("OPS_PAYMENT_UNKNOWN_TIMEOUT_MINUTES", "30"))
	if err != nil || m <= 0 {
		return c, fmt.Errorf("OPS_PAYMENT_UNKNOWN_TIMEOUT_MINUTES 应为正整数")
	}
	c.PaymentUnknownTimeoutMinutes = m
	c.EventRetentionHours = 30 * 24
	if v := get("EVENT_RETENTION_DAYS", ""); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d <= 0 {
			return c, fmt.Errorf("EVENT_RETENTION_DAYS 应为正整数")
		}
		c.EventRetentionHours = d * 24
	} else if v := get("NATS_STREAM_MAX_AGE_HOURS", ""); v != "" { // 旧变量，保留一个版本作兜底
		if h, err := strconv.Atoi(v); err == nil && h > 0 {
			c.EventRetentionHours = h
		}
	}
	return c, nil
}

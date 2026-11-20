// Package config 从环境变量读配置。变量名见 contracts/wire.md 第 5 节，本服务专有的见 README.md。
package config

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// Config 本服务配置。
type Config struct {
	ListenHost     string
	Port           int
	DBDSN          string
	NatsURL        string
	UserJWTPubKeys string
	AdminJWTPubKey string
	ServiceKey     string
	AllowedCallers string
	PayURL         string

	// 本服务专有
	CreditScorer          string // mock｜real；real 尚未接入
	CreditMockScores      string // userId=分值,...
	CreditMockDefault     int
	RefundWorkdaysDefault int    // 未收到 rule.params.changed 之前用的退押时限（工作日）
	DefaultCollectChannel string // 押金收款渠道缺省值：微信支付｜支付宝
	UTCOffsetHours        int
	StreamMaxAgeDays      int // 本模块事件流保留天数：EVENT_RETENTION_DAYS，未设时读旧变量 STREAM_MAX_AGE_DAYS，都没有为 30
}

// Load 读配置。getenv 一般传 os.Getenv。
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		ListenHost:            or(getenv("LISTEN_HOST"), "127.0.0.1"),
		DBDSN:                 getenv("DB_DSN"),
		NatsURL:               getenv("NATS_URL"),
		UserJWTPubKeys:        getenv("USER_JWT_PUBKEYS"),
		AdminJWTPubKey:        getenv("ADMIN_JWT_PUBKEYS"),
		ServiceKey:            getenv("SERVICE_KEY"),
		AllowedCallers:        getenv("ALLOWED_CALLERS"),
		PayURL:                getenv("PAY_URL"),
		CreditScorer:          or(getenv("CREDIT_SCORER"), "mock"),
		CreditMockScores:      getenv("CREDIT_MOCK_SCORES"),
		DefaultCollectChannel: or(getenv("DEFAULT_COLLECT_CHANNEL"), "微信支付"),
	}
	var err error
	if c.Port, err = atoi(getenv("PORT"), 18310, "PORT"); err != nil {
		return c, err
	}
	if c.CreditMockDefault, err = atoi(getenv("CREDIT_MOCK_DEFAULT"), 650, "CREDIT_MOCK_DEFAULT"); err != nil {
		return c, err
	}
	if c.RefundWorkdaysDefault, err = atoi(getenv("REFUND_WORKDAYS_DEFAULT"), 3, "REFUND_WORKDAYS_DEFAULT"); err != nil {
		return c, err
	}
	if c.UTCOffsetHours, err = atoi(getenv("UTC_OFFSET_HOURS"), 8, "UTC_OFFSET_HOURS"); err != nil {
		return c, err
	}
	legacy, err := atoi(getenv("STREAM_MAX_AGE_DAYS"), 30, "STREAM_MAX_AGE_DAYS") // 旧变量，保留一个版本作兜底
	if err != nil {
		return c, err
	}
	if c.StreamMaxAgeDays, err = atoi(getenv("EVENT_RETENTION_DAYS"), legacy, "EVENT_RETENTION_DAYS"); err != nil {
		return c, err
	}
	if c.StreamMaxAgeDays < 1 {
		return c, fmt.Errorf("事件保留天数应至少为 1")
	}
	if c.DBDSN == "" {
		return c, fmt.Errorf("缺少环境变量 DB_DSN")
	}
	if c.DefaultCollectChannel != "微信支付" && c.DefaultCollectChannel != "支付宝" {
		return c, fmt.Errorf("DEFAULT_COLLECT_CHANNEL 只能是 微信支付 或 支付宝")
	}
	if c.CreditScorer != "mock" && c.CreditScorer != "real" {
		return c, fmt.Errorf("CREDIT_SCORER 只能是 mock 或 real")
	}
	return c, nil
}

// Addr 监听地址 LISTEN_HOST:PORT。
func (c Config) Addr() string { return net.JoinHostPort(c.ListenHost, strconv.Itoa(c.Port)) }

// Location 本市时区，用于输出时间与计算到账日。
func (c Config) Location() *time.Location {
	return time.FixedZone(fmt.Sprintf("UTC%+d", c.UTCOffsetHours), c.UTCOffsetHours*3600)
}

func or(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func atoi(v string, d int, name string) (int, error) {
	if v == "" {
		return d, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s 应为整数：%q", name, v)
	}
	return n, nil
}

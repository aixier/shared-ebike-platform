package app

// 环境变量配置（wire.md 第 5 节；本服务专有的见 README）。

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 本服务配置
type Config struct {
	Port              int
	ListenHost        string
	DBDSN             string
	NatsURL           string
	UserPubKeys       string
	AdminPubKeys      string
	ServiceKey        string
	AllowedCallers    string
	AcctURL           string
	ChannelMode       string // mock｜real
	MockDefault       string
	MockUsers         string
	MockResolve       string
	StreamMaxAge      time.Duration     // 事件流保留期，由 EVENT_RETENTION_DAYS 得出
	MockCallbackDelay time.Duration     // 模拟渠道自动回调的延迟
	IntentTTL         time.Duration     // 一键支付调起参数的有效期
	WeChat            map[string]string // 真实渠道的商户配置原样传给适配器
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

// ConfigFromEnv 读环境变量
func ConfigFromEnv() (Config, error) {
	c := Config{
		ListenHost:     env("LISTEN_HOST", "127.0.0.1"),
		DBDSN:          env("DB_DSN", ""),
		NatsURL:        env("NATS_URL", "nats://127.0.0.1:4222"),
		UserPubKeys:    env("USER_JWT_PUBKEYS", ""),
		AdminPubKeys:   env("ADMIN_JWT_PUBKEYS", ""),
		ServiceKey:     env("SERVICE_KEY", ""),
		AllowedCallers: env("ALLOWED_CALLERS", ""),
		AcctURL:        env("ACCT_URL", "http://127.0.0.1:18301"),
		ChannelMode:    env("PAY_CHANNEL_MODE", "mock"),
		MockDefault:    env("PAY_MOCK_DEFAULT", "成功"),
		MockUsers:      env("PAY_MOCK_USERS", ""),
		MockResolve:    env("PAY_MOCK_UNKNOWN_RESOLVE", "成功"),
		WeChat:         map[string]string{},
	}
	p, err := strconv.Atoi(env("PORT", "18309"))
	if err != nil {
		return c, fmt.Errorf("PORT 不是数字")
	}
	c.Port = p
	// 事件保留期：EVENT_RETENTION_DAYS（默认 30）；旧变量 PAY_STREAM_MAX_AGE 保留一个版本作兜底（只在前者未设时生效）
	c.StreamMaxAge = 30 * 24 * time.Hour
	if v := env("EVENT_RETENTION_DAYS", ""); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 1 {
			return c, fmt.Errorf("EVENT_RETENTION_DAYS 应为大于 0 的整数天数")
		}
		c.StreamMaxAge = time.Duration(d) * 24 * time.Hour
	} else if v := env("PAY_STREAM_MAX_AGE", ""); v != "" {
		if c.StreamMaxAge, err = time.ParseDuration(v); err != nil {
			return c, fmt.Errorf("PAY_STREAM_MAX_AGE 格式不对：%w", err)
		}
	}
	if c.MockCallbackDelay, err = time.ParseDuration(env("PAY_MOCK_CALLBACK_DELAY", "3s")); err != nil || c.MockCallbackDelay < 0 {
		return c, fmt.Errorf("PAY_MOCK_CALLBACK_DELAY 格式不对（如 3s、500ms）")
	}
	if c.IntentTTL, err = time.ParseDuration(env("PAY_INTENT_TTL", "15m")); err != nil || c.IntentTTL <= 0 {
		return c, fmt.Errorf("PAY_INTENT_TTL 格式不对（如 15m）")
	}
	if c.DBDSN == "" {
		return c, fmt.Errorf("缺少 DB_DSN")
	}
	if c.ChannelMode != "mock" && c.ChannelMode != "real" {
		return c, fmt.Errorf("PAY_CHANNEL_MODE 只能是 mock 或 real")
	}
	for _, k := range []string{"MCHID", "CERT_SERIAL", "PRIVATE_KEY_PATH", "APIV3_KEY", "APPID", "NOTIFY_URL"} {
		for _, m := range []string{"INCOME", "DEPOSIT"} {
			name := "PAY_WECHAT_" + m + "_" + k
			c.WeChat[name] = env(name, "")
		}
	}
	c.WeChat["PAY_WECHAT_PAYSCORE_SERVICE_ID"] = env("PAY_WECHAT_PAYSCORE_SERVICE_ID", "")
	return c, nil
}

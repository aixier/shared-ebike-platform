// Package config 从环境变量读取配置。变量名见 contracts/wire.md 第 5 节与 README.md。
package config

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bike/acct/internal/token"
)

// Config 本服务配置
type Config struct {
	ListenHost string
	Port       int
	DBDSN      string
	NATSURL    string

	UserKey      ed25519.PrivateKey
	UserKid      string
	UserPubKeys  map[string]ed25519.PublicKey // 含自己的公钥与轮换期的旧公钥
	AdminPubKeys map[string]ed25519.PublicKey

	AllowedCallers map[string]string

	ExternalMode string // mock | real
	TokenTTL     time.Duration
	DeferredDays int // 先骑后验补验期限（天，截至当天 23:59:59）
	LegalAge     int // 法定驾驶年龄

	EventRetention time.Duration

	// 模拟渠道
	MockAuthDown         bool
	MockRealNameDown     bool
	MockRealNameRegistry string
	MockMismatchNames    string

	// 真实渠道
	WeChatAppID, WeChatSecret string
	AlipayAESKey              string
}

// Addr 监听地址
func (c *Config) Addr() string { return fmt.Sprintf("%s:%d", c.ListenHost, c.Port) }

// Load 读取配置；getenv 一般为 os.Getenv
func Load(getenv func(string) string) (*Config, error) {
	c := &Config{
		ListenHost:           def(getenv("LISTEN_HOST"), "127.0.0.1"),
		DBDSN:                getenv("DB_DSN"),
		NATSURL:              def(getenv("NATS_URL"), "nats://127.0.0.1:4222"),
		UserKid:              getenv("USER_JWT_KID"),
		ExternalMode:         def(getenv("ACCT_EXTERNAL_MODE"), "mock"),
		MockAuthDown:         getenv("ACCT_MOCK_AUTH_DOWN") == "true",
		MockRealNameDown:     getenv("ACCT_MOCK_REALNAME_DOWN") == "true",
		MockRealNameRegistry: getenv("ACCT_MOCK_REALNAME_REGISTRY"),
		MockMismatchNames:    getenv("ACCT_MOCK_REALNAME_MISMATCH_NAMES"),
		WeChatAppID:          getenv("ACCT_WECHAT_APPID"),
		WeChatSecret:         getenv("ACCT_WECHAT_SECRET"),
		AlipayAESKey:         getenv("ACCT_ALIPAY_AES_KEY"),
	}
	var err error
	if c.Port, err = intOr(getenv("PORT"), 18301); err != nil {
		return nil, err
	}
	ttl, err := intOr(getenv("ACCT_TOKEN_TTL_HOURS"), 168)
	if err != nil {
		return nil, err
	}
	c.TokenTTL = time.Duration(ttl) * time.Hour
	if c.DeferredDays, err = intOr(getenv("ACCT_DEFERRED_DAYS"), 7); err != nil {
		return nil, err
	}
	if c.LegalAge, err = intOr(getenv("ACCT_LEGAL_AGE"), 16); err != nil {
		return nil, err
	}
	// 事件保留期：EVENT_RETENTION_DAYS（默认 30 天）；旧变量 ACCT_EVENT_RETENTION_HOURS 仅在新变量未设时兜底
	if v := getenv("EVENT_RETENTION_DAYS"); v != "" {
		days, err := intOr(v, 30)
		if err != nil || days < 1 {
			return nil, fmt.Errorf("EVENT_RETENTION_DAYS 须为正整数：%q", v)
		}
		c.EventRetention = time.Duration(days) * 24 * time.Hour
	} else if v := getenv("ACCT_EVENT_RETENTION_HOURS"); v != "" {
		hours, err := intOr(v, 24*30)
		if err != nil || hours < 1 {
			return nil, fmt.Errorf("ACCT_EVENT_RETENTION_HOURS 须为正整数：%q", v)
		}
		c.EventRetention = time.Duration(hours) * time.Hour
	} else {
		c.EventRetention = 30 * 24 * time.Hour
	}

	if c.DBDSN == "" {
		return nil, errors.New("缺 DB_DSN")
	}
	if c.ExternalMode != "mock" && c.ExternalMode != "real" {
		return nil, fmt.Errorf("ACCT_EXTERNAL_MODE 只能是 mock 或 real：%q", c.ExternalMode)
	}
	if getenv("USER_JWT_PRIVKEY") == "" || c.UserKid == "" {
		return nil, errors.New("缺 USER_JWT_PRIVKEY 或 USER_JWT_KID")
	}
	if c.UserKey, err = token.ParsePrivateKey(getenv("USER_JWT_PRIVKEY")); err != nil {
		return nil, fmt.Errorf("USER_JWT_PRIVKEY: %w", err)
	}
	if c.UserPubKeys, err = token.ParsePublicKeys(getenv("USER_JWT_PUBKEYS")); err != nil {
		return nil, fmt.Errorf("USER_JWT_PUBKEYS: %w", err)
	}
	c.UserPubKeys[c.UserKid] = c.UserKey.Public().(ed25519.PublicKey)
	if c.AdminPubKeys, err = token.ParsePublicKeys(getenv("ADMIN_JWT_PUBKEYS")); err != nil {
		return nil, fmt.Errorf("ADMIN_JWT_PUBKEYS: %w", err)
	}
	c.AllowedCallers = map[string]string{}
	for _, p := range strings.Split(getenv("ALLOWED_CALLERS"), ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("ALLOWED_CALLERS 项格式不对：%q", p)
		}
		c.AllowedCallers[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return c, nil
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func intOr(v string, d int) (int, error) {
	if v == "" {
		return d, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("不是整数：%q", v)
	}
	return n, nil
}

// Package config 从环境变量读取本服务配置（变量名见 contracts/wire.md 第 5 节与 README）。
package config

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Windows 等无系统时区库的环境也能加载 Asia/Shanghai

	"bike/iam/internal/token"
)

// Config 是本服务全部配置。
type Config struct {
	ListenHost     string
	Port           int
	DBDSN          string
	NATSURL        string
	PrivKey        ed25519.PrivateKey
	Kid            string
	PubKeys        map[string]ed25519.PublicKey // 其余仍有效的公钥（轮换期旧钥）
	AllowedCallers map[string]string            // 调用方模块名 → 凭证
	TokenTTL       time.Duration
	InitialPwd     string // 统一初始密码；为空时用库里的初始密码哈希
	EventRetry     time.Duration
	OutboxInterval time.Duration
	StreamMaxAge   time.Duration
	Location       *time.Location
	DevSeedAdmin   bool // 本机开发开关 IAM_DEV_SEED_ADMIN=1：种入只读演示账号 admin
}

// MaxTokenTTL 是管理令牌有效期上限（contracts/wire.md 第 2 节）。
const MaxTokenTTL = 30 * time.Minute

// Addr 返回监听地址 LISTEN_HOST:PORT。
func (c Config) Addr() string { return fmt.Sprintf("%s:%d", c.ListenHost, c.Port) }

// FromEnv 读取环境变量。getenv 为空时用 os.Getenv。
func FromEnv(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	var errs []error
	c := Config{
		ListenHost:   def(getenv("LISTEN_HOST"), "127.0.0.1"),
		DBDSN:        getenv("DB_DSN"),
		NATSURL:      def(getenv("NATS_URL"), "nats://127.0.0.1:4222"),
		Kid:          getenv("ADMIN_JWT_KID"),
		InitialPwd:   getenv("INITIAL_PASSWORD"),
		DevSeedAdmin: getenv("IAM_DEV_SEED_ADMIN") == "1",
	}
	var err error
	if c.Port, err = strconv.Atoi(def(getenv("PORT"), "18315")); err != nil {
		errs = append(errs, fmt.Errorf("PORT: %w", err))
	}
	if c.DBDSN == "" {
		errs = append(errs, errors.New("缺少 DB_DSN"))
	}
	if c.Kid == "" {
		errs = append(errs, errors.New("缺少 ADMIN_JWT_KID"))
	}
	if p := getenv("ADMIN_JWT_PRIVKEY"); p == "" {
		errs = append(errs, errors.New("缺少 ADMIN_JWT_PRIVKEY"))
	} else if c.PrivKey, err = token.ParsePrivateKey(p); err != nil {
		errs = append(errs, fmt.Errorf("ADMIN_JWT_PRIVKEY: %w", err))
	}
	if c.PubKeys, err = token.ParsePublicKeys(getenv("ADMIN_JWT_PUBKEYS")); err != nil {
		errs = append(errs, fmt.Errorf("ADMIN_JWT_PUBKEYS: %w", err))
	}
	if c.AllowedCallers, err = parseCallers(getenv("ALLOWED_CALLERS")); err != nil {
		errs = append(errs, err)
	}
	c.TokenTTL = dur(getenv, "ADMIN_TOKEN_TTL", 30*time.Minute, &errs)
	if c.TokenTTL > MaxTokenTTL {
		c.TokenTTL = MaxTokenTTL // 契约：管理令牌有效期不超过 30 分钟
	}
	c.EventRetry = dur(getenv, "EVENT_RETRY", 10*time.Second, &errs)
	c.OutboxInterval = dur(getenv, "OUTBOX_INTERVAL", time.Second, &errs)
	// 事件保留期：EVENT_RETENTION_DAYS（默认 30 天）；旧变量 STREAM_MAX_AGE 仅在前者未设时兜底
	switch {
	case getenv("EVENT_RETENTION_DAYS") != "":
		days, err := strconv.Atoi(strings.TrimSpace(getenv("EVENT_RETENTION_DAYS")))
		if err != nil || days <= 0 {
			errs = append(errs, errors.New("EVENT_RETENTION_DAYS 应为正整数天数"))
			days = 30
		}
		c.StreamMaxAge = time.Duration(days) * 24 * time.Hour
	case getenv("STREAM_MAX_AGE") != "":
		c.StreamMaxAge = dur(getenv, "STREAM_MAX_AGE", 30*24*time.Hour, &errs)
	default:
		c.StreamMaxAge = 30 * 24 * time.Hour
	}
	if c.Location, err = time.LoadLocation(def(getenv("TIME_ZONE"), "Asia/Shanghai")); err != nil {
		errs = append(errs, fmt.Errorf("TIME_ZONE: %w", err))
	}
	return c, errors.Join(errs...)
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func dur(getenv func(string) string, name string, d time.Duration, errs *[]error) time.Duration {
	v := getenv(name)
	if v == "" {
		return d
	}
	x, err := time.ParseDuration(v)
	if err != nil || x <= 0 {
		*errs = append(*errs, fmt.Errorf("%s 应为正的时长，如 8h", name))
		return d
	}
	return x
}

func parseCallers(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("ALLOWED_CALLERS 项格式应为 模块=凭证")
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

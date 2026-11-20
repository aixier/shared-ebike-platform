package app

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 全部来自环境变量（名字见 contracts/wire.md 第 5 节与本模块 README）。
type Config struct {
	ListenHost     string
	Port           int
	DBDSN          string
	NatsURL        string
	AdminPubKeys   map[string]ed25519.PublicKey
	ServiceKey     string
	AllowedCallers map[string]string
	RideURL        string
	FleetURL       string

	// 本模块专有
	SecretKey           []byte              // OPGW_SECRET_KEY：加密存放运营商密钥的主密钥（base64，32 字节）
	Providers           map[string][]string // OPGW_PROVIDERS：服务商编号 → 名下运营商
	HolderNames         map[string]string   // OPGW_HOLDER_NAMES：持有方编号 → 名称
	BaseURLs            map[string]string   // OPGW_BASE_URLS：持有方编号 → 运营商侧接口地址
	IPWhitelists        map[string][]string // OPGW_IP_WHITELISTS：持有方编号 → 来源白名单（IP 或 CIDR）
	TrustedProxies      []netip.Prefix      // OPGW_TRUSTED_PROXIES：入口网关地址，只信它给的 X-Forwarded-For
	RateLimitPerMinute  int                 // OPGW_RATE_LIMIT_PER_MINUTE，默认 600
	UnreachableAfter    int                 // OPGW_UNREACHABLE_THRESHOLD：连续失败几次判不可达，默认 3
	OperatorTimeout     time.Duration       // OPGW_OPERATOR_TIMEOUT_MS，默认 3000
	SignWindowSec       int64               // 签名时间窗，规范定为 300 秒
	DeliveryRetryPeriod time.Duration       // 送达 ride 的重试扫描间隔
	EventRetentionDays  int                 // EVENT_RETENTION_DAYS：本服务事件流保留天数，默认 30
}

// LoadConfig 从环境变量读取。
func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := Config{
		ListenHost:          orDefault(getenv("LISTEN_HOST"), "127.0.0.1"),
		DBDSN:               getenv("DB_DSN"),
		NatsURL:             getenv("NATS_URL"),
		ServiceKey:          getenv("SERVICE_KEY"),
		RideURL:             getenv("RIDE_URL"),
		FleetURL:            getenv("FLEET_URL"),
		SignWindowSec:       300,
		DeliveryRetryPeriod: 5 * time.Second,
	}
	var err error
	if c.Port, err = intOr(getenv("PORT"), 18303); err != nil {
		return c, fmt.Errorf("PORT：%w", err)
	}
	if c.RateLimitPerMinute, err = intOr(getenv("OPGW_RATE_LIMIT_PER_MINUTE"), 600); err != nil {
		return c, fmt.Errorf("OPGW_RATE_LIMIT_PER_MINUTE：%w", err)
	}
	if c.UnreachableAfter, err = intOr(getenv("OPGW_UNREACHABLE_THRESHOLD"), 3); err != nil {
		return c, fmt.Errorf("OPGW_UNREACHABLE_THRESHOLD：%w", err)
	}
	if c.EventRetentionDays, err = intOr(getenv("EVENT_RETENTION_DAYS"), 30); err != nil || c.EventRetentionDays < 1 {
		return c, fmt.Errorf("EVENT_RETENTION_DAYS 须为正整数")
	}
	ms, err := intOr(getenv("OPGW_OPERATOR_TIMEOUT_MS"), 3000)
	if err != nil {
		return c, fmt.Errorf("OPGW_OPERATOR_TIMEOUT_MS：%w", err)
	}
	c.OperatorTimeout = time.Duration(ms) * time.Millisecond
	if c.AdminPubKeys, err = parsePubKeys(getenv("ADMIN_JWT_PUBKEYS")); err != nil {
		return c, fmt.Errorf("ADMIN_JWT_PUBKEYS：%w", err)
	}
	c.AllowedCallers = parseKV(getenv("ALLOWED_CALLERS"))
	c.HolderNames = parseKV(getenv("OPGW_HOLDER_NAMES"))
	c.BaseURLs = parseKV(getenv("OPGW_BASE_URLS"))
	c.Providers = parseKList(getenv("OPGW_PROVIDERS"))
	c.IPWhitelists = parseKList(getenv("OPGW_IP_WHITELISTS"))
	for _, s := range splitNonEmpty(getenv("OPGW_TRUSTED_PROXIES"), ",") {
		p, err := parsePrefix(s)
		if err != nil {
			return c, fmt.Errorf("OPGW_TRUSTED_PROXIES：%w", err)
		}
		c.TrustedProxies = append(c.TrustedProxies, p)
	}
	if k := getenv("OPGW_SECRET_KEY"); k != "" {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != 32 {
			return c, fmt.Errorf("OPGW_SECRET_KEY 须为 32 字节的 base64")
		}
		c.SecretKey = b
	}
	if c.DBDSN == "" {
		return c, fmt.Errorf("缺少 DB_DSN")
	}
	if c.SecretKey == nil {
		return c, fmt.Errorf("缺少 OPGW_SECRET_KEY")
	}
	return c, nil
}

// ProviderOf 运营商由哪家服务商代接；自行接入返回空。
func (c Config) ProviderOf(operatorID string) string {
	for sp, ops := range c.Providers {
		for _, o := range ops {
			if o == operatorID {
				return sp
			}
		}
	}
	return ""
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func intOr(v string, d int) (int, error) {
	if v == "" {
		return d, nil
	}
	return strconv.Atoi(v)
}

func splitNonEmpty(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseKV 解析 "a=1,b=2"。
func parseKV(s string) map[string]string {
	m := map[string]string{}
	for _, p := range splitNonEmpty(s, ",") {
		k, v, ok := strings.Cut(p, "=")
		if ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

// parseKList 解析 "a=x|y,b=z"。
func parseKList(s string) map[string][]string {
	m := map[string][]string{}
	for k, v := range parseKV(s) {
		m[k] = splitNonEmpty(v, "|")
	}
	return m
}

func parsePubKeys(s string) (map[string]ed25519.PublicKey, error) {
	m := map[string]ed25519.PublicKey{}
	for k, v := range parseKV(s) {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("公钥 %s 格式不对", k)
		}
		m[k] = ed25519.PublicKey(b)
	}
	return m, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

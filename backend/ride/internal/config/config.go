// Package config 读环境变量（wire.md 第 5 节，另加本服务专有的几项）。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config 本服务配置。
type Config struct {
	ListenHost      string
	Port            int
	DBDSN           string
	NATSURL         string
	UserJWTPubKeys  string
	AdminJWTPubKeys string
	ServiceKey      string
	AllowedCallers  string
	OpgwURL         string
	BillURL         string
	PayURL          string
	AcctURL         string
	DepositURL      string
	InstanceID      string
	InstanceNo      int
	JournalDir      string
	PileNoLockWait  time.Duration
	ReturnWait      time.Duration
	SnapshotRefresh time.Duration
	EventRetention  time.Duration
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// retention 事件流保留期：EVENT_RETENTION_DAYS（默认 30 天）；没配它时才看旧变量 EVENT_RETENTION_HOURS（兜底一个版本）。
func retention() time.Duration {
	if n := envInt("EVENT_RETENTION_DAYS", 0); n > 0 {
		return time.Duration(n) * 24 * time.Hour
	}
	if n := envInt("EVENT_RETENTION_HOURS", 0); n > 0 {
		return time.Duration(n) * time.Hour
	}
	return 30 * 24 * time.Hour
}

// Load 读取配置。
func Load() (Config, error) {
	host, _ := os.Hostname()
	c := Config{
		ListenHost:      env("LISTEN_HOST", "127.0.0.1"),
		Port:            envInt("PORT", 18302),
		DBDSN:           os.Getenv("DB_DSN"),
		NATSURL:         os.Getenv("NATS_URL"),
		UserJWTPubKeys:  os.Getenv("USER_JWT_PUBKEYS"),
		AdminJWTPubKeys: os.Getenv("ADMIN_JWT_PUBKEYS"),
		ServiceKey:      os.Getenv("SERVICE_KEY"),
		AllowedCallers:  os.Getenv("ALLOWED_CALLERS"),
		OpgwURL:         os.Getenv("OPGW_URL"),
		BillURL:         os.Getenv("BILL_URL"),
		PayURL:          os.Getenv("PAY_URL"),
		AcctURL:         os.Getenv("ACCT_URL"),
		DepositURL:      os.Getenv("DEPOSIT_URL"),
		InstanceID:      env("INSTANCE_ID", fmt.Sprintf("%s-%d", host, os.Getpid())),
		InstanceNo:      envInt("INSTANCE_NO", 0),
		JournalDir:      env("JOURNAL_DIR", "journal"),
		PileNoLockWait:  time.Duration(envInt("PILE_NOLOCK_WAIT_SEC", 15)) * time.Second,
		ReturnWait:      time.Duration(envInt("RETURN_WAIT_MS", 4000)) * time.Millisecond,
		SnapshotRefresh: time.Duration(envInt("SNAPSHOT_REFRESH_MS", 1000)) * time.Millisecond,
		EventRetention:  retention(),
	}
	if c.DBDSN == "" {
		return c, fmt.Errorf("缺少 DB_DSN")
	}
	if c.InstanceNo < 0 || c.InstanceNo > 9 {
		return c, fmt.Errorf("INSTANCE_NO 应为 0～9")
	}
	return c, nil
}

// Addr 监听地址 LISTEN_HOST:PORT。
func (c Config) Addr() string { return fmt.Sprintf("%s:%d", c.ListenHost, c.Port) }

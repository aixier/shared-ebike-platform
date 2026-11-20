// Package config 从环境变量读取本服务配置（contracts/wire.md 第 5 节，专有项见 README）。
package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

type Config struct {
	Port            int
	ListenHost      string
	DBDSN           string
	NatsURL         string // 空表示不连事件总线（仅本机调试）
	AdminJWTPubKeys string
	AllowedCallers  string
	PhotoDir        string        // ENFORCE_PHOTO_DIR：证据照片存放目录
	OrphanPhotoTTL  time.Duration // ENFORCE_ORPHAN_PHOTO_TTL：未被证据引用的照片保留多久
	CleanupSchedule string        // ENFORCE_CLEANUP_SCHEDULE：清理任务的 cron 表达式
	EventRetention  time.Duration // 事件流保留期限：EVENT_RETENTION_DAYS（默认 30 天）；旧变量 EVENT_RETENTION 作兜底，0 为不限
}

// Addr 返回监听地址 LISTEN_HOST:PORT。
func (c Config) Addr() string {
	return net.JoinHostPort(c.ListenHost, strconv.Itoa(c.Port))
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Port:            18314,
		ListenHost:      "127.0.0.1",
		DBDSN:           getenv("DB_DSN"),
		NatsURL:         getenv("NATS_URL"),
		AdminJWTPubKeys: getenv("ADMIN_JWT_PUBKEYS"),
		AllowedCallers:  getenv("ALLOWED_CALLERS"),
		PhotoDir:        getenv("ENFORCE_PHOTO_DIR"),
		OrphanPhotoTTL:  24 * time.Hour,
		CleanupSchedule: "@every 1h",
	}
	if v := getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 {
			return c, errors.New("PORT 不是有效端口")
		}
		c.Port = p
	}
	if v := getenv("LISTEN_HOST"); v != "" {
		c.ListenHost = v
	}
	if v := getenv("ENFORCE_CLEANUP_SCHEDULE"); v != "" {
		c.CleanupSchedule = v
	}
	if v := getenv("ENFORCE_ORPHAN_PHOTO_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return c, errors.New("ENFORCE_ORPHAN_PHOTO_TTL 不是有效时长（如 24h）")
		}
		c.OrphanPhotoTTL = d
	}
	// 事件保留期：统一变量优先，其次旧变量（保留一个版本作兜底），都没有取 30 天
	c.EventRetention = 30 * 24 * time.Hour
	if v := getenv("EVENT_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return c, errors.New("EVENT_RETENTION_DAYS 不是有效天数（非负整数）")
		}
		c.EventRetention = time.Duration(n) * 24 * time.Hour
	} else if v := getenv("EVENT_RETENTION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return c, fmt.Errorf("EVENT_RETENTION 不是有效时长（如 720h）")
		}
		c.EventRetention = d
	}
	if c.DBDSN == "" {
		return c, errors.New("缺少 DB_DSN")
	}
	if c.PhotoDir == "" {
		return c, errors.New("缺少 ENFORCE_PHOTO_DIR")
	}
	return c, nil
}

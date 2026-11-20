package config

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_DSN": "postgres://x", "ENFORCE_PHOTO_DIR": "p"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "127.0.0.1:18314" {
		t.Fatalf("默认监听地址不对：%s", c.Addr())
	}
	if c.NatsURL != "" || c.PhotoDir != "p" {
		t.Fatalf("配置不对：%+v", c)
	}
	if c.OrphanPhotoTTL != 24*time.Hour || c.CleanupSchedule != "@every 1h" || c.EventRetention != 30*24*time.Hour {
		t.Fatalf("默认值不对：%+v", c)
	}
}

func TestLoadDurations(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_DSN": "d", "ENFORCE_PHOTO_DIR": "p", "ENFORCE_ORPHAN_PHOTO_TTL": "2h", "ENFORCE_CLEANUP_SCHEDULE": "0 3 * * *", "EVENT_RETENTION": "720h"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.OrphanPhotoTTL != 2*time.Hour || c.CleanupSchedule != "0 3 * * *" || c.EventRetention != 720*time.Hour {
		t.Fatalf("配置不对：%+v", c)
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "d", "ENFORCE_PHOTO_DIR": "p", "ENFORCE_ORPHAN_PHOTO_TTL": "abc"})); err == nil {
		t.Fatal("时长格式不对应报错")
	}
}

func TestLoadEventRetentionDays(t *testing.T) {
	base := map[string]string{"DB_DSN": "d", "ENFORCE_PHOTO_DIR": "p"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	c, err := Load(env(with("EVENT_RETENTION_DAYS", "7")))
	if err != nil || c.EventRetention != 7*24*time.Hour {
		t.Fatalf("EVENT_RETENTION_DAYS=7 应为 168h：%v %v", c.EventRetention, err)
	}
	// 统一变量优先于旧变量
	c, _ = Load(env(with("EVENT_RETENTION_DAYS", "7", "EVENT_RETENTION", "1h")))
	if c.EventRetention != 7*24*time.Hour {
		t.Fatalf("统一变量应优先：%v", c.EventRetention)
	}
	// 只有旧变量时兜底
	c, _ = Load(env(with("EVENT_RETENTION", "48h")))
	if c.EventRetention != 48*time.Hour {
		t.Fatalf("旧变量应兜底：%v", c.EventRetention)
	}
	for _, bad := range []string{"abc", "-1", "1.5"} {
		if _, err := Load(env(with("EVENT_RETENTION_DAYS", bad))); err == nil {
			t.Fatalf("EVENT_RETENTION_DAYS=%s 应报错", bad)
		}
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_DSN": "d", "PORT": "9000", "LISTEN_HOST": "0.0.0.0", "NATS_URL": "nats://127.0.0.1:4222", "ENFORCE_PHOTO_DIR": "p"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "0.0.0.0:9000" || c.NatsURL != "nats://127.0.0.1:4222" {
		t.Fatalf("配置不对：%+v", c)
	}
}

func TestLoadRequired(t *testing.T) {
	if _, err := Load(env(map[string]string{"ENFORCE_PHOTO_DIR": "p"})); err == nil {
		t.Fatal("缺 DB_DSN 应报错")
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "d"})); err == nil {
		t.Fatal("缺 ENFORCE_PHOTO_DIR 应报错")
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "d", "ENFORCE_PHOTO_DIR": "p", "PORT": "abc"})); err == nil {
		t.Fatal("端口不是数字应报错")
	}
}

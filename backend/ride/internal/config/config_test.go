package config

import (
	"testing"
	"time"
)

func TestRetention(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		days, hours string
		want        time.Duration
	}{
		{"", "", 30 * day},
		{"7", "", 7 * day},
		{"", "48", 48 * time.Hour}, // 旧变量兜底
		{"7", "48", 7 * day},       // 新变量优先
		{"abc", "", 30 * day},
	}
	for _, c := range cases {
		t.Setenv("EVENT_RETENTION_DAYS", c.days)
		t.Setenv("EVENT_RETENTION_HOURS", c.hours)
		if got := retention(); got != c.want {
			t.Errorf("days=%q hours=%q：%v，应为 %v", c.days, c.hours, got, c.want)
		}
	}
}

package config

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_DSN": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "127.0.0.1:18310" {
		t.Fatalf("默认只监听本机回环 18310，得到 %s", c.Addr())
	}
	if c.CreditScorer != "mock" || c.CreditMockDefault != 650 || c.RefundWorkdaysDefault != 3 || c.DefaultCollectChannel != "微信支付" || c.StreamMaxAgeDays != 30 {
		t.Fatalf("默认值不对：%+v", c)
	}
	if _, offset := time.Date(2026, 1, 1, 0, 0, 0, 0, c.Location()).Zone(); offset != 8*3600 {
		t.Fatal("默认时区 +08:00")
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{"DB_DSN": "d", "PORT": "28310", "LISTEN_HOST": "0.0.0.0", "CREDIT_MOCK_DEFAULT": "500", "PAY_URL": "http://127.0.0.1:18309"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "0.0.0.0:28310" || c.CreditMockDefault != 500 || c.PayURL != "http://127.0.0.1:18309" {
		t.Fatalf("覆盖不对：%+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(env(map[string]string{})); err == nil {
		t.Fatal("缺 DB_DSN 应报错")
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "d", "PORT": "abc"})); err == nil {
		t.Fatal("PORT 非数字应报错")
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "d", "DEFAULT_COLLECT_CHANNEL": "现金"})); err == nil {
		t.Fatal("收单渠道只能是微信支付或支付宝")
	}
}

// trace: 第二轮 全体 2 · 事件保留期读 EVENT_RETENTION_DAYS，旧变量兜底
func TestEventRetentionDays(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want int
	}{
		{map[string]string{"DB_DSN": "d"}, 30},
		{map[string]string{"DB_DSN": "d", "STREAM_MAX_AGE_DAYS": "10"}, 10},
		{map[string]string{"DB_DSN": "d", "EVENT_RETENTION_DAYS": "90"}, 90},
		{map[string]string{"DB_DSN": "d", "EVENT_RETENTION_DAYS": "90", "STREAM_MAX_AGE_DAYS": "10"}, 90},
	} {
		got, err := Load(env(c.env))
		if err != nil || got.StreamMaxAgeDays != c.want {
			t.Fatalf("%v：得到 %d（%v），期望 %d", c.env, got.StreamMaxAgeDays, err, c.want)
		}
	}
	if _, err := Load(env(map[string]string{"DB_DSN": "d", "EVENT_RETENTION_DAYS": "0"})); err == nil {
		t.Fatal("保留天数小于 1 应报错")
	}
}

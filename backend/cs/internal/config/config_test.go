package config

import "testing"

// trace: wire.md 第 5 节 环境变量；技术约定 只监听本机回环地址
func TestLoadDefaults(t *testing.T) {
	env := map[string]string{"DB_DSN": "postgres://x"}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "127.0.0.1:18312" {
		t.Fatalf("默认监听地址应为 127.0.0.1:18312，得到 %s", c.Addr())
	}
	if c.DefaultRefundLimit != 2000 {
		t.Fatalf("默认客服退款额度 %d", c.DefaultRefundLimit)
	}
}

func TestLoadAll(t *testing.T) {
	env := map[string]string{
		"PORT": "28312", "LISTEN_HOST": "0.0.0.0", "DB_DSN": "postgres://x", "NATS_URL": "nats://127.0.0.1:4222",
		"SERVICE_KEY": "k", "ALLOWED_CALLERS": "ride=a", "RIDE_URL": "http://127.0.0.1:18302",
		"BILL_URL": "http://b", "ACCT_URL": "http://c", "DEPOSIT_URL": "http://d", "CS_DEFAULT_REFUND_LIMIT": "3000",
	}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "0.0.0.0:28312" || c.RideURL != "http://127.0.0.1:18302" || c.DepositURL != "http://d" || c.DefaultRefundLimit != 3000 || c.Callers["ride"] != "a" {
		t.Fatalf("%+v", c)
	}
}

func TestLoadRequiresDSN(t *testing.T) {
	if _, err := Load(func(string) string { return "" }); err == nil {
		t.Fatal("缺 DB_DSN 应报错")
	}
}

// trace: 第二轮变更单 全体 2
func TestEventRetentionDays(t *testing.T) {
	get := func(m map[string]string) (*Config, error) {
		m["DB_DSN"] = "postgres://x"
		return Load(func(k string) string { return m[k] })
	}
	c, err := get(map[string]string{})
	if err != nil || c.EventRetentionDays != 30 {
		t.Fatalf("默认应为 30：%v %+v", err, c)
	}
	c, err = get(map[string]string{"EVENT_RETENTION_DAYS": "90"})
	if err != nil || c.EventRetentionDays != 90 {
		t.Fatalf("%v %+v", err, c)
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		if _, err := get(map[string]string{"EVENT_RETENTION_DAYS": bad}); err == nil {
			t.Fatalf("%q 应报错", bad)
		}
	}
}

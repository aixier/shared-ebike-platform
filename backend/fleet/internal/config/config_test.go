package config

import "testing"

func TestLoad(t *testing.T) {
	env := map[string]string{
		"DB_DSN":          "postgres://x@127.0.0.1:5432/bike_fleet",
		"ALLOWED_CALLERS": "opgw=a,ops=b",
	}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "127.0.0.1:18305" {
		t.Fatalf("默认监听地址应为 127.0.0.1:18305，得到 %s", c.Addr())
	}
	if c.NatsURL != "nats://127.0.0.1:4222" || len(c.Callers) != 2 || c.EventRetentionDays != 30 {
		t.Fatalf("默认值不对: %+v", c)
	}

	if c.ExitReadonlyDays != 0 {
		t.Fatalf("EXIT_READONLY_DAYS 默认 0: %d", c.ExitReadonlyDays)
	}
	env["EXIT_READONLY_DAYS"] = "90"
	if c, _ = Load(func(k string) string { return env[k] }); c.ExitReadonlyDays != 90 {
		t.Fatalf("EXIT_READONLY_DAYS: %d", c.ExitReadonlyDays)
	}
	env["EXIT_READONLY_DAYS"] = "x"
	if _, err := Load(func(k string) string { return env[k] }); err == nil {
		t.Fatal("EXIT_READONLY_DAYS 非数字应报错")
	}
	delete(env, "EXIT_READONLY_DAYS")

	env["LISTEN_HOST"] = "0.0.0.0"
	env["PORT"] = "9000"
	c, _ = Load(func(k string) string { return env[k] })
	if c.Addr() != "0.0.0.0:9000" {
		t.Fatalf("应按 LISTEN_HOST:PORT 监听，得到 %s", c.Addr())
	}

	if _, err := Load(func(string) string { return "" }); err == nil {
		t.Fatal("缺 DB_DSN 应报错")
	}
	env["ADMIN_JWT_PUBKEYS"] = "k=bad"
	if _, err := Load(func(k string) string { return env[k] }); err == nil {
		t.Fatal("坏公钥应报错")
	}
}

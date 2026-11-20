package adapters

import "testing"

func TestEnabled(t *testing.T) {
	m, err := Enabled([]string{"acme", " "})
	if err != nil || len(m) != 1 || m["acme"].Vendor() != "acme" {
		t.Fatalf("启用 acme 失败: %v %v", m, err)
	}
	if m, _ := Enabled(nil); len(m) != 0 {
		t.Fatal("未配置时不应启用任何适配器")
	}
	if _, err := Enabled([]string{"nobody"}); err == nil {
		t.Fatal("未知厂商应报错")
	}
}

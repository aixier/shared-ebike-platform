package content

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// trace: R-36 接入规范逐项写清 M-01～M-04、时限、异常
func TestSpec(t *testing.T) {
	s := LoadSpec()
	if s.Name == "" || s.Version == "" {
		t.Fatal("规范缺名称或版本")
	}
	if _, err := time.Parse(time.RFC3339, s.PublishedAt); err != nil {
		t.Fatalf("发布时刻不是 RFC 3339: %v", err)
	}
	all := ""
	for _, sec := range s.Sections {
		if sec.No == "" || sec.Title == "" || sec.Summary == "" {
			t.Fatalf("章节不完整: %+v", sec)
		}
		if sec.Example != "" && !json.Valid([]byte(sec.Example)) {
			t.Fatalf("章节 %s 的参考报文不是 JSON", sec.No)
		}
		all += sec.Title + sec.Summary
	}
	for _, must := range []string{"M-01", "M-02", "M-03", "M-04", "30 秒", "72 小时", "SIGNATURE_INVALID", "NONCE_REPLAYED", "TIMESTAMP_EXPIRED", "CREDENTIAL_DISABLED", "VEHICLE_LIST_INVALID", "补报", "/sandbox/open/v1"} {
		if !strings.Contains(all, must) {
			t.Errorf("规范缺少 %q", must)
		}
	}
}

// trace: R-38 模拟车辆含开锁慢、断网、重复上报、入桩不上锁；模拟用户
func TestSandbox(t *testing.T) {
	s := LoadSandbox()
	if len(s.SimVehicles) != 8 || len(s.SimUsers) != 5 {
		t.Fatalf("模拟车辆 8 辆、模拟用户 5 个: %d %d", len(s.SimVehicles), len(s.SimUsers))
	}
	b := ""
	for _, v := range s.SimVehicles {
		b += v.Behavior
	}
	for _, must := range []string{"开锁慢", "断网", "重复", "入桩不上锁", "电量不足"} {
		if !strings.Contains(b, must) {
			t.Errorf("缺少模拟行为 %q", must)
		}
	}
}

// trace: R-39 18 项用例
func TestCases(t *testing.T) {
	c := LoadCases()
	if len(c) != 18 {
		t.Fatalf("应为 18 项: %d", len(c))
	}
	seen := map[string]bool{}
	groups := map[string]bool{"M-01 开锁": true, "M-02 结束骑行": true, "M-03 骑行事件": true, "M-04 车辆清单": true, "接入安全": true}
	for _, x := range c {
		if seen[x.CaseID] {
			t.Fatalf("用例编号重复 %s", x.CaseID)
		}
		seen[x.CaseID] = true
		if !groups[x.Group] {
			t.Fatalf("分组取值不在契约枚举里: %s", x.Group)
		}
	}
}

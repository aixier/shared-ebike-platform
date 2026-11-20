package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 只测不需要数据库的部分；带数据库的工具行为在 core 的测试里覆盖。
func TestSchemaListsAllTools(t *testing.T) {
	b, err := Schema()
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"read_ticket": true, "read_rules": true, "reply": true, "set_category": true, "propose_refund": true, "propose_force_end": true}
	for _, tl := range s.Tools {
		delete(want, tl.Name)
	}
	if len(want) != 0 {
		t.Fatalf("缺工具定义：%v", want)
	}
}

func TestRulesTextEmbedded(t *testing.T) {
	b, err := files.ReadFile("rules/客服规则.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, k := range []string{"版本", "BR-06", "BR-26", "反例", "待客服确认"} {
		if !strings.Contains(s, k) {
			t.Fatalf("规则文本缺少 %q", k)
		}
	}
}

func TestCallUnknownTool(t *testing.T) {
	if _, err := (Tools{}).Call(context.Background(), "refund_now", "TK1", nil); err == nil {
		t.Fatal("没有直接退款的工具")
	}
}

package idcard

import (
	"testing"
	"time"
)

// 用校验位算法生成合法证件号，避免测试数据依赖真实身份
func make18(first17 string) string { return first17 + string(CheckChar(first17)) }

func TestValid(t *testing.T) {
	ok := make18("11010519491231002")
	cases := []struct {
		id   string
		want bool
	}{
		{ok, true},
		{"11010519491231002X", ok[17] == 'X'},
		{"1101051949123100", false},          // 位数不对
		{"11010519491331002" + "1", false},   // 月份 13
		{make18("11010520230229001"), false}, // 2023 年没有 2 月 29 日
		{make18("11010520240229001"), true},  // 闰年
		{"abcdefghijklmnopqr", false},
	}
	for _, c := range cases {
		if got := Valid(c.id); got != c.want {
			t.Errorf("Valid(%q)=%v want %v", c.id, got, c.want)
		}
	}
	// 校验位错一位
	bad := []byte(ok)
	if bad[17] == '0' {
		bad[17] = '1'
	} else {
		bad[17] = '0'
	}
	if Valid(string(bad)) {
		t.Errorf("校验位错误应判无效")
	}
	// 小写 x 视同大写
	x := make18("11010519900307299")
	if x[17] == 'X' && !Valid("11010519900307299x") {
		t.Errorf("小写 x 应接受")
	}
}

func TestAgeAt(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	id := make18("11010520101201001") // 2010-12-01 生
	at := time.Date(2026, 12, 1, 12, 0, 0, 0, loc)
	if a := AgeAt(id, at); a != 16 {
		t.Errorf("生日当天满 16，得 %d", a)
	}
	if a := AgeAt(id, at.AddDate(0, 0, -1)); a != 15 {
		t.Errorf("生日前一天 15，得 %d", a)
	}
	// 按北京时间算：UTC 11-30 16:00 已是北京 12-01 00:00
	if a := AgeAt(id, time.Date(2026, 11, 30, 16, 0, 0, 0, time.UTC)); a != 16 {
		t.Errorf("应按北京时间算年龄，得 %d", a)
	}
}

func TestMask(t *testing.T) {
	if got := MaskPhone("13812345678"); got != "138****5678" {
		t.Errorf("MaskPhone=%s", got)
	}
	if got := MaskID("110105194912310021"); got != "110***********0021" {
		t.Errorf("MaskID=%s", got)
	}
	if got := MaskName("张三丰"); got != "张＊＊" {
		t.Errorf("MaskName=%s", got)
	}
	if got := MaskName("李四"); got != "李＊" {
		t.Errorf("MaskName=%s", got)
	}
	if got := MaskName(""); got != "" {
		t.Errorf("MaskName 空=%s", got)
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"张三", "阿依古丽·买买提"} {
		if !ValidName(n) {
			t.Errorf("%s 应合法", n)
		}
	}
	for _, n := range []string{"张", "Tom", "张3", ""} {
		if ValidName(n) {
			t.Errorf("%s 应不合法", n)
		}
	}
}

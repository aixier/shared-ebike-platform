package domain

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"
)

// checker 收集逐项校验错误。请求体须用 json.Decoder.UseNumber 解码，数字以 json.Number 出现。
type checker struct {
	errs []FieldError
}

func (c *checker) add(row *int, field, msg string) {
	c.errs = append(c.errs, FieldError{Row: row, Field: field, Message: msg})
}

// obj 取子对象；存在但不是对象时记错误。
func (c *checker) obj(m map[string]any, key, path, label string) (map[string]any, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return nil, false
	}
	o, ok := v.(map[string]any)
	if !ok {
		c.add(nil, path, label+"格式不正确")
		return nil, false
	}
	return o, true
}

// intRange 规定一个整数项的取值：[lo, hi]，step>1 时须为 step 的整数倍；hi<0 表示无上限。
type intRange struct {
	lo, hi, step int64
	unit         string // 单位，用于说明，如"分""分钟"
}

func (r intRange) describe() string {
	if r.hi < 0 {
		return fmt.Sprintf("不小于 %d%s", r.lo, r.unit)
	}
	return fmt.Sprintf("应在 %d～%d %s之间", r.lo, r.hi, r.unit)
}

// int 读整数项；不存在返回 nil。
func (c *checker) int(m map[string]any, key, path, label string, r intRange, row *int) *int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	n, ok := v.(json.Number)
	if !ok {
		c.add(row, path, label+"须为数字")
		return nil
	}
	i, err := n.Int64()
	if err != nil {
		if r.unit == "分" {
			c.add(row, path, label+"须为整数（单位分）")
		} else {
			c.add(row, path, label+"须为整数")
		}
		return nil
	}
	if i < r.lo || (r.hi >= 0 && i > r.hi) {
		c.add(row, path, label+r.describe())
		return nil
	}
	if r.step > 1 && i%r.step != 0 {
		c.add(row, path, fmt.Sprintf("%s须为 %d %s的整数倍", label, r.step, r.unit))
		return nil
	}
	return &i
}

// decimal 读 [lo, hi] 区间、最多 places 位小数的数。
func (c *checker) decimal(m map[string]any, key, path, label string, lo, hi float64, places int) *float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	n, ok := v.(json.Number)
	if !ok {
		c.add(nil, path, label+"须为数字")
		return nil
	}
	return c.decimalNumber(n, path, label, lo, hi, places)
}

func (c *checker) decimalNumber(n json.Number, path, label string, lo, hi float64, places int) *float64 {
	r, ok := new(big.Rat).SetString(n.String())
	if !ok {
		c.add(nil, path, label+"须为数字")
		return nil
	}
	f, _ := r.Float64()
	if f < lo || f > hi {
		c.add(nil, path, fmt.Sprintf("%s应在 %g～%g 之间", label, lo, hi))
		return nil
	}
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)))
	if !scaled.IsInt() {
		c.add(nil, path, fmt.Sprintf("%s最多 %d 位小数", label, places))
		return nil
	}
	return &f
}

// str 读字符串项；required 时不得为空白。
func (c *checker) str(m map[string]any, key, path, label string, required bool, row *int) *string {
	v, ok := m[key]
	if !ok || v == nil {
		if required {
			c.add(row, path, "请填写"+label)
		}
		return nil
	}
	s, ok := v.(string)
	if !ok {
		c.add(row, path, label+"须为文字")
		return nil
	}
	s = strings.TrimSpace(s)
	if required && s == "" {
		c.add(row, path, "请填写"+label)
		return nil
	}
	return &s
}

// CheckReason 校验操作事由：去掉首尾空白后至少 4 个字。
func CheckReason(m map[string]any) (string, bool) {
	s, _ := m["reason"].(string)
	s = strings.TrimSpace(s)
	return s, utf8.RuneCountInString(s) >= 4
}

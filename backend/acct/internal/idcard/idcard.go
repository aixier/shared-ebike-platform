// Package idcard 居民身份证号校验、年龄计算与对外脱敏。纯函数，无状态。
package idcard

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	weights   = []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checkSet  = "10X98765432"
	idPattern = regexp.MustCompile(`^\d{17}[\dX]$`)
	namePat   = regexp.MustCompile(`^[\p{Han}·]{2,20}$`)
	// 北京时间：年龄、截止日按本地日历计算
	cst = time.FixedZone("CST", 8*3600)
)

// Normalize 去空白、转大写
func Normalize(id string) string { return strings.ToUpper(strings.TrimSpace(id)) }

// CheckChar 由前 17 位算出校验位
func CheckChar(first17 string) byte {
	sum := 0
	for i := 0; i < 17; i++ {
		sum += weights[i] * int(first17[i]-'0')
	}
	return checkSet[sum%11]
}

// Valid 校验 18 位格式、校验位与出生日期真实存在
func Valid(id string) bool {
	id = Normalize(id)
	if !idPattern.MatchString(id) {
		return false
	}
	if CheckChar(id[:17]) != id[17] {
		return false
	}
	_, ok := Birth(id)
	return ok
}

// Birth 解析出生日期（北京时间零点）
func Birth(id string) (time.Time, bool) {
	id = Normalize(id)
	if len(id) != 18 {
		return time.Time{}, false
	}
	y, e1 := strconv.Atoi(id[6:10])
	m, e2 := strconv.Atoi(id[10:12])
	d, e3 := strconv.Atoi(id[12:14])
	if e1 != nil || e2 != nil || e3 != nil || y < 1900 {
		return time.Time{}, false
	}
	b := time.Date(y, time.Month(m), d, 0, 0, 0, 0, cst)
	if b.Year() != y || int(b.Month()) != m || b.Day() != d {
		return time.Time{}, false
	}
	return b, true
}

// AgeAt 在时刻 at（按北京日历）的周岁
func AgeAt(id string, at time.Time) int {
	b, ok := Birth(id)
	if !ok {
		return -1
	}
	t := at.In(cst)
	age := t.Year() - b.Year()
	if t.Month() < b.Month() || (t.Month() == b.Month() && t.Day() < b.Day()) {
		age--
	}
	return age
}

// ValidName 证件上的中文姓名：2～20 个汉字或间隔号
func ValidName(n string) bool { return namePat.MatchString(strings.TrimSpace(n)) }

// MaskPhone 138****5678
func MaskPhone(p string) string {
	if len(p) < 7 {
		return "****"
	}
	return p[:3] + "****" + p[len(p)-4:]
}

// MaskID 110***********0021
func MaskID(id string) string {
	if len(id) < 7 {
		return "***"
	}
	return id[:3] + "***********" + id[len(id)-4:]
}

// MaskName 张＊＊（全角星号，保留姓）
func MaskName(n string) string {
	r := []rune(strings.TrimSpace(n))
	if len(r) == 0 {
		return ""
	}
	k := len(r) - 1
	if k < 1 {
		k = 1
	}
	return string(r[0]) + strings.Repeat("＊", k)
}

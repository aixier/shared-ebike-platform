// Package workday 计算预计到账时间：加 n 个工作日（周一至周五，不含节假日），到账时刻为当天 18:00。
// 与原型 util.addWorkdays 口径一致。
package workday

import "time"

// Add 从 from 起加 n 个工作日，返回该日 18:00（沿用 from 的时区）。
func Add(from time.Time, n int) time.Time {
	d := from
	for left := n; left > 0; {
		d = d.AddDate(0, 0, 1)
		if w := d.Weekday(); w != time.Saturday && w != time.Sunday {
			left--
		}
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 18, 0, 0, 0, d.Location())
}

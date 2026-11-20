package workday

import (
	"testing"
	"time"
)

// trace: R-17（退押显示预计到账时间：退押时限个工作日，到账时刻 18:00）
func TestAddWorkdays(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	cases := []struct {
		name string
		from time.Time
		n    int
		want time.Time
	}{
		{"周二加3个工作日到周五", time.Date(2026, 12, 1, 12, 0, 0, 0, loc), 3, time.Date(2026, 12, 4, 18, 0, 0, 0, loc)},
		{"周五加3个工作日跨周末到周三", time.Date(2026, 12, 4, 9, 0, 0, 0, loc), 3, time.Date(2026, 12, 9, 18, 0, 0, 0, loc)},
		{"周六加1个工作日到周一", time.Date(2026, 12, 5, 9, 0, 0, 0, loc), 1, time.Date(2026, 12, 7, 18, 0, 0, 0, loc)},
		{"0个工作日为当天18点", time.Date(2026, 12, 1, 9, 0, 0, 0, loc), 0, time.Date(2026, 12, 1, 18, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Add(c.from, c.n)
			if !got.Equal(c.want) {
				t.Fatalf("Add(%v,%d)=%v，期望 %v", c.from, c.n, got, c.want)
			}
		})
	}
}

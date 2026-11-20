// Package metrics 是本模块的指标算术：P90、计费分钟、骑行费估算、可用时长占比、比率。
// 纯函数，不读库、不含业务判断；门槛与参数由调用方从规则版本与平台参数里传入。
package metrics

import (
	"math"
	"sort"
	"time"
)

// P90 用最近秩法求 90 分位；没有样本时 ok 为 false。
func P90(xs []float64) (float64, bool) {
	if len(xs) == 0 {
		return 0, false
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	rank := int(math.Ceil(0.9 * float64(len(s))))
	if rank < 1 {
		rank = 1
	}
	return s[rank-1], true
}

// BilledMinutes 计费分钟：按整分钟向上取整，最少 1 分钟（与原型 rules.billedMinutes 一致）。
func BilledMinutes(sec int64) int64 {
	if sec <= 0 {
		return 1
	}
	m := (sec + 59) / 60
	if m < 1 {
		m = 1
	}
	return m
}

// RidePrice 规则版本里的骑行价格，金额为分。
type RidePrice struct {
	StartFee     int64 `json:"startFee"`
	StartMinutes int64 `json:"startMinutes"`
	PerMinute    int64 `json:"perMinute"`
}

// RideFee 骑行费 = 起步价 + 超出起步时长的分钟 × 每分钟价（分）。
func RideFee(p RidePrice, minutes int64) int64 {
	extra := minutes - p.StartMinutes
	if extra < 0 {
		extra = 0
	}
	return p.StartFee + extra*p.PerMinute
}

// Interval 一段不可用区间；End 为零值表示尚未恢复。
type Interval struct {
	Start time.Time
	End   time.Time
}

// ReachableFraction 区间 [from,to) 内不在任何不可用区间里的时长占比。
func ReachableFraction(from, to time.Time, outs []Interval) float64 {
	total := to.Sub(from)
	if total <= 0 {
		return 1
	}
	type seg struct{ s, e time.Time }
	var segs []seg
	for _, o := range outs {
		s, e := o.Start, o.End
		if e.IsZero() || e.After(to) {
			e = to
		}
		if s.Before(from) {
			s = from
		}
		if e.After(s) {
			segs = append(segs, seg{s, e})
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].s.Before(segs[j].s) })
	var down time.Duration
	var cur *seg
	for i := range segs {
		g := segs[i]
		if cur == nil {
			cur = &seg{g.s, g.e}
			continue
		}
		if !g.s.After(cur.e) {
			if g.e.After(cur.e) {
				cur.e = g.e
			}
			continue
		}
		down += cur.e.Sub(cur.s)
		cur = &seg{g.s, g.e}
	}
	if cur != nil {
		down += cur.e.Sub(cur.s)
	}
	return Round4(float64(total-down) / float64(total))
}

// Ratio 分子/分母，保留 4 位小数；分母为 0 返回 nil。
func Ratio(num, den int64) *float64 {
	if den <= 0 {
		return nil
	}
	v := Round4(float64(num) / float64(den))
	return &v
}

// Round4 四舍五入到 4 位小数。
func Round4(v float64) float64 { return math.Round(v*10000) / 10000 }

package metrics

import (
	"testing"
	"time"
)

func TestP90NearestRank(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
		ok   bool
	}{
		{nil, 0, false},
		{[]float64{3}, 3, true},
		{[]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 9, true},
		{[]float64{10, 1, 9, 2, 8, 3, 7, 4, 6, 5, 11}, 10, true},
	}
	for _, c := range cases {
		got, ok := P90(c.in)
		if ok != c.ok || got != c.want {
			t.Fatalf("P90(%v)=%v,%v 期望 %v,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestBilledMinutesAndFee(t *testing.T) {
	if BilledMinutes(0) != 1 || BilledMinutes(60) != 1 || BilledMinutes(61) != 2 {
		t.Fatal("计费分钟按整分钟向上取整，最少 1 分钟")
	}
	p := RidePrice{StartFee: 150, StartMinutes: 15, PerMinute: 10}
	if RideFee(p, 10) != 150 {
		t.Fatal("起步时长内只收起步价")
	}
	if RideFee(p, 20) != 200 {
		t.Fatalf("超起步时长按每分钟加收，得 %d", RideFee(p, 20))
	}
}

func TestReachableFraction(t *testing.T) {
	base := time.Date(2026, 11, 30, 4, 0, 0, 0, time.UTC)
	from, to := base, base.Add(time.Hour)
	outs := []Interval{{Start: base.Add(-30 * time.Minute), End: base.Add(15 * time.Minute)}}
	if got := ReachableFraction(from, to, outs); got != 0.75 {
		t.Fatalf("不可达 15 分钟，可用率应为 0.75，得 %v", got)
	}
	open := []Interval{{Start: base.Add(30 * time.Minute)}} // 尚未恢复
	if got := ReachableFraction(from, to, open); got != 0.5 {
		t.Fatalf("未恢复的不可达算到区间末尾，得 %v", got)
	}
	if got := ReachableFraction(from, to, nil); got != 1 {
		t.Fatalf("没有不可达记录可用率为 1，得 %v", got)
	}
	// 重叠区间不重复扣
	ov := []Interval{{Start: base, End: base.Add(30 * time.Minute)}, {Start: base.Add(10 * time.Minute), End: base.Add(40 * time.Minute)}}
	if got := ReachableFraction(from, to, ov); got < 0.333 || got > 0.334 {
		t.Fatalf("重叠的不可达应合并，得 %v", got)
	}
}

func TestRatio(t *testing.T) {
	if r := Ratio(0, 0); r != nil {
		t.Fatal("分母为 0 时没有比率")
	}
	if r := Ratio(874, 1000); r == nil || *r != 0.874 {
		t.Fatal("比率保留原值")
	}
	if r := Ratio(2, 3); r == nil || *r != 0.6667 {
		t.Fatalf("比率保留 4 位小数，得 %v", *r)
	}
}

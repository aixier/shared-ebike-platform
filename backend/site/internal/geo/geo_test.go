package geo

import (
	"math"
	"testing"
)

// 原型 stations.json 里 ST0001 的围栏（已闭合）
var st0001 = []Point{{104.09559, 30.57871}, {104.09623, 30.578779}, {104.096212, 30.578905}, {104.095572, 30.578836}, {104.09559, 30.57871}}

var region = Bounds{SW: Point{104.03, 30.57}, NE: Point{104.102, 30.622}}

func TestCloseFence(t *testing.T) {
	open := []Point{{104.05, 30.58}, {104.051, 30.58}, {104.051, 30.581}}
	f, err := CloseFence(open)
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 4 || f[0] != f[3] {
		t.Fatalf("未自动闭合: %v", f)
	}
	// 已闭合的不重复加点
	f2, _ := CloseFence(st0001)
	if len(f2) != len(st0001) {
		t.Fatalf("已闭合围栏被改动: %d", len(f2))
	}
	// 顶点不足
	if _, err := CloseFence([]Point{{1, 1}, {2, 2}}); err == nil {
		t.Fatal("两个顶点应拒绝")
	}
	if _, err := CloseFence([]Point{{1, 1}, {2, 2}, {1, 1}}); err == nil {
		t.Fatal("闭合后不足 3 个不同顶点应拒绝")
	}
}

func TestCloseFenceRoundsTo6(t *testing.T) {
	f, _ := CloseFence([]Point{{104.0500004, 30.58}, {104.051, 30.58}, {104.051, 30.581}})
	if f[0][0] != 104.05 {
		t.Fatalf("应按 6 位小数取整: %v", f[0][0])
	}
}

func TestArea(t *testing.T) {
	a := AreaM2(st0001)
	// 约 62m × 14m ≈ 860 平方米，取宽松区间
	if a < 500 || a > 1500 {
		t.Fatalf("面积异常: %f", a)
	}
	tiny := []Point{{104.05, 30.58}, {104.05001, 30.58}, {104.05001, 30.58001}, {104.05, 30.58}}
	if AreaM2(tiny) >= 20 {
		t.Fatalf("极小围栏面积应小于 20: %f", AreaM2(tiny))
	}
}

func TestInside(t *testing.T) {
	if !Inside(Point{104.095645, 30.57878}, st0001) { // PL00001 的位置
		t.Fatal("PL00001 应在 ST0001 围栏内")
	}
	if Inside(Point{104.05, 30.58}, st0001) {
		t.Fatal("远处点不应在围栏内")
	}
}

func TestBounds(t *testing.T) {
	if !region.ContainsAll(st0001) {
		t.Fatal("ST0001 应在本市范围内")
	}
	if region.ContainsAll([]Point{{103.9, 30.58}, {104.05, 30.58}, {104.05, 30.59}, {103.9, 30.58}}) {
		t.Fatal("越界围栏应判为不在范围内")
	}
}

func TestCentroid(t *testing.T) {
	c := Centroid([]Point{{0, 0}, {2, 0}, {2, 2}, {0, 2}, {0, 0}})
	if math.Abs(c[0]-1) > 1e-9 || math.Abs(c[1]-1) > 1e-9 {
		t.Fatalf("中心点错误: %v", c)
	}
}

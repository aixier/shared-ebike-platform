package geo

import (
	"math"
	"testing"
)

var square = []Pt{{104.0, 30.0}, {104.001, 30.0}, {104.001, 30.001}, {104.0, 30.001}, {104.0, 30.0}}

func TestInPolygon(t *testing.T) {
	if !InPolygon(Pt{104.0005, 30.0005}, square) {
		t.Fatal("中心点应在围栏内")
	}
	if InPolygon(Pt{104.002, 30.0005}, square) {
		t.Fatal("外部点不应在围栏内")
	}
	// 未闭合的多边形也能判断
	if !InPolygon(Pt{104.0005, 30.0005}, square[:4]) {
		t.Fatal("未闭合多边形应自动闭合")
	}
}

func TestDistToPolygon(t *testing.T) {
	if d := DistToPolygon(Pt{104.0005, 30.0005}, square); d != 0 {
		t.Fatalf("内部距离应为 0，得 %v", d)
	}
	// 东边 0.001 度约 96 米
	d := DistToPolygon(Pt{104.002, 30.0005}, square)
	if math.Abs(d-96.4) > 2 {
		t.Fatalf("距离约 96 米，得 %v", d)
	}
}

func TestDistance(t *testing.T) {
	d := Distance(Pt{104.0, 30.0}, Pt{104.0, 30.001})
	if math.Abs(d-111.2) > 1 {
		t.Fatalf("纬度 0.001 度约 111 米，得 %v", d)
	}
}

func TestCentroid(t *testing.T) {
	c := Centroid(square)
	if math.Abs(c[0]-104.0005) > 1e-9 || math.Abs(c[1]-30.0005) > 1e-9 {
		t.Fatalf("质心不对 %v", c)
	}
}

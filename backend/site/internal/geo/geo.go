// Package geo 围栏几何：闭合、面积、点在多边形内、本市范围。坐标为 [经度, 纬度]（GCJ-02）。
package geo

import (
	"errors"
	"math"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
)

// Point 坐标 [经度, 纬度]
type Point [2]float64

// Bounds 本市范围：西南角与东北角
type Bounds struct {
	SW Point
	NE Point
}

// ErrTooFewVertices 围栏顶点不足 3 个
var ErrTooFewVertices = errors.New("围栏至少需要 3 个顶点")

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// CloseFence 坐标取 6 位小数，首尾不同则自动闭合；不同顶点少于 3 个返回错误。
func CloseFence(in []Point) ([]Point, error) {
	f := make([]Point, 0, len(in)+1)
	for _, p := range in {
		f = append(f, Point{round6(p[0]), round6(p[1])})
	}
	if len(f) > 0 && f[0] != f[len(f)-1] {
		f = append(f, f[0])
	}
	if len(f) < 4 {
		return nil, ErrTooFewVertices
	}
	return f, nil
}

// AreaM2 近似面积（平方米），以首点纬度做等距投影，与原型算法一致。
func AreaM2(f []Point) float64 {
	if len(f) < 4 {
		return 0
	}
	kx := 111320 * math.Cos(f[0][1]*math.Pi/180)
	ky := 110540.0
	s := 0.0
	for i := 0; i < len(f)-1; i++ {
		s += f[i][0]*kx*f[i+1][1]*ky - f[i+1][0]*kx*f[i][1]*ky
	}
	return math.Abs(s / 2)
}

// Inside 点是否在闭合多边形内
func Inside(p Point, f []Point) bool {
	ring := make(orb.Ring, len(f))
	for i, q := range f {
		ring[i] = orb.Point{q[0], q[1]}
	}
	return planar.RingContains(ring, orb.Point{p[0], p[1]})
}

// ContainsAll 所有点都在本市范围内（含边界）
func (b Bounds) ContainsAll(pts []Point) bool {
	for _, p := range pts {
		if p[0] < b.SW[0] || p[0] > b.NE[0] || p[1] < b.SW[1] || p[1] > b.NE[1] {
			return false
		}
	}
	return true
}

// Centroid 顶点平均（不含闭合重复点），6 位小数
func Centroid(f []Point) Point {
	pts := f
	if len(f) > 1 && f[0] == f[len(f)-1] {
		pts = f[:len(f)-1]
	}
	var x, y float64
	for _, p := range pts {
		x += p[0]
		y += p[1]
	}
	n := float64(len(pts))
	return Point{round6(x / n), round6(y / n)}
}

// Package geo 围栏与距离的纯计算（坐标 [经度, 纬度]，GCJ-02，按小范围近似）。
package geo

import (
	"math"

	"github.com/paulmach/orb"
	orbgeo "github.com/paulmach/orb/geo"
	"github.com/paulmach/orb/planar"
)

// Pt 坐标 [经度, 纬度]。
type Pt [2]float64

func toOrb(p Pt) orb.Point { return orb.Point{p[0], p[1]} }

// Distance 两点球面距离（米）。
func Distance(a, b Pt) float64 { return orbgeo.Distance(toOrb(a), toOrb(b)) }

// InPolygon 点是否在闭合多边形内。
func InPolygon(p Pt, fence []Pt) bool {
	if len(fence) < 3 {
		return false
	}
	ring := make(orb.Ring, 0, len(fence)+1)
	for _, q := range fence {
		ring = append(ring, toOrb(q))
	}
	if !ring.Closed() {
		ring = append(ring, ring[0])
	}
	return planar.PolygonContains(orb.Polygon{ring}, toOrb(p))
}

// DistToPolygon 点到多边形边界的距离（米），在内为 0。按点所在纬度做局部平面投影。
func DistToPolygon(p Pt, fence []Pt) float64 {
	if len(fence) == 0 {
		return math.Inf(1)
	}
	if InPolygon(p, fence) {
		return 0
	}
	kx := 111320 * math.Cos(p[1]*math.Pi/180)
	ky := 110540.0
	proj := func(q Pt) (float64, float64) { return (q[0] - p[0]) * kx, (q[1] - p[1]) * ky }
	best := math.Inf(1)
	n := len(fence)
	for i := 0; i < n; i++ {
		ax, ay := proj(fence[i])
		bx, by := proj(fence[(i+1)%n])
		dx, dy := bx-ax, by-ay
		den := dx*dx + dy*dy
		t := 0.0
		if den > 0 {
			t = math.Max(0, math.Min(1, -(ax*dx+ay*dy)/den))
		}
		d := math.Hypot(ax+t*dx, ay+t*dy)
		if d < best {
			best = d
		}
	}
	return best
}

// Centroid 多边形顶点平均（用作站点内一点的兜底）。
func Centroid(fence []Pt) Pt {
	if len(fence) == 0 {
		return Pt{}
	}
	var sx, sy float64
	pts := fence
	if len(pts) > 1 && pts[0] == pts[len(pts)-1] {
		pts = pts[:len(pts)-1]
	}
	for _, q := range pts {
		sx += q[0]
		sy += q[1]
	}
	return Pt{sx / float64(len(pts)), sy / float64(len(pts))}
}

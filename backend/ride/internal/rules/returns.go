package rules

import (
	"math"

	"bike/ride/internal/geo"
)

// 在点判定依据（契约 InStationBasis）。
const (
	BasisPile    = "桩识别"
	BasisVehicle = "运营商车端"
	BasisFence   = "围栏"
	BasisOutside = "站外"
)

// PileHit 桩识别到本车在位。
type PileHit struct {
	PileID    string
	StationID string
}

// ReturnInput 在点判定输入。
type ReturnInput struct {
	Loc         *geo.Pt // 锁车位置（或用户所在位置）
	PileHit     *PileHit
	VehicleSide *bool // 运营商车端在点判定
	VehicleID   string
	Stations    []Station
	Piles       []Pile
}

// NearestHit 最近站点。
type NearestHit struct {
	Station   Station
	DistanceM int
}

// ReturnDecision 在点判定结果。
type ReturnDecision struct {
	InStation            bool
	Basis                string
	Station              *Station
	Pile                 *Pile
	DistanceToStationM   *int
	Nearest              *NearestHit
	VehicleSideInStation *bool
}

// stationDisabled 站点停用（site 现发「停用」，兼容旧值「已停用」）。
func stationDisabled(st string) bool { return st == "停用" || st == "已停用" }

// StationAt 位置所在的启用站点。
func StationAt(stations []Station, p geo.Pt) *Station {
	for i := range stations {
		s := &stations[i]
		if stationDisabled(s.Status) {
			continue
		}
		if geo.InPolygon(p, s.Fence) {
			return s
		}
	}
	return nil
}

// Nearest 离位置最近的启用站点（到围栏的距离）。
func Nearest(stations []Station, p geo.Pt) *NearestHit {
	var best *NearestHit
	bd := math.Inf(1)
	for i := range stations {
		s := stations[i]
		if stationDisabled(s.Status) {
			continue
		}
		d := geo.DistToPolygon(p, s.Fence)
		if d < bd {
			bd = d
			best = &NearestHit{Station: s, DistanceM: int(math.Round(d))}
		}
	}
	return best
}

// FreePiles 站点内在线且空闲的桩（被 self 自己占的算空闲）。
func FreePiles(piles []Pile, stationID, self string) []Pile {
	var out []Pile
	for _, p := range piles {
		if p.StationID == stationID && p.Status == "在线" && (p.OccupiedBy == "" || (self != "" && p.OccupiedBy == self)) {
			out = append(out, p)
		}
	}
	return out
}

// DecideInStation 按 BR-08～BR-13 判定是否在点：
// 桩识别在位为准（与车端冲突也以桩为准）；无桩站点、桩故障或离线以车端为准；桩满时围栏内算在点；其余按围栏。
func DecideInStation(in ReturnInput) ReturnDecision {
	var near *NearestHit
	if in.Loc != nil {
		near = Nearest(in.Stations, *in.Loc)
	}
	if in.PileHit != nil {
		var pile *Pile
		for i := range in.Piles {
			if in.Piles[i].PileID == in.PileHit.PileID {
				p := in.Piles[i]
				pile = &p
			}
		}
		var st *Station
		for i := range in.Stations {
			if in.Stations[i].StationID == in.PileHit.StationID {
				s := in.Stations[i]
				st = &s
			}
		}
		d := ReturnDecision{InStation: true, Basis: BasisPile, Station: st, Pile: pile, Nearest: near}
		if in.VehicleSide != nil && !*in.VehicleSide {
			f := false
			d.VehicleSideInStation = &f
		}
		return d
	}
	outside := func() ReturnDecision {
		d := ReturnDecision{InStation: false, Basis: BasisOutside, Nearest: near}
		if near != nil {
			m := near.DistanceM
			d.DistanceToStationM = &m
		}
		return d
	}
	if in.Loc == nil {
		return outside()
	}
	st := StationAt(in.Stations, *in.Loc)
	if st == nil {
		return outside()
	}
	vehicleSays := func(basis string) ReturnDecision {
		if in.VehicleSide != nil && !*in.VehicleSide {
			return outside()
		}
		s := *st
		return ReturnDecision{InStation: true, Basis: basis, Station: &s, Nearest: near}
	}
	if st.Type == "无桩" {
		return vehicleSays(BasisVehicle)
	}
	if len(FreePiles(in.Piles, st.StationID, in.VehicleID)) > 0 {
		return vehicleSays(BasisVehicle)
	}
	for _, p := range in.Piles {
		if p.StationID == st.StationID && p.Status != "在线" {
			return vehicleSays(BasisVehicle)
		}
	}
	// 桩满：车在站点围栏内桩旁锁车，按在点（BR-10）
	s := *st
	return ReturnDecision{InStation: true, Basis: BasisFence, Station: &s, Nearest: near}
}

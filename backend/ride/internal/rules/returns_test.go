package rules

import (
	"testing"

	"bike/ride/internal/geo"
)

// 两个站点：ST0001 有桩（两个桩），ST0002 无桩；相距约 1 公里。
func fixture() ([]Station, []Pile) {
	sq := func(x, y float64) []geo.Pt {
		return []geo.Pt{{x, y}, {x + 0.001, y}, {x + 0.001, y + 0.001}, {x, y + 0.001}, {x, y}}
	}
	st := []Station{
		{StationID: "ST0001", Name: "天府广场东", Type: "有桩", Center: geo.Pt{104.0005, 30.0005}, Fence: sq(104.0, 30.0), Status: "启用"},
		{StationID: "ST0002", Name: "春熙路", Type: "无桩", Center: geo.Pt{104.0105, 30.0005}, Fence: sq(104.01, 30.0), Status: "启用"},
		{StationID: "ST0009", Name: "已停用站", Type: "无桩", Center: geo.Pt{104.0205, 30.0005}, Fence: sq(104.02, 30.0), Status: "已停用"},
		{StationID: "ST0010", Name: "停用站", Type: "无桩", Center: geo.Pt{104.0305, 30.0005}, Fence: sq(104.03, 30.0), Status: "停用"},
	}
	piles := []Pile{
		{PileID: "PL00001", StationID: "ST0001", No: 1, Location: geo.Pt{104.0002, 30.0002}, Status: "在线"},
		{PileID: "PL00002", StationID: "ST0001", No: 2, Location: geo.Pt{104.0004, 30.0002}, Status: "在线"},
	}
	return st, piles
}

func bp(b bool) *bool { return &b }

func TestDecideInStation(t *testing.T) {
	st, piles := fixture()
	in := geo.Pt{104.0005, 30.0005}
	out := geo.Pt{104.0005, 30.0025} // ST0001 北边约 166 米
	full := []Pile{piles[0], piles[1]}
	full[0].OccupiedBy, full[1].OccupiedBy = "VH000001", "VH000002"
	broken := []Pile{piles[0], piles[1]}
	broken[0].OccupiedBy, broken[1].Status = "VH000001", "故障"
	cases := []struct {
		name    string
		in      ReturnInput
		inSt    bool
		basis   string
		station string
	}{
		{"桩识别（BR-08）", ReturnInput{Loc: &in, PileHit: &PileHit{PileID: "PL00001", StationID: "ST0001"}, Stations: st, Piles: piles}, true, "桩识别", "ST0001"},
		{"桩与车端冲突以桩为准（BR-13）", ReturnInput{Loc: &out, PileHit: &PileHit{PileID: "PL00001", StationID: "ST0001"}, VehicleSide: bp(false), Stations: st, Piles: piles}, true, "桩识别", "ST0001"},
		{"有空桩、车端判定", ReturnInput{Loc: &in, Stations: st, Piles: piles}, true, "运营商车端", "ST0001"},
		{"桩满围栏内（BR-10）", ReturnInput{Loc: &in, Stations: st, Piles: full}, true, "围栏", "ST0001"},
		{"桩故障车端判定（BR-12）", ReturnInput{Loc: &in, Stations: st, Piles: broken}, true, "运营商车端", "ST0001"},
		{"桩故障车端说不在点", ReturnInput{Loc: &in, VehicleSide: bp(false), Stations: st, Piles: broken}, false, "站外", ""},
		{"无桩站点", ReturnInput{Loc: &geo.Pt{104.0105, 30.0005}, Stations: st, Piles: piles}, true, "运营商车端", "ST0002"},
		{"站外（BR-11）", ReturnInput{Loc: &out, Stations: st, Piles: piles}, false, "站外", ""},
		{"已停用站不参与判定", ReturnInput{Loc: &geo.Pt{104.0205, 30.0005}, Stations: st, Piles: piles}, false, "站外", ""},
		{"停用站不参与判定", ReturnInput{Loc: &geo.Pt{104.0305, 30.0005}, Stations: st, Piles: piles}, false, "站外", ""},
		{"没有位置按站外", ReturnInput{Stations: st, Piles: piles}, false, "站外", ""},
	}
	for _, c := range cases {
		d := DecideInStation(c.in)
		sid := ""
		if d.Station != nil {
			sid = d.Station.StationID
		}
		if d.InStation != c.inSt || d.Basis != c.basis || sid != c.station {
			t.Errorf("%s：得 inStation=%v basis=%s station=%s", c.name, d.InStation, d.Basis, sid)
		}
	}
	d := DecideInStation(ReturnInput{Loc: &out, Stations: st, Piles: piles})
	if d.DistanceToStationM == nil || *d.DistanceToStationM < 150 || *d.DistanceToStationM > 180 || d.Nearest == nil || d.Nearest.Station.StationID != "ST0001" {
		t.Errorf("站外应给最近站点与距离：%+v", d)
	}
	d = DecideInStation(ReturnInput{Loc: &out, PileHit: &PileHit{PileID: "PL00001", StationID: "ST0001"}, VehicleSide: bp(false), Stations: st, Piles: piles})
	if d.VehicleSideInStation == nil || *d.VehicleSideInStation || d.Pile == nil || d.Pile.PileID != "PL00001" {
		t.Errorf("冲突单要记车端判定结果与桩：%+v", d)
	}
}

func TestFreePiles(t *testing.T) {
	_, piles := fixture()
	piles[0].OccupiedBy = "VH000009"
	if n := len(FreePiles(piles, "ST0001", "")); n != 1 {
		t.Fatalf("空闲桩应为 1，得 %d", n)
	}
	if n := len(FreePiles(piles, "ST0001", "VH000009")); n != 2 {
		t.Fatalf("本车占的桩算空闲，得 %d", n)
	}
}

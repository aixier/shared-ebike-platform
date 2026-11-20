package app

import (
	"context"
	_ "embed"
	"encoding/json"
	"testing"
	"time"

	"bike/site/internal/geo"
)

// 原型 stations.json、vehicles.json 中 ST0001～ST0004 及相关车辆的样例（任务 14）
//
//go:embed testdata/proto_sample.json
var protoSample []byte

type sample struct {
	Stations []struct {
		StationID, Name, District, Type string
		Center                          geo.Point
		Fence                           []geo.Point
		Capacity                        int
		CreatedAt, UpdatedAt, UpdatedBy string
	}
	Piles []struct {
		PileID, StationID, Status    string
		No                           int
		Location                     geo.Point
		OccupiedVehicleID, FaultNote *string
		LastHeartbeatAt              *string
		InstalledAt                  string
	}
	Vehicles []struct {
		VehicleID, PlateNo, PileTagType, PileTag string
	}
}

var cst = time.FixedZone("CST", 8*3600)

func protoTime(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, cst)
	if err != nil {
		panic(err)
	}
	return t
}

// seedProto 把原型样例写进库：4 个站点、28 个桩、相关车辆的识别标识
func seedProto(t *testing.T) *sample {
	t.Helper()
	var s sample
	if err := json.Unmarshal(protoSample, &s); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tags := map[string]string{}
	for _, v := range s.Vehicles {
		tags[v.VehicleID] = v.PileTag
		if _, err := testPool.Exec(ctx, `INSERT INTO vehicle_tags(vehicle_id, plate_no, tag_type, tag, updated_at) VALUES ($1,$2,$3,$4,now())`, v.VehicleID, v.PlateNo, v.PileTagType, v.PileTag); err != nil {
			t.Fatal(err)
		}
	}
	for _, st := range s.Stations {
		_, err := testPool.Exec(ctx, `INSERT INTO stations(`+stationCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,'启用',$8,$9,$10,NULL,NULL)`,
			st.StationID, st.Name, st.District, st.Type, jsonText(st.Center), jsonText(st.Fence), st.Capacity,
			protoTime(st.CreatedAt), protoTime(st.UpdatedAt), jsonText(Actor{Kind: "人", ID: st.UpdatedBy}))
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range s.Piles {
		var hb *time.Time
		if p.LastHeartbeatAt != nil {
			x := protoTime(*p.LastHeartbeatAt)
			hb = &x
		}
		var tagType, tag *string
		if p.OccupiedVehicleID != nil {
			tt, tg := "RFID", tags[*p.OccupiedVehicleID]
			tagType, tag = &tt, &tg
		}
		_, err := testPool.Exec(ctx, `INSERT INTO piles(pile_id, station_id, no, location, status, manual_fault, occupied, occupied_tag_type, occupied_tag, occupied_vehicle_id, last_heartbeat_at, installed_at, fault_note)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			p.PileID, p.StationID, p.No, jsonText(p.Location), p.Status, p.Status == "故障", p.OccupiedVehicleID != nil, tagType, tag, p.OccupiedVehicleID, hb, protoTime(p.InstalledAt), p.FaultNote)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testPool.Exec(ctx, `SELECT setval('station_seq', 4); SELECT setval('pile_seq', 28)`); err != nil {
		t.Fatal(err)
	}
	return &s
}

// 查询辅助
func pileState(t *testing.T, id string) pileRow {
	t.Helper()
	p, err := scanPile(testPool.QueryRow(context.Background(), `SELECT `+pileCols+` FROM piles WHERE pile_id=$1`, id))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// freeSlot 某站点第一个在线且空闲的桩
func freeSlot(t *testing.T, station string) pileRow {
	t.Helper()
	p, err := scanPile(testPool.QueryRow(context.Background(), `SELECT `+pileCols+` FROM piles WHERE station_id=$1 AND status='在线' AND NOT occupied ORDER BY no LIMIT 1`, station))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func countRows(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/site/internal/geo"
)

// 站点状态取值（管理端接口与 site.station.changed 一致）
const (
	statusActive  = "启用"
	statusStopped = "停用"
)

// ---------- 行 ----------

type stationRow struct {
	ID, Name, District, Type string
	Center                   geo.Point
	Fence                    []geo.Point
	Capacity                 int
	Status                   string
	CreatedAt, UpdatedAt     time.Time
	UpdatedBy                *Actor
	RemovedAt                *time.Time
	RemovedBy                *Actor
}

const stationCols = `station_id, name, district, type, center, fence, capacity, status, created_at, updated_at, updated_by, removed_at, removed_by`

func scanStation(r pgx.Row) (stationRow, error) {
	var s stationRow
	err := r.Scan(&s.ID, &s.Name, &s.District, &s.Type, &s.Center, &s.Fence, &s.Capacity, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.UpdatedBy, &s.RemovedAt, &s.RemovedBy)
	return s, err
}

type pileRow struct {
	ID, StationID      string
	No                 int
	Location           geo.Point
	Status             string
	ManualFault        bool
	Occupied           bool
	TagType, Tag       *string
	VehicleID          *string
	OccupiedAt, LastHB *time.Time
	InstalledAt        time.Time
	FaultNote          *string

	// 骑行事件的临时判断（桩的报告迟到时用）：车辆已离开、车辆已回来
	LeftOrder, LeftVehicle *string
	LeftAt                 *time.Time
	BackOrder, BackVehicle *string
	BackAt                 *time.Time
	// Basis 在位情况的依据，只在读模型（loadWorld）里填；写路径看到的永远是桩自己的报告
	Basis string
}

const pileCols = `pile_id, station_id, no, location, status, manual_fault, occupied, occupied_tag_type, occupied_tag, occupied_vehicle_id, occupied_at, last_heartbeat_at, installed_at, fault_note,
	ride_left_order, ride_left_vehicle_id, ride_left_at, ride_back_order, ride_back_vehicle_id, ride_back_at`

func scanPile(r pgx.Row) (pileRow, error) {
	var p pileRow
	err := r.Scan(&p.ID, &p.StationID, &p.No, &p.Location, &p.Status, &p.ManualFault, &p.Occupied, &p.TagType, &p.Tag, &p.VehicleID, &p.OccupiedAt, &p.LastHB, &p.InstalledAt, &p.FaultNote,
		&p.LeftOrder, &p.LeftVehicle, &p.LeftAt, &p.BackOrder, &p.BackVehicle, &p.BackAt)
	return p, err
}

// 在位情况的依据
const (
	basisPile = "桩上报"
	basisLeft = "骑行事件·已离开"
	basisBack = "骑行事件·已回来"
)

// overlay 把骑行事件的临时判断叠到桩自己的报告上，得到对外给出的在位情况：
// 先处理"已离开"（记着的车已被骑走），再处理"已回来"（还车事件说车已回到该桩位）。
func (p pileRow) overlay() pileRow {
	p.Basis = basisPile
	if p.LeftVehicle != nil && p.Occupied && strEq(p.VehicleID, *p.LeftVehicle) {
		p.Occupied, p.VehicleID, p.TagType, p.Tag = false, nil, nil, nil
		p.Basis = basisLeft
	}
	if p.BackVehicle != nil && !(p.Occupied && strEq(p.VehicleID, *p.BackVehicle)) {
		v := *p.BackVehicle
		p.Occupied, p.VehicleID, p.TagType, p.Tag = true, &v, nil, nil
		p.Basis = basisBack
	}
	return p
}

func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }

// ---------- 读模型 ----------

type world struct {
	stations []stationRow
	piles    map[string][]pileRow // 按站点，桩位号升序
	riding   map[string]int       // 站点内进行中骑行数（按开锁站点）
	parked   map[string]int       // 站点内停着的车：在点还车投影 ∪ 桩在位
	plates   map[string]string    // 车辆编号 → 车号
}

func (a *App) loadWorld(ctx context.Context, q queryer) (*world, error) {
	w := &world{piles: map[string][]pileRow{}, riding: map[string]int{}, parked: map[string]int{}, plates: map[string]string{}}
	rows, err := q.Query(ctx, `SELECT `+stationCols+` FROM stations ORDER BY station_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		s, err := scanStation(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		w.stations = append(w.stations, s)
	}
	rows.Close()

	rows, err = q.Query(ctx, `SELECT `+pileCols+` FROM piles ORDER BY station_id, no`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		p, err := scanPile(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		p = p.overlay()
		w.piles[p.StationID] = append(w.piles[p.StationID], p)
	}
	rows.Close()

	if err := func() error {
		rows, err := q.Query(ctx, `SELECT start_station_id, count(*) FROM ride_orders WHERE status='骑行中' AND start_station_id IS NOT NULL GROUP BY 1`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				return err
			}
			w.riding[k] = n
		}
		return rows.Err()
	}(); err != nil {
		return nil, err
	}

	// 站点内停着的车 = 在点还车的投影 ∪ 桩在位（含骑行事件的临时判断），按（站点，车辆）去重
	type pair struct{ station, vehicle string }
	seen := map[pair]bool{}
	rows, err = q.Query(ctx, `SELECT station_id, vehicle_id FROM parked_vehicles`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x pair
		if err := rows.Scan(&x.station, &x.vehicle); err != nil {
			rows.Close()
			return nil, err
		}
		seen[x] = true
	}
	rows.Close()
	var vehicles []string
	for st, ps := range w.piles {
		for _, p := range ps {
			if p.Occupied && p.VehicleID != nil {
				seen[pair{st, *p.VehicleID}] = true
				vehicles = append(vehicles, *p.VehicleID)
			}
		}
	}
	for x := range seen {
		w.parked[x.station]++
	}
	if len(vehicles) > 0 {
		rows, err = q.Query(ctx, `SELECT vehicle_id, coalesce(plate_no,'') FROM vehicle_tags WHERE vehicle_id = ANY($1)`, vehicles)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, plate string
			if err := rows.Scan(&id, &plate); err != nil {
				rows.Close()
				return nil, err
			}
			w.plates[id] = plate
		}
		rows.Close()
	}
	return w, nil
}

func (w *world) station(id string) (stationRow, bool) {
	for _, s := range w.stations {
		if s.ID == id {
			return s, true
		}
	}
	return stationRow{}, false
}

// ---------- 视图 ----------

// StationView 管理端站点（swagger Station）
type StationView struct {
	StationID      string      `json:"stationId"`
	Name           string      `json:"name"`
	District       string      `json:"district"`
	Type           string      `json:"type"`
	Center         geo.Point   `json:"center"`
	Fence          []geo.Point `json:"fence"`
	Capacity       int         `json:"capacity"`
	Status         string      `json:"status"`
	PileIDs        []string    `json:"pileIds"`
	PileCount      int         `json:"pileCount"`
	OnlinePiles    int         `json:"onlinePiles"`
	FreePiles      int         `json:"freePiles"`
	FaultPiles     int         `json:"faultPiles"`
	ParkedVehicles int         `json:"parkedVehicles"`
	FullNow        bool        `json:"fullNow"`
	CreatedAt      string      `json:"createdAt"`
	UpdatedAt      string      `json:"updatedAt"`
	UpdatedBy      *Actor      `json:"updatedBy"`
	RemovedAt      *string     `json:"removedAt"`
	RemovedBy      *Actor      `json:"removedBy"`
	riding         int
}

// PileView 桩（swagger Pile）
type PileView struct {
	PileID            string    `json:"pileId"`
	StationID         string    `json:"stationId"`
	StationName       string    `json:"stationName"`
	No                int       `json:"no"`
	Location          geo.Point `json:"location"`
	Status            string    `json:"status"`
	OccupiedVehicleID *string   `json:"occupiedVehicleId"`
	OccupiedPlateNo   *string   `json:"occupiedPlateNo"`
	PresenceBasis     string    `json:"presenceBasis"`
	LastHeartbeatAt   *string   `json:"lastHeartbeatAt"`
	InstalledAt       string    `json:"installedAt"`
	FaultNote         *string   `json:"faultNote"`
}

// 空闲桩位：在线且无车在位（含识别不出的车）
func free(p pileRow) bool { return p.Status == "在线" && !p.Occupied }

func (a *App) stationView(w *world, s stationRow) StationView {
	ps := w.piles[s.ID]
	v := StationView{StationID: s.ID, Name: s.Name, District: s.District, Type: s.Type, Center: s.Center, Fence: s.Fence,
		Capacity: s.Capacity, Status: s.Status, PileIDs: []string{}, PileCount: len(ps), ParkedVehicles: w.parked[s.ID],
		CreatedAt: a.fmtTime(s.CreatedAt), UpdatedAt: a.fmtTime(s.UpdatedAt), UpdatedBy: s.UpdatedBy,
		RemovedAt: a.fmtTimePtr(s.RemovedAt), RemovedBy: s.RemovedBy, riding: w.riding[s.ID]}
	for _, p := range ps {
		v.PileIDs = append(v.PileIDs, p.ID)
		if p.Status == "在线" {
			v.OnlinePiles++
		} else {
			v.FaultPiles++
		}
		if free(p) {
			v.FreePiles++
		}
	}
	v.FullNow = s.Type == "有桩" && len(ps) > 0 && v.FreePiles == 0
	return v
}

func (a *App) pileView(w *world, p pileRow, stationName string) PileView {
	v := PileView{PileID: p.ID, StationID: p.StationID, StationName: stationName, No: p.No, Location: p.Location, Status: p.Status,
		OccupiedVehicleID: p.VehicleID, PresenceBasis: p.Basis, LastHeartbeatAt: a.fmtTimePtr(p.LastHB), InstalledAt: a.fmtTime(p.InstalledAt), FaultNote: p.FaultNote}
	if p.VehicleID != nil {
		if plate, ok := w.plates[*p.VehicleID]; ok && plate != "" {
			v.OccupiedPlateNo = &plate
		}
	}
	return v
}

// ---------- 查询 ----------

type pageQuery struct {
	Page, PageSize int
	SortKey        string
	Desc           bool
}

func paginate[T any](rows []T, q pageQuery) map[string]any {
	total := len(rows)
	start := (q.Page - 1) * q.PageSize
	if start > total {
		start = total
	}
	end := min(start+q.PageSize, total)
	out := rows[start:end]
	if out == nil {
		out = []T{}
	}
	return map[string]any{"rows": out, "total": total, "page": q.Page, "pageSize": q.PageSize}
}

// sortBy 按白名单字段排序；未知字段返回 INVALID
func sortBy[T any](rows []T, q pageQuery, keys map[string]func(T) any, def string) error {
	key := q.SortKey
	if key == "" {
		key = def
	}
	get, ok := keys[key]
	if !ok {
		return fail("INVALID", "不支持按 "+q.SortKey+" 排序")
	}
	sort.SliceStable(rows, func(i, j int) bool {
		c := compare(get(rows[i]), get(rows[j]))
		if q.Desc {
			return c > 0
		}
		return c < 0
	})
	return nil
}

func compare(x, y any) int {
	switch a := x.(type) {
	case int:
		b := y.(int)
		return a - b
	case string:
		return strings.Compare(a, y.(string))
	case bool:
		b := y.(bool)
		if a == b {
			return 0
		}
		if !a {
			return -1
		}
		return 1
	}
	return 0
}

var stationSortKeys = map[string]func(StationView) any{
	"stationId": func(v StationView) any { return v.StationID }, "name": func(v StationView) any { return v.Name },
	"district": func(v StationView) any { return v.District }, "type": func(v StationView) any { return v.Type },
	"capacity": func(v StationView) any { return v.Capacity }, "status": func(v StationView) any { return v.Status },
	"pileCount": func(v StationView) any { return v.PileCount }, "onlinePiles": func(v StationView) any { return v.OnlinePiles },
	"freePiles": func(v StationView) any { return v.FreePiles }, "faultPiles": func(v StationView) any { return v.FaultPiles },
	"parkedVehicles": func(v StationView) any { return v.ParkedVehicles }, "fullNow": func(v StationView) any { return v.FullNow },
	"createdAt": func(v StationView) any { return v.CreatedAt }, "updatedAt": func(v StationView) any { return v.UpdatedAt },
}

var pileSortKeys = map[string]func(PileView) any{
	"pileId": func(v PileView) any { return v.PileID }, "stationId": func(v PileView) any { return v.StationID },
	"stationName": func(v PileView) any { return v.StationName }, "no": func(v PileView) any { return v.No },
	"status": func(v PileView) any { return v.Status }, "installedAt": func(v PileView) any { return v.InstalledAt },
	"lastHeartbeatAt": func(v PileView) any {
		if v.LastHeartbeatAt == nil {
			return ""
		}
		return *v.LastHeartbeatAt
	},
}

type stationFilter struct {
	District, Type, Q string
	IncludeRemoved    bool
}

// ListStations 站点列表（默认不含停用）
func (a *App) ListStations(ctx context.Context, f stationFilter, q pageQuery) (map[string]any, error) {
	w, err := a.loadWorld(ctx, a.db)
	if err != nil {
		return nil, err
	}
	var rows []StationView
	for _, s := range w.stations {
		if (!f.IncludeRemoved && s.Status == statusStopped) || (f.District != "" && s.District != f.District) || (f.Type != "" && s.Type != f.Type) ||
			(f.Q != "" && !strings.Contains(s.Name, f.Q) && !strings.Contains(s.ID, f.Q)) {
			continue
		}
		rows = append(rows, a.stationView(w, s))
	}
	if err := sortBy(rows, q, stationSortKeys, "stationId"); err != nil {
		return nil, err
	}
	out := paginate(rows, q)
	out["region"] = a.cfg.Region
	return out, nil
}

// AllStations 全部未停用站点（管理端地图）
func (a *App) AllStations(ctx context.Context) (map[string]any, error) {
	w, err := a.loadWorld(ctx, a.db)
	if err != nil {
		return nil, err
	}
	out := []StationView{}
	for _, s := range w.stations {
		if s.Status != statusStopped {
			out = append(out, a.stationView(w, s))
		}
	}
	return map[string]any{"region": a.cfg.Region, "stations": out}, nil
}

// StationDetail 站点与桩
type StationDetail struct {
	StationView
	Piles []PileView `json:"piles"`
}

// GetStation 站点详情（含停用）
func (a *App) GetStation(ctx context.Context, id string) (StationDetail, error) {
	w, err := a.loadWorld(ctx, a.db)
	if err != nil {
		return StationDetail{}, err
	}
	s, ok := w.station(id)
	if !ok {
		return StationDetail{}, fail("NOT_FOUND", "没有找到站点 "+id)
	}
	d := StationDetail{StationView: a.stationView(w, s), Piles: []PileView{}}
	for _, p := range w.piles[id] {
		d.Piles = append(d.Piles, a.pileView(w, p, s.Name))
	}
	return d, nil
}

// ListPiles 桩列表
func (a *App) ListPiles(ctx context.Context, stationID, status string, q pageQuery) (map[string]any, error) {
	w, err := a.loadWorld(ctx, a.db)
	if err != nil {
		return nil, err
	}
	var rows []PileView
	for _, s := range w.stations {
		if stationID != "" && s.ID != stationID {
			continue
		}
		for _, p := range w.piles[s.ID] {
			if status == "" || p.Status == status {
				rows = append(rows, a.pileView(w, p, s.Name))
			}
		}
	}
	if err := sortBy(rows, q, pileSortKeys, "pileId"); err != nil {
		return nil, err
	}
	return paginate(rows, q), nil
}

// MapStation、MapPile 地图用
type MapStation struct {
	StationID string      `json:"stationId"`
	Name      string      `json:"name"`
	District  string      `json:"district"`
	Type      string      `json:"type"`
	Center    geo.Point   `json:"center"`
	Fence     []geo.Point `json:"fence"`
	Capacity  int         `json:"capacity"`
	FreePiles int         `json:"freePiles"`
	PileCount int         `json:"pileCount"`
}

type MapPile struct {
	PileID    string    `json:"pileId"`
	StationID string    `json:"stationId"`
	No        int       `json:"no"`
	Location  geo.Point `json:"location"`
	Status    string    `json:"status"`
	Occupied  bool      `json:"occupied"`
}

// StationMap 扫码页与地图：停用站点及其桩不返回
func (a *App) StationMap(ctx context.Context) (map[string]any, error) {
	w, err := a.loadWorld(ctx, a.db)
	if err != nil {
		return nil, err
	}
	st, ps := []MapStation{}, []MapPile{}
	for _, s := range w.stations {
		if s.Status == statusStopped {
			continue
		}
		v := a.stationView(w, s)
		st = append(st, MapStation{StationID: s.ID, Name: s.Name, District: s.District, Type: s.Type, Center: s.Center, Fence: s.Fence, Capacity: s.Capacity, FreePiles: v.FreePiles, PileCount: v.PileCount})
		for _, p := range w.piles[s.ID] {
			ps = append(ps, MapPile{PileID: p.ID, StationID: p.StationID, No: p.No, Location: p.Location, Status: p.Status, Occupied: p.Occupied})
		}
	}
	return map[string]any{"region": a.cfg.Region, "stations": st, "piles": ps}, nil
}

// ---------- 命令 ----------

// checkFence 闭合、在本市范围内、面积不小于下限
func (a *App) checkFence(in [][]float64) ([]geo.Point, error) {
	pts := make([]geo.Point, 0, len(in))
	for _, p := range in {
		if len(p) != 2 || math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) {
			return nil, fail("INVALID", "围栏顶点应为 [经度, 纬度]")
		}
		pts = append(pts, geo.Point{p[0], p[1]})
	}
	f, err := geo.CloseFence(pts)
	if err != nil {
		return nil, fail("INVALID", "围栏至少需要 3 个顶点")
	}
	b := geo.Bounds{SW: a.cfg.Region.Bounds[0], NE: a.cfg.Region.Bounds[1]}
	if !b.ContainsAll(f) {
		return nil, fail("INVALID", "围栏超出本市范围")
	}
	if geo.AreaM2(f) < a.cfg.FenceMinAreaM2 {
		return nil, fail("INVALID", "围栏面积过小，请重新绘制")
	}
	return f, nil
}

// stationPayload site.station.changed 载荷；状态取值 启用｜停用，与管理端接口一致
func stationPayload(s stationRow) map[string]any {
	st := s.Status
	return map[string]any{"stationId": s.ID, "name": s.Name, "district": s.District, "type": s.Type, "center": s.Center,
		"fence": s.Fence, "capacity": s.Capacity, "status": st}
}

type stationCreate struct {
	Name     string      `json:"name"`
	District string      `json:"district"`
	Type     string      `json:"type"`
	Fence    [][]float64 `json:"fence"`
	Capacity *int        `json:"capacity"`
	Reason   string      `json:"reason"`
}

// CreateStation 新建站点
func (a *App) CreateStation(ctx context.Context, actor Actor, in stationCreate) (StationView, error) {
	reason, err := checkReason(in.Reason)
	if err != nil {
		return StationView{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return StationView{}, fail("INVALID", "请填写站点名称")
	}
	if !slices.Contains(a.cfg.Districts, in.District) {
		return StationView{}, fail("INVALID", "请选择所在区")
	}
	typ := in.Type
	if typ == "" {
		typ = "无桩"
	}
	if typ != "有桩" && typ != "无桩" {
		return StationView{}, fail("INVALID", "站点类型只能是有桩或无桩")
	}
	capacity := 10
	if in.Capacity != nil {
		capacity = *in.Capacity
	}
	if capacity < 1 {
		return StationView{}, fail("INVALID", "容量至少为 1")
	}
	fence, err := a.checkFence(in.Fence)
	if err != nil {
		return StationView{}, err
	}
	now := a.now()
	var s stationRow
	err = a.tx(ctx, func(tx pgx.Tx) error {
		var n int64
		if err := tx.QueryRow(ctx, `SELECT nextval('station_seq')`).Scan(&n); err != nil {
			return err
		}
		s = stationRow{ID: fmt.Sprintf("ST%04d", n), Name: name, District: in.District, Type: typ, Center: geo.Centroid(fence), Fence: fence,
			Capacity: capacity, Status: statusActive, CreatedAt: now, UpdatedAt: now, UpdatedBy: &actor}
		if _, err := tx.Exec(ctx, `INSERT INTO stations(`+stationCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULL,NULL)`,
			s.ID, s.Name, s.District, s.Type, jsonText(s.Center), jsonText(s.Fence), s.Capacity, s.Status, now, now, jsonText(actor)); err != nil {
			return err
		}
		if err := a.emit(ctx, tx, "site.station.changed", s.ID, actor, now, stationPayload(s)); err != nil {
			return err
		}
		return a.audit(ctx, tx, actor, "新建站点", "station", s.ID, fmt.Sprintf("新建站点 %s「%s」（%s）", s.ID, s.Name, typ), reason, nil,
			map[string]any{"name": s.Name, "district": s.District, "type": s.Type, "fence": s.Fence, "capacity": s.Capacity})
	})
	if err != nil {
		return StationView{}, err
	}
	return a.stationAfterWrite(ctx, s.ID)
}

func (a *App) stationAfterWrite(ctx context.Context, id string) (StationView, error) {
	d, err := a.GetStation(ctx, id)
	return d.StationView, err
}

type stationUpdate struct {
	Name     *string     `json:"name"`
	Fence    [][]float64 `json:"fence"`
	Capacity *int        `json:"capacity"`
	Type     *string     `json:"type"`
	Reason   string      `json:"reason"`
}

// UpdateStation 调整站点；留痕只放改动的字段
func (a *App) UpdateStation(ctx context.Context, actor Actor, id string, in stationUpdate) (StationView, error) {
	reason, err := checkReason(in.Reason)
	if err != nil {
		return StationView{}, err
	}
	var fence []geo.Point
	if in.Fence != nil {
		if fence, err = a.checkFence(in.Fence); err != nil {
			return StationView{}, err
		}
	}
	err = a.tx(ctx, func(tx pgx.Tx) error {
		s, err := scanStation(tx.QueryRow(ctx, `SELECT `+stationCols+` FROM stations WHERE station_id=$1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fail("NOT_FOUND", "没有找到站点 "+id)
		}
		if err != nil {
			return err
		}
		var pileLocs []geo.Point
		rows, err := tx.Query(ctx, `SELECT location FROM piles WHERE station_id=$1`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p geo.Point
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			pileLocs = append(pileLocs, p)
		}
		rows.Close()

		before, after := map[string]any{}, map[string]any{}
		var parts []string
		if in.Name != nil {
			n := strings.TrimSpace(*in.Name)
			if n == "" {
				return fail("INVALID", "请填写站点名称")
			}
			if n != s.Name {
				before["name"], after["name"] = s.Name, n
				s.Name = n
				parts = append(parts, "名称")
			}
		}
		if fence != nil {
			// 不变量：桩的位置在所属站点围栏内
			for _, p := range pileLocs {
				if !geo.Inside(p, fence) {
					return fail("INVALID", "调整后的围栏没有覆盖站点下的全部桩，请先调整桩或重新绘制")
				}
			}
			before["fence"], after["fence"] = s.Fence, fence
			s.Fence, s.Center = fence, geo.Centroid(fence)
			parts = append(parts, "电子围栏")
		}
		if in.Capacity != nil {
			if *in.Capacity < 1 {
				return fail("INVALID", "容量至少为 1")
			}
			if *in.Capacity != s.Capacity {
				before["capacity"], after["capacity"] = s.Capacity, *in.Capacity
				s.Capacity = *in.Capacity
				parts = append(parts, "容量")
			}
		}
		if in.Type != nil {
			t := *in.Type
			if t != "有桩" && t != "无桩" {
				return fail("INVALID", "站点类型只能是有桩或无桩")
			}
			if t == "无桩" && len(pileLocs) > 0 {
				return fail("INVALID", fmt.Sprintf("站点下还有 %d 个桩，不能改为无桩", len(pileLocs)))
			}
			if t != s.Type {
				before["type"], after["type"] = s.Type, t
				s.Type = t
				parts = append(parts, "类型")
			}
		}
		now := a.now()
		if _, err := tx.Exec(ctx, `UPDATE stations SET name=$2, fence=$3, center=$4, capacity=$5, type=$6, updated_at=$7, updated_by=$8 WHERE station_id=$1`,
			id, s.Name, jsonText(s.Fence), jsonText(s.Center), s.Capacity, s.Type, now, jsonText(actor)); err != nil {
			return err
		}
		if len(parts) > 0 {
			if err := a.emit(ctx, tx, "site.station.changed", id, actor, now, stationPayload(s)); err != nil {
				return err
			}
		}
		summary := fmt.Sprintf("调整站点 %s「%s」%s", id, s.Name, strings.Join(parts, "、"))
		return a.audit(ctx, tx, actor, "调整站点", "station", id, strings.TrimSpace(summary), reason, nullMap(before), nullMap(after))
	})
	if err != nil {
		return StationView{}, err
	}
	return a.stationAfterWrite(ctx, id)
}

func nullMap(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// RemoveStation 删除站点：有桩、有进行中骑行或停着车时拒绝；只标记停用
func (a *App) RemoveStation(ctx context.Context, actor Actor, id, reasonIn string) (map[string]any, error) {
	reason, err := checkReason(reasonIn)
	if err != nil {
		return nil, err
	}
	err = a.tx(ctx, func(tx pgx.Tx) error {
		s, err := scanStation(tx.QueryRow(ctx, `SELECT `+stationCols+` FROM stations WHERE station_id=$1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fail("NOT_FOUND", "没有找到站点 "+id)
		}
		if err != nil {
			return err
		}
		if s.Status == statusStopped {
			return fail("ALREADY_DONE", "该站点已删除")
		}
		w, err := a.loadWorld(ctx, tx)
		if err != nil {
			return err
		}
		if n := len(w.piles[id]); n > 0 {
			return fail("STATION_IN_USE", fmt.Sprintf("站点下还有 %d 个桩，桩撤除并在桩管理中处理后才能删除站点", n), map[string]any{"piles": n})
		}
		if n := w.riding[id]; n > 0 {
			return fail("STATION_IN_USE", fmt.Sprintf("该站点有 %d 单进行中的骑行，结束后才能删除", n), map[string]any{"riding": n})
		}
		if n := w.parked[id]; n > 0 {
			return fail("STATION_IN_USE", fmt.Sprintf("站点内还停着 %d 辆车，运营商调走后才能删除", n), map[string]any{"parked": n})
		}
		now := a.now()
		if _, err := tx.Exec(ctx, `UPDATE stations SET status='停用', removed_at=$2, removed_by=$3, updated_at=$2, updated_by=$3 WHERE station_id=$1`, id, now, jsonText(actor)); err != nil {
			return err
		}
		s.Status = statusStopped
		if err := a.emit(ctx, tx, "site.station.changed", id, actor, now, stationPayload(s)); err != nil {
			return err
		}
		return a.audit(ctx, tx, actor, "删除站点", "station", id, fmt.Sprintf("删除站点 %s「%s」", id, s.Name), reason,
			map[string]any{"status": statusActive}, map[string]any{"status": statusStopped})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "stationId": id}, nil
}

type pileCreate struct {
	StationID string    `json:"stationId"`
	Location  []float64 `json:"location"`
	Reason    string    `json:"reason"`
}

// pileStatusPayload site.pile.status_changed 载荷
func (a *App) pileStatusPayload(p pileRow, at time.Time) map[string]any {
	return map[string]any{"pileId": p.ID, "stationId": p.StationID, "no": p.No, "location": p.Location, "status": p.Status, "changedAt": a.fmtTime(at)}
}

// CreatePile 登记桩：位置须在围栏内，桩位号顺延；无桩站点登记后变为有桩
func (a *App) CreatePile(ctx context.Context, actor Actor, in pileCreate) (PileView, error) {
	reason, err := checkReason(in.Reason)
	if err != nil {
		return PileView{}, err
	}
	if in.StationID == "" {
		return PileView{}, fail("INVALID", "请选择所属站点")
	}
	if len(in.Location) != 2 {
		return PileView{}, fail("INVALID", "桩的位置应为 [经度, 纬度]")
	}
	var pileID string
	err = a.tx(ctx, func(tx pgx.Tx) error {
		s, err := scanStation(tx.QueryRow(ctx, `SELECT `+stationCols+` FROM stations WHERE station_id=$1 FOR UPDATE`, in.StationID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fail("NOT_FOUND", "没有找到站点 "+in.StationID)
		}
		if err != nil {
			return err
		}
		if s.Status == statusStopped {
			return fail("INVALID", "该站点已删除，不能登记桩")
		}
		loc := geo.Point{math.Round(in.Location[0]*1e6) / 1e6, math.Round(in.Location[1]*1e6) / 1e6}
		if !geo.Inside(loc, s.Fence) {
			return fail("INVALID", "桩的位置须在站点围栏内")
		}
		var no int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(no),0)+1 FROM piles WHERE station_id=$1`, s.ID).Scan(&no); err != nil {
			return err
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT nextval('pile_seq')`).Scan(&n); err != nil {
			return err
		}
		now := a.now()
		p := pileRow{ID: fmt.Sprintf("PL%05d", n), StationID: s.ID, No: no, Location: loc, Status: "在线", LastHB: &now, InstalledAt: now}
		pileID = p.ID
		if _, err := tx.Exec(ctx, `INSERT INTO piles(pile_id, station_id, no, location, status, last_heartbeat_at, installed_at) VALUES ($1,$2,$3,$4,'在线',$5,$5)`,
			p.ID, p.StationID, p.No, jsonText(loc), now); err != nil {
			return err
		}
		if err := a.emit(ctx, tx, "site.pile.status_changed", p.ID, actor, now, a.pileStatusPayload(p, now)); err != nil {
			return err
		}
		if s.Type == "无桩" {
			if _, err := tx.Exec(ctx, `UPDATE stations SET type='有桩', updated_at=$2, updated_by=$3 WHERE station_id=$1`, s.ID, now, jsonText(actor)); err != nil {
				return err
			}
			s.Type = "有桩"
			if err := a.emit(ctx, tx, "site.station.changed", s.ID, actor, now, stationPayload(s)); err != nil {
				return err
			}
		}
		return a.audit(ctx, tx, actor, "登记桩", "pile", p.ID, fmt.Sprintf("站点 %s「%s」登记 %d 号桩 %s", s.ID, s.Name, no, p.ID), reason, nil,
			map[string]any{"stationId": s.ID, "no": no, "location": loc})
	})
	if err != nil {
		return PileView{}, err
	}
	return a.getPile(ctx, pileID)
}

func (a *App) getPile(ctx context.Context, id string) (PileView, error) {
	w, err := a.loadWorld(ctx, a.db)
	if err != nil {
		return PileView{}, err
	}
	for _, s := range w.stations {
		for _, p := range w.piles[s.ID] {
			if p.ID == id {
				return a.pileView(w, p, s.Name), nil
			}
		}
	}
	return PileView{}, fail("NOT_FOUND", "没有找到桩 "+id)
}

type pileStatusUpdate struct {
	Status    string `json:"status"`
	FaultNote string `json:"faultNote"`
	Reason    string `json:"reason"`
}

// SetPileStatus 人工设置桩状态；人工标为故障的桩不因心跳自动恢复
func (a *App) SetPileStatus(ctx context.Context, actor Actor, id string, in pileStatusUpdate) (PileView, error) {
	reason, err := checkReason(in.Reason)
	if err != nil {
		return PileView{}, err
	}
	if in.Status != "在线" && in.Status != "离线" && in.Status != "故障" {
		return PileView{}, fail("INVALID", "桩状态只能是在线、离线或故障")
	}
	err = a.tx(ctx, func(tx pgx.Tx) error {
		p, err := scanPile(tx.QueryRow(ctx, `SELECT `+pileCols+` FROM piles WHERE pile_id=$1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fail("NOT_FOUND", "没有找到桩 "+id)
		}
		if err != nil {
			return err
		}
		before := p.Status
		var note *string
		if in.Status != "在线" {
			n := strings.TrimSpace(in.FaultNote)
			if n == "" {
				n = reason
			}
			note = &n
		}
		if err := a.changePileStatus(ctx, tx, &p, in.Status, note, in.Status == "故障", actor); err != nil {
			return err
		}
		return a.audit(ctx, tx, actor, "设置桩状态", "pile", id, fmt.Sprintf("桩 %s 状态 %s → %s", id, before, in.Status), reason,
			map[string]any{"status": before}, map[string]any{"status": in.Status})
	})
	if err != nil {
		return PileView{}, err
	}
	return a.getPile(ctx, id)
}

// changePileStatus 改状态与说明；状态有变化才发桩状态变更事件
func (a *App) changePileStatus(ctx context.Context, tx pgx.Tx, p *pileRow, status string, note *string, manual bool, actor Actor) error {
	if _, err := tx.Exec(ctx, `UPDATE piles SET status=$2, fault_note=$3, manual_fault=$4 WHERE pile_id=$1`, p.ID, status, note, manual); err != nil {
		return err
	}
	changed := p.Status != status
	p.Status, p.FaultNote, p.ManualFault = status, note, manual
	if !changed {
		return nil
	}
	now := a.now()
	return a.emit(ctx, tx, "site.pile.status_changed", p.ID, actor, now, a.pileStatusPayload(*p, now))
}

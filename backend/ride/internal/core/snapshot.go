package core

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"bike/ride/internal/rules"
)

// DegradeCtl ops 下达的降级控制（降级事件编号、故障组件、收紧、停止）。
type DegradeCtl struct {
	Active     bool     `json:"active"`
	EventID    string   `json:"eventId"`
	Components []string `json:"components"`
	Tightened  bool     `json:"tightened"`
	Stopped    bool     `json:"stopped"`
}

// 快照种类。
const (
	kUser    = "user"
	kVehicle = "vehicle"
	kBrand   = "brand"
	kStation = "station"
	kPile    = "pile"
	kVersion = "version"
	kParams  = "params"
	kDegrade = "degrade"
)

// Snapshot 本实例内存里的准入快照。扫码与准入只读它，不同步调用任何服务；
// 数据库里的 snap 表是持久化的那一份，各实例定时增量刷新，数据库不可用时用最后一次的内容。
type Snapshot struct {
	mu       sync.RWMutex
	users    map[string]*rules.User
	vehicles map[string]*rules.Vehicle
	byGov    map[string]string
	brands   map[string]*rules.Brand
	stations map[string]*rules.Station
	piles    map[string]*rules.Pile
	versions map[string]*rules.Version
	params   rules.Params
	degrade  DegradeCtl
	lastRev  int64
	loadedAt time.Time
}

// NewSnapshot 空快照。
func NewSnapshot() *Snapshot {
	return &Snapshot{
		users: map[string]*rules.User{}, vehicles: map[string]*rules.Vehicle{}, byGov: map[string]string{},
		brands: map[string]*rules.Brand{}, stations: map[string]*rules.Station{}, piles: map[string]*rules.Pile{},
		versions: map[string]*rules.Version{}, params: rules.DefaultParams(),
	}
}

// apply 应用一条快照内容。
func (s *Snapshot) apply(kind, id string, doc []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch kind {
	case kUser:
		var v rules.User
		if json.Unmarshal(doc, &v) == nil {
			v.UserID = id
			s.users[id] = &v
		}
	case kVehicle:
		var v rules.Vehicle
		if json.Unmarshal(doc, &v) == nil {
			v.VehicleID = id
			if old, ok := s.vehicles[id]; ok && old.GovCode != v.GovCode {
				delete(s.byGov, old.GovCode)
			}
			s.vehicles[id] = &v
			if v.GovCode != "" {
				s.byGov[strings.ToUpper(v.GovCode)] = id
			}
		}
	case kBrand:
		var v rules.Brand
		if json.Unmarshal(doc, &v) == nil {
			v.BrandID = id
			s.brands[id] = &v
		}
	case kStation:
		var v rules.Station
		if json.Unmarshal(doc, &v) == nil {
			v.StationID = id
			s.stations[id] = &v
		}
	case kPile:
		var v rules.Pile
		if json.Unmarshal(doc, &v) == nil {
			v.PileID = id
			s.piles[id] = &v
		}
	case kVersion:
		var v rules.Version
		if json.Unmarshal(doc, &v) == nil {
			v.VersionID = id
			s.versions[id] = &v
		}
	case kParams:
		p := rules.DefaultParams()
		var v rules.Params
		if json.Unmarshal(doc, &v) == nil {
			if v.UnlockReportTimeoutSec > 0 {
				p.UnlockReportTimeoutSec = v.UnlockReportTimeoutSec
			}
			if v.NearbyRadiusM > 0 {
				p.NearbyRadiusM = v.NearbyRadiusM
			}
			if v.BackfillWindowHours > 0 {
				p.BackfillWindowHours = v.BackfillWindowHours
			}
		}
		s.params = p
	case kDegrade:
		var v DegradeCtl
		if json.Unmarshal(doc, &v) == nil {
			s.degrade = v
		}
	}
}

// refresh 从数据库增量刷新（带重叠窗口，防止并发提交的乱序）；full 为真时全量。
func (s *Snapshot) refresh(ctx context.Context, st *Store, full bool) error {
	s.mu.RLock()
	since := s.lastRev - 1000
	s.mu.RUnlock()
	if full || since < 0 {
		since = 0
	}
	rows, err := st.loadSnap(ctx, since)
	if err != nil {
		return err
	}
	var maxRev int64
	for _, r := range rows {
		s.apply(r.Kind, r.ID, r.Doc)
		if r.Rev > maxRev {
			maxRev = r.Rev
		}
	}
	s.mu.Lock()
	if maxRev > s.lastRev {
		s.lastRev = maxRev
	}
	s.loadedAt = time.Now()
	s.mu.Unlock()
	return nil
}

var govRe = regexp.MustCompile(`(?i)SF\d{8}`)
var vhRe = regexp.MustCompile(`^VH\d{6}$`)

// findVehicle 按政府码、含政府码的网址或车辆编号找车。
func (s *Snapshot) findVehicle(code string) *rules.Vehicle {
	code = strings.TrimSpace(code)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m := govRe.FindString(code); m != "" {
		if id, ok := s.byGov[strings.ToUpper(m)]; ok {
			v := *s.vehicles[id]
			return &v
		}
		return nil
	}
	if vhRe.MatchString(code) {
		if v, ok := s.vehicles[code]; ok {
			c := *v
			return &c
		}
	}
	return nil
}

func (s *Snapshot) vehicle(id string) *rules.Vehicle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.vehicles[id]; ok {
		c := *v
		return &c
	}
	return nil
}

func (s *Snapshot) vehicleByPlate(brandID, plate string) *rules.Vehicle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.vehicles {
		if v.PlateNo == plate && (brandID == "" || v.BrandID == brandID) {
			c := *v
			return &c
		}
	}
	return nil
}

func (s *Snapshot) allVehicles() []rules.Vehicle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]rules.Vehicle, 0, len(s.vehicles))
	for _, v := range s.vehicles {
		out = append(out, *v)
	}
	return out
}

func (s *Snapshot) brand(id string) *rules.Brand {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.brands[id]; ok {
		c := *b
		return &c
	}
	return nil
}

func (s *Snapshot) user(id string) *rules.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if u, ok := s.users[id]; ok {
		c := *u
		return &c
	}
	return nil
}

// pileOf 车辆当前所在桩（没有为 nil）。
func (s *Snapshot) pileOf(vehicleID string) *string {
	if vehicleID == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, p := range s.piles {
		if p.OccupiedBy == vehicleID {
			x := id
			return &x
		}
	}
	return nil
}

func (s *Snapshot) station(id string) *rules.Station {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.stations[id]; ok {
		c := *v
		return &c
	}
	return nil
}

func (s *Snapshot) pile(id string) *rules.Pile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.piles[id]; ok {
		c := *v
		return &c
	}
	return nil
}

// geoData 站点与桩（按编号排序，保证判定稳定）。
func (s *Snapshot) geoData() ([]rules.Station, []rules.Pile) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := make([]rules.Station, 0, len(s.stations))
	for _, v := range s.stations {
		st = append(st, *v)
	}
	ps := make([]rules.Pile, 0, len(s.piles))
	for _, v := range s.piles {
		ps = append(ps, *v)
	}
	sort.Slice(st, func(i, j int) bool { return st[i].StationID < st[j].StationID })
	sort.Slice(ps, func(i, j int) bool { return ps[i].PileID < ps[j].PileID })
	return st, ps
}

func (s *Snapshot) version(id string) *rules.Version {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.versions[id]
}

// versionAt 按时刻取有效版本。
func (s *Snapshot) versionAt(t time.Time) *rules.Version {
	s.mu.RLock()
	vs := make([]*rules.Version, 0, len(s.versions))
	for _, v := range s.versions {
		vs = append(vs, v)
	}
	s.mu.RUnlock()
	return rules.VersionAt(vs, t)
}

func (s *Snapshot) getParams() rules.Params {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.params
}

func (s *Snapshot) getDegrade() DegradeCtl {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.degrade
}

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	"bike/ride/internal/clients/opgw"
	"bike/ride/internal/geo"
	"bike/ride/internal/journal"
	"bike/ride/internal/rules"
)

// Options 服务的可调参数（来自环境变量）。
type Options struct {
	InstanceID       string
	InstanceNo       int           // 本地日志发订单号用的实例序号 0～9
	PileNoLockWait   time.Duration // 桩识别在位后多久仍无锁车报告就请求运营商结束（BR-09）
	ReturnWait       time.Duration // 页面还车等锁车报告的时长
	SnapshotRefresh  time.Duration // 内存快照增量刷新间隔
	EventRetention   time.Duration // 本服务事件流保留期限
	PollInterval     time.Duration // 开锁等报告时查库的间隔
	UnlockTimeoutMin time.Duration // 测试用：覆盖开锁报告时限（0 表示按平台参数）
}

// Service 骑行服务。
type Service struct {
	store    *Store
	snap     *Snapshot
	journal  *journal.Journal
	opgw     *opgw.Client
	breakers *Breakers
	js       jetstream.JetStream
	opt      Options
	log      *slog.Logger
	now      func() time.Time

	mu      sync.Mutex
	mem     map[string]*Order // 数据库不可用期间建立或修改、尚未回放入库的订单
	memIdem map[string][]byte
	waiters map[string][]chan struct{}
}

// New 新建服务。
func New(pool *pgxpool.Pool, snap *Snapshot, j *journal.Journal, og *opgw.Client, opt Options, log *slog.Logger) *Service {
	if opt.PollInterval == 0 {
		opt.PollInterval = 200 * time.Millisecond
	}
	if opt.ReturnWait == 0 {
		opt.ReturnWait = 4 * time.Second
	}
	if opt.PileNoLockWait == 0 {
		opt.PileNoLockWait = 15 * time.Second
	}
	if opt.SnapshotRefresh == 0 {
		opt.SnapshotRefresh = time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Service{store: &Store{pool: pool}, snap: snap, journal: j, opgw: og, opt: opt, log: log, now: time.Now,
		mem: map[string]*Order{}, memIdem: map[string][]byte{}, waiters: map[string][]chan struct{}{}}
	s.breakers = NewBreakers(nil, s.onBreaker)
	return s
}

// SetBreakers 替换熔断器（装配时带上各依赖的探测）。
func (s *Service) SetBreakers(probes map[string]Probe) {
	s.breakers = NewBreakers(probes, s.onBreaker)
}

// SetJetStream 设置事件总线（发布直发用）。
func (s *Service) SetJetStream(js jetstream.JetStream) { s.js = js }

// SetClock 测试用时钟。
func (s *Service) SetClock(f func() time.Time) { s.now = f }

func (s *Service) nowTS() time.Time { return ts(s.now()) }

// onBreaker 依赖熔断或恢复：发事件（数据库不可用时直发总线，尽力而为）。
func (s *Service) onBreaker(component string, open bool) {
	state := "恢复"
	if open {
		state = "熔断"
	}
	s.log.Warn("依赖熔断状态变更", "component", component, "state", state)
	e := newEnvelope("ride.dependency.state_changed", component, s.now(), systemActor, map[string]any{
		"component": component, "state": state, "at": s.nowTS(), "instance": s.opt.InstanceID,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := s.store.pool.Begin(ctx)
	if err == nil {
		err = insertOutbox(ctx, tx, []Envelope{e})
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
	}
	if err != nil && s.js != nil {
		body, _ := json.Marshal(e)
		_, _ = s.js.Publish(ctx, e.Type, body, jetstream.WithMsgID(e.EventID))
	}
}

func (s *Service) dbOK() bool { return !s.breakers.Open(rules.CompDB) }

// ---------------- 订单读写（数据库优先，不可用时走本地日志） ----------------

type journalOrder struct {
	Order  *Order     `json:"order"`
	IsNew  bool       `json:"isNew"`
	Events []Envelope `json:"events"`
}

// ErrUnique 唯一约束冲突（一车一单、一人一单）。
type ErrUnique struct{ Constraint string }

func (e *ErrUnique) Error() string { return "唯一约束冲突：" + e.Constraint }

// saveOrder 保存订单（o.Rev 已加一）与事件。数据库不可用时追加写入本地日志并落盘，放进内存。
func (s *Service) saveOrder(ctx context.Context, o *Order, isNew bool, evs []Envelope) error {
	if s.dbOK() && !o.fromJournal {
		err := s.store.saveOrderTx(ctx, o, isNew, false, evs, vehicleLocOf(o))
		if err == nil {
			return nil
		}
		if c, ok := uniqueViolation(err); ok {
			return &ErrUnique{Constraint: c}
		}
		if !isConnErr(err) {
			return err
		}
		s.breakers.Trip(rules.CompDB)
	}
	// 本地日志模式：内存里也要守一车一单、一人一单
	s.mu.Lock()
	defer s.mu.Unlock()
	if isNew && o.active() {
		for _, m := range s.mem {
			if m.active() && m.OrderID != o.OrderID {
				if m.VehicleID == o.VehicleID {
					return &ErrUnique{Constraint: "orders_vehicle_active"}
				}
				if m.UserID == o.UserID {
					return &ErrUnique{Constraint: "orders_user_active"}
				}
			}
		}
	}
	if old, ok := s.mem[o.OrderID]; ok && old.Rev >= o.Rev {
		return errCASConflict
	}
	data, _ := json.Marshal(journalOrder{Order: o, IsNew: isNew, Events: evs})
	if err := s.journal.Append(journal.Record{Kind: "order", Key: o.OrderID, At: s.now(), Data: data}); err != nil {
		return fmt.Errorf("本地日志写入失败：%w", err)
	}
	c := *o
	c.fromJournal = true
	s.mem[o.OrderID] = &c
	return nil
}

func vehicleLocOf(o *Order) *vehicleLocUpdate {
	if o.Status != StEnded || o.EndLocation == nil {
		return nil
	}
	st := ""
	if o.EndStationID != nil && o.InStation != nil && *o.InStation {
		st = *o.EndStationID
	}
	return &vehicleLocUpdate{VehicleID: o.VehicleID, Location: o.EndLocation, StationID: st}
}

// loadOrder 读一单：内存（未回放的本地日志订单）优先，其次数据库。
func (s *Service) loadOrder(ctx context.Context, id string) (*Order, error) {
	s.mu.Lock()
	if m, ok := s.mem[id]; ok {
		c := *m
		s.mu.Unlock()
		return &c, nil
	}
	s.mu.Unlock()
	o, err := s.store.getOrder(ctx, id)
	if err != nil && isConnErr(err) {
		s.breakers.Trip(rules.CompDB)
	}
	return o, err
}

var errNoChange = errors.New("无变化")

// mutate 读出订单、按 fn 修改、比较并交换保存；并发冲突时重读重试。fn 返回 errNoChange 表示不保存。
func (s *Service) mutate(ctx context.Context, id string, fn func(o *Order) ([]Envelope, error)) (*Order, error) {
	for i := 0; i < 8; i++ {
		o, err := s.loadOrder(ctx, id)
		if err != nil {
			return nil, err
		}
		evs, err := fn(o)
		if errors.Is(err, errNoChange) {
			return o, nil
		}
		if err != nil {
			return o, err
		}
		o.Rev++
		err = s.saveOrder(ctx, o, false, evs)
		if errors.Is(err, errCASConflict) {
			continue
		}
		if err != nil {
			return nil, err
		}
		s.notify(id)
		return o, nil
	}
	return nil, errCASConflict
}

// activeOrderOfUser 该用户进行中的订单（开锁中、骑行中）。
func (s *Service) activeOrderOfUser(ctx context.Context, userID string) (*Order, error) {
	s.mu.Lock()
	for _, m := range s.mem {
		if m.UserID == userID && m.active() {
			c := *m
			s.mu.Unlock()
			return &c, nil
		}
	}
	s.mu.Unlock()
	if !s.dbOK() {
		return nil, nil
	}
	os, err := s.store.queryOrders(ctx, `SELECT doc, rev FROM orders WHERE user_id=$1 AND status IN ('开锁中','骑行中') LIMIT 1`, userID)
	if err != nil {
		if isConnErr(err) {
			s.breakers.Trip(rules.CompDB)
			return nil, nil
		}
		return nil, err
	}
	if len(os) == 0 {
		return nil, nil
	}
	return os[0], nil
}

// activeOrderOfVehicle 该车进行中的订单。
func (s *Service) activeOrderOfVehicle(ctx context.Context, vehicleID string) (*Order, error) {
	s.mu.Lock()
	for _, m := range s.mem {
		if m.VehicleID == vehicleID && m.active() {
			c := *m
			s.mu.Unlock()
			return &c, nil
		}
	}
	s.mu.Unlock()
	os, err := s.store.queryOrders(ctx, `SELECT doc, rev FROM orders WHERE vehicle_id=$1 AND status IN ('开锁中','骑行中') LIMIT 1`, vehicleID)
	if err != nil || len(os) == 0 {
		return nil, err
	}
	return os[0], nil
}

// activeVehicles 正在被骑的车。
func (s *Service) activeVehicles(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	s.mu.Lock()
	for _, m := range s.mem {
		if m.active() {
			out[m.VehicleID] = true
		}
	}
	s.mu.Unlock()
	if !s.dbOK() {
		return out
	}
	rows, err := s.store.pool.Query(ctx, `SELECT vehicle_id FROM orders WHERE status IN ('开锁中','骑行中')`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			out[v] = true
		}
	}
	return out
}

// waitFor 本实例内的通知（订单有变化时唤醒等待开锁结果的请求）。
func (s *Service) waitChan(id string) chan struct{} {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.waiters[id] = append(s.waiters[id], ch)
	s.mu.Unlock()
	return ch
}

func (s *Service) dropWaiter(id string, ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws := s.waiters[id]
	for i, c := range ws {
		if c == ch {
			s.waiters[id] = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	if len(s.waiters[id]) == 0 {
		delete(s.waiters, id)
	}
}

func (s *Service) notify(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.waiters[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// newOrderID 订单号：OD + 开锁请求日期 + 5 位日序号。数据库不可用时用本地序号段（50000 + 实例序号×5000 起）。
func (s *Service) newOrderID(ctx context.Context, at time.Time) (string, error) {
	day := at.In(cst).Format("20060102")
	if s.dbOK() {
		n, err := s.store.nextOrderSeq(ctx, day)
		if err == nil {
			return fmt.Sprintf("OD%s%05d", day, n), nil
		}
		if !isConnErr(err) {
			return "", err
		}
		s.breakers.Trip(rules.CompDB)
	}
	n, err := s.journal.NextSeq(day)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("OD%s%05d", day, 50000+s.opt.InstanceNo*5000+n), nil
}

// degradeInput 降级开单的本地状态。
func (s *Service) degradeInput(ctx context.Context, userID string) (rules.DegradeInput, DegradeCtl) {
	ctl := s.snap.getDegrade()
	openComps := s.breakers.OpenComponents()
	d := rules.DegradeInput{Active: ctl.Active || len(openComps) > 0, Tightened: ctl.Tightened, Stopped: ctl.Stopped}
	for _, c := range append(openComps, ctl.Components...) {
		if c == rules.CompDeposit {
			d.DepositUnknown = true
		}
	}
	if !d.Active {
		return d, ctl
	}
	since := s.now().Add(-time.Hour)
	s.mu.Lock()
	for _, m := range s.mem {
		if m.Degraded != nil && m.Status != StFailed {
			if m.RequestAt.After(since) {
				d.HourCount++
			}
			if m.UserID == userID {
				d.UserOpenDegraded++
			}
		}
	}
	s.mu.Unlock()
	if s.dbOK() {
		var hour, mine int
		err := s.store.pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE request_at > $1),
			count(*) FILTER (WHERE user_id = $2 AND NOT fee_settled)
			FROM orders WHERE degraded AND status <> '开锁失败' AND (request_at > $1 OR user_id = $2)`, since, userID).Scan(&hour, &mine)
		if err == nil {
			d.HourCount += hour
			d.UserOpenDegraded += mine
		}
	}
	return d, ctl
}

// nearby 附近可租车：按各车最后一次锁车位置，只列可租且不在骑行中的车，按距离升序最多 5 辆。
func (s *Service) nearby(ctx context.Context, center geo.Pt, exclude string, radius int, onlyOtherBrand string) []map[string]any {
	if radius <= 0 {
		radius = s.snap.getParams().NearbyRadiusM
	}
	busy := s.activeVehicles(ctx)
	type hit struct {
		v rules.Vehicle
		d float64
	}
	var hits []hit
	for _, v := range s.snap.allVehicles() {
		if v.VehicleID == exclude || v.Location == nil || busy[v.VehicleID] || v.Removed || v.CodeStatus != rules.CodeEnabled {
			continue
		}
		if onlyOtherBrand != "" && v.BrandID == onlyOtherBrand {
			continue
		}
		b := s.snap.brand(v.BrandID)
		if b == nil || b.AccessStatus != rules.AccessOn {
			continue
		}
		d := geo.Distance(center, *v.Location)
		if d <= float64(radius) {
			hits = append(hits, hit{v, d})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].d != hits[j].d {
			return hits[i].d < hits[j].d
		}
		return hits[i].v.VehicleID < hits[j].v.VehicleID
	})
	if len(hits) > 5 {
		hits = hits[:5]
	}
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, s.nearbyView(h.v, h.d))
	}
	return out
}

func (s *Service) nearbyView(v rules.Vehicle, d float64) map[string]any {
	b := s.snap.brand(v.BrandID)
	m := map[string]any{"vehicleId": v.VehicleID, "plateNo": v.PlateNo, "govCode": v.GovCode, "brandId": v.BrandID,
		"battery": nil, "location": v.Location, "distanceM": int(d + 0.5), "stationId": nil, "stationName": nil}
	if b != nil {
		m["brandName"], m["displayName"], m["color"] = b.Name, b.DisplayName, b.Color
	}
	if v.StationID != "" {
		m["stationId"] = v.StationID
		if st := s.snap.station(v.StationID); st != nil {
			m["stationName"] = st.Name
		}
	}
	return m
}

// priceSummary 现行价格文案（只展示，不算钱）。
func priceSummary(v *rules.Version) map[string]any {
	if v == nil {
		return nil
	}
	r := v.Ride
	return map[string]any{
		"versionId": v.VersionID, "startFee": r.StartFee, "startMinutes": r.StartMinutes, "perMinute": r.PerMinute, "dispatchFee": v.DispatchFee,
		"text":         fmt.Sprintf("起步 %s（含 %d 分钟），之后每分钟 %s", rules.Yuan(r.StartFee), r.StartMinutes, rules.Yuan(r.PerMinute)),
		"dispatchText": fmt.Sprintf("站点外还车加收调度费 %s", rules.Yuan(v.DispatchFee)),
		"monthCard":    map[string]any{"price": v.MonthCard.Price, "days": v.MonthCard.Days, "perOrderCap": v.MonthCard.PerOrderCap},
		"deposit":      v.Deposit.Amount, "creditFreeThreshold": v.CreditFree.Threshold,
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func trimLower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

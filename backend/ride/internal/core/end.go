package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"bike/ride/internal/clients/opgw"
	"bike/ride/internal/geo"
	"bike/ride/internal/journal"
	"bike/ride/internal/rules"
)

// RideEvent opgw 送达的运营商骑行事件（/internal/ride/ride-events 请求体）。
type RideEvent struct {
	OperatorEventID string  `json:"operatorEventId"`
	OrderID         *string `json:"orderId"`
	PlateNo         string  `json:"plateNo"`
	BrandID         string  `json:"brandId"`
	OperatorID      string  `json:"operatorId"`
	Kind            string  `json:"kind"` // 开锁结果｜锁车
	Unlock          *struct {
		Result string    `json:"result"`
		Reason *string   `json:"reason"`
		At     time.Time `json:"at"`
	} `json:"unlock"`
	Lock *struct {
		At                   time.Time `json:"at"`
		Location             *geo.Pt   `json:"location"`
		VehicleSideInStation *bool     `json:"vehicleSideInStation"`
		DurationSec          *int64    `json:"durationSec"`
		DistanceKm           *float64  `json:"distanceKm"`
		Manual               bool      `json:"manual"`
	} `json:"lock"`
	ReceivedAt time.Time `json:"receivedAt"`
	Late       bool      `json:"late"`
}

// Validate 请求校验。
func (e *RideEvent) Validate() string {
	switch {
	case e.OperatorEventID == "" || e.PlateNo == "" || e.BrandID == "" || e.OperatorID == "" || e.ReceivedAt.IsZero():
		return "operatorEventId、plateNo、brandId、operatorId、receivedAt 必填"
	case e.Kind == "开锁结果":
		if e.Unlock == nil || (e.Unlock.Result != "成功" && e.Unlock.Result != "失败") || e.Unlock.At.IsZero() {
			return "开锁结果须带 unlock.result 与 unlock.at"
		}
		if e.OrderID == nil || *e.OrderID == "" {
			return "开锁结果须带平台订单号"
		}
	case e.Kind == "锁车":
		if e.Lock == nil || e.Lock.At.IsZero() {
			return "锁车须带 lock.at"
		}
	default:
		return "kind 只能是 开锁结果 或 锁车"
	}
	return ""
}

func clampTime(t time.Time, lo, hi *time.Time) time.Time {
	if lo != nil && t.Before(*lo) {
		t = *lo
	}
	if hi != nil && t.After(*hi) {
		t = *hi
	}
	return ts(t)
}

// DeliverRideEvent 收运营商骑行事件：按运营商事件号去重并持久化（数据库不可用时写本地日志），再匹配订单生效。
func (s *Service) DeliverRideEvent(ctx context.Context, e *RideEvent) (map[string]any, *APIError) {
	if msg := e.Validate(); msg != "" {
		return nil, apiErr(400, "INVALID", msg, nil)
	}
	if !s.dbOK() {
		data, _ := json.Marshal(e)
		if err := s.journal.Append(journal.Record{Kind: "report", Key: e.OperatorEventID, At: s.now(), Data: data}); err != nil {
			return nil, busy(err)
		}
		// 本实例内存里的订单（数据库不可用期间建立的）可以立即生效
		matched := s.applyReportMem(ctx, e)
		return map[string]any{"accepted": true, "orderId": nullStr(matched)}, nil
	}
	matched, dup, err := s.persistReport(ctx, e)
	if err != nil {
		if isConnErr(err) {
			s.breakers.Trip(rules.CompDB)
			return s.DeliverRideEvent(ctx, e)
		}
		return nil, busy(err)
	}
	if dup {
		return map[string]any{"accepted": true, "orderId": nullStr(matched)}, nil
	}
	id, _ := s.applyReport(ctx, e)
	return map[string]any{"accepted": true, "orderId": nullStr(id)}, nil
}

// persistReport 写入 ride_reports；重复到达返回之前匹配到的订单号。
func (s *Service) persistReport(ctx context.Context, e *RideEvent) (string, bool, error) {
	body, _ := json.Marshal(e)
	tag, err := s.store.pool.Exec(ctx, `INSERT INTO ride_reports (operator_event_id, order_id, plate_no, brand_id, kind, body, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (operator_event_id) DO NOTHING`, e.OperatorEventID, e.OrderID, e.PlateNo, e.BrandID, e.Kind, body, e.ReceivedAt)
	if err != nil {
		return "", false, err
	}
	if tag.RowsAffected() == 1 {
		return "", false, nil
	}
	var matched *string
	_ = s.store.pool.QueryRow(ctx, `SELECT matched_order_id FROM ride_reports WHERE operator_event_id=$1`, e.OperatorEventID).Scan(&matched)
	if matched == nil {
		return "", true, nil
	}
	return *matched, true, nil
}

// applyReport 让报告对订单生效；匹配不到的保留待匹配（恢复回放后再试）。
func (s *Service) applyReport(ctx context.Context, e *RideEvent) (string, error) {
	id, after, err := s.applyReportCore(ctx, e)
	if err != nil || id == "" {
		return id, err
	}
	_, _ = s.store.pool.Exec(ctx, `UPDATE ride_reports SET applied=true, matched_order_id=$2 WHERE operator_event_id=$1`, e.OperatorEventID, id)
	if after != nil {
		after()
	}
	return id, nil
}

// applyReportMem 数据库不可用时，只对本实例内存里的订单生效。
func (s *Service) applyReportMem(ctx context.Context, e *RideEvent) string {
	if e.OrderID == nil {
		return ""
	}
	s.mu.Lock()
	_, ok := s.mem[*e.OrderID]
	s.mu.Unlock()
	if !ok {
		return ""
	}
	id, after, err := s.applyReportCore(ctx, e)
	if err != nil {
		return ""
	}
	if after != nil {
		after()
	}
	return id
}

// matchOrder 找报告对应的订单：带订单号按订单号；没有订单号（手动锁车）按车号找进行中或待人工处理的订单。
func (s *Service) matchOrder(ctx context.Context, e *RideEvent) (string, error) {
	if e.OrderID != nil && *e.OrderID != "" {
		if _, err := s.loadOrder(ctx, *e.OrderID); err != nil {
			if errors.Is(err, errNotFound) {
				return "", nil
			}
			return "", err
		}
		return *e.OrderID, nil
	}
	if e.Kind != "锁车" {
		return "", nil
	}
	s.mu.Lock()
	for _, m := range s.mem {
		if m.PlateNo == e.PlateNo && m.BrandID == e.BrandID && (m.Status == StRiding || m.Status == StManual) {
			id := m.OrderID
			s.mu.Unlock()
			return id, nil
		}
	}
	s.mu.Unlock()
	if !s.dbOK() {
		return "", nil
	}
	os, err := s.store.queryOrders(ctx, `SELECT doc, rev FROM orders WHERE plate_no=$1 AND brand_id=$2 AND status IN ('骑行中','待人工处理')
		AND (start_at IS NULL OR start_at <= $3) ORDER BY request_at DESC LIMIT 1`, e.PlateNo, e.BrandID, e.Lock.At)
	if err != nil || len(os) == 0 {
		return "", err
	}
	return os[0].OrderID, nil
}

// applyReportCore 报告生效的状态迁移。返回匹配到的订单号与提交后要做的事（如超时开锁后请求结束）。
func (s *Service) applyReportCore(ctx context.Context, e *RideEvent) (string, func(), error) {
	id, err := s.matchOrder(ctx, e)
	if err != nil || id == "" {
		return "", nil, err
	}
	recv := ts(e.ReceivedAt)
	var after func()
	_, err = s.mutate(ctx, id, func(o *Order) ([]Envelope, error) {
		after = nil
		switch e.Kind {
		case "开锁结果":
			return s.applyUnlockReport(o, e, recv, &after)
		default:
			return s.applyLockReport(o, e, recv)
		}
	})
	if errors.Is(err, errRetryLater) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	return id, after, nil
}

// errRetryLater 报告暂时不能生效（如锁车报告先于开锁结果到达），保留待匹配。
var errRetryLater = errors.New("稍后再试")

// applyUnlockReport 开锁结果：时限内成功 → 骑行中（计费起点为报告时刻）；失败 → 开锁失败；
// 超时后才报成功（含已判"车辆无响应"的）→ 立即请求运营商结束，订单 0 元（BR-03）。
func (s *Service) applyUnlockReport(o *Order, e *RideEvent, recv time.Time, after *func()) ([]Envelope, error) {
	at := clampTime(e.Unlock.At, &o.RequestAt, &recv)
	deadline := o.RequestAt.Add(s.unlockTimeout())
	lateSuccess := func() ([]Envelope, error) {
		o.Status, o.UnlockResult, o.UnlockFailReason = StEnded, sp(UnlockLate), nil
		o.UnlockReportAt, o.StartAt, o.EndAt, o.LockReportAt = &at, &at, &at, &at
		zero := int64(0)
		o.DurationSec = &zero
		o.EndMethod, o.InStation, o.InStationBasis = sp(EndLateUnlock), bp(true), sp(rules.BasisVehicle)
		o.EndLocation, o.EndStationID, o.EndStationName = o.StartLocation, o.StartStationID, o.StartStationName
		o.ReportReceivedAt, o.LateReport = &recv, e.Late
		o.LockRequest = nil
		*after = func() { s.requestEnd(context.Background(), o.OrderID, opgw.CauseLateUnlock, "") }
		return []Envelope{evEnded(o, systemActor)}, nil
	}
	switch o.Status {
	case StUnlocking:
		if e.Unlock.Result == "成功" {
			if recv.After(deadline) {
				return lateSuccess()
			}
			o.Status, o.UnlockResult, o.UnlockReportAt, o.StartAt = StRiding, sp(UnlockOK), &at, &at
			o.StartPileID = s.snap.pileOf(o.VehicleID)
			return []Envelope{evUnlocked(o)}, nil
		}
		reason := ReasonFault
		if e.Unlock.Reason != nil {
			reason = rejectReason(e.Unlock.Reason)
		}
		o.Status, o.UnlockResult, o.UnlockFailReason, o.UnlockReportAt = StFailed, sp(UnlockFail), sp(reason), &at
		return []Envelope{evUnlockFailed(o, recv)}, nil
	case StFailed:
		if e.Unlock.Result == "成功" && o.UnlockFailReason != nil && *o.UnlockFailReason == ReasonNoResponse {
			return lateSuccess()
		}
	}
	return nil, errNoChange
}

// applyLockReport 锁车报告：骑行中或待人工处理的订单结束，按 BR-01、BR-08～BR-13 定终点与在点。
func (s *Service) applyLockReport(o *Order, e *RideEvent, recv time.Time) ([]Envelope, error) {
	if o.Status == StUnlocking {
		return nil, errRetryLater
	}
	if o.Status != StRiding && o.Status != StManual {
		return nil, errNoChange
	}
	lockAt := clampTime(e.Lock.At, o.StartAt, &recv)
	stations, piles := s.snap.geoData()
	in := rules.ReturnInput{Loc: e.Lock.Location, VehicleSide: e.Lock.VehicleSideInStation, VehicleID: o.VehicleID, Stations: stations, Piles: piles}
	p := o.Pending
	var pileInAt *time.Time
	if p != nil && p.PileID != "" && p.PileInAt != nil {
		in.PileHit = &rules.PileHit{PileID: p.PileID, StationID: p.PileStationID}
		pileInAt = p.PileInAt
	}
	d := rules.DecideInStation(in)
	endAt := lockAt
	if pileInAt != nil && pileInAt.Before(endAt) {
		endAt = ts(*pileInAt)
	}
	method := EndUserLock
	switch {
	case in.PileHit != nil && p.EndCause == opgw.CausePileNoLock:
		method = EndPileNoLock
	case in.PileHit != nil:
		method = EndPile
	case p != nil && p.PageReturn:
		method = EndPage
	}
	o.LockReportAt, o.PileInAt, o.ReportReceivedAt, o.LateReport = &lockAt, pileInAt, &recv, e.Late
	s.finish(o, endAt, method, d, e.Lock.Location, e.Lock.DistanceKm)
	o.DispatchPromptShown = p != nil && p.PageReturn && p.DispatchConfirmed && !d.InStation
	return []Envelope{evEnded(o, systemActor)}, nil
}

// finish 写结束字段。
func (s *Service) finish(o *Order, endAt time.Time, method string, d rules.ReturnDecision, loc *geo.Pt, km *float64) {
	o.Status = StEnded
	o.EndAt, o.EndMethod = &endAt, sp(method)
	if o.StartAt != nil {
		dur := int64(endAt.Sub(*o.StartAt).Seconds())
		if dur < 0 {
			dur = 0
		}
		o.DurationSec = &dur
	}
	if km != nil {
		v := math.Round(*km*10) / 10
		o.DistanceKm = &v
	}
	o.InStation, o.InStationBasis = bp(d.InStation), sp(d.Basis)
	o.DistanceToStationM, o.VehicleSideInStation = d.DistanceToStationM, d.VehicleSideInStation
	o.EndStationID, o.EndStationName, o.EndPileID = nil, nil, nil
	if d.Station != nil && d.InStation {
		o.EndStationID, o.EndStationName = sp(d.Station.StationID), sp(d.Station.Name)
	}
	switch {
	case d.Pile != nil:
		o.EndPileID = sp(d.Pile.PileID)
		pl := d.Pile.Location
		o.EndLocation = &pl
	case loc != nil:
		l := *loc
		o.EndLocation = &l
	}
	if o.EndLocation == nil && d.Station != nil {
		c := d.Station.Center
		o.EndLocation = &c
	}
	o.Pending = nil
}

// requestEnd 请求运营商结束骑行并记下结果。
func (s *Service) requestEnd(ctx context.Context, id, cause, reason string) string {
	o, err := s.loadOrder(ctx, id)
	if err != nil {
		return opgw.Unreachable
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	res, _ := s.opgw.EndRide(cctx, opgw.EndRideCommand{Target: opgw.Target{OrderID: o.OrderID, OperatorID: o.OperatorID, BrandID: o.BrandID, VehicleID: o.VehicleID, PlateNo: o.PlateNo}, Cause: cause, Reason: reason})
	cancel()
	r := res.Result
	if r == "" {
		r = opgw.Unreachable
	}
	_, _ = s.mutate(ctx, id, func(x *Order) ([]Envelope, error) {
		x.LockRequest = sp(r)
		if x.Pending != nil && x.active() {
			x.Pending.EndCause = cause
			x.Pending.EndRequestedAt = tp(s.now())
		}
		return nil, nil
	})
	return r
}

// OnPileOccupancy 桩在位：骑行中的车入位记下桩与时刻（计费终点候选，BR-01、BR-08）；离位清除。
func (s *Service) OnPileOccupancy(ctx context.Context, pileID, stationID, vehicleID string, present bool, at time.Time) error {
	o, err := s.activeOrderOfVehicle(ctx, vehicleID)
	if err != nil || o == nil || o.Status != StRiding {
		return err
	}
	at = ts(at)
	_, err = s.mutate(ctx, o.OrderID, func(x *Order) ([]Envelope, error) {
		if x.Status != StRiding {
			return nil, errNoChange
		}
		if present {
			if x.StartAt != nil && at.Before(*x.StartAt) {
				return nil, errNoChange
			}
			if x.Pending == nil {
				x.Pending = &Pending{}
			}
			if x.Pending.PileID == pileID && x.Pending.PileInAt != nil {
				return nil, errNoChange
			}
			x.Pending.PileID, x.Pending.PileStationID, x.Pending.PileInAt = pileID, stationID, &at
			return nil, nil
		}
		if x.Pending == nil || x.Pending.PileID != pileID {
			return nil, errNoChange
		}
		x.Pending.PileID, x.Pending.PileStationID, x.Pending.PileInAt = "", "", nil
		return nil, nil
	})
	return err
}

// SweepPileNoLock 桩识别在位而迟迟没有锁车报告：请求运营商结束骑行（BR-09）。
func (s *Service) SweepPileNoLock(ctx context.Context) {
	os, err := s.store.queryOrders(ctx, `SELECT doc, rev FROM orders WHERE status='骑行中' AND doc->'_pending'->>'pileInAt' IS NOT NULL
		AND coalesce(doc->'_pending'->>'endCause','') = ''`)
	if err != nil {
		return
	}
	for _, o := range os {
		if o.Pending == nil || o.Pending.PileInAt == nil || s.now().Sub(*o.Pending.PileInAt) < s.opt.PileNoLockWait {
			continue
		}
		s.requestEnd(ctx, o.OrderID, opgw.CausePileNoLock, "")
	}
}

// locationFor 页面还车与预判用的位置：站点 → 桩 → 手机位置 → 车辆最近位置。
func (s *Service) locationFor(o *Order, stationID, pileID string, loc *geo.Pt) (*geo.Pt, *APIError) {
	if pileID != "" {
		p := s.snap.pile(pileID)
		if p == nil {
			return nil, apiErr(400, "INVALID", "没有找到该桩", nil)
		}
		l := p.Location
		return &l, nil
	}
	if stationID != "" {
		st := s.snap.station(stationID)
		if st == nil {
			return nil, apiErr(400, "INVALID", "没有找到该站点", nil)
		}
		c := st.Center
		if !geo.InPolygon(c, st.Fence) {
			c = geo.Centroid(st.Fence)
		}
		return &c, nil
	}
	if loc != nil {
		return loc, nil
	}
	return s.currentLocation(o), nil
}

// ReturnPreview 还车前预判。
func (s *Service) ReturnPreview(ctx context.Context, userID, stationID string, loc *geo.Pt) (map[string]any, *APIError) {
	o, err := s.activeOrderOfUser(ctx, userID)
	if err != nil {
		return nil, busy(err)
	}
	if o == nil || o.Status != StRiding {
		return nil, apiErr(409, "NO_RIDING", "", nil)
	}
	p, ae := s.locationFor(o, stationID, "", loc)
	if ae != nil {
		return nil, ae
	}
	if p == nil {
		return nil, apiErr(400, "INVALID", "暂时不知道车辆位置，请提供当前位置", nil)
	}
	stations, piles := s.snap.geoData()
	d := rules.DecideInStation(rules.ReturnInput{Loc: p, VehicleID: o.VehicleID, Stations: stations, Piles: piles})
	ver := s.snap.version(o.PricingVersionID)
	var fee int64
	if !d.InStation && ver != nil {
		fee = ver.DispatchFee
	}
	res := map[string]any{"location": p, "inStation": d.InStation, "basis": d.Basis, "dispatchFee": fee, "station": nil, "nearestStation": nil}
	if d.Station != nil && d.InStation {
		res["station"] = map[string]any{"stationId": d.Station.StationID, "name": d.Station.Name, "type": d.Station.Type, "freePiles": len(rules.FreePiles(piles, d.Station.StationID, o.VehicleID))}
	}
	if d.Nearest != nil {
		res["nearestStation"] = nearestView(d.Nearest, piles)
	}
	switch {
	case d.InStation && d.Basis == rules.BasisFence:
		res["text"] = "桩位已满，在站点内桩旁锁车即可，不收调度费"
	case d.InStation:
		res["text"] = "在站点内，可以还车"
	case d.Nearest != nil:
		res["text"] = fmt.Sprintf("不在站点内，还车将收取调度费 %s。最近站点「%s」约 %d 米", rules.Yuan(fee), d.Nearest.Station.Name, d.Nearest.DistanceM)
	default:
		res["text"] = fmt.Sprintf("不在站点内，还车将收取调度费 %s", rules.Yuan(fee))
	}
	return res, nil
}

func nearestView(n *rules.NearestHit, piles []rules.Pile) map[string]any {
	return map[string]any{"stationId": n.Station.StationID, "name": n.Station.Name, "type": n.Station.Type, "distanceM": n.DistanceM,
		"center": n.Station.Center, "freePiles": len(rules.FreePiles(piles, n.Station.StationID, ""))}
}

// ReturnRequest 页面还车请求。
type ReturnRequest struct {
	StationID       string  `json:"stationId"`
	PileID          string  `json:"pileId"`
	Location        *geo.Pt `json:"location"`
	ConfirmDispatch bool    `json:"confirmDispatch"`
}

// Return 页面还车：站外须先确认调度费；平台请求运营商结束骑行，以锁车报告为准结束订单。
func (s *Service) Return(ctx context.Context, userID string, req ReturnRequest) (map[string]any, *APIError) {
	o, err := s.activeOrderOfUser(ctx, userID)
	if err != nil {
		return nil, busy(err)
	}
	if o == nil || o.Status != StRiding {
		return nil, apiErr(409, "NO_RIDING", "", nil)
	}
	stations, piles := s.snap.geoData()
	if req.PileID != "" {
		p := s.snap.pile(req.PileID)
		if p == nil {
			return nil, apiErr(400, "INVALID", "没有找到该桩", nil)
		}
		if p.Status != "在线" {
			return nil, apiErr(409, "PILE_UNAVAILABLE", "", map[string]any{"stationId": p.StationID})
		}
		if p.OccupiedBy != "" && p.OccupiedBy != o.VehicleID {
			return nil, apiErr(409, "PILE_FULL", "", map[string]any{"stationId": p.StationID})
		}
	}
	loc, ae := s.locationFor(o, req.StationID, req.PileID, req.Location)
	if ae != nil {
		return nil, ae
	}
	d := rules.DecideInStation(rules.ReturnInput{Loc: loc, VehicleID: o.VehicleID, Stations: stations, Piles: piles})
	ver := s.snap.version(o.PricingVersionID)
	if !d.InStation && !req.ConfirmDispatch {
		var fee int64
		if ver != nil {
			fee = ver.DispatchFee
		}
		extra := map[string]any{"dispatchFee": fee, "nearestStation": nil}
		msg := fmt.Sprintf("当前不在站点内，还车将收取调度费 %s", rules.Yuan(fee))
		if d.Nearest != nil {
			extra["nearestStation"] = nearestView(d.Nearest, piles)
			msg += fmt.Sprintf("。最近站点「%s」约 %d 米", d.Nearest.Station.Name, d.Nearest.DistanceM)
		}
		e := apiErr(409, "DISPATCH_CONFIRM_REQUIRED", msg, extra)
		e.Body["charged"] = true
		return nil, e
	}
	if _, err := s.mutate(ctx, o.OrderID, func(x *Order) ([]Envelope, error) {
		if x.Status != StRiding {
			return nil, errNoChange
		}
		if x.Pending == nil {
			x.Pending = &Pending{}
		}
		x.Pending.PageReturn = true
		x.Pending.DispatchConfirmed = !d.InStation && req.ConfirmDispatch
		return nil, nil
	}); err != nil {
		return nil, busy(err)
	}
	ch := s.waitChan(o.OrderID)
	defer s.dropWaiter(o.OrderID, ch)
	result := s.requestEnd(ctx, o.OrderID, opgw.CauseUserReturn, "")
	final := o
	deadline := s.now().Add(s.opt.ReturnWait)
	tick := time.NewTicker(s.opt.PollInterval)
	defer tick.Stop()
	for result == opgw.Accepted {
		x, err := s.loadOrder(ctx, o.OrderID)
		if err == nil {
			final = x
			if x.Status != StRiding {
				break
			}
		}
		if !s.now().Before(deadline) {
			break
		}
		select {
		case <-ch:
		case <-tick.C:
		case <-ctx.Done():
			deadline = s.now()
		}
	}
	if x, err := s.loadOrder(ctx, o.OrderID); err == nil {
		final = x
	}
	ended := final.Status == StEnded
	msg := "还车确认中，你可以离开，按实际锁车时刻计费"
	switch {
	case ended && final.Degraded != nil:
		msg = "已还车，费用将稍后收取"
	case ended:
		msg = "已还车，账单稍后推送"
	case result != opgw.Accepted:
		msg = "还车请求没有送达车辆，请在站点内手动锁车，按实际锁车时刻计费"
	}
	return map[string]any{"order": final.userView(), "ended": ended, "deferred": final.Degraded != nil, "message": msg}, nil
}

// ForceEndRequest 客服强制结束。
type ForceEndRequest struct {
	BilledEndAt *time.Time `json:"billedEndAt"`
	StationID   *string    `json:"stationId"`
	TicketID    *string    `json:"ticketId"`
	Reason      string     `json:"reason"`
	Actor       *Actor     `json:"actor"`
}

func reasonOK(r string) bool { return len([]rune(trimSpaceStr(r))) >= 4 }

func trimSpaceStr(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// ForceEnd 强制结束（R-13）：请求运营商锁车，不论结果都按计费终点结束订单，记事由，发留痕。
func (s *Service) ForceEnd(ctx context.Context, id string, req ForceEndRequest) (map[string]any, *APIError) {
	if req.Actor == nil || req.Actor.Kind == "" || req.Actor.ID == "" {
		return nil, apiErr(400, "INVALID", "缺少操作人", nil)
	}
	if !reasonOK(req.Reason) {
		return nil, apiErr(400, "REASON_REQUIRED", "请填写操作事由（至少 4 个字）", nil)
	}
	o, err := s.loadOrder(ctx, id)
	if errors.Is(err, errNotFound) {
		return nil, apiErr(404, "NOT_FOUND", "没有找到该订单", nil)
	}
	if err != nil {
		return nil, busy(err)
	}
	if o.Status != StRiding {
		return nil, apiErr(409, "NOT_RIDING", "该订单不在骑行中", nil)
	}
	lock := s.requestEnd(ctx, id, opgw.CauseForceEnd, req.Reason)
	now := s.nowTS()
	var out *Order
	out, err = s.mutate(ctx, id, func(x *Order) ([]Envelope, error) {
		if x.Status != StRiding {
			return nil, &APIError{Status: 409, Body: rules.NewFail("NOT_RIDING", "该订单不在骑行中", nil).Body()}
		}
		end := now
		if req.BilledEndAt != nil {
			end = ts(*req.BilledEndAt)
		}
		end = clampTime(end, x.StartAt, &now)
		d := rules.ReturnDecision{InStation: false, Basis: rules.BasisOutside}
		if req.StationID != nil && *req.StationID != "" {
			if st := s.snap.station(*req.StationID); st != nil {
				d = rules.ReturnDecision{InStation: true, Basis: rules.BasisVehicle, Station: st}
			}
		}
		before := map[string]any{"status": x.Status}
		s.finish(x, end, EndForce, d, nil, nil)
		x.LockRequest = sp(lock)
		x.ReportReceivedAt = &now
		x.ForceEnd = &ForceEnd{At: now, BilledEndAt: end, By: *req.Actor, Reason: req.Reason, TicketID: req.TicketID}
		r := req.Reason
		return []Envelope{
			evEnded(x, *req.Actor),
			evAudit("强制结束订单", x.OrderID, fmt.Sprintf("强制结束订单 %s，计费终点 %s", x.OrderID, end.Format(time.RFC3339)), &r, before,
				map[string]any{"status": StEnded, "endAt": end, "lockRequest": lock}, *req.Actor, now),
		}, nil
	})
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) {
			return nil, ae
		}
		return nil, busy(err)
	}
	return map[string]any{"order": out.adminView(), "lockRequest": lock}, nil
}

// LockFactRequest 补录锁车事实。
type LockFactRequest struct {
	EndAt     *time.Time `json:"endAt"`
	StationID *string    `json:"stationId"`
	Reason    string     `json:"reason"`
	Actor     *Actor     `json:"actor"`
}

// BackfillLockFact 给待人工处理的订单补录锁车时刻与站点，订单结束（R-34、BR-32）。
func (s *Service) BackfillLockFact(ctx context.Context, id string, req LockFactRequest) (map[string]any, *APIError) {
	if req.Actor == nil || req.Actor.Kind == "" || req.Actor.ID == "" || req.EndAt == nil {
		return nil, apiErr(400, "INVALID", "endAt 与操作人必填", nil)
	}
	if !reasonOK(req.Reason) {
		return nil, apiErr(400, "REASON_REQUIRED", "请填写操作事由（至少 4 个字）", nil)
	}
	o, err := s.loadOrder(ctx, id)
	if errors.Is(err, errNotFound) {
		return nil, apiErr(404, "NOT_FOUND", "没有找到该订单", nil)
	}
	if err != nil {
		return nil, busy(err)
	}
	if o.Status != StManual {
		return nil, apiErr(409, "CONFLICT", "订单不是待人工处理状态", nil)
	}
	now := s.nowTS()
	out, err := s.mutate(ctx, id, func(x *Order) ([]Envelope, error) {
		if x.Status != StManual {
			return nil, &APIError{Status: 409, Body: rules.NewFail("CONFLICT", "订单不是待人工处理状态", nil).Body()}
		}
		end := clampTime(*req.EndAt, x.StartAt, &now)
		d := rules.ReturnDecision{InStation: false, Basis: rules.BasisOutside}
		if req.StationID != nil && *req.StationID != "" {
			if st := s.snap.station(*req.StationID); st != nil {
				d = rules.ReturnDecision{InStation: true, Basis: rules.BasisVehicle, Station: st}
			}
		}
		method := EndUserLock
		s.finish(x, end, method, d, nil, nil)
		x.LockReportAt, x.ReportReceivedAt = &end, &now
		x.ForceEnd = &ForceEnd{At: now, BilledEndAt: end, By: *req.Actor, Reason: req.Reason}
		r := req.Reason
		return []Envelope{
			evEnded(x, *req.Actor),
			evAudit("闭合订单", x.OrderID, fmt.Sprintf("补录锁车事实，订单 %s 计费终点 %s", x.OrderID, end.Format(time.RFC3339)), &r,
				map[string]any{"status": StManual}, map[string]any{"status": StEnded, "endAt": end}, *req.Actor, now),
		}, nil
	})
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) {
			return nil, ae
		}
		return nil, busy(err)
	}
	return out.adminView(), nil
}

// SweepStale 定时清理：
// 1）开锁中超过时限仍未结果（处理请求的实例中途退出）→ 开锁失败（车辆无响应）；
// 2）骑行中超过补报窗口仍无结束报告 → 待人工处理，缺锁车事实。
func (s *Service) SweepStale(ctx context.Context) {
	now := s.now()
	stuck, err := s.store.queryOrders(ctx, `SELECT doc, rev FROM orders WHERE status='开锁中' AND request_at < $1`, now.Add(-s.unlockTimeout()-30*time.Second))
	if err == nil {
		for _, o := range stuck {
			_, _ = s.timeoutUnlock(ctx, o.OrderID)
		}
	}
	win := time.Duration(s.snap.getParams().BackfillWindowHours) * time.Hour
	old, err := s.store.queryOrders(ctx, `SELECT doc, rev FROM orders WHERE status='骑行中' AND start_at < $1`, now.Add(-win))
	if err != nil {
		return
	}
	for _, o := range old {
		_, _ = s.mutate(ctx, o.OrderID, func(x *Order) ([]Envelope, error) {
			if x.Status != StRiding {
				return nil, errNoChange
			}
			x.Status = StManual
			return []Envelope{evPendingManual(x, []string{"锁车事实"}, now)}, nil
		})
	}
}

// SweepOverLimit 降级单超过单笔时长或金额上限时提醒一次，不结束骑行（6.7）。
func (s *Service) SweepOverLimit(ctx context.Context) {
	os, err := s.store.queryOrders(ctx, `SELECT doc, rev FROM orders WHERE status='骑行中' AND degraded AND doc->'degraded'->>'overLimitRemindedAt' IS NULL`)
	if err != nil {
		return
	}
	now := s.now()
	for _, o := range os {
		if o.StartAt == nil || o.Degraded == nil {
			continue
		}
		el := int64(now.Sub(*o.StartAt).Seconds())
		over := el > int64(o.Degraded.LimitMinutes)*60
		if v := s.snap.version(o.PricingVersionID); v != nil && rules.EstimateRideFee(v, el) > o.Degraded.LimitAmount {
			over = true
		}
		if !over {
			continue
		}
		_, _ = s.mutate(ctx, o.OrderID, func(x *Order) ([]Envelope, error) {
			if x.Degraded == nil || x.Degraded.OverLimitRemindedAt != nil || x.Status != StRiding {
				return nil, errNoChange
			}
			x.Degraded.OverLimitRemindedAt = tp(now)
			return []Envelope{evOverLimit(x, now)}, nil
		})
	}
}

// RetryUnmatchedReports 未匹配的运营商报告再试一次（恢复回放后、或订单晚于报告入库时）。
func (s *Service) RetryUnmatchedReports(ctx context.Context) {
	rows, err := s.store.pool.Query(ctx, `SELECT body FROM ride_reports WHERE NOT applied ORDER BY created_at LIMIT 500`)
	if err != nil {
		return
	}
	var list []RideEvent
	for rows.Next() {
		var b []byte
		if rows.Scan(&b) == nil {
			var e RideEvent
			if json.Unmarshal(b, &e) == nil {
				list = append(list, e)
			}
		}
	}
	rows.Close()
	for i := range list {
		_, _ = s.applyReport(ctx, &list[i])
	}
}

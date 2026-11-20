package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"bike/ride/internal/clients/opgw"
	"bike/ride/internal/geo"
	"bike/ride/internal/rules"
)

// APIError 对外错误（统一错误体）。
type APIError struct {
	Status int
	Body   map[string]any
}

func (e *APIError) Error() string { return fmt.Sprint(e.Body["code"], ":", e.Body["message"]) }

func apiErr(status int, code, msg string, extra map[string]any) *APIError {
	f := rules.NewFail(code, msg, extra)
	return &APIError{Status: status, Body: f.Body()}
}

func failErr(status int, f *rules.Fail) *APIError { return &APIError{Status: status, Body: f.Body()} }

func busy(err error) *APIError {
	return apiErr(500, "SERVER_BUSY", "", nil)
}

// failStatus 准入失败的 HTTP 状态：未登录 401，其余 409。
func failStatus(code string) int {
	if code == "NOT_LOGGED_IN" {
		return 401
	}
	return 409
}

// vehicleBlockWithNearby 车辆本身不可租时附上附近可租车（暂停可租只含其他品牌）。
func (s *Service) vehicleBlockWithNearby(ctx context.Context, v *rules.Vehicle) *rules.Fail {
	b := s.snap.brand(v.BrandID)
	f := rules.VehicleBlock(*v, b)
	if f == nil {
		return nil
	}
	near := []map[string]any{}
	if v.Location != nil {
		other := ""
		if f.Code == "BRAND_PAUSED" {
			other = v.BrandID
		}
		near = s.nearby(ctx, *v.Location, v.VehicleID, 0, other)
	}
	if f.Extra == nil {
		f.Extra = map[string]any{}
	}
	f.Extra["nearby"] = near
	return f
}

// Scan 扫码确认：只读本地准入快照；不因不能开锁而失败，原因放在 check。
func (s *Service) Scan(ctx context.Context, userID, code string) (map[string]any, *APIError) {
	if code == "" {
		return nil, apiErr(400, "INVALID", "请提供扫码内容", nil)
	}
	v := s.snap.findVehicle(code)
	if v == nil {
		return nil, apiErr(404, "CODE_NOT_FOUND", "", nil)
	}
	b := s.snap.brand(v.BrandID)
	ver := s.snap.versionAt(s.now())
	vb := s.vehicleBlockWithNearby(ctx, v)
	var check map[string]any
	var adm *rules.Admission
	degIn, _ := s.degradeInput(ctx, userID)
	if vb != nil {
		check = vb.Body()
		check["ok"] = false
	} else {
		riding := ""
		if userID != "" {
			if o, err := s.activeOrderOfUser(ctx, userID); err == nil && o != nil {
				riding = o.OrderID
			}
		}
		a, f := rules.UserCheck(rules.UserCheckInput{User: s.snap.user(userID), LoggedIn: userID != "", RidingOrderID: riding, Version: ver, Degrade: degIn})
		if f != nil {
			check = f.Body()
			check["ok"] = false
		} else {
			check = map[string]any{"ok": true}
			adm = a
		}
	}
	res := map[string]any{
		"vehicle": s.vehicleView(v, b),
		"brand":   brandView(v, b),
		"price":   priceSummary(ver),
		"degrade": map[string]any{"active": degIn.Active, "line": nil, "admission": adm},
		"check":   check,
		"nearby":  []map[string]any{},
	}
	if degIn.Active {
		res["degrade"].(map[string]any)["line"] = "费用将稍后收取"
	}
	if vb != nil {
		res["nearby"] = vb.Extra["nearby"]
	}
	return res, nil
}

func (s *Service) vehicleView(v *rules.Vehicle, b *rules.Brand) map[string]any {
	m := map[string]any{"vehicleId": v.VehicleID, "govCode": v.GovCode, "plateNo": v.PlateNo, "model": v.Model, "battery": nil,
		"location": v.Location, "stationId": nil, "stationName": nil, "brandId": v.BrandID}
	if v.StationID != "" {
		m["stationId"] = v.StationID
		if st := s.snap.station(v.StationID); st != nil {
			m["stationName"] = st.Name
		}
	}
	if b != nil {
		m["brandName"], m["displayName"], m["color"], m["vehicleType"] = b.Name, b.DisplayName, b.Color, b.VehicleType
	}
	return m
}

func brandView(v *rules.Vehicle, b *rules.Brand) map[string]any {
	if b == nil {
		return map[string]any{"brandId": v.BrandID, "name": "", "accessStatus": "未接入"}
	}
	return map[string]any{"brandId": b.BrandID, "name": b.Name, "displayName": b.DisplayName, "operatorId": b.OperatorID,
		"vehicleType": b.VehicleType, "color": b.Color, "accessStatus": b.AccessStatus}
}

// Nearby 附近可租车。
func (s *Service) Nearby(ctx context.Context, vehicleID string, center *geo.Pt, radius int) ([]map[string]any, *APIError) {
	if center == nil && vehicleID != "" {
		if v := s.snap.vehicle(vehicleID); v != nil && v.Location != nil {
			center = v.Location
		}
	}
	if center == nil {
		return nil, apiErr(400, "INVALID", "缺少位置", nil)
	}
	return s.nearby(ctx, *center, vehicleID, radius, ""), nil
}

// unlockTimeout 开锁报告时限。
func (s *Service) unlockTimeout() time.Duration {
	if s.opt.UnlockTimeoutMin > 0 {
		return s.opt.UnlockTimeoutMin
	}
	return time.Duration(s.snap.getParams().UnlockReportTimeoutSec) * time.Second
}

// Unlock 开锁：按判定顺序准入 → 建订单 → 请求运营商开锁 → 等运营商报告或时限届满。
func (s *Service) Unlock(ctx context.Context, userID, code string) (map[string]any, *APIError) {
	if code == "" {
		return nil, apiErr(400, "INVALID", "请提供扫码内容", nil)
	}
	v := s.snap.findVehicle(code)
	if v == nil {
		return nil, apiErr(404, "CODE_NOT_FOUND", "", nil)
	}
	if f := s.vehicleBlockWithNearby(ctx, v); f != nil {
		return nil, failErr(409, f)
	}
	if userID == "" {
		return nil, apiErr(401, "NOT_LOGGED_IN", "", nil)
	}
	riding, err := s.activeOrderOfUser(ctx, userID)
	if err != nil {
		return nil, busy(err)
	}
	ridingID := ""
	if riding != nil {
		ridingID = riding.OrderID
	}
	now := s.nowTS()
	ver := s.snap.versionAt(now)
	if ver == nil {
		return nil, apiErr(500, "SERVER_BUSY", "价格规则尚未同步，请稍后再试", nil)
	}
	degIn, ctl := s.degradeInput(ctx, userID)
	u := s.snap.user(userID)
	adm, f := rules.UserCheck(rules.UserCheckInput{User: u, LoggedIn: true, RidingOrderID: ridingID, Version: ver, Degrade: degIn})
	if f != nil {
		return nil, failErr(failStatus(f.Code), f)
	}
	b := s.snap.brand(v.BrandID)
	id, err := s.newOrderID(ctx, now)
	if err != nil {
		return nil, busy(err)
	}
	o := &Order{OrderID: id, UserID: userID, VehicleID: v.VehicleID, BrandID: v.BrandID, PlateNo: v.PlateNo, GovCode: v.GovCode,
		OperatorID: v.OperatorID, PricingVersionID: ver.VersionID, Status: StUnlocking, RequestAt: now, Rev: 1}
	if u != nil {
		o.Channel = u.Channel
	}
	if b != nil {
		o.BrandName, o.DisplayName, o.OperatorName = b.Name, b.DisplayName, b.OperatorShortName
		if o.OperatorID == "" {
			o.OperatorID = b.OperatorID
		}
	}
	if v.Location != nil {
		loc := *v.Location
		o.StartLocation = &loc
		o.UnlockLocation = &LatLng{Lat: loc[1], Lng: loc[0]}
	}
	if v.StationID != "" {
		o.StartStationID = sp(v.StationID)
		if st := s.snap.station(v.StationID); st != nil {
			o.StartStationName = sp(st.Name)
		}
	}
	if adm != nil {
		var evID *string
		if ctl.Active && ctl.EventID != "" {
			evID = sp(ctl.EventID)
		}
		o.Degraded = &DegradedMark{EventID: evID, Admission: adm.Category, LimitMinutes: adm.LimitMinutes, LimitAmount: adm.LimitAmount, FeeLater: true, Line: adm.Line}
	}
	userActor := Actor{Kind: "用户", ID: userID}
	if err := s.saveOrder(ctx, o, true, []Envelope{evCreated(o, userActor)}); err != nil {
		var ue *ErrUnique
		if errors.As(err, &ue) {
			if ue.Constraint == "orders_user_active" {
				cur, _ := s.activeOrderOfUser(ctx, userID)
				ex := map[string]any{}
				if cur != nil {
					ex["orderId"] = cur.OrderID
				}
				return nil, apiErr(409, "HAS_RIDING", "", ex)
			}
			// 这辆车已有进行中的订单：以运营商为准记一笔"车辆被占用"的开锁失败单（BR-04）
			return s.occupiedFail(ctx, o, userActor)
		}
		return nil, busy(err)
	}
	ch := s.waitChan(o.OrderID)
	defer s.dropWaiter(o.OrderID, ch)

	cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	res, _ := s.opgw.Unlock(cctx, opgw.UnlockCommand{Target: opgw.Target{OrderID: o.OrderID, OperatorID: o.OperatorID, BrandID: o.BrandID, VehicleID: o.VehicleID, PlateNo: o.PlateNo}, Degraded: o.Degraded != nil})
	cancel()
	if res.Result == opgw.Rejected {
		reason := rejectReason(res.RejectReason)
		_, _ = s.mutate(ctx, o.OrderID, func(x *Order) ([]Envelope, error) {
			if x.Status != StUnlocking {
				return nil, errNoChange
			}
			t := s.nowTS()
			x.Status, x.UnlockResult, x.UnlockFailReason, x.UnlockReportAt = StFailed, sp(UnlockFail), sp(reason), &t
			return []Envelope{evUnlockFailed(x, t)}, nil
		})
	}
	// 不可达时仍等报告：运营商可能已执行，以运营商报告为准；时限届满未报视为失败（BR-02）
	deadline := o.RequestAt.Add(s.unlockTimeout())
	final := s.waitUnlock(ctx, o.OrderID, deadline, ch)
	return s.unlockOutcome(ctx, final)
}

func rejectReason(r *string) string {
	if r == nil {
		return ReasonFault
	}
	switch *r {
	case ReasonNoResponse, ReasonLowBattery, ReasonFault, ReasonOccupied:
		return *r
	}
	return ReasonFault
}

// waitUnlock 等订单离开"开锁中"；时限届满仍未报告，判开锁失败（车辆无响应）。
func (s *Service) waitUnlock(ctx context.Context, id string, deadline time.Time, ch chan struct{}) *Order {
	tick := time.NewTicker(s.opt.PollInterval)
	defer tick.Stop()
	for {
		o, err := s.loadOrder(ctx, id)
		if err == nil && o.Status != StUnlocking {
			return o
		}
		if !s.now().Before(deadline) {
			o2, err := s.timeoutUnlock(ctx, id)
			if err == nil {
				return o2
			}
			return o
		}
		select {
		case <-ch:
		case <-tick.C:
		case <-ctx.Done():
			// 请求中断：订单继续由报告或定时清理推进，页面改查当前订单
			o, _ := s.loadOrder(context.Background(), id)
			return o
		}
	}
}

// timeoutUnlock 开锁报告时限届满：开锁中 → 开锁失败（车辆无响应），不计费。
func (s *Service) timeoutUnlock(ctx context.Context, id string) (*Order, error) {
	return s.mutate(ctx, id, func(x *Order) ([]Envelope, error) {
		if x.Status != StUnlocking {
			return nil, errNoChange
		}
		t := s.nowTS()
		x.Status, x.UnlockResult, x.UnlockFailReason, x.UnlockReportAt = StFailed, sp(UnlockFail), sp(ReasonNoResponse), nil
		return []Envelope{evUnlockFailed(x, t)}, nil
	})
}

var failCodes = map[string]string{ReasonNoResponse: "UNLOCK_NO_RESPONSE", ReasonLowBattery: "UNLOCK_LOW_BATTERY", ReasonFault: "UNLOCK_FAULT", ReasonOccupied: "UNLOCK_OCCUPIED"}

// unlockOutcome 开锁接口的返回：骑行中返回骑行视图；失败给一句原因、附近车与下一步。
func (s *Service) unlockOutcome(ctx context.Context, o *Order) (map[string]any, *APIError) {
	if o == nil {
		return nil, apiErr(500, "SERVER_BUSY", "", nil)
	}
	switch o.Status {
	case StRiding:
		s.linkAfterFail(ctx, o)
		return s.rideView(o), nil
	case StUnlocking:
		return nil, apiErr(500, "SERVER_BUSY", "开锁结果还在确认中，请稍后查看当前订单", map[string]any{"orderId": o.OrderID})
	}
	reason := ReasonNoResponse
	if o.UnlockFailReason != nil {
		reason = *o.UnlockFailReason
	}
	code := failCodes[reason]
	extra := map[string]any{"orderId": o.OrderID}
	if o.UnlockResult != nil && *o.UnlockResult == UnlockLate {
		extra["lateSuccess"] = true
		code = "UNLOCK_NO_RESPONSE"
	}
	near := []map[string]any{}
	if v := s.snap.vehicle(o.VehicleID); v != nil && v.Location != nil {
		near = s.nearby(ctx, *v.Location, o.VehicleID, 0, "")
	}
	extra["nearby"] = near
	base := rules.DefaultMessage(code)
	msg := base + "。附近暂时没有可用车"
	if len(near) > 0 {
		msg = fmt.Sprintf("%s。附近还有 %d 辆可用车", base, len(near))
	}
	f := rules.NewFail(code, msg, extra)
	if len(near) == 0 {
		f.Next = &rules.Next{Label: "重新扫码", Action: "rescan"}
	}
	if o.FailNotifiedAt == nil {
		_, _ = s.mutate(ctx, o.OrderID, func(x *Order) ([]Envelope, error) {
			if x.FailNotifiedAt != nil {
				return nil, errNoChange
			}
			x.FailNotifiedAt = tp(s.now())
			return nil, nil
		})
	}
	return nil, failErr(409, f)
}

// occupiedFail 车被占用：记一笔开锁失败单。
func (s *Service) occupiedFail(ctx context.Context, o *Order, actor Actor) (map[string]any, *APIError) {
	t := s.nowTS()
	o.Status, o.UnlockResult, o.UnlockFailReason, o.UnlockReportAt = StFailed, sp(UnlockFail), sp(ReasonOccupied), &t
	if err := s.saveOrder(ctx, o, true, []Envelope{evCreated(o, actor), evUnlockFailed(o, t)}); err != nil {
		return nil, busy(err)
	}
	return s.unlockOutcome(ctx, o)
}

// linkAfterFail 本单是 15 分钟内失败单之后换车成功的订单，在失败单上记下。
func (s *Service) linkAfterFail(ctx context.Context, o *Order) {
	if !s.dbOK() {
		return
	}
	prev, err := s.store.queryOrders(ctx, `SELECT doc, rev FROM orders WHERE user_id=$1 AND status='开锁失败' AND request_at > $2 AND request_at < $3
		AND doc->>'afterFailOrderId' IS NULL ORDER BY request_at DESC LIMIT 1`, o.UserID, o.RequestAt.Add(-15*time.Minute), o.RequestAt)
	if err != nil || len(prev) == 0 {
		return
	}
	_, _ = s.mutate(ctx, prev[0].OrderID, func(x *Order) ([]Envelope, error) {
		if x.AfterFailOrderID != nil {
			return nil, errNoChange
		}
		x.AfterFailOrderID = sp(o.OrderID)
		return nil, nil
	})
}

// rideView 骑行中订单视图（RideView）。实时费用估算归 bill，这里不给。
func (s *Service) rideView(o *Order) map[string]any {
	m := o.userView()
	now := s.now()
	var elapsed int64
	if o.StartAt != nil {
		elapsed = int64(now.Sub(*o.StartAt).Seconds())
		if elapsed < 0 {
			elapsed = 0
		}
	}
	m["elapsedSec"] = elapsed
	ver := s.snap.version(o.PricingVersionID)
	m["price"] = priceSummary(ver)
	m["battery"] = nil
	cur := s.currentLocation(o)
	m["currentLocation"] = cur
	m["nearestStation"] = nil
	if cur != nil {
		st, piles := s.snap.geoData()
		if n := rules.Nearest(st, *cur); n != nil {
			m["nearestStation"] = map[string]any{"stationId": n.Station.StationID, "name": n.Station.Name, "type": n.Station.Type,
				"distanceM": n.DistanceM, "center": n.Station.Center, "freePiles": len(rules.FreePiles(piles, n.Station.StationID, ""))}
		}
	}
	m["overLimit"] = nil
	if d := o.Degraded; d != nil {
		reached := d.OverLimitRemindedAt != nil || elapsed > int64(d.LimitMinutes)*60 || (ver != nil && rules.EstimateRideFee(ver, elapsed) > d.LimitAmount)
		m["overLimit"] = map[string]any{"reached": reached, "remindedAt": d.OverLimitRemindedAt,
			"text": fmt.Sprintf("本次最长 %d 分钟或 %s，到时请尽快还车，骑行不会被中断", d.LimitMinutes, rules.Yuan(d.LimitAmount))}
	}
	return m
}

// currentLocation 车辆最近一次已知位置：本服务只从开锁位置与锁车报告得到位置（轨迹不经过本服务）。
func (s *Service) currentLocation(o *Order) *geo.Pt {
	if o.StartLocation != nil {
		p := *o.StartLocation
		return &p
	}
	if v := s.snap.vehicle(o.VehicleID); v != nil && v.Location != nil {
		p := *v.Location
		return &p
	}
	return nil
}

// Current 当前骑行中订单；没有返回 nil。
func (s *Service) Current(ctx context.Context, userID string) (map[string]any, *APIError) {
	o, err := s.activeOrderOfUser(ctx, userID)
	if err != nil {
		return nil, busy(err)
	}
	if o == nil || o.Status != StRiding {
		return nil, nil
	}
	return s.rideView(o), nil
}

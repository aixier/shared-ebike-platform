package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/ops/internal/httpx"
	"bike/ops/internal/metrics"
)

// 故障预设（与原型 system.js DEGRADE_PRESETS 一致）。
var degradePresets = map[string]struct {
	Cause      string
	Components []string
}{
	"支付通道故障":     {"支付通道故障（微信支付回调持续超时）", []string{"支付通道"}},
	"实名核验不可用":    {"实名核验服务不可用（第三方核验接口连续超时）", []string{"实名核验"}},
	"押金与免押查询不可用": {"押金与免押查询服务不可用", []string{"押金与免押查询"}},
	"授权不可用":      {"微信与支付宝授权接口不可用", []string{"授权"}},
}

// 故障组件统一取值（ops.yaml FaultComponent 与 events/ops.md components 同一集合）→ 旧名兼容：
// 请求里仍接受旧页面名，一律折算成统一取值再存、再发。
var componentAlias = map[string]string{
	"支付通道": "支付通道", "实名核验": "实名核验", "押金与免押查询": "押金与免押查询", "授权": "授权", "数据库": "数据库", "计费": "计费",
	"微信支付收款": "支付通道", "支付宝收款": "支付通道", "微信授权": "授权", "支付宝授权": "授权",
}

// normalizeComponents 折算为统一取值并去重（保持先后顺序）；遇到未知组件返回它。
func normalizeComponents(comps []string) ([]string, string) {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range comps {
		n, ok := componentAlias[c]
		if !ok {
			return nil, c
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, ""
}

// TimelineEntry 时间线。
type TimelineEntry struct {
	At     string `json:"at"`
	By     string `json:"by"`
	ByName string `json:"byName,omitempty"`
	ByKind string `json:"byKind"`
	Action string `json:"action"`
	Text   string `json:"text"`
}

// DegradeEvent 降级事件（ops.yaml DegradeEvent）。
type DegradeEvent struct {
	EventID           string              `json:"eventId"`
	Cause             string              `json:"cause"`
	Components        []string            `json:"components"`
	EnteredAt         string              `json:"enteredAt"`
	ExitedAt          *string             `json:"exitedAt"`
	DurationMinutes   *int                `json:"durationMinutes"`
	EnteredBy         string              `json:"enteredBy"`
	EnteredByName     string              `json:"enteredByName,omitempty"`
	ExitedBy          *string             `json:"exitedBy"`
	ExitedByName      *string             `json:"exitedByName"`
	Status            string              `json:"status"`
	OrderIDs          []string            `json:"orderIds"`
	OrderCount        int                 `json:"orderCount"`
	ReceivableSum     int64               `json:"receivableSum"`
	AdmissionCounts   map[string]int      `json:"admissionCounts"`
	LimitsVersionID   string              `json:"limitsVersionId"`
	Limits            RiskLimits          `json:"limits"`
	MaxHourlyOrders   int                 `json:"maxHourlyOrders"`
	MaxUserOpenOrders int                 `json:"maxUserOpenOrders"`
	OverLimitReminders int                `json:"overLimitReminders"`
	Closure           EventClosureSummary `json:"closure"`
	Timeline          []TimelineEntry     `json:"timeline"`

	tightened, stopped bool
	enteredAt          time.Time
}

// DegradeEventDetail 事件加逐单闭合项。
type DegradeEventDetail struct {
	DegradeEvent
	Orders []ClosureItem `json:"orders"`
}

// DegradeUsage 限额使用量。
type DegradeUsage struct {
	LastHourOrders  int   `json:"lastHourOrders"`
	HourlyLimit     int   `json:"hourlyLimit"`
	Orders          int   `json:"orders"`
	Riding          int   `json:"riding"`
	AmountSoFar     int64 `json:"amountSoFar"`
	OverLimitRiding int   `json:"overLimitRiding"`
}

// DegradeStatus 降级与限额使用情况。
type DegradeStatus struct {
	Degraded           bool          `json:"degraded"`
	Tightened          bool          `json:"tightened"`
	Stopped            bool          `json:"stopped"`
	Cause              *string       `json:"cause"`
	Components         []string      `json:"components"`
	EventID            *string       `json:"eventId"`
	LastDegradeEventID *string       `json:"lastDegradeEventId"`
	Event              *DegradeEvent `json:"event"`
	Minutes            int           `json:"minutes"`
	Usage              DegradeUsage  `json:"usage"`
	Limits             RiskLimits    `json:"limits"`
	LimitsVersionID    string        `json:"limitsVersionId"`
}

// PublicDegrade 公开的降级状态。
type PublicDegrade struct {
	Active     bool     `json:"active"`
	EventID    *string  `json:"eventId"`
	Cause      *string  `json:"cause"`
	Components []string `json:"components"`
	Tightened  bool     `json:"tightened"`
	Stopped    bool     `json:"stopped"`
}

// EnterRequest 进入降级。
type EnterRequest struct {
	Preset     string   `json:"preset"`
	Cause      string   `json:"cause"`
	Components []string `json:"components"`
	Reason     *string  `json:"reason"`
}

func (a *App) addTimeline(ctx context.Context, tx pgx.Tx, eventID string, by Actor, action, text string) error {
	_, err := tx.Exec(ctx, `INSERT INTO degrade_timeline (event_id, at, by, by_name, by_kind, action, text) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		eventID, a.now(), by.ID, by.Name, by.Kind, action, text)
	return err
}

const eventCols = `event_id, cause, components, entered_at, exited_at, entered_by, entered_by_name, exited_by, exited_by_name,
	status, tightened, stopped, limits_version_id, limits, max_hourly_orders, max_user_open_orders`

func (a *App) scanEvent(row pgx.Row) (DegradeEvent, error) {
	var e DegradeEvent
	var exitedAt *time.Time
	var limits []byte
	var enteredByName *string
	err := row.Scan(&e.EventID, &e.Cause, &e.Components, &e.enteredAt, &exitedAt, &e.EnteredBy, &enteredByName, &e.ExitedBy, &e.ExitedByName,
		&e.Status, &e.tightened, &e.stopped, &e.LimitsVersionID, &limits, &e.MaxHourlyOrders, &e.MaxUserOpenOrders)
	if err != nil {
		return e, err
	}
	if enteredByName != nil {
		e.EnteredByName = *enteredByName
	}
	e.EnteredAt = a.fmtTime(e.enteredAt)
	e.ExitedAt = a.fmtTimePtr(exitedAt)
	if exitedAt != nil {
		d := int(math.Round(exitedAt.Sub(e.enteredAt).Minutes()))
		e.DurationMinutes = &d
	}
	_ = json.Unmarshal(limits, &e.Limits)
	if e.Components == nil {
		e.Components = []string{}
	}
	return e, nil
}

// fillEvent 补齐订单统计、闭合汇总与时间线。
func (a *App) fillEvent(ctx context.Context, tx pgx.Tx, e *DegradeEvent, now time.Time) ([]orderRow, error) {
	orders, err := queryOrders(ctx, tx, `WHERE degrade_event_id = $1 ORDER BY request_at NULLS LAST, order_id`, e.EventID)
	if err != nil {
		return nil, err
	}
	e.OrderIDs = []string{}
	e.AdmissionCounts = map[string]int{}
	for _, o := range orders {
		e.OrderIDs = append(e.OrderIDs, o.OrderID)
		if o.Receivable != nil {
			e.ReceivableSum += *o.Receivable
		}
		if o.Admission != nil {
			e.AdmissionCounts[*o.Admission]++
		}
		if o.OverLimitRemindedAt != nil {
			e.OverLimitReminders++
		}
	}
	e.OrderCount = len(orders)
	if e.Closure, err = a.eventClosure(ctx, tx, e.Status == "进行中", orders, now); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT at, by, by_name, by_kind, action, text FROM degrade_timeline WHERE event_id = $1 ORDER BY at, id`, e.EventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	e.Timeline = []TimelineEntry{}
	for rows.Next() {
		var t TimelineEntry
		var at time.Time
		var name *string
		if err := rows.Scan(&at, &t.By, &name, &t.ByKind, &t.Action, &t.Text); err != nil {
			return nil, err
		}
		t.At = a.fmtTime(at)
		if name != nil {
			t.ByName = *name
		}
		e.Timeline = append(e.Timeline, t)
	}
	return orders, rows.Err()
}

func (a *App) activeEvent(ctx context.Context, tx pgx.Tx) (*DegradeEvent, error) {
	e, err := a.scanEvent(tx.QueryRow(ctx, `SELECT `+eventCols+` FROM degrade_events WHERE status = '进行中'`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// Enter 进入降级（值班人手动或运行智能体宣布）。已在降级中时返回当前事件，不新建。
func (a *App) Enter(ctx context.Context, actor Actor, req EnterRequest) (DegradeEvent, error) {
	if req.Reason != nil && !httpx.ValidReason(*req.Reason) {
		return DegradeEvent{}, httpx.ReasonRequired()
	}
	cause, comps := strings.TrimSpace(req.Cause), req.Components
	if cause == "" || len(comps) == 0 {
		p, ok := degradePresets[req.Preset]
		if !ok {
			if req.Preset != "" {
				return DegradeEvent{}, httpx.Invalid("没有该故障预设：" + req.Preset)
			}
			return DegradeEvent{}, httpx.Invalid("请选择故障预设，或填写故障原因与故障组件")
		}
		if cause == "" {
			cause = p.Cause
		}
		if len(comps) == 0 {
			comps = p.Components
		}
	}
	comps, bad := normalizeComponents(comps)
	if bad != "" {
		return DegradeEvent{}, httpx.Invalid("没有该故障组件：" + bad)
	}
	var out DegradeEvent
	err := a.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `LOCK TABLE degrade_events IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		cur, err := a.activeEvent(ctx, tx)
		if err != nil {
			return err
		}
		now := a.now()
		if cur != nil {
			_, err = a.fillEvent(ctx, tx, cur, now)
			out = *cur
			return err
		}
		versionID, limits, err := a.effectiveLimits(ctx, tx, now)
		if err != nil {
			return err
		}
		id, err := a.nextID(ctx, tx, "OPS-DG", now)
		if err != nil {
			return err
		}
		lj, _ := json.Marshal(limits)
		if _, err := tx.Exec(ctx, `INSERT INTO degrade_events (event_id, cause, components, entered_at, entered_by, entered_by_name, status, limits_version_id, limits)
			VALUES ($1,$2,$3,$4,$5,$6,'进行中',$7,$8)`, id, cause, comps, now, actor.ID, actor.Name, versionID, lj); err != nil {
			return err
		}
		// ride 已报熔断的依赖记作"检测到故障"
		var tripped []string
		rows, err := tx.Query(ctx, `SELECT DISTINCT component FROM dependency_states WHERE state = '熔断' ORDER BY component`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			_ = rows.Scan(&c)
			tripped = append(tripped, c)
		}
		rows.Close()
		if len(tripped) > 0 {
			if err := a.addTimeline(ctx, tx, id, SystemActor, "检测到故障", "骑行报告依赖熔断："+strings.Join(tripped, "、")); err != nil {
				return err
			}
		}
		text := cause + "；故障组件：" + strings.Join(comps, "、")
		if req.Reason != nil {
			text += "（" + strings.TrimSpace(*req.Reason) + "）"
		}
		if err := a.addTimeline(ctx, tx, id, actor, "降级进入", text); err != nil {
			return err
		}
		// 宣布前 ride 已自主开出的降级单并入本事件
		if _, err := tx.Exec(ctx, `UPDATE orders SET degrade_event_id = $1 WHERE degraded AND degrade_event_id IS NULL AND closed_at IS NULL`, id); err != nil {
			return err
		}
		eid := id
		if err := a.raiseAlert(ctx, tx, "严重", AlertDegradeEnter, "已进入降级："+text, nil, &eid, ""); err != nil {
			return err
		}
		evComps := comps
		if err := a.emit(ctx, tx, "ops.degrade.entered", "degrade", actor, map[string]any{
			"eventId": id, "cause": cause, "components": evComps, "enteredAt": a.fmtTime(now), "limitsVersionId": versionID,
		}); err != nil {
			return err
		}
		if err := a.audit(ctx, tx, actor, "进入降级", "degrade", id, "进入降级："+cause, req.Reason, nil, map[string]any{"components": comps}); err != nil {
			return err
		}
		e, err := a.scanEvent(tx.QueryRow(ctx, `SELECT `+eventCols+` FROM degrade_events WHERE event_id = $1`, id))
		if err != nil {
			return err
		}
		if _, err := a.fillEvent(ctx, tx, &e, now); err != nil {
			return err
		}
		out = e
		return nil
	})
	return out, err
}


// Exit 退出降级。
func (a *App) Exit(ctx context.Context, actor Actor, reason *string) (DegradeEvent, error) {
	if reason != nil && !httpx.ValidReason(*reason) {
		return DegradeEvent{}, httpx.ReasonRequired()
	}
	var out DegradeEvent
	err := a.tx(ctx, func(tx pgx.Tx) error {
		cur, err := a.activeEvent(ctx, tx)
		if err != nil {
			return err
		}
		if cur == nil {
			return httpx.New(409, "DEGRADE_NOT_ACTIVE", "当前不在降级中")
		}
		now := a.now()
		if _, err := tx.Exec(ctx, `UPDATE degrade_events SET status = '已退出', exited_at = $2, exited_by = $3, exited_by_name = $4,
			tightened = false, stopped = false WHERE event_id = $1`, cur.EventID, now, actor.ID, actor.Name); err != nil {
			return err
		}
		dur := int(math.Round(now.Sub(cur.enteredAt).Minutes()))
		text := fmt.Sprintf("故障恢复，退出降级，持续 %d 分钟；各模块开始逐单回放、计价、收款与补验实名", dur)
		if reason != nil {
			text += "（" + strings.TrimSpace(*reason) + "）"
		}
		if err := a.addTimeline(ctx, tx, cur.EventID, actor, "降级退出", text); err != nil {
			return err
		}
		eid := cur.EventID
		if err := a.raiseAlert(ctx, tx, "提示", AlertDegradeExit, fmt.Sprintf("降级 %s 已退出，持续 %d 分钟", eid, dur), nil, &eid, ""); err != nil {
			return err
		}
		if err := a.emit(ctx, tx, "ops.degrade.exited", "degrade", actor, map[string]any{
			"eventId": eid, "exitedAt": a.fmtTime(now), "durationMinutes": dur,
		}); err != nil {
			return err
		}
		if err := a.audit(ctx, tx, actor, "退出降级", "degrade", eid, fmt.Sprintf("退出降级，持续 %d 分钟", dur), reason, nil, nil); err != nil {
			return err
		}
		if err := a.refreshEventClosureTimeline(ctx, tx, eid); err != nil {
			return err
		}
		e, err := a.scanEvent(tx.QueryRow(ctx, `SELECT `+eventCols+` FROM degrade_events WHERE event_id = $1`, eid))
		if err != nil {
			return err
		}
		if _, err := a.fillEvent(ctx, tx, &e, now); err != nil {
			return err
		}
		out = e
		return nil
	})
	return out, err
}

// Control 值班人收紧、停止、放宽（kind 为 tighten、stop、relax）。没有值班人操作，不发收紧与停止。
func (a *App) Control(ctx context.Context, actor Actor, kind, reason string) (DegradeStatus, error) {
	if !httpx.ValidReason(reason) {
		return DegradeStatus{}, httpx.ReasonRequired()
	}
	type spec struct {
		action, text, typ, auditAction string
		tightened, stopped             *bool
	}
	t, f := true, false
	specs := map[string]spec{
		"tighten": {"降级收紧", "暂停押金状态未知的老用户与先骑后验新用户的降级开单", "ops.degrade.tightened", "收紧降级", &t, nil},
		"stop":    {"降级停止", "停止开新单，已开锁的订单不受影响", "ops.degrade.stopped", "停止降级", nil, &t},
		"relax":   {"降级放宽", "恢复正常降级开单", "ops.degrade.relaxed", "放宽降级", &f, &f},
	}
	s, ok := specs[kind]
	if !ok {
		return DegradeStatus{}, httpx.Invalid("未知操作")
	}
	err := a.tx(ctx, func(tx pgx.Tx) error {
		cur, err := a.activeEvent(ctx, tx)
		if err != nil {
			return err
		}
		if cur == nil {
			return httpx.New(409, "DEGRADE_NOT_ACTIVE", "当前不在降级中")
		}
		before := map[string]any{"tightened": cur.tightened, "stopped": cur.stopped}
		nt, ns := cur.tightened, cur.stopped
		if s.tightened != nil {
			nt = *s.tightened
		}
		if s.stopped != nil {
			ns = *s.stopped
		}
		if _, err := tx.Exec(ctx, `UPDATE degrade_events SET tightened = $2, stopped = $3 WHERE event_id = $1`, cur.EventID, nt, ns); err != nil {
			return err
		}
		r := strings.TrimSpace(reason)
		if err := a.addTimeline(ctx, tx, cur.EventID, actor, s.action, s.text+"（"+r+"）"); err != nil {
			return err
		}
		payload := map[string]any{"eventId": cur.EventID, "at": a.fmtTime(a.now()), "reason": r}
		if kind == "relax" {
			payload["to"] = "正常"
		}
		if err := a.emit(ctx, tx, s.typ, "degrade", actor, payload); err != nil {
			return err
		}
		return a.audit(ctx, tx, actor, s.auditAction, "degrade", cur.EventID, "值班人"+s.text, &r, before, map[string]any{"tightened": nt, "stopped": ns})
	})
	if err != nil {
		return DegradeStatus{}, err
	}
	return a.Status(ctx)
}

// Status 降级状态与限额使用量。
func (a *App) Status(ctx context.Context) (DegradeStatus, error) {
	var out DegradeStatus
	err := a.read(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = a.status(ctx, tx)
		return err
	})
	return out, err
}

func (a *App) status(ctx context.Context, tx pgx.Tx) (DegradeStatus, error) {
	now := a.now()
	s := DegradeStatus{Components: []string{}}
	versionID, limits, err := a.effectiveLimits(ctx, tx, now)
	if err != nil {
		return s, err
	}
	s.Limits, s.LimitsVersionID = limits, versionID
	var last string
	if err := tx.QueryRow(ctx, `SELECT event_id FROM degrade_events ORDER BY entered_at DESC LIMIT 1`).Scan(&last); err == nil {
		s.LastDegradeEventID = &last
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return s, err
	}
	usage, err := a.usage(ctx, tx, now, limits)
	if err != nil {
		return s, err
	}
	s.Usage = usage
	cur, err := a.activeEvent(ctx, tx)
	if err != nil || cur == nil {
		return s, err
	}
	orders, err := a.fillEvent(ctx, tx, cur, now)
	if err != nil {
		return s, err
	}
	s.Degraded, s.Tightened, s.Stopped = true, cur.tightened, cur.stopped
	s.Cause, s.Components, s.EventID, s.Event = &cur.Cause, cur.Components, &cur.EventID, cur
	s.Minutes = int(now.Sub(cur.enteredAt).Minutes())
	if err := a.eventUsage(ctx, tx, &s.Usage, orders, now); err != nil {
		return s, err
	}
	return s, nil
}

// usage 近 1 小时降级单量（与是否归属事件无关）。
func (a *App) usage(ctx context.Context, tx pgx.Tx, now time.Time, limits RiskLimits) (DegradeUsage, error) {
	u := DegradeUsage{HourlyLimit: limits.HourlyDegradedOrders}
	err := tx.QueryRow(ctx, `SELECT count(*) FROM orders WHERE degraded AND request_at > $1 AND request_at <= $2`, now.Add(-time.Hour), now).Scan(&u.LastHourOrders)
	return u, err
}

// eventUsage 本次降级累计单数、骑行中单数、累计金额（已出账应收 + 骑行中估算）、超限单数。
func (a *App) eventUsage(ctx context.Context, tx pgx.Tx, u *DegradeUsage, orders []orderRow, now time.Time) error {
	u.Orders = len(orders)
	for _, o := range orders {
		if o.Receivable != nil {
			u.AmountSoFar += *o.Receivable
			continue
		}
		if o.Status != "骑行中" || o.StartAt == nil {
			continue
		}
		u.Riding++
		if o.OverLimitRemindedAt != nil {
			u.OverLimitRiding++
		}
		if o.PricingVersionID == nil {
			continue
		}
		p, ok, err := a.ridePrice(ctx, tx, *o.PricingVersionID)
		if err != nil {
			return err
		}
		if ok {
			u.AmountSoFar += metrics.RideFee(p, metrics.BilledMinutes(int64(now.Sub(*o.StartAt).Seconds())))
		}
	}
	return nil
}

// trackDegradeMaxima 记录期间近 1 小时降级单量与单个用户未闭合降级单的最大值。
func (a *App) trackDegradeMaxima(ctx context.Context, tx pgx.Tx, orderID string) error {
	var eventID, userID *string
	if err := tx.QueryRow(ctx, `SELECT degrade_event_id, user_id FROM orders WHERE order_id = $1`, orderID).Scan(&eventID, &userID); err != nil {
		return err
	}
	if eventID == nil {
		return nil
	}
	now := a.now()
	var hourly, userOpen int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM orders WHERE degraded AND request_at > $1 AND request_at <= $2`, now.Add(-time.Hour), now).Scan(&hourly); err != nil {
		return err
	}
	if userID != nil {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM orders WHERE degraded AND user_id = $1 AND closed_at IS NULL`, *userID).Scan(&userOpen); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE degrade_events SET max_hourly_orders = GREATEST(max_hourly_orders, $2),
		max_user_open_orders = GREATEST(max_user_open_orders, $3) WHERE event_id = $1`, *eventID, hourly, userOpen)
	return err
}

// checkLimits 接近或达到近 1 小时限额、降级持续超过提醒分钟数时提醒值班人（每个事件每类一次）。
// 只提醒，不收紧也不停止：那是值班人的决定。
func (a *App) checkLimits(ctx context.Context, tx pgx.Tx) error {
	cur, err := a.activeEvent(ctx, tx)
	if err != nil || cur == nil {
		return err
	}
	now := a.now()
	_, limits, err := a.effectiveLimits(ctx, tx, now)
	if err != nil {
		return err
	}
	u, err := a.usage(ctx, tx, now, limits)
	if err != nil {
		return err
	}
	eid := cur.EventID
	if limit := limits.HourlyDegradedOrders; limit > 0 {
		near := int(math.Ceil(a.Cfg.LimitNearRatio * float64(limit)))
		switch {
		case u.LastHourOrders >= limit:
			if err := a.raiseAlert(ctx, tx, "严重", AlertLimitReached,
				fmt.Sprintf("近 1 小时降级单 %d 笔，已达到上限 %d 笔；请值班人决定是否收紧或停止", u.LastHourOrders, limit), nil, &eid, "limit-reach:"+eid); err != nil {
				return err
			}
		case u.LastHourOrders >= near:
			if err := a.raiseAlert(ctx, tx, "警告", AlertLimitNear,
				fmt.Sprintf("近 1 小时降级单 %d 笔，接近上限 %d 笔", u.LastHourOrders, limit), nil, &eid, "limit-near:"+eid); err != nil {
				return err
			}
		}
	}
	if m := limits.DegradeDurationAlertMinutes; m > 0 && now.Sub(cur.enteredAt) >= time.Duration(m)*time.Minute {
		if err := a.raiseAlert(ctx, tx, "警告", AlertDegradeTooLong,
			fmt.Sprintf("降级已持续 %d 分钟，超过提醒阈值 %d 分钟", int(now.Sub(cur.enteredAt).Minutes()), m), nil, &eid, "duration:"+eid); err != nil {
			return err
		}
	}
	return nil
}

// CheckLimits 定时任务入口。
func (a *App) CheckLimits(ctx context.Context) error {
	return a.tx(ctx, func(tx pgx.Tx) error { return a.checkLimits(ctx, tx) })
}

// Public 公开的降级状态。
func (a *App) Public(ctx context.Context) (PublicDegrade, error) {
	out := PublicDegrade{Components: []string{}}
	err := a.read(ctx, func(tx pgx.Tx) error {
		cur, err := a.activeEvent(ctx, tx)
		if err != nil || cur == nil {
			return err
		}
		out = PublicDegrade{Active: true, EventID: &cur.EventID, Cause: &cur.Cause, Components: cur.Components, Tightened: cur.tightened, Stopped: cur.stopped}
		return nil
	})
	return out, err
}

// ListEvents 降级事件，按进入时刻新到旧。
func (a *App) ListEvents(ctx context.Context) ([]DegradeEvent, error) {
	out := []DegradeEvent{}
	err := a.read(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+eventCols+` FROM degrade_events ORDER BY entered_at DESC`)
		if err != nil {
			return err
		}
		for rows.Next() {
			e, err := a.scanEvent(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, e)
		}
		rows.Close()
		now := a.now()
		for i := range out {
			if _, err := a.fillEvent(ctx, tx, &out[i], now); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// GetEvent 降级事件详情加逐单闭合项。
func (a *App) GetEvent(ctx context.Context, id string) (DegradeEventDetail, error) {
	var out DegradeEventDetail
	err := a.read(ctx, func(tx pgx.Tx) error {
		e, err := a.scanEvent(tx.QueryRow(ctx, `SELECT `+eventCols+` FROM degrade_events WHERE event_id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("没有找到该降级事件")
		}
		if err != nil {
			return err
		}
		now := a.now()
		orders, err := a.fillEvent(ctx, tx, &e, now)
		if err != nil {
			return err
		}
		sort.SliceStable(orders, func(i, j int) bool {
			ri, rj := orders[i].RequestAt, orders[j].RequestAt
			if ri == nil || rj == nil {
				return ri != nil
			}
			return ri.After(*rj)
		})
		out.DegradeEvent = e
		out.Orders = []ClosureItem{}
		for _, o := range orders {
			out.Orders = append(out.Orders, a.closureItem(o, now))
		}
		return nil
	})
	return out, err
}

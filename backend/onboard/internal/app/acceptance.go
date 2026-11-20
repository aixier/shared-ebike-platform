package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"bike/onboard/internal/auth"
	"bike/onboard/internal/clients/fleet"
	"bike/onboard/internal/content"

	"github.com/jackc/pgx/v5"
)

// 真车联调与验收。验收结论只由测试结果与真车联调记录决定（不变量），智能体只写摘要。
// trace: R-40 BR-37 R-41

// realStats 真车联调记录的核对结果。
type realStats struct {
	VehiclesRidden int
	Orders         int // 被受理的真车开锁请求
	Matched        int // 开锁成功且锁车报告匹配
	LockMatched    int
	TimeLimitOk    bool
	Problems       []string
}

func (a *App) computeRealStats(ctx context.Context, brandID string, p Params) (*realStats, error) {
	rows, err := a.DB.Query(ctx, `SELECT order_id, plate_no, unlock_requested_at, COALESCE(unlock_reply_ms,0) FROM sandbox_orders
		WHERE brand_id=$1 AND kind='真车' AND unlock_reply='已受理' ORDER BY created_at`, brandID)
	if err != nil {
		return nil, err
	}
	type ord struct {
		id, plate string
		reqAt     time.Time
		replyMs   int
	}
	var list []ord
	for rows.Next() {
		var o ord
		var req *time.Time
		if err := rows.Scan(&o.id, &o.plate, &req, &o.replyMs); err != nil {
			rows.Close()
			return nil, err
		}
		if req != nil {
			o.reqAt = *req
		}
		list = append(list, o)
	}
	rows.Close()
	s := &realStats{TimeLimitOk: len(list) > 0}
	plates := map[string]bool{}
	noLock, late := 0, 0
	for _, o := range list {
		s.Orders++
		ds, err := a.deliveries(ctx, o.id)
		if err != nil {
			return nil, err
		}
		un, lk := firstAccepted(ds, "开锁结果"), firstAccepted(ds, "锁车")
		unlockOk := un != nil && un.Body.Unlock != nil && un.Body.Unlock.Result == "成功"
		lockOk := lk != nil && (un == nil || !lk.OccurredAt.Before(un.OccurredAt))
		if lockOk {
			s.LockMatched++
		} else {
			noLock++
		}
		if unlockOk && lockOk {
			s.Matched++
			plates[o.plate] = true
		}
		inTime := time.Duration(o.replyMs)*time.Millisecond <= a.Cfg.ReplyLimit && un != nil && un.ReceivedAt.Sub(o.reqAt) <= p.UnlockTimeout
		if !inTime {
			s.TimeLimitOk = false
			late++
		}
	}
	s.VehiclesRidden = len(plates)
	if s.VehiclesRidden < p.RealVehicles {
		s.Problems = append(s.Problems, fmt.Sprintf("已实骑真车 %d 辆，参数要求 %d 辆", s.VehiclesRidden, p.RealVehicles))
	}
	if s.Orders < p.RealOrders {
		s.Problems = append(s.Problems, fmt.Sprintf("真车联调订单 %d 单，参数要求 %d 单", s.Orders, p.RealOrders))
	}
	if s.Matched < s.Orders {
		s.Problems = append(s.Problems, fmt.Sprintf("%d 单中 %d 单订单与开锁、锁车报告匹配", s.Orders, s.Matched))
	}
	if noLock > 0 {
		s.Problems = append(s.Problems, fmt.Sprintf("%d 单没有匹配的锁车报告", noLock))
	}
	if late > 0 {
		s.Problems = append(s.Problems, fmt.Sprintf("%d 单开锁答复或开锁结果超出时限", late))
	}
	return s, nil
}

func (a *App) acceptanceView(ctx context.Context, r *integRow, p Params) (*AcceptanceView, error) {
	v := &AcceptanceView{}
	var runID string
	err := a.DB.QueryRow(ctx, `SELECT report_id, real_vehicles_required, real_vehicles_ridden, real_orders, matched_orders, lock_reports_matched,
		time_limit_ok, run_id, submitted_at, submitted_by, result, confirmed_by, confirmed_at, summary
		FROM reports WHERE integration_id=$1 ORDER BY submitted_at DESC LIMIT 1`, r.IntegrationID).
		Scan(&v.ReportID, &v.RealVehiclesRequired, &v.RealVehiclesRidden, &v.RealOrders, &v.MatchedOrders, &v.LockReportsMatched,
			&v.TimeLimitOk, &runID, &v.SubmittedAt, &v.SubmittedBy, &v.Result, &v.ConfirmedBy, &v.ConfirmedAt, &v.Summary)
	if err == nil {
		v.ConformanceRunID = &runID
		return v, nil
	}
	if !isNoRows(err) {
		return nil, err
	}
	// 还没有验收报告：按当前记录显示进度
	s, err := a.computeRealStats(ctx, r.BrandID, p)
	if err != nil {
		return nil, err
	}
	v.Result = "未开始"
	v.RealVehiclesRequired = p.RealVehicles
	v.RealVehiclesRidden, v.RealOrders, v.MatchedOrders, v.LockReportsMatched, v.TimeLimitOk = s.VehiclesRidden, s.Orders, s.Matched, s.LockMatched, s.TimeLimitOk
	if lr, err := a.latestRun(ctx, r.IntegrationID); err == nil && lr != nil {
		v.ConformanceRunID = &lr.RunID
	}
	return v, nil
}

// requireRealRideReady 真车联调的前置条件：符合性测试最新一轮全部通过、联调中、凭证有效。
func (a *App) requireRealRideReady(ctx context.Context, in *integRow) (*Run, error) {
	lr, err := a.latestRun(ctx, in.IntegrationID)
	if err != nil {
		return nil, err
	}
	if lr == nil || lr.Summary.Failed > 0 {
		return nil, errf(409, "ACCEPTANCE_NOT_READY", "符合性测试全部通过后才能进行真车联调")
	}
	if in.Status != "联调中" {
		return nil, errf(409, "CONFLICT", fmt.Sprintf("当前接入状态为%s，不在联调中", in.Status))
	}
	return lr, nil
}

type realRideView struct {
	OrderID         string     `json:"orderId"`
	PlateNo         string     `json:"plateNo"`
	SimUserID       *string    `json:"simUserId"`
	UnlockRequestAt *time.Time `json:"unlockRequestedAt"`
	UnlockReply     *string    `json:"unlockReply"`
	UnlockResult    *string    `json:"unlockResult"`
	EndRequestedAt  *time.Time `json:"endRequestedAt"`
	EndCause        *string    `json:"endCause"`
	LockReported    bool       `json:"lockReported"`
	PayResult       *string    `json:"payResult"`
}

func (a *App) realRideView(ctx context.Context, orderID string) (*realRideView, error) {
	v := &realRideView{}
	err := a.DB.QueryRow(ctx, `SELECT order_id, plate_no, sim_user_id, unlock_requested_at, unlock_reply, end_requested_at, end_cause, pay_result
		FROM sandbox_orders WHERE order_id=$1`, orderID).Scan(&v.OrderID, &v.PlateNo, &v.SimUserID, &v.UnlockRequestAt, &v.UnlockReply, &v.EndRequestedAt, &v.EndCause, &v.PayResult)
	if err != nil {
		return nil, err
	}
	ds, err := a.deliveries(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if un := firstAccepted(ds, "开锁结果"); un != nil && un.Body.Unlock != nil {
		s := un.Body.Unlock.Result
		v.UnlockResult = &s
	}
	v.LockReported = firstAccepted(ds, "锁车") != nil
	return v, nil
}

// auditRide 真车联调订单的发起与结束留痕。留痕失败不影响联调本身，记日志。
func (a *App) auditRide(ctx context.Context, actor Actor, in *integRow, orderID, summary string) {
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		return a.audit(ctx, tx, actor, "试骑真车", "integration", in.IntegrationID, summary, nil, nil, map[string]any{"orderId": orderID})
	})
	if err != nil {
		a.logger().Warn("真车联调留痕失败", "order", orderID, "err", err)
	}
}

func (a *App) ownedInteg(ctx context.Context, adm *auth.Admin, brandID string) (*integRow, error) {
	in, err := a.integByBrand(ctx, a.DB, brandID)
	if err != nil {
		return nil, err
	}
	return in, ownOperator(adm, in.OperatorID)
}

// hStartRealRide 模拟用户实骑一辆真车：平台向运营商发 M-01。契约未列出，见交付说明。
func (a *App) hStartRealRide(r *http.Request, adm *auth.Admin) (any, error) {
	var body struct {
		PlateNo   string `json:"plateNo"`
		SimUserID string `json:"simUserId"`
	}
	if err := decodeBody(r, &body, true); err != nil {
		return nil, err
	}
	ctx := context.WithoutCancel(r.Context())
	in, err := a.ownedInteg(ctx, adm, r.PathValue("brandId"))
	if err != nil {
		return nil, err
	}
	if _, err := a.requireRealRideReady(ctx, in); err != nil {
		return nil, err
	}
	cred, err := a.credByBrand(ctx, a.DB, in.BrandID)
	if err != nil {
		return nil, err
	}
	if cred.Status != "有效" {
		return nil, errf(409, "CREDENTIAL_DISABLED", "联调凭证已吊销")
	}
	if in.OperatorBaseURL == nil {
		return nil, errf(409, "CONFLICT", "还没有运营商联调地址，请先发起一轮符合性测试并提供地址")
	}
	var n int
	if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM fleet_vehicles WHERE brand_id=$1 AND plate_no=$2`, in.BrandID, body.PlateNo).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errf(409, "CONFLICT", fmt.Sprintf("车号 %s 不在本品牌已入清单的车辆里，真车联调只能用清单里的车", body.PlateNo))
	}
	known := false
	for _, u := range content.LoadSandbox().SimUsers {
		if u.SimUserID == body.SimUserID {
			known = true
		}
	}
	if !known {
		return nil, errf(400, "INVALID", "模拟用户编号不存在")
	}
	o, err := a.createOrder(ctx, in.BrandID, "真车", "", body.PlateNo, body.SimUserID, false, false)
	if err != nil {
		return nil, err
	}
	if _, err := a.unlock(ctx, *in.OperatorBaseURL, cred, o); err != nil {
		return nil, err
	}
	a.auditRide(ctx, adminActor(adm), in, o.ID, fmt.Sprintf("品牌 %s 发起真车联调订单 %s（车号 %s）", in.BrandID, o.ID, body.PlateNo))
	return a.realRideView(ctx, o.ID)
}

// hEndRealRide 模拟用户还车：平台向运营商发 M-02。
func (a *App) hEndRealRide(r *http.Request, adm *auth.Admin) (any, error) {
	ctx := context.WithoutCancel(r.Context())
	in, err := a.ownedInteg(ctx, adm, r.PathValue("brandId"))
	if err != nil {
		return nil, err
	}
	cred, err := a.credByBrand(ctx, a.DB, in.BrandID)
	if err != nil {
		return nil, err
	}
	if cred.Status != "有效" {
		return nil, errf(409, "CREDENTIAL_DISABLED", "联调凭证已吊销")
	}
	var plate, kind string
	err = a.DB.QueryRow(ctx, `SELECT plate_no, kind FROM sandbox_orders WHERE order_id=$1 AND brand_id=$2`, r.PathValue("orderId"), in.BrandID).Scan(&plate, &kind)
	if isNoRows(err) || kind != "真车" {
		return nil, errf(404, "NOT_FOUND", "没有找到这笔真车联调订单")
	}
	if err != nil {
		return nil, err
	}
	if in.OperatorBaseURL == nil {
		return nil, errf(409, "CONFLICT", "还没有运营商联调地址")
	}
	o := &sbOrder{ID: r.PathValue("orderId"), Brand: in.BrandID, Plate: plate}
	a.endRide(ctx, *in.OperatorBaseURL, cred, o, "用户点还车", "")
	a.auditRide(ctx, adminActor(adm), in, o.ID, fmt.Sprintf("品牌 %s 结束真车联调订单 %s（车号 %s）", in.BrandID, o.ID, plate))
	return a.realRideView(ctx, o.ID)
}

func (a *App) hListRealRides(r *http.Request, adm *auth.Admin) (any, error) {
	ctx := r.Context()
	in, err := a.ownedInteg(ctx, adm, r.PathValue("brandId"))
	if err != nil {
		return nil, err
	}
	rows, err := a.DB.Query(ctx, `SELECT order_id FROM sandbox_orders WHERE brand_id=$1 AND kind='真车' ORDER BY created_at DESC`, in.BrandID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []*realRideView{}
	for _, id := range ids {
		v, err := a.realRideView(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// trace: submitRealRides · R-40 BR-37
func (a *App) hSubmitRealRides(r *http.Request, adm *auth.Admin) (any, error) {
	ctx := r.Context()
	in, err := a.ownedInteg(ctx, adm, r.PathValue("brandId"))
	if err != nil {
		return nil, err
	}
	run, err := a.requireRealRideReady(ctx, in)
	if err != nil {
		return nil, err
	}
	var pending int
	if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM reports WHERE integration_id=$1 AND result='待确认'`, in.IntegrationID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		return nil, errf(409, "CONFLICT", "验收报告已提交，正在等待接入负责人确认")
	}
	p, err := a.params(ctx)
	if err != nil {
		return nil, err
	}
	s, err := a.computeRealStats(ctx, in.BrandID, p)
	if err != nil {
		return nil, err
	}
	if len(s.Problems) > 0 || !s.TimeLimitOk {
		return nil, errf(409, "ACCEPTANCE_NOT_READY", "真车联调还没有达到验收标准："+strings.Join(s.Problems, "；")).
			with("realVehiclesRequired", p.RealVehicles).with("realVehiclesRidden", s.VehiclesRidden).with("realOrders", s.Orders).
			with("matchedOrders", s.Matched).with("lockReportsMatched", s.LockMatched).with("timeLimitOk", s.TimeLimitOk)
	}
	now := a.now()
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		var seq int
		if err := tx.QueryRow(ctx, `UPDATE integrations SET report_seq=report_seq+1 WHERE brand_id=$1 RETURNING report_seq`, in.BrandID).Scan(&seq); err != nil {
			return err
		}
		reportID := fmt.Sprintf("%s-AR%02d", in.IntegrationID, seq)
		if _, err := tx.Exec(ctx, `INSERT INTO reports (report_id, integration_id, real_vehicles_required, real_vehicles_ridden, real_orders, matched_orders,
			lock_reports_matched, time_limit_ok, run_id, submitted_at, submitted_by, result) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'待确认')`,
			reportID, in.IntegrationID, p.RealVehicles, s.VehiclesRidden, s.Orders, s.Matched, s.LockMatched, s.TimeLimitOk, run.RunID, now, adm.AccountID); err != nil {
			return err
		}
		actor := adminActor(adm)
		if err := a.emit(ctx, tx, "onboard.acceptance.submitted", in.IntegrationID, actor, map[string]any{
			"integrationId": in.IntegrationID, "brandId": in.BrandID, "operatorId": in.OperatorID, "reportId": reportID,
			"realVehiclesRidden": s.VehiclesRidden, "realOrders": s.Orders, "matchedOrders": s.Matched, "timeLimitOk": s.TimeLimitOk, "submittedAt": now,
		}); err != nil {
			return err
		}
		return a.audit(ctx, tx, actor, "提交真车联调结果", "integration", in.IntegrationID,
			fmt.Sprintf("%s 真车联调 %d 辆、%d 单全部匹配，验收报告 %s 待接入负责人确认", in.BrandID, s.VehiclesRidden, s.Orders, reportID), nil, nil,
			map[string]any{"reportId": reportID, "result": "待确认"})
	})
	if err != nil {
		return nil, err
	}
	in, err = a.integByBrand(ctx, a.DB, in.BrandID)
	if err != nil {
		return nil, err
	}
	return a.buildIntegration(ctx, in)
}

// trace: listAcceptance · R-40 BR-37
func (a *App) hListAcceptance(r *http.Request, adm *auth.Admin) (any, error) {
	ctx := r.Context()
	list, err := a.listIntegrations(ctx, adm.Scope)
	if err != nil {
		return nil, err
	}
	out := []*AcceptanceRow{}
	now := a.now()
	for _, in := range list {
		v, err := a.integrationView(ctx, in)
		if err != nil {
			return nil, err
		}
		out = append(out, &AcceptanceRow{IntegrationView: *v, ElapsedDays: elapsedDays(in.StartedAt, now)})
	}
	return out, nil
}

// trace: confirmAcceptance · R-40 R-41 BR-37。人确认点：只有接入负责人（acceptance.confirm）可确认，智能体没有这个入口。
func (a *App) hConfirm(r *http.Request, adm *auth.Admin) (any, error) {
	var body reasonBody
	if err := decodeBody(r, &body, true); err != nil {
		return nil, err
	}
	if err := checkReason(body.Reason); err != nil {
		return nil, err
	}
	ctx := context.WithoutCancel(r.Context())
	in, err := a.integByID(ctx, a.DB, r.PathValue("integrationId"))
	if err != nil {
		return nil, err
	}
	if err := ownOperator(adm, in.OperatorID); err != nil {
		return nil, err
	}
	if a.operatorExited(ctx, in.OperatorID) {
		return nil, errf(409, "ACCEPTANCE_NOT_READY", "运营商已退出，不能确认验收")
	}
	var reportID string
	err = a.DB.QueryRow(ctx, `SELECT report_id FROM reports WHERE integration_id=$1 AND result='待确认' ORDER BY submitted_at DESC LIMIT 1`, in.IntegrationID).Scan(&reportID)
	if isNoRows(err) {
		return nil, errf(409, "ACCEPTANCE_NOT_READY", "验收报告尚未提交，或已确认")
	}
	if err != nil {
		return nil, err
	}
	actor := adminActor(adm)
	_, err = a.Fleet.GoLive(ctx, in.BrandID, "onboard-golive-"+reportID, fleet.Actor{Kind: actor.Kind, ID: actor.ID, Name: actor.Name}, body.Reason)
	var fe *fleet.Error
	switch {
	case err == nil:
	case errors.As(err, &fe) && fe.Code == "ALREADY_DONE":
	case errors.As(err, &fe) && fe.Status == 409:
		return nil, errf(409, "ACCEPTANCE_NOT_READY", "品牌当前不能上线："+fe.Message)
	case errors.As(err, &fe) && fe.Status == 404:
		return nil, errf(404, "NOT_FOUND", "接入状态服务里没有这个品牌")
	default:
		return nil, errf(500, "SERVER_BUSY", "接入状态服务暂时不可用，验收未确认，请稍后重试")
	}
	now := a.now()
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE reports SET result='通过', confirmed_by=$2, confirmed_at=$3 WHERE report_id=$1 AND result='待确认'`, reportID, adm.AccountID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		note := fmt.Sprintf("验收通过，上线（%s）", reportID)
		if err := a.setStatus(ctx, tx, in.BrandID, "已接入", now, adm.AccountID, &note); err != nil {
			return err
		}
		if err := a.emit(ctx, tx, "onboard.acceptance.confirmed", in.IntegrationID, actor, map[string]any{
			"integrationId": in.IntegrationID, "brandId": in.BrandID, "operatorId": in.OperatorID, "reportId": reportID, "confirmedAt": now,
		}); err != nil {
			return err
		}
		if err := a.audit(ctx, tx, actor, "确认验收", "integration", in.IntegrationID,
			fmt.Sprintf("%s 验收通过并上线（%s）", in.BrandID, reportID), &body.Reason,
			map[string]any{"result": "待确认"}, map[string]any{"result": "通过"}); err != nil {
			return err
		}
		return a.audit(ctx, tx, actor, "上线品牌", "integration", in.IntegrationID,
			fmt.Sprintf("品牌 %s 验收确认后上线，接入状态置为已接入", in.BrandID), &body.Reason,
			map[string]any{"accessStatus": "联调中"}, map[string]any{"accessStatus": "已接入"})
	})
	if err != nil {
		return nil, err
	}
	in, err = a.integByID(ctx, a.DB, in.IntegrationID)
	if err != nil {
		return nil, err
	}
	return a.buildIntegration(ctx, in)
}

// trace: getOnboardTodos · R-40
func (a *App) hTodos(r *http.Request, adm *auth.Admin) (any, error) {
	var n int
	err := a.DB.QueryRow(r.Context(), `SELECT count(*) FROM reports rp JOIN integrations i ON i.integration_id=rp.integration_id
		WHERE rp.result='待确认' AND ($1='' OR i.operator_id=$1)`, adm.Scope).Scan(&n)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	if n > 0 {
		out = append(out, map[string]any{"key": "acceptance", "label": "待确认的验收报告", "count": n, "menu": "接入验收"})
	}
	return out, nil
}

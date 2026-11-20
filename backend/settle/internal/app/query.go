package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 运营商订单明细与按订单号查结算归属；全部来自本服务的投影，不含任何用户字段（R-46、NFR-11）。

const opOrderCols = `o.order_id, o.vehicle_id, o.plate_no, o.brand_id, COALESCE(b.name, o.brand_id), o.request_at, o.unlock_result, o.start_at, o.start_location,
	o.end_at, o.lock_report_at, o.pile_in_at, o.end_location, o.end_method, o.in_station, o.in_station_basis, o.end_station_id, o.end_station_name,
	o.duration_sec, o.billed_minutes, o.distance_km, o.pricing_version_id, CASE WHEN o.has_bill THEN o.receivable END, CASE WHEN o.has_bill THEN o.dispatch_fee END,
	o.degraded, o.late_report, o.report_received_at, o.settle_month, o.operator_id,
	s.settlement_id, s.kind, s.adjustment_id, s.note,
	COALESCE((SELECT json_agg(d.dispute_id ORDER BY d.dispute_id) FROM disputes d WHERE d.order_ids ? o.order_id), '[]'::json)`

const opOrderFrom = ` FROM orders o LEFT JOIN brands b ON b.brand_id = o.brand_id LEFT JOIN order_settle s ON s.order_id = o.order_id `

func scanOperatorOrder(row pgx.Row) (OperatorOrderView, string, error) {
	var v OperatorOrderView
	var sl, el []byte
	var op string
	var sid, kind, aid, note *string
	var dids []byte
	err := row.Scan(&v.OrderID, &v.VehicleID, &v.PlateNo, &v.BrandID, &v.BrandName, &v.RequestAt, &v.UnlockResult, &v.StartAt, &sl,
		&v.EndAt, &v.LockReportAt, &v.PileInAt, &el, &v.EndMethod, &v.InStation, &v.InStationBasis, &v.EndStationID, &v.EndStationName,
		&v.DurationSec, &v.BilledMinutes, &v.DistanceKm, &v.PricingVersionID, &v.Receivable, &v.DispatchFee,
		&v.Degraded, &v.LateReport, &v.ReportReceivedAt, &v.SettleMonth, &op, &sid, &kind, &aid, &note, &dids)
	if err != nil {
		return v, "", err
	}
	v.Status = "已结束"
	if len(sl) > 0 {
		v.StartLocation = json.RawMessage(sl)
	}
	if len(el) > 0 {
		v.EndLocation = json.RawMessage(el)
	}
	if sid != nil {
		v.Settlement = &SettlementPointer{SettlementID: *sid, Kind: *kind, AdjustmentID: aid}
	}
	v.SettlementNote = note
	_ = json.Unmarshal(dids, &v.DisputeIDs)
	if v.DisputeIDs == nil {
		v.DisputeIDs = []string{}
	}
	return v, op, nil
}

func (a *App) operatorOrdersByID(ctx context.Context, q querier, ids []string) ([]OperatorOrderView, error) {
	rows, err := q.Query(ctx, `SELECT `+opOrderCols+opOrderFrom+` WHERE o.order_id = ANY($1) AND o.ride_ended ORDER BY o.request_at`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OperatorOrderView{}
	for rows.Next() {
		v, _, err := scanOperatorOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// OrderFilter 运营商订单筛选。
type OrderFilter struct {
	OperatorID string
	Date       string
	From, To   *time.Time
	BrandID    string
	Status     string
	Degraded   *bool
	Q          string
	SortKey    string
	Desc       bool
	Page, Size int
}

// 允许的排序列。
var orderSortCols = map[string]string{
	"requestAt": "o.request_at", "endAt": "o.end_at", "receivable": "o.receivable", "billedMinutes": "o.billed_minutes",
	"durationSec": "o.duration_sec", "distanceKm": "o.distance_km", "orderId": "o.order_id",
}

func (f OrderFilter) where() (string, []any) {
	conds := []string{"o.ride_ended", "o.operator_id = $1"}
	args := []any{f.OperatorID}
	add := func(c string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(c, len(args)))
	}
	if f.Date != "" {
		add("to_char((o.request_at AT TIME ZONE 'UTC') + interval '8 hours', 'YYYY-MM-DD') = $%d", f.Date)
	}
	if f.From != nil {
		add("o.request_at >= $%d", *f.From)
	}
	if f.To != nil {
		add("o.request_at <= $%d", *f.To)
	}
	if f.BrandID != "" {
		add("o.brand_id = $%d", f.BrandID)
	}
	if f.Status != "" && f.Status != "已结束" {
		conds = append(conds, "false") // 本服务只订阅订单已结束事件
	}
	if f.Degraded != nil {
		add("o.degraded = $%d", *f.Degraded)
	}
	if f.Q != "" {
		args = append(args, "%"+f.Q+"%", strings.ToUpper(f.Q))
		conds = append(conds, fmt.Sprintf("(o.order_id LIKE $%d OR o.plate_no = $%d)", len(args)-1, len(args)))
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListOperatorOrders 运营商订单明细与筛选范围内的汇总。
func (a *App) ListOperatorOrders(ctx context.Context, f OrderFilter) (OperatorOrderPage, error) {
	p := OperatorOrderPage{Page: Page[OperatorOrderView]{Page: f.Page, PageSize: f.Size, Rows: []OperatorOrderView{}}}
	where, args := f.where()
	if err := a.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE o.has_bill), COALESCE(SUM(o.receivable) FILTER (WHERE o.has_bill), 0), count(*) FILTER (WHERE o.degraded)
		FROM orders o`+where, args...).Scan(&p.Summary.Count, &p.Summary.Finished, &p.Summary.ReceivableSum, &p.Summary.Degraded); err != nil {
		return p, err
	}
	p.Total = p.Summary.Count
	col := orderSortCols[f.SortKey]
	if col == "" {
		col = "o.request_at"
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	n := len(args)
	rows, err := a.DB.Query(ctx, `SELECT `+opOrderCols+opOrderFrom+where+fmt.Sprintf(` ORDER BY %s %s NULLS LAST, o.order_id %s LIMIT $%d OFFSET $%d`, col, dir, dir, n+1, n+2),
		append(args, f.Size, (f.Page-1)*f.Size)...)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		v, _, err := scanOperatorOrder(rows)
		if err != nil {
			return p, err
		}
		p.Rows = append(p.Rows, v)
	}
	return p, rows.Err()
}

// ExportOperatorOrders 导出用：按开锁请求时刻旧到新的全部订单。
func (a *App) ExportOperatorOrders(ctx context.Context, f OrderFilter) ([]OperatorOrderView, error) {
	where, args := f.where()
	rows, err := a.DB.Query(ctx, `SELECT `+opOrderCols+opOrderFrom+where+` ORDER BY o.request_at, o.order_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OperatorOrderView{}
	for rows.Next() {
		v, _, err := scanOperatorOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetOperatorOrder 运营商订单详情。
func (a *App) GetOperatorOrder(ctx context.Context, scope, id string) (OperatorOrderView, error) {
	v, op, err := scanOperatorOrder(a.DB.QueryRow(ctx, `SELECT `+opOrderCols+opOrderFrom+` WHERE o.order_id = $1 AND o.ride_ended`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound("该订单")
	}
	if err != nil {
		return v, err
	}
	if scope != "" && scope != op {
		return OperatorOrderView{}, errScope
	}
	return v, nil
}

func toOrderSettlement(v OperatorOrderView, op string) OrderSettlementView {
	return OrderSettlementView{OrderID: v.OrderID, OperatorID: op, SettleMonth: v.SettleMonth, Settlement: v.Settlement, SettlementNote: v.SettlementNote, DisputeIDs: v.DisputeIDs}
}

// OrderSettlement 按订单号查结算归属。
func (a *App) OrderSettlement(ctx context.Context, scope, id string) (OrderSettlementView, error) {
	v, op, err := scanOperatorOrder(a.DB.QueryRow(ctx, `SELECT `+opOrderCols+opOrderFrom+` WHERE o.order_id = $1 AND o.ride_ended`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderSettlementView{}, errNotFound("该订单的结算记录")
	}
	if err != nil {
		return OrderSettlementView{}, err
	}
	if scope != "" && scope != op {
		return OrderSettlementView{}, errScope
	}
	return toOrderSettlement(v, op), nil
}

// OrderSettlements 按订单号批量查结算归属；运营商账号只返回本家订单。
func (a *App) OrderSettlements(ctx context.Context, scope string, ids []string) ([]OrderSettlementView, error) {
	rows, err := a.DB.Query(ctx, `SELECT `+opOrderCols+opOrderFrom+` WHERE o.order_id = ANY($1) AND o.ride_ended AND ($2 = '' OR o.operator_id = $2) ORDER BY o.order_id`, ids, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrderSettlementView{}
	for rows.Next() {
		v, op, err := scanOperatorOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, toOrderSettlement(v, op))
	}
	return out, rows.Err()
}

// Todos 按权限返回本模块的待办计数。
func (a *App) Todos(ctx context.Context, has func(string) bool, scope string) ([]Todo, error) {
	out := []Todo{}
	if has("settlement.manage") {
		var n int
		if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM settlements WHERE status = '待审核'`).Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, Todo{Key: "settlements", Label: "待审核结算单", Count: n, Menu: "结算单管理"})
	}
	if has("dispute.decide") {
		var n int
		if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM disputes WHERE status = '待核实'`).Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, Todo{Key: "disputes", Label: "待核实异议", Count: n, Menu: "异议处理"})
	}
	if has("dispute.submit.own") && scope != "" {
		var n int
		if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM disputes WHERE status = '待核实' AND operator_id = $1`, scope).Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, Todo{Key: "myDisputes", Label: "异议待平台核实", Count: n, Menu: "结算单"})
	}
	return out, nil
}

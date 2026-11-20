package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"bike/settle/internal/calc"
)

// 运营商异议（BR-16）：只受理时长、里程；财务采纳、驳回或判无需调整；采纳只更正服务费基数，差额进下一期"异议核实"调整项。
// agentComparison / agentSuggestion 由对账智能体填写（运行时未定，当前留空）；platformFact 由代码按订单投影给出。

// SubmitDispute 运营商提交异议。
func (a *App) SubmitDispute(ctx context.Context, scope, sid string, orderIDs []string, field, claim string, actor Actor) (DisputeView, error) {
	if scope == "" {
		return DisputeView{}, apiErr(403, "FORBIDDEN", "只有运营商账号可以提交异议")
	}
	if field != "时长" && field != "里程" {
		return DisputeView{}, apiErr(409, "PRICE_NOT_DISPUTABLE", "价格由主管部门统一制定，不受理异议；可对时长、里程提出异议")
	}
	at := a.Now()
	var id string
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		var op, month string
		err := tx.QueryRow(ctx, `SELECT operator_id, month FROM settlements WHERE settlement_id = $1`, sid).Scan(&op, &month)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound("该结算单")
		}
		if err != nil {
			return err
		}
		if op != scope {
			return errScope
		}
		if len(orderIDs) == 0 {
			return errInvalid("请选择有异议的订单")
		}
		orderIDs = sortStrings(orderIDs)
		facts := []string{}
		for _, oid := range orderIDs {
			var in bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM settlement_lines WHERE settlement_id = $1 AND order_id = $2)
				OR EXISTS(SELECT 1 FROM adjustment_items i JOIN adjustments ad ON ad.adjustment_id = i.adjustment_id WHERE ad.settlement_id = $1 AND i.order_id = $2)`,
				sid, oid).Scan(&in); err != nil {
				return err
			}
			if !in {
				return errInvalid(fmt.Sprintf("订单 %s 不在该结算单中", oid))
			}
			facts = append(facts, platformFact(ctx, tx, oid))
		}
		if utf8.RuneCountInString(strings.TrimSpace(claim)) < 6 {
			return errInvalid("请写明你方的记录与理由（至少 6 个字）")
		}
		ym := at.In(calc.Loc).Format("200601")
		n, err := nextSeq(ctx, tx, "DS"+ym)
		if err != nil {
			return err
		}
		id = fmt.Sprintf("DS%s%03d", ym, n)
		ob, _ := json.Marshal(orderIDs)
		ab, _ := json.Marshal(actor)
		if _, err := tx.Exec(ctx, `INSERT INTO disputes(dispute_id, operator_id, settlement_id, order_ids, field, operator_claim, platform_fact, status, submitted_at, submitted_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'待核实',$8,$9)`, id, op, sid, ob, field, strings.TrimSpace(claim), strings.Join(facts, "；"), at, ab); err != nil {
			return err
		}
		return audit(ctx, tx, actor, at, "提交异议", "dispute", id, fmt.Sprintf("对 %s 的 %d 单提出%s异议", sid, len(orderIDs), field),
			strPtr(strings.TrimSpace(claim)), nil, map[string]any{"status": "待核实"})
	})
	if err != nil {
		return DisputeView{}, err
	}
	return a.disputeView(ctx, a.DB, id, false)
}

// platformFact 平台记录的订单事实（计费分钟与里程，取自订单投影）。
func platformFact(ctx context.Context, q querier, oid string) string {
	var bm *int
	var km *float64
	_ = q.QueryRow(ctx, `SELECT billed_minutes, distance_km FROM orders WHERE order_id = $1`, oid).Scan(&bm, &km)
	mins := "计费分钟未知"
	if bm != nil {
		mins = fmt.Sprintf("%d 分钟", *bm)
	}
	dist := "里程未报告"
	if km != nil {
		dist = fmt.Sprintf("%.1f 公里", *km)
	}
	return fmt.Sprintf("%s 平台记录 %s、%s", oid, mins, dist)
}

// CorrectionInput 采纳时每单核实后的计费分钟数。
type CorrectionInput struct {
	OrderID string `json:"orderId"`
	Minutes int    `json:"minutes"`
}

var decisionStatus = map[string]string{"采纳": "已采纳", "驳回": "已驳回", "无需调整": "无需调整"}

// DecideDispute 财务处理异议。
func (a *App) DecideDispute(ctx context.Context, scope, id, decision string, corrections []CorrectionInput, reason string, actor Actor) (DisputeView, error) {
	if !validReason(reason) {
		return DisputeView{}, errReason
	}
	at := a.Now()
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		var op, sid, field, status string
		var ob []byte
		err := tx.QueryRow(ctx, `SELECT operator_id, settlement_id, field, status, order_ids FROM disputes WHERE dispute_id = $1 FOR UPDATE`, id).Scan(&op, &sid, &field, &status, &ob)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound("该异议")
		}
		if err != nil {
			return err
		}
		if scope != "" && scope != op {
			return errScope
		}
		if status != "待核实" {
			return apiErr(409, "ALREADY_DONE", "该异议已处理")
		}
		st, ok := decisionStatus[decision]
		if !ok {
			return errInvalid("处理结果只能是采纳、驳回或无需调整")
		}
		var orderIDs []string
		_ = json.Unmarshal(ob, &orderIDs)
		out := []DisputeCorrection{}
		if decision == "采纳" {
			if field == "里程" {
				return errInvalid(`里程不参与计价，采纳不会改变服务费，请选择"无需调整"`)
			}
			if len(corrections) == 0 {
				return errInvalid("采纳时请填写每单核实后的分钟数")
			}
			vs, err := loadVersions(ctx, tx)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, c := range corrections {
				if !contains(orderIDs, c.OrderID) || seen[c.OrderID] {
					return errInvalid(fmt.Sprintf("订单 %s 不在该异议中", c.OrderID))
				}
				seen[c.OrderID] = true
				if c.Minutes < 1 || c.Minutes > 600 {
					return errInvalid("核实后的分钟数应在 1～600 之间")
				}
				o, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM orders o WHERE o.order_id = $1`, c.OrderID))
				if err != nil || !o.HasBill {
					return errInvalid(fmt.Sprintf("订单 %s 尚无账单事实，不能更正", c.OrderID))
				}
				v, ok := vs[o.PricingVersionID]
				if !ok {
					return errInvalid(fmt.Sprintf("订单 %s 的规则版本 %s 尚未同步", c.OrderID, o.PricingVersionID))
				}
				d := calc.CorrectionDelta(v, o, c.Minutes)
				out = append(out, DisputeCorrection{OrderID: c.OrderID, Minutes: c.Minutes, BaseDelta: d})
				if _, err := tx.Exec(ctx, `INSERT INTO corrections(dispute_id, order_id, minutes, base_delta, decided_at) VALUES ($1,$2,$3,$4,$5)`,
					id, c.OrderID, c.Minutes, d, at); err != nil {
					return err
				}
			}
		}
		cb, _ := json.Marshal(out)
		ab, _ := json.Marshal(actor)
		why := strings.TrimSpace(reason)
		if _, err := tx.Exec(ctx, `UPDATE disputes SET status = $2, decided_at = $3, decided_by = $4, decision_reason = $5, corrections = $6 WHERE dispute_id = $1`,
			id, st, at, ab, why, cb); err != nil {
			return err
		}
		if err := emit(ctx, tx, "settle.dispute.decided", id, actor, at, map[string]any{
			"disputeId": id, "operatorId": op, "settlementId": sid, "field": field, "decision": st, "adjustmentId": nil, "decidedAt": at,
		}); err != nil {
			return err
		}
		summary := fmt.Sprintf("异议 %s %s", id, st)
		if len(out) > 0 {
			parts := []string{}
			for _, c := range out {
				sign := ""
				if c.BaseDelta >= 0 {
					sign = "+"
				}
				parts = append(parts, fmt.Sprintf("%s %s%s", c.OrderID, sign, yuan(c.BaseDelta)))
			}
			summary += "，基数更正 " + strings.Join(parts, "、") + "，计入下一期调整项"
		}
		return audit(ctx, tx, actor, at, "处理异议", "dispute", id, summary, strPtr(why), map[string]any{"status": "待核实"}, map[string]any{"status": st})
	})
	if err != nil {
		return DisputeView{}, err
	}
	return a.disputeView(ctx, a.DB, id, false)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

const disputeCols = `dispute_id, operator_id, settlement_id, order_ids, field, operator_claim, platform_fact, agent_comparison, agent_suggestion,
	status, submitted_at, submitted_by, decided_at, decided_by, decision_reason, corrections, adjustment_id`

func scanDispute(row pgx.Row) (DisputeView, error) {
	var d DisputeView
	var ob, sb, db, cb []byte
	if err := row.Scan(&d.DisputeID, &d.OperatorID, &d.SettlementID, &ob, &d.Field, &d.OperatorClaim, &d.PlatformFact, &d.AgentComparison, &d.AgentSuggestion,
		&d.Status, &d.SubmittedAt, &sb, &d.DecidedAt, &db, &d.DecisionReason, &cb, &d.AdjustmentID); err != nil {
		return d, err
	}
	_ = json.Unmarshal(ob, &d.OrderIDs)
	_ = json.Unmarshal(sb, &d.SubmittedBy)
	if len(db) > 0 {
		var x Actor
		_ = json.Unmarshal(db, &x)
		d.DecidedBy = &x
	}
	_ = json.Unmarshal(cb, &d.Corrections)
	if d.Corrections == nil {
		d.Corrections = []DisputeCorrection{}
	}
	return d, nil
}

func (a *App) disputeView(ctx context.Context, q querier, id string, withOrders bool) (DisputeView, error) {
	d, err := scanDispute(q.QueryRow(ctx, `SELECT `+disputeCols+` FROM disputes WHERE dispute_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, errNotFound("该异议")
	}
	if err != nil {
		return d, err
	}
	d.OperatorName = operatorName(ctx, q, d.OperatorID)
	if withOrders {
		orders, err := a.operatorOrdersByID(ctx, q, d.OrderIDs)
		if err != nil {
			return d, err
		}
		d.Orders = &orders
	}
	return d, nil
}

// GetDispute 异议详情（含订单事实）。
func (a *App) GetDispute(ctx context.Context, scope, id string) (DisputeView, error) {
	d, err := a.disputeView(ctx, a.DB, id, true)
	if err != nil {
		return d, err
	}
	if scope != "" && scope != d.OperatorID {
		return DisputeView{}, errScope
	}
	return d, nil
}

// ListDisputes 异议列表，按提交时间新到旧。
func (a *App) ListDisputes(ctx context.Context, operatorID, status string, page, size int) (Page[DisputeView], error) {
	p := Page[DisputeView]{Page: page, PageSize: size, Rows: []DisputeView{}}
	where := `WHERE ($1 = '' OR operator_id = $1) AND ($2 = '' OR status = $2)`
	if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM disputes `+where, operatorID, status).Scan(&p.Total); err != nil {
		return p, err
	}
	rows, err := a.DB.Query(ctx, `SELECT `+disputeCols+` FROM disputes `+where+` ORDER BY submitted_at DESC, dispute_id DESC LIMIT $3 OFFSET $4`,
		operatorID, status, size, (page-1)*size)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDispute(rows)
		if err != nil {
			return p, err
		}
		p.Rows = append(p.Rows, d)
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	names := map[string]string{}
	for i := range p.Rows {
		op := p.Rows[i].OperatorID
		if _, ok := names[op]; !ok {
			names[op] = operatorName(ctx, a.DB, op)
		}
		p.Rows[i].OperatorName = names[op]
	}
	return p, nil
}

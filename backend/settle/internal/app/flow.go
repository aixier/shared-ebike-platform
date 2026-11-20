package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"bike/settle/internal/calc"
)

// 结算单状态机：待审核 → 已审核 → 已发送 → 已到账（BR-20）。

type lockedSettlement struct {
	id, operatorID, month, status string
	payable                       int64
}

func lockSettlement(ctx context.Context, tx pgx.Tx, sid string) (lockedSettlement, error) {
	var s lockedSettlement
	err := tx.QueryRow(ctx, `SELECT settlement_id, operator_id, month, status FROM settlements WHERE settlement_id = $1 FOR UPDATE`, sid).
		Scan(&s.id, &s.operatorID, &s.month, &s.status)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, errNotFound("该结算单")
	}
	if err != nil {
		return s, err
	}
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT SUM(fee) FROM settlement_lines WHERE settlement_id = $1), 0)
		+ COALESCE((SELECT SUM(fee_delta) FROM adjustments WHERE settlement_id = $1), 0)`, sid).Scan(&s.payable)
	return s, err
}

func validReason(r string) bool { return utf8.RuneCountInString(strings.TrimSpace(r)) >= 4 }

// AddAdjustment 财务录入人工调整项：录入即确认，须写事由。
func (a *App) AddAdjustment(ctx context.Context, scope, sid, brandID string, feeDelta int64, orderIDs []string, reason string, actor Actor) (SettlementView, error) {
	if !validReason(reason) {
		return SettlementView{}, errReason
	}
	if feeDelta == 0 {
		return SettlementView{}, errInvalid("调整金额须为非零整数分")
	}
	at := a.Now()
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, generateLockKey); err != nil {
			return err
		}
		s, err := lockSettlement(ctx, tx, sid)
		if err != nil {
			return err
		}
		if scope != "" && scope != s.operatorID {
			return errScope
		}
		if s.status != "待审核" {
			return errState("只有待审核的结算单可以录入调整项")
		}
		var owner string
		_ = tx.QueryRow(ctx, `SELECT operator_id FROM brands WHERE brand_id = $1`, brandID).Scan(&owner)
		if owner != s.operatorID {
			return errInvalid("品牌不属于该结算单的运营商")
		}
		var seq int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM adjustments WHERE settlement_id = $1`, sid).Scan(&seq); err != nil {
			return err
		}
		aid := adjustmentID(s.month, s.operatorID, seq)
		if orderIDs == nil {
			orderIDs = []string{}
		}
		ob, _ := json.Marshal(orderIDs)
		ab, _ := json.Marshal(actor)
		if _, err := tx.Exec(ctx, `INSERT INTO adjustments(adjustment_id, settlement_id, seq, type, brand_id, order_ids, base_delta, fee_delta, reason, created_by, created_at, confirmed_by, confirmed_at)
			VALUES ($1,$2,$3,$4,$5,$6,0,$7,$8,$9,$10,$9,$10)`, aid, sid, seq, calc.TypeManual, brandID, ob, feeDelta, strings.TrimSpace(reason), ab, at); err != nil {
			return err
		}
		if err := emit(ctx, tx, "settle.settlement.adjusted", sid, actor, at, map[string]any{
			"settlementId": sid, "adjustmentId": aid, "brandId": brandID, "type": calc.TypeManual, "feeDelta": feeDelta, "confirmed": true, "at": at,
		}); err != nil {
			return err
		}
		sign := ""
		if feeDelta > 0 {
			sign = "+"
		}
		return audit(ctx, tx, actor, at, "调整结算单", "settlement", sid, fmt.Sprintf("录入调整项 %s，服务费 %s%s", aid, sign, yuan(feeDelta)),
			strPtr(strings.TrimSpace(reason)), map[string]any{"payableFee": s.payable}, map[string]any{"payableFee": s.payable + feeDelta, "adjustmentId": aid})
	})
	if err != nil {
		return SettlementView{}, err
	}
	return a.settlementView(ctx, a.DB, sid, false)
}

// ConfirmAdjustment 财务确认代码或对账智能体生成的调整项。
func (a *App) ConfirmAdjustment(ctx context.Context, scope, sid, aid, note string, actor Actor) (SettlementView, error) {
	at := a.Now()
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		s, err := lockSettlement(ctx, tx, sid)
		if err != nil {
			return err
		}
		if scope != "" && scope != s.operatorID {
			return errScope
		}
		var typ, brand, reason string
		var fee int64
		var confirmed bool
		err = tx.QueryRow(ctx, `SELECT type, brand_id, fee_delta, reason, confirmed_by IS NOT NULL FROM adjustments WHERE adjustment_id = $1 AND settlement_id = $2 FOR UPDATE`, aid, sid).
			Scan(&typ, &brand, &fee, &reason, &confirmed)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound("该调整项")
		}
		if err != nil {
			return err
		}
		if s.status != "待审核" {
			return errState("")
		}
		if confirmed {
			return apiErr(409, "ALREADY_DONE", "该调整项已确认")
		}
		ab, _ := json.Marshal(actor)
		if _, err := tx.Exec(ctx, `UPDATE adjustments SET confirmed_by = $2, confirmed_at = $3 WHERE adjustment_id = $1`, aid, ab, at); err != nil {
			return err
		}
		if err := emit(ctx, tx, "settle.settlement.adjusted", sid, actor, at, map[string]any{
			"settlementId": sid, "adjustmentId": aid, "brandId": brand, "type": typ, "feeDelta": fee, "confirmed": true, "at": at,
		}); err != nil {
			return err
		}
		why := strings.TrimSpace(note)
		if why == "" {
			why = reason
		}
		sign := ""
		if fee >= 0 {
			sign = "+"
		}
		return audit(ctx, tx, actor, at, "调整结算单", "settlement", sid, fmt.Sprintf("确认调整项 %s（%s，服务费 %s%s）", aid, typ, sign, yuan(fee)),
			strPtr(why), map[string]any{"confirmed": false}, map[string]any{"confirmed": true, "adjustmentId": aid})
	})
	if err != nil {
		return SettlementView{}, err
	}
	return a.settlementView(ctx, a.DB, sid, false)
}

// Review 审核：待审核 → 已审核；有未确认调整项时不能审核。
func (a *App) Review(ctx context.Context, scope, sid, note string, actor Actor) (SettlementView, error) {
	at := a.Now()
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		s, err := lockSettlement(ctx, tx, sid)
		if err != nil {
			return err
		}
		if scope != "" && scope != s.operatorID {
			return errScope
		}
		if s.status != "待审核" {
			return errState("")
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM adjustments WHERE settlement_id = $1 AND confirmed_by IS NULL`, sid).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return apiErr(409, "ADJUSTMENT_UNCONFIRMED", "还有调整项未确认，确认后才能审核")
		}
		ab, _ := json.Marshal(actor)
		if _, err := tx.Exec(ctx, `UPDATE settlements SET status = '已审核', reviewed_by = $2, reviewed_at = $3 WHERE settlement_id = $1`, sid, ab, at); err != nil {
			return err
		}
		return audit(ctx, tx, actor, at, "审核结算单", "settlement", sid, fmt.Sprintf("审核通过 %s，应付服务费 ¥%s", sid, yuan(s.payable)),
			strPtr(strings.TrimSpace(note)), map[string]any{"status": "待审核"}, map[string]any{"status": "已审核"})
	})
	if err != nil {
		return SettlementView{}, err
	}
	return a.settlementView(ctx, a.DB, sid, false)
}

// Send 发送给运营商：已审核 → 已发送。
func (a *App) Send(ctx context.Context, scope, sid string, actor Actor) (SettlementView, error) {
	at := a.Now()
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		s, err := lockSettlement(ctx, tx, sid)
		if err != nil {
			return err
		}
		if scope != "" && scope != s.operatorID {
			return errScope
		}
		if s.status != "已审核" {
			return errState("审核通过后才能发送给运营商")
		}
		if _, err := tx.Exec(ctx, `UPDATE settlements SET status = '已发送', sent_at = $2 WHERE settlement_id = $1`, sid, at); err != nil {
			return err
		}
		name := operatorName(ctx, tx, s.operatorID)
		if name == "" {
			name = s.operatorID
		}
		return audit(ctx, tx, actor, at, "发送结算单", "settlement", sid, fmt.Sprintf("结算单 %s 已发送给%s", sid, name), nil,
			map[string]any{"status": "已审核"}, map[string]any{"status": "已发送"})
	})
	if err != nil {
		return SettlementView{}, err
	}
	return a.settlementView(ctx, a.DB, sid, false)
}

var bankTradeNoRe = regexp.MustCompile(`^[A-Z0-9]{8,32}$`)

// RegisterPayment 登记到账：已发送 → 已到账（本模块不付款，只登记）。
func (a *App) RegisterPayment(ctx context.Context, scope, sid, bankTradeNo string, paidAt *time.Time, actor Actor) (SettlementView, error) {
	if !bankTradeNoRe.MatchString(bankTradeNo) {
		return SettlementView{}, errInvalid("请填写银行流水号（8～32 位大写字母或数字）")
	}
	at := a.Now()
	when := at
	if paidAt != nil {
		when = *paidAt
	}
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		s, err := lockSettlement(ctx, tx, sid)
		if err != nil {
			return err
		}
		if scope != "" && scope != s.operatorID {
			return errScope
		}
		if s.status != "已发送" {
			return errState("发送给运营商后才能登记到账")
		}
		var due string
		if err := tx.QueryRow(ctx, `UPDATE settlements SET status = '已到账', paid_at = $2, bank_trade_no = $3, invoice_status = '待开票' WHERE settlement_id = $1 RETURNING due_date`,
			sid, when, bankTradeNo).Scan(&due); err != nil {
			return err
		}
		if err := emit(ctx, tx, "settle.settlement.paid", sid, actor, at, map[string]any{
			"settlementId": sid, "operatorId": s.operatorID, "payableFee": s.payable, "paidAt": when, "bankTradeNo": bankTradeNo,
		}); err != nil {
			return err
		}
		late := ""
		if when.In(calc.Loc).Format("2006-01-02") > due {
			late = "（晚于到账期限）"
		}
		return audit(ctx, tx, actor, at, "登记到账", "settlement", sid, fmt.Sprintf("登记 %s 到账 ¥%s%s", sid, yuan(s.payable), late), nil,
			map[string]any{"status": "已发送"}, map[string]any{"status": "已到账", "bankTradeNo": bankTradeNo, "paidAt": when})
	})
	if err != nil {
		return SettlementView{}, err
	}
	return a.settlementView(ctx, a.DB, sid, false)
}

// SettlementFilter 结算单列表筛选。
type SettlementFilter struct {
	Month, OperatorID, Status string
	Page, PageSize            int
}

// ListSettlements 结算单列表：按结算月新到旧、同月按运营商编号升序；不含明细。
func (a *App) ListSettlements(ctx context.Context, f SettlementFilter) (Page[SettlementView], error) {
	p := Page[SettlementView]{Page: f.Page, PageSize: f.PageSize, Rows: []SettlementView{}}
	where := `WHERE ($1 = '' OR month = $1) AND ($2 = '' OR operator_id = $2) AND ($3 = '' OR status = $3)`
	if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM settlements `+where, f.Month, f.OperatorID, f.Status).Scan(&p.Total); err != nil {
		return p, err
	}
	rows, err := a.DB.Query(ctx, `SELECT settlement_id FROM settlements `+where+` ORDER BY month DESC, operator_id LIMIT $4 OFFSET $5`,
		f.Month, f.OperatorID, f.Status, f.PageSize, (f.Page-1)*f.PageSize)
	if err != nil {
		return p, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return p, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		v, err := a.settlementView(ctx, a.DB, id, false)
		if err != nil {
			return p, err
		}
		p.Rows = append(p.Rows, v)
	}
	return p, nil
}

// GetSettlement 结算单详情；scope 不为空时只能看本运营商的。
func (a *App) GetSettlement(ctx context.Context, scope, sid string, withDetails bool) (SettlementView, error) {
	v, err := a.settlementView(ctx, a.DB, sid, withDetails)
	if err != nil {
		return v, err
	}
	if scope != "" && scope != v.OperatorID {
		return SettlementView{}, errScope
	}
	return v, nil
}

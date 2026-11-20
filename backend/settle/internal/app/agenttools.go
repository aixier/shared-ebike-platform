package app

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"bike/settle/internal/calc"
)

// ReconTools 是对账智能体要用的工具（README"对账智能体"）：读结算明细、读订单事实、写调整项草稿、写异议比对结论。
// 工具只取数、算数、写记录，不做判断；金额一律由代码算，草稿由财务确认（confirmSettlementAdjustment）。
// 智能体本身的运行时与模型未定（任务 13 未开始），工具供其接入时直接调用。
type ReconTools struct{ App *App }

// ReadSettlement 读结算单（含逐单明细与调整项）。
func (t ReconTools) ReadSettlement(ctx context.Context, settlementID string) (SettlementView, error) {
	return t.App.GetSettlement(ctx, "", settlementID, true)
}

// ReadOrderFacts 读订单事实（settle 的订单投影，不含用户）。
func (t ReconTools) ReadOrderFacts(ctx context.Context, orderIDs []string) ([]OperatorOrderView, error) {
	return t.App.operatorOrdersByID(ctx, t.App.DB, orderIDs)
}

// AdjustmentDraftInput 调整项草稿：智能体只给订单与理由，基数与差额由代码按当前事实复算。
type AdjustmentDraftInput struct {
	SettlementID string
	OrderIDs     []string
	Reason       string
	Agent        Actor // kind 须为"智能体"
}

// WriteAdjustmentDraft 为待审核结算单写入未确认的调整项（每个类型与品牌一条）。
// 只收录"当前基数与已结算基数不同"或"上期之前结束但尚未结算"的订单；没有差额返回 INVALID。
func (t ReconTools) WriteAdjustmentDraft(ctx context.Context, in AdjustmentDraftInput) ([]string, error) {
	if in.Agent.Kind != "智能体" || in.Agent.ID == "" {
		return nil, errInvalid("草稿的生成人须为智能体")
	}
	if utf8.RuneCountInString(strings.TrimSpace(in.Reason)) < 4 {
		return nil, errReason
	}
	if len(in.OrderIDs) == 0 {
		return nil, errInvalid("请给出订单号")
	}
	a := t.App
	at := a.Now()
	var ids []string
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, generateLockKey); err != nil {
			return err
		}
		s, err := lockSettlement(ctx, tx, in.SettlementID)
		if err != nil {
			return err
		}
		if s.status != "待审核" {
			return errState("只有待审核的结算单可以写入调整项")
		}
		vs, err := loadVersions(ctx, tx)
		if err != nil {
			return err
		}
		// 用下一结算月作为出单月：本单月及以前的订单都只会形成调整项，不会形成明细
		input, err := loadInputs(ctx, tx, s.operatorID, s.month, at, vs)
		if err != nil {
			return err
		}
		want := map[string]bool{}
		for _, id := range in.OrderIDs {
			want[id] = true
		}
		var orders []calc.Order
		for _, o := range input.Orders {
			if want[o.OrderID] {
				orders = append(orders, o)
			}
		}
		input.Orders = orders
		input.Month = calc.NextMonth(s.month)
		_, drafts := calc.Build(input)
		if len(drafts) == 0 {
			return errInvalid("这些订单当前没有需要调整的差额")
		}
		var seq int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM adjustments WHERE settlement_id = $1`, s.id).Scan(&seq); err != nil {
			return err
		}
		for _, d := range drafts {
			seq++
			aid, err := a.insertAdjustment(ctx, tx, s.id, s.operatorID, s.month, seq, d, strings.TrimSpace(in.Reason), in.Agent, at, nil)
			if err != nil {
				return err
			}
			ids = append(ids, aid)
		}
		return nil
	})
	return ids, err
}

// WriteDisputeComparison 写入对账智能体对异议的比对结论与建议（只写这两个字段，不改状态）。
func (t ReconTools) WriteDisputeComparison(ctx context.Context, disputeID, comparison string, suggestion *string, agent Actor) error {
	if agent.Kind != "智能体" {
		return errInvalid("比对结论的填写人须为智能体")
	}
	if strings.TrimSpace(comparison) == "" {
		return errInvalid("比对结论不能为空")
	}
	tag, err := t.App.DB.Exec(ctx, `UPDATE disputes SET agent_comparison = $2, agent_suggestion = $3 WHERE dispute_id = $1 AND status = '待核实'`, disputeID, comparison, suggestion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := t.App.disputeView(ctx, t.App.DB, disputeID, false); err != nil {
			var ae *ApiError
			if errors.As(err, &ae) {
				return ae
			}
			return err
		}
		return apiErr(409, "ALREADY_DONE", "该异议已处理")
	}
	return nil
}

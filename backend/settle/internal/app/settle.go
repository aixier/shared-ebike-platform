package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"bike/settle/internal/calc"
)

// querier 是连接池与事务共有的查询方法。
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// 出单与调整项写入共用的事务级锁，保证同一时刻只有一个出单在跑。
const generateLockKey = 731100001

// ---------------- 取数 ----------------

func loadVersions(ctx context.Context, q querier) (map[string]calc.Version, error) {
	rows, err := q.Query(ctx, `SELECT version_id, effective_at, basis, ratios, start_fee, start_minutes, per_minute, dispatch_fee, allocation, revoked_at IS NOT NULL
		FROM versions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]calc.Version{}
	for rows.Next() {
		var v calc.Version
		var rb []byte
		if err := rows.Scan(&v.ID, &v.EffectiveAt, &v.Basis, &rb, &v.StartFee, &v.StartMinutes, &v.PerMinute, &v.DispatchFee, &v.Allocation, &v.Revoked); err != nil {
			return nil, err
		}
		var rs map[string]string
		if err := json.Unmarshal(rb, &rs); err != nil {
			return nil, err
		}
		v.Ratios = map[string]calc.Ratio{}
		for k, s := range rs {
			r, err := calc.ParseRatio(s)
			if err != nil {
				return nil, err
			}
			v.Ratios[k] = r
		}
		// 已撤销的版本不参与取值
		if !v.Revoked {
			out[v.ID] = v
		}
	}
	return out, rows.Err()
}

const orderCols = `o.order_id, o.brand_id, o.operator_id, o.pricing_version_id, o.request_at, o.start_at, o.end_at, o.report_received_at,
	o.billed_minutes, o.distance_km, o.degraded, o.unlock_result, o.has_bill, o.receivable, o.dispatch_fee, o.payable, o.month_card_deduct`

func scanOrder(row pgx.Row) (calc.Order, error) {
	var o calc.Order
	var req, start, end, rr *time.Time
	var bm *int
	var ur *string
	var rec, disp, pay, mcd *int64
	if err := row.Scan(&o.OrderID, &o.BrandID, &o.OperatorID, &o.PricingVersionID, &req, &start, &end, &rr, &bm, &o.DistanceKm, &o.Degraded, &ur, &o.HasBill, &rec, &disp, &pay, &mcd); err != nil {
		return o, err
	}
	o.RequestAt, o.StartAt, o.EndAt, o.ReportReceivedAt = deref(req), deref(start), deref(end), deref(rr)
	if bm != nil {
		o.BilledMinutes = *bm
	}
	if ur != nil {
		o.UnlockResult = *ur
	}
	o.Receivable, o.DispatchFee, o.Payable, o.MonthCardDeduct = deref(rec), deref(disp), deref(pay), deref(mcd)
	return o, nil
}

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

// unlockFailedSQL 订单别名为 o 时，判断该单开锁失败（开锁失败不计入结算）。
const unlockFailedSQL = `EXISTS (SELECT 1 FROM order_unlock u WHERE u.order_id = o.order_id AND u.state = '开锁失败')`

// loadInputs 取某运营商出单所需的事实：本月及以前未结算的订单，以及上次结算后有退款或异议更正的已结算订单。
func loadInputs(ctx context.Context, q querier, operatorID, month string, at time.Time, vs map[string]calc.Version) (calc.Input, error) {
	in := calc.Input{Month: month, At: at, Versions: vs, Refunds: map[string][]calc.Refund{}, Corrections: map[string][]calc.Correction{}, Settled: map[string]calc.Settled{}}
	rows, err := q.Query(ctx, `SELECT `+orderCols+`, s.settled_base, s.settled_fee, s.last_settled_at, s.settlement_id
		FROM orders o LEFT JOIN order_settle s ON s.order_id = o.order_id
		WHERE o.operator_id = $1 AND o.ride_ended AND NOT `+unlockFailedSQL+` AND (
			(s.order_id IS NULL AND o.settle_month <= $2)
			OR (s.order_id IS NOT NULL AND s.last_settled_at < $3 AND (
				EXISTS (SELECT 1 FROM refunds r WHERE r.order_id = o.order_id AND r.refunded_at > s.last_settled_at AND r.refunded_at <= $3)
				OR EXISTS (SELECT 1 FROM corrections c WHERE c.order_id = o.order_id AND c.decided_at > s.last_settled_at AND c.decided_at <= $3))))`,
		operatorID, month, at)
	if err != nil {
		return in, err
	}
	var ids []string
	for rows.Next() {
		var o calc.Order
		var req, start, end, rr *time.Time
		var bm *int
		var ur *string
		var rec, disp, pay, mcd *int64
		var sb, sf *int64
		var sat *time.Time
		var sid *string
		if err := rows.Scan(&o.OrderID, &o.BrandID, &o.OperatorID, &o.PricingVersionID, &req, &start, &end, &rr, &bm, &o.DistanceKm, &o.Degraded, &ur, &o.HasBill, &rec, &disp, &pay, &mcd, &sb, &sf, &sat, &sid); err != nil {
			rows.Close()
			return in, err
		}
		o.RequestAt, o.StartAt, o.EndAt, o.ReportReceivedAt = deref(req), deref(start), deref(end), deref(rr)
		o.BilledMinutes, o.UnlockResult = deref(bm), deref(ur)
		o.Receivable, o.DispatchFee, o.Payable, o.MonthCardDeduct = deref(rec), deref(disp), deref(pay), deref(mcd)
		in.Orders = append(in.Orders, o)
		ids = append(ids, o.OrderID)
		if sid != nil {
			in.Settled[o.OrderID] = calc.Settled{Base: *sb, Fee: *sf, At: *sat, SettlementID: *sid}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}
	if err := loadFacts(ctx, q, ids, in.Refunds, in.Corrections); err != nil {
		return in, err
	}
	return in, nil
}

func loadFacts(ctx context.Context, q querier, ids []string, refunds map[string][]calc.Refund, corr map[string][]calc.Correction) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `SELECT order_id, refunded_total, net_received, kind, refunded_at FROM refunds WHERE order_id = ANY($1) ORDER BY refunded_at`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r calc.Refund
		if err := rows.Scan(&r.OrderID, &r.RefundedTotal, &r.NetReceived, &r.Kind, &r.At); err != nil {
			rows.Close()
			return err
		}
		refunds[r.OrderID] = append(refunds[r.OrderID], r)
	}
	rows.Close()
	rows, err = q.Query(ctx, `SELECT order_id, dispute_id, base_delta, decided_at FROM corrections WHERE order_id = ANY($1) ORDER BY decided_at`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c calc.Correction
		if err := rows.Scan(&c.OrderID, &c.DisputeID, &c.BaseDelta, &c.At); err != nil {
			return err
		}
		corr[c.OrderID] = append(corr[c.OrderID], c)
	}
	return rows.Err()
}

// monthCard 计算当月全平台月卡收入与按品牌分摊（截至 at 已收到报告的订单）。
func monthCard(ctx context.Context, q querier, month string, at time.Time) (MonthCardView, map[string]BrandCard, error) {
	start, _ := calc.MonthStart(month)
	end := start.AddDate(0, 1, 0)
	var mc MonthCardView
	if err := q.QueryRow(ctx, `SELECT COALESCE(SUM(price), 0) FROM card_purchases WHERE paid_at >= $1 AND paid_at < $2`, start, end).Scan(&mc.PlatformIncome); err != nil {
		return mc, nil, err
	}
	// 分配规则与口径取该月末有效的规则版本
	var basis string
	err := q.QueryRow(ctx, `SELECT allocation, basis FROM versions WHERE revoked_at IS NULL AND effective_at < $1 ORDER BY effective_at DESC LIMIT 1`, end).Scan(&mc.AllocationRule, &basis)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return mc, nil, err
	}
	rows, err := q.Query(ctx, `SELECT brand_id, count(*) FROM orders WHERE settle_month = $1 AND ride_ended AND has_bill AND month_card_deduct > 0 AND NOT EXISTS (SELECT 1 FROM order_unlock u WHERE u.order_id = orders.order_id AND u.state = '开锁失败')
		AND report_received_at <= $2 GROUP BY brand_id`, month, at)
	if err != nil {
		return mc, nil, err
	}
	counts := map[string]int{}
	for rows.Next() {
		var b string
		var n int
		if err := rows.Scan(&b, &n); err != nil {
			rows.Close()
			return mc, nil, err
		}
		counts[b] = n
	}
	rows.Close()
	mc.PlatformCardOrders = calc.SumCounts(counts)
	cards := map[string]BrandCard{}
	var shares map[string]int64
	switch mc.AllocationRule {
	case "按骑行单数", "":
		if mc.AllocationRule == "" {
			mc.AllocationRule = "按骑行单数"
		}
		shares = calc.Allocate(mc.PlatformIncome, counts)
		if basis == calc.BasisReceivable || basis == "" {
			mc.Note = strPtr("服务费基数取订单应收，月卡收入分摊仅作收入归属展示，不进入服务费计算")
		}
	default:
		// 只实现了 R-19 的默认规则；其他规则不冒充，分摊额留 0 并说明
		mc.Note = strPtr(fmt.Sprintf("分配规则「%s」本模块尚未实现，本期未分摊", mc.AllocationRule))
	}
	for b, n := range counts {
		cards[b] = BrandCard{Count: n, Share: shares[b]}
	}
	return mc, cards, nil
}

type brandInfo struct{ name, operatorID string }

func loadBrands(ctx context.Context, q querier) (map[string]brandInfo, error) {
	rows, err := q.Query(ctx, `SELECT brand_id, name, operator_id FROM brands`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]brandInfo{}
	for rows.Next() {
		var id string
		var b brandInfo
		if err := rows.Scan(&id, &b.name, &b.operatorID); err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, rows.Err()
}

func operatorName(ctx context.Context, q querier, operatorID string) string {
	var n string
	_ = q.QueryRow(ctx, `SELECT operator_short_name FROM brands WHERE operator_id = $1 AND operator_short_name <> '' ORDER BY updated_at DESC LIMIT 1`, operatorID).Scan(&n)
	return n
}

func operatorExists(ctx context.Context, q querier, operatorID string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM brands WHERE operator_id = $1) OR EXISTS(SELECT 1 FROM orders WHERE operator_id = $1)`, operatorID).Scan(&ok)
	return ok, err
}

func brandName(bs map[string]brandInfo, id string) string {
	if b, ok := bs[id]; ok && b.name != "" {
		return b.name
	}
	return id
}

// adjustmentReason 代码生成的调整项原因（只陈述事实）。
func adjustmentReason(d calc.AdjDraft, bname string) string {
	switch d.Type {
	case calc.TypeLate:
		days := []string{}
		seen := map[string]bool{}
		for _, it := range d.Items {
			t := it.EndAt.In(calc.Loc)
			s := fmt.Sprintf("%d 月 %d 日", int(t.Month()), t.Day())
			if !seen[s] {
				seen[s] = true
				days = append(days, s)
			}
		}
		return fmt.Sprintf("%s %s结束、出单后才收到运营商补报的 %d 单，按原结算月计价后计入本期", bname, strings.Join(days, "、"), len(d.Items))
	case calc.TypeDispute:
		return fmt.Sprintf("%s 异议 %s 经财务核实采纳，%d 单服务费基数更正", bname, d.DisputeID, len(d.Items))
	default:
		return fmt.Sprintf("%s 上期已结算的 %d 单在出单后发生退款，按退款后金额更正", bname, len(d.Items))
	}
}

func settlementID(month, operatorID string) string {
	return "ST-M-" + strings.ReplaceAll(month, "-", "") + "-" + operatorID
}

func adjustmentID(month, operatorID string, seq int) string {
	return fmt.Sprintf("ADJ-%s-%s-%02d", strings.ReplaceAll(month, "-", ""), operatorID, seq)
}

// ---------------- 出单 ----------------

var (
	errAlreadyGenerated = apiErr(409, "ALREADY_GENERATED", "该结算月的结算单已生成")
)

func errMonthNotEnded(month string) *ApiError {
	return apiErr(409, "MONTH_NOT_ENDED", fmt.Sprintf("%s 结算月尚未结束，%s-01 起可生成", month, calc.NextMonth(month)))
}

// Generate 为该结算月各运营商出单。trigger 为"自动出单"或"财务手动"。
func (a *App) Generate(ctx context.Context, month, trigger string, actor Actor) ([]SettlementView, error) {
	if !calc.ValidMonth(month) {
		return nil, errInvalid("结算月格式为 YYYY-MM")
	}
	at := a.Now()
	next, _ := calc.MonthStart(calc.NextMonth(month))
	if at.Before(next) {
		return nil, errMonthNotEnded(month)
	}
	var ids []string
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, generateLockKey); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM settle_months WHERE month = $1)`, month).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return errAlreadyGenerated
		}
		vs, err := loadVersions(ctx, tx)
		if err != nil {
			return err
		}
		brands, err := loadBrands(ctx, tx)
		if err != nil {
			return err
		}
		mc, cards, err := monthCard(ctx, tx, month, at)
		if err != nil {
			return err
		}
		ops, err := listOperatorIDs(ctx, tx)
		if err != nil {
			return err
		}
		for _, op := range ops {
			in, err := loadInputs(ctx, tx, op, month, at, vs)
			if err != nil {
				return err
			}
			lines, adjs := calc.Build(in)
			if len(lines) == 0 && len(adjs) == 0 {
				continue
			}
			sid := settlementID(month, op)
			if err := a.persistSettlement(ctx, tx, sid, op, month, at, trigger, actor, lines, adjs, mc, cards, brands); err != nil {
				return err
			}
			ids = append(ids, sid)
		}
		_, err = tx.Exec(ctx, `INSERT INTO settle_months(month, generated_at, trigger) VALUES ($1, $2, $3)`, month, at, trigger)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]SettlementView, 0, len(ids))
	for _, id := range ids {
		v, err := a.settlementView(ctx, a.DB, id, false)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func listOperatorIDs(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT operator_id FROM brands UNION SELECT operator_id FROM orders ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

func (a *App) persistSettlement(ctx context.Context, tx pgx.Tx, sid, op, month string, at time.Time, trigger string, actor Actor,
	lines []calc.Line, adjs []calc.AdjDraft, mc MonthCardView, cards map[string]BrandCard, brands map[string]brandInfo) error {
	var note *string
	var exited bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operator_exits WHERE operator_id = $1)`, op).Scan(&exited); err != nil {
		return err
	}
	if exited {
		note = strPtr("运营商已退出，本期为最后一期结算")
	}
	// 结算单只保留本运营商品牌的月卡分摊
	own := map[string]BrandCard{}
	for b, c := range cards {
		if brands[b].operatorID == op {
			own[b] = c
		}
	}
	mcb, _ := json.Marshal(mc)
	cb, _ := json.Marshal(own)
	if _, err := tx.Exec(ctx, `INSERT INTO settlements(settlement_id, operator_id, month, status, generated_at, due_date, month_card, brand_cards, note)
		VALUES ($1,$2,$3,'待审核',$4,$5,$6,$7,$8)`, sid, op, month, at, calc.DueDate(month), mcb, cb, note); err != nil {
		return err
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `INSERT INTO settlement_lines(settlement_id, order_id, brand_id, start_at, end_at, billed_minutes, distance_km, degraded,
			pricing_version_id, receivable, refunded_at_generation, base, ratio, fee, remark) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			sid, l.OrderID, l.BrandID, nullTime(l.StartAt), l.EndAt, l.BilledMinutes, l.DistanceKm, l.Degraded, l.PricingVersionID,
			l.Receivable, l.RefundedAtGeneration, l.Base, int64(l.Ratio), l.Fee, strPtr(l.Remark)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO order_settle(order_id, settlement_id, kind, settled_base, settled_fee, last_settled_at)
			VALUES ($1,$2,'明细',$3,$4,$5)`, l.OrderID, sid, l.Base, l.Fee, at); err != nil {
			return err
		}
	}
	for i, d := range adjs {
		reason := adjustmentReason(d, brandName(brands, d.BrandID))
		if _, err := a.insertAdjustment(ctx, tx, sid, op, month, i+1, d, reason, SystemActor, at, nil); err != nil {
			return err
		}
	}
	tot, _ := calc.Summarize(lines, adjs)
	if err := emit(ctx, tx, "settle.settlement.generated", sid, actor, at, map[string]any{
		"settlementId": sid, "operatorId": op, "month": month, "orderCount": tot.OrderCount,
		"detailFee": tot.DetailFee, "adjustmentFee": tot.AdjustmentFee, "payableFee": tot.PayableFee,
		"generatedAt": at, "dueDate": calc.DueDate(month), "trigger": trigger,
	}); err != nil {
		return err
	}
	name := operatorName(ctx, tx, op)
	if name == "" {
		name = op
	}
	return audit(ctx, tx, actor, at, "生成结算单", "settlement", sid,
		fmt.Sprintf("生成 %s %s 结算单，应付服务费 ¥%s", name, month, yuan(tot.PayableFee)), nil, nil,
		map[string]any{"status": "待审核", "payableFee": tot.PayableFee, "trigger": trigger})
}

// insertAdjustment 写一条带逐单记录的调整项，并更新这些订单的结算状态与指针。
func (a *App) insertAdjustment(ctx context.Context, tx pgx.Tx, sid, op, month string, seq int, d calc.AdjDraft, reason string, by Actor, at time.Time, confirmedBy *Actor) (string, error) {
	aid := adjustmentID(month, op, seq)
	byb, _ := json.Marshal(by)
	var cb []byte
	var cat *time.Time
	if confirmedBy != nil {
		cb, _ = json.Marshal(confirmedBy)
		cat = &at
	}
	if _, err := tx.Exec(ctx, `INSERT INTO adjustments(adjustment_id, settlement_id, seq, type, brand_id, base_delta, fee_delta, reason, dispute_id, created_by, created_at, confirmed_by, confirmed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, aid, sid, seq, d.Type, d.BrandID, d.BaseDelta, d.FeeDelta, reason, strPtr(d.DisputeID), byb, at, cb, cat); err != nil {
		return "", err
	}
	for _, it := range d.Items {
		if _, err := tx.Exec(ctx, `INSERT INTO adjustment_items(adjustment_id, order_id, end_at, previous_base, new_base, base_delta, ratio, fee_delta)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, aid, it.OrderID, it.EndAt, it.PreviousBase, it.NewBase, it.BaseDelta, int64(it.Ratio), it.FeeDelta); err != nil {
			return "", err
		}
		if d.Type == calc.TypeLate {
			if _, err := tx.Exec(ctx, `INSERT INTO order_settle(order_id, settlement_id, kind, adjustment_id, settled_base, settled_fee, last_settled_at, note)
				VALUES ($1,$2,'调整项',$3,$4,$5,$6,$7)`, it.OrderID, sid, aid, it.NewBase, it.FeeDelta, at,
				fmt.Sprintf("出单后才收到补报，计入 %s 结算单调整项", month)); err != nil {
				return "", err
			}
			continue
		}
		kind := "出单后退款"
		if d.Type == calc.TypeDispute {
			kind = "异议核实"
		}
		// 退款与异议类调整项不改指针，只更新累计与差额去向
		if _, err := tx.Exec(ctx, `UPDATE order_settle SET settled_base = $2, settled_fee = settled_fee + $3, last_settled_at = $4, note = $5 WHERE order_id = $1`,
			it.OrderID, it.NewBase, it.FeeDelta, at, fmt.Sprintf("%s，差额计入 %s 结算单调整项 %s", kind, month, aid)); err != nil {
			return "", err
		}
	}
	if d.DisputeID != "" {
		if _, err := tx.Exec(ctx, `UPDATE disputes SET adjustment_id = $2 WHERE dispute_id = $1 AND adjustment_id IS NULL`, d.DisputeID, aid); err != nil {
			return "", err
		}
	}
	if err := emit(ctx, tx, "settle.settlement.adjusted", sid, by, at, map[string]any{
		"settlementId": sid, "adjustmentId": aid, "brandId": d.BrandID, "type": d.Type, "feeDelta": d.FeeDelta,
		"confirmed": confirmedBy != nil, "at": at,
	}); err != nil {
		return "", err
	}
	return aid, nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// yuan 把分格式化为两位小数的元。
func yuan(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

// ---------------- 读结算单 ----------------

func loadAdjustments(ctx context.Context, q querier, sid string, brands map[string]brandInfo) ([]AdjustmentView, error) {
	rows, err := q.Query(ctx, `SELECT adjustment_id, type, brand_id, order_ids, base_delta, fee_delta, reason, dispute_id, created_by, created_at, confirmed_by, confirmed_at
		FROM adjustments WHERE settlement_id = $1 ORDER BY seq`, sid)
	if err != nil {
		return nil, err
	}
	var out []AdjustmentView
	for rows.Next() {
		var v AdjustmentView
		var oids, cby, conf []byte
		if err := rows.Scan(&v.AdjustmentID, &v.Type, &v.BrandID, &oids, &v.BaseDelta, &v.FeeDelta, &v.Reason, &v.DisputeID, &cby, &v.CreatedAt, &conf, &v.ConfirmedAt); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(oids, &v.OrderIDs)
		_ = json.Unmarshal(cby, &v.CreatedBy)
		if len(conf) > 0 {
			var c Actor
			_ = json.Unmarshal(conf, &c)
			v.ConfirmedBy = &c
		}
		if v.OrderIDs == nil {
			v.OrderIDs = []string{}
		}
		v.Items = []AdjustmentItemView{}
		v.BrandName = brandName(brands, v.BrandID)
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, v := range out {
		idx[v.AdjustmentID] = i
	}
	irows, err := q.Query(ctx, `SELECT i.adjustment_id, i.order_id, i.end_at, i.previous_base, i.new_base, i.base_delta, i.ratio, i.fee_delta
		FROM adjustment_items i JOIN adjustments a ON a.adjustment_id = i.adjustment_id WHERE a.settlement_id = $1 ORDER BY i.end_at, i.order_id`, sid)
	if err != nil {
		return nil, err
	}
	defer irows.Close()
	for irows.Next() {
		var aid string
		var it AdjustmentItemView
		var r int64
		if err := irows.Scan(&aid, &it.OrderID, &it.EndAt, &it.PreviousBase, &it.NewBase, &it.BaseDelta, &r, &it.FeeDelta); err != nil {
			return nil, err
		}
		it.Ratio = calc.Ratio(r)
		if i, ok := idx[aid]; ok {
			out[i].Items = append(out[i].Items, it)
		}
	}
	if out == nil {
		out = []AdjustmentView{}
	}
	return out, irows.Err()
}

func loadLines(ctx context.Context, q querier, sid string) ([]calc.Line, error) {
	rows, err := q.Query(ctx, `SELECT order_id, brand_id, start_at, end_at, billed_minutes, distance_km, degraded, pricing_version_id, receivable,
		refunded_at_generation, base, ratio, fee, remark FROM settlement_lines WHERE settlement_id = $1 ORDER BY end_at, order_id`, sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []calc.Line
	for rows.Next() {
		var l calc.Line
		var st *time.Time
		var bm *int
		var r int64
		var remark *string
		if err := rows.Scan(&l.OrderID, &l.BrandID, &st, &l.EndAt, &bm, &l.DistanceKm, &l.Degraded, &l.PricingVersionID, &l.Receivable,
			&l.RefundedAtGeneration, &l.Base, &r, &l.Fee, &remark); err != nil {
			return nil, err
		}
		l.StartAt, l.BilledMinutes, l.Ratio, l.Remark = deref(st), deref(bm), calc.Ratio(r), deref(remark)
		out = append(out, l)
	}
	return out, rows.Err()
}

func lineViews(lines []calc.Line) []LineView {
	out := make([]LineView, 0, len(lines))
	for _, l := range lines {
		out = append(out, LineView{OrderID: l.OrderID, BrandID: l.BrandID, StartAt: nullTime(l.StartAt), EndAt: l.EndAt, BilledMinutes: l.BilledMinutes,
			DistanceKm: l.DistanceKm, Degraded: l.Degraded, PricingVersionID: l.PricingVersionID, Receivable: l.Receivable,
			RefundedAtGeneration: l.RefundedAtGeneration, Base: l.Base, Ratio: l.Ratio, Fee: l.Fee, Remark: strPtr(l.Remark)})
	}
	return out
}

// assemble 由明细、调整项与月卡分摊组装结算单视图（合计与品牌汇总由 calc 计算）。
func assemble(v *SettlementView, lines []calc.Line, adjs []AdjustmentView, cards map[string]BrandCard, brands map[string]brandInfo, withDetails bool) {
	drafts := make([]calc.AdjDraft, 0, len(adjs))
	for _, a := range adjs {
		drafts = append(drafts, calc.AdjDraft{Type: a.Type, BrandID: a.BrandID, FeeDelta: a.FeeDelta, BaseDelta: a.BaseDelta})
	}
	tot, bs := calc.Summarize(lines, drafts)
	v.Totals = tot
	v.Adjustments = adjs
	v.DetailCount = len(lines)
	v.Brands = make([]BrandView, 0, len(bs))
	for _, b := range bs {
		bv := BrandView{BrandID: b.BrandID, BrandName: brandName(brands, b.BrandID), OrderCount: b.OrderCount, ReceivableSum: b.ReceivableSum,
			BaseSum: b.BaseSum, DetailFee: b.DetailFee, AdjustmentFee: b.AdjustmentFee, RatioBreakdown: []RatioGroupView{}}
		for _, g := range b.RatioBreakdown {
			bv.RatioBreakdown = append(bv.RatioBreakdown, RatioGroupView{Ratio: g.Ratio, PricingVersionIDs: g.PricingVersionIDs, OrderCount: g.OrderCount, BaseSum: g.BaseSum, Fee: g.Fee})
		}
		if c, ok := cards[b.BrandID]; ok {
			bv.MonthCardOrderCount, bv.MonthCardShare = c.Count, c.Share
		}
		v.Brands = append(v.Brands, bv)
	}
	if withDetails {
		d := lineViews(lines)
		v.Details = &d
	}
}

// settlementView 读一张已出单的结算单。
func (a *App) settlementView(ctx context.Context, q querier, sid string, withDetails bool) (SettlementView, error) {
	var v SettlementView
	var rb, mcb, cb []byte
	err := q.QueryRow(ctx, `SELECT settlement_id, operator_id, month, status, generated_at, due_date, reviewed_by, reviewed_at, sent_at, paid_at,
		bank_trade_no, invoice_status, month_card, brand_cards, note FROM settlements WHERE settlement_id = $1`, sid).
		Scan(&v.SettlementID, &v.OperatorID, &v.Month, &v.Status, &v.GeneratedAt, &v.DueDate, &rb, &v.ReviewedAt, &v.SentAt, &v.PaidAt,
			&v.BankTradeNo, &v.InvoiceStatus, &mcb, &cb, &v.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound("该结算单")
	}
	if err != nil {
		return v, err
	}
	if len(rb) > 0 {
		var r Actor
		_ = json.Unmarshal(rb, &r)
		v.ReviewedBy = &r
	}
	_ = json.Unmarshal(mcb, &v.MonthCard)
	cards := map[string]BrandCard{}
	_ = json.Unmarshal(cb, &cards)
	v.OperatorName = operatorName(ctx, q, v.OperatorID)
	v.Overdue = v.PaidAt == nil && a.Now().In(calc.Loc).Format("2006-01-02") > v.DueDate
	brands, err := loadBrands(ctx, q)
	if err != nil {
		return v, err
	}
	lines, err := loadLines(ctx, q, sid)
	if err != nil {
		return v, err
	}
	adjs, err := loadAdjustments(ctx, q, sid, brands)
	if err != nil {
		return v, err
	}
	assemble(&v, lines, adjs, cards, brands, withDetails)
	return v, nil
}

// Preview 本期（未出单）实时预览：按截至现在的事实计算，不落库。
func (a *App) Preview(ctx context.Context, operatorID string) (SettlementView, error) {
	ok, err := operatorExists(ctx, a.DB, operatorID)
	if err != nil {
		return SettlementView{}, err
	}
	if !ok {
		return SettlementView{}, errNotFound("该运营商")
	}
	at := a.Now()
	month := calc.MonthOf(at)
	vs, err := loadVersions(ctx, a.DB)
	if err != nil {
		return SettlementView{}, err
	}
	brands, err := loadBrands(ctx, a.DB)
	if err != nil {
		return SettlementView{}, err
	}
	in, err := loadInputs(ctx, a.DB, operatorID, month, at, vs)
	if err != nil {
		return SettlementView{}, err
	}
	lines, drafts := calc.Build(in)
	mc, cards, err := monthCard(ctx, a.DB, month, at)
	if err != nil {
		return SettlementView{}, err
	}
	v := SettlementView{SettlementID: settlementID(month, operatorID), OperatorID: operatorID, OperatorName: operatorName(ctx, a.DB, operatorID),
		Month: month, GeneratedAt: at, DueDate: calc.DueDate(month), Status: "未出单", MonthCard: mc,
		PreviewNote: strPtr(fmt.Sprintf("本期（%s）尚未出单，按截至现在的订单与事件实时汇总；次月 1 日 00:30 出单", month))}
	var exited bool
	_ = a.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operator_exits WHERE operator_id = $1)`, operatorID).Scan(&exited)
	if exited {
		v.Note = strPtr("运营商已退出，本期为最后一期结算")
	}
	adjs := make([]AdjustmentView, 0, len(drafts))
	for i, d := range drafts {
		av := AdjustmentView{AdjustmentID: adjustmentID(month, operatorID, i+1), Type: d.Type, BrandID: d.BrandID, BrandName: brandName(brands, d.BrandID),
			OrderIDs: []string{}, BaseDelta: d.BaseDelta, FeeDelta: d.FeeDelta, Reason: adjustmentReason(d, brandName(brands, d.BrandID)),
			DisputeID: strPtr(d.DisputeID), CreatedBy: SystemActor, CreatedAt: at}
		for _, it := range d.Items {
			av.Items = append(av.Items, AdjustmentItemView{OrderID: it.OrderID, EndAt: it.EndAt, PreviousBase: it.PreviousBase, NewBase: it.NewBase, BaseDelta: it.BaseDelta, Ratio: it.Ratio, FeeDelta: it.FeeDelta})
		}
		adjs = append(adjs, av)
	}
	own := map[string]BrandCard{}
	for b, c := range cards {
		if brands[b].operatorID == operatorID {
			own[b] = c
		}
	}
	assemble(&v, lines, adjs, own, brands, true)
	return v, nil
}

// sortStrings 去重并排序。
func sortStrings(xs []string) []string {
	m := map[string]bool{}
	out := []string{}
	for _, x := range xs {
		if !m[x] {
			m[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

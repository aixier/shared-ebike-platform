package app

// 每日收单对账：采集渠道对账单（事实采集）→ 按口径算术（recon 包）→ 次日 02:10 出具并冻结 → 财务逐项处理差异。
// trace: R-25、R-18、NFR-09、R-47；SCHEMA 4.5

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"bike/pay/internal/channel"
	"bike/pay/internal/recon"
)

// ReconView 契约 Recon
type ReconView struct {
	ReconID     string  `json:"reconId"`
	GeneratedAt *string `json:"generatedAt"`
	recon.Result
}

const reconFormula = "应到账 = 订单实收 + 月卡款 − 押金抵扣；差异 = 渠道实到 − 应到账"

func reconIDOf(date string) string { return "RC" + date[0:4] + date[5:7] + date[8:10] }

func addDay(date string, n int) string {
	t, _ := dayStart(date)
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// FetchBills 下载某日两个渠道收入商户号的对账单并入库（按渠道交易去重）
func (a *App) FetchBills(ctx context.Context, date string) {
	for _, name := range []string{channel.WeChat, channel.Alipay} {
		prov, err := a.Ch.Get(name)
		if err != nil {
			continue
		}
		lines, err := prov.DownloadBill(ctx, channel.IncomeMerchant, date)
		if err != nil {
			a.Log.Warn("下载渠道对账单失败", "channel", name, "date", date, "err", err)
			continue
		}
		for _, l := range lines {
			if _, err := a.Pool.Exec(ctx, `INSERT INTO channel_bill_lines (channel, merchant, out_no, direction, trade_no, amount, settled_date, fetched_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, name, string(channel.IncomeMerchant), l.OutNo, l.Direction, l.TradeNo, l.Amount, l.SettledDate, a.Now()); err != nil {
				a.Log.Warn("写渠道对账单失败", "err", err)
			}
		}
	}
}

// flowsFor 取与某日相关的成功流水（当日完成或当日到账），按创建时刻排序
func (a *App) flowsFor(ctx context.Context, q querier, date string) ([]recon.Flow, []*Payment, error) {
	start, err := dayStart(date)
	if err != nil {
		return nil, nil, err
	}
	ps, err := a.queryPayments(ctx, q, `WHERE c.status = '成功' AND ((c.completed_at >= $1 AND c.completed_at < $2)
		OR c.payment_id IN (SELECT out_no FROM channel_bill_lines WHERE settled_date = $3::date)) ORDER BY c.created_at, c.seq`,
		start, start.AddDate(0, 0, 1), date)
	if err != nil {
		return nil, nil, err
	}
	fs := make([]recon.Flow, 0, len(ps))
	for _, p := range ps {
		f := recon.Flow{PaymentID: p.PaymentID, Type: p.Type, Account: p.Account, Direction: p.Direction, Channel: p.Channel,
			Status: p.Status, OrderID: deref(p.OrderID), Amount: p.Amount, SettledDate: deref(p.ChannelSettledDate)}
		if p.completedAt != nil {
			f.CompletedDate = dateOf(*p.completedAt)
		}
		fs = append(fs, f)
	}
	return fs, ps, nil
}

// Generate 出具某日对账（已出具的不重出）
func (a *App) Generate(ctx context.Context, date string) error {
	var exists bool
	if err := a.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM recon_reports WHERE date = $1::date)`, date).Scan(&exists); err != nil || exists {
		return err
	}
	a.FetchBills(ctx, date)
	a.FetchBills(ctx, addDay(date, 1)) // 跨日到账：次日账单里能见到的算跨日，见不到的算渠道未到账
	fs, _, err := a.flowsFor(ctx, a.Pool, date)
	if err != nil {
		return err
	}
	r := recon.Compute(fs, date)
	body, _ := json.Marshal(r)
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	id := reconIDOf(date)
	tag, err := tx.Exec(ctx, `INSERT INTO recon_reports (recon_id, date, generated_at, body) VALUES ($1,$2::date,$3,$4) ON CONFLICT DO NOTHING`, id, date, a.Now(), body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 && r.Status == "差异待处理" {
		if err := a.enqueue(ctx, tx, "pay.recon.diff_found", date, systemActor, map[string]any{
			"reconId": id, "date": date, "diff": r.Diff, "itemCount": len(r.DiffItems),
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GenerateDue 出具所有到期（次日 02:10 已过）未出具的日对账
func (a *App) GenerateDue(ctx context.Context) error {
	now := a.Now()
	start, err := a.firstUnreportedDate(ctx)
	if err != nil {
		return err
	}
	for d := start; ; d = addDay(d, 1) {
		due, _ := dayStart(addDay(d, 1))
		if now.Before(due.Add(2*time.Hour + 10*time.Minute)) {
			return nil
		}
		if err := a.Generate(ctx, d); err != nil {
			return err
		}
	}
}

// firstUnreportedDate 最后出具日的次日；从未出具时取最早一笔流水的日期（最多回溯 92 天），没有流水取今天
func (a *App) firstUnreportedDate(ctx context.Context) (string, error) {
	var last *time.Time
	if err := a.Pool.QueryRow(ctx, `SELECT max(date)::timestamp FROM recon_reports`).Scan(&last); err != nil {
		return "", err
	}
	today := dateOf(a.Now())
	if last != nil {
		return addDay(last.Format("2006-01-02"), 1), nil
	}
	var first *time.Time
	if err := a.Pool.QueryRow(ctx, `SELECT min(completed_at) FROM payment_results WHERE completed_at IS NOT NULL`).Scan(&first); err != nil {
		return "", err
	}
	if first == nil {
		return today, nil
	}
	d := dateOf(*first)
	if floor := addDay(today, -92); d < floor {
		d = floor
	}
	return d, nil
}

type handling struct {
	by, note string
}

func (a *App) loadReports(ctx context.Context, where string, args ...any) ([]ReconView, error) {
	rows, err := a.Pool.Query(ctx, `SELECT recon_id, date::text, generated_at, body FROM recon_reports `+where+` ORDER BY date`, args...)
	if err != nil {
		return nil, err
	}
	var out []ReconView
	for rows.Next() {
		var v ReconView
		var date string
		var gen time.Time
		var body []byte
		if err := rows.Scan(&v.ReconID, &date, &gen, &body); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(body, &v.Result); err != nil {
			rows.Close()
			return nil, err
		}
		v.GeneratedAt = fmtTimePtr(&gen)
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 叠加处理记录
	hrows, err := a.Pool.Query(ctx, `SELECT recon_id, idx, handled_by, note FROM recon_diff_handlings`)
	if err != nil {
		return nil, err
	}
	hs := map[string]map[int]handling{}
	for hrows.Next() {
		var id, by, note string
		var idx int
		if err := hrows.Scan(&id, &idx, &by, &note); err != nil {
			hrows.Close()
			return nil, err
		}
		if hs[id] == nil {
			hs[id] = map[int]handling{}
		}
		hs[id][idx] = handling{by, note}
	}
	hrows.Close()
	for i := range out {
		for j := range out[i].DiffItems {
			if h, ok := hs[out[i].ReconID][j]; ok {
				by, note := h.by, h.note
				out[i].DiffItems[j].Status = "已处理"
				out[i].DiffItems[j].HandledBy = &by
				out[i].DiffItems[j].Note = &note
			}
		}
		out[i].Status = recon.Status(out[i].DiffItems)
	}
	return out, nil
}

// liveRecon 当日未出：实时计算，不能处理差异
func (a *App) liveRecon(ctx context.Context, date string) (ReconView, error) {
	fs, _, err := a.flowsFor(ctx, a.Pool, date)
	if err != nil {
		return ReconView{}, err
	}
	r := recon.Compute(fs, date)
	r.Status = "当日未出"
	return ReconView{ReconID: reconIDOf(date), Result: r}, nil
}

// allRecons 已出具的 + 未出具到今天的
func (a *App) allRecons(ctx context.Context) ([]ReconView, error) {
	out, err := a.loadReports(ctx, "")
	if err != nil {
		return nil, err
	}
	today := dateOf(a.Now())
	var from string
	if len(out) > 0 {
		from = addDay(out[len(out)-1].Date, 1)
	} else if from, err = a.firstUnreportedDate(ctx); err != nil {
		return nil, err
	}
	for d := from; d <= today; d = addDay(d, 1) {
		v, err := a.liveRecon(ctx, d)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// ReconTotals 契约 ReconTotals
type ReconTotals struct {
	ChannelActual    int64 `json:"channelActual"`
	OrderNetReceived int64 `json:"orderNetReceived"`
	MonthCardIncome  int64 `json:"monthCardIncome"`
	DepositDeducted  int64 `json:"depositDeducted"`
	Expected         int64 `json:"expected"`
	Diff             int64 `json:"diff"`
}

type reconFilter struct {
	from, to, status string
	page, pageSize   int
	asc              bool
}

func (a *App) listRecon(ctx context.Context, f reconFilter) (map[string]any, error) {
	all, err := a.allRecons(ctx)
	if err != nil {
		return nil, err
	}
	var rows []ReconView
	var t ReconTotals
	for _, r := range all {
		if (f.from != "" && r.Date < f.from) || (f.to != "" && r.Date > f.to) || (f.status != "" && r.Status != f.status) {
			continue
		}
		rows = append(rows, r)
		t.ChannelActual += r.ChannelActual.Total
		t.OrderNetReceived += r.OrderNetReceived
		t.MonthCardIncome += r.MonthCardIncome
		t.DepositDeducted += r.DepositDeducted
		t.Expected += r.Expected
		t.Diff += r.Diff
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if f.asc {
			return rows[i].Date < rows[j].Date
		}
		return rows[i].Date > rows[j].Date
	})
	total := len(rows)
	lo := (f.page - 1) * f.pageSize
	if lo > total {
		lo = total
	}
	hi := lo + f.pageSize
	if hi > total {
		hi = total
	}
	page := rows[lo:hi]
	if page == nil {
		page = []ReconView{}
	}
	return map[string]any{"rows": page, "total": total, "page": f.page, "pageSize": f.pageSize, "totals": t, "formula": reconFormula}, nil
}

func (a *App) getRecon(ctx context.Context, key string) (map[string]any, *APIError, error) {
	all, err := a.allRecons(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range all {
		if r.Date != key && r.ReconID != key {
			continue
		}
		_, ps, err := a.flowsFor(ctx, a.Pool, r.Date)
		if err != nil {
			return nil, nil, err
		}
		pays := []*Payment{}
		for _, p := range ps {
			if p.Account == "骑行收入户" || p.Type == "押金抵扣" {
				pays = append(pays, p)
			}
		}
		b, _ := json.Marshal(r)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		m["payments"] = pays
		return m, nil, nil
	}
	return nil, apiErr("NOT_FOUND", "没有该日的对账记录"), nil
}

func (a *App) handleDiff(ctx context.Context, admin Actor, reconID string, idx int, note string) (*ReconView, *APIError, error) {
	if utf8.RuneCountInString(note) < 4 {
		return nil, apiErr("REASON_REQUIRED", "请填写处理说明（至少 4 个字）"), nil
	}
	reps, err := a.loadReports(ctx, `WHERE recon_id = $1`, reconID)
	if err != nil {
		return nil, nil, err
	}
	if len(reps) == 0 {
		return nil, apiErr("CONFLICT", "当日对账次日 02:10 出具后才能处理差异"), nil
	}
	r := reps[0]
	if idx < 0 || idx >= len(r.DiffItems) {
		return nil, apiErr("NOT_FOUND", "没有找到该差异项"), nil
	}
	it := r.DiffItems[idx]
	if it.Status == "已处理" {
		return nil, apiErr("ALREADY_DONE", "该差异已处理"), nil
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO recon_diff_handlings (recon_id, idx, handled_by, note, handled_at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		reconID, idx, admin.ID, note, a.Now())
	if err != nil {
		return nil, nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apiErr("ALREADY_DONE", "该差异已处理"), nil
	}
	target := reconID
	if it.PaymentID != nil {
		target = *it.PaymentID
	}
	sign := ""
	if it.Amount >= 0 {
		sign = "+"
	}
	summary := fmt.Sprintf("处理 %s 收单对账差异（%s %s%s）", r.Date, it.Type, sign, yuan(it.Amount))
	if err := a.audit(ctx, tx, admin, "处理对账差异", "payment", target, summary, &note,
		map[string]any{"status": "待处理"}, map[string]any{"status": "已处理", "reconId": reconID, "index": idx}); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	reps, err = a.loadReports(ctx, `WHERE recon_id = $1`, reconID)
	if err != nil {
		return nil, nil, err
	}
	return &reps[0], nil, nil
}

// pendingDiffCount 已出具对账里待处理的差异项数
func (a *App) pendingDiffCount(ctx context.Context) (int, error) {
	reps, err := a.loadReports(ctx, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range reps {
		for _, it := range r.DiffItems {
			if it.Status == "待处理" {
				n++
			}
		}
	}
	return n, nil
}

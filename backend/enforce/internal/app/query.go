package app

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// evidenceFilter 与契约 EvidenceFilter 一致；空串表示不限。
type evidenceFilter struct {
	ScanResult string `json:"scanResult,omitempty"`
	BrandID    string `json:"brandId,omitempty"`
	OperatorID string `json:"operatorId,omitempty"`
	OfficerID  string `json:"officerId,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Q          string `json:"q,omitempty"`
}

type pageReq struct {
	page, pageSize   int
	sortKey, sortDir string
}

// 可排序字段（接口字段名 → 列名）
var sortColumns = map[string]string{"capturedAt": "e.captured_at", "submittedAt": "e.submitted_at", "evidenceId": "e.evidence_id"}

func parsePage(r *http.Request) (pageReq, error) {
	q := r.URL.Query()
	p := pageReq{page: 1, pageSize: 20, sortKey: q.Get("sortKey"), sortDir: q.Get("sortDir")}
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return p, errInvalid("页码不对")
		}
		p.page = n
	}
	if v := q.Get("pageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			return p, errInvalid("每页条数应在 1 到 200 之间")
		}
		p.pageSize = n
	}
	if p.sortKey != "" && sortColumns[p.sortKey] == "" {
		return p, errInvalid("不支持按该字段排序")
	}
	if p.sortDir != "" && p.sortDir != "asc" && p.sortDir != "desc" {
		return p, errInvalid("排序方向只能是 asc 或 desc")
	}
	return p, nil
}

// where 把筛选条件转成 SQL 条件与参数；scope 不为空时只含该运营商的证据。
func (f evidenceFilter) where(scope string) (string, []any, error) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.ScanResult != "" {
		if !evidenceScanResults[f.ScanResult] {
			return "", nil, errInvalid("扫码结果只能是无政府码、码已失效或码未启用")
		}
		add("e.scan_result = $%d", f.ScanResult)
	}
	if f.BrandID != "" {
		add("e.brand_id = $%d", f.BrandID)
	}
	if f.OperatorID != "" {
		add("e.operator_id = $%d", f.OperatorID)
	}
	if scope != "" {
		add("e.operator_id = $%d", scope)
	}
	if f.OfficerID != "" {
		add("e.officer_id = $%d", f.OfficerID)
	}
	if f.From != "" {
		d, err := time.ParseInLocation("2006-01-02", f.From, shanghai())
		if err != nil {
			return "", nil, errInvalid("起始日期格式应为 YYYY-MM-DD")
		}
		add("e.captured_at >= $%d", d)
	}
	if f.To != "" {
		d, err := time.ParseInLocation("2006-01-02", f.To, shanghai())
		if err != nil {
			return "", nil, errInvalid("截止日期格式应为 YYYY-MM-DD")
		}
		add("e.captured_at < $%d", d.AddDate(0, 0, 1))
	}
	if f.Q != "" {
		args = append(args, f.Q)
		n := len(args)
		conds = append(conds, fmt.Sprintf("(strpos(e.evidence_id, $%[1]d) > 0 OR strpos(e.vehicle_description, $%[1]d) > 0 OR strpos(e.address, $%[1]d) > 0 OR strpos(COALESCE(e.plate_no,''), $%[1]d) > 0)", n))
	}
	if len(conds) == 0 {
		return "", args, nil
	}
	return "WHERE " + strings.Join(conds, " AND ") + " ", args, nil
}

func (a *App) listEvidence(ctx context.Context, f evidenceFilter, scope string, p pageReq) (map[string]any, error) {
	where, args, err := f.where(scope)
	if err != nil {
		return nil, err
	}
	var total int
	if err := a.Pool.QueryRow(ctx, "SELECT count(*) FROM evidence e "+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	col := sortColumns[p.sortKey]
	if col == "" {
		col = "e.captured_at"
	}
	dir := "DESC"
	if p.sortDir == "asc" {
		dir = "ASC"
	}
	args = append(args, p.pageSize, (p.page-1)*p.pageSize)
	sql := fmt.Sprintf("%s%sORDER BY %s %s, e.evidence_id %s LIMIT $%d OFFSET $%d", evidenceSelect, where, col, dir, dir, len(args)-1, len(args))
	rows, err := a.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		ev, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"rows": list, "total": total, "page": p.page, "pageSize": p.pageSize}, nil
}

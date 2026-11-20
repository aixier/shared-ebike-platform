package app

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"bike/enforce/internal/auth"
)

var exportColumns = []string{"证据编号", "取证时间", "执法人员工号", "核查结果", "品牌", "运营商", "车辆描述", "地址", "经度", "纬度", "照片数", "SHA-256"}

func (a *App) handleExport(w http.ResponseWriter, r *http.Request) error {
	adm, err := a.perm(r, true, "evidence.export")
	if err != nil {
		return err
	}
	var body struct {
		Filter evidenceFilter `json:"filter"`
		Reason *string        `json:"reason"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	if body.Reason != nil && utf8.RuneCountInString(strings.TrimSpace(*body.Reason)) < 4 {
		return errReasonRequired
	}
	reason := emptyToNil(body.Reason)
	if _, _, err := body.Filter.where(adm.Scope); err != nil {
		return err
	}
	return a.runIdem(w, r, "export:"+adm.AccountID, func(tx pgx.Tx) (int, any, error) {
		res, err := a.export(r.Context(), tx, adm, body.Filter, reason)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, res, nil
	})
}

func (a *App) export(ctx context.Context, tx pgx.Tx, adm auth.Admin, f evidenceFilter, reason *string) (map[string]any, error) {
	where, args, err := f.where(adm.Scope)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT e.evidence_id, e.captured_at_text, e.badge_no, e.scan_result, COALESCE(b.name,''), COALESCE(o.short_name,''),
			e.vehicle_description, e.address, e.lng, e.lat, (SELECT count(*) FROM evidence_photos ep WHERE ep.evidence_id = e.evidence_id), e.sha256
		FROM evidence e
		LEFT JOIN brands b ON b.brand_id = e.brand_id
		LEFT JOIN operators o ON o.operator_id = e.operator_id `+where+"ORDER BY e.captured_at, e.evidence_id", args...)
	if err != nil {
		return nil, err
	}
	var buf strings.Builder
	buf.WriteString(string(rune(0xFEFF))) // 带 BOM，表格软件按 UTF-8 打开
	cw := csv.NewWriter(&buf)
	_ = cw.Write(exportColumns)
	ids := []string{}
	for rows.Next() {
		var id, capText, badge, result, brand, op, desc, addr, sum string
		var lng, lat float64
		var photos int
		if err := rows.Scan(&id, &capText, &badge, &result, &brand, &op, &desc, &addr, &lng, &lat, &photos, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		_ = cw.Write([]string{id, capText, badge, result, brand, op, desc, addr,
			strconv.FormatFloat(lng, 'f', -1, 64), strconv.FormatFloat(lat, 'f', -1, 64), strconv.Itoa(photos), sum})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, errInvalid("没有符合条件的证据，换个筛选条件试试")
	}
	cw.Flush()

	now := a.now()
	day := now.Format("20060102")
	seq, err := nextSeq(ctx, tx, "export", day)
	if err != nil {
		return nil, err
	}
	exportID := fmt.Sprintf("EV-EXP-%s-%02d", day, seq)
	fileName := "执法证据_" + now.Format("200601021504") + ".zip"
	filterJSON, _ := json.Marshal(f)
	if _, err := tx.Exec(ctx, `INSERT INTO evidence_exports (export_id, at, by_id, by_name, filter, evidence_ids, count, file_name, reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, exportID, now, adm.AccountID, adm.Name, filterJSON, ids, len(ids), fileName, reason); err != nil {
		return nil, err
	}
	act := personActor(adm.AccountID, adm.Name)
	if err := a.enqueue(ctx, tx, "enforce.evidence.exported", exportID, act, map[string]any{
		"exportId": exportID, "count": len(ids), "filter": f, "exportedAt": fmtTime(now),
	}); err != nil {
		return nil, err
	}
	if err := a.audit(ctx, tx, act, "导出证据", "evidence", exportID, fmt.Sprintf("导出执法证据 %d 条", len(ids)), reason); err != nil {
		return nil, err
	}
	return map[string]any{
		"export":      exportRecord(exportID, now, adm.AccountID, adm.Name, f, ids, fileName),
		"csv":         buf.String(),
		"csvFileName": strings.TrimSuffix(fileName, ".zip") + ".csv",
	}, nil
}

func exportRecord(id string, at time.Time, by, byName string, f evidenceFilter, ids []string, fileName string) map[string]any {
	return map[string]any{"exportId": id, "at": fmtTime(at), "by": by, "byName": byName, "filter": f,
		"evidenceIds": ids, "count": len(ids), "fileName": fileName}
}

func (a *App) handleListExports(w http.ResponseWriter, r *http.Request) error {
	adm, err := a.perm(r, false, "evidence.view")
	if err != nil {
		return err
	}
	sql := "SELECT export_id, at, by_id, by_name, filter, evidence_ids, file_name FROM evidence_exports "
	var args []any
	if adm.Scope != "" { // 带数据范围的账号只看本人的导出
		sql += "WHERE by_id = $1 "
		args = append(args, adm.AccountID)
	}
	rows, err := a.Pool.Query(r.Context(), sql+"ORDER BY at DESC, export_id DESC", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, by, byName, file string
		var at time.Time
		var filterJSON []byte
		var ids []string
		if err := rows.Scan(&id, &at, &by, &byName, &filterJSON, &ids, &file); err != nil {
			return err
		}
		var f evidenceFilter
		_ = json.Unmarshal(filterJSON, &f)
		list = append(list, exportRecord(id, at, by, byName, f, ids, file))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

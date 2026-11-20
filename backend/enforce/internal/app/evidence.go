package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/enforce/internal/auth"
	"bike/enforce/internal/digest"
)

// 可取证的扫码结果（契约 EvidenceScanResult）
var evidenceScanResults = map[string]bool{"无政府码": true, "码已失效": true, "码未启用": true}

type captureRequest struct {
	ScanResult         string    `json:"scanResult"`
	GovCode            *string   `json:"govCode"`
	Location           []float64 `json:"location"`
	Address            string    `json:"address"`
	AccuracyM          *float64  `json:"accuracyM"`
	CapturedAt         *string   `json:"capturedAt"`
	VehicleDescription string    `json:"vehicleDescription"`
	BrandID            *string   `json:"brandId"`
	Photos             []string  `json:"photos"`
	Note               *string   `json:"note"`
}

func round6(x float64) float64 { return math.Round(x*1e6) / 1e6 }

func emptyToNil(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}

// validate 只做格式与必填校验，消息直接展示给执法人员。
func (c *captureRequest) validate() error {
	if !evidenceScanResults[c.ScanResult] {
		return errInvalid("核查结果只能是无政府码、码已失效或码未启用")
	}
	if len(c.Photos) == 0 {
		return errInvalid("请至少拍 1 张照片")
	}
	seen := map[string]bool{}
	for _, p := range c.Photos {
		if seen[p] {
			return errInvalid("同一张照片不能重复提交")
		}
		seen[p] = true
	}
	if len(c.Location) != 2 || math.IsNaN(c.Location[0]) || math.IsNaN(c.Location[1]) ||
		math.Abs(c.Location[0]) > 180 || math.Abs(c.Location[1]) > 90 {
		return errInvalid("没有取得位置，请打开定位后重试")
	}
	c.VehicleDescription = strings.TrimSpace(c.VehicleDescription)
	if c.VehicleDescription == "" {
		return errInvalid("请描述车辆（品牌字样、车身编号、颜色等）")
	}
	return nil
}

func (a *App) handleCapture(w http.ResponseWriter, r *http.Request) error {
	adm, err := a.officer(r, true)
	if err != nil {
		return err
	}
	var req captureRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := req.validate(); err != nil {
		return err
	}
	now := a.now()
	capturedAt := now
	if req.CapturedAt != nil && *req.CapturedAt != "" {
		t, err := time.Parse(time.RFC3339, *req.CapturedAt)
		if err != nil {
			return errInvalid("取证时刻格式不对")
		}
		capturedAt = t.In(shanghai()).Truncate(time.Second)
	}
	return a.runIdem(w, r, "capture:"+adm.AccountID, func(tx pgx.Tx) (int, any, error) {
		ev, err := a.capture(r.Context(), tx, adm, req, capturedAt, now)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, ev, nil
	})
}

func (a *App) capture(ctx context.Context, tx pgx.Tx, adm auth.Admin, req captureRequest, capturedAt, now time.Time) (map[string]any, error) {
	// 锁住引用的照片，校验存在、本人上传、未被其他证据引用
	rows, err := tx.Query(ctx, `SELECT p.photo_id, p.officer_id, p.sha256, EXISTS (SELECT 1 FROM evidence_photos ep WHERE ep.photo_id = p.photo_id)
		FROM photos p WHERE p.photo_id = ANY($1) FOR UPDATE`, req.Photos)
	if err != nil {
		return nil, err
	}
	type ph struct {
		owner, sha string
		used       bool
	}
	found := map[string]ph{}
	for rows.Next() {
		var id string
		var p ph
		if err := rows.Scan(&id, &p.owner, &p.sha, &p.used); err != nil {
			rows.Close()
			return nil, err
		}
		found[id] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	shas := make([]string, 0, len(req.Photos))
	for _, id := range req.Photos {
		p, ok := found[id]
		switch {
		case !ok:
			return nil, errInvalid("照片不存在或已过期，请重新拍照")
		case p.owner != adm.AccountID:
			return nil, errInvalid("只能使用本人上传的照片")
		case p.used:
			return nil, errInvalid("照片已用于其他取证记录，请重新拍照")
		}
		shas = append(shas, p.sha)
	}

	// 有码时车辆、品牌、运营商、码状态取自投影；无码时用执法人员判断的品牌
	var vehicleID, plateNo, brandID, operatorID, codeStatus *string
	govCode := emptyToNil(req.GovCode)
	if govCode != nil {
		up := strings.ToUpper(*govCode)
		govCode = &up
		if v, err := lookupVehicle(ctx, tx, up); err != nil {
			return nil, err
		} else if v != nil {
			vehicleID, plateNo, brandID, operatorID, codeStatus = &v.VehicleID, &v.PlateNo, &v.BrandID, &v.OperatorID, &v.CodeStatus
		}
	}
	if brandID == nil {
		if b := emptyToNil(req.BrandID); b != nil {
			brandID = b
			var op string
			err := tx.QueryRow(ctx, "SELECT operator_id FROM brands WHERE brand_id=$1 AND operator_id <> ''", *b).Scan(&op)
			if err == nil {
				operatorID = &op
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
		}
	}

	day := now.Format("20060102")
	seq, err := nextSeq(ctx, tx, "evidence", day)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("EV%s%03d", day, seq)
	lng, lat := round6(req.Location[0]), round6(req.Location[1])
	capText := fmtTime(capturedAt)
	sum := digest.Evidence(digest.Input{EvidenceID: id, OfficerID: adm.AccountID, CapturedAt: capText, Lng: lng, Lat: lat, ScanResult: req.ScanResult, PhotoSHA256: shas})

	if _, err := tx.Exec(ctx, `INSERT INTO evidence (evidence_id, officer_id, officer_name, badge_no, captured_at, captured_at_text, submitted_at,
			lng, lat, address, accuracy_m, scan_result, gov_code, vehicle_id, plate_no, brand_id, operator_id, code_status, vehicle_description, note, sha256)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		id, adm.AccountID, adm.Name, adm.BadgeNo, capturedAt, capText, now, lng, lat, strings.TrimSpace(req.Address), req.AccuracyM,
		req.ScanResult, govCode, vehicleID, plateNo, brandID, operatorID, codeStatus, req.VehicleDescription, emptyToNil(req.Note), sum); err != nil {
		return nil, err
	}
	for i, p := range req.Photos {
		if _, err := tx.Exec(ctx, "INSERT INTO evidence_photos (evidence_id, seq, photo_id) VALUES ($1,$2,$3)", id, i+1, p); err != nil {
			return nil, err
		}
	}
	if err := a.enqueue(ctx, tx, "enforce.evidence.submitted", id, personActor(adm.AccountID, adm.Name), map[string]any{
		"evidenceId": id, "officerId": adm.AccountID, "scanResult": req.ScanResult,
		"vehicleId": vehicleID, "brandId": brandID, "operatorId": operatorID,
		"location": []float64{lng, lat}, "capturedAt": capText, "sha256": sum,
	}); err != nil {
		return nil, err
	}
	if err := a.audit(ctx, tx, personActor(adm.AccountID, adm.Name), "提交取证记录", "evidence", id, "提交取证记录，核查结果："+req.ScanResult, nil); err != nil {
		return nil, err
	}
	return loadEvidence(ctx, tx, id)
}

// ---------- 读取 ----------

const evidenceSelect = `SELECT e.evidence_id, e.officer_id, e.officer_name, e.badge_no, e.captured_at_text, e.submitted_at, e.lng, e.lat,
	e.address, e.accuracy_m, e.scan_result, e.vehicle_id, e.plate_no, e.brand_id, b.name, e.operator_id, o.short_name,
	e.code_status, e.vehicle_description, e.note, e.sha256,
	ARRAY(SELECT ep.photo_id FROM evidence_photos ep WHERE ep.evidence_id = e.evidence_id ORDER BY ep.seq)
	FROM evidence e
	LEFT JOIN brands b ON b.brand_id = e.brand_id
	LEFT JOIN operators o ON o.operator_id = e.operator_id `

type rowScanner interface{ Scan(dest ...any) error }

func scanEvidence(s rowScanner) (map[string]any, error) {
	var (
		id, officer, officerName, badge, capText, address, scanResult, desc, sum string
		submitted                                                                time.Time
		lng, lat                                                                 float64
		accuracy                                                                 *float64
		vehicleID, plateNo, brandID, brandName, operatorID, operatorName         *string
		codeStatus, note                                                         *string
		photos                                                                   []string
	)
	if err := s.Scan(&id, &officer, &officerName, &badge, &capText, &submitted, &lng, &lat, &address, &accuracy, &scanResult,
		&vehicleID, &plateNo, &brandID, &brandName, &operatorID, &operatorName, &codeStatus, &desc, &note, &sum, &photos); err != nil {
		return nil, err
	}
	if photos == nil {
		photos = []string{}
	}
	return map[string]any{
		"evidenceId": id, "officerId": officer, "officerName": officerName, "badgeNo": badge,
		"capturedAt": capText, "submittedAt": fmtTime(submitted), "location": []float64{lng, lat},
		"address": address, "accuracyM": accuracy, "scanResult": scanResult,
		"vehicleId": vehicleID, "plateNo": plateNo, "brandId": brandID, "brandName": brandName,
		"operatorId": operatorID, "operatorName": operatorName, "codeStatus": codeStatus,
		"vehicleDescription": desc, "photos": photos, "note": note, "sha256": sum, "locked": true,
	}, nil
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadEvidence(ctx context.Context, q querier, id string) (map[string]any, error) {
	ev, err := scanEvidence(q.QueryRow(ctx, evidenceSelect+"WHERE e.evidence_id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound("证据")
	}
	return ev, err
}

func (a *App) evidenceExists(ctx context.Context, id string) (owner string, operator *string, err error) {
	err = a.Pool.QueryRow(ctx, "SELECT officer_id, operator_id FROM evidence WHERE evidence_id=$1", id).Scan(&owner, &operator)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, errNotFound("证据")
	}
	return owner, operator, err
}

func (a *App) handleListMine(w http.ResponseWriter, r *http.Request) error {
	adm, err := a.officer(r, false)
	if err != nil {
		return err
	}
	pg, err := parsePage(r)
	if err != nil {
		return err
	}
	pg.sortKey, pg.sortDir = "capturedAt", "desc"
	page, err := a.listEvidence(r.Context(), evidenceFilter{OfficerID: adm.AccountID}, "", pg)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page)
	return nil
}

func (a *App) handleGetMine(w http.ResponseWriter, r *http.Request) error {
	adm, err := a.officer(r, false)
	if err != nil {
		return err
	}
	ev, err := loadEvidence(r.Context(), a.Pool, r.PathValue("evidenceId"))
	if err != nil {
		return err
	}
	if ev["officerId"] != adm.AccountID {
		return &apiErr{http.StatusForbidden, "FORBIDDEN", "只能查看本人提交的取证记录"}
	}
	writeJSON(w, http.StatusOK, ev)
	return nil
}

// handleLockedMine 修改、删除一律拒绝；不读请求体。
func (a *App) handleLockedMine(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.officer(r, true); err != nil {
		return err
	}
	if _, _, err := a.evidenceExists(r.Context(), r.PathValue("evidenceId")); err != nil {
		return err
	}
	return errLocked
}

func (a *App) handleLockedEvidence(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.perm(r, true, "evidence.view"); err != nil {
		return err
	}
	if _, _, err := a.evidenceExists(r.Context(), r.PathValue("evidenceId")); err != nil {
		return err
	}
	return errLocked
}

func (a *App) handleListEvidence(w http.ResponseWriter, r *http.Request) error {
	adm, err := a.perm(r, false, "evidence.view")
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := evidenceFilter{ScanResult: q.Get("scanResult"), BrandID: q.Get("brandId"), OperatorID: q.Get("operatorId"),
		OfficerID: q.Get("officerId"), From: q.Get("from"), To: q.Get("to"), Q: q.Get("q")}
	pg, err := parsePage(r)
	if err != nil {
		return err
	}
	page, err := a.listEvidence(r.Context(), f, adm.Scope, pg)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page)
	return nil
}

func (a *App) handleGetEvidence(w http.ResponseWriter, r *http.Request) error {
	adm, err := a.perm(r, false, "evidence.view")
	if err != nil {
		return err
	}
	ev, err := loadEvidence(r.Context(), a.Pool, r.PathValue("evidenceId"))
	if err != nil {
		return err
	}
	if adm.Scope != "" {
		if op, _ := ev["operatorId"].(*string); op == nil || *op != adm.Scope {
			return errScopeDenied()
		}
	}
	writeJSON(w, http.StatusOK, ev)
	return nil
}

// ---------- 重算摘要 ----------

func (a *App) handleVerify(w http.ResponseWriter, r *http.Request) error {
	adm, err := a.perm(r, false, "evidence.view")
	if err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("evidenceId")
	var officer, capText, scanResult, stored string
	var lng, lat float64
	var operator *string
	err = a.Pool.QueryRow(ctx, "SELECT officer_id, captured_at_text, lng, lat, scan_result, sha256, operator_id FROM evidence WHERE evidence_id=$1", id).
		Scan(&officer, &capText, &lng, &lat, &scanResult, &stored, &operator)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound("证据")
	}
	if err != nil {
		return err
	}
	if adm.Scope != "" && (operator == nil || *operator != adm.Scope) {
		return errScopeDenied()
	}
	rows, err := a.Pool.Query(ctx, "SELECT p.file_name FROM evidence_photos ep JOIN photos p USING (photo_id) WHERE ep.evidence_id=$1 ORDER BY ep.seq", id)
	if err != nil {
		return err
	}
	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			rows.Close()
			return err
		}
		files = append(files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// 照片摘要按当前保存的文件重算；文件缺失则记为空，摘要必然不符
	shas := make([]string, 0, len(files))
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(a.PhotoDir, f))
		if err != nil {
			shas = append(shas, "")
			continue
		}
		shas = append(shas, digest.Bytes(b))
	}
	actual := digest.Evidence(digest.Input{EvidenceID: id, OfficerID: officer, CapturedAt: capText, Lng: lng, Lat: lat, ScanResult: scanResult, PhotoSHA256: shas})
	writeJSON(w, http.StatusOK, map[string]any{"evidenceId": id, "ok": actual == stored, "stored": stored, "actual": actual})
	return nil
}

package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"bike/fleet/internal/apperr"
)

// querier 是连接池与事务共有的查询能力。
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

/* ---------------- 运营商 ---------------- */

const operatorSelect = `SELECT o.operator_id, o.name, o.short_name, o.type, o.access_via, o.provider_id, p.name, p.contact,
	o.credit_code, o.joined_at, o.status, o.exited_at, o.contacts, o.settlement_account,
	COALESCE((SELECT array_agg(brand_id ORDER BY brand_id) FROM brands WHERE operator_id = o.operator_id), '{}')
	FROM operators o LEFT JOIN providers p ON p.provider_id = o.provider_id`

func scanOperator(row pgx.Row) (Operator, error) {
	var o Operator
	var provID, provName *string
	var provContact, contacts, acct []byte
	var joined, exited *time.Time
	err := row.Scan(&o.OperatorID, &o.Name, &o.ShortName, &o.Type, &o.AccessVia, &provID, &provName, &provContact,
		&o.CreditCode, &joined, &o.Status, &exited, &contacts, &acct, &o.BrandIDs)
	if err != nil {
		return o, err
	}
	if provID != nil {
		o.Provider = &Provider{ProviderID: *provID, Name: deref(provName)}
		if len(provContact) > 0 && string(provContact) != "null" {
			o.Provider.Contact = &Contact{}
			_ = json.Unmarshal(provContact, o.Provider.Contact)
		}
	}
	o.JoinedAt, o.ExitedAt = fmtTimePtr(joined), fmtTimePtr(exited)
	o.Contacts = []Contact{}
	_ = json.Unmarshal(contacts, &o.Contacts)
	if len(acct) > 0 && string(acct) != "null" {
		o.SettlementAccount = &SettlementAccount{}
		_ = json.Unmarshal(acct, o.SettlementAccount)
	}
	return o, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ListOperators：scope 非空时只返回该运营商。
func (s *Service) ListOperators(ctx context.Context, scope, status string) ([]Operator, error) {
	rows, err := s.db.Query(ctx, operatorSelect+` WHERE ($1 = '' OR o.operator_id = $1) AND ($2 = '' OR o.status = $2) ORDER BY o.operator_id`, scope, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Operator{}
	for rows.Next() {
		o, err := scanOperator(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func getOperator(ctx context.Context, q querier, id string) (Operator, error) {
	o, err := scanOperator(q.QueryRow(ctx, operatorSelect+` WHERE o.operator_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return o, apperr.NotFound("运营商")
	}
	return o, err
}

func (s *Service) GetOperator(ctx context.Context, scope, id string) (Operator, error) {
	o, err := getOperator(ctx, s.db, id)
	if err != nil {
		return o, err
	}
	if scope != "" && o.OperatorID != scope {
		return Operator{}, apperr.ScopeDenied()
	}
	return o, nil
}

/* ---------------- 品牌 ---------------- */

const brandSelect = `SELECT b.brand_id, b.name, b.display_name, b.color, b.vehicle_type, b.operator_id, o.short_name,
	b.access_status, b.status_since,
	(SELECT count(*) FROM vehicles v WHERE v.brand_id = b.brand_id AND v.removed_at IS NULL),
	COALESCE(b.provider_id, o.provider_id), COALESCE(b.plate_prefix, '')
	FROM brands b JOIN operators o ON o.operator_id = b.operator_id`

func scanBrand(row pgx.Row) (Brand, error) {
	var b Brand
	var since *time.Time
	err := row.Scan(&b.BrandID, &b.Name, &b.DisplayName, &b.Color, &b.VehicleType, &b.OperatorID, &b.OperatorName,
		&b.AccessStatus, &since, &b.VehicleCount, &b.ProviderID, &b.PlatePrefix)
	b.StatusSince = fmtTimePtr(since)
	return b, err
}

func (s *Service) ListBrands(ctx context.Context, scope, operatorID, accessStatus string) ([]Brand, error) {
	return listBrands(ctx, s.db, scope, operatorID, accessStatus)
}

func listBrands(ctx context.Context, q querier, scope, operatorID, accessStatus string) ([]Brand, error) {
	rows, err := q.Query(ctx, brandSelect+` WHERE ($1 = '' OR b.operator_id = $1) AND ($2 = '' OR b.operator_id = $2)
		AND ($3 = '' OR b.access_status = $3) ORDER BY b.brand_id`, scope, operatorID, accessStatus)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Brand{}
	for rows.Next() {
		b, err := scanBrand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func getBrand(ctx context.Context, q querier, id string) (Brand, error) {
	b, err := scanBrand(q.QueryRow(ctx, brandSelect+` WHERE b.brand_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, apperr.NotFound("品牌")
	}
	return b, err
}

func (s *Service) GetBrandDetail(ctx context.Context, scope, id string) (BrandDetail, error) {
	b, err := getBrand(ctx, s.db, id)
	if err != nil {
		return BrandDetail{}, err
	}
	if scope != "" && b.OperatorID != scope {
		return BrandDetail{}, apperr.ScopeDenied()
	}
	return brandDetail(ctx, s.db, b)
}

func brandDetail(ctx context.Context, q querier, b Brand) (BrandDetail, error) {
	d := BrandDetail{Brand: b}
	var err error
	if d.StatusHistory, err = statusHistory(ctx, q, b.BrandID); err != nil {
		return d, err
	}
	d.Pause, err = currentPause(ctx, q, b.BrandID)
	return d, err
}

func statusHistory(ctx context.Context, q querier, brandID string) ([]StatusChange, error) {
	rows, err := q.Query(ctx, `SELECT status, at, actor, note FROM brand_status_history WHERE brand_id = $1 ORDER BY at, id`, brandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatusChange{}
	for rows.Next() {
		var c StatusChange
		var at time.Time
		var actor []byte
		if err := rows.Scan(&c.Status, &at, &actor, &c.Note); err != nil {
			return nil, err
		}
		c.At = fmtTime(at)
		c.By = parseActor(actor)
		out = append(out, c)
	}
	return out, rows.Err()
}

func parseActor(b []byte) *Actor {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	a := &Actor{}
	if json.Unmarshal(b, a) != nil {
		return nil
	}
	return a
}

const pauseSelect = `SELECT pause_id, brand_id, paused_at, resumed_at, metric, value, threshold, sustain_minutes,
	by_actor, resumed_by, note, resume_note FROM pauses`

func scanPause(row pgx.Row) (*Pause, error) {
	p := &Pause{}
	var paused time.Time
	var resumed *time.Time
	var by, rby []byte
	err := row.Scan(&p.PauseID, &p.BrandID, &paused, &resumed, &p.Metric, &p.Value, &p.Threshold, &p.SustainMinutes,
		&by, &rby, &p.Note, &p.ResumeNote)
	if err != nil {
		return nil, err
	}
	p.PausedAt, p.ResumedAt = fmtTime(paused), fmtTimePtr(resumed)
	p.By, p.ResumedBy = parseActor(by), parseActor(rby)
	return p, nil
}

// currentPause 返回当前未恢复的暂停记录；没有为 nil。
func currentPause(ctx context.Context, q querier, brandID string) (*Pause, error) {
	p, err := scanPause(q.QueryRow(ctx, pauseSelect+` WHERE brand_id = $1 AND resumed_at IS NULL ORDER BY paused_at DESC LIMIT 1`, brandID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

/* ---------------- 车辆 ---------------- */

// 车辆当前状态：码失效（骑行中除外）显示码已失效，码待启用显示码未启用，其余为车端运行状态。
const displayStatusExpr = `(CASE WHEN v.code_status = '已失效' AND v.run_status <> '骑行中' THEN '码已失效'
	WHEN v.code_status = '待启用' THEN '码未启用' ELSE v.run_status END)`

// 可租：品牌已接入、码已启用且车辆状态为可用。
const rentableExpr = `(b.access_status = '已接入' AND v.code_status = '已启用' AND v.run_status = '可用')`

const vehicleSelect = `SELECT v.vehicle_id, v.brand_id, b.name, v.operator_id, o.short_name, v.plate_no, v.gov_code,
	v.pile_tag, v.pile_tag_type, v.vehicle_type, v.model, v.listed_at, v.code_issued_at, v.code_status,
	v.code_enable_at, v.code_invalid_at, ` + displayStatusExpr + `, ` + rentableExpr + `
	FROM vehicles v JOIN brands b ON b.brand_id = v.brand_id JOIN operators o ON o.operator_id = v.operator_id`

func scanVehicle(row pgx.Row) (Vehicle, error) {
	var v Vehicle
	var listed, issued time.Time
	var enable, invalid *time.Time
	err := row.Scan(&v.VehicleID, &v.BrandID, &v.BrandName, &v.OperatorID, &v.OperatorName, &v.PlateNo, &v.GovCode,
		&v.PileTag, &v.PileTagType, &v.VehicleType, &v.Model, &listed, &issued, &v.CodeStatus,
		&enable, &invalid, &v.Status, &v.Rentable)
	if err != nil {
		return v, err
	}
	v.ListedAt, v.CodeIssuedAt = fmtTime(listed), fmtTime(issued)
	v.CodeEnableAt, v.CodeInvalidAt = fmtTimePtr(enable), fmtTimePtr(invalid)
	return v, nil
}

func getVehicle(ctx context.Context, q querier, where string, arg any) (Vehicle, error) {
	return scanVehicle(q.QueryRow(ctx, vehicleSelect+` WHERE `+where, arg))
}

func (s *Service) GetVehicle(ctx context.Context, scope, id string) (Vehicle, error) {
	v, err := getVehicle(ctx, s.db, `v.vehicle_id = $1`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, apperr.NotFound("车辆")
	}
	if err != nil {
		return v, err
	}
	if scope != "" && v.OperatorID != scope {
		return Vehicle{}, apperr.ScopeDenied()
	}
	return v, nil
}

func (s *Service) GetVehicleByGovCode(ctx context.Context, scope, code string) (Vehicle, error) {
	v, err := getVehicle(ctx, s.db, `v.gov_code = $1`, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, codeNotFound()
	}
	if err != nil {
		return v, err
	}
	if scope != "" && v.OperatorID != scope {
		return Vehicle{}, apperr.ScopeDenied()
	}
	return v, nil
}

func codeNotFound() *apperr.Error {
	return apperr.New(404, "CODE_NOT_FOUND", "没有找到这个政府码")
}

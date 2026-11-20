package app

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// 以下结构对应 contracts/openapi/onboard.yaml 的 schema。

type CredentialView struct {
	BrandID          string     `json:"brandId,omitempty"`
	SandboxAppID     string     `json:"sandboxAppId"`
	SandboxKeyMasked string     `json:"sandboxKeyMasked"`
	IssuedAt         time.Time  `json:"issuedAt"`
	Status           string     `json:"status"`
	DisabledAt       *time.Time `json:"disabledAt"`
	DisabledBy       *string    `json:"disabledBy"`
	RotatedAt        *time.Time `json:"rotatedAt"`
	RotatedBy        *string    `json:"rotatedBy"`
}

type StatusHistoryEntry struct {
	Status string    `json:"status"`
	At     time.Time `json:"at"`
	By     string    `json:"by"`
	Note   *string   `json:"note"`
}

type VehicleListSummary struct {
	SubmittedAt  *time.Time `json:"submittedAt"`
	VehicleCount int        `json:"vehicleCount"`
	CodesIssued  int        `json:"codesIssued"`
	CodeStatus   *string    `json:"codeStatus"`
	SubmittedBy  *string    `json:"submittedBy"`
}

type AcceptanceView struct {
	ReportID             *string    `json:"reportId"`
	RealVehiclesRequired int        `json:"realVehiclesRequired"`
	RealVehiclesRidden   int        `json:"realVehiclesRidden"`
	RealOrders           int        `json:"realOrders"`
	MatchedOrders        int        `json:"matchedOrders"`
	LockReportsMatched   int        `json:"lockReportsMatched"`
	TimeLimitOk          bool       `json:"timeLimitOk"`
	ConformanceRunID     *string    `json:"conformanceRunId"`
	SubmittedAt          *time.Time `json:"submittedAt"`
	SubmittedBy          *string    `json:"submittedBy"`
	Result               string     `json:"result"`
	ConfirmedBy          *string    `json:"confirmedBy"`
	ConfirmedAt          *time.Time `json:"confirmedAt"`
	Summary              *string    `json:"summary"`
}

type Integration struct {
	IntegrationID   string               `json:"integrationId"`
	BrandID         string               `json:"brandId"`
	OperatorID      string               `json:"operatorId"`
	AccessVia       string               `json:"accessVia"`
	ProviderID      *string              `json:"providerId"`
	Status          string               `json:"status"`
	StatusSince     time.Time            `json:"statusSince"`
	StatusHistory   []StatusHistoryEntry `json:"statusHistory"`
	Credentials     CredentialView       `json:"credentials"`
	OperatorBaseURL *string              `json:"operatorBaseUrl"`
	VehicleList     *VehicleListSummary  `json:"vehicleList"`
	RunIDs          []string             `json:"runIds"`
	Acceptance      *AcceptanceView      `json:"acceptance"`
	TargetDays      int                  `json:"targetDays"`
	StartedAt       *time.Time           `json:"startedAt"`
}

type IntegrationView struct {
	Integration
	LatestRun *Run `json:"latestRun"`
}

type AcceptanceRow struct {
	IntegrationView
	ElapsedDays *int `json:"elapsedDays"`
}

type Case struct {
	CaseID      string  `json:"caseId"`
	Group       string  `json:"group"`
	Name        string  `json:"name"`
	Result      string  `json:"result"`
	DurationMs  int     `json:"durationMs"`
	FailReason  *string `json:"failReason,omitempty"`
	Suggestion  *string `json:"suggestion,omitempty"`
	SuggestedBy *string `json:"suggestedBy,omitempty"`
}

type RunSummary struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

type Run struct {
	RunID         string     `json:"runId"`
	IntegrationID string     `json:"integrationId"`
	BrandID       string     `json:"brandId"`
	At            time.Time  `json:"at"`
	Env           string     `json:"env"`
	Summary       RunSummary `json:"summary"`
	Cases         []Case     `json:"cases"`
}

// integRow 接入记录的本地字段。
type integRow struct {
	IntegrationID, BrandID, OperatorID, AccessVia, Status string
	ProviderID                                            *string
	StatusSince                                           time.Time
	StartedAt                                             *time.Time
	OperatorBaseURL                                       *string
	PendingStart                                          bool
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const integCols = `integration_id, brand_id, operator_id, access_via, provider_id, status, status_since, started_at, operator_base_url, pending_start`

func scanInteg(row pgx.Row) (*integRow, error) {
	var r integRow
	err := row.Scan(&r.IntegrationID, &r.BrandID, &r.OperatorID, &r.AccessVia, &r.ProviderID, &r.Status, &r.StatusSince, &r.StartedAt, &r.OperatorBaseURL, &r.PendingStart)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (a *App) integByBrand(ctx context.Context, q querier, brandID string) (*integRow, error) {
	r, err := scanInteg(q.QueryRow(ctx, `SELECT `+integCols+` FROM integrations WHERE brand_id=$1`, brandID))
	if isNoRows(err) {
		return nil, errf(404, "NOT_FOUND", "没有找到该品牌的接入记录")
	}
	return r, err
}

func (a *App) integByID(ctx context.Context, q querier, id string) (*integRow, error) {
	r, err := scanInteg(q.QueryRow(ctx, `SELECT `+integCols+` FROM integrations WHERE integration_id=$1`, id))
	if isNoRows(err) {
		return nil, errf(404, "NOT_FOUND", "没有找到该接入记录")
	}
	return r, err
}

// credRow 凭证（含密钥原文，只在服务内部用）。
type credRow struct {
	BrandID, AppID, Secret, Status string
	IssuedAt                       time.Time
	DisabledAt, RotatedAt          *time.Time
	DisabledBy, RotatedBy          *string
	Delivered                      bool
}

const credCols = `brand_id, app_id, secret, status, issued_at, disabled_at, rotated_at, disabled_by, rotated_by, delivered`

func scanCred(row pgx.Row) (*credRow, error) {
	var c credRow
	err := row.Scan(&c.BrandID, &c.AppID, &c.Secret, &c.Status, &c.IssuedAt, &c.DisabledAt, &c.RotatedAt, &c.DisabledBy, &c.RotatedBy, &c.Delivered)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *credRow) view() CredentialView {
	return CredentialView{SandboxAppID: c.AppID, SandboxKeyMasked: maskKey(c.Secret), IssuedAt: c.IssuedAt, Status: c.Status,
		DisabledAt: c.DisabledAt, DisabledBy: c.DisabledBy, RotatedAt: c.RotatedAt, RotatedBy: c.RotatedBy}
}

func (a *App) credByBrand(ctx context.Context, q querier, brandID string) (*credRow, error) {
	c, err := scanCred(q.QueryRow(ctx, `SELECT `+credCols+` FROM credentials WHERE brand_id=$1`, brandID))
	if isNoRows(err) {
		return nil, errf(404, "NOT_FOUND", "该品牌还没有联调凭证")
	}
	return c, err
}

// buildIntegration 组装一条接入记录。
func (a *App) buildIntegration(ctx context.Context, r *integRow) (*Integration, error) {
	p, err := a.params(ctx)
	if err != nil {
		return nil, err
	}
	out := &Integration{IntegrationID: r.IntegrationID, BrandID: r.BrandID, OperatorID: r.OperatorID, AccessVia: r.AccessVia,
		ProviderID: r.ProviderID, OperatorBaseURL: r.OperatorBaseURL, Status: r.Status, StatusSince: r.StatusSince, StartedAt: r.StartedAt, TargetDays: p.TargetDays,
		StatusHistory: []StatusHistoryEntry{}, RunIDs: []string{}}
	rows, err := a.DB.Query(ctx, `SELECT status, at, by_id, note FROM status_history WHERE integration_id=$1 ORDER BY id`, r.IntegrationID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h StatusHistoryEntry
		if err := rows.Scan(&h.Status, &h.At, &h.By, &h.Note); err != nil {
			rows.Close()
			return nil, err
		}
		out.StatusHistory = append(out.StatusHistory, h)
	}
	rows.Close()
	if c, err := a.credByBrand(ctx, a.DB, r.BrandID); err == nil {
		out.Credentials = c.view()
	}
	out.VehicleList, err = a.vehicleSummary(ctx, r.BrandID)
	if err != nil {
		return nil, err
	}
	rows, err = a.DB.Query(ctx, `SELECT run_id FROM runs WHERE integration_id=$1 ORDER BY at, run_id`, r.IntegrationID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		out.RunIDs = append(out.RunIDs, id)
	}
	rows.Close()
	out.Acceptance, err = a.acceptanceView(ctx, r, p)
	return out, err
}

func (a *App) vehicleSummary(ctx context.Context, brandID string) (*VehicleListSummary, error) {
	var n, issued, enabled, pending, invalid int
	var last *time.Time
	var nextEnable *time.Time
	err := a.DB.QueryRow(ctx, `SELECT count(*), count(gov_code), count(*) FILTER (WHERE code_status='已启用'),
		count(*) FILTER (WHERE code_status='待启用'), count(*) FILTER (WHERE code_status='已失效'), max(listed_at),
		min(code_enable_at) FILTER (WHERE code_status='待启用')
		FROM fleet_vehicles WHERE brand_id=$1`, brandID).Scan(&n, &issued, &enabled, &pending, &invalid, &last, &nextEnable)
	if err != nil || n == 0 {
		return nil, err
	}
	s := &VehicleListSummary{SubmittedAt: last, VehicleCount: n, CodesIssued: issued}
	var by *string
	_ = a.DB.QueryRow(ctx, `SELECT listed_by FROM fleet_vehicles WHERE brand_id=$1 ORDER BY listed_at DESC LIMIT 1`, brandID).Scan(&by)
	s.SubmittedBy = by
	var txt string
	switch {
	case pending == n && nextEnable != nil:
		txt = fmt.Sprintf("待启用（计划 %s 随上线启用）", nextEnable.Format("2006-01-02 15:04"))
	case pending == n:
		txt = "待启用（随上线启用）"
	case invalid == n:
		txt = "已失效"
	case enabled == n:
		txt = "已启用"
	default:
		txt = fmt.Sprintf("已启用 %d 辆，待启用 %d 辆，其他 %d 辆", enabled, pending, n-enabled-pending)
	}
	s.CodeStatus = &txt
	return s, nil
}

// latestRun 最新一轮测试。
func (a *App) latestRun(ctx context.Context, integrationID string) (*Run, error) {
	var id string
	err := a.DB.QueryRow(ctx, `SELECT run_id FROM runs WHERE integration_id=$1 ORDER BY at DESC, run_id DESC LIMIT 1`, integrationID).Scan(&id)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a.loadRun(ctx, id)
}

func (a *App) loadRun(ctx context.Context, runID string) (*Run, error) {
	r := &Run{Env: "联调环境", Cases: []Case{}}
	err := a.DB.QueryRow(ctx, `SELECT run_id, integration_id, brand_id, at, total, passed, failed FROM runs WHERE run_id=$1`, runID).
		Scan(&r.RunID, &r.IntegrationID, &r.BrandID, &r.At, &r.Summary.Total, &r.Summary.Passed, &r.Summary.Failed)
	if isNoRows(err) {
		return nil, errf(404, "NOT_FOUND", "没有找到该测试轮次")
	}
	if err != nil {
		return nil, err
	}
	rows, err := a.DB.Query(ctx, `SELECT case_id, grp, name, result, duration_ms, fail_reason, suggestion, suggested_by FROM run_cases WHERE run_id=$1 ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Case
		if err := rows.Scan(&c.CaseID, &c.Group, &c.Name, &c.Result, &c.DurationMs, &c.FailReason, &c.Suggestion, &c.SuggestedBy); err != nil {
			return nil, err
		}
		r.Cases = append(r.Cases, c)
	}
	return r, rows.Err()
}

func (a *App) integrationView(ctx context.Context, r *integRow) (*IntegrationView, error) {
	in, err := a.buildIntegration(ctx, r)
	if err != nil {
		return nil, err
	}
	lr, err := a.latestRun(ctx, r.IntegrationID)
	if err != nil {
		return nil, err
	}
	return &IntegrationView{Integration: *in, LatestRun: lr}, nil
}

// listIntegrations scope 为空时返回全部。
func (a *App) listIntegrations(ctx context.Context, scope string) ([]*integRow, error) {
	rows, err := a.DB.Query(ctx, `SELECT `+integCols+` FROM integrations WHERE ($1='' OR operator_id=$1) ORDER BY brand_id`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*integRow
	for rows.Next() {
		r, err := scanInteg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func elapsedDays(start *time.Time, now time.Time) *int {
	if start == nil {
		return nil
	}
	d := int(math.Floor(now.Sub(*start).Hours() / 24))
	return &d
}

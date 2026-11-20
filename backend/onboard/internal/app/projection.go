package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// applyEvent 处理订阅到的事件，更新本模块的投影。返回的函数在事务提交后执行。
func (a *App) applyEvent(ctx context.Context, tx pgx.Tx, env Envelope) (func(), error) {
	switch env.Type {
	case "fleet.brand.registered":
		return nil, a.onBrandRegistered(ctx, tx, env)
	case "fleet.brand.access_changed":
		return nil, a.onAccessChanged(ctx, tx, env)
	case "fleet.vehicle.listed":
		return nil, a.onVehicleListed(ctx, tx, env)
	case "fleet.vehicle.removed":
		var p struct {
			VehicleID string `json:"vehicleId"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, nil
		}
		_, err := tx.Exec(ctx, `DELETE FROM fleet_vehicles WHERE vehicle_id=$1`, p.VehicleID)
		return nil, err
	case "fleet.code.status_changed":
		return nil, a.onCodeStatusChanged(ctx, tx, env)
	case "fleet.operator.exited":
		return nil, a.onOperatorExited(ctx, tx, env)
	case "rule.params.changed":
		return nil, a.onParamsChanged(ctx, tx, env)
	}
	return nil, nil
}

func (a *App) onBrandRegistered(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		BrandID      string `json:"brandId"`
		OperatorID   string `json:"operatorId"`
		Name         string `json:"name"`
		AccessStatus string `json:"accessStatus"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.BrandID == "" {
		return nil
	}
	if p.AccessStatus == "" {
		p.AccessStatus = "未接入"
	}
	integ := "IN-" + p.BrandID
	tag, err := tx.Exec(ctx, `INSERT INTO integrations (integration_id, brand_id, operator_id, brand_name, status, status_since, started_at)
		VALUES ($1,$2,$3,$4,$5,$6,$6) ON CONFLICT (brand_id) DO NOTHING`, integ, p.BrandID, p.OperatorID, p.Name, p.AccessStatus, env.OccurredAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// 基本信息变更：只更新名称与运营商
		_, err = tx.Exec(ctx, `UPDATE integrations SET operator_id=$2, brand_name=$3 WHERE brand_id=$1`, p.BrandID, p.OperatorID, p.Name)
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO status_history (integration_id, status, at, by_id, note) VALUES ($1,$2,$3,$4,$5)`,
		integ, p.AccessStatus, env.OccurredAt, env.Actor.ID, "品牌登记"); err != nil {
		return err
	}
	// 联调凭证随接入记录建立；密钥第一次交到运营商手里（首次轮换）时才算发放，见 Rotate
	appID, secret := newCredential(p.BrandID)
	_, err = tx.Exec(ctx, `INSERT INTO credentials (brand_id, app_id, secret, issued_at) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
		p.BrandID, appID, secret, env.OccurredAt)
	return err
}

func (a *App) onAccessChanged(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		BrandID   string    `json:"brandId"`
		From      string    `json:"from"`
		To        string    `json:"to"`
		ChangedAt time.Time `json:"changedAt"`
		Reason    *string   `json:"reason"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.BrandID == "" || p.To == "" {
		return nil
	}
	if p.ChangedAt.IsZero() {
		p.ChangedAt = env.OccurredAt
	}
	return a.setStatus(ctx, tx, p.BrandID, p.To, p.ChangedAt, env.Actor.ID, p.Reason)
}

// setStatus 更新接入状态投影；与最后一条记录相同则不重复记。
func (a *App) setStatus(ctx context.Context, tx pgx.Tx, brandID, status string, at time.Time, by string, note *string) error {
	var integ, cur string
	err := tx.QueryRow(ctx, `SELECT integration_id, status FROM integrations WHERE brand_id=$1 FOR UPDATE`, brandID).Scan(&integ, &cur)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur == status {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE integrations SET status=$2, status_since=$3 WHERE brand_id=$1`, brandID, status, at); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO status_history (integration_id, status, at, by_id, note) VALUES ($1,$2,$3,$4,$5)`, integ, status, at, by, note)
	return err
}

func (a *App) onVehicleListed(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		VehicleID    string     `json:"vehicleId"`
		BrandID      string     `json:"brandId"`
		OperatorID   string     `json:"operatorId"`
		PlateNo      string     `json:"plateNo"`
		GovCode      *string    `json:"govCode"`
		Model        *string    `json:"model"`
		PileTagType  *string    `json:"pileTagType"`
		PileTag      *string    `json:"pileTag"`
		CodeStatus   *string    `json:"codeStatus"`
		CodeEnableAt *time.Time `json:"codeEnableAt"`
		ListedAt     time.Time  `json:"listedAt"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.VehicleID == "" {
		return nil
	}
	if p.ListedAt.IsZero() {
		p.ListedAt = env.OccurredAt
	}
	_, err := tx.Exec(ctx, `INSERT INTO fleet_vehicles (vehicle_id, brand_id, operator_id, plate_no, gov_code, model, pile_tag_type, pile_tag, code_status, code_enable_at, listed_at, listed_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (vehicle_id) DO UPDATE SET brand_id=$2, operator_id=$3, plate_no=$4, gov_code=$5, model=$6, pile_tag_type=$7, pile_tag=$8,
		code_status=CASE WHEN fleet_vehicles.code_status_at IS NOT NULL AND fleet_vehicles.code_status_at > $11 THEN fleet_vehicles.code_status ELSE $9 END,
		code_enable_at=$10, listed_at=$11, listed_by=$12`,
		p.VehicleID, p.BrandID, p.OperatorID, p.PlateNo, p.GovCode, p.Model, p.PileTagType, p.PileTag, p.CodeStatus, p.CodeEnableAt, p.ListedAt, env.Actor.ID)
	return err
}

// onCodeStatusChanged 刷新车辆的码状态投影。只认 待启用｜已启用｜已失效；未知车辆忽略（清单事件里自带码状态）；
// 比已记录的变更时刻更早的事件（迟到）忽略。
func (a *App) onCodeStatusChanged(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		VehicleID string    `json:"vehicleId"`
		To        string    `json:"to"`
		ChangedAt time.Time `json:"changedAt"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.VehicleID == "" {
		return nil
	}
	switch p.To {
	case "待启用", "已启用", "已失效":
	default:
		return nil
	}
	if p.ChangedAt.IsZero() {
		p.ChangedAt = env.OccurredAt
	}
	_, err := tx.Exec(ctx, `UPDATE fleet_vehicles SET code_status=$2, code_status_at=$3
		WHERE vehicle_id=$1 AND (code_status_at IS NULL OR code_status_at <= $3)`, p.VehicleID, p.To, p.ChangedAt)
	return err
}

// operatorExited 运营商是否已退出（订阅 fleet.operator.exited 得到）。查询失败按未退出处理，不挡住读写。
func (a *App) operatorExited(ctx context.Context, operatorID string) bool {
	var n int
	if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM exited_operators WHERE operator_id=$1`, operatorID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

func (a *App) onOperatorExited(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		OperatorID string    `json:"operatorId"`
		BrandIDs   []string  `json:"brandIds"`
		ExitedAt   time.Time `json:"exitedAt"`
		Reason     *string   `json:"reason"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.OperatorID == "" {
		return nil
	}
	if p.ExitedAt.IsZero() {
		p.ExitedAt = env.OccurredAt
	}
	if _, err := tx.Exec(ctx, `INSERT INTO exited_operators (operator_id, exited_at, reason) VALUES ($1,$2,$3)
		ON CONFLICT (operator_id) DO NOTHING`, p.OperatorID, p.ExitedAt, p.Reason); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT c.brand_id, c.app_id FROM credentials c JOIN integrations i ON i.brand_id=c.brand_id
		WHERE c.status='有效' AND (i.operator_id=$1 OR c.brand_id = ANY($2))`, p.OperatorID, p.BrandIDs)
	if err != nil {
		return err
	}
	type cr struct{ brand, app string }
	var list []cr
	for rows.Next() {
		var x cr
		if err := rows.Scan(&x.brand, &x.app); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	sys := Actor{Kind: "系统", ID: "onboard", Name: "接入验收服务"}
	for _, c := range list {
		if _, err := tx.Exec(ctx, `UPDATE credentials SET status='停用', disabled_at=$2, disabled_by=$3 WHERE brand_id=$1`, c.brand, p.ExitedAt, env.Actor.ID); err != nil {
			return err
		}
		if err := a.audit(ctx, tx, sys, "吊销联调凭证", "credential", c.app, fmt.Sprintf("运营商 %s 已退出，品牌 %s 的联调凭证随之停用", p.OperatorID, c.brand),
			p.Reason, map[string]any{"status": "有效"}, map[string]any{"status": "停用"}); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) onParamsChanged(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var p struct {
		BackfillWindowHours    *int `json:"backfillWindowHours"`
		UnlockReportTimeoutSec *int `json:"unlockReportTimeoutSec"`
		Acceptance             *struct {
			RealVehicles *int `json:"realVehicles"`
			RealOrders   *int `json:"realOrders"`
			TargetDays   *int `json:"targetDays"`
		} `json:"acceptance"`
		ChangedAt *time.Time `json:"changedAt"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil
	}
	var rv, ro, td *int
	if p.Acceptance != nil {
		rv, ro, td = p.Acceptance.RealVehicles, p.Acceptance.RealOrders, p.Acceptance.TargetDays
	}
	_, err := tx.Exec(ctx, `UPDATE params SET real_vehicles=COALESCE($1, real_vehicles), real_orders=COALESCE($2, real_orders),
		target_days=COALESCE($3, target_days), unlock_timeout_sec=COALESCE($4, unlock_timeout_sec),
		backfill_window_hours=COALESCE($5, backfill_window_hours), changed_at=COALESCE($6, changed_at) WHERE id=1`,
		rv, ro, td, p.UnlockReportTimeoutSec, p.BackfillWindowHours, p.ChangedAt)
	return err
}

// Params 平台参数投影。
type Params struct {
	RealVehicles        int
	RealOrders          int
	TargetDays          int
	UnlockTimeout       time.Duration
	BackfillWindowHours int
}

func (a *App) params(ctx context.Context) (Params, error) {
	var p Params
	var sec int
	err := a.DB.QueryRow(ctx, `SELECT real_vehicles, real_orders, target_days, unlock_timeout_sec, backfill_window_hours FROM params WHERE id=1`).
		Scan(&p.RealVehicles, &p.RealOrders, &p.TargetDays, &sec, &p.BackfillWindowHours)
	p.UnlockTimeout = time.Duration(sec) * time.Second
	return p, err
}

// newCredential 生成联调应用编号与密钥。
func newCredential(brandID string) (appID, secret string) {
	n, _ := rand.Int(rand.Reader, big.NewInt(900000))
	b := make([]byte, 14)
	_, _ = rand.Read(b)
	low := strings.ToLower(brandID)
	return fmt.Sprintf("sbx_%s_%06d", low, n.Int64()+100000), fmt.Sprintf("sk_%s_%s", low, hex.EncodeToString(b))
}

func maskKey(secret string) string {
	if len(secret) < 4 {
		return "sk_****"
	}
	return "sk_****" + secret[len(secret)-4:]
}

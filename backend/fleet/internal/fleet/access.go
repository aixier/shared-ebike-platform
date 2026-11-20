package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/fleet/internal/apperr"
)

// AccessStatuses 是五种接入状态，按总览的展示顺序。
var AccessStatuses = []string{"未接入", "联调中", "已接入", "暂停可租", "已退出"}

// canTransit 是接入状态机：未接入→联调中→已接入⇄暂停可租；任一状态→已退出。
func canTransit(from, to string) bool {
	switch {
	case from == to:
		return false
	case to == "已退出":
		return from != "已退出"
	case from == "未接入":
		return to == "联调中"
	case from == "联调中":
		return to == "已接入"
	case from == "已接入":
		return to == "暂停可租"
	case from == "暂停可租":
		return to == "已接入"
	}
	return false
}

// lockBrand 在事务里锁住品牌行并返回其当前接入状态与运营商。
func lockBrand(ctx context.Context, tx pgx.Tx, brandID string) (status, operatorID string, err error) {
	err = tx.QueryRow(ctx, `SELECT access_status, operator_id FROM brands WHERE brand_id = $1 FOR UPDATE`, brandID).Scan(&status, &operatorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", apperr.NotFound("品牌")
	}
	return status, operatorID, err
}

// transit 迁移接入状态：改品牌、记历史、发状态变更事件。调用前须已 lockBrand。
func (s *Service) transit(ctx context.Context, tx pgx.Tx, brandID, operatorID, from, to string, actor Actor, at time.Time, reason *string) error {
	if !canTransit(from, to) {
		return apperr.Conflict(fmt.Sprintf("品牌当前为%s，不能变为%s", from, to))
	}
	if _, err := tx.Exec(ctx, `UPDATE brands SET access_status = $2, status_since = $3 WHERE brand_id = $1`, brandID, to, at); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO brand_status_history (brand_id, status, at, actor, note) VALUES ($1, $2, $3, $4, $5)`,
		brandID, to, at, mustJSON(actor), reason); err != nil {
		return err
	}
	return s.emit(ctx, tx, "fleet.brand.access_changed", brandID, actor, at, map[string]any{
		"brandId": brandID, "operatorId": operatorID, "from": from, "to": to, "changedAt": fmtTime(at), "reason": reason,
	})
}

func validActor(a *Actor) error {
	if !a.valid() {
		return apperr.Invalid("操作人 actor 缺失或格式不对（kind 取 人、智能体、系统、用户，id 必填）")
	}
	return nil
}

/* ---------------- 内部命令 ---------------- */

type StartIntegrationResult struct {
	BrandID      string `json:"brandId"`
	AccessStatus string `json:"accessStatus"`
	StatusSince  string `json:"statusSince"`
}

// StartIntegration：未接入 → 联调中；已在联调中、已接入、暂停可租直接返回当前状态；已退出不能再联调。
func (s *Service) StartIntegration(ctx context.Context, brandID string, actor *Actor) (StartIntegrationResult, error) {
	var out StartIntegrationResult
	if err := validActor(actor); err != nil {
		return out, err
	}
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		status, opID, err := lockBrand(ctx, tx, brandID)
		if err != nil {
			return err
		}
		switch status {
		case "已退出":
			return apperr.Conflict("品牌已退出，不能开始联调")
		case "未接入":
			note := "发放联调环境凭证"
			if err := s.transit(ctx, tx, brandID, opID, status, "联调中", *actor, now, &note); err != nil {
				return err
			}
			if err := s.audit(ctx, tx, *actor, now, "开始联调", "brand", brandID, "品牌开始联调", nil,
				map[string]any{"accessStatus": status}, map[string]any{"accessStatus": "联调中"}); err != nil {
				return err
			}
		}
		var since *time.Time
		out.BrandID = brandID
		if err := tx.QueryRow(ctx, `SELECT access_status, status_since FROM brands WHERE brand_id = $1`, brandID).Scan(&out.AccessStatus, &since); err != nil {
			return err
		}
		if since == nil {
			since = &now
		}
		out.StatusSince = fmtTime(*since)
		return nil
	})
	return out, err
}

type GoLiveResult struct {
	Brand        Brand `json:"brand"`
	EnabledCodes int   `json:"enabledCodes"`
}

// GoLive：联调中 → 已接入，该品牌待启用的码全部启用。
func (s *Service) GoLive(ctx context.Context, brandID string, actor *Actor, reason string) (GoLiveResult, error) {
	var out GoLiveResult
	if err := validActor(actor); err != nil {
		return out, err
	}
	if !reasonOK(reason) {
		return out, apperr.ReasonRequired()
	}
	reason = strings.TrimSpace(reason)
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		status, opID, err := lockBrand(ctx, tx, brandID)
		if err != nil {
			return err
		}
		if status == "已接入" {
			return apperr.AlreadyDone("品牌已是已接入")
		}
		if status != "联调中" {
			return apperr.Conflict("品牌当前为" + status + "，不在联调中，不能上线")
		}
		if err := s.transit(ctx, tx, brandID, opID, status, "已接入", *actor, now, &reason); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE vehicles SET code_status = '已启用', code_enable_at = NULL
			WHERE brand_id = $1 AND code_status = '待启用' RETURNING vehicle_id, gov_code`, brandID)
		if err != nil {
			return err
		}
		type vc struct{ id, code string }
		var enabled []vc
		for rows.Next() {
			var x vc
			if err := rows.Scan(&x.id, &x.code); err != nil {
				rows.Close()
				return err
			}
			enabled = append(enabled, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, x := range enabled {
			if err := s.emitCodeChanged(ctx, tx, *actor, now, x.id, brandID, x.code, "待启用", "已启用"); err != nil {
				return err
			}
		}
		out.EnabledCodes = len(enabled)
		summary := fmt.Sprintf("品牌上线：联调中 → 已接入，启用政府码 %d 个", len(enabled))
		if err := s.audit(ctx, tx, *actor, now, "上线品牌", "brand", brandID, summary, &reason,
			map[string]any{"accessStatus": status}, map[string]any{"accessStatus": "已接入"}); err != nil {
			return err
		}
		out.Brand, err = getBrand(ctx, tx, brandID)
		return err
	})
	return out, err
}

type PauseCommand struct {
	Actor          *Actor   `json:"actor"`
	Metric         string   `json:"metric"`
	Value          *float64 `json:"value"`
	Threshold      *float64 `json:"threshold"`
	SustainMinutes *int     `json:"sustainMinutes"`
	Reason         string   `json:"reason"`
}

type PauseResult struct {
	Brand Brand  `json:"brand"`
	Pause *Pause `json:"pause"`
}

// Pause：已接入 → 暂停可租，并记一条暂停可租记录。该不该暂停由 ops 判断，这里只执行。
func (s *Service) Pause(ctx context.Context, brandID string, c PauseCommand) (PauseResult, error) {
	var out PauseResult
	if err := validActor(c.Actor); err != nil {
		return out, err
	}
	switch c.Metric {
	case "开锁成功率", "可用率", "人工":
	default:
		return out, apperr.Invalid("暂停依据 metric 只能是 开锁成功率、可用率、人工")
	}
	if !reasonOK(c.Reason) {
		return out, apperr.ReasonRequired()
	}
	reason := strings.TrimSpace(c.Reason)
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		status, opID, err := lockBrand(ctx, tx, brandID)
		if err != nil {
			return err
		}
		if status == "暂停可租" {
			return apperr.AlreadyDone("品牌已是暂停可租")
		}
		if status != "已接入" {
			return apperr.Conflict("品牌当前为" + status + "，只有已接入的品牌能暂停可租")
		}
		if err := s.transit(ctx, tx, brandID, opID, status, "暂停可租", *c.Actor, now, &reason); err != nil {
			return err
		}
		seq, err := nextSeq(ctx, tx, "pause")
		if err != nil {
			return err
		}
		pauseID := fmt.Sprintf("PS%s%03d", now.In(CST).Format("20060102"), seq)
		if _, err := tx.Exec(ctx, `INSERT INTO pauses (pause_id, brand_id, paused_at, metric, value, threshold, sustain_minutes, by_actor, note)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, pauseID, brandID, now, c.Metric, c.Value, c.Threshold, c.SustainMinutes, mustJSON(c.Actor), reason); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, *c.Actor, now, "暂停可租", "brand", brandID, "品牌暂停可租（依据："+c.Metric+"）", &reason,
			map[string]any{"accessStatus": status}, map[string]any{"accessStatus": "暂停可租"}); err != nil {
			return err
		}
		if out.Brand, err = getBrand(ctx, tx, brandID); err != nil {
			return err
		}
		out.Pause, err = scanPause(tx.QueryRow(ctx, pauseSelect+` WHERE pause_id = $1`, pauseID))
		return err
	})
	return out, err
}

// Resume：暂停可租 → 已接入，在当前暂停记录上记恢复时刻。
func (s *Service) Resume(ctx context.Context, brandID string, actor *Actor, reason string) (PauseResult, error) {
	var out PauseResult
	if err := validActor(actor); err != nil {
		return out, err
	}
	if !reasonOK(reason) {
		return out, apperr.ReasonRequired()
	}
	reason = strings.TrimSpace(reason)
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		status, opID, err := lockBrand(ctx, tx, brandID)
		if err != nil {
			return err
		}
		if status != "暂停可租" {
			return apperr.Conflict("品牌当前为" + status + "，不在暂停可租")
		}
		if err := s.transit(ctx, tx, brandID, opID, status, "已接入", *actor, now, &reason); err != nil {
			return err
		}
		p, err := currentPause(ctx, tx, brandID)
		if err != nil {
			return err
		}
		if p == nil {
			// 历史数据缺暂停记录时补一条，保证恢复可追溯
			seq, err := nextSeq(ctx, tx, "pause")
			if err != nil {
				return err
			}
			id := fmt.Sprintf("PS%s%03d", now.In(CST).Format("20060102"), seq)
			if _, err := tx.Exec(ctx, `INSERT INTO pauses (pause_id, brand_id, paused_at, metric) VALUES ($1, $2, $3, '人工')`, id, brandID, now); err != nil {
				return err
			}
			p = &Pause{PauseID: id}
		}
		if _, err := tx.Exec(ctx, `UPDATE pauses SET resumed_at = $2, resumed_by = $3, resume_note = $4 WHERE pause_id = $1`,
			p.PauseID, now, mustJSON(actor), reason); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, *actor, now, "恢复可租", "brand", brandID, "品牌恢复可租", &reason,
			map[string]any{"accessStatus": status}, map[string]any{"accessStatus": "已接入"}); err != nil {
			return err
		}
		if out.Brand, err = getBrand(ctx, tx, brandID); err != nil {
			return err
		}
		out.Pause, err = scanPause(tx.QueryRow(ctx, pauseSelect+` WHERE pause_id = $1`, p.PauseID))
		return err
	})
	return out, err
}

/* ---------------- 运营商退出 ---------------- */

type ExitResult struct {
	Operator         Operator `json:"operator"`
	InvalidatedCodes int      `json:"invalidatedCodes"`
}

// SetOperatorExit 按线下结果设置运营商已退出：品牌全部已退出、未失效的政府码全部失效。
func (s *Service) SetOperatorExit(ctx context.Context, operatorID, reason string, actor Actor) (ExitResult, error) {
	var out ExitResult
	if !reasonOK(reason) {
		return out, apperr.ReasonRequired()
	}
	reason = strings.TrimSpace(reason)
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var status, short string
		err := tx.QueryRow(ctx, `SELECT status, short_name FROM operators WHERE operator_id = $1 FOR UPDATE`, operatorID).Scan(&status, &short)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound("运营商")
		}
		if err != nil {
			return err
		}
		if status == "已退出" {
			return apperr.AlreadyDone("该运营商已是退出状态")
		}
		if _, err := tx.Exec(ctx, `UPDATE operators SET status = '已退出', exited_at = $2 WHERE operator_id = $1`, operatorID, now); err != nil {
			return err
		}
		brandRows, err := tx.Query(ctx, `SELECT brand_id FROM brands WHERE operator_id = $1 ORDER BY brand_id`, operatorID)
		if err != nil {
			return err
		}
		var brandIDs []string
		for brandRows.Next() {
			var id string
			_ = brandRows.Scan(&id)
			brandIDs = append(brandIDs, id)
		}
		brandRows.Close()
		if brandIDs == nil {
			brandIDs = []string{}
		}
		for _, b := range brandIDs {
			bs, _, err := lockBrand(ctx, tx, b)
			if err != nil {
				return err
			}
			if bs != "已退出" {
				if err := s.transit(ctx, tx, b, operatorID, bs, "已退出", actor, now, &reason); err != nil {
					return err
				}
			}
			rows, err := tx.Query(ctx, `UPDATE vehicles v SET code_status = '已失效', code_invalid_at = $2
				FROM (SELECT vehicle_id, code_status AS old FROM vehicles WHERE brand_id = $1 AND code_status <> '已失效' FOR UPDATE) o
				WHERE v.vehicle_id = o.vehicle_id RETURNING v.vehicle_id, v.gov_code, o.old`, b, now)
			if err != nil {
				return err
			}
			type vc struct{ id, code, old string }
			var changed []vc
			for rows.Next() {
				var x vc
				if err := rows.Scan(&x.id, &x.code, &x.old); err != nil {
					rows.Close()
					return err
				}
				changed = append(changed, x)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for _, x := range changed {
				if err := s.emitCodeChanged(ctx, tx, actor, now, x.id, b, x.code, x.old, "已失效"); err != nil {
					return err
				}
			}
			out.InvalidatedCodes += len(changed)
		}
		var readonlyUntil *string
		if s.readonlyDays > 0 {
			readonlyUntil = strPtr(fmtTime(now.AddDate(0, 0, s.readonlyDays)))
		}
		if err := s.emit(ctx, tx, "fleet.operator.exited", operatorID, actor, now, map[string]any{
			"operatorId": operatorID, "brandIds": brandIDs, "exitedAt": fmtTime(now), "readonlyUntil": readonlyUntil, "reason": reason,
		}); err != nil {
			return err
		}
		summary := fmt.Sprintf("按线下结果设置 %s 已退出，%d 辆车政府码失效", short, out.InvalidatedCodes)
		if err := s.audit(ctx, tx, actor, now, "设置运营商退出", "operator", operatorID, summary, &reason,
			map[string]any{"status": status}, map[string]any{"status": "已退出"}); err != nil {
			return err
		}
		out.Operator, err = getOperator(ctx, tx, operatorID)
		return err
	})
	return out, err
}

/* ---------------- 接入状态总览 ---------------- */

// AccessOverview 按接入状态汇总品牌数与车辆数，并逐品牌给出分布、可租车数、历史与当前暂停。scope 非空时只看该运营商。
func (s *Service) AccessOverview(ctx context.Context, scope string) (AccessOverview, error) {
	out := AccessOverview{Summary: []AccessSummary{}, Brands: []AccessBrand{}}
	brands, err := listBrands(ctx, s.db, scope, "", "")
	if err != nil {
		return out, err
	}
	sum := map[string]*AccessSummary{}
	for _, st := range AccessStatuses {
		out.Summary = append(out.Summary, AccessSummary{Status: st})
	}
	for i := range out.Summary {
		sum[out.Summary[i].Status] = &out.Summary[i]
	}
	ops := map[string]Operator{}
	for _, b := range brands {
		if x, ok := sum[b.AccessStatus]; ok {
			x.BrandCount++
			x.VehicleCount += b.VehicleCount
		}
		d, err := brandDetail(ctx, s.db, b)
		if err != nil {
			return out, err
		}
		op, ok := ops[b.OperatorID]
		if !ok {
			if op, err = getOperator(ctx, s.db, b.OperatorID); err != nil {
				return out, err
			}
			ops[b.OperatorID] = op
		}
		ab := AccessBrand{BrandDetail: d, Operator: op, ByStatus: map[string]int{}, ByCodeStatus: map[string]int{}}
		rows, err := s.db.Query(ctx, `SELECT `+displayStatusExpr+`, v.code_status, count(*), count(*) FILTER (WHERE `+rentableExpr+`)
			FROM vehicles v JOIN brands b ON b.brand_id = v.brand_id
			WHERE v.brand_id = $1 AND v.removed_at IS NULL GROUP BY 1, 2`, b.BrandID)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var st, cs string
			var n, r int
			if err := rows.Scan(&st, &cs, &n, &r); err != nil {
				rows.Close()
				return out, err
			}
			ab.ByStatus[st] += n
			ab.ByCodeStatus[cs] += n
			ab.Rentable += r
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
		out.Brands = append(out.Brands, ab)
	}
	return out, nil
}

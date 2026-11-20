package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/ops/internal/clients/fleet"
	"bike/ops/internal/httpx"
)

// Pause 暂停可租记录（ops.yaml Pause）。
type Pause struct {
	ID             string   `json:"id"`
	BrandID        string   `json:"brandId"`
	PausedAt       string   `json:"pausedAt"`
	ResumedAt      *string  `json:"resumedAt"`
	Metric         string   `json:"metric"`
	Value          *float64 `json:"value"`
	Threshold      *float64 `json:"threshold"`
	SustainMinutes *int     `json:"sustainMinutes"`
	By             string   `json:"by"`
	ByName         string   `json:"byName,omitempty"`
	Note           *string  `json:"note"`
	AutoResume     bool     `json:"autoResume"`
}

// autoResumeFor 指标恢复后是否自动恢复可租：只有因可用率暂停的才会；
// 因开锁成功率暂停的与人工暂停的只能人工恢复（例外条款）。
func autoResumeFor(metric string) bool { return metric == "可用率" }

// SuspendSuggestion 暂停可租建议（ops.yaml SuspendSuggestion）。
type SuspendSuggestion struct {
	ID             string  `json:"id"`
	BrandID        string  `json:"brandId"`
	At             string  `json:"at"`
	Metric         string  `json:"metric"`
	Value          float64 `json:"value"`
	Threshold      float64 `json:"threshold"`
	SustainMinutes int     `json:"sustainMinutes"`
	By             string  `json:"by"`
	ByName         string  `json:"byName,omitempty"`
	Note           string  `json:"note"`
	Status         string  `json:"status"`
	DecidedBy      *string `json:"decidedBy"`
	DecidedByName  *string `json:"decidedByName"`
	DecidedAt      *string `json:"decidedAt"`
	DecisionReason *string `json:"decisionReason"`
	PauseID        *string `json:"pauseId"`
}

const pauseCols = `id, brand_id, paused_at, resumed_at, metric, value, threshold, sustain_minutes, by, by_name, note`

func (a *App) queryPauses(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]Pause, error) {
	rows, err := tx.Query(ctx, `SELECT `+pauseCols+` FROM pauses `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pause{}
	for rows.Next() {
		var p Pause
		var pa time.Time
		var ra *time.Time
		var name *string
		if err := rows.Scan(&p.ID, &p.BrandID, &pa, &ra, &p.Metric, &p.Value, &p.Threshold, &p.SustainMinutes, &p.By, &name, &p.Note); err != nil {
			return nil, err
		}
		p.PausedAt, p.ResumedAt = a.fmtTime(pa), a.fmtTimePtr(ra)
		p.AutoResume = autoResumeFor(p.Metric)
		if name != nil {
			p.ByName = *name
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const sgCols = `id, brand_id, at, metric, value, threshold, sustain_minutes, by, by_name, note, status, decided_by, decided_by_name, decided_at, decision_reason, pause_id`

func (a *App) querySuggestions(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]SuspendSuggestion, error) {
	rows, err := tx.Query(ctx, `SELECT `+sgCols+` FROM suspend_suggestions `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SuspendSuggestion{}
	for rows.Next() {
		var s SuspendSuggestion
		var at time.Time
		var da *time.Time
		var name *string
		if err := rows.Scan(&s.ID, &s.BrandID, &at, &s.Metric, &s.Value, &s.Threshold, &s.SustainMinutes, &s.By, &name, &s.Note, &s.Status,
			&s.DecidedBy, &s.DecidedByName, &da, &s.DecisionReason, &s.PauseID); err != nil {
			return nil, err
		}
		s.At, s.DecidedAt = a.fmtTime(at), a.fmtTimePtr(da)
		if name != nil {
			s.ByName = *name
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListPauses 暂停可租记录，按暂停时刻新到旧；运营商只见本家品牌。
func (a *App) ListPauses(ctx context.Context, scope string) ([]Pause, error) {
	var out []Pause
	err := a.read(ctx, func(tx pgx.Tx) error {
		inScope, err := a.scopeBrands(ctx, tx, scope)
		if err != nil {
			return err
		}
		out, err = a.queryPauses(ctx, tx, `WHERE ($1::text[] IS NULL OR brand_id = ANY($1)) ORDER BY paused_at DESC, id DESC`, inScope)
		return err
	})
	return out, err
}

// ListSuggestions 暂停可租建议，按提出时刻新到旧。
func (a *App) ListSuggestions(ctx context.Context, status, brandID string) ([]SuspendSuggestion, error) {
	var out []SuspendSuggestion
	err := a.read(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = a.querySuggestions(ctx, tx, `WHERE ($1 = '' OR status = $1) AND ($2 = '' OR brand_id = $2) ORDER BY at DESC, id DESC`, status, brandID)
		return err
	})
	return out, err
}

// pauseSpec 一次暂停的依据。
type pauseSpec struct {
	metric         string
	value          *float64
	threshold      *float64
	sustainMinutes *int
	note           string
}

// pauseBrand 调 fleet 暂停可租；成功后记暂停记录、告警、留痕。after 在同一事务里执行（如更新建议）。
func (a *App) pauseBrand(ctx context.Context, actor Actor, brandID string, spec pauseSpec, idemKey string, after func(tx pgx.Tx, pauseID string) error) (Pause, error) {
	if _, err := a.Fleet.PauseBrand(ctx, brandID, idemKey, fleet.PauseRequest{
		Actor: fleet.Actor(svcActor(actor)), Metric: spec.metric, Value: spec.value, Threshold: spec.threshold, SustainMinutes: spec.sustainMinutes, Reason: spec.note,
	}); err != nil {
		return Pause{}, remoteErr(err, "没有找到该品牌")
	}
	var out Pause
	err := a.tx(ctx, func(tx pgx.Tx) error {
		now := a.now()
		id, err := a.nextID(ctx, tx, "OPS-PS", now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO pauses (`+pauseCols+`) VALUES ($1,$2,$3,NULL,$4,$5,$6,$7,$8,$9,$10)`,
			id, brandID, now, spec.metric, spec.value, spec.threshold, spec.sustainMinutes, actor.ID, actor.Name, spec.note); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brands SET access_status = '暂停可租', updated_at = now() WHERE brand_id = $1`, brandID); err != nil {
			return err
		}
		name := a.brandName(ctx, tx, brandID)
		bid := brandID
		if err := a.raiseAlert(ctx, tx, "警告", AlertPaused, fmt.Sprintf("%s 已暂停可租：%s", name, spec.note), &bid, nil, ""); err != nil {
			return err
		}
		summary := name + " 暂停可租"
		if spec.value != nil {
			summary += fmt.Sprintf("（%s %.1f%%）", spec.metric, *spec.value*100)
		}
		note := spec.note
		if err := a.audit(ctx, tx, actor, "暂停可租", "brand", brandID, summary, &note, map[string]any{"accessStatus": "已接入"}, map[string]any{"accessStatus": "暂停可租"}); err != nil {
			return err
		}
		if after != nil {
			if err := after(tx, id); err != nil {
				return err
			}
		}
		ps, err := a.queryPauses(ctx, tx, `WHERE id = $1`, id)
		if err != nil {
			return err
		}
		out = ps[0]
		return nil
	})
	return out, err
}

// PauseBrand 人工暂停可租。
func (a *App) PauseBrand(ctx context.Context, actor Actor, brandID, reason, idemKey string) (Pause, error) {
	if !httpx.ValidReason(reason) {
		return Pause{}, httpx.ReasonRequired()
	}
	key := randomKey()
	if idemKey != "" {
		key = derivedKey("pause", actor.ID, brandID, idemKey)
	}
	return a.pauseBrand(ctx, actor, brandID, pauseSpec{metric: "人工", note: strings.TrimSpace(reason)}, key, nil)
}

// ResumeBrand 人工恢复可租。
func (a *App) ResumeBrand(ctx context.Context, actor Actor, brandID, reason, idemKey string) (Pause, error) {
	if !httpx.ValidReason(reason) {
		return Pause{}, httpx.ReasonRequired()
	}
	key := randomKey()
	if idemKey != "" {
		key = derivedKey("resume", actor.ID, brandID, idemKey)
	}
	return a.resumeBrand(ctx, actor, brandID, strings.TrimSpace(reason), key)
}

// resumeBrand 调 fleet 恢复可租，补记暂停记录的恢复时刻；本模块没有对应记录时按 fleet 返回的记录补一条。
func (a *App) resumeBrand(ctx context.Context, actor Actor, brandID, reason, idemKey string) (Pause, error) {
	res, err := a.Fleet.ResumeBrand(ctx, brandID, idemKey, fleet.ResumeRequest{Actor: fleet.Actor(svcActor(actor)), Reason: reason})
	if err != nil {
		return Pause{}, remoteErr(err, "没有找到该品牌")
	}
	var out Pause
	err = a.tx(ctx, func(tx pgx.Tx) error {
		now := a.now()
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM pauses WHERE brand_id = $1 AND resumed_at IS NULL ORDER BY paused_at DESC LIMIT 1 FOR UPDATE`, brandID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			pausedAt := now
			if t, e := time.Parse(time.RFC3339, res.Pause.PausedAt); e == nil {
				pausedAt = t
			}
			if id, err = a.nextID(ctx, tx, "OPS-PS", pausedAt); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO pauses (id, brand_id, paused_at, metric, by, note) VALUES ($1,$2,$3,'人工',$4,'暂停记录由 fleet 补入')`,
				id, brandID, pausedAt, actor.ID); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE pauses SET resumed_at = $2 WHERE id = $1`, id, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brands SET access_status = '已接入', updated_at = now() WHERE brand_id = $1`, brandID); err != nil {
			return err
		}
		name := a.brandName(ctx, tx, brandID)
		bid := brandID
		if err := a.raiseAlert(ctx, tx, "提示", AlertResumed, fmt.Sprintf("%s 已恢复可租：%s", name, reason), &bid, nil, ""); err != nil {
			return err
		}
		r := reason
		if err := a.audit(ctx, tx, actor, "恢复可租", "brand", brandID, name+" 恢复可租，回到已接入", &r, map[string]any{"accessStatus": "暂停可租"}, map[string]any{"accessStatus": "已接入"}); err != nil {
			return err
		}
		ps, err := a.queryPauses(ctx, tx, `WHERE id = $1`, id)
		if err != nil {
			return err
		}
		out = ps[0]
		return nil
	})
	return out, err
}

func (a *App) loadSuggestion(ctx context.Context, id string) (SuspendSuggestion, error) {
	var out SuspendSuggestion
	err := a.read(ctx, func(tx pgx.Tx) error {
		ss, err := a.querySuggestions(ctx, tx, `WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if len(ss) == 0 {
			return httpx.NotFound("没有找到该暂停建议")
		}
		out = ss[0]
		return nil
	})
	return out, err
}

// ConfirmSuspend 运维确认建议：调 fleet 暂停可租，记暂停记录，留痕记确认人。
func (a *App) ConfirmSuspend(ctx context.Context, actor Actor, id, reason, idemKey string) (SuspendSuggestion, error) {
	if !httpx.ValidReason(reason) {
		return SuspendSuggestion{}, httpx.ReasonRequired()
	}
	sg, err := a.loadSuggestion(ctx, id)
	if err != nil {
		return sg, err
	}
	if sg.Status != "待确认" {
		return sg, httpx.New(409, "ALREADY_DONE", "该建议已"+sg.Status)
	}
	key := randomKey()
	if idemKey != "" {
		key = derivedKey("confirm", actor.ID, id, idemKey)
	}
	why := strings.TrimSpace(reason)
	v, th, sm := sg.Value, sg.Threshold, sg.SustainMinutes
	_, err = a.pauseBrand(ctx, actor, sg.BrandID, pauseSpec{
		metric: sg.Metric, value: &v, threshold: &th, sustainMinutes: &sm, note: "确认运行智能体建议：" + sg.Note + "（" + why + "）",
	}, key, func(tx pgx.Tx, pauseID string) error {
		tag, err := tx.Exec(ctx, `UPDATE suspend_suggestions SET status = '已确认', decided_by = $2, decided_by_name = $3, decided_at = $4,
			decision_reason = $5, pause_id = $6 WHERE id = $1 AND status = '待确认'`, id, actor.ID, actor.Name, a.now(), why, pauseID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.New(409, "ALREADY_DONE", "该建议已被处理")
		}
		return nil
	})
	if err != nil {
		return SuspendSuggestion{}, err
	}
	return a.loadSuggestion(ctx, id)
}

// DismissSuspend 运维驳回建议，品牌保持已接入。
func (a *App) DismissSuspend(ctx context.Context, actor Actor, id, reason string) (SuspendSuggestion, error) {
	if !httpx.ValidReason(reason) {
		return SuspendSuggestion{}, httpx.ReasonRequired()
	}
	why := strings.TrimSpace(reason)
	err := a.tx(ctx, func(tx pgx.Tx) error {
		ss, err := a.querySuggestions(ctx, tx, `WHERE id = $1 FOR UPDATE`, id)
		if err != nil {
			return err
		}
		if len(ss) == 0 {
			return httpx.NotFound("没有找到该暂停建议")
		}
		if ss[0].Status != "待确认" {
			return httpx.New(409, "ALREADY_DONE", "该建议已"+ss[0].Status)
		}
		if _, err := tx.Exec(ctx, `UPDATE suspend_suggestions SET status = '已驳回', decided_by = $2, decided_by_name = $3, decided_at = $4, decision_reason = $5 WHERE id = $1`,
			id, actor.ID, actor.Name, a.now(), why); err != nil {
			return err
		}
		return a.audit(ctx, tx, actor, "驳回暂停建议", "brand", ss[0].BrandID, "驳回运行智能体对 "+a.brandName(ctx, tx, ss[0].BrandID)+" 的暂停可租建议", &why, nil, nil)
	})
	if err != nil {
		return SuspendSuggestion{}, err
	}
	return a.loadSuggestion(ctx, id)
}

// SetAutoSuspend 自动暂停开关（默认关闭），运维带事由开关。
func (a *App) SetAutoSuspend(ctx context.Context, actor Actor, on bool, reason string) (bool, error) {
	if !httpx.ValidReason(reason) {
		return false, httpx.ReasonRequired()
	}
	why := strings.TrimSpace(reason)
	err := a.tx(ctx, func(tx pgx.Tx) error {
		before, err := a.autoPause(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO settings (key, value, updated_at) VALUES ('autoPauseByAgent', to_jsonb($1::boolean), now())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, on); err != nil {
			return err
		}
		label := map[bool]string{true: "开启", false: "关闭"}
		return a.audit(ctx, tx, actor, "设置自动暂停", "pricing", "autoPauseByAgent", "运行智能体自动暂停可租："+label[before]+" → "+label[on], &why,
			map[string]any{"autoPauseByAgent": before}, map[string]any{"autoPauseByAgent": on})
	})
	return on, err
}

package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/ops/internal/httpx"
)

// 告警类型。前 10 个取自 ops.yaml Alert.type；后 5 个是契约枚举里没有、但"智能体未接入时只告警"必需的类型（见交付说明的契约问题）。
const (
	AlertDegradeEnter        = "降级进入"
	AlertDegradeExit         = "降级退出"
	AlertDegradeTooLong      = "降级持续超阈值"
	AlertOrderOverLimit      = "降级单超限"
	AlertLimitNear           = "接近限额"
	AlertLimitReached        = "达到限额"
	AlertSuspendSuggested    = "暂停可租建议"
	AlertPaused              = "暂停可租"
	AlertResumed             = "恢复可租"
	AlertDependencyTripped   = "依赖熔断"
	AlertDependencyRecovered = "依赖恢复"
	AlertOperatorUnreachable = "运营商不可达"
	AlertOperatorRecovered   = "运营商已恢复"
	AlertBrandBelow          = "品牌指标低于门槛"
	AlertBrandRecovered      = "品牌指标已恢复"
)

// Alert 告警（ops.yaml Alert）。
type Alert struct {
	AlertID        string   `json:"alertId"`
	At             string   `json:"at"`
	Level          string   `json:"level"`
	Type           string   `json:"type"`
	Text           string   `json:"text"`
	BrandID        *string  `json:"brandId"`
	DegradeEventID *string  `json:"degradeEventId"`
	Notified       []string `json:"notified"`
	AckBy          *string  `json:"ackBy"`
	AckByName      *string  `json:"ackByName"`
	AckAt          *string  `json:"ackAt"`
}

// raiseAlert 写告警、通知值班人（notified 记配置的值班人账号）、发 ops.alert.raised。dedupKey 非空时同键只告一次。
func (a *App) raiseAlert(ctx context.Context, tx pgx.Tx, level, typ, text string, brandID, degradeEventID *string, dedupKey string) error {
	at := a.now()
	if dedupKey != "" {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE dedup_key = $1`, dedupKey).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
	}
	id, err := a.nextID(ctx, tx, "OPS-AL", at)
	if err != nil {
		return err
	}
	notified := a.Cfg.OnDuty
	if notified == nil {
		notified = []string{}
	}
	var dk *string
	if dedupKey != "" {
		dk = &dedupKey
	}
	if _, err := tx.Exec(ctx, `INSERT INTO alerts (alert_id, at, level, type, text, brand_id, degrade_event_id, notified, dedup_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, at, level, typ, text, brandID, degradeEventID, notified, dk); err != nil {
		return err
	}
	return a.emit(ctx, tx, "ops.alert.raised", id, SystemActor, map[string]any{
		"alertId": id, "level": level, "type": typ, "text": text, "brandId": brandID, "degradeEventId": degradeEventID, "at": a.fmtTime(at),
	})
}

const alertCols = `alert_id, at, level, type, text, brand_id, degrade_event_id, notified, ack_by, ack_by_name, ack_at`

func (a *App) scanAlert(row pgx.Row) (Alert, error) {
	var x Alert
	var at time.Time
	var ackAt *time.Time
	if err := row.Scan(&x.AlertID, &at, &x.Level, &x.Type, &x.Text, &x.BrandID, &x.DegradeEventID, &x.Notified, &x.AckBy, &x.AckByName, &ackAt); err != nil {
		return x, err
	}
	x.At = a.fmtTime(at)
	x.AckAt = a.fmtTimePtr(ackAt)
	if x.Notified == nil {
		x.Notified = []string{}
	}
	return x, nil
}

// AlertFilter 告警列表条件。
type AlertFilter struct {
	Level    string
	Acked    *bool
	Page     int
	PageSize int
	SortKey  string
	SortDir  string
}

// AlertPage 分页告警。
type AlertPage struct {
	Rows     []Alert `json:"rows"`
	Total    int     `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"pageSize"`
}

// ListAlerts 告警列表，默认按时刻新到旧。
func (a *App) ListAlerts(ctx context.Context, f AlertFilter) (AlertPage, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 20
	}
	if f.PageSize > 200 {
		f.PageSize = 200
	}
	order := "at"
	if f.SortKey == "level" {
		order = "CASE level WHEN '严重' THEN 3 WHEN '警告' THEN 2 ELSE 1 END"
	}
	dir := "DESC"
	if f.SortDir == "asc" {
		dir = "ASC"
	}
	where := `($1 = '' OR level = $1) AND ($2::boolean IS NULL OR (ack_at IS NOT NULL) = $2)`
	out := AlertPage{Rows: []Alert{}, Page: f.Page, PageSize: f.PageSize}
	err := a.read(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE `+where, f.Level, f.Acked).Scan(&out.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+alertCols+` FROM alerts WHERE `+where+
			fmt.Sprintf(` ORDER BY %s %s, alert_id %s LIMIT $3 OFFSET $4`, order, dir, dir), f.Level, f.Acked, f.PageSize, (f.Page-1)*f.PageSize)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			x, err := a.scanAlert(rows)
			if err != nil {
				return err
			}
			out.Rows = append(out.Rows, x)
		}
		return rows.Err()
	})
	return out, err
}

// unackedAlerts 未确认告警，新到旧，最多 n 条。
func (a *App) unackedAlerts(ctx context.Context, tx pgx.Tx, n int) ([]Alert, error) {
	rows, err := tx.Query(ctx, `SELECT `+alertCols+` FROM alerts WHERE ack_at IS NULL ORDER BY at DESC, alert_id DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		x, err := a.scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// AckAlert 确认告警。
func (a *App) AckAlert(ctx context.Context, actor Actor, alertID string) (Alert, error) {
	var out Alert
	err := a.tx(ctx, func(tx pgx.Tx) error {
		x, err := a.scanAlert(tx.QueryRow(ctx, `SELECT `+alertCols+` FROM alerts WHERE alert_id = $1 FOR UPDATE`, alertID))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("没有找到该告警")
		}
		if err != nil {
			return err
		}
		if x.AckAt != nil {
			return httpx.New(409, "ALREADY_DONE", "该告警已确认")
		}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET ack_by = $2, ack_by_name = $3, ack_at = $4 WHERE alert_id = $1`, alertID, actor.ID, actor.Name, a.now()); err != nil {
			return err
		}
		// 值班人确认"降级进入"告警即记入该降级事件时间线
		if x.Type == AlertDegradeEnter && x.DegradeEventID != nil {
			if err := a.addTimeline(ctx, tx, *x.DegradeEventID, actor, "值班人确认", "已知悉降级，关注限额使用量"); err != nil {
				return err
			}
		}
		out, err = a.scanAlert(tx.QueryRow(ctx, `SELECT `+alertCols+` FROM alerts WHERE alert_id = $1`, alertID))
		return err
	})
	return out, err
}

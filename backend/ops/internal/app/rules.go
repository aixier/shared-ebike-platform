package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/ops/internal/metrics"
)

// RiskLimitTier 单笔上限。
type RiskLimitTier struct {
	MaxMinutes int   `json:"maxMinutes"`
	MaxAmount  int64 `json:"maxAmount"`
}

// RiskLimits 降级限额，取自规则版本。
type RiskLimits struct {
	PerUserOpenDegradedOrders   int           `json:"perUserOpenDegradedOrders"`
	Normal                      RiskLimitTier `json:"normal"`
	Tight                       RiskLimitTier `json:"tight"`
	HourlyDegradedOrders        int           `json:"hourlyDegradedOrders"`
	DegradeDurationAlertMinutes int           `json:"degradeDurationAlertMinutes"`
}

// PauseThreshold 暂停可租门槛。
type PauseThreshold struct {
	UnlockSuccessRate float64 `json:"unlockSuccessRate"`
	Availability      float64 `json:"availability"`
	SustainMinutes    int     `json:"sustainMinutes"`
}

// Thresholds 监控门槛（来自平台参数）。
type Thresholds struct {
	Pause              PauseThreshold `json:"pause"`
	AvailabilityTarget float64        `json:"availabilityTarget"`
	UnlockP90TargetSec float64        `json:"unlockP90TargetSec"`
	BillPushTargetSec  float64        `json:"billPushTargetSec"`
}

// effectiveLimits 某时刻有效的规则版本里的限额；没有收到任何版本时返回零值与空版本号。
func (a *App) effectiveLimits(ctx context.Context, q pgx.Tx, at time.Time) (string, RiskLimits, error) {
	var id string
	var raw []byte
	err := q.QueryRow(ctx, `SELECT version_id, risk_limits FROM rule_versions
		WHERE effective_at <= $1 AND NOT revoked ORDER BY effective_at DESC LIMIT 1`, at).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", RiskLimits{}, nil
	}
	if err != nil {
		return "", RiskLimits{}, err
	}
	var l RiskLimits
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &l)
	}
	return id, l, nil
}

// ridePrice 某规则版本的骑行价格。
func (a *App) ridePrice(ctx context.Context, q pgx.Tx, versionID string) (metrics.RidePrice, bool, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT ride FROM rule_versions WHERE version_id = $1`, versionID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return metrics.RidePrice{}, false, nil
	}
	if err != nil {
		return metrics.RidePrice{}, false, err
	}
	var p metrics.RidePrice
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	return p, true, nil
}

// thresholds 当前平台参数里的门槛；未收到参数时为零值（不触发任何低于门槛）。
func (a *App) thresholds(ctx context.Context, q pgx.Tx) (Thresholds, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT body FROM platform_params WHERE id = 1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Thresholds{}, nil
	}
	if err != nil {
		return Thresholds{}, err
	}
	var p struct {
		PauseThreshold     PauseThreshold `json:"pauseThreshold"`
		AvailabilityTarget float64        `json:"availabilityTarget"`
		UnlockP90TargetSec float64        `json:"unlockP90TargetSec"`
		BillPushTargetSec  float64        `json:"billPushTargetSec"`
	}
	_ = json.Unmarshal(raw, &p)
	return Thresholds{Pause: p.PauseThreshold, AvailabilityTarget: p.AvailabilityTarget, UnlockP90TargetSec: p.UnlockP90TargetSec, BillPushTargetSec: p.BillPushTargetSec}, nil
}

// autoPause 自动暂停开关，默认关闭。
func (a *App) autoPause(ctx context.Context, q pgx.Tx) (bool, error) {
	var on bool
	err := q.QueryRow(ctx, `SELECT (value)::boolean FROM settings WHERE key = 'autoPauseByAgent'`).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// readTx 只读事务的便捷封装。
func (a *App) read(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, a.DB, pgx.TxOptions{AccessMode: pgx.ReadOnly}, fn)
}

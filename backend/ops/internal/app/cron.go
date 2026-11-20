package app

import (
	"context"
	"log/slog"

	"github.com/robfig/cron/v3"
)

// 定时任务的数据库锁编号（pg_try_advisory_lock），多实例时只跑一份。
const (
	lockProbe   = 813001
	lockMetrics = 813002
	lockHealth  = 813003
	lockLimits  = 813004
	lockClosure = 813005
)

// withLock 拿到锁才执行；拿不到说明别的实例正在跑，跳过。
func (a *App) withLock(ctx context.Context, id int64, fn func(context.Context) error) (bool, error) {
	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, id).Scan(&got); err != nil {
		return false, err
	}
	if !got {
		return false, nil
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, id) }()
	return true, fn(ctx)
}

// StartCron 每分钟：采样入口可用性、重算指标、检查品牌健康、检查限额与降级时长。
func (a *App) StartCron(ctx context.Context) *cron.Cron {
	c := cron.New()
	job := func(name string, id int64, fn func(context.Context) error) {
		_, _ = c.AddFunc("@every 1m", func() {
			if _, err := a.withLock(ctx, id, fn); err != nil {
				slog.Warn("定时任务失败", "job", name, "err", err)
			}
		})
	}
	job("入口可用性采样", lockProbe, a.ProbeEntry)
	job("指标重算", lockMetrics, a.RefreshMetrics)
	job("品牌健康检查", lockHealth, a.CheckBrandHealth)
	job("限额检查", lockLimits, a.CheckLimits)
	job("闭合刷新", lockClosure, a.SweepClosures)
	c.Start()
	return c
}

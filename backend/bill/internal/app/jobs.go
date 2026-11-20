package app

import (
	"context"

	"github.com/robfig/cron/v3"
)

// 定时任务：多实例时用 pg_try_advisory_lock 保证同一时刻只跑一份。
const jobLockKey = 18308001

// RunJobsOnce 跑一轮全部到点任务（拿不到锁说明别的实例在跑，直接返回）
func (a *App) RunJobsOnce(ctx context.Context) {
	conn, err := a.Pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, jobLockKey).Scan(&got); err != nil || !got {
		return
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, jobLockKey) }()
	for name, job := range map[string]func(context.Context) error{
		"免押扣款重试": a.RetryCredit, "支付期限": a.RunDeadlines, "欠费超期": a.RunOverdue, "月卡支付重查": a.RetryCards,
		"调起参数过期": a.ReleaseStaleIntents, "批量发券": a.RunCouponBatches,
	} {
		if err := job(ctx); err != nil {
			a.Log.Warn("定时任务失败", "job", name, "err", err)
		}
	}
}

// StartJobs 每 30 秒跑一轮
func (a *App) StartJobs(ctx context.Context) *cron.Cron {
	c := cron.New(cron.WithSeconds())
	_, _ = c.AddFunc("*/30 * * * * *", func() { a.RunJobsOnce(ctx) })
	c.Start()
	return c
}

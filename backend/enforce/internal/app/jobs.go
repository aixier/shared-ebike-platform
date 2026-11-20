package app

import (
	"context"

	"github.com/robfig/cron/v3"
)

// 定时任务的数据库咨询锁键，多实例时只跑一份
const jobLockKey int64 = 0x656e666f726365 // "enforce"

// withJobLock 拿到咨询锁才执行 fn；拿不到说明别的实例在跑，直接返回。
func (a *App) withJobLock(ctx context.Context, fn func(context.Context) error) error {
	conn, err := a.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", jobLockKey).Scan(&got); err != nil {
		return err
	}
	if !got {
		return nil
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", jobLockKey) }()
	return fn(ctx)
}

// StartJobs 启动定时任务：按 schedule 清理未被证据引用的过期照片。返回的 cron 由调用方 Stop。
func (a *App) StartJobs(ctx context.Context, schedule string) (*cron.Cron, error) {
	c := cron.New()
	_, err := c.AddFunc(schedule, func() {
		err := a.withJobLock(ctx, func(ctx context.Context) error {
			n, err := a.CleanupOrphanPhotos(ctx)
			if err == nil && n > 0 {
				a.Log.Info("已清理未引用照片", "count", n)
			}
			return err
		})
		if err != nil {
			a.Log.Error("清理未引用照片失败", "err", err)
		}
	})
	if err != nil {
		return nil, err
	}
	c.Start()
	return c, nil
}

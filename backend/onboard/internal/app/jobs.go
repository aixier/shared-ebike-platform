package app

import (
	"context"
	"time"

	"github.com/robfig/cron/v3"
)

// Start 启动后台工作：建流、发件表补发、订阅、定时任务。ctx 取消时全部停止。
func (a *App) Start(ctx context.Context) {
	go func() {
		for {
			if err := a.EnsureStream(ctx); err == nil {
				break
			} else {
				a.logger().Info("事件流暂不可用，稍后重试", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			if _, err := a.PublishPending(ctx); err != nil {
				a.logger().Info("发件表补发失败，稍后重试", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	a.StartConsumers(ctx)
	c := cron.New(cron.WithSeconds())
	_, _ = c.AddFunc("*/30 * * * * *", func() { a.withJobLock(ctx, "retry-start", a.RetryPendingStarts) })
	_, _ = c.AddFunc("0 */10 * * * *", func() { a.withJobLock(ctx, "nonce-cleanup", a.CleanupNonces) })
	c.Start()
	go func() { <-ctx.Done(); c.Stop() }()
}

// withJobLock 多实例时用数据库锁保证只跑一份。
func (a *App) withJobLock(ctx context.Context, name string, fn func(context.Context)) {
	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, "job:"+name).Scan(&ok); err != nil || !ok {
		return
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext($1))`, "job:"+name)
	}()
	fn(ctx)
}

// CleanupNonces 清理超出时间窗的随机数。
func (a *App) CleanupNonces(ctx context.Context) {
	_, _ = a.DB.Exec(ctx, `DELETE FROM sandbox_nonces WHERE seen_at < now() - make_interval(secs => $1)`, 2*a.Cfg.ClockSkew.Seconds())
}

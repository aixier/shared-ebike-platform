package app

import (
	"context"
	"time"

	"github.com/robfig/cron/v3"

	"bike/settle/internal/calc"
)

// 定时任务：次月 1 日 00:30 自动出单（BR-20）。多实例时用 pg_try_advisory_lock 保证只跑一份。

const autoSettleLockKey = 731100002

// AutoSettle 把"已到出单时刻、尚未出单"的结算月补出来，返回出单的月份。
// 从最近一次出单月的下一个月起，到上个月为止；从未出过单时只出上个月。
func (a *App) AutoSettle(ctx context.Context) ([]string, error) {
	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, autoSettleLockKey).Scan(&got); err != nil {
		return nil, err
	}
	if !got {
		return nil, nil
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, autoSettleLockKey) //nolint:errcheck

	now := a.Now()
	cur := calc.MonthOf(now)
	curStart, _ := calc.MonthStart(cur)
	last := calc.PrevMonth(cur)
	if now.Before(curStart.Add(30 * time.Minute)) {
		last = calc.PrevMonth(last) // 本月 1 日 00:30 前，上个月还不到自动出单时刻
	}
	var maxMonth *string
	if err := conn.QueryRow(ctx, `SELECT max(month) FROM settle_months`).Scan(&maxMonth); err != nil {
		return nil, err
	}
	from := last
	if maxMonth != nil {
		from = calc.NextMonth(*maxMonth)
	}
	var done []string
	for m := from; m <= last; m = calc.NextMonth(m) {
		if _, err := a.Generate(ctx, m, "自动出单", SystemActor); err != nil {
			if ae, ok := asAPI(err); ok && ae.Code == "ALREADY_GENERATED" {
				continue
			}
			return done, err
		}
		done = append(done, m)
	}
	return done, nil
}

// StartScheduler 启动定时任务（每月 1 日 00:30，东八区），并立即补跑一次。
func (a *App) StartScheduler(ctx context.Context) (*cron.Cron, error) {
	c := cron.New(cron.WithLocation(calc.Loc))
	run := func() {
		if ms, err := a.AutoSettle(ctx); err != nil {
			a.Log.Error("自动出单失败", "err", err)
		} else if len(ms) > 0 {
			a.Log.Info("自动出单完成", "months", ms)
		}
	}
	if _, err := c.AddFunc("30 0 1 * *", run); err != nil {
		return nil, err
	}
	c.Start()
	go run()
	return c, nil
}

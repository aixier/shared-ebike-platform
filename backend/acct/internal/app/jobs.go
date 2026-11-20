package app

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"
)

// 定时任务的数据库锁编号（多实例只跑一份）
const jobsLockKey = 18301001

// RunJobs 一轮定时任务：先骑后验逾期未验的冻结；降级已退出时对仍待核验的用户重试补验
func (a *App) RunJobs(ctx context.Context) error {
	conn, err := a.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, jobsLockKey).Scan(&got); err != nil {
		return err
	}
	if !got {
		return nil
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, jobsLockKey) }()
	if err := a.freezeOverdue(ctx); err != nil {
		return err
	}
	return a.reverifyPending(ctx)
}

// freezeOverdue 先骑后验逾期未完成实名核验的冻结（BR-34）
func (a *App) freezeOverdue(ctx context.Context) error {
	return a.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+userCols+` FROM users WHERE rn_status='待核验' AND rn_deadline < $1 AND status='正常' FOR UPDATE`, a.Now())
		if err != nil {
			return err
		}
		var list []*userRow
		for rows.Next() {
			u, err := scanUser(rows)
			if err != nil {
				rows.Close()
				return err
			}
			list = append(list, u)
		}
		rows.Close()
		for _, u := range list {
			if err := a.freeze(ctx, tx, u, "核验", "先骑后验逾期未完成实名核验", "完成实名核验并结清欠费后自动解冻", systemActor()); err != nil {
				return err
			}
		}
		return nil
	})
}

// reverifyPending 降级期间不补验（实名服务仍故障）；降级退出后对待核验用户逐个补验
func (a *App) reverifyPending(ctx context.Context) error {
	active, _, err := degradeActive(ctx, a.Pool, "实名核验")
	if err != nil || active {
		return err
	}
	rows, err := a.Pool.Query(ctx, `SELECT user_id FROM users WHERE rn_status='待核验' AND id_no IS NOT NULL AND status <> '已注销' ORDER BY user_id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := a.reverifyUser(ctx, id); err != nil {
			a.Log.Error("补验失败", "userId", id, "err", err)
		}
	}
	return nil
}

// RunScheduler 每分钟跑一轮定时任务
func (a *App) RunScheduler(ctx context.Context) {
	c := cron.New()
	_, _ = c.AddFunc("@every 1m", func() {
		if err := a.RunJobs(ctx); err != nil {
			a.Log.Error("定时任务失败", "err", err)
		}
	})
	c.Start()
	<-ctx.Done()
	<-c.Stop().Done()
}

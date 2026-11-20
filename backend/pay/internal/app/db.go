package app

// 迁移启动与数据库锁。

import (
	"context"
	"database/sql"
	"fmt"

	"bike/pay/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Migrate 执行内嵌的迁移
func Migrate(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("迁移失败：%w", err)
	}
	return nil
}

// OpenPool 连接池
func OpenPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, dsn)
}

// lockKey 把业务对象映射为事务级咨询锁（同一业务单号、同一订单串行）
func lockKey(ctx context.Context, tx pgx.Tx, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, key)
	return err
}

// 定时任务锁编号（pg_try_advisory_lock，多实例只跑一份）
const (
	jobLockResolve   int64 = 1830901
	jobLockRecon     int64 = 1830902
	jobLockPublisher int64 = 1830903
	jobLockCallback  int64 = 1830904
)

// withJobLock 拿到锁才执行
func (a *App) withJobLock(ctx context.Context, key int64, fn func(context.Context) error) (bool, error) {
	conn, err := a.Pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&ok); err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
	return true, fn(ctx)
}

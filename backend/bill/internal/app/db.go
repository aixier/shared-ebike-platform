package app

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"bike/bill/migrations"
)

// OpenDB 连库并执行迁移
func OpenDB(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if err := Migrate(dsn); err != nil {
		return nil, err
	}
	return pgxpool.New(ctx, dsn)
}

// Migrate 用 goose 执行内嵌迁移
func Migrate(dsn string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return err
	}
	db := stdlib.OpenDB(*cfg)
	defer func(db *sql.DB) { _ = db.Close() }(db)
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

// inTx 在事务里执行 fn
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

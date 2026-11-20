// Package app 是 settle 服务的全部实现：投影、出单、调整项、结算单流程、异议、运营商查询、HTTP 接口、事件收发与定时任务。
// 金额算术在 internal/calc；这里只负责取数、落库与对外格式。
package app

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"bike/settle/internal/auth"
)

// App 持有数据库、时钟与令牌校验器。
type App struct {
	DB       *pgxpool.Pool
	Now      func() time.Time
	Log      *slog.Logger
	Verifier *auth.Verifier
}

// New 构造 App；now 为空时用系统时钟。
func New(db *pgxpool.Pool, v *auth.Verifier, log *slog.Logger, now func() time.Time) *App {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &App{DB: db, Now: now, Log: log, Verifier: v}
}

// Migrate 执行内嵌的 SQL 迁移。
func Migrate(ctx context.Context, pool *pgxpool.Pool, fs embed.FS) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	goose.SetBaseFS(fs)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.UpContext(ctx, db, ".")
}

// Actor 是操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// SystemActor 自动出单与代码生成调整项时的操作人。
var SystemActor = Actor{Kind: "系统", ID: "AC0900", Name: "系统定时任务"}

// ApiError 是带错误码的业务错误，HTTP 层按 Status 返回。
type ApiError struct {
	Status  int
	Code    string
	Message string
	Extra   map[string]any
}

func (e *ApiError) Error() string { return e.Code + ": " + e.Message }

func apiErr(status int, code, msg string) *ApiError {
	return &ApiError{Status: status, Code: code, Message: msg}
}

// 常用错误。
func errNotFound(what string) *ApiError { return apiErr(404, "NOT_FOUND", "没有找到"+what) }
func errInvalid(msg string) *ApiError   { return apiErr(400, "INVALID", msg) }
func errState(msg string) *ApiError {
	if msg == "" {
		msg = "结算单当前状态不允许该操作"
	}
	return apiErr(409, "SETTLEMENT_STATE", msg)
}

var (
	errScope    = apiErr(403, "SCOPE_DENIED", "只能查看和操作本运营商的数据")
	errReadonly = apiErr(403, "READONLY", "运营商已退出，账号为只读，不能提交或修改")
	errReason   = apiErr(400, "REASON_REQUIRED", "请填写事由（至少 4 个字）")
)

// inTx 在事务里执行 fn。
func (a *App) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func asAPI(err error) (*ApiError, bool) {
	var ae *ApiError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

// nextSeq 取名为 name 的序号（事务内递增）。
func nextSeq(ctx context.Context, tx pgx.Tx, name string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `INSERT INTO seqs(name, n) VALUES ($1, 1)
		ON CONFLICT (name) DO UPDATE SET n = seqs.n + 1 RETURNING n`, name).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("取序号 %s: %w", name, err)
	}
	return n, nil
}

// Package app 是 ops 运行值守的全部实现：投影、降级事件、限额使用量、恢复闭合、品牌健康指标、
// 暂停可租、告警、运行智能体的工具接口与页面接口。
//
// 代码只做：事实采集（订阅事件存投影）、指标算术、状态机（降级事件、建议、闭合项）、权限、留痕、外部适配。
// "是否宣布降级""是否品牌自身问题"是运行智能体的判断，本包只给它工具（agenttools.go），不用规则冒充。
package app

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"bike/ops/internal/auth"
	"bike/ops/internal/clients/fleet"
	"bike/ops/internal/clients/pay"
	"bike/ops/internal/clients/ride"
	"bike/ops/internal/config"
	"bike/ops/migrations"
)

// FleetAPI fleet 的暂停与恢复可租。
type FleetAPI interface {
	PauseBrand(ctx context.Context, brandID, idemKey string, req fleet.PauseRequest) (*fleet.PauseResult, error)
	ResumeBrand(ctx context.Context, brandID, idemKey string, req fleet.ResumeRequest) (*fleet.PauseResult, error)
}

// RideAPI ride 的补录锁车事实与健康检查。
type RideAPI interface {
	BackfillLockFact(ctx context.Context, orderID, idemKey string, req ride.LockFactRequest) (*ride.Order, error)
	Healthy(ctx context.Context) bool
}

// PayAPI pay 的确认收款结果。
type PayAPI interface {
	ConfirmPayment(ctx context.Context, paymentID, idemKey string, req pay.ConfirmRequest) (*pay.CommandResult, error)
}

// App 本服务。
type App struct {
	DB       *pgxpool.Pool
	Cfg      config.Config
	Loc      *time.Location
	Now      func() time.Time
	Fleet    FleetAPI
	Ride     RideAPI
	Pay      PayAPI
	Verifier *auth.AdminVerifier
	Callers  *auth.CallerVerifier
}

// New 构造；loc 按 Cfg.Timezone 加载。
func New(db *pgxpool.Pool, cfg config.Config, f FleetAPI, r RideAPI, p PayAPI, v *auth.AdminVerifier) (*App, error) {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return nil, fmt.Errorf("时区 %s 无法加载：%w", cfg.Timezone, err)
	}
	return &App{DB: db, Cfg: cfg, Loc: loc, Now: time.Now, Fleet: f, Ride: r, Pay: p, Verifier: v, Callers: auth.NewCallerVerifier(cfg.AllowedCallers)}, nil
}

// now 服务器时间（本市时区）。
func (a *App) now() time.Time { return a.Now().In(a.Loc) }

// fmtTime RFC 3339 带时区。
func (a *App) fmtTime(t time.Time) string { return t.In(a.Loc).Format(time.RFC3339) }

func (a *App) fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := a.fmtTime(*t)
	return &s
}

// tx 在一个事务里执行。
func (a *App) tx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, a.DB, fn)
}

// Migrate 执行内嵌迁移。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func(db *sql.DB) { _ = db.Close() }(db)
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.UpContext(ctx, db, ".")
}

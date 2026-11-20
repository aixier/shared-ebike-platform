// Package app 是 bill 服务的实现：事件处理、收款编排、定时任务与 HTTP 接口。
package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"bike/bill/internal/auth"
	"bike/bill/internal/clients/deposit"
	"bike/bill/internal/clients/pay"
)

// PayAPI pay 的三条命令
type PayAPI interface {
	ChargeCreditFree(ctx context.Context, idem string, cmd pay.CreditChargeCommand) (pay.Result, error)
	StartOneTap(ctx context.Context, idem string, cmd pay.OneTapPaymentCommand) (pay.Result, error)
	Refund(ctx context.Context, idem string, cmd pay.RefundCommand) (pay.Result, error)
}

// DepositAPI deposit 的押金抵扣命令
type DepositAPI interface {
	Deduct(ctx context.Context, idem string, cmd deposit.DeductCommand) (deposit.DeductResult, error)
}

// CityLoc 本市时区：券到期日、领券窗口按本地日期算
var CityLoc = time.FixedZone("CST", 8*3600)

// App 服务本体
type App struct {
	Pool     *pgxpool.Pool
	Pay      PayAPI
	Deposit  DepositAPI
	Verifier auth.Verifier
	Callers  auth.Callers
	Now      func() time.Time
	Loc      *time.Location
	Log      *slog.Logger
}

// New 组装
func New(pool *pgxpool.Pool, p PayAPI, d DepositAPI, v auth.Verifier, c auth.Callers, log *slog.Logger) *App {
	return &App{Pool: pool, Pay: p, Deposit: d, Verifier: v, Callers: c, Now: time.Now, Loc: CityLoc, Log: log}
}

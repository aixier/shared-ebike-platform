// Package app 是 deposit 服务的全部实现：押金账户、免押授权、押金缴纳、押金抵扣、退押申请、专户台账，
// 以及发件表、事件消费、投影、HTTP 接口与定时补发。
//
// 本模块全部是确定性代码，属于合法范围：状态机（押金状态、退押申请）、指标算术（余额与台账汇总）、
// 权限（令牌与服务凭证）、审计留痕（留痕事件）、外部系统适配（pay、信用分渠道）、事实采集（投影与去重）。
// 没有需要 LLM 判断的业务规则。
package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // goose 用 database/sql
	"github.com/pressly/goose/v3"

	"bike/deposit/internal/auth"
	"bike/deposit/internal/clients/pay"
	"bike/deposit/internal/credit"
	"bike/deposit/migrations"
)

// PayAPI 本服务用到的 pay 命令。
type PayAPI interface {
	ChargeDeposit(ctx context.Context, idemKey string, cmd pay.ChargeCommand) (pay.Result, error)
	RefundDeposit(ctx context.Context, idemKey string, cmd pay.RefundCommand) (pay.Result, error)
	TransferDeposit(ctx context.Context, idemKey string, cmd pay.TransferCommand) (pay.Result, error)
}

// Deps 依赖。
type Deps struct {
	DB                    *pgxpool.Pool
	Pay                   PayAPI
	Credit                credit.Scorer
	UserVerifier          *auth.Verifier
	AdminVerifier         *auth.Verifier
	Callers               *auth.ServiceAuth
	Loc                   *time.Location
	Now                   func() time.Time
	RefundWorkdaysDefault int
	DefaultChannel        string // 押金收款渠道缺省：微信支付｜支付宝
	Log                   *slog.Logger
}

// App 服务。
type App struct{ Deps }

// New 建服务。
func New(d Deps) *App {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Loc == nil {
		d.Loc = time.Local
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &App{Deps: d}
}

func (a *App) now() time.Time { return a.Now().In(a.Loc).Truncate(time.Second) }

// Migrate 执行内嵌迁移。
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
	return goose.Up(db, ".")
}

// inTx 在一个事务里执行 fn。
func (a *App) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, a.DB, fn)
}

// nextID 发号：前缀 + 8 位序号。
func nextID(ctx context.Context, tx pgx.Tx, seq, prefix string) (string, error) {
	var n int64
	if err := tx.QueryRow(ctx, "SELECT nextval('"+seq+"')").Scan(&n); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%08d", prefix, n), nil
}

// Actor 操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

func userActor(uid string) Actor { return Actor{Kind: "用户", ID: uid} }

var systemActor = Actor{Kind: "系统", ID: "AC0900", Name: "系统定时任务"}

// yuan 把分格式化为页面口径的元：整角显示一位小数，否则两位。
func yuan(c int64) string {
	if c%10 == 0 {
		return fmt.Sprintf("%d.%d", c/100, (c%100)/10)
	}
	return fmt.Sprintf("%d.%02d", c/100, c%100)
}

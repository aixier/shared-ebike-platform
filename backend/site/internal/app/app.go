// Package app site 服务的全部有状态实现：站点与桩、桩消息、MQTT、HTTP、事件收发、投影、定时任务。
// 用到数据库的代码都在这一个包里，测试时只起一份 embedded-postgres。
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go"
	"github.com/pressly/goose/v3"
	"github.com/robfig/cron/v3"

	"bike/site/adapters"
	"bike/site/internal/auth"
	"bike/site/internal/config"
	"bike/site/migrations"
)

// App 一个 site 进程
type App struct {
	cfg      config.Config
	db       *pgxpool.Pool
	log      *slog.Logger
	now      func() time.Time
	admin    *auth.AdminVerifier
	callers  auth.CallerVerifier
	adapters map[string]adapters.Adapter
	kick     chan struct{} // 发件表有新事件时唤醒发布循环
	broker   *broker

	ctlMu  sync.Mutex // 登记的控制器缓存
	ctlAt  time.Time
	ctlMap map[string]config.Device
}

// 系统操作人：心跳超时、设备上报等非人为变更
var systemActor = Actor{Kind: "系统", ID: "site", Name: "站点与桩"}

// Actor 操作人
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// queryer 连接池与事务的公共部分
type queryer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// OpenDB 连库并执行迁移
func OpenDB(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := Migrate(pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("迁移失败: %w", err)
	}
	return pool, nil
}

// Migrate 执行内嵌的 goose 迁移
func Migrate(pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func(db *sql.DB) { _ = db.Close() }(db)
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

// New 组装（不启动后台任务）
func New(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) (*App, error) {
	ads, err := adapters.Enabled(cfg.Adapters)
	if err != nil {
		return nil, err
	}
	return &App{
		cfg: cfg, db: pool, log: log, now: time.Now,
		admin:    auth.NewAdminVerifier(cfg.AdminKeys),
		callers:  auth.CallerVerifier{Allowed: cfg.AllowedCallers},
		adapters: ads,
		kick:     make(chan struct{}, 1),
	}, nil
}

// tx 在事务里执行
func (a *App) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, a.db, fn)
}

// Run 启动整个进程，直到 ctx 结束
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pool, err := OpenDB(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	a, err := New(cfg, pool, log)
	if err != nil {
		return err
	}

	// 事件总线：连不上不影响对外接口，事件留在发件表里等恢复后补发
	nc, err := nats.Connect(cfg.NatsURL, nats.MaxReconnects(-1), nats.RetryOnFailedConnect(true), nats.ReconnectWait(2*time.Second))
	if err != nil {
		log.Error("事件总线连接失败", "err", err)
	} else {
		defer nc.Close()
		go a.RunPublisher(ctx, nc)
		go a.RunConsumers(ctx, nc)
	}

	if cfg.AccessMQTT {
		if err := a.StartMQTT(); err != nil {
			return fmt.Errorf("MQTT 服务启动失败: %w", err)
		}
		defer a.StopMQTT()
	}

	c := cron.New()
	if _, err := c.AddFunc(cfg.OfflineScan, func() {
		if n, err := a.ScanOffline(context.Background()); err != nil {
			log.Error("心跳超时扫描失败", "err", err)
		} else if n > 0 {
			log.Info("心跳超时判离线", "piles", n)
		}
	}); err != nil {
		return fmt.Errorf("PILE_OFFLINE_SCAN: %w", err)
	}
	c.Start()
	defer c.Stop()

	srv := &http.Server{Addr: cfg.ListenAddr(), Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("site 已启动", "addr", cfg.ListenAddr(), "mqtt", cfg.AccessMQTT, "http", cfg.AccessHTTP, "adapters", cfg.Adapters)
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// fmtTime 输出 RFC 3339（本市时区）
func (a *App) fmtTime(t time.Time) string { return t.In(a.cfg.TZ).Format(time.RFC3339) }

func (a *App) fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := a.fmtTime(*t)
	return &s
}

// isUniqueViolation 唯一约束冲突
func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

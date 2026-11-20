// Package app 是 opgw（运营商通道）的全部实现。
package app

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pressly/goose/v3"
	"github.com/robfig/cron/v3"

	"bike/opgw/internal/clients/fleet"
	"bike/opgw/internal/clients/operator"
	"bike/opgw/internal/clients/ride"
	"bike/opgw/migrations"
)

// App 服务实例。
type App struct {
	Cfg   Config
	DB    *pgxpool.Pool
	JS    jetstream.JetStream // 可为空：总线不可用时事件留在发件表
	Op    *operator.Client
	Ride  *ride.Client
	Fleet *fleet.Client
	Now   func() time.Time
	Log   *slog.Logger
}

// New 组装；DB 必须已迁移。
func New(cfg Config, db *pgxpool.Pool, js jetstream.JetStream, log *slog.Logger) *App {
	if log == nil {
		log = slog.Default()
	}
	return &App{
		Cfg: cfg, DB: db, JS: js, Log: log, Now: time.Now,
		Op:    operator.New(cfg.OperatorTimeout),
		Ride:  ride.New(cfg.RideURL, cfg.ServiceKey),
		Fleet: fleet.New(cfg.FleetURL, cfg.ServiceKey),
	}
}

// Migrate 执行嵌入的迁移。
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

// Handler 路由。
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	// 运营商入口
	mux.HandleFunc("POST /open/v1/ride-events", a.operatorSigned(a.handleRideEvent))
	mux.HandleFunc("POST /open/v1/vehicle-lists", a.operatorSigned(a.handleVehicleList))
	// 内部
	mux.HandleFunc("POST /internal/opgw/unlock", a.internalOnly(a.handleUnlock))
	mux.HandleFunc("POST /internal/opgw/end-ride", a.internalOnly(a.handleEndRide))
	// 运维
	perm := []string{"credential.manage"}
	mux.HandleFunc("POST /api/opgw/credentials", a.adminOnly(true, perm, a.handleCreateCredential))
	mux.HandleFunc("GET /api/opgw/credentials", a.adminOnly(false, perm, a.handleListCredentials))
	mux.HandleFunc("GET /api/opgw/credentials/{credentialId}", a.adminOnly(false, perm, a.handleGetCredential))
	mux.HandleFunc("PATCH /api/opgw/credentials/{credentialId}", a.adminOnly(true, perm, a.handleUpdateCredential))
	mux.HandleFunc("POST /api/opgw/credentials/{credentialId}/rotate", a.adminOnly(true, perm, a.handleRotate))
	mux.HandleFunc("POST /api/opgw/credentials/{credentialId}/disable", a.adminOnly(true, perm, a.handleDisable))
	return a.recoverer(utf8Body(mux))
}

func (a *App) recoverer(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				a.Log.Error("请求处理异常", "path", r.URL.Path, "panic", v)
				serverBusy(w)
			}
		}()
		h.ServeHTTP(w, r)
	})
}

// StartJobs 在本进程里跑定时任务：发件表补发、送达 ride 重试、清理过期窗口。
func (a *App) StartJobs(ctx context.Context) (stop func()) {
	c := cron.New(cron.WithSeconds())
	_, _ = c.AddFunc("@every 1s", func() {
		if _, err := a.PublishOutbox(ctx); err != nil {
			a.Log.Warn("事件补发未完成", "err", err.Error())
		}
	})
	_, _ = c.AddFunc("@every "+a.Cfg.DeliveryRetryPeriod.String(), func() {
		if _, err := a.RetryDeliveries(ctx); err != nil {
			a.Log.Warn("送达重试未完成", "err", err.Error())
		}
	})
	_, _ = c.AddFunc("@every 1m", func() {
		// 多实例时只跑一份
		conn, err := a.DB.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		var got bool
		if conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(830302)`).Scan(&got) != nil || !got {
			return
		}
		defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(830302)`)
		if err := a.CleanupWindows(ctx); err != nil {
			a.Log.Warn("清理未完成", "err", err.Error())
		}
	})
	c.Start()
	return func() { <-c.Stop().Done() }
}

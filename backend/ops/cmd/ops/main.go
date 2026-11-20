// ops 运行值守服务的可执行入口：读配置、跑迁移、起 HTTP、发件表补发、事件消费与定时任务。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // Windows 上也能加载本市时区

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"bike/ops/internal/app"
	"bike/ops/internal/auth"
	"bike/ops/internal/clients/fleet"
	"bike/ops/internal/clients/pay"
	"bike/ops/internal/clients/ride"
	"bike/ops/internal/config"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("服务退出", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := app.Migrate(ctx, pool); err != nil {
		return err
	}
	verifier, err := auth.NewAdminVerifier(cfg.AdminJWTPubkeys)
	if err != nil {
		return err
	}
	a, err := app.New(pool, cfg,
		fleet.New(cfg.FleetURL, cfg.ServiceKey), ride.New(cfg.RideURL, cfg.ServiceKey), pay.New(cfg.PayURL, cfg.ServiceKey), verifier)
	if err != nil {
		return err
	}

	// 事件总线不可用不影响对外接口：后台重连，事件留在发件表
	if cfg.NatsURL != "" {
		nc, err := nats.Connect(cfg.NatsURL, nats.Name("ops"), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
		if err != nil {
			return err
		}
		defer nc.Close()
		js, err := jetstream.New(nc)
		if err != nil {
			return err
		}
		maxAge := time.Duration(cfg.EventRetentionHours) * time.Hour
		go func() {
			for ctx.Err() == nil {
				if err := app.EnsureStream(ctx, js, maxAge); err == nil {
					return
				}
				time.Sleep(3 * time.Second)
			}
		}()
		go a.RunPublisher(ctx, js)
		a.RunConsumers(ctx, js)
	} else {
		slog.Warn("未配置 NATS_URL，事件只写发件表")
	}
	c := a.StartCron(ctx)
	defer c.Stop()

	srv := &http.Server{Addr: cfg.ListenAddr(), Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("ops 已启动", "addr", cfg.ListenAddr())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

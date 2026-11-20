// enforce 执法核查服务入口：读配置、跑迁移、起 HTTP、发件表补发、消费 fleet 事件、定时清理。
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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"bike/enforce/internal/app"
	"bike/enforce/internal/auth"
	"bike/enforce/internal/config"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("服务退出", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	keys, err := auth.ParsePublicKeys(cfg.AdminJWTPubKeys)
	if err != nil {
		return err
	}
	callers, err := auth.ParseAllowedCallers(cfg.AllowedCallers)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.PhotoDir, 0o755); err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := app.Migrate(ctx, pool); err != nil {
		return err
	}

	a := &app.App{
		Pool: pool, Verifier: auth.NewAdminVerifier(keys), Callers: callers,
		PhotoDir: cfg.PhotoDir, OrphanTTL: cfg.OrphanPhotoTTL, StreamMaxAge: cfg.EventRetention,
		Now: time.Now, Log: log,
	}

	// 事件总线不可用不影响对外接口：事件留在发件表，连上后补发
	if cfg.NatsURL != "" {
		nc, err := nats.Connect(cfg.NatsURL, nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1), nats.Name("enforce"))
		if err != nil {
			return err
		}
		defer nc.Drain()
		js, err := jetstream.New(nc)
		if err != nil {
			return err
		}
		go a.RunPublisher(ctx, js)
		go func() { _ = a.RunConsumer(ctx, js) }()
	} else {
		log.Warn("未配置 NATS_URL，不收发事件")
	}

	jobs, err := a.StartJobs(ctx, cfg.CleanupSchedule)
	if err != nil {
		return err
	}
	defer jobs.Stop()

	srv := &http.Server{Addr: cfg.Addr(), Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		log.Info("enforce 已启动", "addr", cfg.Addr())
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

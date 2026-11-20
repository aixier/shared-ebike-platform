// onboard 接入验收服务的可执行入口。
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

	"bike/onboard/internal/app"
	"bike/onboard/internal/auth"
	"bike/onboard/internal/clients/fleet"
	"bike/onboard/internal/config"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)
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
	keys, err := auth.ParsePubKeys(cfg.AdminJWTPubKeys)
	if err != nil {
		return err
	}
	callers, err := auth.ParseCallers(cfg.AllowedCallers)
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

	a := &app.App{DB: pool, Fleet: fleet.New(cfg.FleetURL, cfg.ServiceKey), Admin: &auth.AdminVerifier{Keys: keys}, Callers: callers, Cfg: cfg.App, Log: log}
	if cfg.NATSURL != "" {
		// 总线不可用不影响对外接口：连接失败时自动重连，事件留在发件表里
		nc, err := nats.Connect(cfg.NATSURL, nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
		if err != nil {
			return err
		}
		defer nc.Close()
		js, err := jetstream.New(nc)
		if err != nil {
			return err
		}
		a.JS = js
	}
	a.Start(ctx)

	// 符合性测试一轮要等运营商回报，写超时放宽
	srv := &http.Server{Addr: cfg.Addr, Handler: a.Routes(), ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 5 * time.Minute}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("onboard 启动", "addr", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// opgw 运营商通道服务入口。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"bike/opgw/internal/app"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("opgw 退出", "err", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := app.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := app.Migrate(ctx, pool); err != nil {
		return err
	}

	// 事件总线不可用不影响对外接口：事件留在发件表，连上后补发
	var js jetstream.JetStream
	if cfg.NatsURL != "" {
		nc, err := nats.Connect(cfg.NatsURL, nats.MaxReconnects(-1), nats.RetryOnFailedConnect(true))
		if err != nil {
			log.Warn("事件总线暂不可用", "err", err.Error())
		} else {
			defer nc.Close()
			js, _ = jetstream.New(nc)
		}
	}

	a := app.New(cfg, pool, js, log)
	if js != nil {
		if err := app.EnsureStream(ctx, js, cfg.EventRetentionDays); err != nil {
			log.Warn("建流未完成，稍后发布时重试", "err", err.Error())
		}
		go a.RunConsumers(ctx)
	}
	if err := a.SyncHolderSettings(ctx); err != nil {
		return err
	}
	stopJobs := a.StartJobs(ctx)
	defer stopJobs()

	srv := &http.Server{
		Addr:              net.JoinHostPort(cfg.ListenHost, strconv.Itoa(cfg.Port)),
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("opgw 已启动", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

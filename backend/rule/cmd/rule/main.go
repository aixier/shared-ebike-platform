// rule 规则配置服务的可执行入口：读配置、跑迁移、（可选）载入原型样例、起事件发布循环与 HTTP 服务。
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

	"bike/rule/internal/auth"
	"bike/rule/internal/config"
	"bike/rule/internal/service"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := service.Migrate(ctx, cfg.DBDSN); err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	if cfg.Seed == "prototype" {
		if err := service.SeedIfEmpty(ctx, pool, time.Now()); err != nil {
			return err
		}
	}
	if len(cfg.AdminKeys) == 0 {
		log.Warn("未配置 ADMIN_JWT_PUBKEYS，页面接口将一律返回未登录")
	}

	svc := &service.Service{Pool: pool, Now: time.Now, Admin: &auth.AdminVerifier{Keys: cfg.AdminKeys}, Callers: cfg.Callers, Log: log}

	// 事件总线：连不上也照常提供接口，事件留在发件表，恢复后补发。
	if cfg.NatsURL != "" {
		nc, err := nats.Connect(cfg.NatsURL, nats.Name("rule"), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
		if err != nil {
			return err
		}
		defer nc.Close()
		relay, err := service.NewRelay(pool, nc, cfg.EventRetention, log)
		if err != nil {
			return err
		}
		svc.Notify = relay.Wake
		go relay.Run(ctx)
		// 订阅 fleet 的品牌事件建品牌投影；有投影来源时才校验版本里引用的品牌。
		svc.CheckBrands = true
		go (&service.Consumer{Pool: pool, NC: nc, Log: log}).Run(ctx)
	} else {
		log.Warn("未配置 NATS_URL，事件只写入发件表，不发布")
	}

	srv := &http.Server{Addr: cfg.Addr(), Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Info("rule 服务启动", "addr", cfg.Addr())
		errc <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

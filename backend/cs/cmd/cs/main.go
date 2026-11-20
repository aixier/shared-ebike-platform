// cs 客服服务的可执行入口：读配置、跑迁移、连数据库与事件总线、起定时任务与 HTTP。
// trace: backend/cs/README.md「运行」；backend/技术约定.md
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

	"github.com/nats-io/nats.go"
	"github.com/robfig/cron/v3"

	"bike/cs/internal/clients/acct"
	"bike/cs/internal/clients/bill"
	"bike/cs/internal/clients/deposit"
	"bike/cs/internal/clients/ride"
	"bike/cs/internal/config"
	"bike/cs/internal/core"
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

	if err := core.Migrate(cfg.DBDSN); err != nil {
		return err
	}
	pool, err := core.OpenPool(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	app := &core.App{
		DB: pool, Ride: ride.New(cfg.RideURL, cfg.ServiceKey), Bill: bill.New(cfg.BillURL, cfg.ServiceKey),
		Acct: acct.New(cfg.AcctURL, cfg.ServiceKey), Deposit: deposit.New(cfg.DepositURL, cfg.ServiceKey),
		UserKeys: cfg.UserKeys, AdminKeys: cfg.AdminKeys, Callers: cfg.Callers,
		DefaultRefundLimit: cfg.DefaultRefundLimit, EventRetentionDays: cfg.EventRetentionDays,
		// Agent 为空：客服智能体运行时与模型未定，用户消息进"待处理"等客服接手
	}

	sched := cron.New()
	if cfg.NatsURL != "" {
		// 总线不可用时照常对外服务，事件留在发件表，连上后补发
		nc, err := nats.Connect(cfg.NatsURL, nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1), nats.Name("cs"))
		if err != nil {
			return err
		}
		defer nc.Close()
		bus, err := core.NewBus(app, nc)
		if err != nil {
			return err
		}
		defer bus.Stop()
		if err := bus.Schedule(sched); err != nil {
			return err
		}
	} else {
		slog.Warn("未配置 NATS_URL，事件只写发件表不发送")
	}
	sched.Start()
	defer sched.Stop()

	srv := &http.Server{Addr: cfg.Addr(), Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("cs 已启动", "addr", cfg.Addr())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

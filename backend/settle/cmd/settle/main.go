// settle 结算服务的可执行入口：读配置、跑迁移、起 HTTP、事件收发与定时任务。
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

	"bike/settle/internal/app"
	"bike/settle/internal/auth"
	"bike/settle/internal/config"
	"bike/settle/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("settle 退出", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(nil)
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
	if err := app.Migrate(ctx, pool, migrations.FS); err != nil {
		return err
	}
	keys, err := auth.ParseKeys(cfg.AdminJWTPubKeys)
	if err != nil {
		return err
	}
	a := app.New(pool, auth.NewVerifier(keys), log, nil)

	if cfg.NatsURL != "" {
		bus, err := app.ConnectBus(cfg.NatsURL)
		if err != nil {
			// 总线不可用不影响对外接口：事件留在发件表，恢复后补发
			log.Warn("事件总线暂不可用", "err", err)
		} else {
			defer bus.NC.Close()
			if err := bus.EnsureStream(ctx, time.Duration(cfg.EventRetentionDays)*24*time.Hour); err != nil {
				log.Warn("确保事件流失败", "err", err)
			}
			go a.RunPublisher(ctx, bus, time.Second)
			a.RunConsumers(ctx, bus)
		}
	}
	if cfg.AutoSettle {
		c, err := a.StartScheduler(ctx)
		if err != nil {
			return err
		}
		defer c.Stop()
	}

	srv := &http.Server{Addr: cfg.Addr(), Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("settle 已启动", "addr", cfg.Addr())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

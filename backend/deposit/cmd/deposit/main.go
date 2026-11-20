// deposit 押金与免押服务的可执行入口：读配置、迁移、连事件总线、起 HTTP、跑发布者、消费者与定时补发。
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
	"github.com/robfig/cron/v3"

	"bike/deposit/internal/app"
	"bike/deposit/internal/auth"
	"bike/deposit/internal/clients/pay"
	"bike/deposit/internal/config"
	"bike/deposit/internal/credit"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("deposit 退出", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	userKeys, err := auth.ParseKeys(cfg.UserJWTPubKeys)
	if err != nil {
		return err
	}
	adminKeys, err := auth.ParseKeys(cfg.AdminJWTPubKey)
	if err != nil {
		return err
	}
	callers, err := auth.ParseCallers(cfg.AllowedCallers)
	if err != nil {
		return err
	}
	var scorer credit.Scorer = credit.Unavailable{}
	if cfg.CreditScorer == "mock" {
		m, err := credit.NewMock(cfg.CreditMockScores, cfg.CreditMockDefault)
		if err != nil {
			return err
		}
		scorer = m
		log.Warn("信用分使用联调模拟实现（CREDIT_SCORER=mock），真实渠道尚未接入")
	}

	if err := app.Migrate(cfg.DBDSN); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	a := app.New(app.Deps{
		DB: pool, Pay: pay.New(cfg.PayURL, cfg.ServiceKey), Credit: scorer,
		UserVerifier: auth.NewVerifier(userKeys), AdminVerifier: auth.NewVerifier(adminKeys), Callers: callers,
		Loc: cfg.Location(), RefundWorkdaysDefault: cfg.RefundWorkdaysDefault, DefaultChannel: cfg.DefaultCollectChannel, Log: log,
	})

	// 事件总线：不可用时照常对外服务，事件留在发件表，连上后补发
	if cfg.NatsURL != "" {
		nc, err := nats.Connect(cfg.NatsURL, nats.MaxReconnects(-1), nats.RetryOnFailedConnect(true))
		if err != nil {
			return err
		}
		defer nc.Close()
		js, err := jetstream.New(nc)
		if err != nil {
			return err
		}
		go func() {
			for ctx.Err() == nil {
				if err := app.EnsureStream(ctx, js, time.Duration(cfg.StreamMaxAgeDays)*24*time.Hour); err == nil {
					break
				} else {
					log.Warn("建流失败，稍后重试", "err", err)
				}
				time.Sleep(5 * time.Second)
			}
			go a.RunConsumers(ctx, js, 5*time.Second)
			a.RunPublisher(ctx, js, time.Second)
		}()
	} else {
		log.Warn("未配置 NATS_URL，事件只写发件表")
	}

	c := cron.New()
	_, _ = c.AddFunc("@every 1m", func() { a.RetryPending(ctx) })
	c.Start()
	defer c.Stop()

	srv := &http.Server{Addr: cfg.Addr(), Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()
	log.Info("deposit 启动", "addr", cfg.Addr())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

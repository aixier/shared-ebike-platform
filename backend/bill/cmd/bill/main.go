// bill 计费与权益服务的可执行入口。
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"bike/bill/internal/app"
	"bike/bill/internal/auth"
	"bike/bill/internal/clients/deposit"
	"bike/bill/internal/clients/pay"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := app.LoadConfig(nil)
	if err != nil {
		log.Error("配置有误", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := app.OpenDB(ctx, cfg.DBDSN)
	if err != nil {
		log.Error("数据库不可用", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	a := app.New(pool, pay.New(cfg.PayURL, cfg.ServiceKey), deposit.New(cfg.DepositURL, cfg.ServiceKey),
		auth.Verifier{UserKeys: cfg.UserKeys, AdminKeys: cfg.AdminKeys}, cfg.Callers, log)

	// 事件总线：连不上不影响对外接口，事件留在发件表里
	if cfg.NatsURL != "" {
		nc, err := nats.Connect(cfg.NatsURL, nats.MaxReconnects(-1), nats.RetryOnFailedConnect(true))
		if err != nil {
			log.Warn("事件总线连接失败", "err", err)
		} else {
			defer nc.Close()
			js, _ := jetstream.New(nc)
			go func() {
				for ctx.Err() == nil {
					if err := app.EnsureStream(ctx, js, cfg.EventRetention()); err == nil {
						break
					}
					time.Sleep(2 * time.Second)
				}
				go (&app.Publisher{Pool: pool, JS: js, Log: log}).Run(ctx)
			}()
			for _, s := range app.Subscriptions {
				go app.Consume(ctx, js, pool, s, a.HandleEvent, log)
			}
		}
	}
	jobs := a.StartJobs(ctx)
	defer jobs.Stop()

	srv := &http.Server{Addr: cfg.Addr(), Handler: a.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()
	log.Info("bill 已启动", "addr", cfg.Addr())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("监听失败", "err", err)
		os.Exit(1)
	}
}

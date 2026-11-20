// ride 骑行服务可执行入口。
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

	"bike/ride/internal/auth"
	"bike/ride/internal/clients/opgw"
	"bike/ride/internal/config"
	"bike/ride/internal/core"
	"bike/ride/internal/journal"
	"bike/ride/internal/rules"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("启动失败", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	userKeys, err := auth.ParseKeys(cfg.UserJWTPubKeys)
	if err != nil {
		return err
	}
	adminKeys, err := auth.ParseKeys(cfg.AdminJWTPubKeys)
	if err != nil {
		return err
	}
	verifier := &auth.Verifier{UserKeys: userKeys, AdminKeys: adminKeys, Callers: auth.ParseCallers(cfg.AllowedCallers)}

	poolCfg, err := pgxpool.ParseConfig(cfg.DBDSN)
	if err != nil {
		return err
	}
	poolCfg.ConnConfig.ConnectTimeout = 2 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	// 启动时须能迁移；运行中数据库不可用由熔断与本地日志兜底
	if err := core.Migrate(ctx, pool); err != nil {
		return err
	}
	j, err := journal.Open(cfg.JournalDir)
	if err != nil {
		return err
	}
	snap := core.NewSnapshot()
	svc := core.New(pool, snap, j, opgw.New(cfg.OpgwURL, cfg.ServiceKey), core.Options{
		InstanceID: cfg.InstanceID, InstanceNo: cfg.InstanceNo, PileNoLockWait: cfg.PileNoLockWait,
		ReturnWait: cfg.ReturnWait, SnapshotRefresh: cfg.SnapshotRefresh, EventRetention: cfg.EventRetention,
	}, log)
	probes := map[string]core.Probe{rules.CompDB: func(c context.Context) error { return pool.Ping(c) }}
	for comp, u := range map[string]string{rules.CompBill: cfg.BillURL, rules.CompPay: cfg.PayURL, rules.CompAcct: cfg.AcctURL, rules.CompDeposit: cfg.DepositURL} {
		if u != "" {
			probes[comp] = core.HTTPProbe(u)
		}
	}
	svc.SetBreakers(probes)
	if err := svc.LoadSnapshot(ctx); err != nil {
		return err
	}

	var js jetstream.JetStream
	if cfg.NATSURL != "" {
		nc, err := nats.Connect(cfg.NATSURL, nats.MaxReconnects(-1), nats.RetryOnFailedConnect(true))
		if err != nil {
			log.Warn("事件总线暂不可用，事件留在发件表", "err", err)
		} else {
			defer nc.Close()
			js, _ = jetstream.New(nc)
			svc.SetJetStream(js)
			go func() {
				for ctx.Err() == nil {
					if err := core.EnsureStream(ctx, js, cfg.EventRetention); err == nil {
						return
					}
					time.Sleep(5 * time.Second)
				}
			}()
			svc.StartConsumers(ctx, js)
		}
	}
	svc.RunBackground(ctx, js)

	srv := &http.Server{Addr: cfg.Addr(), Handler: svc.Handler(verifier), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("ride 已启动", "addr", cfg.Addr(), "instance", cfg.InstanceID)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

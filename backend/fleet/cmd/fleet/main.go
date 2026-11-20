// fleet 服务入口：读环境变量 → 连库并迁移 → 装入登记数据（可选）→ 连事件总线并启动发件表补发 → 起 HTTP。
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
	"github.com/nats-io/nats.go/jetstream"

	"bike/fleet/internal/auth"
	"bike/fleet/internal/config"
	"bike/fleet/internal/db"
	"bike/fleet/internal/fleet"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("fleet 退出", "err", err)
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

	pool, err := db.Open(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	systemActor := fleet.Actor{Kind: "系统", ID: or(os.Getenv("SYSTEM_ACTOR_ID"), "AC0900"), Name: "系统定时任务"}
	svc := fleet.NewService(pool, time.Now, systemActor)
	svc.SetReadonlyDays(cfg.ExitReadonlyDays)
	if cfg.SeedFile != "" {
		data, err := os.ReadFile(cfg.SeedFile)
		if err != nil {
			return err
		}
		n, err := svc.LoadSeed(ctx, data)
		if err != nil {
			return err
		}
		slog.Info("登记数据已装入", "新增", n)
	}

	// 总线不可用不影响启动与接口，事件留在发件表里，恢复后补发
	nc, err := nats.Connect(cfg.NatsURL, nats.Name("fleet"), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	relay := fleet.NewRelay(pool, js, time.Duration(cfg.EventRetentionDays)*24*time.Hour)
	c := relay.StartCron(ctx)
	defer c.Stop()

	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           fleet.NewHandler(svc, auth.NewAdminVerifier(cfg.AdminKeys), cfg.Callers),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("fleet 已启动", "addr", cfg.Addr())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func or(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

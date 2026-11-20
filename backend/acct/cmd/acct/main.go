// acct 用户账户服务的可执行入口：读配置、迁移、起 HTTP、事件收发与定时任务。
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

	"bike/acct/internal/app"
	"bike/acct/internal/config"
	"bike/acct/internal/external"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("服务退出", "err", err)
		os.Exit(1)
	}
}

// externals 按 ACCT_EXTERNAL_MODE 选择授权与实名渠道
func externals(cfg *config.Config) (external.Authorizer, external.RealNameVerifier) {
	if cfg.ExternalMode == "real" {
		return external.ChannelRouter{
			"微信":  &external.WeChatAuthorizer{AppID: cfg.WeChatAppID, Secret: cfg.WeChatSecret},
			"支付宝": &external.AlipayAuthorizer{AESKey: cfg.AlipayAESKey},
		}, external.RealRealName{}
	}
	auth := &external.MockAuthorizer{}
	auth.SetDown(cfg.MockAuthDown)
	rn := external.NewMockRealName(cfg.MockRealNameRegistry, cfg.MockMismatchNames)
	rn.SetDown(cfg.MockRealNameDown)
	return auth, rn
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Migrate(ctx, cfg.DBDSN); err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	auth, rn := externals(cfg)
	a := app.New(app.Deps{Pool: pool, Cfg: cfg, Auth: auth, RealName: rn, Log: log})

	// 事件总线不可用时照常提供接口，事件留在发件表里，连上后补发
	nc, err := nats.Connect(cfg.NATSURL, nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1), nats.Name("acct"))
	if err != nil {
		log.Warn("事件总线连接失败，稍后重连", "err", err)
	} else {
		defer nc.Close()
		js, err := jetstream.New(nc)
		if err != nil {
			return err
		}
		go a.RunPublisher(ctx, js)
		go a.RunConsumers(ctx, js)
	}
	go a.RunScheduler(ctx)

	srv := &http.Server{Addr: cfg.Addr(), Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Info("acct 已启动", "addr", cfg.Addr(), "externalMode", cfg.ExternalMode)
		errc <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	a.WaitBackground()
	return nil
}

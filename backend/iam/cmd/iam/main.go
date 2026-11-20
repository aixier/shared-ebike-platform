// iam 账号与留痕服务的可执行入口。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"bike/iam/internal/config"
	"bike/iam/internal/iam"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.FromEnv(nil)
	if err != nil {
		log.Error("配置有误", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := iam.Start(ctx, cfg, log)
	if err != nil {
		log.Error("启动失败", "err", err)
		os.Exit(1)
	}
	<-ctx.Done()
	log.Info("正在退出")
	app.Close()
}

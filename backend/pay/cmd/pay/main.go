// pay 收付款服务入口。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"bike/pay/internal/app"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := app.ConfigFromEnv()
	if err != nil {
		log.Error("配置有误", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := app.New(ctx, cfg, log)
	if err != nil {
		log.Error("启动失败", "err", err)
		os.Exit(1)
	}
	defer a.Pool.Close()
	if err := a.Run(ctx, cfg); err != nil {
		log.Error("运行出错", "err", err)
		os.Exit(1)
	}
}

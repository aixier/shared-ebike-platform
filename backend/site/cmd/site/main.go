// site 站点与桩服务的可执行入口。配置全部来自环境变量（见 README.md 运行一节）。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"bike/site/internal/app"
	"bike/site/internal/config"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Error("配置有误", "err", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg, log); err != nil {
		log.Error("site 退出", "err", err)
		os.Exit(1)
	}
}

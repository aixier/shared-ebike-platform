package iam

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // goose 用 database/sql
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/robfig/cron/v3"
	"golang.org/x/crypto/bcrypt"

	"bike/iam/internal/bus"
	"bike/iam/internal/config"
	"bike/iam/internal/token"
	"bike/iam/migrations"
)

// 定时清理任务的数据库锁编号（多实例只跑一份）
const cleanupLockKey = 1831501

// Migrate 执行全部迁移；多实例同时启动时用会话锁串行。
func Migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

// App 是运行中的整个服务进程。
type App struct {
	svc    *Service
	srv    *http.Server
	nc     *nats.Conn
	cron   *cron.Cron
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Start 跑迁移、开监听、连事件总线、起发件与订阅、起定时任务。
// 事件总线不可用或别的服务的流还不存在都不影响启动。
func Start(parent context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	if err := Migrate(parent, cfg.DBDSN); err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(parent, cfg.DBDSN)
	if err != nil {
		return nil, err
	}
	var initHash string
	if cfg.InitialPwd != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(cfg.InitialPwd), bcrypt.DefaultCost)
		if err != nil {
			pool.Close()
			return nil, err
		}
		initHash = string(h)
	}
	svc := NewService(Options{
		DB: pool, Signer: token.NewSigner(cfg.Kid, cfg.PrivKey, cfg.TokenTTL), ExtraPubKeys: cfg.PubKeys,
		AllowedCallers: cfg.AllowedCallers, InitialPwdHash: initHash, Location: cfg.Location, Log: log,
	})
	if err := svc.SeedDevAdmin(parent, cfg.DevSeedAdmin); err != nil {
		pool.Close()
		return nil, err
	}
	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		pool.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	a := &App{svc: svc, cancel: cancel, srv: &http.Server{Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second}}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		if err := a.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP 服务退出", "err", err)
		}
	}()

	nc, err := nats.Connect(cfg.NATSURL, nats.Name("iam"), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1), nats.ReconnectWait(2*time.Second))
	if err != nil {
		log.Warn("事件总线暂不可用，稍后重连", "err", err)
	}
	a.nc = nc
	if nc != nil {
		js, err := jetstream.New(nc)
		if err == nil {
			a.wg.Add(2)
			go func() { defer a.wg.Done(); a.relayLoop(ctx, js, cfg, log) }()
			go func() { defer a.wg.Done(); a.subscribe(ctx, js, cfg, log) }()
		}
	}

	a.cron = cron.New(cron.WithLocation(cfg.Location))
	_, _ = a.cron.AddFunc("30 3 * * *", func() { a.runCleanup(ctx, log) })
	a.cron.Start()
	log.Info("iam 已启动", "addr", cfg.Addr())
	return a, nil
}

// relayLoop 确保本服务的流存在，并定时把发件表里的事件发出去。
func (a *App) relayLoop(ctx context.Context, js jetstream.JetStream, cfg config.Config, log *slog.Logger) {
	ready := false
	t := time.NewTicker(cfg.OutboxInterval)
	defer t.Stop()
	for {
		if !ready {
			c, cancel := context.WithTimeout(ctx, 3*time.Second)
			if err := bus.EnsureStream(c, js, "EVT_IAM", []string{"iam.>"}, cfg.StreamMaxAge); err == nil {
				ready = true
			} else if ctx.Err() == nil {
				log.Debug("暂不能建流 EVT_IAM", "err", err)
			}
			cancel()
		}
		if ready {
			_, err := a.svc.RelayOnce(ctx, func(c context.Context, subj, id string, body []byte) error {
				c, cancel := context.WithTimeout(c, 3*time.Second)
				defer cancel()
				return bus.Publish(c, js, subj, id, body)
			})
			if err != nil && ctx.Err() == nil {
				log.Debug("发件暂未完成，稍后补发", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// subscribe 订阅全部模块的留痕事件与 fleet 的运营商已退出。
func (a *App) subscribe(ctx context.Context, js jetstream.JetStream, cfg config.Config, log *slog.Logger) {
	var subs []Subscription
	for _, m := range Modules {
		filters := []string{m + ".audit.recorded"}
		if m == "fleet" {
			filters = append(filters, "fleet.operator.exited")
		}
		subs = append(subs, Subscription{Stream: "EVT_" + strings.ToUpper(m), Filters: filters})
	}
	bus.NewSubscriber(js, "iam", cfg.EventRetry, a.svc.HandleEvent, log).Run(ctx, subs)
}

// Subscription 是 bus.Subscription 的别名，便于在本包内列订阅。
type Subscription = bus.Subscription

func (a *App) runCleanup(ctx context.Context, log *slog.Logger) {
	conn, err := a.svc.db.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, cleanupLockKey).Scan(&got); err != nil || !got {
		return
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, cleanupLockKey)
	if err := a.svc.Cleanup(ctx); err != nil {
		log.Warn("清理过期会话失败", "err", err)
	}
}

// Close 停止全部后台任务并关闭连接。
func (a *App) Close() {
	a.cancel()
	if a.cron != nil {
		<-a.cron.Stop().Done()
	}
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = a.srv.Shutdown(c)
	cancel()
	if a.nc != nil {
		a.nc.Close()
	}
	a.wg.Wait()
	a.svc.db.Close()
}

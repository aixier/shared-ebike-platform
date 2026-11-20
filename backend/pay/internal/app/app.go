// Package app 是 pay 收付款服务的实现：流水、命令、对账、接口、定时任务、事件发布。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"bike/pay/internal/auth"
	"bike/pay/internal/channel"
	"bike/pay/internal/clients/acct"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/robfig/cron/v3"
)

// App 服务
type App struct {
	Pool     *pgxpool.Pool
	Ch       channel.Router
	Acct     PayeeChecker
	Verifier *auth.Verifier
	Callers  auth.Callers
	Now      func() time.Time
	Log      *slog.Logger

	IntentTTL         time.Duration // 一键支付调起参数有效期（PAY_INTENT_TTL）
	MockCallbackDelay time.Duration // 模拟渠道自动回调延迟（PAY_MOCK_CALLBACK_DELAY）
}

// BuildChannels 按 PAY_CHANNEL_MODE 选渠道实现
func BuildChannels(cfg Config, pool *pgxpool.Pool, now func() time.Time) (channel.Router, error) {
	if cfg.ChannelMode == "mock" {
		mc, err := channel.ParseMockConfig(cfg.MockDefault, cfg.MockUsers, cfg.MockResolve)
		if err != nil {
			return nil, err
		}
		st := &PGMockStore{Pool: pool}
		r := channel.Router{}
		for _, n := range []string{channel.WeChat, channel.Alipay} {
			m := channel.NewMock(n, mc, st)
			m.Now = now
			r[n] = m
		}
		return r, nil
	}
	m := func(prefix string) channel.WeChatMerchant {
		return channel.WeChatMerchant{
			MchID: cfg.WeChat[prefix+"MCHID"], CertSerialNo: cfg.WeChat[prefix+"CERT_SERIAL"], PrivateKeyPath: cfg.WeChat[prefix+"PRIVATE_KEY_PATH"],
			APIv3Key: cfg.WeChat[prefix+"APIV3_KEY"], AppID: cfg.WeChat[prefix+"APPID"], NotifyURL: cfg.WeChat[prefix+"NOTIFY_URL"],
		}
	}
	wc := channel.WeChatConfig{Income: m("PAY_WECHAT_INCOME_"), Deposit: m("PAY_WECHAT_DEPOSIT_")}
	wc.Income.PayScoreID = cfg.WeChat["PAY_WECHAT_PAYSCORE_SERVICE_ID"]
	wx, err := channel.NewWeChatPay(wc)
	if err != nil {
		return nil, fmt.Errorf("真实渠道模式下微信支付不可用：%w", err)
	}
	return channel.Router{channel.WeChat: wx, channel.Alipay: channel.Unavailable{Name: "支付宝"}}, nil
}

// New 由配置装配
func New(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	if err := Migrate(cfg.DBDSN); err != nil {
		return nil, err
	}
	pool, err := OpenPool(ctx, cfg.DBDSN)
	if err != nil {
		return nil, err
	}
	uk, err := auth.ParseKeys(cfg.UserPubKeys)
	if err != nil {
		return nil, err
	}
	ak, err := auth.ParseKeys(cfg.AdminPubKeys)
	if err != nil {
		return nil, err
	}
	callers, err := auth.ParseCallers(cfg.AllowedCallers)
	if err != nil {
		return nil, err
	}
	a := &App{Pool: pool, Verifier: &auth.Verifier{UserKeys: uk, AdminKeys: ak}, Callers: callers, Now: time.Now, Log: log,
		Acct: &acct.Client{BaseURL: cfg.AcctURL, ServiceKey: cfg.ServiceKey}, IntentTTL: cfg.IntentTTL, MockCallbackDelay: cfg.MockCallbackDelay}
	if a.Ch, err = BuildChannels(cfg, pool, a.Now); err != nil {
		pool.Close()
		return nil, err
	}
	return a, nil
}

// Run 起 HTTP、事件发布循环与定时任务，直到 ctx 结束
func (a *App) Run(ctx context.Context, cfg Config) error {
	go a.publishLoop(ctx, cfg)
	if cfg.ChannelMode == "mock" {
		go a.mockCallbackLoop(ctx)
	}
	c := cron.New(cron.WithLocation(bj))
	c.AddFunc("@every 30s", func() {
		a.withJobLock(ctx, jobLockResolve, func(ctx context.Context) error {
			_, err := a.ResolvePending(ctx, 30*time.Second)
			if err != nil {
				a.Log.Warn("查询补齐失败", "err", err)
			}
			return err
		})
	})
	reconJob := func() {
		a.withJobLock(ctx, jobLockRecon, func(ctx context.Context) error {
			err := a.GenerateDue(ctx)
			if err != nil {
				a.Log.Warn("出具日对账失败", "err", err)
			}
			return err
		})
	}
	c.AddFunc("10 2 * * *", reconJob)
	c.Start()
	defer c.Stop()
	go reconJob() // 启动时补出漏掉的日对账

	addr := net.JoinHostPort(cfg.ListenHost, strconv.Itoa(cfg.Port))
	srv := &http.Server{Addr: addr, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	a.Log.Info("pay 已启动", "addr", addr, "channelMode", cfg.ChannelMode)
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// mockCallbackLoop 模拟渠道的自动回调：每秒看一次有没有到点的待支付流水
func (a *App) mockCallbackLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.withJobLock(ctx, jobLockCallback, func(ctx context.Context) error {
			if _, err := a.RunMockCallbacks(ctx); err != nil {
				a.Log.Warn("模拟渠道回调失败", "err", err)
				return err
			}
			return nil
		})
	}
}

// publishLoop 连事件总线并持续补发发件表；总线不可用不影响接口
func (a *App) publishLoop(ctx context.Context, cfg Config) {
	nc, err := nats.Connect(cfg.NatsURL, nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1), nats.Name("pay"))
	if err != nil {
		a.Log.Error("连接事件总线失败", "err", err)
		return
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		a.Log.Error("初始化 JetStream 失败", "err", err)
		return
	}
	ensured := false
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !nc.IsConnected() {
			continue
		}
		if !ensured {
			if err := EnsureStream(ctx, js, cfg.StreamMaxAge); err != nil {
				a.Log.Warn("确保事件流失败", "err", err)
				continue
			}
			ensured = true
		}
		a.withJobLock(ctx, jobLockPublisher, func(ctx context.Context) error {
			_, err := a.PublishPending(ctx, js)
			if err != nil {
				a.Log.Warn("发布事件失败，稍后重试", "err", err)
			}
			return nil
		})
	}
}

// PGMockStore 模拟渠道的交易存在本服务库里（只在 mock 模式用）
type PGMockStore struct{ Pool *pgxpool.Pool }

func (s *PGMockStore) Get(ctx context.Context, ch, out string) (*channel.Trade, error) {
	t := &channel.Trade{}
	var kind, mer, st, beh string
	var user, trade, fr, settled *string
	var at *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT channel, out_no, kind, merchant, user_id, trade_no, direction, amount, status, fail_reason, behavior, completed_at, settled_date::text
		FROM mock_channel_trades WHERE channel=$1 AND out_no=$2`, ch, out).Scan(&t.Channel, &t.OutNo, &kind, &mer, &user, &trade, &t.Direction, &t.Amount, &st, &fr, &beh, &at, &settled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Kind, t.Merchant, t.Status, t.Behavior = channel.QueryKind(kind), channel.Merchant(mer), channel.Outcome(st), channel.Behavior(beh)
	t.UserID, t.TradeNo, t.FailReason, t.SettledDate = deref(user), deref(trade), deref(fr), deref(settled)
	if at != nil {
		t.CompletedAt = *at
	}
	return t, nil
}

func (s *PGMockStore) Put(ctx context.Context, t channel.Trade) error {
	var at *time.Time
	if !t.CompletedAt.IsZero() {
		at = &t.CompletedAt
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO mock_channel_trades (channel, out_no, kind, merchant, user_id, trade_no, direction, amount, status, fail_reason, behavior, completed_at, settled_date)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::date)
		ON CONFLICT (channel, out_no) DO UPDATE SET status=EXCLUDED.status, fail_reason=EXCLUDED.fail_reason, trade_no=EXCLUDED.trade_no,
		completed_at=EXCLUDED.completed_at, settled_date=EXCLUDED.settled_date`,
		t.Channel, t.OutNo, string(t.Kind), string(t.Merchant), strPtr(t.UserID), strPtr(t.TradeNo), t.Direction, t.Amount, string(t.Status),
		strPtr(t.FailReason), string(t.Behavior), at, strPtr(t.SettledDate))
	return err
}

func (s *PGMockStore) Settled(ctx context.Context, ch string, m channel.Merchant, date string) ([]channel.Trade, error) {
	rows, err := s.Pool.Query(ctx, `SELECT out_no FROM mock_channel_trades WHERE channel=$1 AND merchant=$2 AND status='成功' AND settled_date=$3::date ORDER BY completed_at`, ch, string(m), date)
	if err != nil {
		return nil, err
	}
	var outs []string
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			rows.Close()
			return nil, err
		}
		outs = append(outs, o)
	}
	rows.Close()
	var ts []channel.Trade
	for _, o := range outs {
		t, err := s.Get(ctx, ch, o)
		if err != nil {
			return nil, err
		}
		ts = append(ts, *t)
	}
	return ts, nil
}

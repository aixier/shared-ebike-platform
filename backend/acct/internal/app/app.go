// Package app acct 用户账户服务的全部实现：HTTP 接口、账户状态、事件收发与定时任务。
// 依赖数据库的代码都在本包内（测试共用一个嵌入式数据库）。
package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // goose 用 database/sql
	"github.com/pressly/goose/v3"

	"bike/acct/internal/config"
	"bike/acct/internal/external"
	"bike/acct/internal/token"
	"bike/acct/migrations"
)

// 系统定时任务账号（原型 AC0900）
const systemActorID = "AC0900"

var cst = time.FixedZone("CST", 8*3600)

// Deps 外部依赖
type Deps struct {
	Pool     *pgxpool.Pool
	Cfg      *config.Config
	Auth     external.Authorizer
	RealName external.RealNameVerifier
	Now      func() time.Time
	Log      *slog.Logger
}

// App 服务实例
type App struct {
	Deps
	issuer *token.Issuer
	kick   chan struct{} // 有新事件写入发件表时提醒发布
	bg     sync.WaitGroup
}

// New 组装服务
func New(d Deps) *App {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &App{
		Deps:   d,
		issuer: &token.Issuer{Kid: d.Cfg.UserKid, Key: d.Cfg.UserKey, TTL: d.Cfg.TokenTTL},
		kick:   make(chan struct{}, 1),
	}
}

// Migrate 执行内嵌的 SQL 迁移
func Migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("迁移失败: %w", err)
	}
	return nil
}

// WaitBackground 等后台补验等异步任务做完（测试用，也用于退出时收尾）
func (a *App) WaitBackground() { a.bg.Wait() }

func (a *App) goBackground(f func(ctx context.Context)) {
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		f(ctx)
	}()
}

func (a *App) notifyOutbox() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// Handler 全部路由
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })

	// 用户端
	mux.HandleFunc("POST /api/acct/authorize", a.idempotent("authorize", func(r *http.Request) string { return "anon" }, a.handleAuthorize))
	mux.HandleFunc("GET /api/acct/me", a.userAuth(true, a.handleMe))
	mux.HandleFunc("POST /api/acct/logout", a.handleLogout)
	mux.HandleFunc("POST /api/acct/real-name", a.userAuth(false, a.idempotentUser("real-name", a.handleVerifyRealName)))
	mux.HandleFunc("GET /api/acct/account", a.userAuth(false, a.handleAccount))
	mux.HandleFunc("GET /api/acct/account/cancel-check", a.userAuth(false, a.handleCancelCheck))
	mux.HandleFunc("POST /api/acct/account/cancel", a.userAuth(false, a.idempotentUser("cancel", a.handleCancel)))
	mux.HandleFunc("GET /api/acct/notices", a.userAuth(false, a.handleNotices))
	mux.HandleFunc("POST /api/acct/notices/read", a.userAuth(false, a.idempotentUser("notices-read", a.handleMarkRead)))

	// 客服（管理令牌）
	mux.HandleFunc("GET /api/acct/admin/frozen-accounts", a.adminAuth([]string{"account.unfreeze", "user.view"}, a.handleFrozenList))
	mux.HandleFunc("GET /api/acct/admin/users/{userId}", a.adminAuth([]string{"user.view"}, a.handleCsUser))

	// 内部
	mux.HandleFunc("GET /internal/acct/users/lookup", a.internalAuth(false, a.handleLookup))
	mux.HandleFunc("GET /internal/acct/users/{userId}", a.internalAuth(false, a.handleInternalUser))
	mux.HandleFunc("POST /internal/acct/users/{userId}/unfreeze", a.internalAuth(true, a.handleInternalUnfreeze))
	mux.HandleFunc("POST /internal/acct/users/{userId}/cancel", a.internalAuth(true, a.handleInternalCancel))
	mux.HandleFunc("GET /internal/acct/token-keys", a.internalAuth(false, a.handleTokenKeys))
	mux.HandleFunc("POST /internal/acct/users/{userId}/payee-name-check", a.internalAuth(false, a.handlePayeeCheck))
	return mux
}

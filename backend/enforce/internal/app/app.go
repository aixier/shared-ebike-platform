// Package app 是 enforce 服务的全部实现：接口、投影、发件表、事件消费、定时任务。
// 依赖数据库的代码都放在本包，测试库只需起一个（端口 19314）。
package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"
	_ "time/tzdata" // Windows 等无时区库的环境也能载入 Asia/Shanghai

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"bike/enforce/internal/auth"
	"bike/enforce/migrations"
)

// App 汇集服务运行所需的依赖。
type App struct {
	Pool         *pgxpool.Pool
	Verifier     *auth.AdminVerifier
	Callers      *auth.CallerChecker
	PhotoDir     string        // 证据照片目录（ENFORCE_PHOTO_DIR）
	OrphanTTL    time.Duration // 未被证据引用的照片保留多久后清理
	StreamMaxAge time.Duration // 本服务事件流的保留期限，0 表示不限
	Now          func() time.Time
	Log          *slog.Logger
}

var shanghaiLoc *time.Location

func shanghai() *time.Location {
	if shanghaiLoc == nil {
		loc, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			loc = time.FixedZone("CST", 8*3600)
		}
		shanghaiLoc = loc
	}
	return shanghaiLoc
}

// now 返回服务器时间（北京时间，精确到秒）。
func (a *App) now() time.Time {
	f := a.Now
	if f == nil {
		f = time.Now
	}
	return f().In(shanghai()).Truncate(time.Second)
}

// fmtTime 输出 RFC 3339 带时区。
func fmtTime(t time.Time) string { return t.In(shanghai()).Format(time.RFC3339) }

func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

// Migrate 执行内嵌的 SQL 迁移。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func(db *sql.DB) { _ = db.Close() }(db)
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("迁移准备失败：%w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("迁移失败：%w", err)
	}
	return nil
}

// Handler 返回全部 HTTP 路由。
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	h := func(pattern string, f func(http.ResponseWriter, *http.Request) error) {
		mux.HandleFunc(pattern, a.wrap(f))
	}
	h("POST /api/enforce/scan", a.handleScan)
	h("POST /api/enforce/photos", a.handleUploadPhoto)
	h("GET /api/enforce/photos/{photoId}", a.handleGetPhoto)
	h("POST /api/enforce/my/evidence", a.handleCapture)
	h("GET /api/enforce/my/evidence", a.handleListMine)
	h("GET /api/enforce/my/evidence/{evidenceId}", a.handleGetMine)
	h("PUT /api/enforce/my/evidence/{evidenceId}", a.handleLockedMine)
	h("DELETE /api/enforce/my/evidence/{evidenceId}", a.handleLockedMine)
	h("GET /api/enforce/evidence", a.handleListEvidence)
	h("GET /api/enforce/evidence/{evidenceId}", a.handleGetEvidence)
	h("PUT /api/enforce/evidence/{evidenceId}", a.handleLockedEvidence)
	h("GET /api/enforce/evidence/{evidenceId}/verify", a.handleVerify)
	h("POST /api/enforce/evidence-exports", a.handleExport)
	h("GET /api/enforce/evidence-exports", a.handleListExports)
	return mux
}

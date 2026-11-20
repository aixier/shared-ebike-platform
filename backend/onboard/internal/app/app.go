// Package app onboard 接入验收服务的全部业务：接入记录投影、联调凭证、联调入口与测试驱动、
// 符合性测试、真车联调、验收报告、验收确认，以及接入智能体要用的工具后端。
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"bike/onboard/internal/auth"
	"bike/onboard/internal/clients/fleet"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
)

// Config 本服务的运行参数。规范里的时限与测试等待窗口都从这里来，不写死在判断里。
type Config struct {
	AgentAccountID string        // 接入智能体账号编号（ONBOARD_AGENT_ACCOUNT_ID）
	SandboxBaseURL string        // 联调环境入口地址（SANDBOX_BASE_URL）
	ReplyLimit     time.Duration // M-01、M-02 同步答复时限（SPEC_REPLY_LIMIT_MS，规范 1 秒）
	CallTimeout    time.Duration // 平台调运营商的超时（SPEC_CALL_TIMEOUT_MS，规范 3 秒）
	ClockSkew      time.Duration // 签名时间戳允许偏差（SPEC_CLOCK_SKEW_SEC，规范 300 秒）
	Grace          time.Duration // 观察迟到报告的额外窗口（CONFORMANCE_GRACE_SEC）
	LockWait       time.Duration // 结束请求后等锁车报告的窗口（CONFORMANCE_LOCK_WAIT_SEC）
	Poll           time.Duration // 测试驱动轮询联调记录的间隔
	EventMaxAge    time.Duration // 本模块事件流保留期限（EVENT_MAX_AGE_HOURS）
}

// DefaultConfig 规范与需求文档给出的默认值。
func DefaultConfig() Config {
	return Config{
		AgentAccountID: "AC0903",
		SandboxBaseURL: "http://127.0.0.1:18304/sandbox/open/v1",
		ReplyLimit:     time.Second,
		CallTimeout:    3 * time.Second,
		ClockSkew:      300 * time.Second,
		Grace:          15 * time.Second,
		LockWait:       30 * time.Second,
		Poll:           100 * time.Millisecond,
		EventMaxAge:    30 * 24 * time.Hour,
	}
}

// App 服务实例。
type App struct {
	DB      *pgxpool.Pool
	JS      jetstream.JetStream // 为空时只写发件表，不发总线
	Fleet   *fleet.Client
	Admin   *auth.AdminVerifier
	Callers auth.Callers
	Cfg     Config
	Log     *slog.Logger
	HTTP    *http.Client // 测试驱动调运营商用
}

func (a *App) now() time.Time { return time.Now() }

func (a *App) logger() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

func (a *App) operatorHTTP() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: a.Cfg.CallTimeout}
}

// Routes 全部 HTTP 入口。
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	// 运营商页面
	mux.Handle("GET /api/onboard/integrations", a.admin(false, a.hListMyIntegrations, "acceptance.view.own", "access.view.own"))
	mux.Handle("GET /api/onboard/spec", a.admin(false, a.hGetSpec, "integration.doc"))
	mux.Handle("GET /api/onboard/sandbox", a.admin(false, a.hGetSandbox, "integration.sandbox.own"))
	mux.Handle("PUT /api/onboard/sandbox/payment-mode", a.admin(true, a.hSetPaymentMode, "integration.sandbox.own"))
	mux.Handle("POST /api/onboard/integrations/{brandId}/credential/rotate", a.admin(true, a.hRotate, "integration.sandbox.own"))
	mux.Handle("POST /api/onboard/integrations/{brandId}/credential/issue", a.admin(true, a.hIssue, "integration.sandbox.own"))
	mux.Handle("POST /api/onboard/integrations/{brandId}/credential/revoke", a.admin(true, a.hRevoke, "integration.sandbox.own"))
	mux.Handle("POST /api/onboard/integrations/{brandId}/credential/restore", a.admin(true, a.hRestore, "acceptance.confirm"))
	mux.Handle("PUT /api/onboard/integrations/{brandId}/operator-base-url", a.admin(true, a.hSetOperatorBaseURL, "integration.sandbox.own"))
	mux.Handle("GET /api/onboard/runs", a.admin(false, a.hListRuns, "integration.conformance.own", "ops.monitor"))
	mux.Handle("POST /api/onboard/integrations/{brandId}/runs", a.admin(true, a.hStartRun, "integration.conformance.own"))
	mux.Handle("POST /api/onboard/integrations/{brandId}/real-rides", a.admin(true, a.hSubmitRealRides, "integration.conformance.own"))
	// 真车联调的骑行发起、结束与列表（swagger：listRealRides、startRealRide、endRealRide）
	mux.Handle("GET /api/onboard/integrations/{brandId}/real-rides/rides", a.admin(false, a.hListRealRides, "integration.conformance.own"))
	mux.Handle("POST /api/onboard/integrations/{brandId}/real-rides/rides", a.admin(true, a.hStartRealRide, "integration.conformance.own"))
	mux.Handle("POST /api/onboard/integrations/{brandId}/real-rides/rides/{orderId}/end", a.admin(true, a.hEndRealRide, "integration.conformance.own"))

	// 运维页面
	mux.Handle("GET /api/onboard/acceptance", a.admin(false, a.hListAcceptance, "ops.monitor"))
	mux.Handle("POST /api/onboard/acceptance/{integrationId}/confirm", a.admin(true, a.hConfirm, "acceptance.confirm"))
	mux.Handle("GET /api/onboard/todos", a.admin(false, a.hTodos, "acceptance.confirm"))

	// 联调入口（运营商签名）
	mux.HandleFunc("POST /sandbox/open/v1/ride-events", a.hSandboxRideEvent)
	mux.HandleFunc("POST /sandbox/open/v1/vehicle-lists", a.hSandboxVehicleList)

	// 本模块没有被其他模块调用的接口；/internal 先验调用方凭证再答复不存在
	mux.HandleFunc("/internal/", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.Callers.Check(r); !ok {
			writeJSON(w, 401, map[string]string{"code": "UNAUTHORIZED", "message": "服务凭证无效"})
			return
		}
		writeJSON(w, 404, map[string]string{"code": "NOT_FOUND", "message": "没有这个接口"})
	})
	return mux
}

type adminHandler func(r *http.Request, adm *auth.Admin) (any, error)

// admin 管理端接口的公共处理：验令牌、查权限点、只读拒写、幂等键。
func (a *App) admin(write bool, fn adminHandler, perms ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adm, err := a.Admin.Verify(r.Header.Get("Authorization"))
		if err != nil {
			writeJSON(w, 401, errNotLoggedIn.body())
			return
		}
		if !adm.HasAny(perms...) {
			writeJSON(w, 403, errForbidden.body())
			return
		}
		if write {
			if adm.Readonly {
				writeJSON(w, 403, errReadonly.body())
				return
			}
			// 运营商已退出（订阅 fleet.operator.exited）：不依赖令牌里的 readonly 何时刷新
			if adm.Scope != "" && a.operatorExited(r.Context(), adm.Scope) {
				writeJSON(w, 403, errReadonly.body())
				return
			}
		}
		key := r.Header.Get("Idempotency-Key")
		if !write || key == "" {
			status, body := toResponse(fn(r, adm))
			writeJSON(w, status, body)
			return
		}
		if len(key) > 64 {
			writeJSON(w, 400, errf(400, "INVALID", "幂等键不能超过 64 个字符").body())
			return
		}
		scope := adm.AccountID + " " + r.Method + " " + r.URL.Path
		var st int
		var saved []byte
		err = a.DB.QueryRow(r.Context(), `SELECT status, body FROM idempotency WHERE scope=$1 AND key=$2`, scope, key).Scan(&st, &saved)
		if err == nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(st)
			_, _ = w.Write(saved)
			return
		}
		status, body := toResponse(fn(r, adm))
		buf, _ := json.Marshal(body)
		if status < 500 {
			_, _ = a.DB.Exec(context.WithoutCancel(r.Context()), `INSERT INTO idempotency (scope, key, status, body) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, scope, key, status, buf)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(append(buf, '\n'))
	})
}

// decodeBody 解析可选或必填的 JSON 请求体。
func decodeBody(r *http.Request, v any, required bool) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return errf(400, "INVALID", "请求体读取失败")
	}
	if !utf8.Valid(b) {
		return errNotUTF8
	}
	if len(bytes.TrimSpace(b)) == 0 {
		if required {
			return errf(400, "INVALID", "缺少请求体")
		}
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return errf(400, "INVALID", "请求体不是合法的 JSON")
	}
	return nil
}

// inTx 在一个事务里执行。
func (a *App) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

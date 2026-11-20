// Package iam 实现账号与留痕服务：登录会话、管理令牌、账号开通停用、留痕保存与查询、事件收发。
package iam

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"bike/iam/internal/token"
)

// Options 是创建服务所需的依赖。
type Options struct {
	DB             *pgxpool.Pool
	Signer         *token.Signer
	ExtraPubKeys   map[string]ed25519.PublicKey // 轮换期仍有效的旧公钥
	AllowedCallers map[string]string
	InitialPwdHash string // 统一初始密码的哈希；为空时读库里 iam_settings
	Location       *time.Location
	Now            func() time.Time
	Log            *slog.Logger
	Retention      time.Duration // 过期会话与幂等记录保留多久后清理
}

// Service 是 iam 的业务实现。
type Service struct {
	db             *pgxpool.Pool
	signer         *token.Signer
	verifier       *token.Verifier
	pubKeys        map[string]ed25519.PublicKey
	callers        map[string]string
	initialPwdHash string
	loc            *time.Location
	now            func() time.Time
	log            *slog.Logger
	retention      time.Duration
	dummyHash      []byte
}

// NewService 创建服务。
func NewService(o Options) *Service {
	keys := map[string]ed25519.PublicKey{}
	for k, v := range o.ExtraPubKeys {
		keys[k] = v
	}
	keys[o.Signer.Kid()] = o.Signer.PublicKey()
	if o.Location == nil {
		o.Location = time.Local
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Retention <= 0 {
		o.Retention = 7 * 24 * time.Hour
	}
	// 账号不存在时也做一次哈希比对，避免靠响应时间探测账号名
	dummy, _ := bcrypt.GenerateFromPassword([]byte(randomID()), bcrypt.DefaultCost)
	return &Service{
		db: o.DB, signer: o.Signer, verifier: token.NewVerifier(keys), pubKeys: keys, callers: o.AllowedCallers,
		initialPwdHash: o.InitialPwdHash, loc: o.Location, now: o.Now, log: o.Log, retention: o.Retention, dummyHash: dummy,
	}
}

// Handler 返回全部 HTTP 路由。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/iam/login", s.handleLogin)
	mux.HandleFunc("POST /api/iam/logout", s.handleLogout)
	mux.HandleFunc("POST /api/iam/password", s.authed(s.handleChangePassword))
	mux.HandleFunc("POST /api/iam/token/refresh", s.authed(s.handleRefresh))
	mux.HandleFunc("GET /api/iam/me", s.authed(s.handleMe))
	mux.HandleFunc("GET /api/iam/accounts", s.authed(s.handleListAccounts))
	mux.HandleFunc("POST /api/iam/accounts", s.authed(s.handleCreateAccount))
	mux.HandleFunc("PUT /api/iam/accounts/{accountId}/status", s.authed(s.handleSetAccountStatus))
	mux.HandleFunc("GET /api/iam/audit", s.authed(s.handleListAudit))
	mux.HandleFunc("GET /api/iam/audit/actions", s.authed(s.handleAuditActions))
	mux.HandleFunc("GET /internal/iam/token-keys", s.internal(s.handleTokenKeys))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, apiError("NOT_FOUND", "没有这个接口"))
	})
	return mux
}

/* ---------- 错误体 ---------- */

// APIError 是按 _common.yaml Error 返回的业务错误。
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

var codeStatus = map[string]int{
	"INVALID": 400, "REASON_REQUIRED": 400,
	"ADMIN_NOT_LOGGED_IN": 401, "LOGIN_FAILED": 401, "UNAUTHORIZED": 401,
	"FORBIDDEN": 403, "READONLY": 403, "ACCOUNT_DISABLED": 403,
	"NOT_FOUND": 404, "SERVER_ERROR": 500,
}

var codeMessage = map[string]string{
	"INVALID":             "请求内容有误，请检查后重试",
	"REASON_REQUIRED":     "请填写操作事由（至少 4 个字）",
	"ADMIN_NOT_LOGGED_IN": "登录已失效，请重新登录",
	"LOGIN_FAILED":        "账号或密码不正确",
	"UNAUTHORIZED":        "调用方凭证不正确",
	"FORBIDDEN":           "你的账号没有该操作的权限",
	"READONLY":            "账号为只读，不能进行修改操作",
	"ACCOUNT_DISABLED":    "账号已停用，请联系运维账号管理员",
	"NOT_FOUND":           "没有找到",
	"SERVER_ERROR":        "服务暂时异常，请稍后重试",
}

func apiError(code, msg string) *APIError {
	st, ok := codeStatus[code]
	if !ok {
		st = http.StatusConflict
	}
	if msg == "" {
		msg = codeMessage[code]
	}
	return &APIError{Status: st, Code: code, Message: msg}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *APIError
	if !errors.As(err, &ae) {
		ae = apiError("SERVER_ERROR", "")
	}
	writeJSON(w, ae.Status, map[string]string{"code": ae.Code, "message": ae.Message})
}

func (s *Service) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *APIError
	if !errors.As(err, &ae) {
		s.log.Error("内部错误", "path", r.URL.Path, "err", err)
	}
	writeErr(w, err)
}

func decodeBody(r *http.Request, v any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		return apiError("INVALID", "请求内容不是有效的 JSON")
	}
	if !utf8.Valid(raw) {
		return apiError("INVALID", "请求体不是 UTF-8 编码")
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return apiError("INVALID", "请求内容不是有效的 JSON")
	}
	return nil
}

/* ---------- 登录校验 ---------- */

type principal struct {
	acct      *accountRow
	sessionID string
	client    string
	tokenRO   bool // 令牌声明 readonly
}

func (p *principal) readonly() bool { return p.tokenRO || isReadonly(p.acct.Status) }

func (p *principal) has(perm string) bool {
	for _, x := range p.acct.Permissions {
		if x == perm {
			return true
		}
	}
	return false
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// authenticate 验令牌、账号状态与会话。本服务自己的接口以库里的最新状态为准。
func (s *Service) authenticate(ctx context.Context, r *http.Request) (*principal, error) {
	tok := bearer(r)
	if tok == "" {
		return nil, apiError("ADMIN_NOT_LOGGED_IN", "")
	}
	c, err := s.verifier.Verify(tok, s.now())
	if err != nil {
		return nil, apiError("ADMIN_NOT_LOGGED_IN", "")
	}
	acct, err := s.getAccount(ctx, s.db, c.Subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apiError("ADMIN_NOT_LOGGED_IN", "")
	}
	if err != nil {
		return nil, err
	}
	if acct.Status == statusDisabled {
		return nil, apiError("ACCOUNT_DISABLED", "")
	}
	var revoked *time.Time
	var expires time.Time
	err = s.db.QueryRow(ctx, `SELECT revoked_at, expires_at FROM iam_sessions WHERE session_id=$1 AND account_id=$2`, c.SessionID, acct.AccountID).Scan(&revoked, &expires)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (revoked != nil || !s.now().Before(expires))) {
		return nil, apiError("ADMIN_NOT_LOGGED_IN", "")
	}
	if err != nil {
		return nil, err
	}
	return &principal{acct: acct, sessionID: c.SessionID, client: c.Client, tokenRO: c.Readonly}, nil
}

func (s *Service) authed(h func(http.ResponseWriter, *http.Request, *principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.authenticate(r.Context(), r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		h(w, r, p)
	}
}

func requirePerm(p *principal, perm string) error {
	if !p.has(perm) {
		return apiError("FORBIDDEN", "你的账号（"+p.acct.Role+"）没有该操作的权限")
	}
	return nil
}

func requireWritable(p *principal) error {
	if p.readonly() {
		return apiError("READONLY", "")
	}
	return nil
}

// internal 校验服务间调用凭证：Authorization Bearer 与 X-Caller 须对上允许的调用方表。
func (s *Service) internal(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := r.Header.Get("X-Caller")
		want, ok := s.callers[caller]
		if !ok || want == "" || bearer(r) != want {
			s.fail(w, r, apiError("UNAUTHORIZED", ""))
			return
		}
		h(w, r)
	}
}

func (s *Service) handleTokenKeys(w http.ResponseWriter, _ *http.Request) {
	type key struct {
		Kid       string  `json:"kid"`
		Alg       string  `json:"alg"`
		PublicKey string  `json:"publicKey"`
		NotAfter  *string `json:"notAfter"`
	}
	keys := []key{{Kid: s.signer.Kid(), Alg: "EdDSA", PublicKey: token.PublicKeyPEM(s.signer.PublicKey())}}
	for kid, pub := range s.pubKeys {
		if kid != s.signer.Kid() {
			keys = append(keys, key{Kid: kid, Alg: "EdDSA", PublicKey: token.PublicKeyPEM(pub)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

/* ---------- 小工具 ---------- */

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) fmtTime(t time.Time) string { return t.In(s.loc).Format(time.RFC3339) }

func (s *Service) fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := s.fmtTime(*t)
	return &v
}

// validReason 校验事由至少 4 个字。
func validReason(r string) bool { return len([]rune(strings.TrimSpace(r))) >= 4 }

package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"bike/fleet/internal/apperr"
	"bike/fleet/internal/auth"
)

var (
	reVehicleID  = regexp.MustCompile(`^VH\d{6}$`)
	reOperatorID = regexp.MustCompile(`^OP\d{2}$`)
	reBrandID    = regexp.MustCompile(`^BR\d{2}$`)
	reGovCode    = regexp.MustCompile(`^SF\d{8}$`)
)

type handler struct {
	svc      *Service
	verifier *auth.AdminVerifier
	callers  auth.Callers
}

// NewHandler 装配本服务全部接口（contracts/openapi/fleet.yaml）。
func NewHandler(svc *Service, verifier *auth.AdminVerifier, callers auth.Callers) http.Handler {
	h := &handler{svc: svc, verifier: verifier, callers: callers}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	// 管理端
	view := []string{"vehicle.view.own", "access.view", "ops.monitor"}
	edit := []string{"vehicle.edit.own"}
	register := []string{"access.setExit"} // 主管部门接入管理权（iam 未单列登记权限点，沿用）
	providerView := []string{"access.view", "access.setExit"}
	mux.HandleFunc("GET /api/fleet/operator/vehicles", h.admin(view, false, h.listVehicles))
	mux.HandleFunc("POST /api/fleet/operator/vehicles/upload", h.admin(edit, true, h.uploadVehicles))
	mux.HandleFunc("PATCH /api/fleet/operator/vehicles/{vehicleId}", h.admin(edit, true, h.updateVehicle))
	mux.HandleFunc("POST /api/fleet/operator/vehicles/{vehicleId}/remove", h.admin(edit, true, h.removeVehicle))
	mux.HandleFunc("GET /api/fleet/access/overview", h.admin([]string{"access.view", "access.view.own", "ops.monitor"}, false, h.accessOverview))
	mux.HandleFunc("POST /api/fleet/operators/{operatorId}/exit", h.admin([]string{"access.setExit"}, true, h.setExit))
	mux.HandleFunc("GET /api/fleet/operators", h.admin(nil, false, h.listOperators))
	mux.HandleFunc("POST /api/fleet/operators", h.admin(register, true, h.registerOperator))
	mux.HandleFunc("POST /api/fleet/brands", h.admin(register, true, h.registerBrand))
	mux.HandleFunc("GET /api/fleet/providers", h.admin(providerView, false, h.listProviders))
	mux.HandleFunc("POST /api/fleet/providers", h.admin(register, true, h.registerProvider))
	mux.HandleFunc("GET /api/fleet/providers/{providerId}", h.admin(providerView, false, h.getProvider))
	mux.HandleFunc("PUT /api/fleet/providers/{providerId}", h.admin(register, true, h.updateProvider))
	mux.HandleFunc("GET /api/fleet/operators/{operatorId}", h.admin(nil, false, h.getOperator))
	mux.HandleFunc("GET /api/fleet/brands", h.admin(nil, false, h.listBrands))
	mux.HandleFunc("GET /api/fleet/brands/{brandId}", h.admin(nil, false, h.getBrand))
	mux.HandleFunc("GET /api/fleet/vehicles/{vehicleId}", h.admin(nil, false, h.getVehicle))
	mux.HandleFunc("GET /api/fleet/gov-codes/{govCode}", h.admin(nil, false, h.getByGovCode))

	// 模块间
	mux.HandleFunc("POST /internal/fleet/vehicle-lists", h.internal(h.submitVehicleList))
	mux.HandleFunc("POST /internal/fleet/brands/{brandId}/go-live", h.internal(h.goLive))
	mux.HandleFunc("POST /internal/fleet/brands/{brandId}/pause", h.internal(h.pause))
	mux.HandleFunc("POST /internal/fleet/brands/{brandId}/resume", h.internal(h.resume))
	mux.HandleFunc("POST /internal/fleet/brands/{brandId}/start-integration", h.internal(h.startIntegration))
	return mux
}

/* ---------------- 基础件：错误体、鉴权、幂等 ---------------- */

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		slog.Error("内部错误", "err", err)
		ae = apperr.Internal()
	}
	writeJSON(w, ae.Status, ae.Body())
}

// respond 把业务结果写成响应。
func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func decode(r *http.Request, v any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return apperr.Invalid("读取请求体失败")
	}
	// 中文必须是 UTF-8。Windows 命令行（git-bash 的 curl -d）直传中文会被转成本地代码页（GBK），
	// 不拦住的话解出来是乱码，会被误报成别的字段错误；改用文件 --data-binary @文件 传。
	if !utf8.Valid(raw) {
		return apperr.Invalid("请求体不是 UTF-8 编码（命令行直传中文会被转成本地代码页，请改用文件传：curl --data-binary @body.json）")
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return apperr.Invalid("请求体不是合法的 JSON")
	}
	return nil
}

type adminHandler func(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims)

// admin：验管理令牌 → 查权限点（nil 表示登录即可）→ 写操作拒只读账号 → 写操作按幂等键去重。
func (h *handler) admin(perms []string, write bool, fn adminHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := h.verifier.FromRequest(r)
		if err != nil {
			writeErr(w, apperr.NotLoggedIn())
			return
		}
		if len(perms) > 0 && !c.HasAny(perms...) {
			writeErr(w, apperr.Forbidden())
			return
		}
		if write && c.Readonly {
			writeErr(w, apperr.Readonly())
			return
		}
		if !write {
			fn(w, r, c)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			fn(w, r, c)
			return
		}
		h.idempotent(w, r, "admin:"+c.AccountID, key, func(w http.ResponseWriter) { fn(w, r, c) })
	}
}

// internal：校验调用方服务凭证，幂等键必带。
func (h *handler) internal(fn func(w http.ResponseWriter, r *http.Request, caller string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := h.callers.Check(r)
		if !ok {
			writeErr(w, apperr.Unauthorized("调用方凭证无效"))
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 64 {
			writeErr(w, apperr.Invalid("缺少幂等键 Idempotency-Key（最长 64 位）"))
			return
		}
		h.idempotent(w, r, "internal:"+caller, key, func(w http.ResponseWriter) { fn(w, r, caller) })
	}
}

type recorder struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (rc *recorder) Header() http.Header         { return rc.header }
func (rc *recorder) Write(b []byte) (int, error) { return rc.buf.Write(b) }
func (rc *recorder) WriteHeader(s int)           { rc.status = s }

// idempotent：同一调用方、同一接口、同一个键只执行一次，重复到达返回第一次的结果。
// 用会话级数据库锁串行同键请求；500 不记，允许重试。
func (h *handler) idempotent(w http.ResponseWriter, r *http.Request, who, key string, fn func(w http.ResponseWriter)) {
	ctx := r.Context()
	scope := who + " " + r.Method + " " + r.URL.Path
	conn, err := h.svc.db.Acquire(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer conn.Release()
	lockKey := scope + "|" + key
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		writeErr(w, err)
		return
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey)

	var status int
	var body []byte
	err = conn.QueryRow(ctx, `SELECT status, body FROM idempotency WHERE scope = $1 AND key = $2`, scope, key).Scan(&status, &body)
	if err == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, err)
		return
	}
	rec := &recorder{header: http.Header{}, status: 200}
	fn(rec)
	if rec.status < 500 {
		if _, err := conn.Exec(ctx, `INSERT INTO idempotency (scope, key, status, body) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			scope, key, rec.status, rec.buf.Bytes()); err != nil {
			slog.Error("记录幂等结果失败", "err", err)
		}
	}
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.buf.Bytes())
}

func adminActor(c *auth.AdminClaims) Actor { return Actor{Kind: "人", ID: c.AccountID, Name: c.Name} }

/* ---------------- 管理端接口 ---------------- */

func (h *handler) listVehicles(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	q := r.URL.Query()
	f := VehicleFilter{BrandID: q.Get("brandId"), CodeStatus: q.Get("codeStatus"), Status: q.Get("status"), Q: q.Get("q"),
		SortKey: q.Get("sortKey"), SortDir: q.Get("sortDir"), OperatorID: q.Get("operatorId")}
	if c.Scope != "" {
		if f.OperatorID != "" && f.OperatorID != c.Scope {
			writeErr(w, apperr.ScopeDenied())
			return
		}
		f.OperatorID = c.Scope
	}
	var err error
	if f.Page, err = intParam(q.Get("page")); err != nil {
		writeErr(w, err)
		return
	}
	if f.PageSize, err = intParam(q.Get("pageSize")); err != nil {
		writeErr(w, err)
		return
	}
	if q.Has("page") && f.Page < 1 {
		writeErr(w, apperr.Invalid("page 从 1 起"))
		return
	}
	if q.Has("pageSize") && f.PageSize < 1 {
		writeErr(w, apperr.Invalid("pageSize 为 1～200"))
		return
	}
	page, err := h.svc.ListVehicles(r.Context(), f)
	respond(w, page, err)
}

func intParam(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, apperr.Invalid("分页参数应为整数")
	}
	return n, nil
}

func (h *handler) uploadVehicles(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	if c.Scope == "" {
		writeErr(w, apperr.New(403, "FORBIDDEN", "车辆清单由运营商账号上传"))
		return
	}
	var body struct {
		Rows []VehicleListRow `json:"rows"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.svc.SubmitVehicleList(r.Context(), c.Scope, body.Rows, adminActor(c))
	respond(w, res, err)
}

func (h *handler) updateVehicle(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	id := r.PathValue("vehicleId")
	if !reVehicleID.MatchString(id) {
		writeErr(w, apperr.NotFound("车辆"))
		return
	}
	var p VehiclePatch
	if err := decode(r, &p); err != nil {
		writeErr(w, err)
		return
	}
	v, err := h.svc.UpdateVehicle(r.Context(), c.Scope, id, p, adminActor(c))
	respond(w, v, err)
}

func (h *handler) removeVehicle(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	id := r.PathValue("vehicleId")
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if !reasonOK(body.Reason) {
		writeErr(w, apperr.ReasonRequired())
		return
	}
	if !reVehicleID.MatchString(id) {
		writeErr(w, apperr.NotFound("车辆"))
		return
	}
	v, err := h.svc.RemoveVehicle(r.Context(), c.Scope, id, body.Reason, adminActor(c))
	respond(w, v, err)
}

func (h *handler) accessOverview(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	ov, err := h.svc.AccessOverview(r.Context(), c.Scope)
	respond(w, ov, err)
}

func (h *handler) setExit(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	id := r.PathValue("operatorId")
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if !reasonOK(body.Reason) {
		writeErr(w, apperr.ReasonRequired())
		return
	}
	if !reOperatorID.MatchString(id) {
		writeErr(w, apperr.NotFound("运营商"))
		return
	}
	res, err := h.svc.SetOperatorExit(r.Context(), id, body.Reason, adminActor(c))
	respond(w, res, err)
}

func (h *handler) listOperators(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	ops, err := h.svc.ListOperators(r.Context(), c.Scope, r.URL.Query().Get("status"))
	respond(w, ops, err)
}

func (h *handler) getOperator(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	id := r.PathValue("operatorId")
	if !reOperatorID.MatchString(id) {
		writeErr(w, apperr.NotFound("运营商"))
		return
	}
	o, err := h.svc.GetOperator(r.Context(), c.Scope, id)
	respond(w, o, err)
}

func (h *handler) registerOperator(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	var req RegisterOperatorReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	o, err := h.svc.RegisterOperator(r.Context(), req, adminActor(c))
	respond(w, o, err)
}

func (h *handler) registerBrand(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	var req RegisterBrandReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	b, err := h.svc.RegisterBrand(r.Context(), req, adminActor(c))
	respond(w, b, err)
}

func (h *handler) registerProvider(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	var req RegisterProviderReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := h.svc.RegisterProvider(r.Context(), req, adminActor(c))
	respond(w, p, err)
}

func (h *handler) updateProvider(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	id := r.PathValue("providerId")
	if !reProviderID.MatchString(id) {
		writeErr(w, apperr.NotFound("车联网服务商"))
		return
	}
	var req RegisterProviderReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := h.svc.UpdateProvider(r.Context(), id, req, adminActor(c))
	respond(w, p, err)
}

func (h *handler) listProviders(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	ps, err := h.svc.ListProviders(r.Context())
	respond(w, ps, err)
}

func (h *handler) getProvider(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	id := r.PathValue("providerId")
	if !reProviderID.MatchString(id) {
		writeErr(w, apperr.NotFound("车联网服务商"))
		return
	}
	p, err := h.svc.GetProvider(r.Context(), id)
	respond(w, p, err)
}

func (h *handler) listBrands(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	q := r.URL.Query()
	bs, err := h.svc.ListBrands(r.Context(), c.Scope, q.Get("operatorId"), q.Get("accessStatus"))
	respond(w, bs, err)
}

func (h *handler) getBrand(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	id := r.PathValue("brandId")
	if !reBrandID.MatchString(id) {
		writeErr(w, apperr.NotFound("品牌"))
		return
	}
	b, err := h.svc.GetBrandDetail(r.Context(), c.Scope, id)
	respond(w, b, err)
}

func (h *handler) getVehicle(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	id := r.PathValue("vehicleId")
	if !reVehicleID.MatchString(id) {
		writeErr(w, apperr.NotFound("车辆"))
		return
	}
	v, err := h.svc.GetVehicle(r.Context(), c.Scope, id)
	respond(w, v, err)
}

func (h *handler) getByGovCode(w http.ResponseWriter, r *http.Request, c *auth.AdminClaims) {
	code := strings.ToUpper(r.PathValue("govCode"))
	if !reGovCode.MatchString(code) {
		writeErr(w, codeNotFound())
		return
	}
	v, err := h.svc.GetVehicleByGovCode(r.Context(), c.Scope, code)
	respond(w, v, err)
}

/* ---------------- 模块间接口 ---------------- */

func (h *handler) submitVehicleList(w http.ResponseWriter, r *http.Request, _ string) {
	var body struct {
		OperatorID string           `json:"operatorId"`
		Rows       []VehicleListRow `json:"rows"`
		Actor      *Actor           `json:"actor"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := validActor(body.Actor); err != nil {
		writeErr(w, err)
		return
	}
	if body.OperatorID == "" {
		writeErr(w, apperr.Invalid("缺少 operatorId"))
		return
	}
	res, err := h.svc.SubmitVehicleList(r.Context(), body.OperatorID, body.Rows, *body.Actor)
	respond(w, res, err)
}

type actorReason struct {
	Actor  *Actor `json:"actor"`
	Reason string `json:"reason"`
}

func (h *handler) brandIDOr404(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("brandId")
	if !reBrandID.MatchString(id) {
		writeErr(w, apperr.NotFound("品牌"))
		return "", false
	}
	return id, true
}

func (h *handler) goLive(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := h.brandIDOr404(w, r)
	if !ok {
		return
	}
	var body actorReason
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.svc.GoLive(r.Context(), id, body.Actor, body.Reason)
	respond(w, res, err)
}

func (h *handler) pause(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := h.brandIDOr404(w, r)
	if !ok {
		return
	}
	var body PauseCommand
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.svc.Pause(r.Context(), id, body)
	respond(w, res, err)
}

func (h *handler) resume(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := h.brandIDOr404(w, r)
	if !ok {
		return
	}
	var body actorReason
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.svc.Resume(r.Context(), id, body.Actor, body.Reason)
	respond(w, res, err)
}

func (h *handler) startIntegration(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := h.brandIDOr404(w, r)
	if !ok {
		return
	}
	var body actorReason
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.svc.StartIntegration(r.Context(), id, body.Actor)
	respond(w, res, err)
}

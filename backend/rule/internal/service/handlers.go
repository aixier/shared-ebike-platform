// Package service 是 rule 服务的接口、存储与事件发布。业务判断只有"版本不可变、任一时刻一个生效版本"这类不变量，
// 全部属于版本不可变与状态机的确定性范围；本模块不算金额、不执行限额。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bike/rule/internal/auth"
	"bike/rule/internal/domain"
)

// Service rule 服务。
type Service struct {
	Pool    *pgxpool.Pool
	Now     func() time.Time
	Admin   *auth.AdminVerifier
	Callers auth.Callers
	Log     *slog.Logger
	// Notify 发件表有新事件时唤醒发布循环；可为 nil。
	Notify func()
	// CheckBrands 为真时，新增版本里服务费比例引用的品牌必须在品牌投影里（投影来自 fleet.brand.registered）。
	// 未接事件总线（没有投影来源）时不开，避免把所有比例都拒掉。
	CheckBrands bool
}

// paramsOwner 平台参数归属（契约 PlatformParams.owner）。
const paramsOwner = "平台公司"

var versionIDPattern = regexp.MustCompile(`^PR\d{4}$`)

// 接口所需权限点（rule.yaml 的 x-permissions）。
var (
	permView   = []string{"pricing.view", "pricing.edit"}
	permEdit   = []string{"pricing.edit"}
	permParams = []string{"platformParams.edit"}
)

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Handler 全部路由。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []byte(`{"ok":true}`))
	})
	mux.HandleFunc("GET /api/rule/pricing/versions", s.wrap(s.listVersions))
	mux.HandleFunc("POST /api/rule/pricing/versions", s.wrap(s.createVersion))
	mux.HandleFunc("GET /api/rule/pricing/current", s.wrap(s.currentVersion))
	mux.HandleFunc("GET /api/rule/pricing/versions/{versionId}", s.wrap(s.getVersion))
	mux.HandleFunc("POST /api/rule/pricing/versions/{versionId}/revoke", s.wrap(s.revokeVersion))
	mux.HandleFunc("GET /api/rule/platform-params", s.wrap(s.getParams))
	mux.HandleFunc("PATCH /api/rule/platform-params", s.wrap(s.updateParams))
	mux.HandleFunc("GET /internal/rule/versions/{versionId}", s.wrap(s.internal(s.internalVersion)))
	mux.HandleFunc("GET /internal/rule/effective-version", s.wrap(s.internal(s.internalEffective)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, nil, errNotFound("接口"))
	})
	return mux
}

// result 处理结果：状态码与已序列化的响应体。
type result struct {
	status int
	body   []byte
}

func ok(v any) (result, error) {
	b, err := json.Marshal(v)
	return result{200, b}, err
}

func (s *Service) wrap(h func(*http.Request) (result, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := h(r)
		if err != nil {
			writeErr(w, s.log(), err)
			return
		}
		writeJSON(w, res.status, res.body)
	}
}

func (s *Service) internal(h func(*http.Request) (result, error)) func(*http.Request) (result, error) {
	return func(r *http.Request) (result, error) {
		if _, err := s.Callers.Check(r); err != nil {
			return result{}, errCaller()
		}
		return h(r)
	}
}

// writeTx 写操作的事务骨架：串行锁 → 幂等键查重 → 业务 → 记幂等结果 → 提交 → 唤醒发布。
func (s *Service) writeTx(ctx context.Context, lock int64, scope, key string, fn func(tx pgx.Tx, now time.Time) (any, error)) (result, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return result{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockXact(ctx, tx, lock); err != nil {
		return result{}, err
	}
	if status, body, found, err := idemGet(ctx, tx, scope, key); err != nil {
		return result{}, err
	} else if found {
		return result{status, body}, nil
	}
	v, err := fn(tx, s.Now())
	if err != nil {
		return result{}, err
	}
	res, err := ok(v)
	if err != nil {
		return result{}, err
	}
	if err := idemPut(ctx, tx, scope, key, res.status, res.body); err != nil {
		return result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return result{}, err
	}
	if s.Notify != nil {
		s.Notify()
	}
	return res, nil
}

// ---------------------------------------------------------------- 规则版本

func (s *Service) listVersions(r *http.Request) (result, error) {
	if _, err := s.adminGuard(r, permView, false); err != nil {
		return result{}, err
	}
	vs, err := loadVersions(r.Context(), s.Pool)
	if err != nil {
		return result{}, err
	}
	domain.SortNewestFirst(vs)
	return ok(domain.Chain(vs, s.Now()))
}

func (s *Service) currentVersion(r *http.Request) (result, error) {
	if _, err := s.adminGuard(r, nil, false); err != nil {
		return result{}, err
	}
	return s.effectiveAt(r.Context(), s.Now(), "现行规则版本")
}

func (s *Service) effectiveAt(ctx context.Context, at time.Time, what string) (result, error) {
	vs, err := loadVersions(ctx, s.Pool)
	if err != nil {
		return result{}, err
	}
	v := domain.At(vs, at)
	if v == nil {
		return result{}, errNotFound(what)
	}
	view, _ := domain.ViewOf(vs, v.VersionID, s.Now())
	return ok(view)
}

func (s *Service) byID(ctx context.Context, id string) (result, error) {
	if !versionIDPattern.MatchString(id) {
		return result{}, errNotFound("规则版本")
	}
	vs, err := loadVersions(ctx, s.Pool)
	if err != nil {
		return result{}, err
	}
	view, found := domain.ViewOf(vs, id, s.Now())
	if !found {
		return result{}, errNotFound("规则版本")
	}
	return ok(view)
}

func (s *Service) getVersion(r *http.Request) (result, error) {
	if _, err := s.adminGuard(r, permView, false); err != nil {
		return result{}, err
	}
	return s.byID(r.Context(), r.PathValue("versionId"))
}

func (s *Service) createVersion(r *http.Request) (result, error) {
	a, err := s.adminGuard(r, permEdit, true)
	if err != nil {
		return result{}, err
	}
	key, err := idemKey(r)
	if err != nil {
		return result{}, err
	}
	body, err := decodeBody(r)
	if err != nil {
		return result{}, err
	}
	reason, okReason := domain.CheckReason(body)
	if !okReason {
		return result{}, errReasonRequired()
	}
	in, errs := domain.ParseCreate(body)
	if len(errs) > 0 {
		return result{}, errInvalid(errs)
	}
	actor := a.Actor()
	ctx := r.Context()
	return s.writeTx(ctx, lockVersions, "createPricingVersion:"+a.AccountID, key, func(tx pgx.Tx, now time.Time) (any, error) {
		now = now.Truncate(time.Second)
		eff, fe := domain.ResolveEffectiveAt(in.EffectiveAt, now)
		if fe != nil {
			return nil, errInvalid([]domain.FieldError{*fe})
		}
		vs, err := loadVersions(ctx, tx)
		if err != nil {
			return nil, err
		}
		maxSeq := 0
		for _, v := range vs {
			if v.Seq > maxSeq {
				maxSeq = v.Seq
			}
			if v.RevokedAt == nil && v.EffectiveAt.Equal(eff) {
				return nil, errInvalidMsg("effectiveAt", fmt.Sprintf("该生效时刻已有版本 %s「%s」，请换一个时刻或先撤销它", v.VersionID, v.Name))
			}
		}
		if maxSeq >= 9999 {
			return nil, errConflict("规则版本号已用完")
		}
		base := baseFor(vs, eff)
		var baseContent *domain.Content
		if base != nil {
			baseContent = &base.Content
		}
		content, errs := in.Apply(baseContent)
		if len(errs) > 0 {
			return nil, errInvalid(errs)
		}
		if s.CheckBrands {
			miss, err := missingBrands(ctx, tx, in.RatioBrands())
			if err != nil {
				return nil, err
			}
			if len(miss) > 0 {
				fes := make([]domain.FieldError, 0, len(miss))
				for _, b := range miss {
					fes = append(fes, domain.FieldError{Field: "serviceFee.ratios." + b, Message: fmt.Sprintf("品牌 %s 未在 fleet 登记", b)})
				}
				return nil, errInvalid(fes)
			}
		}
		note := in.Note
		if note == nil {
			note = &reason
		}
		nv := domain.Version{
			VersionID: fmt.Sprintf("PR%04d", maxSeq+1), Seq: maxSeq + 1, Name: in.Name,
			EffectiveAt: eff, CreatedAt: now, CreatedBy: actor, Note: note, Content: content,
		}
		if err := insertVersion(ctx, tx, nv); err != nil {
			return nil, err
		}
		all := append(vs, nv)
		var prevID *string
		if p := domain.Previous(all, nv); p != nil {
			prevID = &p.VersionID
		}
		if err := enqueue(ctx, tx, domain.EventVersionPublished, domain.KeyPricing, actor, now, domain.PublishedPayload(nv, prevID)); err != nil {
			return nil, err
		}
		before, after := domain.ContentDiff(baseContent, content)
		if before == nil {
			before = map[string]any{}
		}
		before["versionId"] = nil
		if base != nil {
			before["versionId"] = base.VersionID
		}
		after["versionId"] = nv.VersionID
		if err := enqueueAudit(ctx, tx, actor, now, domain.AuditEvent{
			Action: "新增规则版本", TargetType: "pricing", TargetID: nv.VersionID,
			Summary: fmt.Sprintf("录入规则版本 %s「%s」，%s 生效", nv.VersionID, nv.Name, domain.FormatTime(eff)),
			Reason:  &reason, Before: before, After: after,
		}); err != nil {
			return nil, err
		}
		view, _ := domain.ViewOf(all, nv.VersionID, now)
		return view, nil
	})
}

// baseFor 新版本以哪个版本为底：生效时刻有效的版本；没有（新版本早于全部现存版本）时取最早的未撤销版本；库里没有版本时为 nil。
func baseFor(vs []domain.Version, eff time.Time) *domain.Version {
	if v := domain.At(vs, eff); v != nil {
		return v
	}
	for _, v := range vs { // vs 已按生效时刻升序
		if v.RevokedAt == nil {
			v := v
			return &v
		}
	}
	return nil
}

func (s *Service) revokeVersion(r *http.Request) (result, error) {
	a, err := s.adminGuard(r, permEdit, true)
	if err != nil {
		return result{}, err
	}
	key, err := idemKey(r)
	if err != nil {
		return result{}, err
	}
	id := r.PathValue("versionId")
	body, err := decodeBody(r)
	if err != nil {
		return result{}, err
	}
	reason, okReason := domain.CheckReason(body)
	if !okReason {
		return result{}, errReasonRequired()
	}
	if !versionIDPattern.MatchString(id) {
		return result{}, errNotFound("规则版本")
	}
	actor := a.Actor()
	ctx := r.Context()
	return s.writeTx(ctx, lockVersions, "revokePricingVersion:"+a.AccountID, key, func(tx pgx.Tx, now time.Time) (any, error) {
		now = now.Truncate(time.Second)
		vs, err := loadVersions(ctx, tx)
		if err != nil {
			return nil, err
		}
		idx := -1
		for i, v := range vs {
			if v.VersionID == id {
				idx = i
			}
		}
		if idx < 0 {
			return nil, errNotFound("规则版本")
		}
		v := vs[idx]
		if v.RevokedAt != nil {
			return nil, errConflict("该版本已撤销")
		}
		if !v.EffectiveAt.After(now) {
			return nil, errConflict("只能撤销尚未生效的版本，已生效的版本留存可查")
		}
		ab, _ := json.Marshal(actor)
		if _, err := tx.Exec(ctx, `UPDATE pricing_versions SET revoked_at = $2, revoked_by = $3, revoke_reason = $4 WHERE version_id = $1`, id, now, ab, reason); err != nil {
			return nil, err
		}
		vs[idx].RevokedAt, vs[idx].RevokedBy = &now, &actor
		if err := enqueue(ctx, tx, domain.EventVersionRevoked, domain.KeyPricing, actor, now, domain.RevokedPayload(id, now)); err != nil {
			return nil, err
		}
		if err := enqueueAudit(ctx, tx, actor, now, domain.AuditEvent{
			Action: "撤销规则版本", TargetType: "pricing", TargetID: id,
			Summary: fmt.Sprintf("撤销待生效规则版本 %s「%s」", id, v.Name),
			Reason:  &reason, Before: map[string]any{"status": domain.StatusPending}, After: map[string]any{"status": domain.StatusRevoked},
		}); err != nil {
			return nil, err
		}
		view, _ := domain.ViewOf(vs, id, now)
		return view, nil
	})
}

// ---------------------------------------------------------------- 内部接口

func (s *Service) internalVersion(r *http.Request) (result, error) {
	return s.byID(r.Context(), r.PathValue("versionId"))
}

func (s *Service) internalEffective(r *http.Request) (result, error) {
	at := s.Now()
	if q := r.URL.Query().Get("at"); q != "" {
		t, err := time.Parse(time.RFC3339, q)
		if err != nil {
			return result{}, errInvalidMsg("at", "时刻格式不正确，应为带时区的时刻，如 2026-12-01T12:00:00+08:00")
		}
		at = t
	}
	return s.effectiveAt(r.Context(), at, "该时刻有效的规则版本")
}

// ---------------------------------------------------------------- 平台参数

type paramsView struct {
	Owner     string            `json:"owner"`
	UpdatedAt *string           `json:"updatedAt"`
	UpdatedBy *domain.Actor     `json:"updatedBy"`
	Items     domain.ParamItems `json:"items"`
}

func toParamsView(p paramsRow) paramsView {
	v := paramsView{Owner: paramsOwner, UpdatedBy: p.UpdatedBy, Items: p.Items}
	if p.UpdatedAt != nil {
		s := domain.FormatTime(*p.UpdatedAt)
		v.UpdatedAt = &s
	}
	return v
}

func (s *Service) getParams(r *http.Request) (result, error) {
	if _, err := s.adminGuard(r, nil, false); err != nil {
		return result{}, err
	}
	p, err := loadParams(r.Context(), s.Pool)
	if err != nil {
		return result{}, err
	}
	return ok(toParamsView(p))
}

func (s *Service) updateParams(r *http.Request) (result, error) {
	a, err := s.adminGuard(r, permParams, true)
	if err != nil {
		return result{}, err
	}
	key, err := idemKey(r)
	if err != nil {
		return result{}, err
	}
	body, err := decodeBody(r)
	if err != nil {
		return result{}, err
	}
	reason, okReason := domain.CheckReason(body)
	if !okReason {
		return result{}, errReasonRequired()
	}
	patch, errs := domain.ParseParamsPatch(body["items"])
	if len(errs) > 0 {
		return result{}, errInvalid(errs)
	}
	actor := a.Actor()
	ctx := r.Context()
	return s.writeTx(ctx, lockParams, "updatePlatformParams:"+a.AccountID, key, func(tx pgx.Tx, now time.Time) (any, error) {
		now = now.Truncate(time.Second)
		cur, err := loadParams(ctx, tx)
		if err != nil {
			return nil, err
		}
		next := domain.MergeParams(cur.Items, patch)
		before, after := domain.DiffParams(cur.Items, next)
		if err := saveParams(ctx, tx, next, now, actor); err != nil {
			return nil, err
		}
		bb, _ := json.Marshal(before)
		afb, _ := json.Marshal(after)
		ab, _ := json.Marshal(actor)
		ib, _ := json.Marshal(next)
		if _, err := tx.Exec(ctx, `INSERT INTO platform_params_history (changed_at, changed_by, reason, before, after, items) VALUES ($1,$2,$3,$4,$5,$6)`,
			now, ab, reason, bb, afb, ib); err != nil {
			return nil, err
		}
		if err := enqueue(ctx, tx, domain.EventParamsChanged, domain.KeyParams, actor, now, domain.ParamsChangedPayload(next, now)); err != nil {
			return nil, err
		}
		if err := enqueueAudit(ctx, tx, actor, now, domain.AuditEvent{
			Action: "修改平台参数", TargetType: "pricing", TargetID: "platformParams",
			Summary: fmt.Sprintf("修改平台公司参数 %d 项", len(after)),
			Reason:  &reason, Before: before, After: after,
		}); err != nil {
			return nil, err
		}
		return toParamsView(paramsRow{Items: next, UpdatedAt: &now, UpdatedBy: &actor}), nil
	})
}

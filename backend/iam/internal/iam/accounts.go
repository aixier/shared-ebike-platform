package iam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"bike/iam/internal/token"
)

const (
	statusNormal   = "正常"
	statusDisabled = "停用"
	statusReadonly = "只读（运营商已退出）"
	statusDemo     = "只读（演示账号）"
)

func isReadonly(status string) bool { return strings.HasPrefix(status, "只读") }

// eventStatus 把账号状态映射成 iam.account.changed 载荷里的取值（正常｜停用｜只读）。
func eventStatus(status string) string {
	if isReadonly(status) {
		return "只读"
	}
	return status
}

// 运营商岗位：必须带数据范围
var operatorRoles = map[string]bool{"运营商业务": true, "运营商技术对接": true}

var (
	usernameRe   = regexp.MustCompile(`^[a-z0-9.]{3,30}$`)
	operatorIDRe = regexp.MustCompile(`^OP[0-9]{2}$`)
	badgeFromRe  = regexp.MustCompile(`^zf([0-9]+)$`)
)

type accountRow struct {
	AccountID     string
	Role          string
	Post          string
	Name          string
	Username      *string
	PasswordHash  *string
	Org           string
	OperatorScope *string
	BadgeNo       *string
	Permissions   []string
	Menus         []string
	Status        string
	CreatedAt     time.Time
	CreatedBy     *string
	LastLoginAt   *time.Time
	AgentRole     *string
	Client        *string // 岗位允许登录的端
	Loginable     bool
}

const accountCols = `a.account_id, a.role, a.post, a.name, a.username, a.password_hash, a.org, a.operator_scope, a.badge_no,
	a.permissions, a.menus, a.status, a.created_at, a.created_by, a.last_login_at, a.agent_role, t.client, t.loginable`

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func scanAccount(row pgx.Row) (*accountRow, error) {
	var a accountRow
	err := row.Scan(&a.AccountID, &a.Role, &a.Post, &a.Name, &a.Username, &a.PasswordHash, &a.Org, &a.OperatorScope, &a.BadgeNo,
		&a.Permissions, &a.Menus, &a.Status, &a.CreatedAt, &a.CreatedBy, &a.LastLoginAt, &a.AgentRole, &a.Client, &a.Loginable)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Service) getAccount(ctx context.Context, q querier, id string) (*accountRow, error) {
	return scanAccount(q.QueryRow(ctx, `SELECT `+accountCols+` FROM iam_accounts a JOIN iam_role_templates t USING (role) WHERE a.account_id=$1`, id))
}

// Account 是对外返回的账号（不含密码）。
type Account struct {
	AccountID     string   `json:"accountId"`
	Role          string   `json:"role"`
	Post          string   `json:"post"`
	Name          string   `json:"name"`
	Username      *string  `json:"username"`
	Org           string   `json:"org"`
	OperatorScope *string  `json:"operatorScope"`
	BadgeNo       *string  `json:"badgeNo"`
	Permissions   []string `json:"permissions"`
	Menus         []string `json:"menus"`
	Status        string   `json:"status"`
	CreatedAt     string   `json:"createdAt"`
	CreatedBy     *string  `json:"createdBy"`
	LastLoginAt   *string  `json:"lastLoginAt"`
	AgentRole     *string  `json:"agentRole"`
}

func (s *Service) toAccount(a *accountRow) Account {
	perms, menus := a.Permissions, a.Menus
	if perms == nil {
		perms = []string{}
	}
	if menus == nil {
		menus = []string{}
	}
	return Account{
		AccountID: a.AccountID, Role: a.Role, Post: a.Post, Name: a.Name, Username: a.Username, Org: a.Org,
		OperatorScope: a.OperatorScope, BadgeNo: a.BadgeNo, Permissions: perms, Menus: menus, Status: a.Status,
		CreatedAt: s.fmtTime(a.CreatedAt), CreatedBy: a.CreatedBy, LastLoginAt: s.fmtTimePtr(a.LastLoginAt), AgentRole: a.AgentRole,
	}
}

/* ---------- 登录、退出、改密码、当前账号 ---------- */

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Client   string `json:"client"`
	}
	if err := decodeBody(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if req.Client != "管理端" && req.Client != "执法端" {
		s.fail(w, r, apiError("INVALID", "请说明从管理端还是执法端登录"))
		return
	}
	res, err := s.login(r.Context(), strings.TrimSpace(req.Username), req.Password, req.Client)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Service) login(ctx context.Context, username, password, client string) (map[string]any, error) {
	acct, err := scanAccount(s.db.QueryRow(ctx, `SELECT `+accountCols+` FROM iam_accounts a JOIN iam_role_templates t USING (role) WHERE a.username=$1`, username))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	hash := s.dummyHash
	if acct != nil && acct.PasswordHash != nil {
		hash = []byte(*acct.PasswordHash)
	}
	pwOK := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	// 账号名或密码不对、岗位与端不匹配、不可登录账号：一律登录失败
	if acct == nil || !pwOK || !acct.Loginable || acct.Client == nil || *acct.Client != client {
		return nil, apiError("LOGIN_FAILED", "")
	}
	if acct.Status == statusDisabled {
		return nil, apiError("ACCOUNT_DISABLED", "")
	}
	now := s.now()
	sid := randomID()
	tok, exp, err := s.signer.Sign(s.claimsFor(acct, client, sid), now)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO iam_sessions(session_id, account_id, client, created_at, expires_at) VALUES ($1,$2,$3,$4,$5)`, sid, acct.AccountID, client, now, exp); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_accounts SET last_login_at=$2 WHERE account_id=$1`, acct.AccountID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	acct.LastLoginAt = &now
	return map[string]any{"token": tok, "expiresAt": s.fmtTime(exp), "account": s.toAccount(acct)}, nil
}

// claimsFor 组装令牌声明：岗位、权限点、数据范围、只读、姓名、端。
func (s *Service) claimsFor(a *accountRow, client, sid string) token.Claims {
	return token.Claims{
		Subject: a.AccountID, Name: a.Name, BadgeNo: badgeFor(a, client), Role: a.Role, Client: client, Perms: a.Permissions,
		Scope: a.OperatorScope, Readonly: isReadonly(a.Status), SessionID: sid,
	}
}

// badgeFor：执法证号只进执法端令牌。
func badgeFor(a *accountRow, client string) *string {
	if client == "执法端" {
		return a.BadgeNo
	}
	return nil
}

// handleRefresh 用未过期的旧令牌换新令牌：账号未停用、会话有效才可；旧会话同时作废。
func (s *Service) handleRefresh(w http.ResponseWriter, r *http.Request, p *principal) {
	now := s.now()
	sid := randomID()
	acct := p.acct
	tok, exp, err := s.signer.Sign(s.claimsFor(acct, p.client, sid), now)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `UPDATE iam_sessions SET revoked_at=$2 WHERE session_id=$1 AND revoked_at IS NULL`, p.sessionID, now); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO iam_sessions(session_id, account_id, client, created_at, expires_at) VALUES ($1,$2,$3,$4,$5)`, sid, acct.AccountID, p.client, now, exp); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expiresAt": s.fmtTime(exp), "account": s.toAccount(acct)})
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	// 令牌已失效时也返回成功
	if c, err := s.verifier.Verify(bearer(r), s.now()); err == nil {
		if _, err := s.db.Exec(r.Context(), `UPDATE iam_sessions SET revoked_at=$2 WHERE session_id=$1 AND revoked_at IS NULL`, c.SessionID, s.now()); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// validNewPassword：至少 8 位、同时含字母和数字。
func validNewPassword(p string) bool {
	if len([]rune(p)) < 8 {
		return false
	}
	var letter, digit bool
	for _, c := range p {
		switch {
		case c < unicode.MaxASCII && unicode.IsLetter(c):
			letter = true
		case unicode.IsDigit(c):
			digit = true
		}
	}
	return letter && digit
}

func (s *Service) handleChangePassword(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := requireWritable(p); err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := decodeBody(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	s.idempotent(w, r, p.acct.AccountID, "changePassword", func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		acct, err := scanAccount(tx.QueryRow(ctx, `SELECT `+accountCols+` FROM iam_accounts a JOIN iam_role_templates t USING (role) WHERE a.account_id=$1 FOR UPDATE OF a`, p.acct.AccountID))
		if err != nil {
			return 0, nil, err
		}
		if acct.PasswordHash == nil || bcrypt.CompareHashAndPassword([]byte(*acct.PasswordHash), []byte(req.OldPassword)) != nil {
			return 0, nil, apiError("LOGIN_FAILED", "当前密码不正确")
		}
		if !validNewPassword(req.NewPassword) {
			return 0, nil, apiError("INVALID", "新密码至少 8 位，且同时包含字母和数字")
		}
		if req.NewPassword == req.OldPassword {
			return 0, nil, apiError("INVALID", "新密码不能与当前密码相同")
		}
		h, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_accounts SET password_hash=$2 WHERE account_id=$1`, acct.AccountID, string(h)); err != nil {
			return 0, nil, err
		}
		reason := "本人修改密码"
		err = s.recordOwnAudit(ctx, tx, s.personActor(acct), auditPayload{
			Action: "修改密码", TargetType: "account", TargetID: acct.AccountID,
			Summary: fmt.Sprintf("账号 %s 修改了登录密码", deref(acct.Username)), Reason: &reason,
		})
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]bool{"ok": true}, nil
	})
}

func (s *Service) handleMe(w http.ResponseWriter, _ *http.Request, p *principal) {
	a := s.toAccount(p.acct)
	var op any
	if p.acct.OperatorScope != nil {
		op = map[string]string{"operatorId": *p.acct.OperatorScope}
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": a, "menus": a.Menus, "permissions": a.Permissions, "readonly": p.readonly(), "operator": op})
}

/* ---------- 账号管理 ---------- */

var accountSort = map[string]string{
	"accountId": "a.account_id", "createdAt": "a.created_at", "lastLoginAt": "a.last_login_at",
	"name": "a.name", "username": "a.username", "role": "a.role", "status": "a.status",
}

func pageParams(r *http.Request) (page, size int) {
	page, size = 1, 20
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v >= 1 {
		page = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("pageSize")); err == nil && v >= 1 {
		size = min(v, 200)
	}
	return
}

func sortDir(r *http.Request, def string) string {
	switch strings.ToLower(r.URL.Query().Get("sortDir")) {
	case "asc":
		return "ASC"
	case "desc":
		return "DESC"
	}
	return def
}

func likePattern(q string) string {
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	return "%" + q + "%"
}

func (s *Service) handleListAccounts(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := requirePerm(p, "account.manage"); err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	where := []string{"TRUE"}
	var args []any
	if v := q.Get("role"); v != "" {
		args = append(args, v)
		where = append(where, fmt.Sprintf("a.role=$%d", len(args)))
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		args = append(args, likePattern(v))
		n := len(args)
		where = append(where, fmt.Sprintf("(a.name LIKE $%d OR a.username LIKE $%d OR a.org LIKE $%d)", n, n, n))
	}
	order := "a.account_id ASC"
	if col, ok := accountSort[q.Get("sortKey")]; ok {
		order = col + " " + sortDir(r, "ASC") + " NULLS LAST, a.account_id ASC"
	}
	page, size := pageParams(r)
	cond := strings.Join(where, " AND ")
	ctx := r.Context()
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM iam_accounts a WHERE `+cond, args...).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	args = append(args, size, (page-1)*size)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT %s FROM iam_accounts a JOIN iam_role_templates t USING (role) WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`, accountCols, cond, order, len(args)-1, len(args)), args...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rows.Close()
	list := []Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		list = append(list, s.toAccount(a))
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "page": page, "pageSize": size, "rows": list})
}

type roleTemplate struct {
	Post, IDPrefix, DefaultOrg string
	Loginable                  bool
	Permissions, Menus         []string
}

func (s *Service) handleCreateAccount(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := requirePerm(p, "account.manage"); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := requireWritable(p); err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		Username      string  `json:"username"`
		Name          string  `json:"name"`
		Role          string  `json:"role"`
		Org           string  `json:"org"`
		OperatorScope *string `json:"operatorScope"`
		Reason        string  `json:"reason"`
	}
	if err := decodeBody(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if !validReason(req.Reason) {
		s.fail(w, r, apiError("REASON_REQUIRED", ""))
		return
	}
	if !usernameRe.MatchString(req.Username) {
		s.fail(w, r, apiError("INVALID", "账号名只能用小写字母、数字和点，3～30 位"))
		return
	}
	s.idempotent(w, r, p.acct.AccountID, "createAccount", func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var tpl roleTemplate
		err := tx.QueryRow(ctx, `SELECT post, id_prefix, default_org, loginable, permissions, menus FROM iam_role_templates WHERE role=$1`, req.Role).
			Scan(&tpl.Post, &tpl.IDPrefix, &tpl.DefaultOrg, &tpl.Loginable, &tpl.Permissions, &tpl.Menus)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !tpl.Loginable) {
			return 0, nil, apiError("INVALID", "岗位不存在或不可开通")
		}
		if err != nil {
			return 0, nil, err
		}
		var scope *string
		if operatorRoles[req.Role] {
			if req.OperatorScope == nil || !operatorIDRe.MatchString(*req.OperatorScope) {
				return 0, nil, apiError("INVALID", "运营商账号须指定所属运营商（运营商编号，如 OP01）")
			}
			scope = req.OperatorScope
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam_accounts WHERE username=$1)`, req.Username).Scan(&exists); err != nil {
			return 0, nil, err
		}
		if exists {
			return 0, nil, apiError("INVALID", "账号名已存在")
		}
		status := statusNormal
		if scope != nil {
			exited, err := s.operatorExited(ctx, tx, *scope)
			if err != nil {
				return 0, nil, err
			}
			if exited {
				status = statusReadonly
			}
		}
		pwHash, err := s.initialPasswordHash(ctx, tx)
		if err != nil {
			return 0, nil, err
		}
		id, err := s.nextAccountID(ctx, tx, tpl.IDPrefix)
		if err != nil {
			return 0, nil, err
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = req.Role
		}
		org := strings.TrimSpace(req.Org)
		if org == "" {
			org = tpl.DefaultOrg
		}
		var badge *string
		if req.Role == "执法人员" {
			if m := badgeFromRe.FindStringSubmatch(req.Username); m != nil {
				badge = &m[1]
			}
		}
		now := s.now()
		creator := p.acct.AccountID
		_, err = tx.Exec(ctx, `INSERT INTO iam_accounts(account_id, role, post, name, username, password_hash, org, operator_scope, badge_no, permissions, menus, status, created_at, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			id, req.Role, tpl.Post, name, req.Username, pwHash, org, scope, badge, tpl.Permissions, tpl.Menus, status, now, creator)
		if err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" {
				return 0, nil, apiError("INVALID", "账号名已存在")
			}
			return 0, nil, err
		}
		acct, err := s.getAccount(ctx, tx, id)
		if err != nil {
			return 0, nil, err
		}
		reason := strings.TrimSpace(req.Reason)
		if err := s.recordOwnAudit(ctx, tx, s.personActor(p.acct), auditPayload{
			Action: "开通账号", TargetType: "account", TargetID: id,
			Summary: fmt.Sprintf("开通%s账号 %s", req.Role, req.Username), Reason: &reason,
			After: map[string]any{"role": req.Role, "operatorScope": scope, "status": status},
		}); err != nil {
			return 0, nil, err
		}
		if err := s.publishAccountChanged(ctx, tx, s.personActor(p.acct), acct, "开通"); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, s.toAccount(acct), nil
	})
}

// nextAccountID 在岗位编号段内取下一个：AC + 2 位段号 + 2 位序号。
func (s *Service) nextAccountID(ctx context.Context, tx pgx.Tx, prefix string) (string, error) {
	// 同一段号并发开通时串行
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('iam_account_id_' || $1))`, prefix); err != nil {
		return "", err
	}
	var maxID *string
	if err := tx.QueryRow(ctx, `SELECT max(account_id) FROM iam_accounts WHERE account_id LIKE $1`, "AC"+prefix+"%").Scan(&maxID); err != nil {
		return "", err
	}
	n := 0
	if maxID != nil {
		n, _ = strconv.Atoi((*maxID)[4:])
	}
	if n+1 > 99 {
		return "", apiError("INVALID", "该岗位的账号编号已用完，请联系平台处理")
	}
	return fmt.Sprintf("AC%s%02d", prefix, n+1), nil
}

func (s *Service) initialPasswordHash(ctx context.Context, tx pgx.Tx) (string, error) {
	if s.initialPwdHash != "" {
		return s.initialPwdHash, nil
	}
	var h string
	err := tx.QueryRow(ctx, `SELECT value FROM iam_settings WHERE key='initial_password_hash'`).Scan(&h)
	return h, err
}

func (s *Service) operatorExited(ctx context.Context, q querier, op string) (bool, error) {
	var b bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam_exited_operators WHERE operator_id=$1)`, op).Scan(&b)
	return b, err
}

func (s *Service) handleSetAccountStatus(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := requirePerm(p, "account.manage"); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := requireWritable(p); err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := decodeBody(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if !validReason(req.Reason) {
		s.fail(w, r, apiError("REASON_REQUIRED", ""))
		return
	}
	if req.Status != statusNormal && req.Status != statusDisabled {
		s.fail(w, r, apiError("INVALID", "状态只能是正常或停用"))
		return
	}
	id := r.PathValue("accountId")
	s.idempotent(w, r, p.acct.AccountID, "setAccountStatus:"+id, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		acct, err := scanAccount(tx.QueryRow(ctx, `SELECT `+accountCols+` FROM iam_accounts a JOIN iam_role_templates t USING (role) WHERE a.account_id=$1 FOR UPDATE OF a`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, apiError("NOT_FOUND", "没有找到该账号")
		}
		if err != nil {
			return 0, nil, err
		}
		if !acct.Loginable {
			return 0, nil, apiError("INVALID", "智能体与系统账号不能修改")
		}
		if acct.AccountID == p.acct.AccountID && req.Status == statusDisabled {
			return 0, nil, apiError("INVALID", "不能停用自己的账号")
		}
		target := req.Status
		// 运营商已退出的账号恢复后回到只读
		if target == statusNormal && acct.OperatorScope != nil {
			exited, err := s.operatorExited(ctx, tx, *acct.OperatorScope)
			if err != nil {
				return 0, nil, err
			}
			if exited {
				target = statusReadonly
			}
		}
		before := acct.Status
		if before == target {
			return http.StatusOK, s.toAccount(acct), nil
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `UPDATE iam_accounts SET status=$2 WHERE account_id=$1`, id, target); err != nil {
			return 0, nil, err
		}
		if target == statusDisabled {
			// 已签发的会话同时作废
			if _, err := tx.Exec(ctx, `UPDATE iam_sessions SET revoked_at=$2 WHERE account_id=$1 AND revoked_at IS NULL`, id, now); err != nil {
				return 0, nil, err
			}
		}
		acct.Status = target
		reason := strings.TrimSpace(req.Reason)
		if err := s.recordOwnAudit(ctx, tx, s.personActor(p.acct), auditPayload{
			Action: "调整权限", TargetType: "account", TargetID: id,
			Summary: fmt.Sprintf("账号 %s 状态 %s → %s", deref(acct.Username), before, target), Reason: &reason,
			Before: map[string]any{"status": before}, After: map[string]any{"status": target},
		}); err != nil {
			return 0, nil, err
		}
		change := "启用"
		if target == statusDisabled {
			change = "停用"
		}
		if err := s.publishAccountChanged(ctx, tx, s.personActor(p.acct), acct, change); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, s.toAccount(acct), nil
	})
}

func (s *Service) publishAccountChanged(ctx context.Context, tx pgx.Tx, actor Actor, a *accountRow, change string) error {
	_, err := s.enqueue(ctx, tx, "iam.account.changed", a.AccountID, actor, map[string]any{
		"accountId": a.AccountID, "role": a.Role, "operatorScope": a.OperatorScope,
		"change": change, "status": eventStatus(a.Status), "at": s.fmtTime(s.now()),
	})
	return err
}

func (s *Service) personActor(a *accountRow) Actor {
	name := a.Name
	return Actor{Kind: "人", ID: a.AccountID, Name: &name}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

package app

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"bike/acct/internal/token"
)

var (
	phoneRe = regexp.MustCompile(`^1[3-9]\d{9}$`)
	tailRe  = regexp.MustCompile(`^\d{4,10}$`)
)

// ---------------- 客服：冻结账户列表、按用户编号读用户 ----------------

func (a *App) handleFrozenList(r *http.Request) (any, error) {
	ctx := r.Context()
	qs := r.URL.Query()
	page, _ := strconv.Atoi(qs.Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(qs.Get("pageSize"))
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	kind := qs.Get("kind")
	if kind != "" && kind != "欠费" && kind != "核验" && kind != "其他" {
		return nil, errf("INVALID", "冻结类别只能是 欠费、核验、其他")
	}
	q := strings.TrimSpace(qs.Get("q"))
	if q != "" && !strings.HasPrefix(q, "U") && !tailRe.MatchString(q) {
		return nil, errf("INVALID", "请输入用户编号、手机号或至少 4 位手机尾号")
	}
	dir := "DESC"
	if qs.Get("sortDir") == "asc" {
		dir = "ASC"
	}
	order := "freeze_at " + dir
	switch qs.Get("sortKey") {
	case "frozenDays":
		// 冻结天数越多，冻结时刻越早
		if dir == "DESC" {
			order = "freeze_at ASC"
		} else {
			order = "freeze_at DESC"
		}
	case "userId":
		order = "user_id " + dir
	}
	where := `status='冻结' AND ($1 = '' OR freeze_kind = $1) AND ($2 = '' OR user_id = $2 OR phone = $2 OR (length($2) >= 4 AND $2 ~ '^\d+$' AND phone LIKE '%' || $2))`
	var total int
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE `+where, kind, q).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := a.Pool.Query(ctx, `SELECT `+userCols+` FROM users WHERE `+where+` ORDER BY `+order+`, user_id LIMIT $3 OFFSET $4`,
		kind, q, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	now := a.Now()
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		k := deref(u.FreezeKind)
		var days any
		if u.FreezeAt != nil {
			days = int(now.Sub(*u.FreezeAt) / (24 * time.Hour))
		}
		out = append(out, map[string]any{
			"userId": u.UserID, "name": maskedName(u), "phoneMasked": briefView(u)["phoneMasked"], "channel": u.Channel,
			"freeze": freezeView(u), "kind": k, "autoUnfreeze": k != "其他", "realNameStatus": u.RNStatus, "frozenDays": days,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	counts := map[string]int{"欠费": 0, "核验": 0, "其他": 0}
	crows, err := a.Pool.Query(ctx, `SELECT COALESCE(freeze_kind,'其他'), count(*) FROM users WHERE status='冻结' GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var k string
		var n int
		if err := crows.Scan(&k, &n); err != nil {
			return nil, err
		}
		counts[k] += n
	}
	return map[string]any{"rows": out, "total": total, "page": page, "pageSize": size, "counts": counts}, crows.Err()
}

func adminActor(c token.AdminClaims) actor {
	return actor{Kind: "人", ID: c.AccountID}.withName(c.Name)
}

func (a *App) handleCsUser(r *http.Request) (any, error) {
	ctx := r.Context()
	uid := r.PathValue("userId")
	adm := adminFrom(r)
	var out map[string]any
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		u, err := loadUser(ctx, tx, uid, false)
		if err != nil {
			return err
		}
		if out, err = userView(ctx, tx, u); err != nil {
			return err
		}
		bl, err := cancelBlockers(ctx, tx, uid)
		if err != nil {
			return err
		}
		out["phone"] = u.Phone
		out["lastActiveAt"] = fmtTimePtr(u.LastActiveAt)
		out["degradedSignupEventId"] = u.DegradedSignupEventID
		out["cancelBlockers"] = bl
		// 读取完整手机号留痕
		return a.audit(ctx, tx, adminActor(adm), "查看用户信息", uid, fmt.Sprintf("客服查看用户 %s 的账户信息（含完整手机号）", uid), nil, nil, nil)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------- 内部接口 ----------------

func (a *App) handleLookup(r *http.Request) (any, error) {
	qs := r.URL.Query()
	phone, tail := qs.Get("phone"), qs.Get("phoneTail")
	if (phone == "") == (tail == "") {
		return nil, errf("INVALID", "phone 与 phoneTail 二选一")
	}
	if phone != "" && !phoneRe.MatchString(phone) {
		return nil, errf("INVALID", "手机号须为 11 位")
	}
	if tail != "" && !tailRe.MatchString(tail) {
		return nil, errf("INVALID", "手机尾号至少 4 位数字")
	}
	var rows pgx.Rows
	var err error
	if phone != "" {
		rows, err = a.Pool.Query(r.Context(), `SELECT `+userCols+` FROM users WHERE phone=$1 ORDER BY registered_at DESC LIMIT 20`, phone)
	} else {
		rows, err = a.Pool.Query(r.Context(), `SELECT `+userCols+` FROM users WHERE phone LIKE '%' || $1 ORDER BY registered_at DESC LIMIT 20`, tail)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []map[string]any{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, briefView(u))
	}
	return map[string]any{"users": users}, rows.Err()
}

func (a *App) handleInternalUser(r *http.Request) (any, error) {
	u, err := loadUser(r.Context(), a.Pool, r.PathValue("userId"), false)
	if err != nil {
		return nil, err
	}
	return userView(r.Context(), a.Pool, u)
}

type internalCmd struct {
	Reason   string  `json:"reason"`
	Actor    *actor  `json:"actor"`
	TicketID *string `json:"ticketId"`
}

func parseCmd(r *http.Request) (internalCmd, error) {
	var c internalCmd
	if err := decode(r, &c, false); err != nil {
		return c, err
	}
	c.Reason = strings.TrimSpace(c.Reason)
	if utf8.RuneCountInString(c.Reason) < 4 {
		return c, errf("REASON_REQUIRED", "")
	}
	if c.Actor == nil || c.Actor.ID == "" || c.Actor.Kind == "" {
		return c, errf("INVALID", "缺少操作人 actor")
	}
	return c, nil
}

// handleInternalUnfreeze 人工解冻（客服确认并写事由后由 cs 调用）
func (a *App) handleInternalUnfreeze(r *http.Request) (any, error) {
	ctx := r.Context()
	uid := r.PathValue("userId")
	cmd, err := parseCmd(r)
	if err != nil {
		return nil, err
	}
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		u, err := loadUser(ctx, tx, uid, true)
		if err != nil {
			return err
		}
		if u.Status != "冻结" {
			return errf("CONFLICT", "该账户未冻结")
		}
		before := freezeView(u)
		now := a.Now()
		if _, err := tx.Exec(ctx, `UPDATE users SET status='正常', freeze_kind=NULL, freeze_reason=NULL, freeze_at=NULL, freeze_how=NULL WHERE user_id=$1`, uid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO account_history (user_id, action, at, reason, by_actor) VALUES ($1,'人工解冻',$2,$3,$4)`, uid, now, cmd.Reason, cmd.Actor.ID); err != nil {
			return err
		}
		if err := a.addNotice(ctx, tx, uid, "账户", "账户已解冻", "客服已为你解冻账户，可以继续骑行。", nil); err != nil {
			return err
		}
		if err := a.emit(ctx, tx, "acct.account.unfrozen", uid, *cmd.Actor, map[string]any{"userId": uid, "mode": "人工解冻", "unfrozenAt": fmtTime(now)}); err != nil {
			return err
		}
		summary := fmt.Sprintf("人工解冻账户 %s（原因：%s）", uid, deref(u.FreezeReason))
		if cmd.TicketID != nil && *cmd.TicketID != "" {
			summary += "，工单 " + *cmd.TicketID
		}
		return a.audit(ctx, tx, *cmd.Actor, "解冻账户", uid, summary, &cmd.Reason, before, map[string]any{"status": "正常"})
	})
	if err != nil {
		return nil, err
	}
	u, err := loadUser(ctx, a.Pool, uid, false)
	if err != nil {
		return nil, err
	}
	return userView(ctx, a.Pool, u)
}

func (a *App) handleInternalCancel(r *http.Request) (any, error) {
	cmd, err := parseCmd(r)
	if err != nil {
		return nil, err
	}
	return a.cancelUser(r.Context(), r.PathValue("userId"), cmd.Reason, *cmd.Actor, cmd.TicketID)
}

func (a *App) handleTokenKeys(r *http.Request) (any, error) {
	kids := make([]string, 0, len(a.Cfg.UserPubKeys))
	for k := range a.Cfg.UserPubKeys {
		kids = append(kids, k)
	}
	sort.Strings(kids)
	keys := []map[string]any{}
	for _, k := range kids {
		keys = append(keys, map[string]any{"kid": k, "alg": "EdDSA", "publicKey": token.PublicPEM(a.Cfg.UserPubKeys[k])})
	}
	return map[string]any{"keys": keys}, nil
}

// handlePayeeCheck 只回户名与实名是否一致，实名姓名不出本模块
func (a *App) handlePayeeCheck(r *http.Request) (any, error) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req, false); err != nil {
		return nil, err
	}
	u, err := loadUser(r.Context(), a.Pool, r.PathValue("userId"), false)
	if err != nil {
		return nil, err
	}
	match := u.RNStatus == "已核验" && u.RealName != nil && *u.RealName == strings.TrimSpace(req.Name)
	return map[string]any{"match": match}, nil
}

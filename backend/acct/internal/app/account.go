package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/acct/internal/external"
	"bike/acct/internal/idcard"
)

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// inTx 在事务里执行；提交成功后提醒发布发件表
func (a *App) inTx(ctx context.Context, f func(tx pgx.Tx) error) error {
	err := pgx.BeginFunc(ctx, a.Pool, f)
	if err == nil {
		a.notifyOutbox()
	}
	return err
}

// degradeActive 平台是否处于降级且故障组件含 component；返回降级事件编号
func degradeActive(ctx context.Context, q dbtx, component string) (bool, string, error) {
	var active bool
	var eventID *string
	var comps []string
	err := q.QueryRow(ctx, `SELECT active, event_id, components FROM degrade_state WHERE id=1`).Scan(&active, &eventID, &comps)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if !active {
		return false, "", nil
	}
	for _, c := range comps {
		if c == component {
			return true, deref(eventID), nil
		}
	}
	return false, "", nil
}

// ---------------- 冻结与解冻（R-14） ----------------

// 冻结类别 → 事件里的冻结原因
var freezeEventReason = map[string]string{"欠费": "欠费追缴", "核验": "核验未通过", "其他": "其他"}

// freeze 冻结账户。u 须是本事务里加了行锁读出的最新行。
// 已因欠费冻结、又出现核验问题时改记为核验冻结（解冻条件更严：须核验通过且结清欠费）；其余已冻结的不重复冻结。
func (a *App) freeze(ctx context.Context, tx pgx.Tx, u *userRow, kind, reason, how string, who actor) error {
	if u.Status == "已注销" {
		return nil
	}
	if u.Status == "冻结" && !(deref(u.FreezeKind) == "欠费" && kind == "核验") {
		return nil
	}
	now := a.Now()
	if _, err := tx.Exec(ctx, `UPDATE users SET status='冻结', freeze_kind=$2, freeze_reason=$3, freeze_at=$4, freeze_how=$5 WHERE user_id=$1`,
		u.UserID, kind, reason, now, how); err != nil {
		return err
	}
	u.Status, u.FreezeKind, u.FreezeReason, u.FreezeAt, u.FreezeHow = "冻结", &kind, &reason, &now, &how
	if _, err := tx.Exec(ctx, `INSERT INTO account_history (user_id, action, at, reason, by_actor) VALUES ($1,'冻结',$2,$3,$4)`, u.UserID, now, reason, who.ID); err != nil {
		return err
	}
	if err := a.addNotice(ctx, tx, u.UserID, "账户", "账户已冻结", reason+"。"+how+"。", nil); err != nil {
		return err
	}
	if err := a.emit(ctx, tx, "acct.account.frozen", u.UserID, who, map[string]any{
		"userId": u.UserID, "reason": freezeEventReason[kind], "frozenAt": fmtTime(now), "unfreezeHow": how,
	}); err != nil {
		return err
	}
	return a.audit(ctx, tx, who, "冻结账户", u.UserID, fmt.Sprintf("冻结账户 %s：%s", u.UserID, reason), &reason, nil, map[string]any{"status": "冻结", "reason": reason})
}

// tryAutoUnfreeze 自动解冻：欠费冻结在无欠费时解冻；核验冻结在实名已核验（达到法定年龄）且无欠费时解冻；其他冻结只能人工解冻
func (a *App) tryAutoUnfreeze(ctx context.Context, tx pgx.Tx, u *userRow) error {
	if u.Status != "冻结" {
		return nil
	}
	kind := deref(u.FreezeKind)
	if kind != "欠费" && kind != "核验" {
		return nil
	}
	arr, err := arrearsTotal(ctx, tx, u.UserID)
	if err != nil {
		return err
	}
	if arr > 0 {
		return nil
	}
	reason := "欠费已结清"
	if kind == "核验" {
		if u.RNStatus != "已核验" || u.RNAdult == nil || !*u.RNAdult {
			return nil
		}
		reason = "已完成实名核验并结清欠费"
	}
	now := a.Now()
	before := freezeView(u)
	if _, err := tx.Exec(ctx, `UPDATE users SET status='正常', freeze_kind=NULL, freeze_reason=NULL, freeze_at=NULL, freeze_how=NULL WHERE user_id=$1`, u.UserID); err != nil {
		return err
	}
	u.Status, u.FreezeKind, u.FreezeReason, u.FreezeAt, u.FreezeHow = "正常", nil, nil, nil, nil
	if _, err := tx.Exec(ctx, `INSERT INTO account_history (user_id, action, at, reason, by_actor) VALUES ($1,'自动解冻',$2,$3,$4)`, u.UserID, now, reason, systemActorID); err != nil {
		return err
	}
	if err := a.addNotice(ctx, tx, u.UserID, "账户", "账户已恢复正常", "已满足解冻条件，可以继续骑行。", nil); err != nil {
		return err
	}
	if err := a.emit(ctx, tx, "acct.account.unfrozen", u.UserID, systemActor(), map[string]any{"userId": u.UserID, "mode": "自动解冻", "unfrozenAt": fmtTime(now)}); err != nil {
		return err
	}
	return a.audit(ctx, tx, systemActor(), "解冻账户", u.UserID, fmt.Sprintf("账户 %s 满足条件自动解冻", u.UserID), &reason, before, map[string]any{"status": "正常"})
}

// ---------------- 授权登录（R-03 BR-29） ----------------

type authorizeReq struct {
	Channel  string `json:"channel"`
	AuthCode string `json:"authCode"`
	Phone    string `json:"phone"`
}

func (a *App) handleAuthorize(r *http.Request) (any, error) {
	var req authorizeReq
	if err := decode(r, &req, false); err != nil {
		return nil, err
	}
	if req.Channel != "微信" && req.Channel != "支付宝" {
		return nil, errf("INVALID", "只支持微信或支付宝授权")
	}
	ctx := r.Context()
	phone, err := a.Auth.Phone(ctx, req.Channel, req.AuthCode, req.Phone)
	switch {
	case errors.Is(err, external.ErrUnavailable):
		return nil, &apiError{status: 409, Code: "AUTH_UNAVAILABLE", Message: defaultMessages["AUTH_UNAVAILABLE"], Next: &next{Label: "稍后重试", Action: "retry"}}
	case errors.Is(err, external.ErrInvalid):
		return nil, errf("INVALID", "手机号格式不正确或授权已失效，请重新授权")
	case err != nil:
		return nil, err
	}
	now := a.Now()
	var u *userRow
	isNew := false
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE phone=$1 AND status<>'已注销' FOR UPDATE`, phone))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var uid string
		if u == nil {
			during, _, err := degradeActive(ctx, tx, "实名核验")
			if err != nil {
				return err
			}
			var seq int64
			if err := tx.QueryRow(ctx, `SELECT nextval('user_no_seq')`).Scan(&seq); err != nil {
				return err
			}
			uid = fmt.Sprintf("U%06d", seq)
			tag, err := tx.Exec(ctx, `INSERT INTO users (user_id, phone, channel, registered_at, last_active_at, during_degrade)
				VALUES ($1,$2,$3,$4,$4,$5) ON CONFLICT (phone) WHERE status <> '已注销' DO NOTHING`, uid, phone, req.Channel, now, during)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errf("CONFLICT", "该手机号正在注册，请稍后重试")
			}
			isNew = true
			var channel any // 没有渠道为 null
			if req.Channel != "" {
				channel = req.Channel
			}
			if err := a.emit(ctx, tx, "acct.user.registered", uid, userActor(uid), map[string]any{
				"userId": uid, "channel": channel, "registeredAt": fmtTime(now), "duringDegrade": during,
			}); err != nil {
				return err
			}
			summary := "用户 " + uid + " 通过" + req.Channel + "授权注册"
			if during {
				summary += "（实名核验降级期间，先骑后验）"
			}
			if err := a.audit(ctx, tx, userActor(uid), "注册用户", uid, summary, nil, nil, map[string]any{"status": "正常", "channel": channel}); err != nil {
				return err
			}
		} else {
			uid = u.UserID
			if _, err := tx.Exec(ctx, `UPDATE users SET last_active_at=$2 WHERE user_id=$1`, uid, now); err != nil {
				return err
			}
		}
		u, err = loadUser(ctx, tx, uid, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	tok, _, exp, err := a.issuer.Issue(u.UserID, now)
	if err != nil {
		return nil, err
	}
	view, err := userView(ctx, a.Pool, u)
	if err != nil {
		return nil, err
	}
	return map[string]any{"user": view, "isNew": isNew, "token": tok, "expiresAt": fmtTime(exp)}, nil
}

func (a *App) handleMe(r *http.Request) (any, error) {
	u, err := loadUser(r.Context(), a.Pool, userIDFrom(r), false)
	if err != nil {
		return nil, err
	}
	return userView(r.Context(), a.Pool, u)
}

// ---------------- 实名核验（R-03 R-32 BR-34） ----------------

type verifyReq struct {
	Name string `json:"name"`
	IDNo string `json:"idNo"`
}

func (a *App) underAgeReason() string {
	return fmt.Sprintf("未达法定驾驶年龄（%d 周岁）", a.Cfg.LegalAge)
}

// deferredDeadline 先骑后验补验截止：N 天后当天 23:59:59（北京时间）
func (a *App) deferredDeadline(now time.Time) time.Time {
	d := now.In(cst).AddDate(0, 0, a.Cfg.DeferredDays)
	return time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, cst)
}

// deferredResult 先骑后验返回：截止、紧档限额（取现行规则版本）与开锁前须展示的说明
func (a *App) deferredResult(ctx context.Context, deadline time.Time) (map[string]any, error) {
	out := map[string]any{"status": "待核验", "deferred": true, "deadline": fmtTime(deadline)}
	var mins *int
	var amt *int64
	err := a.Pool.QueryRow(ctx, `SELECT tight_minutes, tight_amount FROM rule_versions WHERE NOT revoked AND effective_at <= $1
		ORDER BY effective_at DESC LIMIT 1`, a.Now()).Scan(&mins, &amt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if mins != nil && amt != nil {
		out["limitMinutes"] = *mins
		out["limitAmount"] = *amt
		out["text"] = fmt.Sprintf("实名核验稍后完成，你可以先骑，本次最长 %d 分钟或 ¥%s。核验未通过将冻结账户并追缴本次费用。", *mins, yuan(*amt))
	} else {
		out["text"] = "实名核验稍后完成，你可以先骑；降级期间单笔骑行有时长与金额上限。核验未通过将冻结账户并追缴本次费用。"
	}
	return out, nil
}

// emitRealName 发布"实名状态变更"并记留痕"实名核验"（摘要不含姓名与证件号）
func (a *App) emitRealName(ctx context.Context, tx pgx.Tx, u *userRow, afterDegrade bool, who actor) error {
	if err := a.emit(ctx, tx, "acct.realname.changed", u.UserID, who, map[string]any{
		"userId": u.UserID, "status": u.RNStatus, "adult": u.RNAdult, "deadline": fmtTimePtr(u.RNDeadline),
		"failReason": u.RNFailReason, "afterDegrade": afterDegrade,
	}); err != nil {
		return err
	}
	summary := fmt.Sprintf("用户 %s 实名核验结果：%s", u.UserID, u.RNStatus)
	if afterDegrade {
		summary = fmt.Sprintf("用户 %s 降级恢复后补验结果：%s", u.UserID, u.RNStatus)
	}
	after := map[string]any{"status": u.RNStatus}
	if u.RNFailReason != nil {
		after["failReason"] = *u.RNFailReason
	}
	if u.RNDeadline != nil {
		after["deadline"] = fmtTime(*u.RNDeadline)
	}
	return a.audit(ctx, tx, who, "核验实名", u.UserID, summary, u.RNFailReason, nil, after)
}

// saveRealName 写实名字段并回读
func saveRealName(ctx context.Context, tx pgx.Tx, u *userRow) error {
	_, err := tx.Exec(ctx, `UPDATE users SET real_name=$2, id_no=$3, rn_status=$4, rn_verified_at=$5, rn_adult=$6, rn_method=$7,
		rn_fail_reason=$8, rn_deadline=$9, degraded_signup_event_id=$10 WHERE user_id=$1`,
		u.UserID, u.RealName, u.IDNo, u.RNStatus, u.RNVerifiedAt, u.RNAdult, u.RNMethod, u.RNFailReason, u.RNDeadline, u.DegradedSignupEventID)
	return err
}

func boolp(b bool) *bool { return &b }

func (a *App) handleVerifyRealName(r *http.Request) (any, error) {
	var req verifyReq
	if err := decode(r, &req, false); err != nil {
		return nil, err
	}
	ctx := r.Context()
	uid := userIDFrom(r)
	u, err := loadUser(ctx, a.Pool, uid, false)
	if err != nil {
		return nil, err
	}
	if u.RNStatus == "已核验" {
		return nil, errf("ALREADY_DONE", "你已完成实名核验")
	}
	name := strings.TrimSpace(req.Name)
	id := idcard.Normalize(req.IDNo)
	if !idcard.ValidName(name) {
		return nil, errf("INVALID", "请填写证件上的中文姓名")
	}
	if !idcard.Valid(id) {
		return nil, errf("REALNAME_INVALID_ID", "")
	}
	now := a.Now()
	wasPending := u.RNStatus == "待核验"
	method := "在线实名核验"
	if wasPending {
		method = "先骑后验补验"
	}
	// 年龄由证件号的出生日期确定，不依赖外部服务
	if idcard.AgeAt(id, now) < a.Cfg.LegalAge {
		err := a.inTx(ctx, func(tx pgx.Tx) error {
			u, err := loadUser(ctx, tx, uid, true)
			if err != nil {
				return err
			}
			var riding bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM riding_orders WHERE user_id=$1)`, uid).Scan(&riding); err != nil {
				return err
			}
			fr := a.underAgeReason()
			u.RealName, u.IDNo, u.RNStatus, u.RNVerifiedAt, u.RNAdult, u.RNMethod, u.RNFailReason, u.RNDeadline = &name, &id, "未通过", &now, boolp(false), &method, &fr, nil
			if err := saveRealName(ctx, tx, u); err != nil {
				return err
			}
			if err := a.emitRealName(ctx, tx, u, false, userActor(uid)); err != nil {
				return err
			}
			// 已用过车的同时冻结（先骑后验用户必然已可骑）
			if wasPending || u.HasRidden || riding {
				prefix := "核验未通过"
				if wasPending {
					prefix = "先骑后验核验未通过"
				}
				return a.freeze(ctx, tx, u, "核验", prefix+"（"+fr+"）", "完成实名核验（达到法定年龄）并结清欠费后自动解冻", systemActor())
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return nil, errf("UNDER_AGE", fmt.Sprintf("未满 %d 周岁，不能骑行电动自行车", a.Cfg.LegalAge))
	}

	verr := a.RealName.Verify(ctx, name, id)
	switch {
	case errors.Is(verr, external.ErrInvalidID):
		return nil, errf("REALNAME_INVALID_ID", "")
	case errors.Is(verr, external.ErrNameMismatch):
		return nil, errf("REALNAME_NAME_MISMATCH", "")
	case errors.Is(verr, external.ErrUnavailable):
		return a.deferRealName(ctx, uid, name, id)
	case verr != nil:
		return nil, verr
	}
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		u, err := loadUser(ctx, tx, uid, true)
		if err != nil {
			return err
		}
		if u.RNStatus == "已核验" {
			return errf("ALREADY_DONE", "你已完成实名核验")
		}
		u.RealName, u.IDNo, u.RNStatus, u.RNVerifiedAt, u.RNAdult, u.RNMethod, u.RNFailReason, u.RNDeadline = &name, &id, "已核验", &now, boolp(true), &method, nil, nil
		if err := saveRealName(ctx, tx, u); err != nil {
			return err
		}
		if err := a.emitRealName(ctx, tx, u, false, userActor(uid)); err != nil {
			return err
		}
		return a.tryAutoUnfreeze(ctx, tx, u)
	})
	if err != nil {
		return nil, err
	}
	u, err = loadUser(ctx, a.Pool, uid, false)
	if err != nil {
		return nil, err
	}
	view, err := userView(ctx, a.Pool, u)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "已核验", "deferred": false, "user": view}, nil
}

// deferRealName 实名服务不可用：平台处于降级且故障组件含实名核验时先骑后验，否则请用户稍后再试
func (a *App) deferRealName(ctx context.Context, uid, name, id string) (any, error) {
	var deadline time.Time
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		active, eventID, err := degradeActive(ctx, tx, "实名核验")
		if err != nil {
			return err
		}
		if !active {
			return &apiError{status: 409, Code: "SERVER_BUSY", Message: "实名核验服务暂时不可用，请稍后再试", Next: &next{Label: "稍后重试", Action: "retry"}}
		}
		u, err := loadUser(ctx, tx, uid, true)
		if err != nil {
			return err
		}
		if u.RNStatus == "待核验" && u.RNDeadline != nil {
			deadline = *u.RNDeadline
			return nil
		}
		deadline = a.deferredDeadline(a.Now())
		m := "先骑后验（降级期间注册）"
		u.RealName, u.IDNo, u.RNStatus, u.RNVerifiedAt, u.RNAdult, u.RNMethod, u.RNFailReason, u.RNDeadline = &name, &id, "待核验", nil, nil, &m, nil, &deadline
		u.DegradedSignupEventID = &eventID
		if err := saveRealName(ctx, tx, u); err != nil {
			return err
		}
		return a.emitRealName(ctx, tx, u, false, userActor(uid))
	})
	if err != nil {
		return nil, err
	}
	return a.deferredResult(ctx, deadline)
}

// reverifyUser 先骑后验补验：用登记的姓名与证件号重做核验；服务仍不可用时保持待核验
func (a *App) reverifyUser(ctx context.Context, uid string) error {
	u, err := loadUser(ctx, a.Pool, uid, false)
	if err != nil {
		return err
	}
	if u.RNStatus != "待核验" || u.IDNo == nil || u.RealName == nil {
		return nil
	}
	verr := a.RealName.Verify(ctx, *u.RealName, *u.IDNo)
	if errors.Is(verr, external.ErrUnavailable) {
		return nil
	}
	var fail string
	adult := (*bool)(nil)
	switch {
	case errors.Is(verr, external.ErrNameMismatch):
		fail = "姓名与证件信息不一致"
	case errors.Is(verr, external.ErrInvalidID):
		fail = "证件号码不正确"
	case verr != nil:
		return verr
	case idcard.AgeAt(*u.IDNo, a.Now()) < a.Cfg.LegalAge:
		fail, adult = a.underAgeReason(), boolp(false)
	}
	return a.inTx(ctx, func(tx pgx.Tx) error {
		u, err := loadUser(ctx, tx, uid, true)
		if err != nil {
			return err
		}
		if u.RNStatus != "待核验" {
			return nil
		}
		now := a.Now()
		u.RNVerifiedAt, u.RNDeadline = &now, nil
		if fail == "" {
			m := "先骑后验（降级期间注册，恢复后补验通过）"
			u.RNStatus, u.RNAdult, u.RNMethod, u.RNFailReason = "已核验", boolp(true), &m, nil
		} else {
			m := "先骑后验补验"
			u.RNStatus, u.RNAdult, u.RNMethod, u.RNFailReason = "未通过", adult, &m, &fail
		}
		if err := saveRealName(ctx, tx, u); err != nil {
			return err
		}
		if err := a.emitRealName(ctx, tx, u, true, systemActor()); err != nil {
			return err
		}
		if fail == "" {
			if err := a.addNotice(ctx, tx, uid, "实名", "实名核验已通过", "你的实名核验已完成，可以正常骑行。", nil); err != nil {
				return err
			}
			return a.tryAutoUnfreeze(ctx, tx, u)
		}
		how := "重新完成实名核验并结清欠费后自动解冻"
		if adult != nil {
			how = "完成实名核验（达到法定年龄）并结清欠费后自动解冻"
		}
		return a.freeze(ctx, tx, u, "核验", "先骑后验核验未通过（"+fail+"）", how, systemActor())
	})
}

// ---------------- 账户页、注销（R-14 R-48） ----------------

func (a *App) selfService(ctx context.Context, u *userRow) ([]map[string]any, error) {
	out := []map[string]any{}
	if u.Status != "冻结" {
		return out, nil
	}
	kind := deref(u.FreezeKind)
	arr, err := arrearsTotal(ctx, a.Pool, u.UserID)
	if err != nil {
		return nil, err
	}
	if kind == "欠费" || arr > 0 {
		out = append(out, map[string]any{"label": "补付欠费", "action": "payArrears"})
	}
	if kind == "核验" && u.RNStatus != "已核验" {
		out = append(out, map[string]any{"label": "重新实名核验", "action": "verifyRealName"})
	}
	if kind == "其他" {
		out = append(out, map[string]any{"label": "联系客服解冻", "action": "contactCS"})
	}
	return out, nil
}

func (a *App) handleAccount(r *http.Request) (any, error) {
	ctx := r.Context()
	u, err := loadUser(ctx, a.Pool, userIDFrom(r), false)
	if err != nil {
		return nil, err
	}
	view, err := userView(ctx, a.Pool, u)
	if err != nil {
		return nil, err
	}
	ss, err := a.selfService(ctx, u)
	if err != nil {
		return nil, err
	}
	bl, err := cancelBlockers(ctx, a.Pool, u.UserID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"user": view, "freeze": freezeView(u), "selfService": ss, "cancelBlockers": bl}, nil
}

func (a *App) handleCancelCheck(r *http.Request) (any, error) {
	bl, err := cancelBlockers(r.Context(), a.Pool, userIDFrom(r))
	if err != nil {
		return nil, err
	}
	return map[string]any{"canCancel": len(bl) == 0, "blockers": bl}, nil
}

// cancelUser 注销（用户自助与客服协助同一规则）：有待处理项时 CANCEL_BLOCKED；成功后该用户全部令牌作废
func (a *App) cancelUser(ctx context.Context, uid, reason string, who actor, ticketID *string) (map[string]any, error) {
	var at time.Time
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		u, err := loadUser(ctx, tx, uid, true)
		if err != nil {
			return err
		}
		if u.Status == "已注销" {
			return errf("ALREADY_DONE", "该账户已注销")
		}
		bl, err := cancelBlockers(ctx, tx, uid)
		if err != nil {
			return err
		}
		if len(bl) > 0 {
			msgs := make([]string, len(bl))
			for i, b := range bl {
				msgs[i] = b.Message
			}
			return &apiError{status: 409, Code: "CANCEL_BLOCKED", Message: "注销前还需处理：" + strings.Join(msgs, "；"), Blockers: bl,
				Next: &next{Label: "逐项处理", Action: "resolveBlockers"}}
		}
		at = a.Now()
		if _, err := tx.Exec(ctx, `UPDATE users SET status='已注销', cancelled_at=$2, tokens_valid_after=$2,
			freeze_kind=NULL, freeze_reason=NULL, freeze_at=NULL, freeze_how=NULL WHERE user_id=$1`, uid, at); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO account_history (user_id, action, at, reason, by_actor) VALUES ($1,'注销',$2,$3,$4)`, uid, at, reason, who.ID); err != nil {
			return err
		}
		if err := a.emit(ctx, tx, "acct.account.cancelled", uid, who, map[string]any{"userId": uid, "cancelledAt": fmtTime(at)}); err != nil {
			return err
		}
		summary := fmt.Sprintf("账户 %s 注销", uid)
		if who.Kind != "用户" {
			summary += "（客服协助）"
		}
		if ticketID != nil && *ticketID != "" {
			summary += "，工单 " + *ticketID
		}
		return a.audit(ctx, tx, who, "注销账户", uid, summary, &reason, map[string]any{"status": u.Status}, map[string]any{"status": "已注销"})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "userId": uid, "cancelledAt": fmtTime(at)}, nil
}

func (a *App) handleCancel(r *http.Request) (any, error) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &req, true); err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "用户申请注销"
	}
	uid := userIDFrom(r)
	return a.cancelUser(r.Context(), uid, reason, userActor(uid), nil)
}

// ---------------- 通知（R-09 BR-22） ----------------

func (a *App) handleNotices(r *http.Request) (any, error) {
	unread := r.URL.Query().Get("unreadOnly") == "true"
	rows, err := a.Pool.Query(r.Context(), `SELECT notice_id, at, kind, title, text, order_id, read FROM notices
		WHERE user_id=$1 AND (NOT $2 OR NOT read) ORDER BY at DESC, notice_id DESC`, userIDFrom(r), unread)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, kind, title, text string
		var at time.Time
		var order *string
		var read bool
		if err := rows.Scan(&id, &at, &kind, &title, &text, &order, &read); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"noticeId": id, "at": fmtTime(at), "kind": kind, "title": title, "text": text, "orderId": order, "read": read})
	}
	return out, rows.Err()
}

func (a *App) handleMarkRead(r *http.Request) (any, error) {
	var req struct {
		IDs *[]string `json:"ids"`
	}
	if err := decode(r, &req, true); err != nil {
		return nil, err
	}
	var n int64
	if req.IDs == nil {
		tag, err := a.Pool.Exec(r.Context(), `UPDATE notices SET read=true WHERE user_id=$1 AND NOT read`, userIDFrom(r))
		if err != nil {
			return nil, err
		}
		n = tag.RowsAffected()
	} else {
		tag, err := a.Pool.Exec(r.Context(), `UPDATE notices SET read=true WHERE user_id=$1 AND NOT read AND notice_id = ANY($2)`, userIDFrom(r), *req.IDs)
		if err != nil {
			return nil, err
		}
		n = tag.RowsAffected()
	}
	return map[string]any{"marked": n}, nil
}

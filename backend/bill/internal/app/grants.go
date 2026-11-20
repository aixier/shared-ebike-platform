package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/bill/internal/billing"
)

// 优惠券自动发放（R-02）：注册券、渠道券（用户注册时）、系统券、定时券（批量，到点一次）、关怀券（开锁失败，见 events_in.go）。
// 类别由券模板的发放方式文字判定，见 billing.KindOf。

// grantTemplate 向用户发一个模板的券（每人张数取 perUser，至少 1）；grantKind 与 (用户, 模板) 去重，已发过返回 0
func (a *App) grantTemplate(ctx context.Context, tx pgx.Tx, userID string, tpl billing.CouponTemplate, grantKind string, at time.Time) (int, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO coupon_grants (user_id, template_id, kind, at) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, userID, tpl.TemplateID, grantKind, at)
	if err != nil || tag.RowsAffected() == 0 {
		return 0, err
	}
	n := maxInt(tpl.PerUser, 1)
	for i := 0; i < n; i++ {
		if _, err := a.issueCoupon(ctx, tx, userID, tpl, "系统发放", at); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// grantRegistration 注册券与渠道券：按注册时刻有效的版本发；规则尚未到达时返回 false，由定时任务补发
func (a *App) grantRegistration(ctx context.Context, tx pgx.Tx, userID, channel string, at time.Time) (bool, error) {
	v, err := currentVersion(ctx, tx, at)
	if errors.Is(err, errNotFound) {
		v, err = currentVersion(ctx, tx, a.Now())
	}
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, tpl := range v.Coupons {
		switch billing.KindOf(tpl, a.Loc) {
		case billing.KindRegister:
		case billing.KindChannel:
			if billing.ChannelOf(tpl) != channel {
				continue
			}
		default:
			continue
		}
		if _, err := a.grantTemplate(ctx, tx, userID, tpl, "注册", a.Now()); err != nil {
			return false, err
		}
	}
	return true, nil
}

type userRegistered struct {
	UserID       string    `json:"userId"`
	Channel      *string   `json:"channel"`
	RegisteredAt time.Time `json:"registeredAt"`
}

// onUserRegistered 用户注册：建投影、记付款渠道，按渠道与注册发放对应优惠券
func (a *App) onUserRegistered(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e userRegistered
	if err := json.Unmarshal(env.Payload, &e); err != nil || e.UserID == "" {
		return nil
	}
	if e.RegisteredAt.IsZero() {
		e.RegisteredAt = env.OccurredAt
	}
	if err := ensureProfile(ctx, tx, e.UserID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE user_profiles SET channel=COALESCE($2, channel), registered_at=COALESCE(registered_at, $3), updated_at=now() WHERE user_id=$1`,
		e.UserID, e.Channel, e.RegisteredAt); err != nil {
		return err
	}
	prof, err := getProfile(ctx, tx, e.UserID)
	if err != nil {
		return err
	}
	done, err := a.grantRegistration(ctx, tx, e.UserID, str(prof.Channel), e.RegisteredAt)
	if err != nil || !done {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE user_profiles SET reg_granted_at=$2 WHERE user_id=$1`, e.UserID, a.Now())
	return err
}

// RunRegistrationGrants 补发规则到达前就注册的用户的注册券与渠道券
func (a *App) RunRegistrationGrants(ctx context.Context) error {
	ids, err := a.idsWhere(ctx, `SELECT user_id FROM user_profiles WHERE registered_at IS NOT NULL AND reg_granted_at IS NULL AND NOT cancelled ORDER BY registered_at LIMIT 200`)
	if err != nil {
		return err
	}
	for _, id := range ids {
		err := inTx(ctx, a.Pool, func(tx pgx.Tx) error {
			var reg time.Time
			var ch *string
			if err := tx.QueryRow(ctx, `SELECT registered_at, channel FROM user_profiles WHERE user_id=$1 AND reg_granted_at IS NULL FOR UPDATE`, id).Scan(&reg, &ch); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil
				}
				return err
			}
			done, err := a.grantRegistration(ctx, tx, id, str(ch), reg)
			if err != nil || !done {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE user_profiles SET reg_granted_at=$2 WHERE user_id=$1`, id, a.Now())
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// RunCouponBatches 系统券（版本生效后一次性）与定时券（到点一次性）：每个模板只发一批，发给当时全部未注销用户
func (a *App) RunCouponBatches(ctx context.Context) error {
	if err := a.RunRegistrationGrants(ctx); err != nil {
		return err
	}
	now := a.Now()
	v, err := currentVersion(ctx, a.Pool, now)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, tpl := range v.Coupons {
		var at time.Time
		kind := billing.KindOf(tpl, a.Loc)
		switch kind {
		case billing.KindSystem:
			at = v.EffectiveAt
		case billing.KindScheduled:
			at, _ = billing.ScheduledAt(tpl, a.Loc)
		default:
			continue
		}
		if at.After(now) {
			continue
		}
		if err := a.issueBatch(ctx, tpl, kind, now); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) issueBatch(ctx context.Context, tpl billing.CouponTemplate, kind string, now time.Time) error {
	return inTx(ctx, a.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO coupon_batches (template_id, kind, issued, done_at) VALUES ($1,$2,0,$3) ON CONFLICT DO NOTHING`, tpl.TemplateID, kind, now)
		if err != nil || tag.RowsAffected() == 0 {
			return err // 别的实例已发或已发过
		}
		rows, err := tx.Query(ctx, `SELECT user_id FROM user_profiles WHERE NOT cancelled ORDER BY user_id`)
		if err != nil {
			return err
		}
		var users []string
		for rows.Next() {
			var u string
			if err := rows.Scan(&u); err != nil {
				rows.Close()
				return err
			}
			users = append(users, u)
		}
		rows.Close()
		total := 0
		for _, u := range users {
			n, err := a.grantTemplate(ctx, tx, u, tpl, "批量", now)
			if err != nil {
				return err
			}
			total += n
		}
		_, err = tx.Exec(ctx, `UPDATE coupon_batches SET issued=$2 WHERE template_id=$1`, tpl.TemplateID, total)
		return err
	})
}

/* ---------------- ride：开锁状态 ---------------- */

const (
	ulOK     = "已开锁"
	ulFailed = "开锁失败"
	ulManual = "待人工处理"
)

func setUnlockState(ctx context.Context, tx pgx.Tx, orderID, state string, reason *string, at time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO order_unlock_states (order_id, state, reason, at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (order_id) DO UPDATE SET state=EXCLUDED.state, reason=EXCLUDED.reason, at=EXCLUDED.at, updated_at=now()`, orderID, state, reason, at)
	return err
}

func unlockStateOf(ctx context.Context, q querier, orderID string) (string, error) {
	var st string
	err := q.QueryRow(ctx, `SELECT state FROM order_unlock_states WHERE order_id=$1`, orderID).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return st, err
}

func (a *App) onUnlocked(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e struct {
		OrderID string    `json:"orderId"`
		StartAt time.Time `json:"startAt"`
	}
	if err := json.Unmarshal(env.Payload, &e); err != nil || e.OrderID == "" {
		return nil
	}
	if e.StartAt.IsZero() {
		e.StartAt = env.OccurredAt
	}
	return setUnlockState(ctx, tx, e.OrderID, ulOK, nil, e.StartAt)
}

func (a *App) onPendingManual(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e struct {
		OrderID string `json:"orderId"`
	}
	if err := json.Unmarshal(env.Payload, &e); err != nil || e.OrderID == "" {
		return nil
	}
	// 已有明确的开锁结果（已开锁、开锁失败）时不降级为待人工
	if st, err := unlockStateOf(ctx, tx, e.OrderID); err != nil || st == ulOK || st == ulFailed {
		return err
	}
	return setUnlockState(ctx, tx, e.OrderID, ulManual, nil, env.OccurredAt)
}

/* ---------------- fleet：运营商退出 ---------------- */

func (a *App) onOperatorExited(ctx context.Context, tx pgx.Tx, env Envelope) error {
	var e struct {
		OperatorID    string     `json:"operatorId"`
		ExitedAt      time.Time  `json:"exitedAt"`
		ReadonlyUntil *time.Time `json:"readonlyUntil"`
	}
	if err := json.Unmarshal(env.Payload, &e); err != nil || e.OperatorID == "" {
		return nil
	}
	if e.ExitedAt.IsZero() {
		e.ExitedAt = env.OccurredAt
	}
	_, err := tx.Exec(ctx, `INSERT INTO exited_operators (operator_id, exited_at, readonly_until) VALUES ($1,$2,$3)
		ON CONFLICT (operator_id) DO UPDATE SET exited_at=EXCLUDED.exited_at, readonly_until=EXCLUDED.readonly_until`, e.OperatorID, e.ExitedAt, e.ReadonlyUntil)
	return err
}

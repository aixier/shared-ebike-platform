package app

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// protoUser 原型 users.json 的用户（只取本模块相关字段）
type protoUser struct {
	UserID       string  `json:"userId"`
	Name         *string `json:"name"`
	Phone        string  `json:"phone"`
	Channel      string  `json:"channel"`
	RegisteredAt string  `json:"registeredAt"`
	LastActiveAt *string `json:"lastActiveAt"`
	RealName     struct {
		Status     string  `json:"status"`
		VerifiedAt *string `json:"verifiedAt"`
		Adult      *bool   `json:"adult"`
		Method     *string `json:"method"`
		FailReason *string `json:"failReason"`
		Deadline   *string `json:"deadline"`
	} `json:"realName"`
	Account struct {
		Status string `json:"status"`
		Freeze *struct {
			Reason      string `json:"reason"`
			At          string `json:"at"`
			UnfreezeHow string `json:"unfreezeHow"`
		} `json:"freeze"`
		History []struct {
			Action string  `json:"action"`
			At     string  `json:"at"`
			Reason *string `json:"reason"`
			By     string  `json:"by"`
		} `json:"history"`
	} `json:"account"`
	CancelledAt           *string `json:"cancelledAt"`
	DegradedSignupEventID *string `json:"degradedSignupEventId"`
}

// 原型时间为北京时间、无时区
func protoTime(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", *s, cst)
	if err != nil {
		return nil
	}
	return &t
}

// seedProtoUser 原型的姓名与证件号只有脱敏值，这里把脱敏姓名当作姓名存、证件号留空
func seedProtoUser(ctx context.Context, pool *pgxpool.Pool, u protoUser) error {
	var kind, reason, how *string
	var at *time.Time
	if f := u.Account.Freeze; f != nil {
		k := freezeKindOf(f.Reason)
		kind, reason, how, at = &k, &f.Reason, &f.UnfreezeHow, protoTime(&f.At)
	}
	reg := protoTime(&u.RegisteredAt)
	_, err := pool.Exec(ctx, `INSERT INTO users (user_id, phone, channel, registered_at, last_active_at, status, cancelled_at, tokens_valid_after,
		real_name, rn_status, rn_verified_at, rn_adult, rn_method, rn_fail_reason, rn_deadline, degraded_signup_event_id, during_degrade,
		freeze_kind, freeze_reason, freeze_at, freeze_how)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		u.UserID, u.Phone, u.Channel, reg, protoTime(u.LastActiveAt), u.Account.Status, protoTime(u.CancelledAt),
		u.Name, u.RealName.Status, protoTime(u.RealName.VerifiedAt), u.RealName.Adult, u.RealName.Method, u.RealName.FailReason,
		protoTime(u.RealName.Deadline), u.DegradedSignupEventID, u.DegradedSignupEventID != nil, kind, reason, at, how)
	if err != nil {
		return err
	}
	for _, h := range u.Account.History {
		if _, err := pool.Exec(ctx, `INSERT INTO account_history (user_id, action, at, reason, by_actor) VALUES ($1,$2,$3,$4,$5)`,
			u.UserID, h.Action, protoTime(&h.At), h.Reason, h.By); err != nil {
			return err
		}
	}
	return nil
}

// freezeKindOf 原型按冻结原因文字归类（只用于导入原型样例）
func freezeKindOf(reason string) string {
	switch {
	case strings.Contains(reason, "欠费"):
		return "欠费"
	case strings.Contains(reason, "核验"):
		return "核验"
	}
	return "其他"
}

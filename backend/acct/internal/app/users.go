package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"bike/acct/internal/idcard"
)

// dbtx 连接池与事务的共同方法
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// userRow users 表一行
type userRow struct {
	UserID                string
	Phone                 string
	Channel               string
	RegisteredAt          time.Time
	LastActiveAt          *time.Time
	DuringDegrade         bool
	Status                string
	CancelledAt           *time.Time
	RealName              *string
	IDNo                  *string
	RNStatus              string
	RNVerifiedAt          *time.Time
	RNAdult               *bool
	RNMethod              *string
	RNFailReason          *string
	RNDeadline            *time.Time
	DegradedSignupEventID *string
	HasRidden             bool
	FreezeKind            *string
	FreezeReason          *string
	FreezeAt              *time.Time
	FreezeHow             *string
}

const userCols = `user_id, phone, channel, registered_at, last_active_at, during_degrade, status, cancelled_at, real_name, id_no,
	rn_status, rn_verified_at, rn_adult, rn_method, rn_fail_reason, rn_deadline, degraded_signup_event_id, has_ridden,
	freeze_kind, freeze_reason, freeze_at, freeze_how`

func scanUser(row pgx.Row) (*userRow, error) {
	u := &userRow{}
	err := row.Scan(&u.UserID, &u.Phone, &u.Channel, &u.RegisteredAt, &u.LastActiveAt, &u.DuringDegrade, &u.Status, &u.CancelledAt,
		&u.RealName, &u.IDNo, &u.RNStatus, &u.RNVerifiedAt, &u.RNAdult, &u.RNMethod, &u.RNFailReason, &u.RNDeadline,
		&u.DegradedSignupEventID, &u.HasRidden, &u.FreezeKind, &u.FreezeReason, &u.FreezeAt, &u.FreezeHow)
	if err != nil {
		return nil, err
	}
	return u, nil
}

var errUserNotFound = errf("NOT_FOUND", "没有找到该用户")

// loadUser 读用户；forUpdate 时加行锁
func loadUser(ctx context.Context, q dbtx, userID string, forUpdate bool) (*userRow, error) {
	sql := `SELECT ` + userCols + ` FROM users WHERE user_id=$1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	u, err := scanUser(q.QueryRow(ctx, sql, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errUserNotFound
	}
	return u, err
}

// ---------------- 视图（对外一律脱敏） ----------------

func maskedName(u *userRow) any {
	if u.RealName == nil || *u.RealName == "" {
		return nil
	}
	return idcard.MaskName(*u.RealName)
}

func realNameView(u *userRow) map[string]any {
	var idMasked any
	if u.IDNo != nil && *u.IDNo != "" {
		idMasked = idcard.MaskID(*u.IDNo)
	}
	return map[string]any{
		"status":     u.RNStatus,
		"verifiedAt": fmtTimePtr(u.RNVerifiedAt),
		"idMasked":   idMasked,
		"adult":      u.RNAdult,
		"method":     u.RNMethod,
		"failReason": u.RNFailReason,
		"deadline":   fmtTimePtr(u.RNDeadline),
	}
}

func freezeView(u *userRow) any {
	if u.Status != "冻结" || u.FreezeReason == nil {
		return nil
	}
	return map[string]any{"reason": *u.FreezeReason, "at": fmtTimePtr(u.FreezeAt), "unfreezeHow": deref(u.FreezeHow)}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func accountHistory(ctx context.Context, q dbtx, userID string) ([]map[string]any, error) {
	rows, err := q.Query(ctx, `SELECT action, at, reason, by_actor FROM account_history WHERE user_id=$1 ORDER BY at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var action, by string
		var at time.Time
		var reason *string
		if err := rows.Scan(&action, &at, &reason, &by); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"action": action, "at": fmtTime(at), "reason": reason, "by": by})
	}
	return out, rows.Err()
}

// userView acct.yaml User
func userView(ctx context.Context, q dbtx, u *userRow) (map[string]any, error) {
	hist, err := accountHistory(ctx, q, u.UserID)
	if err != nil {
		return nil, err
	}
	var unread int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM notices WHERE user_id=$1 AND NOT read`, u.UserID).Scan(&unread); err != nil {
		return nil, err
	}
	return map[string]any{
		"userId":        u.UserID,
		"name":          maskedName(u),
		"phoneMasked":   idcard.MaskPhone(u.Phone),
		"channel":       u.Channel,
		"registeredAt":  fmtTime(u.RegisteredAt),
		"realName":      realNameView(u),
		"account":       map[string]any{"status": u.Status, "freeze": freezeView(u), "history": hist},
		"cancelledAt":   fmtTimePtr(u.CancelledAt),
		"unreadNotices": unread,
	}, nil
}

func briefView(u *userRow) map[string]any {
	return map[string]any{"userId": u.UserID, "name": maskedName(u), "phoneMasked": idcard.MaskPhone(u.Phone), "channel": u.Channel, "accountStatus": u.Status}
}

// ---------------- 注销前待处理项（依据订阅 ride、bill、deposit 得到的投影） ----------------

type blocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Next    *next  `json:"next,omitempty"`
	OrderID string `json:"orderId,omitempty"`
	Amount  *int64 `json:"amount,omitempty"`
}

// yuan 分 → 元的显示文本：450 → 4.5，1105 → 11.05
func yuan(fen int64) string {
	sign := ""
	if fen < 0 {
		sign, fen = "-", -fen
	}
	if fen%10 == 0 {
		return fmt.Sprintf("%s%d.%d", sign, fen/100, (fen%100)/10)
	}
	return fmt.Sprintf("%s%d.%02d", sign, fen/100, fen%100)
}

func arrearsTotal(ctx context.Context, q dbtx, userID string) (int64, error) {
	var total int64
	err := q.QueryRow(ctx, `SELECT COALESCE(sum(amount),0) FROM arrears_orders WHERE user_id=$1`, userID).Scan(&total)
	return total, err
}

func cancelBlockers(ctx context.Context, q dbtx, userID string) ([]blocker, error) {
	out := []blocker{}
	var riding string
	err := q.QueryRow(ctx, `SELECT order_id FROM riding_orders WHERE user_id=$1 ORDER BY created_at LIMIT 1`, userID).Scan(&riding)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if riding != "" {
		out = append(out, blocker{Code: "HAS_RIDING", Message: "有一单正在骑行，还车后再处理", Next: &next{Label: "查看骑行中订单", Action: "goRide"}, OrderID: riding})
	}
	arr, err := arrearsTotal(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	if arr > 0 {
		out = append(out, blocker{Code: "HAS_ARREARS", Message: "有 ¥" + yuan(arr) + " 欠费未结清", Next: &next{Label: "一键补付", Action: "payArrears"}, Amount: &arr})
	}
	var unpaidN int
	var firstUnpaid *string
	var firstAmount *int64
	err = q.QueryRow(ctx, `SELECT count(*) OVER (), order_id, payable FROM order_bills WHERE user_id=$1 AND NOT paid AND NOT in_arrears
		AND collect_by='一键支付' AND payable > 0 ORDER BY pay_deadline NULLS LAST LIMIT 1`, userID).Scan(&unpaidN, &firstUnpaid, &firstAmount)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if unpaidN > 0 {
		out = append(out, blocker{Code: "UNPAID_ORDER", Message: fmt.Sprintf("有 %d 单待支付", unpaidN), Next: &next{Label: "去支付", Action: "payOrder"}, OrderID: deref(firstUnpaid), Amount: firstAmount})
	}
	var mode string
	var status *string
	var balance *int64
	err = q.QueryRow(ctx, `SELECT mode, status, balance FROM user_deposit WHERE user_id=$1`, userID).Scan(&mode, &status, &balance)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if mode == "押金" && status != nil {
		bal := int64(0)
		if balance != nil {
			bal = *balance
		}
		switch *status {
		case "已缴", "押金不足":
			if bal > 0 {
				out = append(out, blocker{Code: "DEPOSIT_NOT_REFUNDED", Message: "押金 ¥" + yuan(bal) + " 未退", Next: &next{Label: "申请退押", Action: "refundDeposit"}})
			}
		case "退押中":
			out = append(out, blocker{Code: "DEPOSIT_REFUNDING", Message: "押金正在退还，到账后即可注销", Next: &next{Label: "查看押金", Action: "viewDeposit"}})
		}
	}
	return out, nil
}

// ---------------- 发件表、留痕、通知 ----------------

type actor struct {
	Kind string  `json:"kind"`
	ID   string  `json:"id"`
	Name *string `json:"name"`
}

func systemActor() actor        { return actor{Kind: "系统", ID: systemActorID} }
func userActor(id string) actor { return actor{Kind: "用户", ID: id} }
func (x actor) withName(n string) actor {
	if n != "" {
		x.Name = &n
	}
	return x
}

// emit 在业务事务里写发件表（信封见 contracts/events/README.md 第 1 节）
func (a *App) emit(ctx context.Context, tx dbtx, typ, key string, who actor, payload map[string]any) error {
	id := "acct-" + newID()
	env := map[string]any{"eventId": id, "type": typ, "version": 1, "occurredAt": fmtTime(a.Now()), "key": key, "actor": who, "payload": payload}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (event_id, subject, body, created_at) VALUES ($1,$2,$3,$4)`, id, typ, b, a.Now())
	return err
}

// audit 留痕已记录（R-47），由 iam 保存
func (a *App) audit(ctx context.Context, tx dbtx, who actor, action, userID, summary string, reason *string, before, after any) error {
	return a.emit(ctx, tx, "acct.audit.recorded", "user:"+userID, who, map[string]any{
		"action": action, "targetType": "user", "targetId": userID, "summary": summary, "reason": reason, "before": before, "after": after,
	})
}

// addNotice 给用户发一条通知
func (a *App) addNotice(ctx context.Context, tx dbtx, userID, kind, title, text string, orderID *string) error {
	now := a.Now()
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT nextval('notice_no_seq')`).Scan(&seq); err != nil {
		return err
	}
	id := fmt.Sprintf("NT%s%04d", now.In(cst).Format("20060102"), seq)
	_, err := tx.Exec(ctx, `INSERT INTO notices (notice_id, user_id, at, kind, title, text, order_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, userID, now, kind, title, text, orderID)
	return err
}

func strp(s string) *string { return &s }

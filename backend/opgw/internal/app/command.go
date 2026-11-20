package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"bike/opgw/internal/clients/operator"
)

// CommandResult 返回给 ride 的同步答复（opgw.yaml 的 CommandResult）。
type CommandResult struct {
	Result          string  `json:"result"`
	RejectReason    *string `json:"rejectReason"`
	OperatorMessage *string `json:"operatorMessage"`
	At              string  `json:"at"`
	CostMs          int64   `json:"costMs"`
}

type commandBody struct {
	OrderID    string  `json:"orderId"`
	OperatorID string  `json:"operatorId"`
	BrandID    string  `json:"brandId"`
	VehicleID  *string `json:"vehicleId"`
	PlateNo    string  `json:"plateNo"`
	Degraded   *bool   `json:"degraded"`
	Cause      string  `json:"cause"`
	Reason     string  `json:"reason"`
}

// 结束原因 → 发给运营商的幂等键后缀（请求头只用 ASCII）
var endCauses = map[string]string{"用户点还车": "user", "入桩未锁": "pile", "超时后才报开锁成功": "late", "客服强制结束": "force"}

var rejectReasons = map[string]bool{"电量不足": true, "车辆故障": true, "车辆被占用": true, "订单不存在": true, "其他": true}

func (a *App) handleUnlock(w http.ResponseWriter, r *http.Request)  { a.handleCommand(w, r, true) }
func (a *App) handleEndRide(w http.ResponseWriter, r *http.Request) { a.handleCommand(w, r, false) }

func (a *App) handleCommand(w http.ResponseWriter, r *http.Request, unlock bool) {
	ctx := r.Context()
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 64 {
		writeErr(w, http.StatusBadRequest, "INVALID", "缺少幂等键 Idempotency-Key")
		return
	}
	scope := "end-ride"
	if unlock {
		scope = "unlock"
	}
	if hit, err := a.idemGet(ctx, scope, key); err != nil {
		serverBusy(w)
		return
	} else if hit != nil {
		writeRaw(w, hit.Status, hit.Body)
		return
	}
	raw, _ := readBody(r)
	var b commandBody
	if err := json.Unmarshal(raw, &b); err != nil || b.OrderID == "" || b.OperatorID == "" || b.BrandID == "" || b.PlateNo == "" {
		writeErr(w, http.StatusBadRequest, "INVALID", "缺少订单号、运营商、品牌或车号")
		return
	}
	kind := "开锁"
	if unlock {
		if b.Degraded == nil {
			writeErr(w, http.StatusBadRequest, "INVALID", "缺少降级标记 degraded")
			return
		}
	} else {
		if endCauses[b.Cause] == "" {
			writeErr(w, http.StatusBadRequest, "INVALID", "结束原因不在规定取值内")
			return
		}
		b.Reason = strings.TrimSpace(b.Reason)
		if b.Cause == "客服强制结束" && utf8.RuneCountInString(b.Reason) < 4 {
			writeErr(w, http.StatusBadRequest, "REASON_REQUIRED", "客服强制结束须填写事由，至少 4 个字")
			return
		}
		kind = "结束骑行:" + b.Cause
	}

	// 同一订单同一指令跨实例串行
	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		serverBusy(w)
		return
	}
	defer conn.Release()
	lockKey := b.OrderID + "|" + kind
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1))`, lockKey); err != nil {
		serverBusy(w)
		return
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, lockKey)

	// 同一订单已有运营商答复（已受理或拒绝）时不再发，答复与首次一致
	var prev []byte
	err = conn.QueryRow(ctx, `SELECT result FROM commands WHERE order_id=$1 AND kind=$2`, b.OrderID, kind).Scan(&prev)
	if err == nil {
		var pr CommandResult
		if json.Unmarshal(prev, &pr) == nil && pr.Result != "不可达" {
			_ = a.idemPut(ctx, a.DB, scope, key, http.StatusOK, prev)
			writeRaw(w, http.StatusOK, prev)
			return
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		serverBusy(w)
		return
	}

	creds, err := a.credsCovering(ctx, a.DB, b.OperatorID)
	if err != nil {
		serverBusy(w)
		return
	}
	if len(creds) == 0 || creds[0].Status != "有效" {
		msg := "该运营商的生产凭证已停用，平台不再向其发送指令"
		if len(creds) == 0 {
			msg = "该运营商没有生产凭证"
		}
		writeErr(w, http.StatusConflict, "CREDENTIAL_DISABLED", msg)
		return
	}
	cred := creds[0]
	secret, err := a.secretOf(cred)
	if err != nil {
		serverBusy(w)
		return
	}
	target := operator.Target{BaseURL: cred.BaseURL, AppID: cred.AppID, Secret: secret}
	start := a.Now()
	reqAt := rfc3339(start)
	var rep operator.Reply
	var callErr error
	if unlock {
		rep, callErr = a.Op.Unlock(ctx, target, "unlock-"+b.OrderID, operator.UnlockRequest{OrderID: b.OrderID, PlateNo: b.PlateNo, Degraded: *b.Degraded, RequestAt: reqAt})
	} else {
		rep, callErr = a.Op.EndRide(ctx, target, "end-"+b.OrderID+"-"+endCauses[b.Cause], operator.EndRideRequest{OrderID: b.OrderID, PlateNo: b.PlateNo, Cause: b.Cause, Reason: b.Reason, RequestAt: reqAt})
	}
	end := a.Now()
	res := CommandResult{At: rfc3339(end), CostMs: end.Sub(start).Milliseconds()}
	if callErr != nil {
		res.Result = "不可达"
		m := callErr.Error()
		res.OperatorMessage = &m
	} else {
		res.Result = rep.Result
		if rep.Result == "拒绝" {
			rr := rep.RejectReason
			if !rejectReasons[rr] {
				rr = "其他"
			}
			res.RejectReason = &rr
		}
		if rep.Message != "" {
			res.OperatorMessage = &rep.Message
		}
	}
	body, _ := json.Marshal(res)

	tx, err := a.DB.Begin(ctx)
	if err != nil {
		serverBusy(w)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO commands (order_id, kind, operator_id, brand_id, vehicle_id, plate_no, credential_id, result, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) ON CONFLICT (order_id, kind) DO UPDATE SET result=EXCLUDED.result, updated_at=EXCLUDED.updated_at`,
		b.OrderID, kind, b.OperatorID, b.BrandID, b.VehicleID, b.PlateNo, cred.CredentialID, body, a.Now()); err != nil {
		serverBusy(w)
		return
	}
	errText := ""
	if callErr != nil {
		errText = callErr.Error()
	}
	if err := a.recordReachability(ctx, tx, b.OperatorID, b.BrandID, callErr == nil, errText, start); err != nil {
		serverBusy(w)
		return
	}
	if err := a.idemPut(ctx, tx, scope, key, http.StatusOK, body); err != nil {
		serverBusy(w)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		serverBusy(w)
		return
	}
	writeRaw(w, http.StatusOK, body)
}

// recordReachability 连续失败达到阈值发"运营商不可达"，之后首次成功发"运营商已恢复"。
func (a *App) recordReachability(ctx context.Context, q querier, operatorID, brandID string, ok bool, errText string, at time.Time) error {
	var fails int
	var unreachable bool
	var since *time.Time
	err := q.QueryRow(ctx, `SELECT consecutive_failures, unreachable, since FROM operator_health WHERE operator_id=$1 FOR UPDATE`, operatorID).Scan(&fails, &unreachable, &since)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := q.Exec(ctx, `INSERT INTO operator_health (operator_id, updated_at) VALUES ($1,$2) ON CONFLICT DO NOTHING`, operatorID, a.Now()); err != nil {
			return err
		}
		err = q.QueryRow(ctx, `SELECT consecutive_failures, unreachable, since FROM operator_health WHERE operator_id=$1 FOR UPDATE`, operatorID).Scan(&fails, &unreachable, &since)
	}
	if err != nil {
		return err
	}
	brands, err := a.brandsOf(ctx, q, operatorID, brandID)
	if err != nil {
		return err
	}
	if ok {
		if unreachable {
			if err := a.enqueue(ctx, q, "opgw.operator.recovered", operatorID, systemActor, map[string]any{
				"operatorId": operatorID, "brandIds": brands, "recoveredAt": rfc3339(a.Now()),
			}); err != nil {
				return err
			}
		}
		_, err := q.Exec(ctx, `UPDATE operator_health SET consecutive_failures=0, unreachable=false, since=NULL, last_error=NULL, updated_at=$2 WHERE operator_id=$1`, operatorID, a.Now())
		return err
	}
	fails++
	if since == nil {
		since = &at
	}
	becomes := !unreachable && fails >= a.Cfg.UnreachableAfter
	if becomes {
		if err := a.enqueue(ctx, q, "opgw.operator.unreachable", operatorID, systemActor, map[string]any{
			"operatorId": operatorID, "brandIds": brands, "since": rfc3339(*since), "lastError": errText,
		}); err != nil {
			return err
		}
	}
	_, err = q.Exec(ctx, `UPDATE operator_health SET consecutive_failures=$2, unreachable=$3, since=$4, last_error=$5, updated_at=$6 WHERE operator_id=$1`,
		operatorID, fails, unreachable || becomes, since, errText, a.Now())
	return err
}

func (a *App) brandsOf(ctx context.Context, q querier, operatorID, extra string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT brand_id FROM brands WHERE operator_id=$1 AND access_status<>'已退出' ORDER BY brand_id`, operatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
		seen[b] = true
	}
	if extra != "" && !seen[extra] {
		out = append(out, extra)
	}
	return out, rows.Err()
}

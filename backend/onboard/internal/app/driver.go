package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bike/onboard/internal/sign"
)

// 测试驱动：按接入规范向运营商联调地址发 M-01、M-02。签名用该品牌的联调凭证（两个方向同一把密钥）。

type signedReq struct {
	URL     string
	Headers http.Header
	Body    []byte
}

type callResult struct {
	Status       int
	Result       string // 已受理｜拒绝
	RejectReason string
	Elapsed      time.Duration
	Err          error
	Req          *signedReq
}

func (c callResult) accepted() bool {
	return c.Err == nil && c.Status == 200 && c.Result == "已受理"
}

// describe 用一句话说出答复的事实。
func (c callResult) describe(limit time.Duration) string {
	switch {
	case c.Err != nil:
		return fmt.Sprintf("请求在 %s内没有得到答复", fmtDur(limit))
	case c.Status != 200:
		return fmt.Sprintf("返回 HTTP %d", c.Status)
	case c.Result == "拒绝":
		return fmt.Sprintf("答复拒绝（%s）", c.RejectReason)
	case c.Result != "已受理":
		return fmt.Sprintf("答复的 result 为 %q，规范要求 已受理 或 拒绝", c.Result)
	}
	return "答复已受理"
}

type signOpts struct {
	AppID, Secret string    // 为空用联调凭证
	At            time.Time // 为零用当前时刻
	Nonce         string
	Tamper        func([]byte) []byte // 签名后改动报文
}

// prepare 组装并签名一个请求。
func prepare(baseURL, path string, body any, idemKey string, cred *credRow, o signOpts, now time.Time) (*signedReq, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + path)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(body)
	appID, secret := cred.AppID, cred.Secret
	if o.AppID != "" {
		appID, secret = o.AppID, o.Secret
	}
	at := now
	if !o.At.IsZero() {
		at = o.At
	}
	nonce := o.Nonce
	if nonce == "" {
		nonce = sign.NewNonce()
	}
	ts := strconv.FormatInt(at.Unix(), 10)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-App-Id", appID)
	h.Set("X-Timestamp", ts)
	h.Set("X-Nonce", nonce)
	h.Set("X-Signature", sign.Sign(secret, http.MethodPost, u.Path, appID, ts, nonce, b))
	if idemKey != "" {
		h.Set("Idempotency-Key", idemKey)
	}
	if o.Tamper != nil {
		b = o.Tamper(b)
	}
	return &signedReq{URL: u.String(), Headers: h, Body: b}, nil
}

// send 发出请求，记录答复与用时。
func (a *App) send(ctx context.Context, req *signedReq) callResult {
	res := callResult{Req: req}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		res.Err = err
		return res
	}
	hr.Header = req.Headers.Clone()
	start := time.Now()
	resp, err := a.operatorHTTP().Do(hr)
	res.Elapsed = time.Since(start)
	if err != nil {
		res.Err = err
		return res
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var reply struct {
		Result       string `json:"result"`
		RejectReason string `json:"rejectReason"`
	}
	if resp.StatusCode == 200 {
		_ = json.Unmarshal(data, &reply)
	}
	res.Result, res.RejectReason = reply.Result, reply.RejectReason
	return res
}

// newOrderID 联调环境订单号：OD + 日期 + 5 位序号（编号里的日期只是标签）。
func (a *App) newOrderID(ctx context.Context) (string, error) {
	var n int64
	if err := a.DB.QueryRow(ctx, `SELECT nextval('sandbox_order_seq')`).Scan(&n); err != nil {
		return "", err
	}
	return fmt.Sprintf("OD%s%05d", a.now().Format("20060102"), n%100000), nil
}

type sbOrder struct {
	ID       string
	Brand    string
	Plate    string
	Degraded bool
	Idem     string
	ReqAt    time.Time
	First    callResult
}

// createOrder 建一笔联调环境订单。
func (a *App) createOrder(ctx context.Context, brand, kind, runID, plate, simUser string, degraded, rejectFirstLock bool) (*sbOrder, error) {
	id, err := a.newOrderID(ctx)
	if err != nil {
		return nil, err
	}
	var run, user *string
	if runID != "" {
		run = &runID
	}
	if simUser != "" {
		user = &simUser
	}
	_, err = a.DB.Exec(ctx, `INSERT INTO sandbox_orders (order_id, brand_id, kind, run_id, plate_no, sim_user_id, degraded, reject_first_lock)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, id, brand, kind, run, plate, user, degraded, rejectFirstLock)
	if err != nil {
		return nil, err
	}
	return &sbOrder{ID: id, Brand: brand, Plate: plate, Degraded: degraded, Idem: "unlock-" + id}, nil
}

func unlockBody(o *sbOrder, at time.Time) map[string]any {
	return map[string]any{"orderId": o.ID, "plateNo": o.Plate, "degraded": o.Degraded, "requestAt": at.Format(time.RFC3339)}
}

// unlock 发 M-01 并记录首次答复。
func (a *App) unlock(ctx context.Context, baseURL string, cred *credRow, o *sbOrder) (callResult, error) {
	now := a.now()
	if o.ReqAt.IsZero() {
		o.ReqAt = now
	}
	req, err := prepare(baseURL, "/v1/unlock", unlockBody(o, o.ReqAt), o.Idem, cred, signOpts{}, now)
	if err != nil {
		return callResult{Err: err}, err
	}
	res := a.send(ctx, req)
	o.First = res
	reply := res.Result
	if res.Err != nil {
		reply = "无答复"
	} else if res.Status != 200 {
		reply = fmt.Sprintf("HTTP %d", res.Status)
	}
	ms := int(res.Elapsed.Milliseconds())
	_, err = a.DB.Exec(ctx, `UPDATE sandbox_orders SET unlock_requested_at=$2, unlock_reply=$3, unlock_reject=NULLIF($4,''), unlock_reply_ms=$5 WHERE order_id=$1`,
		o.ID, o.ReqAt, reply, res.RejectReason, ms)
	return res, err
}

// resendUnlock 原样重发同一订单的开锁请求（同一幂等键与报文，新的时间戳与随机数）。
func (a *App) resendUnlock(ctx context.Context, baseURL string, cred *credRow, o *sbOrder) callResult {
	req, err := prepare(baseURL, "/v1/unlock", unlockBody(o, o.ReqAt), o.Idem, cred, signOpts{}, a.now())
	if err != nil {
		return callResult{Err: err}
	}
	return a.send(ctx, req)
}

// endRide 发 M-02。
func (a *App) endRide(ctx context.Context, baseURL string, cred *credRow, o *sbOrder, cause, reason string) callResult {
	now := a.now()
	body := map[string]any{"orderId": o.ID, "plateNo": o.Plate, "cause": cause, "requestAt": now.Format(time.RFC3339)}
	if reason != "" {
		body["reason"] = reason
	}
	req, err := prepare(baseURL, "/v1/end-ride", body, "end-"+o.ID, cred, signOpts{}, now)
	if err != nil {
		return callResult{Err: err}
	}
	res := a.send(ctx, req)
	reply := res.Result
	if res.Err != nil {
		reply = "无答复"
	} else if res.Status != 200 {
		reply = fmt.Sprintf("HTTP %d", res.Status)
	}
	_, _ = a.DB.Exec(ctx, `UPDATE sandbox_orders SET end_requested_at=$2, end_cause=$3, end_reply=$4 WHERE order_id=$1`, o.ID, now, cause, reply)
	return res
}

// delivery 运营商的一次 M-03 投递。
type delivery struct {
	EventID    string
	Type       string
	OccurredAt time.Time
	ReceivedAt time.Time
	Outcome    string
	Body       RideEvent
	Raw        map[string]any
}

func (a *App) deliveries(ctx context.Context, orderID string) ([]delivery, error) {
	rows, err := a.DB.Query(ctx, `SELECT event_id, type, occurred_at, received_at, outcome, body FROM sandbox_deliveries WHERE order_id=$1 ORDER BY id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []delivery
	for rows.Next() {
		var d delivery
		var raw []byte
		if err := rows.Scan(&d.EventID, &d.Type, &d.OccurredAt, &d.ReceivedAt, &d.Outcome, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &d.Body)
		_ = json.Unmarshal(raw, &d.Raw)
		out = append(out, d)
	}
	return out, rows.Err()
}

// firstAccepted 某类事件第一条被平台收下的投递。
func firstAccepted(ds []delivery, typ string) *delivery {
	for i := range ds {
		if ds[i].Type == typ && ds[i].Outcome == "已收到" {
			return &ds[i]
		}
	}
	return nil
}

// waitFor 轮询联调记录，直到某类事件被收下或到达截止时刻。
func (a *App) waitFor(ctx context.Context, orderID, typ string, deadline time.Time) *delivery {
	for {
		ds, err := a.deliveries(ctx, orderID)
		if err == nil {
			if d := firstAccepted(ds, typ); d != nil {
				return d
			}
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(a.Cfg.Poll):
		}
	}
}

func sleepUntil(ctx context.Context, t time.Time) {
	d := time.Until(t)
	if d <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// fmtDur 把时长写成人读的秒数。
func fmtDur(d time.Duration) string {
	s := d.Seconds()
	if s >= 10 || s == float64(int(s)) {
		return fmt.Sprintf("%d 秒", int(s+0.5))
	}
	return fmt.Sprintf("%.1f 秒", s)
}

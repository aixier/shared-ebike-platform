// Package fakes 是本队自用的依赖替身：按 ride、bill、acct、deposit 的 swagger 返回，只实现本服务调用的 /internal 接口。
// 用于单元测试与对方未交付时的本机联调；不进生产装配。
// trace: backend/cs/任务列表.md 第 13 项；contracts/openapi/ride.yaml、bill.yaml、acct.yaml、deposit.yaml
package fakes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Order 是替身里的订单（行程部分）。
type Order struct {
	OrderID        string     `json:"orderId"`
	UserID         string     `json:"userId"`
	OperatorID     string     `json:"operatorId"`
	VehicleID      string     `json:"vehicleId"`
	BrandID        string     `json:"brandId"`
	PlateNo        string     `json:"plateNo"`
	Status         string     `json:"status"`
	RequestAt      time.Time  `json:"requestAt"`
	StartAt        *time.Time `json:"startAt"`
	EndAt          *time.Time `json:"endAt"`
	EndMethod      *string    `json:"endMethod"`
	DistanceKm     *float64   `json:"distanceKm"`
	InStation      *bool      `json:"inStation"`
	InStationBasis *string    `json:"inStationBasis"`
}

// Bill 是替身里的账单事实（分）。
type Bill struct {
	OrderID       string
	UserID        string
	BilledMinutes *int64
	Receivable    int64
	Payable       int64
	ChannelPaid   int64
	Refunded      int64
	HasFee        bool
	// RefundMode 控制下一笔退款结果：""=成功，"channel"=原路失败，"unknown"=结果未知
	RefundMode string
}

// User 是替身里的用户。
type User struct {
	UserID        string
	Phone         string
	AccountStatus string // 正常｜冻结｜已注销
	// CancelBlockers 非空时协助注销返回 CANCEL_BLOCKED
	CancelBlockers []map[string]any
	// Deposit：""=无押金（NOT_DEPOSIT_USER），"ok"=可退，"blocked"=有骑行中订单
	Deposit       string
	DepositAmount int64
}

// Call 记录替身收到的命令。
type Call struct {
	Service, Path, IdemKey, Caller string
	Body                           map[string]any
}

// World 是四个替身共享的世界。
type World struct {
	mu         sync.Mutex
	ServiceKey string
	Orders     map[string]*Order
	Bills      map[string]*Bill
	Users      map[string]*User
	Calls      []Call
	idem       map[string][]byte
	paySeq     int
	// Down 为 true 的服务一律 500
	Down map[string]bool
}

// NewWorld 建一个空世界；serviceKey 是 cs 调用时应带的凭证。
func NewWorld(serviceKey string) *World {
	return &World{ServiceKey: serviceKey, Orders: map[string]*Order{}, Bills: map[string]*Bill{}, Users: map[string]*User{}, idem: map[string][]byte{}, Down: map[string]bool{}}
}

// CallsTo 返回发到某个路径前缀的命令。
func (w *World) CallsTo(prefix string) []Call {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []Call
	for _, c := range w.Calls {
		if strings.HasPrefix(c.Path, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func writeJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

func writeErr(rw http.ResponseWriter, status int, code, msg string, extra map[string]any) {
	m := map[string]any{"code": code, "message": msg}
	for k, v := range extra {
		m[k] = v
	}
	writeJSON(rw, status, m)
}

// guard 校验服务凭证与命令的幂等键；重复的幂等键直接回放。返回 false 表示已经写了响应。
func (w *World) guard(service string, rw http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	if w.Down[service] {
		rw.WriteHeader(500)
		return nil, false
	}
	if r.Header.Get("Authorization") != "Bearer "+w.ServiceKey || r.Header.Get("X-Caller") != "cs" {
		writeErr(rw, 401, "UNAUTHORIZED", "服务凭证不对", nil)
		return nil, false
	}
	if r.Method != http.MethodPost {
		return nil, true
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeErr(rw, 400, "INVALID", "缺少幂等键", nil)
		return nil, false
	}
	if b, ok := w.idem[service+"|"+key]; ok {
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write(b)
		return nil, false
	}
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	w.Calls = append(w.Calls, Call{Service: service, Path: r.URL.Path, IdemKey: key, Caller: r.Header.Get("X-Caller"), Body: body})
	return body, true
}

func (w *World) remember(service string, r *http.Request, v any) {
	b, _ := json.Marshal(v)
	w.idem[service+"|"+r.Header.Get("Idempotency-Key")] = append(b, '\n')
}

func (w *World) nextPaymentID() string {
	w.paySeq++
	return fmt.Sprintf("PY2026120112%07d", w.paySeq)
}

func str(s string) *string { return &s }

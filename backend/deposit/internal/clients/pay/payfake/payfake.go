// Package payfake 是本队自用的 pay 替身：按 contracts/openapi/pay.yaml 的三个押金命令返回 PayCommandResult。
// 用于单元测试与本机联调（pay 未交付时）。同一幂等键重复到达返回同一结果；测试可以预设下一次结果或改写某笔结果。
package payfake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// Call 一次收到的命令。
type Call struct {
	Path    string
	Caller  string
	Auth    string
	IdemKey string
	Body    map[string]any
}

// Outcome 预设的结果。
type Outcome struct {
	Result     string // 成功｜失败｜结果未知
	FailCode   string
	FailReason string
}

// Server pay 替身。
type Server struct {
	*httptest.Server
	mu      sync.Mutex
	next    []Outcome
	byKey   map[string]map[string]any
	calls   []Call
	seq     int
	Key     string // 期望的服务凭证，空则不校验
	FailAll bool   // 为真时一律 500
}

// New 起替身（只监听 127.0.0.1）。
func New() *Server {
	s := &Server{byKey: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/pay/deposit-charges", s.handle("押金专户", "收"))
	mux.HandleFunc("POST /internal/pay/deposit-refunds", s.handle("押金专户", "付"))
	mux.HandleFunc("POST /internal/pay/deposit-transfers", s.handle("押金专户", "转"))
	s.Server = httptest.NewServer(mux)
	return s
}

// Next 预设接下来新命令的结果（按顺序消费；用完后默认成功）。
func (s *Server) Next(o ...Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = append(s.next, o...)
}

// Resolve 改写某个幂等键已有的结果（模拟结果未知后查到最终结果）。
func (s *Server) Resolve(idemKey string, o Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.byKey[idemKey]; ok {
		apply(r, o)
	}
}

// Calls 已收到的命令。
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

func apply(r map[string]any, o Outcome) {
	r["result"] = o.Result
	r["failCode"], r["failReason"], r["completedAt"] = nil, nil, nil
	switch o.Result {
	case "成功":
		r["status"] = "成功"
		r["completedAt"] = time.Now().Format(time.RFC3339)
	case "失败":
		r["status"] = "失败"
		r["failCode"], r["failReason"] = o.FailCode, o.FailReason
	default:
		r["status"] = "处理中"
	}
}

func (s *Server) handle(account, _ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		defer s.mu.Unlock()
		c := Call{Path: r.URL.Path, Caller: r.Header.Get("X-Caller"), Auth: r.Header.Get("Authorization"), IdemKey: r.Header.Get("Idempotency-Key"), Body: body}
		s.calls = append(s.calls, c)
		w.Header().Set("Content-Type", "application/json")
		if s.FailAll {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"code":"SERVER_BUSY","message":"替身故障"}`))
			return
		}
		if s.Key != "" && c.Auth != "Bearer "+s.Key {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"code":"UNAUTHORIZED","message":"凭证不对"}`))
			return
		}
		if c.IdemKey == "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"code":"INVALID","message":"缺少幂等键"}`))
			return
		}
		if prev, ok := s.byKey[c.IdemKey]; ok {
			_ = json.NewEncoder(w).Encode(prev)
			return
		}
		o := Outcome{Result: "成功"}
		if len(s.next) > 0 {
			o, s.next = s.next[0], s.next[1:]
		}
		s.seq++
		typ, _ := body["type"].(string)
		switch r.URL.Path {
		case "/internal/pay/deposit-refunds":
			typ = "退押"
		case "/internal/pay/deposit-transfers":
			typ = "押金抵扣"
		}
		res := map[string]any{"paymentId": fmt.Sprintf("PY%s%03d", time.Now().Format("20060102150405"), s.seq%1000) + fmt.Sprint(s.seq),
			"type": typ, "account": account, "amount": body["amount"]}
		apply(res, o)
		s.byKey[c.IdemKey] = res
		_ = json.NewEncoder(w).Encode(res)
	}
}

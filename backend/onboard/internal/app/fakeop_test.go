package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"bike/onboard/internal/sign"
)

// fakeOp 运营商联调实现的替身：按接入规范实现 M-01、M-02，按模拟车辆行为回报 M-03。
// 各开关用来制造不合规的实现，检验符合性测试给出的失败事实。
type fakeOp struct {
	t          *testing.T
	sandboxURL string
	appID      string
	secret     string
	srv        *httptest.Server

	normalDelay time.Duration // 正常车辆开锁结果回报延迟
	slowDelay   time.Duration // 开锁慢的车（SBX10004）

	noIdem          bool // 重复开锁请求再次开锁
	noReplayCheck   bool // 不校验随机数
	noBackfill      bool // 首次投递失败后不补报
	noRepeat        bool // 不重复发送锁车报告
	noLockOnPile    bool // 入桩未锁的结束请求答复已受理但不锁车
	missingDuration bool // 锁车报告缺 durationSec、distanceKm

	mu     sync.Mutex
	idem   map[string][]byte
	nonces map[string]bool
	seq    int
}

func newFakeOp(t *testing.T, sandboxURL, appID, secret string) *fakeOp {
	f := &fakeOp{t: t, sandboxURL: sandboxURL, appID: appID, secret: secret, normalDelay: 50 * time.Millisecond, slowDelay: 1300 * time.Millisecond,
		idem: map[string][]byte{}, nonces: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/unlock", f.unlock)
	mux.HandleFunc("POST /v1/end-ride", f.endRide)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOp) verify(r *http.Request, body []byte) bool {
	ts, _ := strconv.ParseInt(r.Header.Get("X-Timestamp"), 10, 64)
	if d := time.Since(time.Unix(ts, 0)); d > 300*time.Second || d < -300*time.Second {
		return false
	}
	if r.Header.Get("X-App-Id") != f.appID {
		return false
	}
	want := sign.Sign(f.secret, r.Method, r.URL.Path, f.appID, r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"), body)
	if !sign.Equal(want, r.Header.Get("X-Signature")) {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := r.Header.Get("X-Nonce")
	if f.nonces[n] && !f.noReplayCheck {
		return false
	}
	f.nonces[n] = true
	return true
}

// once 按幂等键去重：重复请求返回首次答复。返回 true 表示是首次。
func (f *fakeOp) once(w http.ResponseWriter, key string, reply []byte) bool {
	f.mu.Lock()
	prev, seen := f.idem[key]
	if !seen {
		f.idem[key] = reply
	}
	f.mu.Unlock()
	if seen {
		_, _ = w.Write(prev)
		return f.noIdem // 不合规实现：仍然执行
	}
	_, _ = w.Write(reply)
	return true
}

func (f *fakeOp) eventID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	return fmt.Sprintf("EVT-FAKE-%06d-%d", f.seq, time.Now().UnixNano()%100000)
}

func (f *fakeOp) unlock(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if !f.verify(r, body) {
		w.WriteHeader(401)
		return
	}
	var req struct{ OrderID, PlateNo string }
	_ = json.Unmarshal(body, &req)
	if !f.once(w, r.Header.Get("Idempotency-Key"), []byte(`{"result":"已受理"}`)) {
		return
	}
	go func() {
		delay := f.normalDelay
		if req.PlateNo == "SBX10004" {
			delay = f.slowDelay
		}
		at := time.Now()
		time.Sleep(delay)
		unlock := map[string]any{"result": "成功"}
		if req.PlateNo == "SBX10005" {
			unlock = map[string]any{"result": "失败", "failReason": "电量不足"}
		}
		f.post(map[string]any{"eventId": f.eventID(), "type": "开锁结果", "orderId": req.OrderID, "plateNo": req.PlateNo, "occurredAt": at.Add(delay).Format(time.RFC3339Nano), "unlock": unlock})
	}()
}

func (f *fakeOp) endRide(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if !f.verify(r, body) {
		w.WriteHeader(401)
		return
	}
	var req struct{ OrderID, PlateNo, Cause string }
	_ = json.Unmarshal(body, &req)
	if !f.once(w, r.Header.Get("Idempotency-Key"), []byte(`{"result":"已受理"}`)) {
		return
	}
	if req.Cause == "入桩未锁" && f.noLockOnPile {
		return
	}
	go func() {
		at := time.Now().Add(10 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		lock := map[string]any{"location": []float64{104.0657, 30.6595}, "inStation": true, "durationSec": 1100, "distanceKm": 3.2}
		if f.missingDuration {
			delete(lock, "durationSec")
			delete(lock, "distanceKm")
		}
		ev := map[string]any{"eventId": f.eventID(), "type": "锁车", "orderId": req.OrderID, "plateNo": req.PlateNo, "occurredAt": at.Format(time.RFC3339Nano), "lock": lock}
		switch req.PlateNo {
		case "SBX10006":
			if st := f.post(ev); st >= 500 && !f.noBackfill {
				time.Sleep(100 * time.Millisecond)
				f.post(ev)
			}
		case "SBX10007":
			n := 3
			if f.noRepeat {
				n = 1
			}
			for i := 0; i < n; i++ {
				f.post(ev)
				time.Sleep(20 * time.Millisecond)
			}
		default:
			f.post(ev)
		}
	}()
}

// post 签名后向联调入口发 M-03，返回 HTTP 状态。
func (f *fakeOp) post(ev map[string]any) int {
	b, _ := json.Marshal(ev)
	st, _ := signedPost(f.sandboxURL, "/sandbox/open/v1/ride-events", f.appID, f.secret, b, time.Now(), sign.NewNonce())
	return st
}

// signedPost 运营商侧发请求的通用做法。
func signedPost(base, path, appID, secret string, body []byte, at time.Time, nonce string) (int, map[string]any) {
	ts := strconv.FormatInt(at.Unix(), 10)
	req, _ := http.NewRequest("POST", base+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-Id", appID)
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", sign.Sign(secret, "POST", path, appID, ts, nonce, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

// readyBR05 登记 BR05、领取联调密钥，返回应用标识与密钥。
func (e *testEnv) readyBR05() (string, string) {
	e.t.Helper()
	e.registerBR05()
	_, m, raw := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/rotate", map[string]any{"reason": "首次领取联调密钥"})
	cred, ok := m["credentials"].(map[string]any)
	if !ok {
		e.t.Fatalf("领取密钥失败: %s", raw)
	}
	return cred["sandboxAppId"].(string), m["secret"].(string)
}

// bike-sim：容器内联调用的小工具，只用标准库，对应 deploy/local 的 mock-operator.mjs 与 pile-sim.mjs。
//
//	bike-sim keygen <目录> <名字>            生成 Ed25519 令牌密钥对，写 <名字>.priv.b64（32 字节种子）与 <名字>.pub.b64
//	bike-sim mock-operator                   运营商模拟器，监听 127.0.0.1:18390
//	bike-sim pile-sim run                    桩模拟器常驻：先发 snapshot，之后每 30 秒 heartbeat，每 5 次再发 snapshot
//	bike-sim pile-sim occupy <桩位号> <tag> [tagType] | leave <桩位号> | snapshot | heartbeat
//
// 环境变量：MOCK_OP_PORT（默认 18390）、MOCK_OP_GATEWAY（默认 http://127.0.0.1:18300）、MOCK_OP_CREDS（appId 到密钥的 JSON）、
// MOCK_OP_UNLOCK_DELAY_MS、MOCK_OP_LOCK_DELAY_MS、PILE_SIM_GATEWAY、PILE_SIM_SECRET（必填）、PILE_SIM_DEVICE、PILE_SIM_STATE。
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "keygen":
		if len(os.Args) != 4 {
			usage()
		}
		keygen(os.Args[2], os.Args[3])
	case "mock-operator":
		mockOperator()
	case "pile-sim":
		if len(os.Args) < 3 {
			usage()
		}
		pileSim(os.Args[2], os.Args[3:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法：bike-sim keygen <目录> <名字> | mock-operator | pile-sim run|occupy|leave|snapshot|heartbeat")
	os.Exit(2)
}

func keygen(dir, name string) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	must(os.WriteFile(filepath.Join(dir, name+".priv.b64"), []byte(base64.StdEncoding.EncodeToString(priv.Seed())), 0o600))
	must(os.WriteFile(filepath.Join(dir, name+".pub.b64"), []byte(base64.StdEncoding.EncodeToString(pub)), 0o644))
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// ---------------------------------------------------------------- mock-operator

type mockState struct {
	mu       sync.Mutex
	Rides    map[string]rideRec        `json:"rides"`
	Scenario map[string]map[string]any `json:"scenario"`
	Park     map[string]map[string]any `json:"park"`
	Log      []string                  `json:"log"`
}
type rideRec struct {
	PlateNo  string `json:"plateNo"`
	AppID    string `json:"appId"`
	UnlockAt int64  `json:"unlockAt"`
}

var ms = &mockState{Rides: map[string]rideRec{}, Scenario: map[string]map[string]any{}, Park: map[string]map[string]any{}}

func note(a ...any) {
	s := time.Now().UTC().Format(time.RFC3339Nano) + " " + fmt.Sprint(a...)
	ms.mu.Lock()
	ms.Log = append(ms.Log, s)
	if len(ms.Log) > 200 {
		ms.Log = ms.Log[1:]
	}
	ms.mu.Unlock()
	fmt.Println(s)
}

func creds() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(env("MOCK_OP_CREDS", "/data/mock/creds.json"))
	if err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func sign(method, p, appID, ts, nonce string, body []byte, secret string) string {
	s := strings.Join([]string{method, p, appID, ts, nonce, sha(body)}, "\n")
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))
}

func rfc3339() string {
	return time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02T15:04:05-07:00")
}

func strOf(m map[string]any, k string) string { v, _ := m[k].(string); return v }

// 没指定位置时，在站点中心附近按车号做固定的小偏移，与 mock-operator.mjs 一致
func defaultLoc(plate string) []float64 {
	var h uint32
	for _, c := range plate {
		h = h*31 + uint32(c)
	}
	return []float64{round6(104.066 + float64(int(h%7)-3)*0.00008), round6(30.596 + float64(int((h>>3)%7)-3)*0.00008)}
}
func round6(f float64) float64 { r, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'f', 6, 64), 64); return r }

func report(appID string, event map[string]any) {
	secret := creds()[appID]
	if secret == "" {
		note("没有应用 ", appID, " 的密钥，无法报告 ", event["type"], " ", event["orderId"])
		return
	}
	body, _ := json.Marshal(event)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nb := make([]byte, 12)
	_, _ = rand.Read(nb)
	nonce := hex.EncodeToString(nb)
	p := "/open/v1/ride-events"
	req, _ := http.NewRequest("POST", env("MOCK_OP_GATEWAY", "http://127.0.0.1:18300")+p, bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-app-id", appID)
	req.Header.Set("x-timestamp", ts)
	req.Header.Set("x-nonce", nonce)
	req.Header.Set("x-signature", sign("POST", p, appID, ts, nonce, body, secret))
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		note("报告失败 ", err.Error())
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 120))
	note("报告 ", event["type"], " ", event["orderId"], " -> ", resp.StatusCode, " ", string(b))
}

func mockOperator() {
	port := env("MOCK_OP_PORT", "18390")
	unlockDelay := atoi(env("MOCK_OP_UNLOCK_DELAY_MS", "900"))
	lockDelay := atoi(env("MOCK_OP_LOCK_DELAY_MS", "600"))
	send := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("content-type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	loc := func(m map[string]any, plate string) any {
		if m != nil {
			if l, ok := m["location"]; ok {
				return l
			}
		}
		return defaultLoc(plate)
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		appID := r.Header.Get("x-app-id")
		switch {
		case r.Method == "GET" && r.URL.Path == "/mock/state":
			ms.mu.Lock()
			b, _ := json.Marshal(ms)
			ms.mu.Unlock()
			w.Header().Set("content-type", "application/json; charset=utf-8")
			_, _ = w.Write(b)
		case r.Method == "POST" && r.URL.Path == "/mock/scenario":
			ms.mu.Lock()
			ms.Scenario[strOf(body, "plateNo")] = body
			ms.mu.Unlock()
			send(w, 200, map[string]any{"ok": true})
		case r.Method == "POST" && r.URL.Path == "/mock/park":
			ms.mu.Lock()
			ms.Park[strOf(body, "plateNo")] = body
			ms.mu.Unlock()
			send(w, 200, map[string]any{"ok": true})
		case r.Method == "POST" && r.URL.Path == "/v1/unlock":
			orderID, plate := strOf(body, "orderId"), strOf(body, "plateNo")
			ms.mu.Lock()
			sc := ms.Scenario[plate]
			delete(ms.Scenario, plate)
			park := ms.Park[plate]
			ms.mu.Unlock()
			tag := ""
			if sc != nil {
				tag = "（场景 " + strOf(sc, "unlock") + "）"
			}
			note("开锁请求 ", orderID, " ", plate, tag)
			send(w, 200, map[string]any{"result": "已受理"})
			if sc != nil && strOf(sc, "unlock") == "无响应" {
				return // 受理后不报告，平台按时限判失败
			}
			ms.mu.Lock()
			ms.Rides[orderID] = rideRec{PlateNo: plate, AppID: appID, UnlockAt: time.Now().UnixMilli()}
			ms.mu.Unlock()
			failed := sc != nil && strOf(sc, "unlock") == "失败"
			go func() {
				time.Sleep(time.Duration(unlockDelay) * time.Millisecond)
				u := map[string]any{"result": "成功", "location": loc(park, plate)}
				if failed {
					fr := strOf(sc, "failReason")
					if fr == "" {
						fr = "车辆故障"
					}
					u = map[string]any{"result": "失败", "failReason": fr}
				}
				report(appID, map[string]any{"eventId": "MOCK-U-" + orderID, "type": "开锁结果", "orderId": orderID, "plateNo": plate, "occurredAt": rfc3339(), "unlock": u})
			}()
		case r.Method == "POST" && r.URL.Path == "/v1/end-ride":
			orderID, plate := strOf(body, "orderId"), strOf(body, "plateNo")
			note("结束骑行请求 ", orderID, " ", plate, " ", strOf(body, "cause"))
			send(w, 200, map[string]any{"result": "已受理"})
			ms.mu.Lock()
			rec, ok := ms.Rides[orderID]
			pk := ms.Park[plate]
			ms.mu.Unlock()
			if !ok {
				rec = rideRec{AppID: appID, UnlockAt: time.Now().UnixMilli() - 600000}
			}
			dur := (time.Now().UnixMilli() - rec.UnlockAt + 500) / 1000
			if dur < 1 {
				dur = 1
			}
			inStation := true
			if pk != nil {
				if v, ok := pk["inStation"].(bool); ok {
					inStation = v
				}
			}
			a := rec.AppID
			if a == "" {
				a = appID
			}
			go func() {
				time.Sleep(time.Duration(lockDelay) * time.Millisecond)
				report(a, map[string]any{"eventId": "MOCK-L-" + orderID, "type": "锁车", "orderId": orderID, "plateNo": plate, "occurredAt": rfc3339(),
					"lock": map[string]any{"location": loc(pk, plate), "inStation": inStation, "durationSec": dur, "distanceKm": float64(int(float64(dur)*0.004*100+0.5)) / 100}})
			}()
		default:
			send(w, 404, map[string]any{"code": "NOT_FOUND", "message": "没有这个接口"})
		}
	})
	addr := "127.0.0.1:" + port
	note("mock-operator 已启动 ", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// ---------------------------------------------------------------- pile-sim

type slotRec struct {
	Tag     string `json:"tag"`
	TagType string `json:"tagType"`
}

var pileSlots = []int{1, 2, 3, 4}

func pileState() string { return env("PILE_SIM_STATE", "/data/mock/pile-sim-state.json") }

func loadSlots() map[string]slotRec {
	m := map[string]slotRec{}
	if b, err := os.ReadFile(pileState()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}
func saveSlots(m map[string]slotRec) {
	_ = os.MkdirAll(filepath.Dir(pileState()), 0o755)
	b, _ := json.MarshalIndent(m, "", " ")
	must(os.WriteFile(pileState(), b, 0o644))
}

var seq int

func msgID() string { seq++; return fmt.Sprintf("%d-%d", time.Now().UnixMilli(), seq) }

func devPost(p string, body any) (int, string) {
	gw := strings.TrimRight(env("PILE_SIM_GATEWAY", "http://127.0.0.1:18300"), "/")
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", gw+p, bytes.NewReader(b))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-device-key", os.Getenv("PILE_SIM_SECRET"))
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return resp.StatusCode, string(rb)
}

func deviceID() string { return env("PILE_SIM_DEVICE", "SIM-CTRL-ST0001") }
func nowISO() string   { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func slotsFull() []map[string]any {
	st := loadSlots()
	out := []map[string]any{}
	for _, n := range pileSlots {
		if v, ok := st[strconv.Itoa(n)]; ok {
			out = append(out, map[string]any{"slotNo": n, "present": true, "tag": v.Tag, "tagType": v.TagType})
		} else {
			out = append(out, map[string]any{"slotNo": n, "present": false, "tag": "", "tagType": "RFID"})
		}
	}
	return out
}

func heartbeat() int {
	slots := []map[string]any{}
	for _, n := range pileSlots {
		slots = append(slots, map[string]any{"slotNo": n, "status": "正常"})
	}
	c, b := devPost("/device/v1/heartbeat", map[string]any{"msgId": msgID(), "deviceId": deviceID(), "ts": nowISO(), "slots": slots, "fw": "sim-1.0", "rssi": -60})
	log.Println("heartbeat", c, b)
	return c
}
func snapshot() int {
	c, b := devPost("/device/v1/snapshot", map[string]any{"msgId": msgID(), "deviceId": deviceID(), "ts": nowISO(), "slots": slotsFull()})
	log.Println("snapshot", c, b)
	return c
}
func occupancy(slots []map[string]any) int {
	c, b := devPost("/device/v1/occupancy", map[string]any{"msgId": msgID(), "deviceId": deviceID(), "ts": nowISO(), "slots": slots})
	log.Println("occupancy", c, b)
	return c
}

func validSlot(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	for _, x := range pileSlots {
		if x == n {
			return n, true
		}
	}
	return 0, false
}

func pileSim(cmd string, a []string) {
	if os.Getenv("PILE_SIM_SECRET") == "" {
		log.Fatal("缺少 PILE_SIM_SECRET")
	}
	exit := func(c int) {
		if c == 200 {
			os.Exit(0)
		}
		os.Exit(1)
	}
	switch cmd {
	case "run":
		snapshot()
		heartbeat()
		n := 0
		for range time.Tick(30 * time.Second) {
			heartbeat()
			if n++; n%5 == 0 {
				snapshot()
			}
		}
	case "occupy":
		if len(a) < 2 {
			log.Fatal("用法：occupy <1-4> <tag> [tagType]")
		}
		n, ok := validSlot(a[0])
		if !ok {
			log.Fatal("桩位号应为 1-4")
		}
		tt := "RFID"
		if len(a) > 2 {
			tt = a[2]
		}
		st := loadSlots()
		st[strconv.Itoa(n)] = slotRec{a[1], tt}
		saveSlots(st)
		exit(occupancy([]map[string]any{{"slotNo": n, "present": true, "tag": a[1], "tagType": tt}}))
	case "leave":
		if len(a) < 1 {
			log.Fatal("用法：leave <1-4>")
		}
		n, ok := validSlot(a[0])
		if !ok {
			log.Fatal("桩位号应为 1-4")
		}
		st := loadSlots()
		old, had := st[strconv.Itoa(n)]
		delete(st, strconv.Itoa(n))
		saveSlots(st)
		tag, tt := "", "RFID"
		if had {
			tag, tt = old.Tag, old.TagType
		}
		exit(occupancy([]map[string]any{{"slotNo": n, "present": false, "tag": tag, "tagType": tt}}))
	case "snapshot":
		exit(snapshot())
	case "heartbeat":
		exit(heartbeat())
	default:
		usage()
	}
}

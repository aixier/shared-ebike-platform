package core

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"
)

// TestUnlockP90 本机测：运营商报告延迟按原型取 0.7～1.6 秒，20 个用户并发开锁，点开锁到返回"已开锁"的 P90 不超过 5 秒（NFR-01）。
// 只测本服务自身的开销；接真运营商的实测须在部署环境做。
func TestUnlockP90(t *testing.T) {
	e := newEnv(t)
	const n = 20
	for i := 0; i < n; i++ {
		uid := fmt.Sprintf("U2000%02d", i)
		e.ev("acct.user.registered", map[string]any{"userId": uid, "channel": "微信", "registeredAt": time.Now(), "duringDegrade": false})
		e.ev("acct.realname.changed", map[string]any{"userId": uid, "status": "已核验"})
		e.ev("deposit.status.changed", map[string]any{"userId": uid, "mode": "免押", "rideable": true})
		e.ev("fleet.vehicle.listed", map[string]any{"vehicleId": fmt.Sprintf("VH9000%02d", i), "brandId": "BR01", "operatorId": "OP01",
			"plateNo": fmt.Sprintf("P%02d", i), "govCode": fmt.Sprintf("SF900000%02d", i), "model": "Q3", "codeStatus": "已启用"})
	}
	e.svc.opt.UnlockTimeoutMin = 30 * time.Second
	e.og.set(func(f *fakeOpgw) { f.delay = time.Duration(700+rand.Intn(900)) * time.Millisecond })
	var mu sync.Mutex
	var durs []time.Duration
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			t0 := time.Now()
			r := e.do("POST", "/api/ride/unlock", fmt.Sprintf("U2000%02d", i), map[string]any{"code": fmt.Sprintf("SF900000%02d", i)})
			d := time.Since(t0)
			if r.status != 200 {
				t.Errorf("开锁失败：%s", r.raw)
			}
			mu.Lock()
			durs = append(durs, d)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	p90 := durs[int(float64(len(durs))*0.9)-1]
	t.Logf("开锁耗时 P50=%v P90=%v 最大=%v", durs[len(durs)/2], p90, durs[len(durs)-1])
	if p90 > 5*time.Second {
		t.Fatalf("P90 超过 5 秒：%v", p90)
	}
}

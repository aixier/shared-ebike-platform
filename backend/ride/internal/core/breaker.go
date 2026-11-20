package core

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"bike/ride/internal/rules"
)

// Probe 探测一个依赖是否可用。
type Probe func(ctx context.Context) error

// HTTPProbe 探测对方 /healthz。
func HTTPProbe(baseURL string) Probe {
	cl := &http.Client{Timeout: time.Second}
	u := strings.TrimRight(baseURL, "/") + "/healthz"
	return func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := cl.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return errStatus(resp.StatusCode)
		}
		return nil
	}
}

type errStatus int

func (e errStatus) Error() string { return "healthz 返回 " + http.StatusText(int(e)) }

// breaker 一个依赖的熔断状态：连续失败 failN 次熔断，连续成功 okN 次恢复。
type breaker struct {
	component string
	probe     Probe
	open      bool
	fails     int
	oks       int
}

// Breakers 对数据库、计费、支付通道、实名核验、押金与免押查询五个依赖的熔断（架构 5.6）。
// 熔断后新单带降级标记照常开，不等任何人批准；状态变化发 ride.dependency.state_changed。
type Breakers struct {
	mu       sync.Mutex
	list     []*breaker
	failN    int
	okN      int
	onChange func(component string, open bool)
}

// NewBreakers 新建；probes 为空的依赖不探测（视为可用）。
func NewBreakers(probes map[string]Probe, onChange func(string, bool)) *Breakers {
	b := &Breakers{failN: 3, okN: 2, onChange: onChange}
	for _, c := range []string{rules.CompDB, rules.CompBill, rules.CompPay, rules.CompAcct, rules.CompDeposit} {
		b.list = append(b.list, &breaker{component: c, probe: probes[c]})
	}
	return b
}

func (b *Breakers) get(c string) *breaker {
	for _, x := range b.list {
		if x.component == c {
			return x
		}
	}
	return nil
}

// Report 记一次结果（探测或业务调用）。
func (b *Breakers) Report(component string, err error) {
	b.mu.Lock()
	x := b.get(component)
	if x == nil {
		b.mu.Unlock()
		return
	}
	changed := false
	if err != nil {
		x.oks = 0
		x.fails++
		if !x.open && x.fails >= b.failN {
			x.open, changed = true, true
		}
	} else {
		x.fails = 0
		x.oks++
		if x.open && x.oks >= b.okN {
			x.open, changed = false, true
		}
	}
	open := x.open
	b.mu.Unlock()
	if changed && b.onChange != nil {
		b.onChange(component, open)
	}
}

// Trip 立即熔断（业务写库时发现数据库不可用）。
func (b *Breakers) Trip(component string) {
	b.mu.Lock()
	x := b.get(component)
	if x == nil || x.open {
		b.mu.Unlock()
		return
	}
	x.open, x.oks, x.fails = true, 0, b.failN
	b.mu.Unlock()
	if b.onChange != nil {
		b.onChange(component, true)
	}
}

// Open 某依赖是否熔断。
func (b *Breakers) Open(component string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if x := b.get(component); x != nil {
		return x.open
	}
	return false
}

// OpenComponents 熔断中的依赖。
func (b *Breakers) OpenComponents() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, x := range b.list {
		if x.open {
			out = append(out, x.component)
		}
	}
	return out
}

// ProbeAll 探测一轮。
func (b *Breakers) ProbeAll(ctx context.Context) {
	b.mu.Lock()
	list := append([]*breaker(nil), b.list...)
	b.mu.Unlock()
	for _, x := range list {
		if x.probe == nil {
			continue
		}
		c, cancel := context.WithTimeout(ctx, time.Second)
		err := x.probe(c)
		cancel()
		b.Report(x.component, err)
	}
}

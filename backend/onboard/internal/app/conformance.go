package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"bike/onboard/internal/auth"
	"bike/onboard/internal/content"

	"github.com/jackc/pgx/v5"
)

// 符合性测试：真实向运营商联调地址发 M-01、M-02，读运营商在联调环境里的 M-03、M-04 记录，逐项判定。
// 判定只陈述事实（failReason），修改建议（suggestion）留给接入智能体。
// trace: R-39；用例目录在 content/cases.json

// 模拟车辆在用例里的角色（车号见 content/sandbox.json）
const (
	plateNormalA    = "SBX10001"
	plateNormalB    = "SBX10002"
	plateNormalC    = "SBX10003"
	plateSlow       = "SBX10004"
	plateLowBatt    = "SBX10005"
	plateOffline    = "SBX10006"
	plateRepeat     = "SBX10007"
	platePileNoLock = "SBX10008"
)

type caseOut struct {
	Result     string
	DurationMs int
	FailReason string
	Facts      map[string]any
}

type unlockObs struct {
	Order *sbOrder
	Slow  bool
	Reply callResult
	Event *delivery
}

type runner struct {
	a                                  *App
	ctx                                context.Context
	brand                              string
	runID                              string
	base                               string
	cred                               *credRow
	p                                  Params
	mu                                 sync.Mutex
	res                                map[string]*caseOut
	obs                                []unlockObs
	orders                             []*sbOrder
	backfillRejected, backfillAccepted *delivery
}

func (r *runner) set(id string, start time.Time, reason string, facts map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, done := r.res[id]; done {
		return
	}
	c := &caseOut{Result: "通过", DurationMs: int(time.Since(start).Milliseconds()), Facts: facts}
	if reason != "" {
		c.Result, c.FailReason = "失败", reason
	}
	r.res[id] = c
}

func (r *runner) pass(id string, start time.Time, facts map[string]any) { r.set(id, start, "", facts) }
func (r *runner) fail(id string, start time.Time, reason string, facts map[string]any) {
	r.set(id, start, reason, facts)
}

// skip 前置步骤没通过时，依赖它的用例判失败并写明原因。
func (r *runner) skip(start time.Time, why string, ids ...string) {
	for _, id := range ids {
		r.fail(id, start, "前置步骤未通过："+why, nil)
	}
}

func (r *runner) addObs(o unlockObs) {
	r.mu.Lock()
	r.obs = append(r.obs, o)
	r.mu.Unlock()
}

func (r *runner) newOrder(plate string, degraded, rejectFirstLock bool) (*sbOrder, error) {
	o, err := r.a.createOrder(r.ctx, r.brand, "符合性", r.runID, plate, "", degraded, rejectFirstLock)
	if err == nil {
		r.mu.Lock()
		r.orders = append(r.orders, o)
		r.mu.Unlock()
	}
	return o, err
}

func (r *runner) T() time.Duration { return r.p.UnlockTimeout }
func (r *runner) W() time.Duration { return r.p.UnlockTimeout + r.a.Cfg.Grace }

// replyProblem 同步答复的问题；没有问题返回空串。
func (r *runner) replyProblem(what string, c callResult) string {
	if !c.accepted() {
		return what + c.describe(r.a.Cfg.CallTimeout) + "，规范要求答复已受理"
	}
	if c.Elapsed > r.a.Cfg.ReplyLimit {
		return fmt.Sprintf("%s答复用时 %s，规范要求 %s内", what, fmtDur(c.Elapsed), fmtDur(r.a.Cfg.ReplyLimit))
	}
	return ""
}

// unlockAndWait 开锁并等开锁结果。返回失败原因（空串表示开锁成功）。
func (r *runner) unlockAndWait(o *sbOrder, slow bool) (string, *delivery) {
	res, err := r.a.unlock(r.ctx, r.base, r.cred, o)
	if err != nil {
		return "联调环境内部错误，未能发出开锁请求", nil
	}
	if p := r.replyProblem(fmt.Sprintf("车号 %s 的开锁请求", o.Plate), res); p != "" {
		r.addObs(unlockObs{Order: o, Slow: slow, Reply: res})
		return p, nil
	}
	ev := r.a.waitFor(r.ctx, o.ID, "开锁结果", o.ReqAt.Add(r.W()))
	r.addObs(unlockObs{Order: o, Slow: slow, Reply: res, Event: ev})
	if ev == nil {
		return fmt.Sprintf("车号 %s 开锁请求后 %s内未收到开锁结果（M-03）", o.Plate, fmtDur(r.W())), nil
	}
	if ev.Body.Unlock == nil || ev.Body.Unlock.Result != "成功" {
		fr := ""
		if ev.Body.Unlock != nil {
			fr = ev.Body.Unlock.FailReason
		}
		return fmt.Sprintf("车号 %s 的开锁结果为失败（%s）", o.Plate, fr), ev
	}
	return "", ev
}

// endAndWaitLock 发结束请求并等锁车报告。返回失败原因。
func (r *runner) endAndWaitLock(o *sbOrder, cause, reason string) (string, *delivery) {
	res := r.a.endRide(r.ctx, r.base, r.cred, o, cause, reason)
	if p := r.replyProblem(fmt.Sprintf("结束骑行请求（%s）", cause), res); p != "" {
		return p, nil
	}
	lock := r.a.waitFor(r.ctx, o.ID, "锁车", time.Now().Add(r.a.Cfg.LockWait))
	if lock == nil {
		return fmt.Sprintf("结束骑行请求（%s）答复已受理，但 %s内没有收到锁车报告", cause, fmtDur(r.a.Cfg.LockWait)), nil
	}
	return "", lock
}

func distinctIDs(ds []delivery, typ string, outcomes ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range ds {
		if d.Type != typ {
			continue
		}
		ok := len(outcomes) == 0
		for _, o := range outcomes {
			if d.Outcome == o {
				ok = true
			}
		}
		if ok && !seen[d.EventID] {
			seen[d.EventID] = true
			out = append(out, d.EventID)
		}
	}
	return out
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

// 链 A：正常车辆，M01-01 → M02-01 → M01-04
func (r *runner) chainA() {
	start := time.Now()
	o, err := r.newOrder(plateNormalA, false, false)
	if err != nil {
		r.skip(start, "联调环境内部错误", "M01-01", "M02-01", "M01-04")
		return
	}
	why, ev := r.unlockAndWait(o, false)
	if why != "" {
		r.fail("M01-01", start, why, map[string]any{"orderId": o.ID, "plateNo": o.Plate})
		r.skip(start, "M01-01 开锁未成功", "M02-01", "M01-04")
		return
	}
	r.pass("M01-01", start, map[string]any{"orderId": o.ID, "replyMs": ms(o.First.Elapsed), "resultAfterMs": ms(ev.ReceivedAt.Sub(o.ReqAt))})

	s2 := time.Now()
	if why, lock := r.endAndWaitLock(o, "用户点还车", ""); why != "" {
		r.fail("M02-01", s2, why, map[string]any{"orderId": o.ID})
	} else {
		r.pass("M02-01", s2, map[string]any{"orderId": o.ID, "lockEventId": lock.EventID})
	}

	s3 := time.Now()
	ds, _ := r.a.deliveries(r.ctx, o.ID)
	before := distinctIDs(ds, "开锁结果", "已收到", "重复")
	again := r.a.resendUnlock(r.ctx, r.base, r.cred, o)
	if again.Err != nil || again.Status != o.First.Status || again.Result != o.First.Result || again.RejectReason != o.First.RejectReason {
		r.fail("M01-04", s3, fmt.Sprintf("延迟到达的重复开锁请求%s，与首次答复（%s）不一致", again.describe(r.a.Cfg.CallTimeout), o.First.describe(r.a.Cfg.CallTimeout)),
			map[string]any{"orderId": o.ID})
		return
	}
	sleepUntil(r.ctx, time.Now().Add(r.a.Cfg.Grace))
	ds, _ = r.a.deliveries(r.ctx, o.ID)
	after := distinctIDs(ds, "开锁结果", "已收到", "重复")
	if len(after) > len(before) {
		r.fail("M01-04", s3, fmt.Sprintf("订单已结束后延迟到达的重复开锁请求触发了新的开锁结果（eventId %s）", after[len(after)-1]), map[string]any{"orderId": o.ID})
		return
	}
	r.pass("M01-04", s3, map[string]any{"orderId": o.ID})
}

// 链 B：正常车辆，M01-02（同时到达的重复请求）→ M02-04
func (r *runner) chainB() {
	start := time.Now()
	o, err := r.newOrder(plateNormalB, false, false)
	if err != nil {
		r.skip(start, "联调环境内部错误", "M01-02", "M02-04")
		return
	}
	o.ReqAt = r.a.now()
	var first, dup callResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); first, _ = r.a.unlock(r.ctx, r.base, r.cred, o) }()
	go func() { defer wg.Done(); dup = r.a.resendUnlock(r.ctx, r.base, r.cred, o) }()
	wg.Wait()
	if p := r.replyProblem("车号 "+o.Plate+" 的开锁请求", first); p != "" {
		r.addObs(unlockObs{Order: o, Reply: first})
		r.fail("M01-02", start, p, map[string]any{"orderId": o.ID})
		r.skip(start, "M01-02 开锁未成功", "M02-04")
		return
	}
	ev := r.a.waitFor(r.ctx, o.ID, "开锁结果", o.ReqAt.Add(r.W()))
	r.addObs(unlockObs{Order: o, Reply: first, Event: ev})
	if dup.Err != nil || dup.Status != first.Status || dup.Result != first.Result || dup.RejectReason != first.RejectReason {
		r.fail("M01-02", start, fmt.Sprintf("同一订单号的两次开锁请求答复不一致（首次%s，重复%s）", first.describe(r.a.Cfg.CallTimeout), dup.describe(r.a.Cfg.CallTimeout)), map[string]any{"orderId": o.ID})
	} else if ev == nil {
		r.fail("M01-02", start, fmt.Sprintf("开锁请求后 %s内未收到开锁结果（M-03）", fmtDur(r.W())), map[string]any{"orderId": o.ID})
	} else {
		sleepUntil(r.ctx, time.Now().Add(r.a.Cfg.Grace))
		ds, _ := r.a.deliveries(r.ctx, o.ID)
		ids := distinctIDs(ds, "开锁结果", "已收到", "重复")
		if len(ids) > 1 {
			r.fail("M01-02", start, fmt.Sprintf("同一订单号第二次开锁请求触发了第二次开锁（收到 %d 个不同 eventId 的开锁结果）", len(ids)), map[string]any{"orderId": o.ID, "eventIds": ids})
		} else {
			r.pass("M01-02", start, map[string]any{"orderId": o.ID})
		}
	}
	s2 := time.Now()
	if ev == nil || ev.Body.Unlock == nil || ev.Body.Unlock.Result != "成功" {
		r.skip(s2, "车号 "+o.Plate+" 未开锁成功", "M02-04")
		return
	}
	if why, _ := r.endAndWaitLock(o, "客服强制结束", "用户反映车辆无法还车，客服核实后强制结束"); why != "" {
		r.fail("M02-04", s2, why, map[string]any{"orderId": o.ID})
	} else {
		r.pass("M02-04", s2, map[string]any{"orderId": o.ID})
	}
}

// 链 C：带降级标记的开锁，M01-03
func (r *runner) chainC() {
	start := time.Now()
	o, err := r.newOrder(plateNormalC, true, false)
	if err != nil {
		r.skip(start, "联调环境内部错误", "M01-03")
		return
	}
	why, _ := r.unlockAndWait(o, false)
	if why != "" {
		r.fail("M01-03", start, "降级标记 degraded=true 时，"+why, map[string]any{"orderId": o.ID})
		return
	}
	r.pass("M01-03", start, map[string]any{"orderId": o.ID})
	r.a.endRide(r.ctx, r.base, r.cred, o, "用户点还车", "")
}

// 链 D：开锁慢的车，M02-03
func (r *runner) chainD() {
	start := time.Now()
	o, err := r.newOrder(plateSlow, false, false)
	if err != nil {
		r.skip(start, "联调环境内部错误", "M02-03")
		return
	}
	res, _ := r.a.unlock(r.ctx, r.base, r.cred, o)
	if p := r.replyProblem("车号 "+o.Plate+" 的开锁请求", res); p != "" {
		r.addObs(unlockObs{Order: o, Slow: true, Reply: res})
		r.fail("M02-03", start, p, map[string]any{"orderId": o.ID})
		return
	}
	ev := r.a.waitFor(r.ctx, o.ID, "开锁结果", o.ReqAt.Add(r.W()))
	r.addObs(unlockObs{Order: o, Slow: true, Reply: res, Event: ev})
	if ev == nil {
		r.fail("M02-03", start, fmt.Sprintf("模拟车辆 %s（开锁慢）开锁请求后 %s内未收到开锁结果，无法测试超时后才报开锁成功的处理", o.Plate, fmtDur(r.W())), map[string]any{"orderId": o.ID})
		return
	}
	delay := ev.ReceivedAt.Sub(o.ReqAt)
	if ev.Body.Unlock == nil || ev.Body.Unlock.Result != "成功" {
		r.fail("M02-03", start, fmt.Sprintf("模拟车辆 %s（开锁慢）报告了开锁失败，未按模拟行为在超时后报告开锁成功", o.Plate), map[string]any{"orderId": o.ID})
		return
	}
	if delay <= r.T() {
		r.fail("M02-03", start, fmt.Sprintf("模拟车辆 %s（开锁慢）的开锁结果在请求后 %s到达，未超过时限 %s，无法测试超时后才报开锁成功的处理；请按模拟车辆标注的行为模拟",
			o.Plate, fmtDur(delay), fmtDur(r.T())), map[string]any{"orderId": o.ID, "resultAfterMs": ms(delay)})
		return
	}
	if why, _ := r.endAndWaitLock(o, "超时后才报开锁成功", ""); why != "" {
		r.fail("M02-03", start, fmt.Sprintf("开锁成功在请求后 %s 才报告（超过时限 %s），平台随即请求结束：%s", fmtDur(delay), fmtDur(r.T()), why), map[string]any{"orderId": o.ID, "resultAfterMs": ms(delay)})
		return
	}
	r.pass("M02-03", start, map[string]any{"orderId": o.ID, "resultAfterMs": ms(delay)})
}

// 链 E：入桩不上锁的车，M02-02
func (r *runner) chainE() {
	start := time.Now()
	o, err := r.newOrder(platePileNoLock, false, false)
	if err != nil {
		r.skip(start, "联调环境内部错误", "M02-02")
		return
	}
	if why, _ := r.unlockAndWait(o, false); why != "" {
		r.skip(start, why, "M02-02")
		return
	}
	if why, _ := r.endAndWaitLock(o, "入桩未锁", ""); why != "" {
		r.fail("M02-02", start, why+"（车辆未上锁）", map[string]any{"orderId": o.ID})
		return
	}
	r.pass("M02-02", start, map[string]any{"orderId": o.ID})
}

// 链 F：断网的车，锁车报告首次投递拒收，M03-03
func (r *runner) chainF() {
	start := time.Now()
	o, err := r.newOrder(plateOffline, false, true)
	if err != nil {
		r.skip(start, "联调环境内部错误", "M03-03")
		return
	}
	if why, _ := r.unlockAndWait(o, false); why != "" {
		r.skip(start, why, "M03-03")
		return
	}
	res := r.a.endRide(r.ctx, r.base, r.cred, o, "用户点还车", "")
	if p := r.replyProblem("结束骑行请求（用户点还车）", res); p != "" {
		r.fail("M03-03", start, p, map[string]any{"orderId": o.ID})
		return
	}
	r.a.waitFor(r.ctx, o.ID, "锁车", time.Now().Add(r.a.Cfg.LockWait))
	ds, _ := r.a.deliveries(r.ctx, o.ID)
	var rejected, accepted *delivery
	for i := range ds {
		if ds[i].Type != "锁车" {
			continue
		}
		if ds[i].Outcome == "拒收" && rejected == nil {
			rejected = &ds[i]
		}
		if ds[i].Outcome == "已收到" && accepted == nil {
			accepted = &ds[i]
		}
	}
	r.mu.Lock()
	r.backfillRejected, r.backfillAccepted = rejected, accepted
	r.mu.Unlock()
	switch {
	case rejected == nil && accepted == nil:
		r.fail("M03-03", start, fmt.Sprintf("结束骑行请求后 %s内没有收到锁车报告", fmtDur(r.a.Cfg.LockWait)), map[string]any{"orderId": o.ID})
	case accepted == nil:
		r.fail("M03-03", start, fmt.Sprintf("模拟断网：锁车报告首次投递平台答复 SERVER_BUSY 后，%s内未补报", fmtDur(r.a.Cfg.LockWait)), map[string]any{"orderId": o.ID, "eventId": rejected.EventID})
	case rejected != nil && accepted.EventID != rejected.EventID:
		r.fail("M03-03", start, fmt.Sprintf("补报使用了新的 eventId（%s → %s），规范要求补报时 eventId 不变", rejected.EventID, accepted.EventID), map[string]any{"orderId": o.ID})
	default:
		r.pass("M03-03", start, map[string]any{"orderId": o.ID, "retryAfterMs": ms(accepted.ReceivedAt.Sub(rejected.ReceivedAt))})
	}
}

// 链 G：锁车报告重复发送的车，M03-04
func (r *runner) chainG() {
	start := time.Now()
	o, err := r.newOrder(plateRepeat, false, false)
	if err != nil {
		r.skip(start, "联调环境内部错误", "M03-04")
		return
	}
	if why, _ := r.unlockAndWait(o, false); why != "" {
		r.skip(start, why, "M03-04")
		return
	}
	if why, _ := r.endAndWaitLock(o, "用户点还车", ""); why != "" {
		r.fail("M03-04", start, why, map[string]any{"orderId": o.ID})
		return
	}
	sleepUntil(r.ctx, time.Now().Add(r.a.Cfg.Grace))
	ds, _ := r.a.deliveries(r.ctx, o.ID)
	n := 0
	for _, d := range ds {
		if d.Type == "锁车" {
			n++
		}
	}
	ids := distinctIDs(ds, "锁车")
	switch {
	case len(ids) > 1:
		r.fail("M03-04", start, fmt.Sprintf("同一次锁车收到 %d 个不同 eventId 的报告，重复上报须保持 eventId 不变，否则会被当成多次锁车", len(ids)), map[string]any{"orderId": o.ID, "eventIds": ids})
	case n < 2:
		r.fail("M03-04", start, fmt.Sprintf("模拟车辆 %s 的锁车报告只收到 %d 次，未按模拟行为重复发送，无法验证重复上报", o.Plate, n), map[string]any{"orderId": o.ID})
	default:
		r.pass("M03-04", start, map[string]any{"orderId": o.ID, "deliveries": n})
	}
}

// 链 H：电量不足的车，只为 M03-01 提供开锁失败结果的时限观察
func (r *runner) chainH() {
	o, err := r.newOrder(plateLowBatt, false, false)
	if err != nil {
		return
	}
	res, _ := r.a.unlock(r.ctx, r.base, r.cred, o)
	if !res.accepted() {
		r.addObs(unlockObs{Order: o, Reply: res})
		return
	}
	ev := r.a.waitFor(r.ctx, o.ID, "开锁结果", o.ReqAt.Add(r.W()))
	r.addObs(unlockObs{Order: o, Reply: res, Event: ev})
}

// 接入安全：SEC-01、SEC-02（运营商收到平台请求也必须验签）
func (r *runner) chainSec() {
	start := time.Now()
	expect401 := func(what string, c callResult) string {
		if c.Err == nil && c.Status == http.StatusUnauthorized {
			return ""
		}
		return fmt.Sprintf("%s%s，规范要求拒绝（HTTP 401）", what, c.describe(r.a.Cfg.CallTimeout))
	}
	send := func(plate string, o signOpts) (callResult, *sbOrder, error) {
		ord, err := r.newOrder(plate, false, false)
		if err != nil {
			return callResult{}, nil, err
		}
		now := r.a.now()
		req, err := prepare(r.base, "/v1/unlock", unlockBody(ord, now), ord.Idem, r.cred, o, now)
		if err != nil {
			return callResult{}, nil, err
		}
		return r.a.send(r.ctx, req), ord, nil
	}
	// SEC-01：未登记应用标识；签名错误
	var problems []string
	if c, _, err := send(plateNormalA, signOpts{AppID: "sbx_unregistered_000000", Secret: "not-a-registered-secret"}); err == nil {
		if p := expect401("未登记应用标识的开锁请求", c); p != "" {
			problems = append(problems, p)
		}
	}
	if c, _, err := send(plateNormalA, signOpts{AppID: r.cred.AppID, Secret: r.cred.Secret + "-wrong"}); err == nil {
		if p := expect401("签名错误的开锁请求", c); p != "" {
			problems = append(problems, p)
		}
	}
	if len(problems) > 0 {
		r.fail("SEC-01", start, strings.Join(problems, "；"), nil)
	} else {
		r.pass("SEC-01", start, nil)
	}

	// SEC-02：重放同一请求；时间戳过期；签名后被篡改
	s2 := time.Now()
	problems = nil
	ord, err := r.newOrder(plateNormalC, false, false)
	if err != nil {
		r.skip(s2, "联调环境内部错误", "SEC-02")
		return
	}
	now := r.a.now()
	req, _ := prepare(r.base, "/v1/unlock", unlockBody(ord, now), ord.Idem, r.cred, signOpts{}, now)
	first := r.a.send(r.ctx, req)
	if first.Err != nil || first.Status != 200 {
		problems = append(problems, "签名正确的开锁请求"+first.describe(r.a.Cfg.CallTimeout)+"，无法进行重放测试")
	} else if p := expect401("原样重放同一请求（随机数相同）", r.a.send(r.ctx, req)); p != "" {
		problems = append(problems, p)
	}
	stale := r.a.now().Add(-(r.a.Cfg.ClockSkew + time.Minute))
	if c, _, err := send(plateNormalC, signOpts{At: stale}); err == nil {
		if p := expect401(fmt.Sprintf("时间戳早于当前 %s 的开锁请求", fmtDur(r.a.Cfg.ClockSkew+time.Minute)), c); p != "" {
			problems = append(problems, p)
		}
	}
	tamper := func(b []byte) []byte { return bytes.Replace(b, []byte(plateNormalC), []byte("SBX19999"), 1) }
	if c, _, err := send(plateNormalC, signOpts{Tamper: tamper}); err == nil {
		if p := expect401("签名后报文被改动的开锁请求", c); p != "" {
			problems = append(problems, p)
		}
	}
	if len(problems) > 0 {
		r.fail("SEC-02", s2, strings.Join(problems, "；"), nil)
	} else {
		r.pass("SEC-02", s2, nil)
	}
}

// evalM0301 开锁结果时限（不含开锁慢的模拟车辆）。
func (r *runner) evalM0301() {
	start := time.Now()
	type viol struct {
		delay time.Duration
		text  string
	}
	var vs []viol
	checked := 0
	for _, o := range r.obs {
		if o.Slow || !o.Reply.accepted() {
			continue
		}
		checked++
		if o.Event == nil {
			vs = append(vs, viol{r.W() + time.Hour, fmt.Sprintf("开锁请求后 %s内未收到开锁结果（订单 %s，车号 %s）", fmtDur(r.W()), o.Order.ID, o.Order.Plate)})
			continue
		}
		d := o.Event.ReceivedAt.Sub(o.Order.ReqAt)
		if d > r.T() {
			vs = append(vs, viol{d, fmt.Sprintf("开锁结果在请求后 %s才到达，规范要求 %s内（订单 %s，车号 %s）", fmtDur(d), fmtDur(r.T()), o.Order.ID, o.Order.Plate)})
		}
	}
	if checked == 0 {
		r.fail("M03-01", start, "本轮没有被受理的开锁请求，无法检查开锁结果时限", nil)
		return
	}
	if len(vs) == 0 {
		r.pass("M03-01", start, map[string]any{"checked": checked})
		return
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].delay > vs[j].delay })
	reason := vs[0].text
	if len(vs) > 1 {
		reason += fmt.Sprintf("；本轮共 %d 单超时", len(vs))
	}
	r.fail("M03-01", start, reason, map[string]any{"checked": checked, "violations": len(vs)})
}

// evalM0302 锁车事件字段完整。
func (r *runner) evalM0302() {
	start := time.Now()
	n := 0
	for _, o := range r.orders {
		ds, _ := r.a.deliveries(r.ctx, o.ID)
		for _, d := range ds {
			if d.Type != "锁车" || d.Outcome != "已收到" {
				continue
			}
			n++
			lock, _ := d.Raw["lock"].(map[string]any)
			var miss []string
			for _, f := range []string{"location", "inStation", "durationSec", "distanceKm"} {
				if v, ok := lock[f]; !ok || v == nil {
					miss = append(miss, f)
				}
			}
			if len(miss) > 0 {
				r.fail("M03-02", start, fmt.Sprintf("锁车报告缺少 %s（订单 %s，eventId %s）", strings.Join(miss, "、"), o.ID, d.EventID), map[string]any{"orderId": o.ID})
				return
			}
		}
	}
	if n == 0 {
		r.fail("M03-02", start, "本轮没有收到任何锁车报告，无法检查字段", nil)
		return
	}
	r.pass("M03-02", start, map[string]any{"checked": n})
}

// evalM0305 事件时间：occurredAt 是车端发生时刻，补报不改。
func (r *runner) evalM0305() {
	start := time.Now()
	checked := 0
	for _, o := range r.orders {
		ds, _ := r.a.deliveries(r.ctx, o.ID)
		un, lk := firstAccepted(ds, "开锁结果"), firstAccepted(ds, "锁车")
		for _, d := range ds {
			if d.OccurredAt.After(d.ReceivedAt.Add(r.a.Cfg.ClockSkew)) {
				r.fail("M03-05", start, fmt.Sprintf("事件 %s 的 occurredAt（%s）晚于平台收到时刻超过 %s，应填车端发生时刻", d.EventID, d.OccurredAt.Format(time.RFC3339), fmtDur(r.a.Cfg.ClockSkew)), map[string]any{"orderId": o.ID})
				return
			}
		}
		if un != nil && lk != nil {
			checked++
			if lk.OccurredAt.Before(un.OccurredAt) {
				r.fail("M03-05", start, fmt.Sprintf("订单 %s 的锁车时刻（%s）早于开锁时刻（%s）", o.ID, lk.OccurredAt.Format(time.RFC3339), un.OccurredAt.Format(time.RFC3339)), map[string]any{"orderId": o.ID})
				return
			}
		}
	}
	if r.backfillRejected != nil && r.backfillAccepted != nil && r.backfillRejected.EventID == r.backfillAccepted.EventID &&
		!r.backfillRejected.OccurredAt.Equal(r.backfillAccepted.OccurredAt) {
		r.fail("M03-05", start, fmt.Sprintf("补报的锁车事件 occurredAt 由 %s 改成了 %s，规范要求填车端发生时刻、补报时不变",
			r.backfillRejected.OccurredAt.Format(time.RFC3339), r.backfillAccepted.OccurredAt.Format(time.RFC3339)), nil)
		return
	}
	if checked == 0 {
		r.fail("M03-05", start, "本轮没有同时收到开锁结果与锁车报告的订单，无法检查事件时间", nil)
		return
	}
	r.pass("M03-05", start, map[string]any{"checked": checked})
}

type submission struct {
	OK     bool
	Rows   []VehicleRow
	Errors []rowError
	At     time.Time
}

// evalM04 车辆清单三项，依据联调环境收到的 M-04 记录。
func (r *runner) evalM04() {
	start := time.Now()
	rows, err := r.a.DB.Query(r.ctx, `SELECT ok, rows, errors, submitted_at FROM vehicle_submissions WHERE brand_id=$1 ORDER BY id`, r.brand)
	if err != nil {
		r.skip(start, "联调环境内部错误", "M04-01", "M04-02", "M04-03")
		return
	}
	var subs []submission
	for rows.Next() {
		var s submission
		var rj, ej []byte
		if rows.Scan(&s.OK, &rj, &ej, &s.At) == nil {
			_ = json.Unmarshal(rj, &s.Rows)
			_ = json.Unmarshal(ej, &s.Errors)
			subs = append(subs, s)
		}
	}
	rows.Close()
	if len(subs) == 0 {
		msg := "联调环境未收到本品牌的车辆清单（M-04），请先向 /sandbox/open/v1/vehicle-lists 提交"
		r.fail("M04-01", start, msg, nil)
		r.fail("M04-02", start, msg, nil)
		r.fail("M04-03", start, msg, nil)
		return
	}
	last := subs[len(subs)-1]
	var okSubs []submission
	for _, s := range subs {
		if s.OK {
			okSubs = append(okSubs, s)
		}
	}
	byRule := func(rule string) []rowError {
		var out []rowError
		for _, e := range last.Errors {
			if (rule == "必填" && (e.Rule == "必填" || e.Rule == "取值")) || e.Rule == rule {
				out = append(out, e)
			}
		}
		return out
	}
	if es := byRule("必填"); len(es) > 0 {
		r.fail("M04-01", start, fmt.Sprintf("最近一次提交的清单有 %d 处字段缺失或取值不对，如第 %d 行：%s", len(es), es[0].Row, es[0].Message), map[string]any{"errors": len(es)})
	} else if len(okSubs) == 0 {
		r.fail("M04-01", start, "联调环境还没有收到一份全部通过校验的车辆清单", nil)
	} else {
		r.pass("M04-01", start, map[string]any{"rows": len(okSubs[len(okSubs)-1].Rows)})
	}
	if es := byRule("重复"); len(es) > 0 {
		tag := ""
		for _, e := range es {
			if e.Field == "pileTag" && e.Row-1 < len(last.Rows) {
				tag = maskTag(last.Rows[e.Row-1].PileTag)
				break
			}
		}
		reason := fmt.Sprintf("最近一次提交的清单中 %d 辆车的车号或车辆标识重复", len(es)+1)
		if tag != "" {
			reason = fmt.Sprintf("最近一次提交的清单中 %d 辆车的车辆标识重复（%s）", len(es)+1, tag)
		}
		r.fail("M04-02", start, reason, map[string]any{"errors": len(es)})
	} else if len(okSubs) == 0 {
		r.fail("M04-02", start, "联调环境还没有收到一份全部通过校验的车辆清单", nil)
	} else {
		r.pass("M04-02", start, nil)
	}
	if len(okSubs) < 2 {
		r.fail("M04-03", start, fmt.Sprintf("联调环境只收到 %d 次通过校验的车辆清单提交，请再提交一次有新增或变更车辆的清单以验证增量更新", len(okSubs)), nil)
		return
	}
	seen := map[string]VehicleRow{}
	for _, row := range okSubs[0].Rows {
		seen[row.PlateNo] = row
	}
	changed := false
	for _, s := range okSubs[1:] {
		for _, row := range s.Rows {
			old, ok := seen[row.PlateNo]
			if !ok || old.Model != row.Model || old.PileTag != row.PileTag || old.PileTagType != row.PileTagType {
				changed = true
			}
			seen[row.PlateNo] = row
		}
	}
	if !changed {
		r.fail("M04-03", start, "后续提交的清单与首次相同，没有新增或变更车辆，无法验证增量更新", nil)
		return
	}
	r.pass("M04-03", start, map[string]any{"submissions": len(okSubs)})
}

func maskTag(t string) string {
	if len([]rune(t)) <= 10 {
		return t
	}
	rs := []rune(t)
	return string(rs[:6]) + "…" + string(rs[len(rs)-4:])
}

// noBaseURL 没有运营商联调地址时，需要向运营商发请求的用例全部判失败。
var operatorCases = []string{"M01-01", "M01-02", "M01-03", "M01-04", "M02-01", "M02-02", "M02-03", "M02-04", "M03-01", "M03-02", "M03-03", "M03-04", "M03-05", "SEC-01", "SEC-02"}

// StartRun 发起一轮符合性测试。
func (a *App) StartRun(ctx context.Context, adm *auth.Admin, brandID, baseURL string) (*Run, error) {
	in, err := a.integByBrand(ctx, a.DB, brandID)
	if err != nil {
		return nil, err
	}
	if err := ownOperator(adm, in.OperatorID); err != nil {
		return nil, err
	}
	if in.Status == "已退出" || in.Status == "未接入" {
		return nil, errf(409, "CONFLICT", fmt.Sprintf("品牌当前接入状态为%s，不能发起测试", in.Status))
	}
	cred, err := a.credByBrand(ctx, a.DB, brandID)
	if err != nil {
		return nil, err
	}
	if cred.Status != "有效" {
		return nil, errf(409, "CREDENTIAL_DISABLED", "联调凭证已吊销，不能发起符合性测试")
	}
	if baseURL != "" {
		if err := validateBaseURL(baseURL); err != nil {
			return nil, err
		}
		if err := a.registerBaseURL(ctx, adminActor(adm), in, baseURL); err != nil {
			return nil, err
		}
	} else if in.OperatorBaseURL != nil {
		baseURL = *in.OperatorBaseURL
	}

	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, "run:"+brandID).Scan(&locked); err != nil {
		return nil, err
	}
	if !locked {
		return nil, errf(409, "CONFLICT", "该品牌上一轮测试还在进行，请稍后再看结果")
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext($1))`, "run:"+brandID)
	}()

	p, err := a.params(ctx)
	if err != nil {
		return nil, err
	}
	var seq int
	if err := a.DB.QueryRow(ctx, `UPDATE integrations SET run_seq=run_seq+1 WHERE brand_id=$1 RETURNING run_seq`, brandID).Scan(&seq); err != nil {
		return nil, err
	}
	runAt := a.now()
	rn := &runner{a: a, ctx: ctx, brand: brandID, runID: fmt.Sprintf("%s-R%03d", in.IntegrationID, seq), base: baseURL, cred: cred, p: p, res: map[string]*caseOut{}}
	if baseURL == "" {
		for _, id := range operatorCases {
			rn.fail(id, time.Now(), "没有运营商联调地址，无法向运营商发起开锁与结束骑行请求；发起测试时请提供运营商联调地址", nil)
		}
	} else {
		chains := []func(){rn.chainA, rn.chainB, rn.chainC, rn.chainD, rn.chainE, rn.chainF, rn.chainG, rn.chainH, rn.chainSec}
		var wg sync.WaitGroup
		for _, c := range chains {
			wg.Add(1)
			go func(c func()) { defer wg.Done(); c() }(c)
		}
		wg.Wait()
		rn.evalM0301()
		rn.evalM0302()
		rn.evalM0305()
	}
	rn.evalM04()
	return a.saveRun(ctx, adm, in, rn, runAt)
}

func (a *App) saveRun(ctx context.Context, adm *auth.Admin, in *integRow, rn *runner, at time.Time) (*Run, error) {
	defs := content.LoadCases()
	run := &Run{RunID: rn.runID, IntegrationID: in.IntegrationID, BrandID: in.BrandID, At: at, Env: "联调环境", Cases: []Case{}}
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		for i, d := range defs {
			c := rn.res[d.CaseID]
			if c == nil {
				c = &caseOut{Result: "失败", FailReason: "本轮未执行到该用例"}
			}
			var fr *string
			if c.FailReason != "" {
				s := c.FailReason
				fr = &s
			}
			facts, _ := json.Marshal(c.Facts)
			if _, err := tx.Exec(ctx, `INSERT INTO run_cases (run_id, case_id, seq, grp, name, result, duration_ms, fail_reason, facts) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
				rn.runID, d.CaseID, i, d.Group, d.Name, c.Result, c.DurationMs, fr, facts); err != nil {
				return err
			}
			run.Cases = append(run.Cases, Case{CaseID: d.CaseID, Group: d.Group, Name: d.Name, Result: c.Result, DurationMs: c.DurationMs, FailReason: fr})
			run.Summary.Total++
			if c.Result == "通过" {
				run.Summary.Passed++
			} else {
				run.Summary.Failed++
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runs (run_id, integration_id, brand_id, at, total, passed, failed) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			rn.runID, in.IntegrationID, in.BrandID, at, run.Summary.Total, run.Summary.Passed, run.Summary.Failed); err != nil {
			return err
		}
		return a.audit(ctx, tx, adminActor(adm), "执行符合性测试", "integration", in.IntegrationID,
			fmt.Sprintf("%s：%d 项中 %d 项通过，%d 项未通过", rn.runID, run.Summary.Total, run.Summary.Passed, run.Summary.Failed), nil, nil,
			map[string]any{"runId": rn.runID, "passed": run.Summary.Passed, "failed": run.Summary.Failed})
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// trace: startConformanceRun · R-39
func (a *App) hStartRun(r *http.Request, adm *auth.Admin) (any, error) {
	var body struct {
		OperatorBaseURL string `json:"operatorBaseUrl"`
	}
	if err := decodeBody(r, &body, false); err != nil {
		return nil, err
	}
	// 测试轮次要跑完才落库，与请求方是否断开无关
	return a.StartRun(context.WithoutCancel(r.Context()), adm, r.PathValue("brandId"), body.OperatorBaseURL)
}

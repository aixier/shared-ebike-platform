package channel

// 联调用的模拟渠道：全平台本机联调时代替微信支付、支付宝。
// 行为按用户编号配置（PAY_MOCK_USERS），其余用默认（PAY_MOCK_DEFAULT）；结果未知的单在查询时按 PAY_MOCK_UNKNOWN_RESOLVE 补齐。
// 模拟渠道的交易存放在 Store 里（运行时用本服务数据库的 mock_channel_trades 表，重启不丢，次日对账仍能下载到账单）。

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

// Behavior 模拟渠道的行为
type Behavior string

const (
	BehaveOK        Behavior = "成功"
	BehaveFail      Behavior = "失败"
	BehaveUnknown   Behavior = "未知"
	BehaveUnsettled Behavior = "未到账" // 渠道返回成功，但对账单里没有这笔
	BehaveNextDay   Behavior = "跨日"  // 渠道返回成功，次日才入账
)

func parseBehavior(s string) (Behavior, error) {
	switch b := Behavior(strings.TrimSpace(s)); b {
	case BehaveOK, BehaveFail, BehaveUnknown, BehaveUnsettled, BehaveNextDay:
		return b, nil
	}
	return "", fmt.Errorf("模拟渠道行为只能是 成功、失败、未知、未到账、跨日，得到 %q", s)
}

// MockConfig 模拟渠道配置
type MockConfig struct {
	Default        Behavior
	Users          map[string]Behavior
	UnknownResolve Behavior // 结果未知的单被查询时的结局：成功、失败、未知（仍未知则交运维确认）
}

// ParseMockConfig 解析环境变量取值；空值取默认（成功、无、成功）。
func ParseMockConfig(def, users, resolve string) (MockConfig, error) {
	c := MockConfig{Default: BehaveOK, Users: map[string]Behavior{}, UnknownResolve: BehaveOK}
	var err error
	if strings.TrimSpace(def) != "" {
		if c.Default, err = parseBehavior(def); err != nil {
			return c, err
		}
	}
	if strings.TrimSpace(resolve) != "" {
		if c.UnknownResolve, err = parseBehavior(resolve); err != nil {
			return c, err
		}
	}
	for _, part := range strings.Split(users, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		u, b, ok := strings.Cut(part, ":")
		if !ok {
			return c, fmt.Errorf("PAY_MOCK_USERS 格式应为 用户编号:行为，得到 %q", part)
		}
		bh, err := parseBehavior(b)
		if err != nil {
			return c, err
		}
		c.Users[strings.TrimSpace(u)] = bh
	}
	return c, nil
}

// Trade 模拟渠道记下的一笔交易
type Trade struct {
	Channel     string
	OutNo       string
	Kind        QueryKind
	Merchant    Merchant
	UserID      string
	TradeNo     string
	Direction   string
	Amount      int64
	Status      Outcome
	FailReason  string
	Behavior    Behavior
	CompletedAt time.Time
	SettledDate string
}

// Store 模拟渠道的交易存储
type Store interface {
	Get(ctx context.Context, channel, outNo string) (*Trade, error)
	Put(ctx context.Context, t Trade) error
	Settled(ctx context.Context, channel string, m Merchant, date string) ([]Trade, error)
}

// MemStore 内存存储（单元测试用）
type MemStore struct {
	mu sync.Mutex
	m  map[string]Trade
	ks []string
}

func NewMemStore() *MemStore { return &MemStore{m: map[string]Trade{}} }

func (s *MemStore) Get(_ context.Context, ch, out string) (*Trade, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[ch+"|"+out]
	if !ok {
		return nil, nil
	}
	return &t, nil
}

func (s *MemStore) Put(_ context.Context, t Trade) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := t.Channel + "|" + t.OutNo
	if _, ok := s.m[k]; !ok {
		s.ks = append(s.ks, k)
	}
	s.m[k] = t
	return nil
}

func (s *MemStore) Settled(_ context.Context, ch string, m Merchant, date string) ([]Trade, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Trade
	for _, k := range s.ks {
		t := s.m[k]
		if t.Channel == ch && t.Merchant == m && t.Status == Succeeded && t.SettledDate == date {
			out = append(out, t)
		}
	}
	return out, nil
}

// Mock 模拟渠道
type Mock struct {
	Name  string
	Cfg   MockConfig
	Store Store
	Now   func() time.Time
	Loc   *time.Location
}

// NewMock 建一个模拟渠道
func NewMock(name string, cfg MockConfig, st Store) *Mock {
	return &Mock{Name: name, Cfg: cfg, Store: st, Now: time.Now, Loc: time.FixedZone("CST", 8*3600)}
}

func (m *Mock) behavior(userID string) Behavior {
	if b, ok := m.Cfg.Users[userID]; ok {
		return b
	}
	return m.Cfg.Default
}

func randDigits(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		b.WriteByte(byte('0' + d.Int64()))
	}
	return b.String()
}

// tradeNo 按原型的渠道交易号样式生成：微信收款 4200…、退款 5030…；支付宝 日期+22001…
func (m *Mock) tradeNo(kind QueryKind, at time.Time) string {
	if m.Name == Alipay {
		return at.In(m.Loc).Format("20060102") + "22001" + randDigits(15)
	}
	if kind == QueryRefund {
		return "5030" + randDigits(24)
	}
	return "4200" + randDigits(24)
}

func (m *Mock) settle(t *Trade, b Behavior, at time.Time) {
	t.Status = Succeeded
	t.CompletedAt = at
	d := at.In(m.Loc)
	switch b {
	case BehaveUnsettled:
		t.SettledDate = ""
	case BehaveNextDay:
		t.SettledDate = d.AddDate(0, 0, 1).Format("2006-01-02")
	default:
		t.SettledDate = d.Format("2006-01-02")
	}
}

func toResult(t *Trade) Result {
	return Result{Outcome: t.Status, TradeNo: t.TradeNo, FailReason: t.FailReason, CompletedAt: t.CompletedAt}
}

func (m *Mock) collect(ctx context.Context, merchant Merchant, out, user string, amount int64) (Result, error) {
	if old, err := m.Store.Get(ctx, m.Name, out); err != nil {
		return Result{}, err
	} else if old != nil {
		return toResult(old), nil
	}
	now := m.Now()
	b := m.behavior(user)
	t := Trade{Channel: m.Name, OutNo: out, Kind: QueryCharge, Merchant: merchant, UserID: user, Direction: "收",
		Amount: amount, Behavior: b, TradeNo: m.tradeNo(QueryCharge, now)}
	switch b {
	case BehaveFail:
		t.Status, t.FailReason = Failed, "用户支付账户余额不足"
	case BehaveUnknown:
		t.Status = Unknown
	default:
		m.settle(&t, b, now)
	}
	if err := m.Store.Put(ctx, t); err != nil {
		return Result{}, err
	}
	return toResult(&t), nil
}

// Charge 一键支付、押金收款
func (m *Mock) Charge(ctx context.Context, req ChargeReq) (Result, error) {
	return m.collect(ctx, req.Merchant, req.OutTradeNo, req.UserID, req.Amount)
}

// Prepay 一键支付第一步：记下一笔待支付的交易（状态未知），返回模拟的调起参数。
// 结果由之后的"渠道回调"（应用层按延迟调 Query）按该用户的行为给出；行为为"未知"的不回调，交给定时查询。
func (m *Mock) Prepay(ctx context.Context, req PrepayReq) (PrepayResult, error) {
	b := m.behavior(req.UserID)
	if old, err := m.Store.Get(ctx, m.Name, req.OutTradeNo); err != nil {
		return PrepayResult{}, err
	} else if old == nil {
		t := Trade{Channel: m.Name, OutNo: req.OutTradeNo, Kind: QueryCharge, Merchant: req.Merchant, UserID: req.UserID, Direction: "收",
			Amount: req.Amount, Behavior: b, TradeNo: m.tradeNo(QueryCharge, m.Now()), Status: Unknown}
		if err := m.Store.Put(ctx, t); err != nil {
			return PrepayResult{}, err
		}
	}
	params := map[string]any{"prepayId": "mock_prepay_" + req.OutTradeNo, "tip": "模拟渠道：无需真实调起支付，平台稍后自动模拟渠道回调"}
	if m.Name == Alipay {
		params = map[string]any{"tradeNO": "mock_trade_" + req.OutTradeNo, "tip": "模拟渠道：无需真实调起支付，平台稍后自动模拟渠道回调"}
	}
	return PrepayResult{Params: params, Mode: "mock", AutoCallback: b != BehaveUnknown}, nil
}

// CreditCharge 免押后扣款（支付分、芝麻信用），一律进收入商户号
func (m *Mock) CreditCharge(ctx context.Context, req CreditChargeReq) (Result, error) {
	return m.collect(ctx, IncomeMerchant, req.OutTradeNo, req.UserID, req.Amount)
}

// Refund 原路退回。收入商户号的退款同步出结果；押金商户号的退押先受理（未知），查询时到账或失败。
func (m *Mock) Refund(ctx context.Context, req RefundReq) (Result, error) {
	if old, err := m.Store.Get(ctx, m.Name, req.OutRefundNo); err != nil {
		return Result{}, err
	} else if old != nil {
		return toResult(old), nil
	}
	now := m.Now()
	b := m.behavior(req.UserID)
	t := Trade{Channel: m.Name, OutNo: req.OutRefundNo, Kind: QueryRefund, Merchant: req.Merchant, UserID: req.UserID,
		Direction: "付", Amount: req.Amount, Behavior: b, TradeNo: m.tradeNo(QueryRefund, now)}
	switch {
	case b == BehaveUnknown || req.Merchant == DepositMerchant:
		t.Status = Unknown
	case b == BehaveFail:
		t.Status, t.FailReason = Failed, "原支付账户已注销，渠道拒绝原路退回"
	default:
		m.settle(&t, b, now)
	}
	if err := m.Store.Put(ctx, t); err != nil {
		return Result{}, err
	}
	return toResult(&t), nil
}

// Query 查询；渠道无此单视为失败（未发生扣款）。
func (m *Mock) Query(ctx context.Context, req QueryReq) (Result, error) {
	t, err := m.Store.Get(ctx, m.Name, req.OutNo)
	if err != nil {
		return Result{}, err
	}
	if t == nil {
		return Result{Outcome: Failed, FailReason: "渠道无此交易"}, nil
	}
	if t.Status != Unknown {
		return toResult(t), nil
	}
	now := m.Now()
	// 退押的受理单：按该用户的行为给结局；结果未知的单：按 UnknownResolve 给结局
	end := m.Cfg.UnknownResolve
	if t.Behavior != BehaveUnknown {
		end = t.Behavior
	}
	switch end {
	case BehaveUnknown:
		return toResult(t), nil
	case BehaveFail:
		t.Status = Failed
		if t.Kind == QueryRefund {
			t.FailReason = "原支付账户已注销，渠道拒绝原路退回"
		} else {
			t.FailReason = "用户支付账户余额不足"
		}
	default:
		m.settle(t, end, now)
	}
	if err := m.Store.Put(ctx, *t); err != nil {
		return Result{}, err
	}
	return toResult(t), nil
}

// DownloadBill 某商户号某日的对账单（按到账日）
func (m *Mock) DownloadBill(ctx context.Context, mer Merchant, date string) ([]BillLine, error) {
	ts, err := m.Store.Settled(ctx, m.Name, mer, date)
	if err != nil {
		return nil, err
	}
	out := make([]BillLine, 0, len(ts))
	for _, t := range ts {
		out = append(out, BillLine{OutNo: t.OutNo, TradeNo: t.TradeNo, Direction: t.Direction, Amount: t.Amount, SettledDate: t.SettledDate})
	}
	return out, nil
}

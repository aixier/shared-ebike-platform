// Package credit 是信用免押的信用分查询接口（外部系统适配）。
// 真实渠道（微信支付分、芝麻信用）尚无接入条件，先提供联调用的模拟实现；
// 真实适配器实现 Scorer 接口后在 main 里替换即可。
package credit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Scorer 查询用户在某信用服务方的信用分。provider 取值：微信支付分｜芝麻信用。
type Scorer interface {
	Score(ctx context.Context, userID, provider string) (int, error)
}

// ErrUnavailable 信用渠道不可用。
var ErrUnavailable = errors.New("信用分查询渠道不可用")

// Mock 联调用模拟：按用户固定分值，未配置的取默认分。
type Mock struct {
	scores map[string]int
	def    int
}

// NewMock 由 `userId=分值` 逗号分隔的配置和默认分建模拟实现。
func NewMock(spec string, def int) (*Mock, error) {
	m := &Mock{scores: map[string]int{}, def: def}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("信用分配置格式应为 userId=分值：%q", part)
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("信用分不是整数：%q", part)
		}
		m.scores[strings.TrimSpace(k)] = n
	}
	return m, nil
}

// Score 返回配置的分值。
func (m *Mock) Score(_ context.Context, userID, _ string) (int, error) {
	if s, ok := m.scores[userID]; ok {
		return s, nil
	}
	return m.def, nil
}

// Unavailable 真实渠道未接入时的占位：一律返回不可用。
type Unavailable struct{}

// Score 总是返回 ErrUnavailable。
func (Unavailable) Score(context.Context, string, string) (int, error) { return 0, ErrUnavailable }

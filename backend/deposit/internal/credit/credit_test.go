package credit

import (
	"context"
	"testing"
)

// trace: R-16 信用免押的信用分查询（联调用模拟实现）
func TestMockScorer(t *testing.T) {
	m, err := NewMock("U100004=520, U100087=700", 650)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if s, _ := m.Score(ctx, "U100004", "微信支付分"); s != 520 {
		t.Fatalf("按用户固定分值，得到 %d", s)
	}
	if s, _ := m.Score(ctx, "U100087", "芝麻信用"); s != 700 {
		t.Fatalf("得到 %d", s)
	}
	if s, _ := m.Score(ctx, "U999999", "微信支付分"); s != 650 {
		t.Fatalf("未配置用户取默认分，得到 %d", s)
	}
	if _, err := NewMock("U1=abc", 650); err == nil {
		t.Fatal("非法分值应报错")
	}
}

func TestUnavailableScorer(t *testing.T) {
	var s Scorer = Unavailable{}
	if _, err := s.Score(context.Background(), "U1", "微信支付分"); err == nil {
		t.Fatal("真实渠道未接入时应报错")
	}
}

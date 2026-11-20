// Package external 本服务对外部授权与实名核验渠道的适配。
// 只做"取数据"：授权码换手机号、姓名与证件号是否一致；不含业务判断。
// ACCT_EXTERNAL_MODE=mock 用联调模拟实现；real 用真实渠道适配（缺商户资质，未对真实渠道验证）。
package external

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bike/acct/internal/idcard"
)

var (
	// ErrUnavailable 渠道不可用（故障、超时、未配置）
	ErrUnavailable = errors.New("外部服务不可用")
	// ErrInvalid 授权凭证无效或手机号格式不对
	ErrInvalid = errors.New("授权凭证无效")
	// ErrInvalidID 证件号无效
	ErrInvalidID = errors.New("证件号无效")
	// ErrNameMismatch 姓名与证件信息不一致
	ErrNameMismatch = errors.New("姓名与证件信息不一致")
)

var phonePat = regexp.MustCompile(`^1[3-9]\d{9}$`)

// Authorizer 用渠道授权凭证换手机号
type Authorizer interface {
	// Phone channel 为 微信｜支付宝；phone 只在模拟实现里使用
	Phone(ctx context.Context, channel, authCode, phone string) (string, error)
}

// RealNameVerifier 姓名与证件号一致性核验
type RealNameVerifier interface {
	Verify(ctx context.Context, name, idNo string) error
}

// ---------------- 模拟实现（联调用） ----------------

// MockAuthorizer 请求里直接带手机号
type MockAuthorizer struct{ down atomic.Bool }

// SetDown 模拟授权故障
func (m *MockAuthorizer) SetDown(v bool) { m.down.Store(v) }

func (m *MockAuthorizer) Phone(_ context.Context, _ string, authCode, phone string) (string, error) {
	if m.down.Load() {
		return "", ErrUnavailable
	}
	p := strings.TrimSpace(phone)
	if p == "" {
		p = strings.TrimSpace(authCode)
	}
	if !phonePat.MatchString(p) {
		return "", ErrInvalid
	}
	return p, nil
}

// MockRealName 证件校验位与生日校验；姓名一致性按登记表与"必不一致姓名"清单
type MockRealName struct {
	down     atomic.Bool
	registry map[string]string
	mismatch map[string]bool
}

// NewMockRealName registry 形如 "证件号:姓名;证件号:姓名"，mismatch 形如 "姓名,姓名"
func NewMockRealName(registry, mismatch string) *MockRealName {
	m := &MockRealName{registry: map[string]string{}, mismatch: map[string]bool{}}
	for _, it := range strings.Split(registry, ";") {
		id, name, ok := strings.Cut(strings.TrimSpace(it), ":")
		if ok {
			m.registry[idcard.Normalize(id)] = strings.TrimSpace(name)
		}
	}
	for _, n := range strings.Split(mismatch, ",") {
		if n = strings.TrimSpace(n); n != "" {
			m.mismatch[n] = true
		}
	}
	return m
}

// SetDown 模拟实名服务不可用（走先骑后验）
func (m *MockRealName) SetDown(v bool) { m.down.Store(v) }

func (m *MockRealName) Verify(_ context.Context, name, idNo string) error {
	if m.down.Load() {
		return ErrUnavailable
	}
	id := idcard.Normalize(idNo)
	if !idcard.Valid(id) {
		return ErrInvalidID
	}
	if m.mismatch[name] {
		return ErrNameMismatch
	}
	if want, ok := m.registry[id]; ok && want != name {
		return ErrNameMismatch
	}
	return nil
}

// ---------------- 真实渠道（缺商户资质，未对真实渠道验证） ----------------

// ChannelRouter 按渠道分派授权
type ChannelRouter map[string]Authorizer

func (r ChannelRouter) Phone(ctx context.Context, channel, authCode, phone string) (string, error) {
	a, ok := r[channel]
	if !ok || a == nil {
		return "", ErrUnavailable
	}
	return a.Phone(ctx, channel, authCode, phone)
}

// WeChatAuthorizer 微信小程序手机号快速验证：code 换手机号（getuserphonenumber）
type WeChatAuthorizer struct {
	AppID, Secret string
	BaseURL       string // 默认 https://api.weixin.qq.com
	HTTP          *http.Client

	mu      sync.Mutex
	token   string
	tokenAt time.Time
}

func (w *WeChatAuthorizer) client() *http.Client {
	if w.HTTP != nil {
		return w.HTTP
	}
	return &http.Client{Timeout: 3 * time.Second}
}

func (w *WeChatAuthorizer) base() string {
	if w.BaseURL != "" {
		return w.BaseURL
	}
	return "https://api.weixin.qq.com"
}

func (w *WeChatAuthorizer) accessToken(ctx context.Context) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.token != "" && time.Since(w.tokenAt) < 90*time.Minute {
		return w.token, nil
	}
	u := w.base() + "/cgi-bin/token?grant_type=client_credential&appid=" + url.QueryEscape(w.AppID) + "&secret=" + url.QueryEscape(w.Secret)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := w.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	var r struct {
		AccessToken string `json:"access_token"`
		ErrCode     int    `json:"errcode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.AccessToken == "" {
		return "", fmt.Errorf("%w: 取 access_token 失败 %d", ErrUnavailable, r.ErrCode)
	}
	w.token, w.tokenAt = r.AccessToken, time.Now()
	return w.token, nil
}

func (w *WeChatAuthorizer) Phone(ctx context.Context, _ string, authCode, _ string) (string, error) {
	if w.AppID == "" || w.Secret == "" {
		return "", fmt.Errorf("%w: 未配置微信小程序商户资质", ErrUnavailable)
	}
	if authCode == "" {
		return "", ErrInvalid
	}
	at, err := w.accessToken(ctx)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"code": authCode})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, w.base()+"/wxa/business/getuserphonenumber?access_token="+url.QueryEscape(at), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	var r struct {
		ErrCode   int `json:"errcode"`
		PhoneInfo struct {
			PurePhoneNumber string `json:"purePhoneNumber"`
		} `json:"phone_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	switch {
	case r.ErrCode == 0 && phonePat.MatchString(r.PhoneInfo.PurePhoneNumber):
		return r.PhoneInfo.PurePhoneNumber, nil
	case r.ErrCode == -1 || r.ErrCode == 40001 || r.ErrCode == 42001:
		// 系统繁忙或 access_token 失效
		w.mu.Lock()
		w.token = ""
		w.mu.Unlock()
		return "", ErrUnavailable
	default:
		return "", ErrInvalid
	}
}

// AlipayAuthorizer 支付宝小程序 my.getPhoneNumber：authCode 为加密的 response，用内容加密密钥 AES 解密。
// 验签（支付宝公钥 RSA2）未实现，缺商户资质无法对真实渠道验证。
type AlipayAuthorizer struct {
	AESKey string // base64
}

func (a *AlipayAuthorizer) Phone(_ context.Context, _ string, authCode, _ string) (string, error) {
	if a.AESKey == "" {
		return "", fmt.Errorf("%w: 未配置支付宝内容加密密钥", ErrUnavailable)
	}
	key, err := base64.StdEncoding.DecodeString(a.AESKey)
	if err != nil {
		return "", fmt.Errorf("%w: 密钥格式不对", ErrUnavailable)
	}
	enc, err := base64.StdEncoding.DecodeString(authCode)
	if err != nil || len(enc) == 0 || len(enc)%aes.BlockSize != 0 {
		return "", ErrInvalid
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("%w: 密钥长度不对", ErrUnavailable)
	}
	out := make([]byte, len(enc))
	cipher.NewCBCDecrypter(blk, make([]byte, aes.BlockSize)).CryptBlocks(out, enc)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize {
		return "", ErrInvalid
	}
	out = out[:len(out)-pad]
	var r struct {
		Code   string `json:"code"`
		Mobile string `json:"mobile"`
	}
	if json.Unmarshal(out, &r) != nil || r.Code != "10000" || !phonePat.MatchString(r.Mobile) {
		return "", ErrInvalid
	}
	return r.Mobile, nil
}

// RealRealName 真实实名核验渠道：服务商未定、缺商户资质，始终返回不可用（会走先骑后验或提示稍后再试）
type RealRealName struct{}

func (RealRealName) Verify(context.Context, string, string) error {
	return fmt.Errorf("%w: 未接入真实实名核验渠道", ErrUnavailable)
}

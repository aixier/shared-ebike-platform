package external

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"bike/acct/internal/idcard"
)

func TestMockAuthorizer(t *testing.T) {
	m := &MockAuthorizer{}
	ctx := context.Background()
	if p, err := m.Phone(ctx, "微信", "", "13812345678"); err != nil || p != "13812345678" {
		t.Fatalf("应直接取请求里的手机号: %v %s", err, p)
	}
	if p, err := m.Phone(ctx, "支付宝", "13912345678", ""); err != nil || p != "13912345678" {
		t.Fatalf("authCode 为手机号时也接受: %v %s", err, p)
	}
	if _, err := m.Phone(ctx, "微信", "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("没有手机号应 ErrInvalid: %v", err)
	}
	if _, err := m.Phone(ctx, "微信", "", "12345"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("格式不对应 ErrInvalid: %v", err)
	}
	m.SetDown(true)
	if _, err := m.Phone(ctx, "微信", "", "13812345678"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("模拟故障应 ErrUnavailable: %v", err)
	}
}

func TestMockRealName(t *testing.T) {
	id2 := "11010519900307299" + string(idcard.CheckChar("11010519900307299"))
	ctx := context.Background()
	m := NewMockRealName("11010519491231002X:张三", "李鬼")
	if err := m.Verify(ctx, "王五", "11010519491231002X"); !errors.Is(err, ErrNameMismatch) {
		t.Fatalf("登记表里证件号对应别的姓名应不一致: %v", err)
	}
	if err := m.Verify(ctx, "张三", "11010519491231002X"); err != nil {
		t.Fatalf("登记表一致应通过: %v", err)
	}
	if err := m.Verify(ctx, "李鬼", id2); !errors.Is(err, ErrNameMismatch) {
		t.Fatalf("配置为不一致的姓名应不一致: %v", err)
	}
	if err := m.Verify(ctx, "赵六", id2); err != nil {
		t.Fatalf("登记表外默认一致: %v", err)
	}
	if err := m.Verify(ctx, "赵六", "110105194912310022"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("校验位错误应 ErrInvalidID: %v", err)
	}
	m.SetDown(true)
	if err := m.Verify(ctx, "张三", "11010519491231002X"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("模拟不可用: %v", err)
	}
}

func TestRealRealNameUnconfigured(t *testing.T) {
	if err := (RealRealName{}).Verify(context.Background(), "张三", "11010519491231002X"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("未接入真实实名渠道应不可用: %v", err)
	}
}

func TestWeChatAuthorizer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/token":
			if r.URL.Query().Get("appid") != "app" {
				t.Errorf("appid 未带")
			}
			_, _ = w.Write([]byte(`{"access_token":"AT","expires_in":7200}`))
		case "/wxa/business/getuserphonenumber":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.URL.Query().Get("access_token") != "AT" || body["code"] != "C1" {
				_, _ = w.Write([]byte(`{"errcode":40029,"errmsg":"invalid code"}`))
				return
			}
			_, _ = w.Write([]byte(`{"errcode":0,"phone_info":{"purePhoneNumber":"13812345678","countryCode":"86"}}`))
		}
	}))
	defer srv.Close()
	w := &WeChatAuthorizer{AppID: "app", Secret: "s", BaseURL: srv.URL, HTTP: srv.Client()}
	p, err := w.Phone(context.Background(), "微信", "C1", "")
	if err != nil || p != "13812345678" {
		t.Fatalf("换手机号失败: %v %s", err, p)
	}
	if _, err := w.Phone(context.Background(), "微信", "BAD", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("无效授权码应 ErrInvalid: %v", err)
	}
	if _, err := (&WeChatAuthorizer{}).Phone(context.Background(), "微信", "C1", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("未配置应不可用: %v", err)
	}
}

func TestAlipayAuthorizer(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 16)
	plain := []byte(`{"code":"10000","msg":"Success","mobile":"13912345678"}`)
	pad := 16 - len(plain)%16
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	blk, _ := aes.NewCipher(key)
	enc := make([]byte, len(plain))
	cipher.NewCBCEncrypter(blk, make([]byte, 16)).CryptBlocks(enc, plain)
	a := &AlipayAuthorizer{AESKey: base64.StdEncoding.EncodeToString(key)}
	p, err := a.Phone(context.Background(), "支付宝", base64.StdEncoding.EncodeToString(enc), "")
	if err != nil || p != "13912345678" {
		t.Fatalf("解密失败: %v %s", err, p)
	}
	if _, err := a.Phone(context.Background(), "支付宝", "!!!", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("坏密文应 ErrInvalid: %v", err)
	}
}

func TestChannelRouter(t *testing.T) {
	r := ChannelRouter{"微信": &MockAuthorizer{}}
	if _, err := r.Phone(context.Background(), "支付宝", "", "13812345678"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("没配的渠道应不可用: %v", err)
	}
}

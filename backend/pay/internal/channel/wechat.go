package channel

// 微信支付适配骨架（github.com/wechatpay-apiv3/wechatpay-go）。
// 状态：缺商户号，未对真实渠道验证。退款、查询、交易账单下载按 APIv3 写出；
// 一键支付需前端调起（JSAPI 预下单返回调起参数），契约未提供 openid 与调起参数的往返，见 README；
// 支付分后扣（/v3/payscore/serviceorder/{out_order_no}/complete）SDK 无现成服务，按原始请求写出骨架。

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/jsapi"
	"github.com/wechatpay-apiv3/wechatpay-go/services/refunddomestic"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

// WeChatMerchant 一个商户号的凭据（全部来自环境变量，见 README）
type WeChatMerchant struct {
	MchID          string
	CertSerialNo   string
	PrivateKeyPath string
	APIv3Key       string
	AppID          string
	NotifyURL      string
	PayScoreID     string // 支付分服务 ID（只收入商户号用）
}

func (m WeChatMerchant) ok() bool {
	return m.MchID != "" && m.CertSerialNo != "" && m.PrivateKeyPath != "" && m.APIv3Key != "" && m.AppID != ""
}

// WeChatConfig 两个商户号（R-18）
type WeChatConfig struct {
	Income  WeChatMerchant
	Deposit WeChatMerchant
}

// WeChatPay 微信支付渠道
type WeChatPay struct {
	cfg     WeChatConfig
	clients map[Merchant]*core.Client
	loc     *time.Location
}

// NewWeChatPay 建微信支付渠道；缺商户号返回 ErrNotConfigured。
func NewWeChatPay(cfg WeChatConfig) (*WeChatPay, error) {
	if !cfg.Income.ok() || !cfg.Deposit.ok() {
		return nil, ErrNotConfigured
	}
	w := &WeChatPay{cfg: cfg, clients: map[Merchant]*core.Client{}, loc: time.FixedZone("CST", 8*3600)}
	for m, mc := range map[Merchant]WeChatMerchant{IncomeMerchant: cfg.Income, DepositMerchant: cfg.Deposit} {
		key, err := utils.LoadPrivateKeyWithPath(mc.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("读取商户私钥失败：%w", err)
		}
		c, err := core.NewClient(context.Background(),
			option.WithWechatPayAutoAuthCipher(mc.MchID, mc.CertSerialNo, key, mc.APIv3Key))
		if err != nil {
			return nil, fmt.Errorf("初始化微信支付客户端失败：%w", err)
		}
		w.clients[m] = c
	}
	return w, nil
}

func (w *WeChatPay) merchant(m Merchant) WeChatMerchant {
	if m == DepositMerchant {
		return w.cfg.Deposit
	}
	return w.cfg.Income
}

// Charge 一键支付、押金收款。微信 JSAPI 需前端拿预下单参数调起支付，结果经回调或查询得到；
// 契约的同步命令没有 openid 与调起参数的往返，这里不发起，明确失败，不假装成功。
func (w *WeChatPay) Charge(ctx context.Context, req ChargeReq) (Result, error) {
	_ = jsapi.PrepayRequest{} // 接入时用 jsapi.JsapiApiService.PrepayWithRequestPayment
	return Result{Outcome: Failed, FailReason: "微信支付一键支付尚未接通，请稍后再试"}, nil
}

// Prepay 一键支付第一步：JSAPI 预下单，返回前端调起支付的参数（骨架，未对真实渠道验证）。
func (w *WeChatPay) Prepay(ctx context.Context, req PrepayReq) (PrepayResult, error) {
	if strings.TrimSpace(req.OpenID) == "" {
		return PrepayResult{Outcome: Failed, FailReason: "缺少付款人 openid，无法发起微信支付"}, nil
	}
	mc := w.merchant(req.Merchant)
	svc := jsapi.JsapiApiService{Client: w.clients[req.Merchant]}
	resp, _, err := svc.PrepayWithRequestPayment(ctx, jsapi.PrepayRequest{
		Appid:       core.String(mc.AppID),
		Mchid:       core.String(mc.MchID),
		Description: core.String(req.Description),
		OutTradeNo:  core.String(req.OutTradeNo),
		TimeExpire:  core.Time(req.ExpiresAt),
		NotifyUrl:   core.String(mc.NotifyURL),
		Amount:      &jsapi.Amount{Total: core.Int64(req.Amount), Currency: core.String("CNY")},
		Payer:       &jsapi.Payer{Openid: core.String(req.OpenID)},
	})
	if err != nil {
		r, _ := w.apiErr(err, "微信支付预下单失败")
		if r.Outcome == Unknown {
			return PrepayResult{}, err
		}
		return PrepayResult{Outcome: r.Outcome, FailReason: r.FailReason}, nil
	}
	return PrepayResult{Mode: "real", Params: map[string]any{
		"appId": deref(resp.Appid), "timeStamp": deref(resp.TimeStamp), "nonceStr": deref(resp.NonceStr),
		"package": deref(resp.Package), "signType": deref(resp.SignType), "paySign": deref(resp.PaySign),
	}}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// CreditCharge 支付分后扣：完结支付分订单（骨架，未验证）。
func (w *WeChatPay) CreditCharge(ctx context.Context, req CreditChargeReq) (Result, error) {
	mc := w.cfg.Income
	body := map[string]any{
		"appid":         mc.AppID,
		"service_id":    mc.PayScoreID,
		"post_payments": []map[string]any{{"name": "骑行费", "amount": req.Amount}},
		"total_amount":  req.Amount,
	}
	u := fmt.Sprintf("https://api.mch.weixin.qq.com/v3/payscore/serviceorder/%s/complete", url.PathEscape(req.OutTradeNo))
	res, err := w.clients[IncomeMerchant].Post(ctx, u, body)
	if err != nil {
		return w.apiErr(err, "用户支付分扣款失败")
	}
	_ = res
	// 完结后扣款异步进行，结果以查询为准
	return Result{Outcome: Unknown}, nil
}

// Refund 原路退回
func (w *WeChatPay) Refund(ctx context.Context, req RefundReq) (Result, error) {
	svc := refunddomestic.RefundsApiService{Client: w.clients[req.Merchant]}
	mc := w.merchant(req.Merchant)
	cr := refunddomestic.CreateRequest{
		OutTradeNo:  core.String(req.OrigOutTradeNo),
		OutRefundNo: core.String(req.OutRefundNo),
		Reason:      core.String(req.Reason),
		Amount:      &refunddomestic.AmountReq{Refund: core.Int64(req.Amount), Total: core.Int64(req.OrigAmount), Currency: core.String("CNY")},
	}
	if mc.NotifyURL != "" {
		cr.NotifyUrl = core.String(mc.NotifyURL)
	}
	rf, _, err := svc.Create(ctx, cr)
	if err != nil {
		return w.apiErr(err, "原支付账户已注销，渠道拒绝原路退回")
	}
	return refundResult(rf), nil
}

func refundResult(rf *refunddomestic.Refund) Result {
	r := Result{Outcome: Unknown}
	if rf.RefundId != nil {
		r.TradeNo = *rf.RefundId
	}
	if rf.Status != nil {
		switch *rf.Status {
		case refunddomestic.STATUS_SUCCESS:
			r.Outcome = Succeeded
			if rf.SuccessTime != nil {
				r.CompletedAt = *rf.SuccessTime
			}
		case refunddomestic.STATUS_CLOSED, refunddomestic.STATUS_ABNORMAL:
			r.Outcome, r.FailReason = Failed, "原支付账户异常，渠道无法原路退回"
		}
	}
	return r
}

// Query 查询收款或退款结果
func (w *WeChatPay) Query(ctx context.Context, req QueryReq) (Result, error) {
	c := w.clients[req.Merchant]
	if req.Kind == QueryRefund {
		rf, _, err := (&refunddomestic.RefundsApiService{Client: c}).QueryByOutRefundNo(ctx, refunddomestic.QueryByOutRefundNoRequest{OutRefundNo: core.String(req.OutNo)})
		if err != nil {
			return w.apiErr(err, "渠道无此退款")
		}
		return refundResult(rf), nil
	}
	mc := w.merchant(req.Merchant)
	tx, _, err := (&jsapi.JsapiApiService{Client: c}).QueryOrderByOutTradeNo(ctx, jsapi.QueryOrderByOutTradeNoRequest{OutTradeNo: core.String(req.OutNo), Mchid: core.String(mc.MchID)})
	if err != nil {
		return w.apiErr(err, "渠道无此交易")
	}
	r := Result{Outcome: Unknown}
	if tx.TransactionId != nil {
		r.TradeNo = *tx.TransactionId
	}
	if tx.TradeState != nil {
		switch *tx.TradeState {
		case "SUCCESS":
			r.Outcome = Succeeded
			if tx.SuccessTime != nil {
				r.CompletedAt, _ = time.Parse(time.RFC3339, *tx.SuccessTime)
			}
		case "CLOSED", "REVOKED", "PAYERROR":
			r.Outcome, r.FailReason = Failed, "用户未完成支付"
		}
	}
	return r, nil
}

// DownloadBill 下载交易账单（ALL）并解析
func (w *WeChatPay) DownloadBill(ctx context.Context, m Merchant, date string) ([]BillLine, error) {
	c := w.clients[m]
	res, err := c.Get(ctx, "https://api.mch.weixin.qq.com/v3/bill/tradebill?bill_type=ALL&tar_type=GZIP&bill_date="+url.QueryEscape(date))
	if err != nil {
		return nil, err
	}
	var meta struct {
		DownloadURL string `json:"download_url"`
	}
	if err := json.NewDecoder(res.Response.Body).Decode(&meta); err != nil {
		return nil, err
	}
	dl, err := c.Get(ctx, meta.DownloadURL)
	if err != nil {
		return nil, err
	}
	defer dl.Response.Body.Close()
	zr, err := gzip.NewReader(dl.Response.Body)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	return ParseWeChatTradeBill(raw, date)
}

// apiErr：渠道明确拒绝（4xx）为失败，网络与 5xx 为结果未知
func (w *WeChatPay) apiErr(err error, reason string) (Result, error) {
	var ae *core.APIError
	if errors.As(err, &ae) && ae.StatusCode >= 400 && ae.StatusCode < 500 && ae.StatusCode != http.StatusTooManyRequests {
		return Result{Outcome: Failed, FailReason: reason}, nil
	}
	return Result{Outcome: Unknown}, err
}

// ParseWeChatTradeBill 解析微信支付交易账单 CSV（每个字段前带反引号）。
// 收款行（交易状态 SUCCESS）记为收；退款行（REFUND 且退款状态 SUCCESS）记为付。汇总行忽略。
func ParseWeChatTradeBill(raw []byte, date string) ([]BillLine, error) {
	r := csv.NewReader(bytes.NewReader(raw))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	idx := map[string]int{}
	for i, h := range rows[0] {
		idx[strings.TrimSpace(h)] = i
	}
	get := func(row []string, name string) string {
		i, ok := idx[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimPrefix(strings.TrimSpace(row[i]), "`")
	}
	var out []BillLine
	for _, row := range rows[1:] {
		if len(row) < len(rows[0]) {
			continue // 汇总区
		}
		if strings.HasPrefix(strings.TrimSpace(row[0]), "总") {
			break
		}
		switch get(row, "交易状态") {
		case "SUCCESS":
			a, err := yuanToFen(get(row, "订单金额"))
			if err != nil {
				return nil, err
			}
			out = append(out, BillLine{OutNo: get(row, "商户订单号"), TradeNo: get(row, "微信订单号"), Direction: "收", Amount: a, SettledDate: date})
		case "REFUND":
			if get(row, "退款状态") != "SUCCESS" {
				continue
			}
			a, err := yuanToFen(get(row, "申请退款金额"))
			if err != nil {
				return nil, err
			}
			out = append(out, BillLine{OutNo: get(row, "商户退款单号"), TradeNo: get(row, "微信退款单号"), Direction: "付", Amount: a, SettledDate: date})
		}
	}
	return out, nil
}

// yuanToFen 把"元"字符串转为分（整数运算，不经浮点）
func yuanToFen(s string) (int64, error) {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	ip, fp, _ := strings.Cut(s, ".")
	if ip == "" {
		ip = "0"
	}
	if len(fp) > 2 {
		return 0, fmt.Errorf("金额精度超过分：%q", s)
	}
	fp = (fp + "00")[:2]
	i, err := strconv.ParseInt(ip, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("金额格式不对：%q", s)
	}
	f, err := strconv.ParseInt(fp, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("金额格式不对：%q", s)
	}
	v := i*100 + f
	if neg {
		v = -v
	}
	return v, nil
}

// Unavailable 尚未接通的真实渠道（支付宝、芝麻信用）：一律明确失败，查询返回未知，不假装成功。
type Unavailable struct{ Name string }

func (u Unavailable) fail() (Result, error) {
	return Result{Outcome: Failed, FailReason: u.Name + "尚未接通，请换用其他支付方式"}, nil
}
func (u Unavailable) Charge(context.Context, ChargeReq) (Result, error) { return u.fail() }
func (u Unavailable) Prepay(context.Context, PrepayReq) (PrepayResult, error) {
	return PrepayResult{Outcome: Failed, FailReason: u.Name + "尚未接通，请换用其他支付方式"}, nil
}
func (u Unavailable) CreditCharge(context.Context, CreditChargeReq) (Result, error) { return u.fail() }
func (u Unavailable) Refund(context.Context, RefundReq) (Result, error) {
	return Result{Outcome: Unknown}, ErrNotConfigured
}
func (u Unavailable) Query(context.Context, QueryReq) (Result, error) {
	return Result{Outcome: Unknown}, ErrNotConfigured
}
func (u Unavailable) DownloadBill(context.Context, Merchant, string) ([]BillLine, error) {
	return nil, ErrNotConfigured
}

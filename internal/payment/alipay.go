package payment

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type AlipayConfig struct{ AppID, SellerID, PrivateKey, PublicKey, Mode string }
type Alipay struct {
	cfg     AlipayConfig
	private *rsa.PrivateKey
	public  *rsa.PublicKey
}

func NewAlipay(c AlipayConfig) (*Alipay, error) {
	if len(c.AppID) < 10 || strings.Trim(c.AppID, "0123456789") != "" || len(c.SellerID) < 10 || strings.Trim(c.SellerID, "0123456789") != "" {
		return nil, errors.New("请填写支付宝 APPID 和收款账号 PID")
	}
	if c.Mode == "" {
		c.Mode = "page"
	}
	if c.Mode != "page" && c.Mode != "wap" {
		return nil, errors.New("请选择电脑网站或手机网站支付")
	}
	pri, err := parsePrivate(c.PrivateKey)
	if err != nil {
		return nil, err
	}
	pub, err := parsePublic(c.PublicKey)
	if err != nil {
		return nil, err
	}
	return &Alipay{c, pri, pub}, nil
}
func (a *Alipay) Name() string                   { return "alipay" }
func (a *Alipay) Title() string                  { return "支付宝" }
func (a *Alipay) SupportsCurrency(c string) bool { return c == "CNY" }
func (a *Alipay) RefundURL(string) string        { return "https://b.alipay.com/" }
func (a *Alipay) Create(_ context.Context, r Request) (*Action, error) {
	if r.Currency != "CNY" || r.AmountCents <= 0 {
		return nil, errors.New("支付宝仅支持人民币正金额订单")
	}
	method, code := "alipay.trade.page.pay", "FAST_INSTANT_TRADE_PAY"
	if a.cfg.Mode == "wap" {
		method, code = "alipay.trade.wap.pay", "QUICK_WAP_WAY"
	}
	biz := map[string]string{"out_trade_no": r.OrderNo, "product_code": code, "subject": byteLimit(r.Subject, 128), "total_amount": fmt.Sprintf("%d.%02d", r.AmountCents/100, r.AmountCents%100), "seller_id": a.cfg.SellerID}
	if !r.ExpiresAt.IsZero() {
		biz["time_expire"] = r.ExpiresAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
	}
	b, err := json.Marshal(biz)
	if err != nil {
		return nil, err
	}
	p := map[string]string{"app_id": a.cfg.AppID, "method": method, "format": "JSON", "charset": "utf-8", "sign_type": "RSA2", "timestamp": time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05"), "version": "1.0", "notify_url": r.NotifyURL, "return_url": r.ReturnURL, "biz_content": string(b)}
	p["sign"], err = rsaSign(a.private, canonical(p, false))
	if err != nil {
		return nil, err
	}
	return &Action{Kind: "form", URL: "https://openapi.alipay.com/gateway.do", Fields: p}, nil
}
func (a *Alipay) VerifyCallback(_ context.Context, p map[string]string, body []byte) (*Callback, error) {
	if p["sign_type"] != "RSA2" || rsaVerify(a.public, canonical(p, true), p["sign"]) != nil {
		return nil, errors.New("invalid Alipay signature")
	}
	if p["app_id"] != a.cfg.AppID || p["seller_id"] != a.cfg.SellerID || p["trade_no"] == "" || p["out_trade_no"] == "" {
		return nil, errors.New("Alipay merchant or order mismatch")
	}
	cents, err := moneyToCents(p["total_amount"])
	if err != nil {
		return nil, err
	}
	return &Callback{Driver: a.Name(), TxnID: p["trade_no"], OrderNo: p["out_trade_no"], AmountCents: cents, Paid: p["trade_status"] == "TRADE_SUCCESS" || p["trade_status"] == "TRADE_FINISHED", Raw: string(body)}, nil
}

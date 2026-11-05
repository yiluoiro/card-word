package payment

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type EpayV2Config struct{ GatewayURL, PID, PayType, PrivateKey, PublicKey string }
type EpayV2 struct {
	cfg     EpayV2Config
	private *rsa.PrivateKey
	public  *rsa.PublicKey
}

func NewEpayV2(c EpayV2Config) (*EpayV2, error) {
	u, err := url.Parse(c.GatewayURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("易支付网关必须为 HTTPS 地址")
	}
	if c.PID == "" || strings.Trim(c.PID, "0123456789") != "" {
		return nil, errors.New("请填写易支付商户 PID")
	}
	pri, err := parsePrivate(c.PrivateKey)
	if err != nil {
		return nil, err
	}
	pub, err := parsePublic(c.PublicKey)
	if err != nil {
		return nil, err
	}
	c.GatewayURL = strings.TrimRight(c.GatewayURL, "/")
	return &EpayV2{c, pri, pub}, nil
}
func (e *EpayV2) Name() string                   { return "epay" }
func (e *EpayV2) Title() string                  { return "易支付" }
func (e *EpayV2) PayType() string                { return e.cfg.PayType }
func (e *EpayV2) SupportsCurrency(c string) bool { return c == "CNY" }
func (e *EpayV2) RefundURL(string) string        { return e.cfg.GatewayURL }
func (e *EpayV2) Create(_ context.Context, r Request) (*Action, error) {
	method, err := epayMethod(r.Method, e.cfg.PayType)
	if err != nil {
		return nil, err
	}
	if r.Currency != "CNY" || r.AmountCents <= 0 {
		return nil, errors.New("易支付仅支持人民币正金额订单")
	}
	p := map[string]string{"pid": e.cfg.PID, "type": method, "out_trade_no": r.OrderNo, "notify_url": r.NotifyURL, "return_url": r.ReturnURL, "name": byteLimit(r.Subject, 127), "money": fmt.Sprintf("%d.%02d", r.AmountCents/100, r.AmountCents%100), "timestamp": strconv.FormatInt(time.Now().Unix(), 10), "sign_type": "RSA"}
	p["sign"], err = rsaSign(e.private, canonical(p, true))
	if err != nil {
		return nil, err
	}
	return &Action{Kind: "form", URL: e.cfg.GatewayURL + "/api/pay/submit", Fields: p}, nil
}
func (e *EpayV2) VerifyCallback(_ context.Context, p map[string]string, body []byte) (*Callback, error) {
	if p["sign_type"] != "RSA" || rsaVerify(e.public, canonical(p, true), p["sign"]) != nil {
		return nil, errors.New("invalid EPay RSA signature")
	}
	if p["pid"] != e.cfg.PID || p["out_trade_no"] == "" || p["trade_no"] == "" {
		return nil, errors.New("EPay merchant or order mismatch")
	}
	// Retry notifications can be delayed: order settlement, not a short timestamp window, prevents replay.
	if ts, err := strconv.ParseInt(p["timestamp"], 10, 64); err != nil || ts <= 0 {
		return nil, errors.New("missing EPay timestamp")
	}
	cents, err := moneyToCents(p["money"])
	if err != nil {
		return nil, err
	}
	return &Callback{Method: p["type"], Driver: e.Name(), TxnID: p["trade_no"], OrderNo: p["out_trade_no"], AmountCents: cents, Paid: p["trade_status"] == "TRADE_SUCCESS", Raw: string(body)}, nil
}

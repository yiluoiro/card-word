package payment

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WeChat uses API v3 Native payments and the platform public-key verification mode.
type WeChatConfig struct{ AppID, MchID, SerialNo, PrivateKey, APIKey, PublicKeyID, PublicKey string }
type WeChat struct {
	cfg     WeChatConfig
	private *rsa.PrivateKey
	public  *rsa.PublicKey
	client  *http.Client
}

func NewWeChat(c WeChatConfig) (*WeChat, error) {
	if !strings.HasPrefix(c.AppID, "wx") || len(c.AppID) > 64 || c.MchID == "" || strings.Trim(c.MchID, "0123456789") != "" {
		return nil, errors.New("请填写微信 APPID 和商户号")
	}
	if c.SerialNo == "" || strings.Trim(c.SerialNo, "0123456789abcdefABCDEF") != "" {
		return nil, errors.New("请填写商户 API 证书序列号")
	}
	if len(c.APIKey) != 32 {
		return nil, errors.New("API v3 密钥必须为 32 字节")
	}
	if !strings.HasPrefix(c.PublicKeyID, "PUB_KEY_ID_") || strings.ContainsAny(c.PublicKeyID, "\r\n\" ") {
		return nil, errors.New("请填写微信支付公钥 ID（PUB_KEY_ID_ 开头）")
	}
	pri, err := parsePrivate(c.PrivateKey)
	if err != nil {
		return nil, err
	}
	pub, err := parsePublic(c.PublicKey)
	if err != nil {
		return nil, err
	}
	return &WeChat{c, pri, pub, &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (w *WeChat) Name() string                   { return "wechatpay" }
func (w *WeChat) Title() string                  { return "微信支付" }
func (w *WeChat) SupportsCurrency(c string) bool { return c == "CNY" }
func (w *WeChat) RefundURL(string) string        { return "https://pay.weixin.qq.com/" }
func (w *WeChat) Create(ctx context.Context, r Request) (*Action, error) {
	if r.Currency != "CNY" || r.AmountCents <= 0 {
		return nil, errors.New("微信支付仅支持人民币正金额订单")
	}
	payload := map[string]any{"appid": w.cfg.AppID, "mchid": w.cfg.MchID, "description": byteLimit(r.Subject, 127), "out_trade_no": r.OrderNo, "notify_url": r.NotifyURL, "amount": map[string]any{"total": r.AmountCents, "currency": "CNY"}}
	if !r.ExpiresAt.IsZero() {
		payload["time_expire"] = r.ExpiresAt.Format(time.RFC3339)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	ts, n := strconv.FormatInt(time.Now().Unix(), 10), hex.EncodeToString(nonce)
	const path = "/v3/pay/transactions/native"
	sig, err := rsaSign(w.private, "POST\n"+path+"\n"+ts+"\n"+n+"\n"+string(body)+"\n")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.mch.weixin.qq.com"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Wechatpay-Serial", w.cfg.PublicKeyID)
	req.Header.Set("Authorization", fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",timestamp="%s",serial_no="%s",signature="%s"`, w.cfg.MchID, n, ts, w.cfg.SerialNo, sig))
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, errors.New("微信支付请求失败")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("微信支付响应异常")
	}
	if err = w.verifySignedBody(resp.Header, raw); err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("微信支付未受理订单（HTTP %d），请检查商户配置及产品权限", resp.StatusCode)
	}
	var data struct {
		CodeURL string `json:"code_url"`
	}
	if json.Unmarshal(raw, &data) != nil || !strings.HasPrefix(data.CodeURL, "weixin://wxpay/") || len(data.CodeURL) > 2048 {
		return nil, errors.New("微信支付未返回有效二维码")
	}
	return &Action{Kind: "qr", URL: data.CodeURL, Message: "请使用微信扫描二维码付款"}, nil
}
func (w *WeChat) verifySignedBody(h http.Header, body []byte) error {
	if kind := h.Get("Wechatpay-Signature-Type"); kind != "" && kind != "WECHATPAY2-SHA256-RSA2048" {
		return errors.New("微信支付签名算法无效")
	}
	ts, nonce := h.Get("Wechatpay-Timestamp"), h.Get("Wechatpay-Nonce")
	timestamp, err := strconv.ParseInt(ts, 10, 64)
	now := time.Now().Unix()
	if err != nil || timestamp < now-300 || timestamp > now+300 || nonce == "" || len(nonce) > 256 || h.Get("Wechatpay-Serial") != w.cfg.PublicKeyID {
		return errors.New("微信支付签名时间或公钥 ID 无效")
	}
	if err = rsaVerify(w.public, ts+"\n"+nonce+"\n"+string(body)+"\n", h.Get("Wechatpay-Signature")); err != nil {
		return errors.New("微信支付验签失败")
	}
	return nil
}
func (w *WeChat) VerifyCallback(context.Context, map[string]string, []byte) (*Callback, error) {
	return nil, errors.New("微信支付回调需要原始请求头")
}
func (w *WeChat) VerifyNotification(_ context.Context, h http.Header, body []byte) (*Callback, error) {
	if err := w.verifySignedBody(h, body); err != nil {
		return nil, err
	}
	var event struct {
		EventType string `json:"event_type"`
		Resource  struct {
			Algorithm, Nonce, Ciphertext string
			AssociatedData               string `json:"associated_data"`
		} `json:"resource"`
	}
	if json.Unmarshal(body, &event) != nil || event.EventType != "TRANSACTION.SUCCESS" || event.Resource.Algorithm != "AEAD_AES_256_GCM" {
		return nil, errors.New("微信支付通知类型无效")
	}
	block, err := aes.NewCipher([]byte(w.cfg.APIKey))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(event.Resource.Nonce) != aead.NonceSize() {
		return nil, errors.New("微信支付通知 nonce 无效")
	}
	encrypted, err := base64.StdEncoding.DecodeString(event.Resource.Ciphertext)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, []byte(event.Resource.Nonce), encrypted, []byte(event.Resource.AssociatedData))
	if err != nil {
		return nil, errors.New("微信支付通知解密失败")
	}
	var t struct {
		AppID         string `json:"appid"`
		MchID         string `json:"mchid"`
		OutTradeNo    string `json:"out_trade_no"`
		TransactionID string `json:"transaction_id"`
		TradeState    string `json:"trade_state"`
		Amount        struct {
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if json.Unmarshal(plain, &t) != nil || t.AppID != w.cfg.AppID || t.MchID != w.cfg.MchID || t.OutTradeNo == "" || t.TransactionID == "" || t.Amount.Currency != "CNY" || t.Amount.Total <= 0 {
		return nil, errors.New("微信支付商户、订单或金额无效")
	}
	return &Callback{Driver: w.Name(), TxnID: t.TransactionID, OrderNo: t.OutTradeNo, AmountCents: t.Amount.Total, Paid: t.TradeState == "SUCCESS", Raw: string(body)}, nil
}

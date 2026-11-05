package payment

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func paymentTestKeys(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
}
func TestOfficialAlipayAndEpayV2(t *testing.T) {
	key, pri, pub := paymentTestKeys(t)
	ctx := context.Background()
	a, err := NewAlipay(AlipayConfig{AppID: "2026110512345678", SellerID: "2088123456789012", PrivateKey: pri, PublicKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{OrderNo: "order-123", Subject: "月卡", Currency: "CNY", AmountCents: 1234, ExpiresAt: time.Now().Add(time.Minute), NotifyURL: "https://shop.test/notify", ReturnURL: "https://shop.test/return"}
	action, err := a.Create(ctx, req)
	if err != nil || action.Fields["method"] != "alipay.trade.page.pay" || rsaVerify(&key.PublicKey, canonical(action.Fields, false), action.Fields["sign"]) != nil {
		t.Fatal("invalid signed Alipay request", err)
	}
	p := map[string]string{"app_id": a.cfg.AppID, "seller_id": a.cfg.SellerID, "trade_no": "ali123", "out_trade_no": req.OrderNo, "total_amount": "12.34", "trade_status": "TRADE_SUCCESS", "sign_type": "RSA2"}
	p["sign"], _ = rsaSign(key, canonical(p, true))
	cb, err := a.VerifyCallback(ctx, p, nil)
	if err != nil || !cb.Paid || cb.AmountCents != 1234 {
		t.Fatal(cb, err)
	}
	p["total_amount"] = "0.01"
	if _, err = a.VerifyCallback(ctx, p, nil); err == nil {
		t.Fatal("tampered amount accepted")
	}
	p["sign"], _ = rsaSign(key, canonical(p, true))
	p["seller_id"] = "other"
	p["sign"], _ = rsaSign(key, canonical(p, true))
	if _, err = a.VerifyCallback(ctx, p, nil); err == nil {
		t.Fatal("wrong seller accepted")
	}
	e, err := NewEpayV2(EpayV2Config{GatewayURL: "https://pay.novapis.com", PID: "1234", PrivateKey: pri, PublicKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	action, err = e.Create(ctx, req)
	if err != nil || action.URL != "https://pay.novapis.com/api/pay/submit" || rsaVerify(&key.PublicKey, canonical(action.Fields, true), action.Fields["sign"]) != nil {
		t.Fatal("invalid EPay V2 request", err)
	}
	p = map[string]string{"pid": "1234", "trade_no": "epay123", "out_trade_no": req.OrderNo, "money": "12.34", "trade_status": "TRADE_SUCCESS", "timestamp": strconv.FormatInt(time.Now().Unix(), 10), "sign_type": "RSA", "future_extension": "signed too"}
	p["sign"], _ = rsaSign(key, canonical(p, true))
	cb, err = e.VerifyCallback(ctx, p, nil)
	if err != nil || !cb.Paid || cb.AmountCents != 1234 {
		t.Fatal(cb, err)
	}
	p["future_extension"] = "modified"
	if _, err = e.VerifyCallback(ctx, p, nil); err == nil {
		t.Fatal("unsigned extension accepted")
	}
}

type paymentTransport func(*http.Request) (*http.Response, error)

func (f paymentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestWeChatSignedRequestResponseAndNotification(t *testing.T) {
	key, pri, pub := paymentTestKeys(t)
	c := WeChatConfig{AppID: "wx1234567890", MchID: "1234567890", SerialNo: "ABC123", PrivateKey: pri, PublicKey: pub, PublicKeyID: "PUB_KEY_ID_123", APIKey: strings.Repeat("k", 32)}
	w, err := NewWeChat(c)
	if err != nil {
		t.Fatal(err)
	}
	signedHeaders := func(body []byte) http.Header {
		h := http.Header{}
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		sig, _ := rsaSign(key, ts+"\nnonce\n"+string(body)+"\n")
		h.Set("Wechatpay-Serial", c.PublicKeyID)
		h.Set("Wechatpay-Timestamp", ts)
		h.Set("Wechatpay-Nonce", "nonce")
		h.Set("Wechatpay-Signature", sig)
		return h
	}
	w.client.Transport = paymentTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.mch.weixin.qq.com/v3/pay/transactions/native" {
			t.Fatal(r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil || payload["mchid"] != c.MchID {
			t.Fatal("bad request body")
		}
		fields := map[string]string{}
		for _, part := range strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "WECHATPAY2-SHA256-RSA2048 "), ",") {
			k, v, _ := strings.Cut(part, "=")
			fields[k] = strings.Trim(v, `"`)
		}
		if rsaVerify(&key.PublicKey, "POST\n"+r.URL.RequestURI()+"\n"+fields["timestamp"]+"\n"+fields["nonce_str"]+"\n"+string(body)+"\n", fields["signature"]) != nil {
			t.Fatal("bad request signature")
		}
		response := []byte(`{"code_url":"weixin://wxpay/bizpayurl?pr=test"}`)
		return &http.Response{StatusCode: 200, Header: signedHeaders(response), Body: io.NopCloser(strings.NewReader(string(response)))}, nil
	})
	action, err := w.Create(context.Background(), Request{OrderNo: "wx123", Subject: "测试", AmountCents: 100, Currency: "CNY", NotifyURL: "https://shop.test/notify"})
	if err != nil || action.Kind != "qr" {
		t.Fatal(action, err)
	}
	block, _ := aes.NewCipher([]byte(c.APIKey))
	gcm, _ := cipher.NewGCM(block)
	makeNotification := func(mchid string, nonce string) ([]byte, http.Header) {
		plain, _ := json.Marshal(map[string]any{"appid": c.AppID, "mchid": mchid, "out_trade_no": "wx123", "transaction_id": "transaction123", "trade_state": "SUCCESS", "amount": map[string]any{"total": 100, "currency": "CNY"}})
		encrypted := gcm.Seal(nil, []byte("123456789012"), plain, []byte("transaction"))
		body, _ := json.Marshal(map[string]any{"event_type": "TRANSACTION.SUCCESS", "resource": map[string]string{"algorithm": "AEAD_AES_256_GCM", "nonce": nonce, "associated_data": "transaction", "ciphertext": base64.StdEncoding.EncodeToString(encrypted)}})
		return body, signedHeaders(body)
	}
	body, h := makeNotification(c.MchID, "123456789012")
	cb, err := w.VerifyNotification(context.Background(), h, body)
	if err != nil || !cb.Paid || cb.AmountCents != 100 {
		t.Fatal(cb, err)
	}
	bad := append([]byte{}, body...)
	bad[len(bad)-2] ^= 1
	if _, err = w.VerifyNotification(context.Background(), h, bad); err == nil {
		t.Fatal("tampered body accepted")
	}
	for _, tc := range [][2]string{{"wrong", "123456789012"}, {c.MchID, "short"}} {
		body, h = makeNotification(tc[0], tc[1])
		if _, err = w.VerifyNotification(context.Background(), h, body); err == nil {
			t.Fatal("invalid merchant/nonce accepted")
		}
	}
	body, h = makeNotification(c.MchID, "123456789012")
	h.Set("Wechatpay-Serial", "PUB_KEY_ID_OTHER")
	if _, err = w.VerifyNotification(context.Background(), h, body); err == nil {
		t.Fatal("unknown key accepted")
	}
	h = signedHeaders(body)
	h.Set("Wechatpay-Timestamp", "1")
	if _, err = w.VerifyNotification(context.Background(), h, body); err == nil {
		t.Fatal("stale signature accepted")
	}
}

// Package payment defines the payment driver contract and ships the built-in
// drivers. Third party drivers are supplied by plugins through the same
// interface, so the checkout flow never needs to know which provider is used.
package payment

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Request describes a payment to start.
type Request struct {
	OrderNo     string
	Subject     string
	AmountCents int64
	Currency    string
	NotifyURL   string
	ReturnURL   string
	ClientIP    string
	Email       string
}

// Action tells the checkout page how to continue.
type Action struct {
	// Kind is "redirect" (send the browser to URL), "form" (POST Fields to
	// URL) or "inline" (render Message and wait for an out of band payment).
	Kind    string
	URL     string
	Fields  map[string]string
	Message string
}

// Callback is a verified provider notification.
type Callback struct {
	Driver      string
	TxnID       string
	OrderNo     string
	AmountCents int64
	Paid        bool
	Raw         string
}

// Driver is implemented by every payment provider.
type Driver interface {
	// Name is the stable identifier stored on orders.
	Name() string
	// Title is shown to buyers.
	Title() string
	// Create starts a payment and returns what the browser should do next.
	Create(ctx context.Context, req Request) (*Action, error)
	// VerifyCallback authenticates a provider notification. Returning an
	// error means the notification must be rejected.
	VerifyCallback(ctx context.Context, params map[string]string, body []byte) (*Callback, error)
	// RefundURL is an optional operator deep link.
	RefundURL(orderNo string) string
}

// Registry holds the available drivers.
type Registry struct {
	drivers map[string]Driver
	order   []string
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{drivers: map[string]Driver{}}
}

// Register adds a driver. Later registrations win, which lets a plugin
// override a built-in driver on purpose.
func (r *Registry) Register(d Driver) {
	if d == nil {
		return
	}
	if _, ok := r.drivers[d.Name()]; !ok {
		r.order = append(r.order, d.Name())
	}
	r.drivers[d.Name()] = d
}

// Unregister removes a driver by name.
func (r *Registry) Unregister(name string) {
	delete(r.drivers, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// Get resolves a driver by name.
func (r *Registry) Get(name string) (Driver, bool) {
	d, ok := r.drivers[name]
	return d, ok
}

// List returns drivers in registration order.
func (r *Registry) List() []Driver {
	out := make([]Driver, 0, len(r.order))
	for _, n := range r.order {
		if d, ok := r.drivers[n]; ok {
			out = append(out, d)
		}
	}
	return out
}

// ---------- manual driver ----------

// Manual is an offline driver: the buyer pays out of band (bank transfer,
// chat, crypto) and an operator confirms the order in the admin console. It
// also makes the whole flow testable without a third party account.
type Manual struct {
	Instructions string
}

// Name implements Driver.
func (m *Manual) Name() string { return "manual" }

// Title implements Driver.
func (m *Manual) Title() string { return "Manual / offline confirmation" }

// Create implements Driver.
func (m *Manual) Create(_ context.Context, req Request) (*Action, error) {
	msg := m.Instructions
	if strings.TrimSpace(msg) == "" {
		msg = "请联系客服并附上订单号完成付款，付款后由管理员确认发货。"
	}
	return &Action{
		Kind:    "inline",
		Message: fmt.Sprintf("%s\n订单号: %s", msg, req.OrderNo),
	}, nil
}

// VerifyCallback implements Driver. The manual driver never receives
// callbacks; confirmation happens through an authenticated admin action.
func (m *Manual) VerifyCallback(_ context.Context, _ map[string]string, _ []byte) (*Callback, error) {
	return nil, errors.New("payment: manual driver does not accept callbacks")
}

// RefundURL implements Driver.
func (m *Manual) RefundURL(string) string { return "" }

// ---------- EPay (彩虹易支付) compatible driver ----------

// EpayConfig configures an EPay compatible gateway.
type EpayConfig struct {
	GatewayURL string // e.g. https://pay.example.com
	PID        string
	Key        string
	PayType    string // alipay, wxpay, qqpay ...
}

// Epay implements the widely used 彩虹易支付 protocol.
type Epay struct {
	cfg EpayConfig
}

// NewEpay builds an EPay driver.
func NewEpay(cfg EpayConfig) (*Epay, error) {
	if strings.TrimSpace(cfg.GatewayURL) == "" {
		return nil, errors.New("payment: epay gateway url required")
	}
	if strings.TrimSpace(cfg.PID) == "" || strings.TrimSpace(cfg.Key) == "" {
		return nil, errors.New("payment: epay pid and key required")
	}
	if cfg.PayType == "" {
		cfg.PayType = "alipay"
	}
	cfg.GatewayURL = strings.TrimRight(cfg.GatewayURL, "/")
	return &Epay{cfg: cfg}, nil
}

// Name implements Driver.
func (e *Epay) Name() string { return "epay" }

// Title implements Driver.
func (e *Epay) Title() string { return "EPay (Alipay / WeChat / QQ)" }

// Create implements Driver.
func (e *Epay) Create(_ context.Context, req Request) (*Action, error) {
	money := strconv.FormatFloat(float64(req.AmountCents)/100.0, 'f', 2, 64)
	params := map[string]string{
		"pid":          e.cfg.PID,
		"type":         e.cfg.PayType,
		"out_trade_no": req.OrderNo,
		"notify_url":   req.NotifyURL,
		"return_url":   req.ReturnURL,
		"name":         req.Subject,
		"money":        money,
		"clientip":     req.ClientIP,
		"sign_type":    "MD5",
	}
	params["sign"] = epaySign(params, e.cfg.Key)
	return &Action{Kind: "form", URL: e.cfg.GatewayURL + "/submit.php", Fields: params}, nil
}

// VerifyCallback implements Driver.
func (e *Epay) VerifyCallback(_ context.Context, params map[string]string, body []byte) (*Callback, error) {
	if len(params) == 0 {
		return nil, errors.New("payment: empty epay callback")
	}
	sign := params["sign"]
	if sign == "" {
		return nil, errors.New("payment: epay callback without signature")
	}
	want := epaySign(params, e.cfg.Key)
	if !hmac.Equal([]byte(strings.ToLower(want)), []byte(strings.ToLower(sign))) {
		return nil, errors.New("payment: epay signature mismatch")
	}
	// A callback signed with our key but carrying another merchant id means a
	// misconfigured or swapped gateway; never settle an order in that case.
	if pid := strings.TrimSpace(params["pid"]); pid != "" && pid != e.cfg.PID {
		return nil, fmt.Errorf("payment: epay pid mismatch (got %q)", pid)
	}
	orderNo := firstNonEmpty(params["out_trade_no"], params["trade_no"])
	if orderNo == "" {
		return nil, errors.New("payment: epay callback without trade number")
	}
	moneyCents, err := moneyToCents(params["money"])
	if err != nil {
		return nil, err
	}
	paid := strings.EqualFold(params["trade_status"], "TRADE_SUCCESS")
	return &Callback{
		Driver:      e.Name(),
		TxnID:       params["trade_no"],
		OrderNo:     params["out_trade_no"],
		AmountCents: moneyCents,
		Paid:        paid,
		Raw:         string(body),
	}, nil
}

// RefundURL implements Driver.
func (e *Epay) RefundURL(string) string { return e.cfg.GatewayURL }

// epaySign reproduces the EPay MD5 signature: sorted key=value pairs joined by
// '&' (sign, sign_type and empty values excluded) plus the merchant key.
func epaySign(params map[string]string, key string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(params[k])
	}
	sum := md5.Sum([]byte(sb.String() + key)) //nolint:gosec // protocol requirement
	return hex.EncodeToString(sum[:])
}

// HMACSign signs a payload with SHA-256 HMAC, used for plugin callbacks.
func HMACSign(key []byte, payload []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func moneyToCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("payment: missing amount")
	}
	parts := strings.SplitN(s, ".", 2)
	units, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("payment: invalid amount %q", s)
	}
	var cents int64
	if len(parts) == 2 {
		frac := parts[1]
		for len(frac) < 2 {
			frac += "0"
		}
		frac = frac[:2]
		c, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("payment: invalid amount %q", s)
		}
		cents = c
	}
	return units*100 + cents, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

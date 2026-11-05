package httpapi_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/httpapi"
	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/store"
)

func TestCheckoutFormSupportsRepeatPurchases(t *testing.T) {
	h := newHarness(t)
	p := h.seedProduct(t)
	var locations []string
	var previous string
	for range 2 {
		resp, body := h.get("/p/" + p.Slug)
		if resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode)
		}
		match := regexp.MustCompile(`name="idem" value="([^"]+)"`).FindStringSubmatch(body)
		if len(match) != 2 || match[1] == previous {
			t.Fatal("each purchase form needs a fresh idempotency key")
		}
		previous = match[1]
		if !strings.Contains(body, `max="2"`) {
			t.Fatal("quantity must respect available stock")
		}
		form := url.Values{"product_id": {strconv.FormatInt(p.ID, 10)}, "quantity": {"1"}, "driver": {"manual"}, "agree": {"1"}, "idem": {match[1]}}
		checkout := h.postForm("/checkout", form)
		location := checkout.Header.Get("Location")
		if !strings.HasPrefix(location, "/order/") {
			t.Fatalf("checkout: %s", location)
		}
		locations = append(locations, location)
		_, orderBody := h.get(location)
		u, _ := url.Parse(location)
		if !strings.Contains(orderBody, "<code>"+u.Query().Get("token")+"</code>") {
			t.Fatal("buyer must be able to save the query credential")
		}
		replay := h.postForm("/checkout", form)
		if replay.Header.Get("Location") != u.Path {
			t.Fatalf("authorized replay should reopen order: %s", replay.Header.Get("Location"))
		}
	}
	if locations[0] == locations[1] {
		t.Fatal("repeat purchase reused an old order")
	}
}

func TestErrorPageHasHTMLContentType(t *testing.T) {
	h := newHarness(t)
	resp, body := h.get("/p/not-found")
	if resp.StatusCode != 404 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(body, "商品不存在") {
		t.Fatalf("invalid error response: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	h.get("/account/register")
	resp = h.postForm("/account/register", url.Values{"username": {"x"}, "password": {"short"}})
	if resp.StatusCode != 400 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatal("invalid registration response must remain HTML with its original status")
	}
}

type reviewPaymentDriver struct {
	calls    int
	action   *payment.Action
	callback *payment.Callback
}

func (*reviewPaymentDriver) Name() string  { return "review" }
func (*reviewPaymentDriver) Title() string { return "Review" }
func (d *reviewPaymentDriver) Create(context.Context, payment.Request) (*payment.Action, error) {
	d.calls++
	return d.action, nil
}
func (d *reviewPaymentDriver) VerifyCallback(context.Context, map[string]string, []byte) (*payment.Callback, error) {
	return d.callback, nil
}
func (*reviewPaymentDriver) RefundURL(string) string { return "" }

func TestPaymentHandoffCSPAndValidation(t *testing.T) {
	h := newHarness(t)
	p := h.seedProduct(t)
	d := &reviewPaymentDriver{action: &payment.Action{Kind: "form", URL: "https://payments.example.test/submit", Fields: map[string]string{"order": "review"}}}
	h.payments.Register(d)
	order, err := h.db.CreateOrder(context.Background(), store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: d.Name(), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	lookup := h.solved("query", url.Values{"order_no": {order.Order.OrderNo}, "token": {order.QueryToken}})
	if w := h.postForm("/query", lookup); w.StatusCode != 303 {
		t.Fatal("order lookup failed")
	}
	path := "/order/" + order.Order.OrderNo + "/pay"
	resp, body := h.get(path)
	if strings.Contains(body, "data-autosubmit") || !strings.Contains(body, "captcha_id") {
		t.Fatal("GET initiated payment")
	}
	pay := func() (*http.Response, string) {
		return postUXForm(t, h, path, h.solved("pay:"+order.Order.OrderNo, url.Values{}))
	}
	resp, body = pay()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "form-action 'self' https://payments.example.test") || !strings.Contains(body, "data-autosubmit") {
		t.Fatal("provider form is blocked")
	}
	resp, _ = h.get("/")
	if strings.Contains(resp.Header.Get("Content-Security-Policy"), "payments.example.test") {
		t.Fatal("handoff policy leaked to other pages")
	}
	for _, action := range []*payment.Action{nil, {Kind: "unknown"}, {Kind: "redirect", URL: "javascript:alert(1)"}, {Kind: "form", URL: "https://user:pass@payments.example.test"}, {Kind: "form", URL: "https://*.example.test"}, {Kind: "form", URL: "https://example.test;"}} {
		d.action = action
		resp, _ = pay()
		if resp.StatusCode != 502 {
			t.Fatalf("invalid payment action accepted: %d", resp.StatusCode)
		}
	}
}

func TestFailedCallbackIsNotAcknowledged(t *testing.T) {
	h := newHarness(t)
	p := h.seedProduct(t)
	order, err := h.db.CreateOrder(context.Background(), store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "review", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	d := &reviewPaymentDriver{callback: &payment.Callback{Driver: "review", OrderNo: order.Order.OrderNo, TxnID: "review-transaction", AmountCents: 1, Paid: true}}
	h.payments.Register(d)
	resp := h.postForm("/pay/notify/review", nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("failed fulfillment acknowledged: %d", resp.StatusCode)
	}
	d.callback.AmountCents = order.Order.TotalCents
	resp = h.postForm("/pay/notify/review", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	resp = h.postForm("/pay/notify/review", nil)
	if resp.StatusCode != 200 {
		t.Fatal("duplicate successful notification must be acknowledged")
	}
}

func TestClearedPaymentSettingsRemoveDriver(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for k, v := range map[string]string{"payment.epay.gateway": "https://payments.example.test", "payment.epay.pid": "review", "payment.epay.key": "review-secret"} {
		if err := h.db.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	httpapi.LoadIntegrations(h.app, ctx)
	if _, ok := h.payments.Get("epay"); !ok {
		t.Fatal("driver not loaded")
	}
	if err := h.db.SetSetting(ctx, "payment.epay.gateway", ""); err != nil {
		t.Fatal(err)
	}
	httpapi.LoadIntegrations(h.app, ctx)
	if _, ok := h.payments.Get("epay"); ok {
		t.Fatal("removed configuration left a stale driver")
	}
}

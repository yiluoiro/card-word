package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/store"
)

func TestAPICheckoutHonorsStoreRestrictions(t *testing.T) {
	for _, tc := range []struct {
		name, selected, driver, maintenance string
		want                                int
	}{
		{"maintenance", "manual", "manual", "1", 503},
		{"disabled channel", "manual", "epay", "0", 400},
		{"all default", "all", "", "0", 201},
		{"list default", "manual,epay", "", "0", 201},
		{"missing configured channel", "missing", "manual", "0", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := securityServer(t)
			ctx := context.Background()
			p := securityProduct(t, s)
			ep, err := payment.NewEpay(payment.EpayConfig{GatewayURL: "https://pay.example.test", PID: "test", Key: "test"})
			if err != nil {
				t.Fatal(err)
			}
			s.payments.Register(ep)
			if err := s.db.SetSettings(ctx, map[string]string{store.SettingPaymentDriver: tc.selected, store.SettingMaintenance: tc.maintenance}); err != nil {
				t.Fatal(err)
			}
			if err := s.reloadSettings(ctx); err != nil {
				t.Fatal(err)
			}
			_, token, err := s.db.CreateAPIToken(ctx, store.TokenInput{Name: "checkout", Scopes: []string{"orders:write"}})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/api/v1/orders", strings.NewReader(fmt.Sprintf(`{"product_id":%d,"quantity":1,"driver":%q}`, p.ID, tc.driver)))
			r.Header.Set("Authorization", "Bearer "+token)
			w := solveAPI(t, s.Handler(), s, r)
			if w.Code != tc.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			n, err := s.db.CountOrders(ctx, store.OrderFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if (tc.want == 201 && n != 1) || (tc.want != 201 && n != 0) {
				t.Fatalf("unexpected orders: %d", n)
			}
		})
	}
}

func TestUnpublishedProductIsNotExposedByAlternateEndpoints(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	p := securityProduct(t, s)
	if _, err := s.db.SetProductsStatus(ctx, []int64{p.ID}, store.ProductDraft); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.db.CreateAPIToken(ctx, store.TokenInput{Name: "catalog", Scopes: []string{"products:read"}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/products/"+p.Slug, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 404 || strings.Contains(w.Body.String(), p.Name) {
		t.Fatalf("draft API disclosure: %d %s", w.Code, w.Body.String())
	}
	csrf := strings.Repeat("a", 32)
	r = securityRequest(nil, "POST", "/checkout", url.Values{"product_id": {fmt.Sprint(p.ID)}})
	r.AddCookie(&http.Cookie{Name: s.cookieName(cookieCSRF), Value: csrf})
	r.Header.Set("X-CSRF-Token", csrf)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 404 || strings.Contains(w.Body.String(), p.Name) {
		t.Fatalf("draft checkout disclosure: %d %s", w.Code, w.Body.String())
	}
}

func TestAPIRejectsTrailingAndOversizedJSON(t *testing.T) {
	s := securityServer(t)
	_, token, err := s.db.CreateAPIToken(context.Background(), store.TokenInput{Name: "json", Scopes: []string{"orders:write"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"quantity":1} {"quantity":2}`, `{"quantity":1}` + strings.Repeat(" ", 64<<10)} {
		r := httptest.NewRequest("POST", "/api/v1/orders", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 400 && w.Code != 413 {
			t.Fatalf("accepted invalid body, status %d", w.Code)
		}
	}
}

func TestCurrencyRestrictedPaymentCannotSettleForeignOrder(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	ep, err := payment.NewEpay(payment.EpayConfig{GatewayURL: "https://pay.example.test", PID: "test", Key: "test"})
	if err != nil {
		t.Fatal(err)
	}
	s.payments.Register(ep)
	if err := s.db.SetSetting(ctx, store.SettingPaymentDriver, "epay"); err != nil {
		t.Fatal(err)
	}
	if err := s.reloadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.db.CreateProduct(ctx, store.ProductInput{Name: "USD", Slug: "usd", PriceCents: 100, Currency: "USD", Status: store.ProductActive, MinQty: 1, MaxQty: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.AddCards(ctx, p.ID, []string{"foreign-currency-card"}, ""); err != nil {
		t.Fatal(err)
	}
	if len(s.availableDriversForCurrency("USD")) != 0 || len(s.availableDriversForCurrency("CNY")) != 1 {
		t.Fatal("currency filter did not match driver support")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/p/usd", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), `name="driver" value="epay"`) || !strings.Contains(w.Body.String(), "暂无可用支付方式") {
		t.Fatalf("unsupported checkout displayed: %d", w.Code)
	}
	_, token, err := s.db.CreateAPIToken(ctx, store.TokenInput{Name: "currency", Scopes: []string{"orders:write"}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/orders", strings.NewReader(fmt.Sprintf(`{"product_id":%d,"quantity":1,"driver":"epay"}`, p.ID)))
	r.Header.Set("Authorization", "Bearer "+token)
	w = solveAPI(t, s.Handler(), s, r)
	if w.Code != 400 {
		t.Fatalf("API accepted foreign settlement: %d", w.Code)
	}
	// Existing orders created before the fix must also reject a yuan callback.
	o, err := s.db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "epay"})
	if err != nil {
		t.Fatal(err)
	}
	err = s.completePayment(ctx, &payment.Callback{Driver: "epay", OrderNo: o.Order.OrderNo, TxnID: "foreign", AmountCents: 100, Paid: true}, "127.0.0.1")
	if err == nil {
		t.Fatal("settled foreign-currency order with a yuan callback")
	}
	stored, err := s.db.OrderByNo(ctx, o.Order.OrderNo)
	if err != nil || stored.Status != store.OrderPending {
		t.Fatal("rejected callback changed order")
	}
}

func TestMaintenanceBlocksPaymentInitiation(t *testing.T) {
	s := securityServer(t)
	if err := s.db.SetSetting(context.Background(), store.SettingMaintenance, "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.reloadSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handlePay(w, httptest.NewRequest("GET", "/order/test/pay", nil))
	if w.Code != 503 {
		t.Fatalf("payment not blocked during maintenance: %d", w.Code)
	}
}

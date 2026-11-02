package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/config"
	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/httpapi"
	"github.com/yiluoiro/card-word/internal/market"
	"github.com/yiluoiro/card-word/internal/notify"
	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/plugin"
	"github.com/yiluoiro/card-word/internal/store"
)

type harness struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	db     *store.DB
	cfg    *config.Config
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	key, err := cryptoutil.RandomBytes(32)
	if err != nil {
		t.Fatalf("random key: %v", err)
	}
	cfg := &config.Config{
		Addr:               "127.0.0.1:0",
		BaseURL:            "http://127.0.0.1",
		DataDir:            dir,
		DBPath:             filepath.Join(dir, "test.db"),
		Env:                "development",
		MasterKey:          key,
		PluginDir:          filepath.Join(dir, "plugins"),
		SessionTTL:         time.Hour,
		SessionIdle:        time.Hour,
		OrderTTL:           15 * time.Minute,
		MaxBodyBytes:       1 << 20,
		RateLimitRPS:       10000,
		RateLimitBurst:     10000,
		ReadPoolSize:       4,
		DBBusyTimeout:      5 * time.Second,
		OutboundTimeout:    5 * time.Second,
		MarketAllowInstall: true,
	}
	db, err := store.Open(store.Options{
		Path:         cfg.DBPath,
		ReadPoolSize: 4,
		BusyTimeout:  5 * time.Second,
		MasterKey:    key,
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	installer := plugin.NewInstaller(cfg.PluginDir, false, nil)
	host := plugin.NewHost(db, installer, logger)
	if err := host.Reload(context.Background()); err != nil {
		t.Fatalf("plugin reload: %v", err)
	}
	tmpl, err := httpapi.Templates()
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	srv, err := httpapi.New(httpapi.Deps{
		Config:    cfg,
		DB:        db,
		Logger:    logger,
		Plugins:   host,
		Installer: installer,
		Market:    market.NewClient(market.Builtin()),
		Payments:  payment.NewRegistry(),
		Notifier:  notify.NewDispatcher(2*time.Second, nil),
		Templates: tmpl,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	httpapi.LoadIntegrations(srv, context.Background())

	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Start()
	t.Cleanup(ts.Close)
	cfg.BaseURL = ts.URL

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &harness{t: t, server: ts, client: client, db: db, cfg: cfg}
}

func (h *harness) csrf() string {
	h.t.Helper()
	base, _ := url.Parse(h.server.URL)
	for _, c := range h.client.Jar.Cookies(base) {
		if c.Name == "cw_csrf" {
			return c.Value
		}
	}
	return ""
}

func (h *harness) get(path string) (*http.Response, string) {
	h.t.Helper()
	resp, err := h.client.Get(h.server.URL + path)
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

func (h *harness) postForm(path string, values url.Values) *http.Response {
	h.t.Helper()
	if values == nil {
		values = url.Values{}
	}
	if values.Get("csrf") == "" {
		values.Set("csrf", h.csrf())
	}
	resp, err := h.client.PostForm(h.server.URL+path, values)
	if err != nil {
		h.t.Fatalf("POST %s: %v", path, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func (h *harness) seedProduct(t *testing.T) *store.Product {
	t.Helper()
	ctx := context.Background()
	product, err := h.db.CreateProduct(ctx, store.ProductInput{
		Name:       "端到端商品",
		Slug:       "e2e-product",
		PriceCents: 990,
		Currency:   "CNY",
		Status:     store.ProductActive,
		MinQty:     1,
		MaxQty:     5,
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	if _, err := h.db.AddCards(ctx, product.ID, []string{"E2E-KEY-1", "E2E-KEY-2"}, "e2e"); err != nil {
		t.Fatalf("add cards: %v", err)
	}
	return product
}

func TestStorefrontAndAdminFlow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	product := h.seedProduct(t)

	if _, err := h.db.CreateUser(ctx, "operator", "super-secret-passphrase", "admin"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Homepage renders and contains the product.
	resp, body := h.get("/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("home status %d", resp.StatusCode)
	}
	if !strings.Contains(body, "端到端商品") {
		t.Fatal("homepage does not list the product")
	}

	// Product page.
	resp, body = h.get("/p/e2e-product")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "立即购买") {
		t.Fatalf("product page unexpected: %d", resp.StatusCode)
	}

	// Admin is protected.
	resp, _ = h.get("/admin")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("expected redirect to login, got %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	// Login requeries a CSRF token from the login page first.
	_, _ = h.get("/admin/login")
	login := h.postForm("/admin/login", url.Values{
		"username": {"operator"},
		"password": {"super-secret-passphrase"},
	})
	if login.StatusCode != http.StatusSeeOther || login.Header.Get("Location") != "/admin" {
		t.Fatalf("login failed: %d %s", login.StatusCode, login.Header.Get("Location"))
	}
	resp, body = h.get("/admin")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "概览") {
		t.Fatalf("dashboard failed: %d", resp.StatusCode)
	}

	// CSRF must be enforced: an empty token is rejected before any work happens.
	csrfless, err := http.NewRequest(http.MethodPost, h.server.URL+"/checkout",
		strings.NewReader("csrf=&product_id="+cryptoutil.Itoa(int(product.ID))))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	csrfless.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	csrfResp, err := h.client.Do(csrfless)
	if err != nil {
		t.Fatalf("csrf request: %v", err)
	}
	csrfResp.Body.Close()
	if csrfResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected CSRF rejection, got %d", csrfResp.StatusCode)
	}

	// Create an order as the shop visitor.
	checkout := h.postForm("/checkout", url.Values{
		"product_id": {cryptoutil.Itoa(int(product.ID))},
		"quantity":   {"1"},
		"email":      {"buyer@example.com"},
		"driver":     {"manual"},
		"agree":      {"1"},
	})
	if checkout.StatusCode != http.StatusSeeOther {
		t.Fatalf("checkout failed: %d", checkout.StatusCode)
	}
	location := checkout.Header.Get("Location")
	if !strings.HasPrefix(location, "/order/") || !strings.Contains(location, "token=") {
		t.Fatalf("unexpected redirect %q", location)
	}
	orderPath := location
	parts := strings.SplitN(strings.TrimPrefix(orderPath, "/order/"), "?", 2)
	orderNo := parts[0]
	token := strings.TrimPrefix(parts[1], "token=")

	// The order page hides keys until payment.
	_, body = h.get("/order/" + orderNo + "?token=" + token)
	if strings.Contains(body, "E2E-KEY") {
		t.Fatal("cards leaked before payment")
	}
	if !strings.Contains(body, "去支付") {
		t.Fatal("pending order should offer payment")
	}

	// Operator confirms the manual payment.
	fulfill := h.postForm("/admin/orders/"+orderNo+"/fulfill", url.Values{})
	if fulfill.StatusCode != http.StatusSeeOther {
		t.Fatalf("fulfill failed: %d", fulfill.StatusCode)
	}
	order, err := h.db.OrderByNo(ctx, orderNo)
	if err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if order.Status != store.OrderDelivered {
		t.Fatalf("expected delivered, got %s", order.Status)
	}

	// Buyer sees the delivered key.
	_, body = h.get("/order/" + orderNo + "?token=" + token)
	if !strings.Contains(body, "E2E-KEY") {
		t.Fatal("delivered cards are missing from the order page")
	}

	// A fresh browser (no order cookie) presenting a wrong token must be refused.
	anonJar, _ := cookiejar.New(nil)
	anon := &http.Client{
		Jar: anonJar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	anonResp, err := anon.Get(h.server.URL + "/order/" + orderNo + "?token=wrong")
	if err != nil {
		t.Fatalf("anonymous order request: %v", err)
	}
	anonResp.Body.Close()
	if anonResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for wrong token, got %d", anonResp.StatusCode)
	}

	// Order status API.
	resp, body = h.get("/order/" + orderNo + "/status?token=" + token)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "delivered") {
		t.Fatalf("status endpoint failed: %d %s", resp.StatusCode, body)
	}

	// Health endpoints.
	if resp, _ := h.get("/healthz"); resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz %d", resp.StatusCode)
	}
	if resp, _ := h.get("/readyz"); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz %d", resp.StatusCode)
	}

	// Security headers present.
	resp, _ = h.get("/")
	header := resp.Header
	for _, name := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if header.Get(name) == "" {
			t.Fatalf("missing security header %s", name)
		}
	}
	if header.Get("X-Request-Id") == "" {
		t.Fatal("missing request id header")
	}
}

func TestAPITokensAndScopes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.seedProduct(t)

	// Unauthenticated API access is refused.
	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/v1/products", nil)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("api call: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	token, plain, err := h.db.CreateAPIToken(ctx, store.TokenInput{Name: "test", Scopes: []string{"products:read"}})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if token.Prefix == "" || plain == "" {
		t.Fatal("token material missing")
	}

	req, _ = http.NewRequest(http.MethodGet, h.server.URL+"/api/v1/products", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	resp, err = h.client.Do(req)
	if err != nil {
		t.Fatalf("api call: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "e2e-product") {
		t.Fatalf("api products failed: %d %s", resp.StatusCode, body)
	}

	// Scope enforcement.
	req, _ = http.NewRequest(http.MethodPost, h.server.URL+"/api/v1/cards", strings.NewReader(`{"product_id":1,"secrets":["A"]}`))
	req.Header.Set("Authorization", "Bearer "+plain)
	req.Header.Set("Content-Type", "application/json")
	resp, err = h.client.Do(req)
	if err != nil {
		t.Fatalf("api call: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for missing scope, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}

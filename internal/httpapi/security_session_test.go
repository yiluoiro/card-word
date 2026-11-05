package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

func TestAdminSessionAbsoluteExpiryAndDisabledUser(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u, err := h.db.CreateUser(ctx, "session_admin", "session-passphrase-123", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(10 * time.Minute)
	sess := &store.Session{ID: "session-regression", UserID: u.ID, TokenHash: cryptoutil.TokenHash("test-session-token"), CSRFToken: "csrf-session-regression-12345678901234567890", CreatedAt: now.Add(-30 * time.Minute), LastSeenAt: now, ExpiresAt: expires}
	if err = h.db.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(h.server.URL)
	h.client.Jar.SetCookies(base, []*http.Cookie{{Name: "cw_sess", Value: "test-session-token", Path: "/"}})
	resp, _ := h.get("/admin")
	if resp.StatusCode != 200 {
		t.Fatalf("session failed: %d", resp.StatusCode)
	}
	stored, err := h.db.SessionByTokenHash(ctx, sess.TokenHash)
	if err != nil || !stored.ExpiresAt.Equal(expires) {
		t.Fatal("absolute expiry was extended")
	}
	if err = h.db.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	resp, _ = h.get("/admin")
	if resp.StatusCode != 303 {
		t.Fatalf("disabled user retained access: %d", resp.StatusCode)
	}
}

func TestOrderCredentialHostCookiePath(t *testing.T) {
	h := newHarness(t)
	p := h.seedProduct(t)
	result, err := h.db.CreateOrder(context.Background(), store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	// HTTPS cookie policy is selected by the deployment's public base URL.
	tlsServer := httptest.NewTLSServer(h.app.Handler())
	defer tlsServer.Close()
	h.server = tlsServer
	h.client.Transport = tlsServer.Client().Transport
	h.cfg.BaseURL = tlsServer.URL
	vals := h.solved("query", url.Values{"order_no": {result.Order.OrderNo}, "token": {result.QueryToken}})
	vals.Set("csrf", h.csrf())

	resp, err := h.client.PostForm(h.server.URL+"/query", vals)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-cw_ord_"+cryptoutil.TokenHash(result.Order.OrderNo)[:16] {
			found = true
			if c.Path != "/" || !c.Secure || !c.HttpOnly {
				t.Fatalf("invalid Host cookie: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("order credential cookie missing")
	}
}

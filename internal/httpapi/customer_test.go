package httpapi_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func registerBuyer(t *testing.T, h *harness, name string) {
	t.Helper()
	h.get("/account/register")
	resp := h.postForm("/account/register", url.Values{"username": {name}, "password": {"buyer-passphrase-123"}, "confirm_password": {"buyer-passphrase-123"}})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/account" {
		t.Fatalf("registration: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	h.get("/account") // receive session-bound CSRF
}

func TestCustomerLifecycleAndOrderIsolation(t *testing.T) {
	h := newHarness(t)
	product := h.seedProduct(t)
	registerBuyer(t, h, "buyer_one")
	resp, body := h.get("/account")
	if resp.StatusCode != 200 || !strings.Contains(body, "buyer_one") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("account not rendered safely")
	}
	resp, _ = h.get("/admin")
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/admin/login" {
		t.Fatal("buyer obtained administrator access")
	}
	resp = h.postForm("/checkout", url.Values{"product_id": {strconv.FormatInt(product.ID, 10)}, "quantity": {"1"}, "driver": {"manual"}, "agree": {"1"}, "idem": {"same-client-key"}, "email": {"shared@example.com"}})
	location := resp.Header.Get("Location")
	if !strings.HasPrefix(location, "/order/") {
		t.Fatalf("checkout: %s", location)
	}
	orderPath := strings.SplitN(location, "?", 2)[0]
	resp, _ = h.get(orderPath)
	if resp.StatusCode != 200 {
		t.Fatal("owner cannot access order without token")
	}
	resp, _ = h.get(orderPath + "/status")
	if resp.StatusCode != 200 {
		t.Fatal("owner cannot poll order")
	}
	_, body = h.get("/account")
	if !strings.Contains(body, strings.TrimPrefix(orderPath, "/order/")) {
		t.Fatal("order absent in account")
	}
	// Preserve the first account's session; a second customer sharing email and
	// client-provided idempotency key must neither own nor replay its order.
	originalJar := h.client.Jar
	h.client.Jar, _ = cookiejar.New(nil)
	registerBuyer(t, h, "buyer_two")
	resp, _ = h.get(orderPath)
	if resp.StatusCode != 403 {
		t.Fatalf("cross-account order read: %d", resp.StatusCode)
	}
	resp, _ = h.get(orderPath + "/status")
	if resp.StatusCode != 403 {
		t.Fatal("cross-account status exposed")
	}
	resp = h.postForm("/checkout", url.Values{"product_id": {strconv.FormatInt(product.ID, 10)}, "quantity": {"1"}, "driver": {"manual"}, "agree": {"1"}, "idem": {"same-client-key"}, "email": {"shared@example.com"}})
	if resp.Header.Get("Location") == location || !strings.HasPrefix(resp.Header.Get("Location"), "/order/") {
		t.Fatal("cross-account idempotency collision")
	}
	_, body = h.get("/account")
	if strings.Contains(body, strings.TrimPrefix(orderPath, "/order/")) {
		t.Fatal("other customer's order in list")
	}
	h.client.Jar = originalJar
	h.get("/account")
	resp = h.postForm("/account/password", url.Values{"current_password": {"buyer-passphrase-123"}, "new_password": {"new-buyer-passphrase-456"}, "confirm_password": {"new-buyer-passphrase-456"}})
	if resp.StatusCode != 303 {
		t.Fatalf("password change: %d", resp.StatusCode)
	}
	resp, _ = h.get("/account")
	if resp.StatusCode != 303 {
		t.Fatal("password change retained session")
	}
	h.get("/account/login")
	resp = h.postForm("/account/login", url.Values{"username": {"buyer_one"}, "password": {"buyer-passphrase-123"}})
	if resp.StatusCode != 401 {
		t.Fatal("old password still valid")
	}
	resp = h.postForm("/account/login", url.Values{"username": {"BUYER_ONE"}, "password": {"new-buyer-passphrase-456"}})
	if resp.StatusCode != 303 {
		t.Fatal("new password login failed")
	}
	h.get("/account")
	resp = h.postForm("/account/logout", nil)
	if resp.StatusCode != 303 {
		t.Fatal("logout failed")
	}
	resp, _ = h.get("/account")
	if resp.StatusCode != 303 {
		t.Fatal("logout retained access")
	}
	users, err := h.db.CountUsers(context.Background())
	if err != nil || users != 0 {
		t.Fatal("customers polluted administrator table")
	}
}

func TestCustomerCSRFAndValidation(t *testing.T) {
	h := newHarness(t)
	h.get("/account/register")
	resp := h.postForm("/account/register", url.Values{"csrf": {"invalid"}, "username": {"buyer_test"}, "password": {"buyer-passphrase-123"}, "confirm_password": {"buyer-passphrase-123"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal("registration bypassed CSRF")
	}
	resp = h.postForm("/account/register", url.Values{"username": {"buyer_test"}, "password": {"short"}, "confirm_password": {"short"}})
	if resp.StatusCode != 400 {
		t.Fatal("short password accepted")
	}
	registerBuyer(t, h, "buyer_test")
	resp = h.postForm("/account/password", url.Values{"csrf": {"invalid"}, "current_password": {"buyer-passphrase-123"}, "new_password": {"new-buyer-passphrase-456"}, "confirm_password": {"new-buyer-passphrase-456"}})
	if resp.StatusCode != 403 {
		t.Fatal("password change bypassed CSRF")
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/config"
	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

func securityServer(t *testing.T) *Server {
	t.Helper()
	key := make([]byte, 32)
	db, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "security.db"), MasterKey: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tmpl, err := Templates()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Deps{DB: db, Templates: tmpl, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Config: &config.Config{
		BaseURL: "http://example.com", MasterKey: key, SessionTTL: time.Hour, SessionIdle: time.Hour,
		MaxBodyBytes: 1 << 20, RateLimitRPS: 10000, RateLimitBurst: 10000, OrderTTL: time.Hour,
	}})
	if err != nil {
		t.Fatal(err)
	}
	s.reloadIntegrations(context.Background())
	return s
}

func securityUser(t *testing.T, s *Server, role string) *store.User {
	t.Helper()
	u, err := s.db.CreateUser(context.Background(), role, "test-password-12345", role)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func securityRequest(u *store.User, method, path string, form url.Values) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if u != nil {
		r = r.WithContext(context.WithValue(r.Context(), ctxUser, u))
	}
	return r
}

func securitySession(t *testing.T, s *Server, u *store.User) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	if err := s.startSession(w, httptest.NewRequest("GET", "/", nil), u); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == s.cookieName(cookieSession) {
			return c
		}
	}
	t.Fatal("missing session cookie")
	return nil
}

func TestSecurityTOTPStartCannotDisableEnabledFactor(t *testing.T) {
	s := securityServer(t)
	u := securityUser(t, s, store.RoleAdmin) // deliberately stale ctxUser snapshot
	secret, err := cryptoutil.Seal(s.cfg.MasterKey, []byte("JBSWY3DPEHPK3PXP"), totpAAD(u.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetTOTP(context.Background(), u.ID, secret, true); err != nil {
		t.Fatal(err)
	}
	if err := s.db.ReplaceRecoveryCodes(context.Background(), u.ID, []string{"ABCDE-FGHIJ"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			s.handleTOTPStart(w, securityRequest(u, "POST", "/admin/account/2fa/start", nil))
			if w.Code != http.StatusConflict {
				t.Errorf("start status %d", w.Code)
			}
		}()
	}
	wg.Wait()
	w := httptest.NewRecorder()
	s.handleTOTPEnable(w, securityRequest(u, "POST", "/admin/account/2fa/enable", url.Values{"code": {"123456"}}))
	if w.Code != http.StatusConflict {
		t.Fatalf("repeat enable status %d", w.Code)
	}
	got, err := s.db.UserByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TOTPEnabled || got.TOTPSecret != secret {
		t.Fatal("existing factor overwritten or disabled")
	}
	n, err := s.db.CountRecoveryCodes(context.Background(), u.ID)
	if err != nil || n != 1 {
		t.Fatalf("recovery codes changed: %d %v", n, err)
	}
}

func TestSecurityTOTPSetupAndDisable(t *testing.T) {
	s := securityServer(t)
	u := securityUser(t, s, store.RoleAdmin)
	w := httptest.NewRecorder()
	s.handleTOTPEnable(w, securityRequest(u, "POST", "/admin/account/2fa/enable", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("enable without start: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.handleTOTPStart(w, securityRequest(u, "POST", "/admin/account/2fa/start", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("initial setup: %d", w.Code)
	}
	fresh, err := s.db.UserByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.TOTPEnabled || fresh.TOTPSecret == "" {
		t.Fatal("incorrect pending setup state")
	}
	if err := s.db.SetTOTP(context.Background(), u.ID, fresh.TOTPSecret, true); err != nil {
		t.Fatal(err)
	}
	if err := s.db.ReplaceRecoveryCodes(context.Background(), u.ID, []string{"ABCDE-FGHIJ"}); err != nil {
		t.Fatal(err)
	}
	for _, form := range []url.Values{
		{"password": {"wrong"}, "code": {"ABCDE-FGHIJ"}},
		{"password": {"test-password-12345"}, "code": {"invalid"}},
	} {
		w = httptest.NewRecorder()
		s.handleTOTPDisable(w, securityRequest(u, "POST", "/admin/account/2fa/disable", form))
		fresh, err = s.db.UserByID(context.Background(), u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !fresh.TOTPEnabled {
			t.Fatal("disabled without password and factor")
		}
	}
	w = httptest.NewRecorder()
	s.handleTOTPDisable(w, securityRequest(u, "POST", "/admin/account/2fa/disable", url.Values{"password": {"test-password-12345"}, "code": {"ABCDE-FGHIJ"}}))
	fresh, err = s.db.UserByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.TOTPEnabled || fresh.TOTPSecret != "" {
		t.Fatal("verified disable failed")
	}
}

func TestSecurityTwoFactorHostCookie(t *testing.T) {
	s := securityServer(t)
	s.cfg.BaseURL = "https://example.com"
	for _, clear := range []bool{false, true} {
		w := httptest.NewRecorder()
		if clear {
			s.clearTwoFactorCookie(w)
		} else {
			s.setTwoFactorCookie(w, "test")
		}
		cs := w.Result().Cookies()
		if len(cs) != 1 {
			t.Fatal("missing cookie")
		}
		c := cs[0]
		if c.Name != "__Host-cw_2fa" || c.Path != "/" || !c.Secure || !c.HttpOnly || c.Domain != "" {
			t.Fatalf("invalid host cookie: %+v", c)
		}
	}
}

func TestSecurityDisabledAndCustomerCannotEnterAdmin(t *testing.T) {
	s := securityServer(t)
	u := securityUser(t, s, store.RoleViewer)
	cookie := securitySession(t, s, u)
	if err := s.db.SetUserDisabled(context.Background(), u.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin", "/admin/categories", "/admin/account"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login" {
			t.Fatalf("disabled account reached %s: %d", path, w.Code)
		}
	}
	// Customer cookies must never be treated as an operator session or ctxUser.
	r := httptest.NewRequest("GET", "/admin", nil)
	r.AddCookie(&http.Cookie{Name: "cw_customer", Value: cookie.Value})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("customer reached admin: %d", w.Code)
	}
	for _, role := range []string{"customer", "unknown", ""} {
		r = securityRequest(&store.User{Role: role}, "GET", "/", nil)
		if s.currentUser(r) != nil {
			t.Fatalf("accepted non-operator role %q", role)
		}
	}
}

func TestSecurityViewerCannotMutateCategories(t *testing.T) {
	s := securityServer(t)
	u := securityUser(t, s, store.RoleViewer)
	cookie := securitySession(t, s, u)
	cat, err := s.db.CreateCategory(context.Background(), "existing", "existing", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.db.SessionByTokenHash(context.Background(), cryptoutil.TokenHash(cookie.Value))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/categories", fmt.Sprintf("/admin/categories/%d/delete", cat.ID)} {
		r := securityRequest(nil, "POST", path, url.Values{"name": {"unauthorized"}, "slug": {"unauthorized"}})
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", sess.CSRFToken)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("viewer mutation %s: %d", path, w.Code)
		}
	}
	cats, err := s.db.ListCategories(context.Background(), false)
	if err != nil || len(cats) != 1 {
		t.Fatalf("categories mutated: %v %v", cats, err)
	}
	admin := securityUser(t, s, store.RoleAdmin)
	w := httptest.NewRecorder()
	s.handleAdminCategorySave(w, securityRequest(admin, "POST", "/admin/categories", url.Values{"name": {"allowed"}, "slug": {"allowed"}}))
	cats, err = s.db.ListCategories(context.Background(), false)
	if err != nil || len(cats) != 2 {
		t.Fatalf("admin create failed: %v", err)
	}
}

func securityProduct(t *testing.T, s *Server) *store.Product {
	t.Helper()
	p, err := s.db.CreateProduct(context.Background(), store.ProductInput{Name: "test", Slug: "test", PriceCents: 100, Currency: "CNY", Status: store.ProductActive, MinQty: 1, MaxQty: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.AddCards(context.Background(), p.ID, []string{"security-card-secret"}, ""); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSecurityViewerOrderDoesNotRevealCards(t *testing.T) {
	s := securityServer(t)
	p := securityProduct(t, s)
	order, err := s.db.CreateOrder(context.Background(), store.CreateOrderInput{ProductID: p.ID, Quantity: 1, Email: "test@example.com", PaymentDriver: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.FulfillOrder(context.Background(), order.Order.OrderNo, "manual", "test", 100, ""); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{store.RoleViewer, store.RoleAdmin} {
		u := securityUser(t, s, role)
		r := httptest.NewRequest("GET", "/admin/orders/"+order.Order.OrderNo, nil)
		r.AddCookie(securitySession(t, s, u))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("order status %d", w.Code)
		}
		if strings.Contains(w.Body.String(), "security-card-secret") != (role == store.RoleAdmin) {
			t.Fatalf("wrong card visibility for %s", role)
		}
	}
}

func TestSecurityAPIIdempotencyIsTokenScoped(t *testing.T) {
	s := securityServer(t)
	p := securityProduct(t, s)
	for _, explicit := range []bool{false, true} {
		var first string
		for i := 0; i < 2; i++ {
			_, token, err := s.db.CreateAPIToken(context.Background(), store.TokenInput{Name: fmt.Sprintf("client-%v-%d", explicit, i), Scopes: []string{"orders:write"}})
			if err != nil {
				t.Fatal(err)
			}
			var previous string
			for retry := 0; retry < 2; retry++ {
				r := httptest.NewRequest("POST", "/api/v1/orders", strings.NewReader(fmt.Sprintf(`{"product_id":%d,"quantity":1,"email":"test@example.com"}`, p.ID)))
				r.Header.Set("Authorization", "Bearer "+token)
				if explicit {
					r.Header.Set("Idempotency-Key", "shared-key")
				}
				w := solveAPI(t, s.Handler(), s, r)
				if w.Code != http.StatusCreated {
					t.Fatalf("create: %d %s", w.Code, w.Body.String())
				}
				var result struct {
					Data apiCreateOrderResponse `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				no := result.Data.OrderNo
				if (result.Data.PayURL == "") != (retry > 0) {
					t.Fatal("pay URL must only be issued with the original credential")
				}
				if retry == 1 && no != previous {
					t.Fatal("same-token retry lost idempotency")
				}
				if i == 1 && no == first {
					t.Fatal("cross-token order disclosure via idempotency")
				}
				previous = no
			}
			if i == 0 {
				first = previous
			}
		}
	}
}

func TestSecurityTokenCreationDoesNotPutSecretInURL(t *testing.T) {
	s := securityServer(t)
	u := securityUser(t, s, store.RoleAdmin)
	w := httptest.NewRecorder()
	s.handleAdminTokenCreate(w, securityRequest(u, "POST", "/admin/tokens", url.Values{"name": {"test"}}))
	if w.Code != http.StatusOK || w.Header().Get("Location") != "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsafe token response: %d %v", w.Code, w.Header())
	}
	if !strings.Contains(w.Body.String(), "cwk_") {
		t.Fatal("new token not displayed")
	}
	w = httptest.NewRecorder()
	s.handleAdminTokens(w, securityRequest(u, "GET", "/admin/tokens?token=untrusted-secret", nil))
	if strings.Contains(w.Body.String(), "untrusted-secret") {
		t.Fatal("GET reflects credential query")
	}
}

func TestSecurityEnabledTOTPStillRequiresSecondFactor(t *testing.T) {
	s := securityServer(t)
	u := securityUser(t, s, store.RoleAdmin)
	secret, err := cryptoutil.Seal(s.cfg.MasterKey, []byte("JBSWY3DPEHPK3PXP"), totpAAD(u.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetTOTP(context.Background(), u.ID, secret, true); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleTOTPStart(w, securityRequest(u, "POST", "/admin/account/2fa/start", nil))
	capToken, code, err := s.issueCaptcha(securityRequest(nil, "POST", "/admin/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	r := securityRequest(nil, "POST", "/admin/login", url.Values{"username": {u.Username}, "password": {"test-password-12345"}, "captcha": {code}})
	r.AddCookie(&http.Cookie{Name: s.cookieName(captchaCookie), Value: capToken})
	w = httptest.NewRecorder()
	s.handleLoginSubmit(w, r)
	if w.Header().Get("Location") != "/admin/login/2fa" {
		t.Fatalf("password bypassed second factor: %d %v", w.Code, w.Header())
	}
	var challenge *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == s.cookieName(cookieSession) && c.Value != "" {
			t.Fatal("session issued before second factor")
		}
		if c.Name == s.cookieName(cookieTwoFactor) {
			challenge = c
		}
	}
	if challenge == nil {
		t.Fatal("no second-factor challenge")
	}
	if err := s.db.ReplaceRecoveryCodes(context.Background(), u.ID, []string{"ABCDE-FGHIJ"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.ClearTOTP(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	r = securityRequest(nil, "POST", "/admin/login/2fa", url.Values{"code": {"ABCDE-FGHIJ"}})
	r.AddCookie(challenge)
	w = httptest.NewRecorder()
	s.handleLoginTwoFactorSubmit(w, r)
	if w.Header().Get("Location") != "/admin/login" {
		t.Fatalf("obsolete challenge accepted: %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == s.cookieName(cookieSession) && c.Value != "" {
			t.Fatal("obsolete challenge issued session")
		}
	}
}

func TestSecurityManualConfirmationUsesOrderAmountAndUniqueTransaction(t *testing.T) {
	s := securityServer(t)
	u := securityUser(t, s, store.RoleAdmin)
	p := securityProduct(t, s)
	if _, err := s.db.AddCards(context.Background(), p.ID, []string{"second-secret"}, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		created, err := s.db.CreateOrder(context.Background(), store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual"})
		if err != nil {
			t.Fatal(err)
		}
		r := securityRequest(u, "POST", "/", nil)
		r.SetPathValue("no", created.Order.OrderNo)
		w := httptest.NewRecorder()
		s.handleAdminFulfill(w, r)
		order, err := s.db.OrderByNo(context.Background(), created.Order.OrderNo)
		if err != nil {
			t.Fatal(err)
		}
		if order.Status != store.OrderDelivered {
			t.Fatalf("confirmation failed: %s %v", order.Status, w.Header())
		}
		payments, err := s.db.ListPayments(context.Background(), order.ID)
		if err != nil || len(payments) != 1 || payments[0].AmountCents != order.TotalCents {
			t.Fatalf("wrong payment amount: %v %v", payments, err)
		}
	}
}

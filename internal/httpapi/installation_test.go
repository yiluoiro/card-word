package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

func TestInstallationGateAndPermanentLock(t *testing.T) {
	s := securityServer(t)
	s.cfg.DataDir = t.TempDir()
	h, err := s.WithInstallation(s.Handler())
	if err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(s.cfg.DataDir, "install-token.txt")
	b, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(b))
	for _, path := range []string{"/", "/admin", "/s/example/", "/api/v1/products"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 303 || w.Header().Get("Location") != "/install" {
			t.Fatalf("unguarded %s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/install?install_token=ignored", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), token) {
		t.Fatal("installation page leaked token or failed")
	}
	var csrf *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieCSRF {
			csrf = c
		}
	}
	if csrf == nil {
		t.Fatal("missing csrf")
	}
	post := func(tokenValue, password, confirm string, withCSRF bool) *httptest.ResponseRecorder {
		form := url.Values{"install_token": {tokenValue}, "site_name": {"测试店铺"}, "username": {"owner_test"}, "password": {password}, "confirm_password": {confirm}}
		if withCSRF {
			form.Set("csrf", csrf.Value)
		}
		r := httptest.NewRequest("POST", "/install", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	password := "long-install-password-123"
	if w = post(token, password, password, false); w.Code != 403 {
		t.Fatal("missing csrf accepted")
	}
	if w = post("wrong", password, password, true); w.Code != 403 {
		t.Fatal("wrong token accepted")
	}
	if w = post(token, password, "different", true); w.Code != 422 {
		t.Fatal("mismatched password accepted")
	}
	if n, _ := s.db.CountUsers(context.Background()); n != 0 {
		t.Fatal("invalid installation wrote account")
	}
	if w = post(token, password, password, true); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/admin/login") {
		t.Fatalf("install failed %d %s", w.Code, w.Body.String())
	}
	u, err := s.db.UserByUsername(context.Background(), "owner_test")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := cryptoutil.VerifyPassword(u.PasswordHash, password); !ok || u.Role != store.RoleOwner {
		t.Fatal("owner not created correctly")
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatal("installation token not removed")
	}
	if w = post(token, password, password, true); w.Code != 404 {
		t.Fatal("reinstall accepted")
	}
	if n, _ := s.db.CountProducts(context.Background(), store.ProductFilter{}); n != 0 {
		t.Fatal("unexpected demo products")
	}
	h, err = s.WithInstallation(s.Handler())
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/install", nil))
	if w.Code != 404 {
		t.Fatal("restart reopened installer")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 200 {
		t.Fatal("installed service not ready")
	}
}

func TestInstallationDoesNotOpenForExistingAdmin(t *testing.T) {
	s := securityServer(t)
	s.cfg.DataDir = t.TempDir()
	securityUser(t, s, store.RoleAdmin)
	h, err := s.WithInstallation(s.Handler())
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/install", nil))
	if w.Code != 404 {
		t.Fatal("existing site exposed installer")
	}
	if _, err := os.Stat(filepath.Join(s.cfg.DataDir, "install-token.txt")); !os.IsNotExist(err) {
		t.Fatal("token created for existing site")
	}
}

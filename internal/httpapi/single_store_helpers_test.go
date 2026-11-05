package httpapi

import (
	"context"

	"net/http"
	"net/http/httptest"
	"net/url"

	"strings"

	"testing"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

func singleStoreServer(t *testing.T) *Server {
	t.Helper()
	s := securityServer(t)
	s.cfg.DataDir = t.TempDir()
	return s
}
func storeRequest(s *Server, method, path string, form url.Values, cookie *http.Cookie, csrf string, extra ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for _, c := range extra {
		if c != nil {
			r.AddCookie(c)
		}
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func storeAdmin(t *testing.T, s *Server, role string) (*http.Cookie, string) {
	t.Helper()
	user := securityUser(t, s, role)
	cookie := securitySession(t, s, user)
	session, err := s.db.SessionByTokenHash(context.Background(), cryptoutil.TokenHash(cookie.Value))
	if err != nil {
		t.Fatal(err)
	}
	return cookie, session.CSRFToken
}
func storeBuyer(t *testing.T, s *Server, name string) (*store.Customer, *http.Cookie, string) {
	t.Helper()
	c, err := s.db.CreateCustomer(context.Background(), name, "applicant-password-123")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := s.startCustomerSession(w, httptest.NewRequest("GET", "/", nil), c); err != nil {
		t.Fatal(err)
	}
	cookie := w.Result().Cookies()[0]
	csrf := cryptoutil.HMACSHA256Hex(s.cfg.MasterKey, "customer-csrf:"+cryptoutil.TokenHash(cookie.Value))
	return c, cookie, csrf
}

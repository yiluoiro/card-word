package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func captchaRequest(token string) *http.Request {
	r := httptest.NewRequest("POST", "/admin/login", nil)
	r.Header.Set("User-Agent", "captcha-test-browser")
	if token != "" {
		r.AddCookie(&http.Cookie{Name: captchaCookie, Value: token})
	}
	return r
}
func TestCaptchaSingleUseAndBinding(t *testing.T) {
	for _, mode := range []string{"valid", "wrong", "empty", "expired", "ip", "agent", "session", "refresh", "other-shop", "tampered"} {
		t.Run(mode, func(t *testing.T) {
			s := securityServer(t)
			token, code, err := s.issueCaptcha(captchaRequest(""))
			if err != nil {
				t.Fatal(err)
			}
			r := captchaRequest(token)
			answer := code
			switch mode {
			case "wrong":
				answer = "!!!!!!"
			case "empty":
				answer = ""
			case "expired":
				c := s.captchas.entries[cryptoutil.TokenHash(token)]
				c.expires = time.Now()
				s.captchas.entries[cryptoutil.TokenHash(token)] = c
			case "ip":
				r.RemoteAddr = "192.0.2.7:1234"
			case "agent":
				r.Header.Set("User-Agent", "another-agent")
			case "session":
				r = r.WithContext(context.WithValue(r.Context(), ctxCSRF, "different-browser-session"))
			case "refresh":
				if _, _, err := s.issueCaptcha(r); err != nil {
					t.Fatal(err)
				}
			case "other-shop":
				if securityServer(t).verifyCaptcha(r, code) {
					t.Fatal("cross-shop acceptance")
				}
				return
			case "tampered":
				r = captchaRequest(strings.Repeat("x", 43))
			}
			if got := s.verifyCaptcha(r, answer); got != (mode == "valid") {
				t.Fatalf("acceptance: %v", got)
			}
			if mode != "tampered" && s.verifyCaptcha(captchaRequest(token), code) {
				t.Fatal("replayed consumed challenge")
			}
		})
	}
}
func TestCaptchaConcurrentConsumption(t *testing.T) {
	s := securityServer(t)
	token, code, err := s.issueCaptcha(captchaRequest(""))
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.verifyCaptcha(captchaRequest(token), code) {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d times", accepted.Load())
	}
}
func TestCaptchaOpaqueRasterAndBoundedStore(t *testing.T) {
	s := securityServer(t)
	token, code, err := s.issueCaptcha(captchaRequest(""))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || bytes.Contains(raw, []byte(code)) {
		t.Fatal("token must be opaque random bytes")
	}
	if s.captchas.entries[cryptoutil.TokenHash(token)].answer == code {
		t.Fatal("plaintext stored")
	}
	for _, ch := range []byte(captchaAlphabet) {
		if _, ok := captchaGlyphs[ch]; !ok {
			t.Fatalf("missing glyph %c", ch)
		}
	}
	src, err := captchaPNG(code)
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(src, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil || img.Bounds().Dx() != 240 || img.Bounds().Dy() != 76 {
		t.Fatal("invalid raster image")
	}
	other, _ := captchaPNG(code)
	if src == other {
		t.Fatal("image variation missing")
	}
	for len(s.captchas.entries) < captchaCapacity {
		s.captchas.entries[cryptoutil.TokenHash(string(rune(len(s.captchas.entries))))] = captchaChallenge{expires: time.Now().Add(time.Minute)}
	}
	if _, _, err := s.issueCaptcha(captchaRequest("")); err == nil {
		t.Fatal("unbounded challenge store")
	}
	for k, c := range s.captchas.entries {
		c.expires = time.Now().Add(-time.Second)
		s.captchas.entries[k] = c
	}
	if _, _, err := s.issueCaptcha(captchaRequest("")); err != nil || len(s.captchas.entries) != 1 {
		t.Fatal("expired state was not reclaimed")
	}
}
func TestCaptchaRefreshRateLimitAndNoCache(t *testing.T) {
	s := securityServer(t)
	for i := 0; i < 9; i++ {
		w := httptest.NewRecorder()
		s.handleCaptchaRefresh(w, captchaRequest(""))
		want := 200
		if i == 8 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("request %d status %d", i, w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("captcha can be cached")
		}
		if i == 8 && w.Header().Get("Retry-After") == "" {
			t.Fatal("missing retry guidance")
		}
	}
}
func TestCaptchaRefreshRequiresCSRF(t *testing.T) {
	s := securityServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, captchaRequest(""))
	// Test the refresh route separately, with no session or double-submit token.
	r := httptest.NewRequest("POST", "http://example.com/admin/captcha", nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("refresh bypassed CSRF: %d", w.Code)
	}
}

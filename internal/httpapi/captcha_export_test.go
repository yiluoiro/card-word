package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
)

// CaptchaAnswerForTest is absent from production builds; no HTTP bypass exists.
func CaptchaAnswerForTest(s *Server, token string) string {
	s.captchas.mu.Lock()
	defer s.captchas.mu.Unlock()
	key := cryptoutil.TokenHash(token)
	c, ok := s.captchas.entries[key]
	if !ok {
		return ""
	}
	c.answer = s.captchaDigest(token, "ABC234")
	s.captchas.entries[key] = c
	return "ABC234"
}

func solvePage(t *testing.T, s *Server, html string, form url.Values) url.Values {
	t.Helper()
	m := regexp.MustCompile(`name="captcha_id" value="([^"]+)"`).FindStringSubmatch(html)
	if len(m) != 2 {
		t.Fatal("missing rendered challenge")
	}
	form.Set("captcha_id", m[1])
	form.Set("captcha", CaptchaAnswerForTest(s, m[1]))
	return form
}

// Retry the exact authenticated API request after the challenge response.
func solveAPI(t *testing.T, handler http.Handler, s *Server, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 428 {
		return w
	}
	var data struct{ Challenge formChallenge }
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	next := httptest.NewRequest(r.Method, r.RequestURI, bytes.NewReader(body))
	next.Header = r.Header.Clone()
	next.Header.Set("X-Captcha-ID", data.Challenge.ID)
	next.Header.Set("X-Captcha-Answer", CaptchaAnswerForTest(s, data.Challenge.ID))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, next)
	return w
}

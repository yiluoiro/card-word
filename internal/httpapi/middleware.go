package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

type ctxKey string

const (
	ctxRequestID ctxKey = "rid"
	ctxUser      ctxKey = "user"
	ctxSession   ctxKey = "session"
	ctxCSRF      ctxKey = "csrf"
)

const (
	cookieSession = "cw_sess"
	cookieCSRF    = "cw_csrf"
)

// cookieName applies the __Host- prefix on HTTPS deployments. That prefix is
// browser enforced: the cookie must be Secure, Path=/ and carry no Domain, so
// a sibling subdomain cannot overwrite the admin session cookie.
func (s *Server) cookieName(base string) string {
	if s.cfg.Secure() {
		return "__Host-" + base
	}
	return base
}

func requestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxRequestID).(string); ok {
		return v
	}
	return ""
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			if _, err := rand.Read(b); err == nil {
				id = hex.EncodeToString(b)
			} else {
				id = "unknown"
			}
		}
		ctx := context.WithValue(r.Context(), ctxRequestID, id)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic while serving request",
					"rid", requestIDFrom(r.Context()),
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		s.log.Info("request",
			"rid", requestIDFrom(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"bytes", sw.bytes,
			"ip", s.clientIP(r),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	csp := strings.Join([]string{
		"default-src 'self'",
		"img-src 'self' data:",
		"style-src 'self' 'unsafe-inline'",
		"script-src 'self'",
		"connect-src 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"object-src 'none'",
	}, "; ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("X-Permitted-Cross-Domain-Policies", "none")
		if s.cfg.Secure() {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)
		if !s.limiter.allow(ip) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var sess *store.Session
		if c, err := r.Cookie(s.cookieName(cookieSession)); err == nil && c.Value != "" {
			stored, err := s.db.SessionByTokenHash(ctx, cryptoutil.TokenHash(c.Value))
			switch {
			case err == nil:
				now := time.Now().UTC()
				if now.Before(stored.ExpiresAt) && now.Sub(stored.LastSeenAt) < s.cfg.SessionIdle {
					sess = stored
					expires := now.Add(s.cfg.SessionTTL)
					_ = s.db.TouchSession(ctx, stored.ID, now, expires)
					s.setSessionCookies(w, c.Value, stored.CSRFToken, expires)
					user, err := s.db.UserByID(ctx, stored.UserID)
					if err == nil {
						ctx = context.WithValue(ctx, ctxUser, user)
					}
				} else {
					_ = s.db.DeleteSession(ctx, stored.ID)
					s.clearSessionCookies(w)
				}
			case errors.Is(err, store.ErrNotFound):
				s.clearSessionCookies(w)
			}
		}
		if sess != nil {
			ctx = context.WithValue(ctx, ctxSession, sess)
			ctx = context.WithValue(ctx, ctxCSRF, sess.CSRFToken)
		} else {
			// Guests get a double submit token so plain forms stay CSRF safe.
			token := ""
			if c, err := r.Cookie(s.cookieName(cookieCSRF)); err == nil {
				token = c.Value
			}
			if token == "" || len(token) < 32 {
				token, _ = cryptoutil.RandomToken(32)
				s.setCSRFCookie(w, token)
			}
			ctx = context.WithValue(ctx, ctxCSRF, token)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// csrfProtect enforces double submit tokens plus origin checks on mutations.
func (s *Server) csrfProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		// Provider and plugin callbacks authenticate with their own signatures.
		if strings.HasPrefix(r.URL.Path, "/pay/notify/") || strings.HasPrefix(r.URL.Path, "/api/plugins/") {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		if !s.originAllowed(r) {
			s.log.Warn("cross-site request rejected",
				"rid", requestIDFrom(r.Context()),
				"path", r.URL.Path,
				"origin", r.Header.Get("Origin"),
				"host", r.Host,
				"sec_fetch_site", r.Header.Get("Sec-Fetch-Site"))
			http.Error(w, "cross-site request rejected", http.StatusForbidden)
			return
		}
		want, _ := r.Context().Value(ctxCSRF).(string)
		got := r.Header.Get("X-CSRF-Token")
		if got == "" {
			// Reading the body here is safe: MaxBytesReader and ParseMultipartForm
			// are already in place for form submissions.
			if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
				if err2 := r.ParseForm(); err2 != nil {
					http.Error(w, "malformed form body", http.StatusBadRequest)
					return
				}
			}
			got = r.FormValue("csrf")
		}
		if want == "" || got == "" || !constantTimeEqual(want, got) {
			s.log.Warn("csrf validation failed", "rid", requestIDFrom(r.Context()), "path", r.URL.Path)
			http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func (s *Server) originAllowed(r *http.Request) bool {
	// Prefer the fetch metadata hint when the browser provides it.
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // same-origin navigations and older browsers
	}
	// Sandboxed frames and embedded previews send "null"; the CSRF token check
	// that follows is still mandatory, so this is not a bypass.
	if origin == "null" {
		s.log.Debug("request with opaque origin", "path", r.URL.Path, "host", r.Host)
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	// Same-origin means the Origin host matches the host the request actually
	// arrived on (browsers compare scheme/host/port, proxies preserve Host).
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	// Also accept the explicitly configured public base URL, which is what
	// reverse proxies forward and what payment providers call back on.
	if base, err := url.Parse(s.cfg.BaseURL); err == nil && base.Host != "" {
		if strings.EqualFold(u.Host, base.Host) {
			return true
		}
	}
	return false
}

func (s *Server) setSessionCookies(w http.ResponseWriter, sessionToken, csrf string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(cookieSession),
		Value:    sessionToken,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.cfg.Secure(),
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(cookieCSRF),
		Value:    csrf,
		Path:     "/",
		Expires:  expires,
		HttpOnly: false,
		Secure:   s.cfg.Secure(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) setCSRFCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(cookieCSRF),
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(24 * time.Hour),
		HttpOnly: false,
		Secure:   s.cfg.Secure(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{s.cookieName(cookieSession), s.cookieName(cookieCSRF)} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == cookieSession,
			Secure:   s.cfg.Secure(),
			SameSite: http.SameSiteLaxMode,
		})
	}
}

// ---------- rate limiter ----------

type bucket struct {
	tokens float64
	last   time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
	lastGC  time.Time
}

func newRateLimiter(rate float64, burst int) *rateLimiter {
	if rate <= 0 {
		rate = 20
	}
	if burst <= 0 {
		burst = 60
	}
	return &rateLimiter{
		buckets: make(map[string]*bucket),
		rate:    rate,
		burst:   float64(burst),
		lastGC:  time.Now(),
	}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastGC) > 5*time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.lastGC = now
	}
	b, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

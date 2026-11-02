package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

func (s *Server) currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxUser).(*store.User)
	return u
}

// admin wraps a handler so only authenticated operators reach it.
func (s *Server) admin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := s.currentUser(r)
		if user == nil {
			if r.Header.Get("Accept") == "application/json" {
				writeAPIError(w, http.StatusUnauthorized, "unauthorized", "login required")
				return
			}
			redirect(w, r, "/admin/login")
			return
		}
		next(w, r)
	})
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.currentUser(r) != nil {
		redirect(w, r, "/admin")
		return
	}
	s.render(w, r, "admin_login", map[string]any{"Title": "管理员登录"})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.authLimit.allow(ip) {
		s.renderError(w, r, http.StatusTooManyRequests, "登录尝试过于频繁，请稍后再试。")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == "" || password == "" {
		s.renderError(w, r, http.StatusBadRequest, "请输入用户名和密码。")
		return
	}
	if wait, locked := s.guard.locked(strings.ToLower(username)); locked {
		s.log.Warn("login rejected by account lock", "username", username, "ip", ip, "retry_after", wait.String())
		s.renderError(w, r, http.StatusTooManyRequests, "该账号连续登录失败次数过多，请 15 分钟后再试。")
		return
	}

	user, err := s.db.UserByUsername(r.Context(), username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("login lookup failed", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "登录失败。")
		return
	}
	// Always spend the same amount of work, whether the account exists or not.
	var ok bool
	if user == nil {
		_, _ = cryptoutil.VerifyPassword(s.dummyPasswordHash(), password)
	} else {
		ok, err = cryptoutil.VerifyPassword(user.PasswordHash, password)
		if err != nil {
			s.log.Error("password verification failed", "error", err)
		}
	}
	if user == nil || !ok {
		s.guard.fail(strings.ToLower(username))
		_ = s.db.Audit(r.Context(), username, "auth.login.failed", "", ip, "invalid credentials")
		s.log.Warn("failed admin login", "username", username, "ip", ip)
		s.renderError(w, r, http.StatusUnauthorized, "用户名或密码错误。")
		return
	}
	if user.Disabled {
		s.audit(r, "auth.login.disabled", user.Username, "")
		s.renderError(w, r, http.StatusForbidden, "该账号已被禁用，请联系站点所有者。")
		return
	}
	if user.TOTPEnabled {
		_, challengeToken, err := s.db.CreateChallenge(r.Context(), user.ID)
		if err != nil {
			s.log.Error("failed to create login challenge", "error", err)
			s.renderError(w, r, http.StatusInternalServerError, "登录流程初始化失败。")
			return
		}
		s.setTwoFactorCookie(w, challengeToken)
		s.audit(r, "auth.2fa.challenge", user.Username, "password verified, waiting for TOTP")
		redirect(w, r, "/admin/login/2fa")
		return
	}

	token, err := cryptoutil.RandomToken(32)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "会话创建失败。")
		return
	}
	sessionID, err := cryptoutil.RandomToken(16)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "会话创建失败。")
		return
	}
	csrfToken, err := cryptoutil.RandomToken(32)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "会话创建失败。")
		return
	}
	now := time.Now().UTC()
	expires := now.Add(s.cfg.SessionTTL)
	sess := &store.Session{
		ID:         sessionID,
		UserID:     user.ID,
		TokenHash:  cryptoutil.TokenHash(token),
		CSRFToken:  csrfToken,
		IP:         ip,
		UserAgent:  truncateUA(r.UserAgent()),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  expires,
	}
	// Rotate: drop previously issued sessions of this account so a stolen old
	// cookie cannot outlive a fresh login.
	if err := s.db.DeleteUserSessions(r.Context(), user.ID, ""); err != nil {
		s.log.Warn("failed to rotate sessions", "error", err)
	}
	if err := s.db.CreateSession(r.Context(), sess); err != nil {
		s.log.Error("failed to create session", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "会话创建失败。")
		return
	}
	_ = s.db.TouchLastLogin(r.Context(), user.ID)
	s.guard.reset(strings.ToLower(username))
	_ = s.db.Audit(r.Context(), user.Username, "auth.login", "", ip, "session created")
	s.setSessionCookies(w, token, csrfToken, expires)
	redirect(w, r, "/admin")
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if sess, ok := r.Context().Value(ctxSession).(*store.Session); ok && sess != nil {
		_ = s.db.DeleteSession(r.Context(), sess.ID)
	}
	s.clearSessionCookies(w)
	redirect(w, r, "/admin/login")
}

// dummyPasswordHash keeps failed logins as expensive as successful ones.
func (s *Server) dummyPasswordHash() string {
	s.dummyMu.Lock()
	defer s.dummyMu.Unlock()
	if s.cachedDummy == "" {
		h, err := cryptoutil.HashPassword(cryptoutil.Itoa(int(time.Now().UnixNano()%100000)) + "dummy-password")
		if err == nil {
			s.cachedDummy = h
		}
	}
	return s.cachedDummy
}

func truncateUA(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// audit is a convenience wrapper that never fails a request.
func (s *Server) audit(r *http.Request, action, target, detail string) {
	actor := "system"
	if u := s.currentUser(r); u != nil {
		actor = u.Username
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
	defer cancel()
	if err := s.db.Audit(ctx, actor, action, target, s.clientIP(r), detail); err != nil {
		s.log.Warn("audit write failed", "action", action, "error", err)
	}
}

const cookieTwoFactor = "cw_2fa"

func (s *Server) setTwoFactorCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(cookieTwoFactor),
		Value:    token,
		Path:     "/admin",
		MaxAge:   int(store.ChallengeTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.Secure(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearTwoFactorCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(cookieTwoFactor),
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.Secure(),
		SameSite: http.SameSiteLaxMode,
	})
}

// startSession issues a fresh session, rotating every previous one.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user *store.User) error {
	token, err := cryptoutil.RandomToken(32)
	if err != nil {
		return err
	}
	sessionID, err := cryptoutil.RandomToken(16)
	if err != nil {
		return err
	}
	csrfToken, err := cryptoutil.RandomToken(32)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	expires := now.Add(s.cfg.SessionTTL)
	sess := &store.Session{
		ID:         sessionID,
		UserID:     user.ID,
		TokenHash:  cryptoutil.TokenHash(token),
		CSRFToken:  csrfToken,
		IP:         s.clientIP(r),
		UserAgent:  truncateUA(r.UserAgent()),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  expires,
	}
	if err := s.db.DeleteUserSessions(r.Context(), user.ID, ""); err != nil {
		s.log.Warn("failed to rotate sessions", "error", err)
	}
	if err := s.db.CreateSession(r.Context(), sess); err != nil {
		return err
	}
	_ = s.db.TouchLastLogin(r.Context(), user.ID)
	s.setSessionCookies(w, token, csrfToken, expires)
	return nil
}

func (s *Server) handleLoginTwoFactorForm(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(s.cookieName(cookieTwoFactor))
	if err != nil || c.Value == "" {
		redirect(w, r, "/admin/login")
		return
	}
	ch, err := s.db.ChallengeByToken(r.Context(), c.Value)
	if err != nil || time.Now().UTC().After(ch.ExpiresAt) {
		s.clearTwoFactorCookie(w)
		redirect(w, r, "/admin/login")
		return
	}
	s.render(w, r, "admin_login_2fa", map[string]any{"Title": "两步验证"})
}

func (s *Server) handleLoginTwoFactorSubmit(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(s.cookieName(cookieTwoFactor))
	if err != nil || c.Value == "" {
		redirect(w, r, "/admin/login")
		return
	}
	ch, err := s.db.ChallengeByToken(r.Context(), c.Value)
	if err != nil || time.Now().UTC().After(ch.ExpiresAt) {
		s.clearTwoFactorCookie(w)
		redirect(w, r, "/admin/login")
		return
	}
	if ch.Attempts >= store.MaxChallengeAttempts {
		_ = s.db.DeleteChallenge(r.Context(), ch.ID)
		s.clearTwoFactorCookie(w)
		s.renderError(w, r, http.StatusTooManyRequests, "验证尝试次数过多，请重新登录。")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	user, err := s.db.UserByID(r.Context(), ch.UserID)
	if err != nil || user.Disabled {
		_ = s.db.DeleteChallenge(r.Context(), ch.ID)
		s.clearTwoFactorCookie(w)
		redirect(w, r, "/admin/login")
		return
	}

	valid := false
	if secret, err := cryptoutil.Open(s.cfg.MasterKey, user.TOTPSecret, totpAAD(user.ID)); err == nil {
		valid = cryptoutil.VerifyTOTP(string(secret), code, time.Now())
	}
	if !valid && code != "" {
		valid, _ = s.db.ConsumeRecoveryCode(r.Context(), user.ID, code)
		if valid {
			s.audit(r, "auth.2fa.recovery", user.Username, "recovery code consumed")
		}
	}
	if !valid {
		attempts, _ := s.db.BumpChallengeAttempts(r.Context(), ch.ID)
		s.audit(r, "auth.2fa.failed", user.Username, "invalid second factor")
		if attempts >= store.MaxChallengeAttempts {
			_ = s.db.DeleteChallenge(r.Context(), ch.ID)
			s.clearTwoFactorCookie(w)
			s.renderError(w, r, http.StatusTooManyRequests, "验证尝试次数过多，请重新登录。")
			return
		}
		s.renderError(w, r, http.StatusUnauthorized, "验证码不正确。")
		return
	}

	_ = s.db.DeleteChallenge(r.Context(), ch.ID)
	s.clearTwoFactorCookie(w)
	if err := s.startSession(w, r, user); err != nil {
		s.log.Error("failed to create session after 2fa", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "会话创建失败。")
		return
	}
	s.guard.reset(strings.ToLower(user.Username))
	s.audit(r, "auth.login", user.Username, "second factor verified")
	redirect(w, r, "/admin")
}

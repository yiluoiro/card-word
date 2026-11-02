package httpapi

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

// loginGuard throttles credential stuffing per account. The IP rate limiter
// already slows a single source; this adds an account dimension so a botnet
// cannot hammer one username from many addresses.
type loginGuard struct {
	mu       sync.Mutex
	failures map[string]int
	lockedTo map[string]time.Time
	max      int
	window   time.Duration
}

func newLoginGuard() *loginGuard {
	return &loginGuard{
		failures: map[string]int{},
		lockedTo: map[string]time.Time{},
		max:      5,
		window:   15 * time.Minute,
	}
}

func (g *loginGuard) locked(username string) (time.Duration, bool) {
	if g == nil || username == "" {
		return 0, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.lockedTo[username]
	if !ok {
		return 0, false
	}
	if time.Now().After(until) {
		delete(g.lockedTo, username)
		delete(g.failures, username)
		return 0, false
	}
	return time.Until(until), true
}

func (g *loginGuard) fail(username string) {
	if g == nil || username == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures[username]++
	if g.failures[username] >= g.max {
		g.lockedTo[username] = time.Now().Add(g.window)
		g.failures[username] = 0
	}
	// Keep the map bounded; a few thousand usernames is plenty for a shop.
	if len(g.lockedTo) > 4096 {
		for k, until := range g.lockedTo {
			if time.Now().After(until) {
				delete(g.lockedTo, k)
			}
		}
	}
}

func (g *loginGuard) reset(username string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, username)
	delete(g.lockedTo, username)
}

// handleAdminAccount renders the account page.
func (s *Server) handleAdminAccount(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		redirect(w, r, "/admin/login")
		return
	}
	recoveryCount, err := s.db.CountRecoveryCodes(r.Context(), user.ID)
	if err != nil {
		s.log.Warn("count recovery codes failed", "error", err)
	}
	s.render(w, r, "admin_account", map[string]any{
		"Title":         "账号安全",
		"Account":       user,
		"SessionTTL":    s.cfg.SessionTTL.String(),
		"SessionIdle":   s.cfg.SessionIdle.String(),
		"RecoveryCount": recoveryCount,
	})
}

// handleAdminChangePassword rotates the operator password and invalidates every
// other session of the same account.
func (s *Server) handleAdminChangePassword(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		redirect(w, r, "/admin/login")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	ok, err := cryptoutil.VerifyPassword(user.PasswordHash, current)
	if err != nil || !ok {
		s.audit(r, "account.password.failed", user.Username, "wrong current password")
		flashRedirect(w, r, "/admin/account", "", "当前密码不正确。")
		return
	}
	switch {
	case len(next) < 12:
		flashRedirect(w, r, "/admin/account", "", "新密码至少 12 个字符。")
		return
	case next != confirm:
		flashRedirect(w, r, "/admin/account", "", "两次输入的新密码不一致。")
		return
	case next == current:
		flashRedirect(w, r, "/admin/account", "", "新密码不能与当前密码相同。")
		return
	}
	if err := s.db.UpdateUserPassword(r.Context(), user.ID, next); err != nil {
		s.log.Error("password update failed", "error", err)
		flashRedirect(w, r, "/admin/account", "", "密码更新失败，请查看服务日志。")
		return
	}
	keep := ""
	if sess, ok := r.Context().Value(ctxSession).(*store.Session); ok && sess != nil {
		keep = sess.ID
	}
	if err := s.db.DeleteUserSessions(r.Context(), user.ID, keep); err != nil {
		s.log.Warn("failed to rotate sessions after password change", "error", err)
	}
	s.audit(r, "account.password.change", user.Username, "sessions of other devices revoked")
	flashRedirect(w, r, "/admin/account", "密码已更新，其它设备的会话已失效", "")
}

// totpAAD binds a sealed TOTP secret to its account.
func totpAAD(userID int64) string { return fmt.Sprintf("totp:%d", userID) }

// handleTOTPStart provisions a new (not yet enabled) TOTP secret.
func (s *Server) handleTOTPStart(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		redirect(w, r, "/admin/login")
		return
	}
	secret, err := cryptoutil.NewTOTPSecret()
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "生成密钥失败。")
		return
	}
	sealed, err := cryptoutil.Seal(s.cfg.MasterKey, []byte(secret), totpAAD(user.ID))
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "保存密钥失败。")
		return
	}
	if err := s.db.SetTOTP(r.Context(), user.ID, sealed, false); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "保存密钥失败。")
		return
	}
	s.audit(r, "account.2fa.start", user.Username, "secret provisioned, pending verification")
	s.render(w, r, "admin_totp_setup", map[string]any{
		"Title":  "启用两步验证",
		"Secret": secret,
		"URI":    cryptoutil.TOTPURI(s.setting(store.SettingSiteName, "Card Word"), user.Username, secret),
	})
}

// handleTOTPEnable verifies the first code and issues recovery codes.
func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		redirect(w, r, "/admin/login")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	code := r.FormValue("code")
	secret, err := cryptoutil.Open(s.cfg.MasterKey, user.TOTPSecret, totpAAD(user.ID))
	if err != nil || !cryptoutil.VerifyTOTP(string(secret), code, time.Now()) {
		s.audit(r, "account.2fa.enable.failed", user.Username, "invalid verification code")
		flashRedirect(w, r, "/admin/account", "", "验证码不正确，请确认设备时间已同步后重试。")
		return
	}
	if err := s.db.SetTOTP(r.Context(), user.ID, user.TOTPSecret, true); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "启用失败。")
		return
	}
	codes, err := cryptoutil.NewRecoveryCodes(10)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "生成恢复码失败。")
		return
	}
	if err := s.db.ReplaceRecoveryCodes(r.Context(), user.ID, codes); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "保存恢复码失败。")
		return
	}
	s.audit(r, "account.2fa.enable", user.Username, "totp enabled, 10 recovery codes issued")
	s.render(w, r, "admin_totp_recovery", map[string]any{
		"Title": "恢复码",
		"Codes": codes,
	})
}

// handleTOTPDisable turns 2FA off, requiring password and a valid factor.
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		redirect(w, r, "/admin/login")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	ok, err := cryptoutil.VerifyPassword(user.PasswordHash, r.FormValue("password"))
	if err != nil || !ok {
		s.audit(r, "account.2fa.disable.failed", user.Username, "wrong password")
		flashRedirect(w, r, "/admin/account", "", "密码不正确。")
		return
	}
	code := r.FormValue("code")
	valid := false
	if secret, err := cryptoutil.Open(s.cfg.MasterKey, user.TOTPSecret, totpAAD(user.ID)); err == nil {
		valid = cryptoutil.VerifyTOTP(string(secret), code, time.Now())
	}
	if !valid {
		valid, _ = s.db.ConsumeRecoveryCode(r.Context(), user.ID, code)
	}
	if !valid {
		s.audit(r, "account.2fa.disable.failed", user.Username, "invalid factor")
		flashRedirect(w, r, "/admin/account", "", "验证码或恢复码不正确。")
		return
	}
	if err := s.db.ClearTOTP(r.Context(), user.ID); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "关闭失败。")
		return
	}
	_ = s.db.ReplaceRecoveryCodes(r.Context(), user.ID, nil)
	s.audit(r, "account.2fa.disable", user.Username, "totp disabled")
	flashRedirect(w, r, "/admin/account", "两步验证已关闭", "")
}

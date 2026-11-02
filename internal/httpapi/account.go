package httpapi

import (
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
	s.render(w, r, "admin_account", map[string]any{
		"Title":       "账号安全",
		"Account":     user,
		"SessionTTL":  s.cfg.SessionTTL.String(),
		"SessionIdle": s.cfg.SessionIdle.String(),
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

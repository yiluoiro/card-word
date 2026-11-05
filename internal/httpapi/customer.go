package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

const ctxCustomer ctxKey = "customer"
const cookieCustomer = "cw_customer"

var customerUsername = regexp.MustCompile(`^[a-zA-Z0-9_]{3,32}$`)

func (s *Server) currentCustomer(r *http.Request) *store.Customer {
	c, _ := r.Context().Value(ctxCustomer).(*store.Customer)
	return c
}

func (s *Server) customerCookie(w http.ResponseWriter, token string, expires time.Time) {
	c := &http.Cookie{Name: s.cookieName(cookieCustomer), Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.Secure(), SameSite: http.SameSiteLaxMode, Expires: expires}
	if token == "" {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

func (s *Server) customerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(s.cookieName(cookieCustomer)); err == nil && c.Value != "" {
			hash := cryptoutil.TokenHash(c.Value)
			sess, err := s.db.CustomerSessionByHash(r.Context(), hash)
			now := time.Now().UTC()
			if err == nil && now.Before(sess.ExpiresAt) && now.Sub(sess.LastSeenAt) < s.cfg.SessionIdle {
				customer, err := s.db.CustomerByID(r.Context(), sess.CustomerID)
				if err == nil {
					ctx := context.WithValue(r.Context(), ctxCustomer, customer)
					// Bind buyer forms to the server-side session, not a freely chosen cookie.
					if s.currentUser(r) == nil {
						csrf := cryptoutil.HMACSHA256Hex(s.cfg.MasterKey, "customer-csrf:"+hash)
						ctx = context.WithValue(ctx, ctxCSRF, csrf)
						s.setCSRFCookie(w, csrf)
					}
					r = r.WithContext(ctx)

					_ = s.db.TouchCustomerSession(ctx, hash, now)
				}
			} else if err == nil || errors.Is(err, store.ErrNotFound) {
				_ = s.db.DeleteCustomerSession(r.Context(), hash)
				s.customerCookie(w, "", time.Time{})
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) customerOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.currentCustomer(r) == nil {
			target := "/account/login"
			if next := customerReturn(r.URL.Path); next != "" {
				target += "?next=" + urlEscape(next)
			}
			redirect(w, r, target)
			return
		}
		next(w, r)
	}
}

func customerReturn(next string) string {
	if strings.HasPrefix(next, "/reports/new/") {
		if id, err := strconv.ParseInt(strings.TrimPrefix(next, "/reports/new/"), 10, 64); err == nil && id > 0 {
			return "/reports/new/" + strconv.FormatInt(id, 10)
		}
	}
	return ""
}

func (s *Server) handleCustomerAuthForm(w http.ResponseWriter, r *http.Request) {
	if s.currentCustomer(r) != nil {
		redirect(w, r, "/account")
		return
	}
	register := r.URL.Path == "/account/register"
	title := "登录用户中心"
	if register {
		title = "注册买家账号"
	}
	s.renderChallengeForm(w, r, 200, "customer_auth", strings.TrimPrefix(r.URL.Path, "/"), map[string]any{"Title": title, "Register": register, "ReturnTo": customerReturn(r.URL.Query().Get("next"))})
}

func (s *Server) startCustomerSession(w http.ResponseWriter, r *http.Request, c *store.Customer) error {
	token, err := cryptoutil.RandomToken(32)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	expires := now.Add(s.cfg.SessionTTL)
	err = s.db.CreateCustomerSession(r.Context(), &store.CustomerSession{TokenHash: cryptoutil.TokenHash(token), CustomerID: c.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: expires})
	if err != nil {
		return err
	}
	s.customerCookie(w, token, expires)
	return nil
}

func (s *Server) handleCustomerAuth(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit.allow(s.clientIP(r)) {
		s.renderError(w, r, 429, "操作过于频繁，请稍后重试。")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, 400, "表单格式错误。")
		return
	}
	username := strings.ToLower(strings.TrimSpace(r.FormValue("username")))
	password := r.FormValue("password")
	register := r.URL.Path == "/account/register"
	title := "登录用户中心"
	if register {
		title = "注册买家账号"
	}
	fail := func(status int, msg string) {
		s.renderChallengeForm(w, r, status, "customer_auth", strings.TrimPrefix(r.URL.Path, "/"), map[string]any{"Title": title, "Register": register, "Error": msg, "Username": username, "ReturnTo": customerReturn(r.FormValue("next"))})
	}
	if !s.formVerified(r, strings.TrimPrefix(r.URL.Path, "/")) {
		fail(422, captchaRetry)
		return
	}
	if !customerUsername.MatchString(username) || len(password) > 1024 || len(password) == 0 {
		fail(400, "用户名须为 3–32 位字母、数字或下划线；密码最长 1024 字节。")
		return
	}
	key := "customer:" + username
	if _, locked := s.guard.locked(key); locked {
		fail(429, "该账号尝试过于频繁，请稍后重试。")
		return
	}
	var customer *store.Customer
	var err error
	if register {
		if len(password) < 12 || password != r.FormValue("confirm_password") {
			fail(400, "密码至少 12 字节，且两次输入必须一致。")
			return
		}
		customer, err = s.db.CreateCustomer(r.Context(), username, password)
		if err != nil {
			fail(400, "无法创建账号，请更换用户名或稍后重试。")
			return
		}
	} else {
		customer, err = s.db.CustomerByUsername(r.Context(), username)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			fail(500, "登录暂不可用，请稍后重试。")
			return
		}
		hash := s.dummyPasswordHash()
		if customer != nil {
			hash = customer.PasswordHash
		}
		valid, verifyErr := cryptoutil.VerifyPassword(hash, password)
		if customer == nil || !valid || verifyErr != nil {
			s.guard.fail(key)
			_ = s.db.Audit(r.Context(), "customer:"+username, "customer.login.failed", "", s.clientIP(r), "invalid credentials")
			fail(401, "用户名或密码错误。")
			return
		}
	}
	if err = s.startCustomerSession(w, r, customer); err != nil {
		fail(500, "会话创建失败，请重新登录。")
		return
	}
	s.guard.reset(key)
	_ = s.db.Audit(r.Context(), "customer:"+username, "customer.login", "", s.clientIP(r), "buyer session created")
	if next := customerReturn(r.FormValue("next")); next != "" {
		redirect(w, r, next)
		return
	}
	redirect(w, r, "/account")
}

func (s *Server) checkoutIdempotencyKey(r *http.Request, key string) string {
	scope, _ := r.Context().Value(ctxCSRF).(string)
	if c := s.currentCustomer(r); c != nil {
		scope = "customer:" + strconv.FormatInt(c.ID, 10)
	}
	return "checkout:" + cryptoutil.HMACSHA256Hex(s.cfg.MasterKey, scope+":"+key)
}

func (s *Server) checkoutCustomerID(r *http.Request) int64 {
	if c := s.currentCustomer(r); c != nil {
		return c.ID
	}
	return 0
}

func (s *Server) handleCustomerAccount(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 10000 {
		page = 10000
	}
	orders, err := s.db.ListCustomerOrders(r.Context(), s.currentCustomer(r).ID, 21, (page-1)*20)
	if err != nil {
		s.renderError(w, r, 500, "加载订单失败，请稍后重试。")
		return
	}
	more := len(orders) > 20
	if more {
		orders = orders[:20]
	}
	s.render(w, r, "customer_account", map[string]any{"Title": "用户中心", "Orders": orders, "Page": page, "Previous": page - 1, "Next": page + 1, "More": more})
}

func (s *Server) handleCustomerLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookieName(cookieCustomer)); err == nil {
		if err = s.db.DeleteCustomerSession(r.Context(), cryptoutil.TokenHash(c.Value)); err != nil {
			s.renderError(w, r, 500, "退出失败，请重试。")
			return
		}
	}
	s.customerCookie(w, "", time.Time{})
	redirect(w, r, "/account/login")
}

func (s *Server) handleCustomerPassword(w http.ResponseWriter, r *http.Request) {
	customer := s.currentCustomer(r)
	if !s.authLimit.allow(s.clientIP(r)) {
		s.renderError(w, r, 429, "操作过于频繁，请稍后重试。")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, 400, "表单格式错误。")
		return
	}
	current, next := r.FormValue("current_password"), r.FormValue("new_password")
	if len(current) > 1024 || len(next) < 12 || len(next) > 1024 || next != r.FormValue("confirm_password") || next == current {
		flashRedirect(w, r, "/account", "", "新密码须为 12–1024 字节、两次输入一致，且不能与当前密码相同。")
		return
	}
	valid, err := cryptoutil.VerifyPassword(customer.PasswordHash, current)
	if err != nil || !valid {
		flashRedirect(w, r, "/account", "", "当前密码不正确。")
		return
	}
	if err = s.db.ChangeCustomerPassword(r.Context(), customer.ID, customer.PasswordHash, next); err != nil {
		s.renderError(w, r, 500, "密码更新失败，请重新登录后重试。")
		return
	}
	s.customerCookie(w, "", time.Time{})
	_ = s.db.Audit(r.Context(), "customer:"+customer.Username, "customer.password.change", "", s.clientIP(r), "all buyer sessions revoked")
	flashRedirect(w, r, "/account/login", "密码已更新，请重新登录。所有买家会话已失效。", "")
}

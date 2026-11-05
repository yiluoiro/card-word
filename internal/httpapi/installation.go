package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

// WithInstallation gates the shop until an owner exists.
// Called explicitly by the executable so isolated test servers remain usable.
func (s *Server) WithInstallation(next http.Handler) (http.Handler, error) {
	ctx := context.Background()
	count, err := s.db.CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	complete, err := s.db.GetSetting(ctx, "installation.completed", "")
	if err != nil {
		return nil, err
	}
	tokenPath := filepath.Join(s.cfg.DataDir, "install-token.txt")
	closed := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/install" || strings.HasPrefix(r.URL.Path, "/install/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	}
	if count > 0 || complete == "1" {
		_ = os.Remove(tokenPath)
		return http.HandlerFunc(closed), nil
	}
	token, err := installationToken(tokenPath)
	if err != nil {
		return nil, err
	}
	s.log.Info("installation required", "path", "/install", "token_file", tokenPath)
	var finished atomic.Bool
	var submit sync.Mutex
	install := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		if strings.HasPrefix(r.URL.Path, "/static/") {
			s.mux.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/healthz" && (r.Method == "GET" || r.Method == "HEAD") {
			s.handleHealth(w, r)
			return
		}
		if r.URL.Path == "/readyz" {
			writeJSON(w, 503, map[string]string{"status": "installation_required"})
			return
		}
		if r.URL.Path != "/install" {
			if r.Method == "GET" || r.Method == "HEAD" {
				http.Redirect(w, r, "/install", http.StatusSeeOther)
			} else {
				http.Error(w, "installation required", http.StatusServiceUnavailable)
			}
			return
		}
		data := map[string]any{"Title": "安装 Card Word", "BaseURL": s.cfg.BaseURL, "SiteName": "Card Word", "Username": "admin"}
		if r.Method == "GET" || r.Method == "HEAD" {
			s.render(w, r, "installation", data)
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "GET, HEAD, POST")
			http.Error(w, "method not allowed", 405)
			return
		}
		if !s.authLimit.allow(s.clientIP(r)) {
			http.Error(w, "操作过于频繁，请稍后重试。", 429)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "表单格式错误。", 400)
			return
		}
		submit.Lock()
		defer submit.Unlock()
		if finished.Load() {
			http.NotFound(w, r)
			return
		}
		fail := func(status int, msg string) {
			data["Error"] = msg
			data["SiteName"] = r.PostFormValue("site_name")
			data["Username"] = r.PostFormValue("username")
			data["Contact"] = r.PostFormValue("contact")
			s.renderStatus(w, r, status, "installation", data)
		}
		if !constantTimeEqual(strings.TrimSpace(r.PostFormValue("install_token")), token) {
			fail(403, "安装码不正确。请从服务器的数据目录读取 install-token.txt。")
			return
		}
		if r.PostFormValue("password") != r.PostFormValue("confirm_password") {
			fail(422, "两次输入的密码不一致。")
			return
		}
		err := s.db.Install(r.Context(), store.Installation{SiteName: r.PostFormValue("site_name"), Username: r.PostFormValue("username"), Password: r.PostFormValue("password"), Contact: r.PostFormValue("contact")})
		if errors.Is(err, store.ErrConflict) {
			finished.Store(true)
			http.NotFound(w, r)
			return
		}
		if err != nil {
			fail(422, "安装未完成。站点名称最多 80 字；用户名为 3–32 位字母、数字或下划线；密码为 12–1024 字节；联系方式最多 200 字。")
			s.log.Warn("installation failed", "error", err)
			return
		}
		if err := s.reloadSettings(r.Context()); err != nil {
			s.log.Error("installation cache reload failed", "error", err)
		}
		finished.Store(true)
		if err := os.Remove(tokenPath); err != nil {
			s.log.Warn("installation token cleanup failed", "error", err)
		}
		http.Redirect(w, r, "/admin/login?ok="+urlEscape("安装完成，请使用刚创建的管理员账号登录。"), http.StatusSeeOther)
	})
	var protected http.Handler = install
	protected = s.csrfProtect(protected)
	protected = s.bodyLimit(protected)
	protected = s.sessionMiddleware(protected)
	protected = s.rateLimit(protected)
	protected = s.securityHeaders(protected)
	protected = s.recoverer(protected)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if finished.Load() {
			closed(w, r)
			return
		}
		protected.ServeHTTP(w, r)
	}), nil
}

func installationToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(b))
		if len(token) != 43 {
			return "", fmt.Errorf("invalid installation token file: %s", path)
		}
		return token, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	token, err := cryptoutil.RandomToken(32)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return installationToken(path)
	}
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(token + "\n")
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	return token, nil
}

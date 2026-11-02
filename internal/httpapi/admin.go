package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/notify"
	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/store"
)

// reloadIntegrations (re)builds the payment drivers and mail sender from the
// settings table. It is called at startup and after settings are saved.
func (s *Server) reloadIntegrations(ctx context.Context) {
	if s.payments == nil {
		s.payments = payment.NewRegistry()
	}
	instructions, _ := s.db.GetSetting(ctx, "payment.manual.instructions", "")
	s.payments.Register(&payment.Manual{Instructions: instructions})

	gateway, _ := s.db.GetSetting(ctx, "payment.epay.gateway", "")
	pid, _ := s.db.GetSetting(ctx, "payment.epay.pid", "")
	key, _ := s.db.GetSetting(ctx, "payment.epay.key", "")
	payType, _ := s.db.GetSetting(ctx, "payment.epay.type", "alipay")
	if gateway != "" && pid != "" && key != "" {
		if ep, err := payment.NewEpay(payment.EpayConfig{GatewayURL: gateway, PID: pid, Key: key, PayType: payType}); err == nil {
			s.payments.Register(ep)
		} else {
			s.log.Warn("epay driver disabled", "error", err)
		}
	}

	if s.notifier != nil {
		host, _ := s.db.GetSetting(ctx, "smtp.host", "")
		from, _ := s.db.GetSetting(ctx, "smtp.from", "")
		if host != "" && from != "" {
			port := int(parseInt64(orDefault(mustSetting(ctx, s.db, "smtp.port", "587"), "587")))
			sender := notify.NewSMTP(notify.SMTPConfig{
				Host:       host,
				Port:       port,
				Username:   mustSetting(ctx, s.db, "smtp.username", ""),
				Password:   mustSetting(ctx, s.db, "smtp.password", ""),
				From:       from,
				FromName:   mustSetting(ctx, s.db, "smtp.from_name", "Card Word"),
				UseTLS:     mustSetting(ctx, s.db, "smtp.tls", "1") == "1",
				SkipVerify: mustSetting(ctx, s.db, "smtp.insecure", "0") == "1",
			})
			if sender != nil {
				s.notifier.SetSenders([]notify.Sender{sender})
			}
		}
	}
}

// LoadIntegrations rebuilds payment drivers and mail senders from settings.
// It is exported so the entry point can prime them right after startup.
func LoadIntegrations(s *Server, ctx context.Context) {
	if err := s.reloadSettings(ctx); err != nil {
		s.log.Warn("settings load failed", "error", err)
	}
	s.reloadIntegrations(ctx)
}

func mustSetting(ctx context.Context, db *store.DB, key, def string) string {
	v, err := db.GetSetting(ctx, key, def)
	if err != nil {
		return def
	}
	return v
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stats, err := s.db.Stats(ctx)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载统计失败。")
		return
	}
	orders, err := s.db.ListOrders(ctx, store.OrderFilter{Limit: 10})
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载订单失败。")
		return
	}
	products, err := s.db.ListProducts(ctx, store.ProductFilter{Limit: 200})
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载商品失败。")
		return
	}
	lowStock := make([]store.Product, 0, 8)
	for _, p := range products {
		if p.Stock <= 3 && p.Status == store.ProductActive {
			lowStock = append(lowStock, p)
		}
	}
	var pluginCount int
	if s.plugins != nil {
		pluginCount = len(s.plugins.Active())
	}
	s.render(w, r, "admin_dashboard", map[string]any{
		"Title":       "控制台",
		"Stats":       stats,
		"Orders":      orders,
		"Products":    products,
		"LowStock":    lowStock,
		"PluginCount": pluginCount,
	})
}

// ---------- categories ----------

func (s *Server) handleAdminCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.db.ListCategories(r.Context(), false)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载分类失败。")
		return
	}
	s.render(w, r, "admin_categories", map[string]any{"Title": "分类管理", "Categories": cats})
}

func (s *Server) handleAdminCategorySave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	slug := strings.TrimSpace(r.FormValue("slug"))
	sortOrder := int(parseInt64(r.FormValue("sort")))
	visible := r.FormValue("visible") != ""
	if name == "" {
		flashRedirect(w, r, "/admin/categories", "", "分类名称不能为空。")
		return
	}
	if _, err := s.db.CreateCategory(r.Context(), name, slug, sortOrder, visible); err != nil {
		flashRedirect(w, r, "/admin/categories", "", "创建失败："+err.Error())
		return
	}
	s.audit(r, "category.create", name, "")
	flashRedirect(w, r, "/admin/categories", "分类已创建", "")
}

func (s *Server) handleAdminCategoryDelete(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	if err := s.db.DeleteCategory(r.Context(), id); err != nil {
		flashRedirect(w, r, "/admin/categories", "", "删除失败。")
		return
	}
	s.audit(r, "category.delete", cryptoutil.Itoa(int(id)), "")
	flashRedirect(w, r, "/admin/categories", "分类已删除", "")
}

// ---------- settings ----------

var editableSettings = []struct {
	Key   string
	Label string
}{
	{store.SettingSiteName, "站点名称"},
	{store.SettingSiteTagline, "站点副标题"},
	{store.SettingSiteNotice, "站点公告"},
	{store.SettingContactEmail, "联系邮箱"},
	{store.SettingCurrency, "默认货币"},
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	all, err := s.db.AllSettings(ctx)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载设置失败。")
		return
	}
	drivers := make([]map[string]string, 0, 8)
	for _, d := range s.allDrivers() {
		drivers = append(drivers, map[string]string{"Name": d.Name(), "Title": d.Title()})
	}
	s.render(w, r, "admin_settings", map[string]any{
		"Title":    "系统设置",
		"Settings": all,
		"Drivers":  drivers,
		"Fields":   editableSettings,
	})
}

func (s *Server) handleAdminSettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	ctx := r.Context()
	keys := []string{
		store.SettingSiteName,
		store.SettingSiteTagline,
		store.SettingSiteNotice,
		store.SettingContactEmail,
		store.SettingCurrency,
		store.SettingPaymentDriver,
		"payment.manual.instructions",
		"payment.epay.gateway",
		"payment.epay.pid",
		"payment.epay.key",
		"payment.epay.type",
		"smtp.host",
		"smtp.port",
		"smtp.username",
		"smtp.password",
		"smtp.from",
		"smtp.from_name",
	}
	for _, k := range keys {
		if !r.Form.Has(k) {
			continue
		}
		value := strings.TrimSpace(r.FormValue(k))
		if k == "payment.epay.key" || k == "smtp.password" {
			// Never overwrite a stored secret with the masked placeholder.
			if value == "" || strings.HasPrefix(value, "••") {
				continue
			}
		}
		if err := s.db.SetSetting(ctx, k, value); err != nil {
			flashRedirect(w, r, "/admin/settings", "", "保存失败："+err.Error())
			return
		}
	}
	// Booleans are checkbox based.
	maintenance := "0"
	if r.FormValue("maintenance") != "" {
		maintenance = "1"
	}
	_ = s.db.SetSetting(ctx, store.SettingMaintenance, maintenance)
	smtpTLS := "0"
	if r.FormValue("smtp.tls") != "" {
		smtpTLS = "1"
	}
	_ = s.db.SetSetting(ctx, "smtp.tls", smtpTLS)

	if err := s.reloadSettings(ctx); err != nil {
		s.log.Warn("settings cache reload failed", "error", err)
	}
	s.reloadIntegrations(ctx)
	s.audit(r, "settings.update", "", "settings saved")
	flashRedirect(w, r, "/admin/settings", "设置已保存", "")
}

// ---------- audit ----------

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	entries, err := s.db.ListAudit(r.Context(), action, 200, 0)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载审计日志失败。")
		return
	}
	s.render(w, r, "admin_audit", map[string]any{
		"Title":   "审计日志",
		"Entries": entries,
		"Action":  action,
	})
}

// ---------- API tokens ----------

func (s *Server) handleAdminTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.db.ListAPITokens(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载令牌失败。")
		return
	}
	s.render(w, r, "admin_tokens", map[string]any{
		"Title":    "API 令牌",
		"Tokens":   tokens,
		"NewToken": r.URL.Query().Get("token"),
	})
}

func (s *Server) handleAdminTokenCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	scopes := []string{"products:read", "orders:read"}
	if r.FormValue("scope_write") != "" {
		scopes = append(scopes, "orders:write", "cards:write")
	}
	token, plain, err := s.db.CreateAPIToken(r.Context(), name, scopes)
	if err != nil {
		flashRedirect(w, r, "/admin/tokens", "", "创建失败："+err.Error())
		return
	}
	s.audit(r, "token.create", token.Name, strings.Join(scopes, ","))
	flashRedirect(w, r, "/admin/tokens?token="+urlEscape(plain), "令牌已创建，请立即保存", "")
}

func (s *Server) handleAdminTokenRevoke(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	if err := s.db.RevokeAPIToken(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			flashRedirect(w, r, "/admin/tokens", "", "令牌不存在或已撤销。")
			return
		}
		flashRedirect(w, r, "/admin/tokens", "", "撤销失败。")
		return
	}
	s.audit(r, "token.revoke", cryptoutil.Itoa(int(id)), "")
	flashRedirect(w, r, "/admin/tokens", "令牌已撤销", "")
}

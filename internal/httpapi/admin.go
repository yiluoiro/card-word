package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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

	s.reloadBuiltinPayments(ctx)

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
		} else {
			s.notifier.SetSenders(nil)
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
	currency := s.setting(store.SettingCurrency, "CNY")
	currencies, err := s.db.OrderCurrencies(ctx)
	if err != nil {
		s.renderError(w, r, 500, "加载统计币种失败。")
		return
	}
	found := false
	for _, c := range currencies {
		if c == currency {
			found = true
		}
	}
	if !found {
		currencies = append([]string{currency}, currencies...)
	}
	for _, c := range currencies {
		if r.URL.Query().Get("currency") == c {
			currency = c
			break
		}
	}
	now := time.Now().UTC()
	period, err := parseDashboardPeriod(r.URL.Query(), now)
	if err != nil {
		s.renderError(w, r, 400, err.Error())
		return
	}
	periodStats, err := s.db.PeriodStats(ctx, currency, period.Start, period.End)
	if err != nil {
		s.renderError(w, r, 500, "加载周期统计失败。")
		return
	}
	previous, err := s.db.PeriodStats(ctx, currency, period.Start.Add(-period.End.Sub(period.Start)), period.Start)
	if err != nil {
		s.renderError(w, r, 500, "加载对比统计失败。")
		return
	}
	month, _ := parseDashboardPeriod(nil, now)
	monthStats, err := s.db.PeriodStats(ctx, currency, month.Start, month.End)
	if err != nil {
		s.renderError(w, r, 500, "加载本月统计失败。")
		return
	}
	exportQuery := url.Values{"currency": {currency}, "from": {period.From}, "to": {period.To}}
	stats, err := s.db.StatsCurrency(ctx, currency)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载统计失败。")
		return
	}
	orders, err := s.db.ListOrders(ctx, store.OrderFilter{Limit: 10})
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载订单失败。")
		return
	}
	stockFilter := store.ProductFilter{Status: store.ProductActive, Stock: "low", Limit: 8}
	lowCount, err := s.db.CountProducts(ctx, stockFilter)
	if err != nil {
		s.renderError(w, r, 500, "加载库存统计失败。")
		return
	}
	lowStock, err := s.db.ListProducts(ctx, stockFilter)
	if err != nil {
		s.renderError(w, r, 500, "加载库存失败。")
		return
	}
	tasks, err := s.workspaceTasks(r, lowCount)
	if err != nil {
		s.renderError(w, r, 500, "加载经营待办失败。")
		return
	}
	daily, err := s.db.DailyStatsCurrency(ctx, 7, currency)
	if err != nil {
		s.renderError(w, r, 500, "加载订单趋势失败。")
		return
	}
	var ordersSeries, revenueSeries, pendingSeries, paidSeries []int64
	for _, d := range daily {
		ordersSeries = append(ordersSeries, d.Orders)
		pendingSeries = append(pendingSeries, d.PendingOrders)
		paidSeries = append(paidSeries, d.PaidOrders)
		revenueSeries = append(revenueSeries, d.RevenueCents)
	}

	s.render(w, r, "admin_dashboard", map[string]any{
		"TodayUTC":        now.Format("2006-01-02"),
		"Period":          period,
		"PeriodStats":     periodStats,
		"MonthStats":      monthStats,
		"PeriodExportURL": "/admin/orders/export.csv?" + exportQuery.Encode(),
		"OrdersChange":    periodChange(periodStats.Orders, previous.Orders),
		"RevenueChange":   periodChange(periodStats.Revenue, previous.Revenue),
		"CompletedChange": periodChange(periodStats.Completed, previous.Completed),
		"ClosedChange":    periodChange(periodStats.Closed, previous.Closed),
		"Chart":           buildDashboardChart(daily, currency),
		"Title":           "概览",
		"Currency":        currency,
		"Currencies":      currencies,
		"Stats":           stats,
		"Orders":          orders,
		"Tasks":           tasks,
		"LowStockCount":   lowCount,
		"LowStock":        lowStock,
		"SparkOrders":     sparklinePoints(ordersSeries),
		"SparkRevenue":    sparklinePoints(revenueSeries),
		"SparkPending":    sparklinePoints(pendingSeries),
		"SparkPaid":       sparklinePoints(paidSeries),
	})
}

// sparklinePoints builds a 100x30 polyline for a KPI card.
func sparklinePoints(values []int64) string {
	if len(values) == 0 {
		return ""
	}
	var max int64
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		max = 1
	}
	var sb strings.Builder
	n := len(values)
	for i, v := range values {
		x := 0.0
		if n > 1 {
			x = float64(i) * 100 / float64(n-1)
		}
		y := 28 - float64(v)/float64(max)*24
		sb.WriteString(strconv.FormatFloat(x, 'f', 1, 64))
		sb.WriteString(",")
		sb.WriteString(strconv.FormatFloat(y, 'f', 1, 64))
		if i < n-1 {
			sb.WriteString(" ")
		}
	}
	return sb.String()
}

// ---------- categories ----------

func (s *Server) handleAdminCategories(w http.ResponseWriter, r *http.Request) {
	rows, view, ok := s.adminList(w, r, s.db, "categories", "")
	if !ok {
		return
	}
	s.render(w, r, "admin_categories", map[string]any{"Title": "分类管理", "Categories": rows, "List": view})
}

func (s *Server) handleAdminCategorySave(w http.ResponseWriter, r *http.Request) {
	if !s.requireStaffRole(w, r, store.RoleAdmin) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	slug := strings.TrimSpace(r.FormValue("slug"))
	sortOrder := int(parseInt64(r.FormValue("sort")))
	visible := r.FormValue("visible") != ""
	if name == "" {
		s.editOrRedirect(w, r, "categories", "分类名称不能为空。")
		return
	}
	var err error
	if id := parseInt64(r.FormValue("id")); id > 0 {
		if slug == "" {
			slug = store.Slugify(name)
		}
		err = s.db.UpdateCategory(r.Context(), &store.Category{ID: id, Name: name, Slug: slug, Sort: sortOrder, Visible: visible})
	} else {
		_, err = s.db.CreateCategory(r.Context(), name, slug, sortOrder, visible)
	}
	if err != nil {
		s.editOrRedirect(w, r, "categories", "保存失败，请检查重复名称或别名。")
		return
	}
	s.audit(r, "category.save", name, "")
	flashRedirect(w, r, "/admin/categories", "分类已保存", "")
}

func (s *Server) handleAdminCategoryDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireStaffRole(w, r, store.RoleAdmin) {
		return
	}
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
	rows, view, ok := s.adminList(w, r, s.db, "audit", "")
	if !ok {
		return
	}
	s.render(w, r, "admin_audit", map[string]any{"Title": "审计日志", "Entries": rows, "List": view})
}

// ---------- API tokens ----------

func (s *Server) handleAdminTokens(w http.ResponseWriter, r *http.Request) {
	s.renderTokens(w, r, "")
}
func (s *Server) renderTokens(w http.ResponseWriter, r *http.Request, plain string) {
	rows, view, ok := s.adminList(w, r, s.db, "tokens", "")
	if !ok {
		return
	}
	s.render(w, r, "admin_tokens", map[string]any{"Title": "API 令牌", "Tokens": rows, "List": view, "NewToken": plain})
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
	if r.FormValue("scope_cards") != "" {
		scopes = append(scopes, "cards:read")
	}
	in := store.TokenInput{Name: name, Scopes: scopes}
	if days := parseInt64(r.FormValue("expires_days")); days > 0 {
		expires := time.Now().UTC().AddDate(0, 0, int(days))
		in.ExpiresAt = &expires
	}
	in.AllowedIPs = strings.FieldsFunc(r.FormValue("allowed_ips"), func(r rune) bool {
		return r == ',' || r == '\n' || r == ' ' || r == '\t'
	})
	token, plain, err := s.db.CreateAPIToken(r.Context(), in)
	if err != nil {
		flashRedirect(w, r, "/admin/tokens", "", "创建失败："+err.Error())
		return
	}
	s.audit(r, "token.create", token.Name, strings.Join(scopes, ","))
	// Display once in the POST response, never in URLs/history/access logs.
	w.Header().Set("Cache-Control", "no-store")
	s.renderTokens(w, r, plain)
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

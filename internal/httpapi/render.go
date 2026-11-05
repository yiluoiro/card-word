package httpapi

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/plugin"
	"github.com/yiluoiro/card-word/internal/store"
	"github.com/yiluoiro/card-word/internal/version"
)

//go:embed web/templates/*.html
var templateFS embed.FS

//go:embed web/static/*
var staticFS embed.FS

// Templates parses the embedded template set.
func Templates() (*template.Template, error) {
	funcs := template.FuncMap{
		"expired":     func(t *time.Time) bool { return t != nil && !time.Now().Before(*t) },
		"brandImage":  productBrandImage,
		"money":       formatMoney,
		"markdown":    renderMarkdown,
		"mdExcerpt":   markdownExcerpt,
		"moneyShort":  moneyShort,
		"datetime":    formatTime,
		"date":        func(t time.Time) string { return t.Local().Format("2006-01-02") },
		"ago":         humanAgo,
		"statusText":  statusText,
		"statusClass": statusClass,
		"maskEmail":   maskEmail,
		"join":        strings.Join,
		"hasPrefix":   strings.HasPrefix,
		"dict":        dict,
		"add":         func(a, b int) int { return a + b },
		"pct": func(part, total int64) int {
			if total == 0 {
				return 0
			}
			return int(part * 100 / total)
		},
		"version": func() string { return version.Version },
	}
	return template.New("cardword").Funcs(funcs).ParseFS(templateFS, "web/templates/*.html")
}

func staticHandler() http.Handler {
	sub, err := fsSub(staticFS, "web/static")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(sub))
}

func dict(values ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i+1 < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			continue
		}
		out[key] = values[i+1]
	}
	return out
}

func formatMoney(cents int64, currency string) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	symbol := currencySymbol(currency)
	return fmt.Sprintf("%s%s%d.%02d", sign, symbol, cents/100, cents%100)
}

func moneyShort(cents int64) string {
	return strconv.FormatFloat(float64(cents)/100.0, 'f', 2, 64)
}

func currencySymbol(currency string) string {
	switch strings.ToUpper(currency) {
	case "CNY", "RMB", "":
		return "¥"
	case "USD":
		return "$"
	case "EUR":
		return "€"
	case "JPY":
		return "¥"
	default:
		return strings.ToUpper(currency) + " "
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func humanAgo(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	}
}

func statusText(status string) string {
	switch status {
	case store.OrderPending:
		return "待支付"
	case store.OrderPaid:
		return "已支付(待补发)"
	case store.OrderDelivered:
		return "已发货"
	case store.OrderClosed:
		return "已关闭"
	case store.OrderRefunded:
		return "已退款"
	case store.CardAvailable:
		return "可售"
	case store.CardSold:
		return "已售"
	case store.CardDisabled:
		return "已停用"
	case store.ProductActive:
		return "上架"
	case store.ProductDraft:
		return "草稿"
	case store.ProductArchived:
		return "归档"
	default:
		return status
	}
}

func statusClass(status string) string {
	switch status {
	case store.OrderDelivered, store.CardAvailable, store.ProductActive:
		return "ok"
	case store.OrderPending, store.ProductDraft:
		return "warn"
	case store.OrderPaid:
		return "info"
	case store.OrderClosed, store.OrderRefunded, store.CardDisabled, store.ProductArchived:
		return "muted"
	default:
		return "muted"
	}
}

func maskEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 1 {
		return email
	}
	name := email[:at]
	if len(name) <= 2 {
		return string(name[0]) + "***" + email[at:]
	}
	return name[:2] + "***" + email[at:]
}

// viewData is the base payload passed to every template.
type viewData struct {
	Site        siteInfo
	User        *store.User
	CSRF        string
	Path        string
	Query       string
	Flash       string
	Error       string
	Currency    string
	Version     string
	CurrentYear int
}

type siteInfo struct {
	IconURL     string
	Name        string
	Tagline     string
	Notice      string
	Contact     string
	Maintenance bool
}

// baseData builds the shared template payload.
func (s *Server) baseData(r *http.Request) map[string]any {
	user, _ := r.Context().Value(ctxUser).(*store.User)
	csrf, _ := r.Context().Value(ctxCSRF).(string)
	currency := s.setting(store.SettingCurrency, "CNY")
	appearance := s.appearance(r)
	style, accent := "", ""
	if appearance.Style != "default" && !strings.HasPrefix(r.URL.Path, "/admin") {
		style, accent = appearance.Style, appearance.Accent
	}

	return map[string]any{
		"Site": siteInfo{
			IconURL:     s.siteIconURL(),
			Name:        s.setting(store.SettingSiteName, "Card Word"),
			Tagline:     s.setting(store.SettingSiteTagline, "数字商品与礼品卡，付款确认后查看卡密。"),
			Notice:      s.setting(store.SettingSiteNotice, ""),
			Contact:     s.setting(store.SettingContactEmail, ""),
			Maintenance: s.setting(store.SettingMaintenance, "0") == "1",
		},
		"User":                user,
		"PublicAnnouncements": s.visibleAnnouncements(r),
		"CanManage":           user != nil && store.RoleRank(user.Role) >= store.RoleRank(store.RoleAdmin),
		"Customer":            s.currentCustomer(r),
		"CSRF":                csrf,
		"Path":                r.URL.Path,
		"Query":               r.URL.Query().Get("q"),
		"Flash":               r.URL.Query().Get("ok"),
		"Error":               r.URL.Query().Get("err"),
		"Currency":            currency,
		"Version":             version.Version,
		"CurrentYear":         time.Now().Year(),
		"ShopStyle":           style,
		"ShopAccent":          accent,
		"ShopCustomized":      appearance.Style != "default",
		"Appearance":          appearance,
		"AppearancePreview":   r.Context().Value(ctxAppearance) != nil,
	}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	s.renderStatus(w, r, http.StatusOK, name, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, status int, name string, data map[string]any) {
	if s.tmpl == nil {
		http.Error(w, "templates unavailable", http.StatusInternalServerError)
		return
	}
	base := s.baseData(r)
	if strings.HasPrefix(r.URL.Path, "/admin") {
		base["ListReturn"] = listReturn(r.URL.RequestURI(), r.URL.Path)
	}
	for k, v := range data {
		base[k] = v
	}
	base["SEO"] = s.metadata(r, name, status, base)
	var body bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&body, name, base); err != nil {
		s.log.Error("template render failed", "template", name, "error", err, "rid", requestIDFrom(r.Context()))
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.renderStatus(w, r, status, "error", map[string]any{
		"Status":  status,
		"Message": msg,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(payload)
}

func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": map[string]string{"code": code, "message": msg}})
}

func redirect(w http.ResponseWriter, r *http.Request, path string) {
	if strings.HasPrefix(path, "/") {
		http.Redirect(w, r, path, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// flashRedirect sends the operator back with a message in the query string.
func flashRedirect(w http.ResponseWriter, r *http.Request, path, ok, errMsg string) {
	base, err := url.Parse(listReturn(r.FormValue("list_return"), path))
	if err != nil {
		base = &url.URL{Path: path}
	}
	params := base.Query()
	if strings.HasPrefix(path, "/admin/orders/") && r.FormValue("return_to") != "" {
		params.Set("return_to", listReturn(r.FormValue("return_to"), "/admin/orders"))
	}

	q := ""
	if ok != "" {
		q = "ok=" + urlEscape(ok)
	}
	if errMsg != "" {
		if q != "" {
			q += "&"
		}
		q += "err=" + urlEscape(errMsg)
	}
	extra, _ := url.ParseQuery(q)
	for k, vs := range extra {
		params[k] = vs
	}
	q = params.Encode()
	target := path
	if q != "" {
		if strings.Contains(target, "?") {
			target += "&" + q
		} else {
			target += "?" + q
		}
	}
	redirect(w, r, target)
}

func urlEscape(s string) string {
	return template.URLQueryEscaper(s)
}

// pluginViewDecorator converts a manifest into template friendly fields.
func pluginViewDecorator(m plugin.Manifest, rec store.PluginRecord) map[string]any {
	return map[string]any{
		"UID":         m.UID,
		"Name":        m.Name,
		"Version":     m.Version,
		"Kind":        m.Kind,
		"Author":      m.Author,
		"Description": m.Description,
		"Runtime":     m.Runtime.Type,
		"Permissions": m.Permissions,
		"Hooks":       m.Hooks,
		"Settings":    m.Settings,
		"Enabled":     rec.Enabled,
		"Signed":      rec.Signature != "",
		"Source":      rec.Source,
		"Error":       rec.Error,
		"SHA256":      rec.SHA256,
		"InstalledAt": rec.InstalledAt,
		"Homepage":    m.Homepage,
	}
}

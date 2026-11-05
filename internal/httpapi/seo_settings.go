package httpapi

import (
	"fmt"
	"github.com/yiluoiro/card-word/internal/store"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

type seoSettings struct {
	Enabled                                                         bool
	Title, Description, Keywords, ProductTitle, Image, Google, Bing string
}
type seoMetadata struct {
	Title, Description, Keywords, Robots, Canonical, Image, SiteName, Google, Bing string
	Public                                                                         bool
}

func (s *Server) seoSettings() seoSettings {
	return seoSettings{
		Enabled:      s.setting("seo.enabled", "0") == "1",
		Title:        s.setting("seo.title", ""),
		Description:  s.setting("seo.description", ""),
		Keywords:     s.setting("seo.keywords", ""),
		ProductTitle: s.setting("seo.product_title", "{product} · {site}"),
		Image:        s.setting("seo.image", ""),
		Google:       s.setting("seo.google", ""),
		Bing:         s.setting("seo.bing", ""),
	}
}
func validSEOImage(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
func parseSEOSettings(r *http.Request) (seoSettings, error) {
	cfg := seoSettings{Enabled: r.FormValue("seo.enabled") == "1"}
	var validationErr error
	for _, f := range []struct {
		key   string
		value *string
		limit int
	}{
		{"seo.title", &cfg.Title, 120}, {"seo.description", &cfg.Description, 320},
		{"seo.keywords", &cfg.Keywords, 200}, {"seo.product_title", &cfg.ProductTitle, 180},
		{"seo.image", &cfg.Image, 2048}, {"seo.google", &cfg.Google, 200}, {"seo.bing", &cfg.Bing, 200},
	} {
		*f.value = strings.TrimSpace(r.FormValue(f.key))
		if utf8.RuneCountInString(*f.value) > f.limit && validationErr == nil {
			validationErr = fmt.Errorf("字段 %s 超过 %d 个字符，请缩短后重试。", f.key, f.limit)
		}
	}
	if validationErr != nil {
		return cfg, validationErr
	}
	if cfg.ProductTitle == "" {
		cfg.ProductTitle = "{product} · {site}"
	}
	if !strings.Contains(cfg.ProductTitle, "{product}") {
		return cfg, fmt.Errorf("商品标题格式必须包含 {product}，用于区分不同商品。")
	}
	remaining := strings.NewReplacer("{product}", "", "{site}", "").Replace(cfg.ProductTitle)
	if strings.ContainsAny(remaining, "{}") {
		return cfg, fmt.Errorf("标题格式仅支持 {product} 与 {site} 占位符。")
	}
	if cfg.Image != "" && !validSEOImage(cfg.Image) {
		return cfg, fmt.Errorf("分享图片请输入完整的 HTTP 或 HTTPS 图片地址，不包含账号、密码或片段。")
	}
	for _, token := range []string{cfg.Google, cfg.Bing} {
		if strings.ContainsAny(token, "<>\"' \t\r\n") {
			return cfg, fmt.Errorf("站点验证仅填写 content 中的验证码，不要粘贴整段 HTML。")
		}
	}
	return cfg, nil
}
func (s *Server) renderSEOSettings(w http.ResponseWriter, r *http.Request, status int, cfg seoSettings, message string) {
	site := s.setting(store.SettingSiteName, "Card Word")
	title := cfg.Title
	if title == "" {
		title = site
	}
	desc := cfg.Description
	if desc == "" {
		desc = s.setting(store.SettingSiteTagline, "数字商品与礼品卡，付款确认后查看卡密。")
	}
	s.renderStatus(w, r, status, "admin_seo", map[string]any{"Title": "SEO 设置", "SEOSettings": cfg, "SEOBase": strings.TrimRight(s.cfg.BaseURL, "/"), "PreviewTitle": title, "PreviewDescription": desc, "SiteName": site, "Error": message})
}
func (s *Server) handleSEOSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSEOSettings(w, r, 200, s.seoSettings(), "")
}
func (s *Server) handleSEOSettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, 400, "表单格式错误。")
		return
	}
	cfg, err := parseSEOSettings(r)
	if err != nil {
		s.renderSEOSettings(w, r, 422, cfg, err.Error())
		return
	}
	enabled := "0"
	if cfg.Enabled {
		enabled = "1"
	}
	values := map[string]string{"seo.enabled": enabled, "seo.title": cfg.Title, "seo.description": cfg.Description, "seo.keywords": cfg.Keywords, "seo.product_title": cfg.ProductTitle, "seo.image": cfg.Image, "seo.google": cfg.Google, "seo.bing": cfg.Bing}
	if err = s.db.SetSettings(r.Context(), values); err != nil {
		s.renderSEOSettings(w, r, 500, cfg, "保存失败，请稍后重试。")
		return
	}
	if err = s.reloadSettings(r.Context()); err != nil {
		s.renderSEOSettings(w, r, 500, cfg, "设置已保存，但缓存刷新失败，请稍后重新打开页面。")
		return
	}
	s.audit(r, "seo.update", "", "SEO settings updated")
	flashRedirect(w, r, "/admin/seo", "SEO 设置已保存", "")
}
func (s *Server) metadata(r *http.Request, name string, status int, data map[string]any) seoMetadata {
	cfg := s.seoSettings()
	site := s.setting(store.SettingSiteName, "Card Word")
	title, _ := data["Title"].(string)
	if title == "" || title == site {
		title = site
	} else {
		title += " · " + site
	}
	meta := seoMetadata{Title: title, Robots: "noindex, nofollow", SiteName: site}
	// Private templates and failed POSTs never emit share metadata or query tokens.
	if status != 200 || r.Method != http.MethodGet || (name != "home" && name != "product" && name != "stores") {
		return meta
	}
	meta.Public = true
	meta.Description = cfg.Description
	if meta.Description == "" {
		meta.Description = s.setting(store.SettingSiteTagline, "数字商品与礼品卡，付款确认后查看卡密。")
	}
	meta.Keywords = cfg.Keywords
	meta.Image = cfg.Image
	meta.Google, meta.Bing = cfg.Google, cfg.Bing
	base := strings.TrimRight(s.cfg.BaseURL, "/")
	query := url.Values{}
	path := "/"
	validCategory := true
	switch name {
	case "home":
		validCategory = r.URL.Query().Get("cat") == ""
		meta.Title = cfg.Title
		if meta.Title == "" {
			meta.Title = site
		}
		if cat, ok := data["ActiveCat"].(*store.Category); ok && cat != nil && cat.Visible {
			validCategory = true
			query.Set("cat", cat.Slug)
			meta.Title = cat.Name + " · " + site
		}
	case "product":
		product, ok := data["Product"].(*store.Product)
		if !ok || product == nil || product.Status != store.ProductActive {
			meta.Public = false
			return meta
		}
		meta.Title = strings.NewReplacer("{product}", product.Name, "{site}", site).Replace(cfg.ProductTitle)
		if desc := markdownExcerpt(product.Summary); desc != "" {
			meta.Description = desc
		} else if desc = markdownExcerpt(product.Description); desc != "" {
			meta.Description = desc
		}
		path = "/p/" + url.PathEscape(product.Slug)
		if product.CoverPath != "" {
			meta.Image = base + "/media/" + url.PathEscape(product.CoverPath)
		}
	case "stores":
		path = "/stores"
	}
	if pagination, ok := data["Pagination"].(listPagination); ok && pagination.Page > 1 {
		query.Set("page", strconv.Itoa(pagination.Page))
		meta.Title += " · " + strconv.Itoa(pagination.Page)
	}
	meta.Canonical = base + path
	if len(query) > 0 {
		meta.Canonical += "?" + query.Encode()
	}
	if cfg.Enabled && validCategory && s.setting(store.SettingMaintenance, "0") != "1" && r.Context().Value(ctxAppearance) == nil && r.URL.Query().Get("q") == "" {
		meta.Robots = "index, follow"
	}
	return meta
}

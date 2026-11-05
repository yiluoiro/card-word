package httpapi

import (
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/yiluoiro/card-word/internal/store"
)

// handleSitemap lists public, indexable URLs. Admin routes are never included.
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(s.cfg.BaseURL, "/")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	writeURL := func(loc, priority string) {
		b.WriteString("  <url><loc>")
		b.WriteString(html.EscapeString(loc))
		b.WriteString("</loc><priority>")
		b.WriteString(priority)
		b.WriteString("</priority></url>\n")
	}
	if s.setting("seo.enabled", "0") == "1" && s.setting(store.SettingMaintenance, "0") != "1" {
		writeURL(base+"/", "1.0")

		categories, err := s.db.ListCategories(r.Context(), true)
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		count := 2
		for _, c := range categories {
			if count >= 50000 {
				break
			}
			writeURL(base+"/?cat="+url.QueryEscape(c.Slug), "0.6")
			count++
		}
		for offset := 0; count < 50000; offset += 500 {
			products, err := s.db.ListProducts(r.Context(), store.ProductFilter{Status: store.ProductActive, Limit: 500, Offset: offset})
			if err != nil {
				http.Error(w, "unavailable", 503)
				return
			}
			for _, p := range products {
				if count >= 50000 {
					break
				}
				writeURL(base+"/p/"+url.PathEscape(p.Slug), "0.8")
				count++
			}
			if len(products) < 500 {
				break
			}
		}
	}
	b.WriteString("</urlset>\n")

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(b.String()))
}

// handleRobots exposes the crawl policy: storefront yes, admin/API no.
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(s.cfg.BaseURL, "/")
	// Search indexing is enabled by the store owner.
	body := "User-agent: *\nDisallow: /admin\nDisallow: /order\nDisallow: /pay/\nDisallow: /api/\nDisallow: /assets/\n"
	if s.setting("seo.enabled", "0") == "1" && s.setting(store.SettingMaintenance, "0") != "1" {
		body += "Sitemap: " + base + "/sitemap.xml\n"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(body))
}

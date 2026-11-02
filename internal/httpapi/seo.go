package httpapi

import (
	"net/http"
	"strings"

	"github.com/yiluoiro/card-word/internal/store"
)

// handleSitemap lists public, indexable URLs. Admin routes are never included.
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(s.cfg.BaseURL, "/")
	products, err := s.db.ListProducts(r.Context(), store.ProductFilter{Status: store.ProductActive, Limit: 200})
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	categories, _ := s.db.ListCategories(r.Context(), true)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	writeURL := func(loc, priority string) {
		b.WriteString("  <url><loc>")
		b.WriteString(loc)
		b.WriteString("</loc><priority>")
		b.WriteString(priority)
		b.WriteString("</priority></url>\n")
	}
	writeURL(base+"/", "1.0")
	writeURL(base+"/query", "0.3")
	for _, c := range categories {
		writeURL(base+"/?cat="+urlEscape(c.Slug), "0.6")
	}
	for _, p := range products {
		writeURL(base+"/p/"+urlEscape(p.Slug), "0.8")
	}
	b.WriteString("</urlset>\n")

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=600")
	_, _ = w.Write([]byte(b.String()))
}

// handleRobots exposes the crawl policy: storefront yes, admin/API no.
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(s.cfg.BaseURL, "/")
	body := "User-agent: *\n" +
		"Allow: /\n" +
		"Disallow: /admin\n" +
		"Disallow: /order\n" +
		"Disallow: /api/\n" +
		"Disallow: /pay/\n" +
		"Disallow: /assets/\n" +
		"Sitemap: " + base + "/sitemap.xml\n"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=600")
	_, _ = w.Write([]byte(body))
}

package httpapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"github.com/yiluoiro/card-word/internal/store"
	"net/http/httptest"

	"strings"
	"testing"
)

func TestSEOSitemapPaginationAndEscaping(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	if err := s.db.SetSetting(ctx, "seo.enabled", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.reloadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 501; i++ {
		if _, err := s.db.CreateProduct(ctx, store.ProductInput{Name: "商品", Slug: fmt.Sprintf("item-%d", i), Status: store.ProductActive}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.CreateProduct(ctx, store.ProductInput{Name: "草稿", Slug: "draft", Status: store.ProductDraft}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.CreateCategory(ctx, "A & B", "a&b", 0, true); err != nil {
		t.Fatal(err)
	}
	response := storeRequest(s, "GET", "/sitemap.xml", nil, nil, "")
	var sitemap struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(response.Body.Bytes(), &sitemap); err != nil {
		t.Fatal(err)
	}
	if len(sitemap.URLs) != 503 {
		t.Fatalf("lost sitemap pages: %d", len(sitemap.URLs))
	}
	seen := map[string]bool{}
	for _, entry := range sitemap.URLs {
		if seen[entry.Loc] {
			t.Fatal("duplicate URL")
		}
		seen[entry.Loc] = true
	}
	if !seen["http://example.com/p/item-0"] || !seen["http://example.com/p/item-500"] || seen["http://example.com/p/draft"] {
		t.Fatal("wrong public product set")
	}
	if err := s.db.SetSetting(ctx, store.SettingMaintenance, "1"); err != nil {
		t.Fatal(err)
	}
	s.reloadSettings(ctx)
	if strings.Contains(storeRequest(s, "GET", "/sitemap.xml", nil, nil, "").Body.String(), "<loc>") {
		t.Fatal("maintenance published URLs")
	}
}
func TestSEONoIndexForSensitiveTemplates(t *testing.T) {
	s := securityServer(t)
	s.db.SetSetting(context.Background(), "seo.enabled", "1")
	s.reloadSettings(context.Background())
	for _, name := range []string{"order", "query", "pay", "admin_dashboard", "customer_account", "customer_auth", "error"} {
		m := s.metadata(httptest.NewRequest("GET", "/order/test?token=private-token", nil), name, 200, map[string]any{"Title": "标题"})
		if m.Robots != "noindex, nofollow" || m.Public || m.Canonical != "" {
			t.Fatal(name, m)
		}
	}
}

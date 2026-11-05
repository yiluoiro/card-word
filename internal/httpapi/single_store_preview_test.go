package httpapi

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/yiluoiro/card-word/internal/store"
)

// Explicit opt-in export of synthetic pages for browser layout review.
func TestExportSingleStorePreview(t *testing.T) {
	dir := os.Getenv("CARDWORD_COMMUNITY_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set CARDWORD_COMMUNITY_PREVIEW_DIR to export synthetic HTML")
	}
	s := singleStoreServer(t)
	owner, _ := storeAdmin(t, s, store.RoleOwner)
	p, err := s.db.CreateProduct(context.Background(), store.ProductInput{Name: "数字商品示例", Slug: "example", Summary: "查看商品说明，付款后自动获取卡密。", PriceCents: 1000, Currency: "CNY", Status: store.ProductActive, MinQty: 1, MaxQty: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.AddCards(context.Background(), p.ID, []string{"PREVIEW-ONLY-A", "PREVIEW-ONLY-B"}, ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/admin", "/admin/products", "/admin/orders", "/admin/plugins", "/admin/payments", "/admin/appearance", "/p/example"} {
		w := storeRequest(s, "GET", path, nil, owner, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		file := filepath.Join(dir, filepath.FromSlash(path), "index.html")
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, w.Body.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
	}
	assets, err := fs.Sub(staticFS, "web/static")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(dir, "static"), assets); err != nil {
		t.Fatal(err)
	}
}

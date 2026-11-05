package httpapi

import (
	"context"
	"fmt"
	"html"

	"regexp"
	"strings"
	"testing"

	"github.com/yiluoiro/card-word/internal/store"
)

func TestAdminWorkspaceInventoryAndRoles(t *testing.T) {
	s := singleStoreServer(t)
	ctx := context.Background()
	owner, _ := storeAdmin(t, s, store.RoleOwner)
	for i := 0; i < 211; i++ {
		if _, err := s.db.CreateProduct(ctx, store.ProductInput{Name: fmt.Sprintf("Stock %03d", i), Slug: fmt.Sprintf("stock-%03d", i), Status: store.ProductActive, Currency: "CNY"}); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := s.db.CreateProduct(ctx, store.ProductInput{Name: "Ready_%", Slug: "ready", Status: store.ProductActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.AddCards(ctx, ready.ID, []string{"a", "b", "c", "d"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.CreateProduct(ctx, store.ProductInput{Name: "Draft", Slug: "draft", Status: store.ProductDraft}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, want, absent string }{
		{"/admin", "库存不足（共 211 件", "Draft"},
		{"/admin/products?stock=low&status=active&size=20&page=999", "共 211 条", "Ready_%"},
		{"/admin/products?stock=ready", "Ready_%", "Stock 000"},
		{"/admin/products?q=_%25", "Ready_%", "Stock 000"},
	} {
		w := storeRequest(s, "GET", tc.path, nil, owner, "")
		if w.Code != 200 || !strings.Contains(adminVisibleText(w.Body.String()), tc.want) || strings.Contains(w.Body.String(), tc.absent) {
			t.Fatalf("%s status %d missing %s", tc.path, w.Code, tc.want)
		}
	}
	viewer, _ := storeAdmin(t, s, store.RoleViewer)
	for _, path := range []string{"/admin", "/admin/products", fmt.Sprintf("/admin/products/%d", ready.ID), fmt.Sprintf("/admin/products/%d/cards", ready.ID)} {
		w := storeRequest(s, "GET", path, nil, viewer, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		for _, forbidden := range []string{"新建商品", "保存商品", "id=\"bulkProducts\"", "id=\"bulkCards\"", "data-reveal=", "批量导入"} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Fatalf("viewer sees %s on %s", forbidden, path)
			}
		}
	}
	if got := listReturn("/admin/products?stock=low&size=20&page=2", "/admin/products"); !strings.Contains(got, "stock=low") {
		t.Fatal(got)
	}
}

// Customer selection persists across filtering, pagination, export, and reset.

func adminVisibleText(body string) string {
	return html.UnescapeString(regexp.MustCompile("<[^>]*>").ReplaceAllString(body, ""))
}

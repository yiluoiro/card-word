package httpapi_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/store"
)

// TestAdminPagesRender walks every console page so template regressions fail
// the build instead of showing up as a 500 in production.
func TestAdminPagesRender(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	product := h.seedProduct(t)
	if _, err := h.db.CreateUser(ctx, "operator", "super-secret-passphrase", "admin"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := h.db.CreateCoupon(ctx, &store.Coupon{
		Code:   "RENDER10",
		Kind:   store.CouponPercent,
		Value:  10,
		Active: true,
	}); err != nil {
		t.Fatalf("create coupon: %v", err)
	}
	if _, _, err := h.db.CreateAPIToken(ctx, "render", []string{"products:read"}); err != nil {
		t.Fatalf("create token: %v", err)
	}

	_, _ = h.get("/admin/login")
	login := h.postForm("/admin/login", url.Values{
		"username": {"operator"},
		"password": {"super-secret-passphrase"},
	})
	if login.StatusCode != http.StatusSeeOther {
		t.Fatalf("login failed: %d", login.StatusCode)
	}

	// Create an order so the order detail page has data.
	res, err := h.db.CreateOrder(ctx, store.CreateOrderInput{
		ProductID:     product.ID,
		Quantity:      1,
		Email:         "render@example.com",
		PaymentDriver: "manual",
		TTL:           15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	id := strconv.FormatInt(product.ID, 10)

	pages := []string{
		"/admin",
		"/admin/products",
		"/admin/products/0",
		"/admin/products/" + id,
		"/admin/products/" + id + "/cards",
		"/admin/categories",
		"/admin/orders",
		"/admin/orders/" + res.Order.OrderNo,
		"/admin/coupons",
		"/admin/plugins",
		"/admin/settings",
		"/admin/tokens",
		"/admin/audit",
		"/query",
		"/p/" + product.Slug,
		"/order/" + res.Order.OrderNo + "?token=" + res.QueryToken,
	}
	for _, path := range pages {
		resp, body := h.get(path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s => %d", path, resp.StatusCode)
			continue
		}
		if strings.Contains(body, "template error") {
			t.Errorf("GET %s rendered a template error", path)
		}
	}
}

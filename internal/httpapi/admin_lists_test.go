package httpapi

import (
	"context"
	"fmt"
	"github.com/yiluoiro/card-word/internal/store"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAdminListEditsAndRecordProtection(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	owner, csrf := storeAdmin(t, s, store.RoleOwner)
	viewer, vcsrf := storeAdmin(t, s, store.RoleViewer)
	category, err := s.db.CreateCategory(ctx, "Old", "old", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	coupon, err := s.db.CreateCoupon(ctx, &store.Coupon{Code: "SAVE", Kind: "fixed", Value: 100, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	token, plain, err := s.db.CreateAPIToken(ctx, store.TokenInput{Name: "Old token", Scopes: []string{"orders:read"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fmt.Sprintf("/admin/lists/categories/%d", category.ID), fmt.Sprintf("/admin/lists/coupons/%d", coupon.ID), fmt.Sprintf("/admin/lists/tokens/%d", token.ID)} {
		w := storeRequest(s, "GET", path, nil, owner, "")
		if w.Code != 200 {
			t.Fatalf("edit %s: %d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), plain) {
			t.Fatal("plaintext token exposed")
		}
		if w := storeRequest(s, "GET", path, nil, viewer, ""); w.Code != 403 {
			t.Fatal("readonly editor allowed")
		}
	}
	w := storeRequest(s, "POST", "/admin/categories", url.Values{"id": {fmt.Sprint(category.ID)}, "name": {"Changed"}, "slug": {"changed"}, "sort": {"9"}}, owner, csrf)
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
	cats, _, _ := s.db.AdminList(ctx, "categories", store.AdminFilter{Search: "Changed"})
	if len(cats.([]store.Category)) != 1 {
		t.Fatal("category edit absent")
	}
	form := url.Values{"id": {fmt.Sprint(coupon.ID)}, "code": {"SAVE"}, "kind": {"fixed"}, "amount": {"2.50"}, "max_uses": {"4"}, "active": {"1"}}
	if w := storeRequest(s, "POST", "/admin/coupons", form, viewer, vcsrf); w.Code != 403 {
		t.Fatal("readonly mutation")
	}
	if w := storeRequest(s, "POST", "/admin/coupons", form, owner, "wrong"); w.Code != 403 {
		t.Fatal("CSRF bypass")
	}
	if w := storeRequest(s, "POST", "/admin/coupons", form, owner, csrf); w.Code != 303 {
		t.Fatal(w.Code)
	}
	edited, _ := s.db.CouponByID(ctx, coupon.ID)
	if edited.Value != 250 {
		t.Fatal("coupon unchanged")
	}
	tokenPath := fmt.Sprintf("/admin/lists/tokens/%d", token.ID)
	if w := storeRequest(s, "POST", tokenPath, url.Values{"name": {"Restricted"}, "scopes": {"products:read"}, "allowed_ips": {"192.0.2.0/24"}}, owner, csrf); w.Code != 303 {
		t.Fatal(w.Code)
	}
	updated, err := s.db.APITokenByPlain(ctx, plain)
	if err != nil || len(updated.Scopes) != 1 || updated.Scopes[0] != "products:read" {
		t.Fatal("token edit failed")
	}
	if w := storeRequest(s, "POST", tokenPath, url.Values{"name": {"Bad"}, "scopes": {"orders:read"}, "allowed_ips": {"not-an-ip"}}, owner, csrf); w.Code != 422 {
		t.Fatal("invalid IP allowed")
	}
	product := securityProduct(t, s)
	result, err := s.db.CreateOrder(ctx, store.CreateOrderInput{ProductID: product.ID, Quantity: 1, PaymentDriver: "manual", CouponCode: "SAVE", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.DeleteCoupon(ctx, coupon.ID); err == nil {
		t.Fatal("used coupon deleted")
	}
	edited, _ = s.db.CouponByID(ctx, coupon.ID)
	edited.Active = false
	if err = s.db.UpdateCoupon(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.FulfillOrder(ctx, result.Order.OrderNo, "manual", "fixture", result.Order.TotalCents, ""); err != nil {
		t.Fatal(err)
	}
	cards, err := s.db.OrderCards(ctx, result.Order.ID)
	if err != nil || len(cards) == 0 {
		t.Fatal(err)
	}
	c := cards[0]
	if err := s.db.EditCard(ctx, c.ID, product.ID, "bad", "replacement"); err == nil {
		t.Fatal("sold card edited")
	}
	if _, err := s.db.SetCardsStatus(ctx, []int64{c.ID}, store.CardAvailable); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.db.DeleteCards(ctx, []int64{c.ID}); n != 0 {
		t.Fatal("sold card deleted")
	}
	stored, _ := s.db.CardMetadata(ctx, c.ID)
	if stored.Status != store.CardSold {
		t.Fatal("sold card returned to inventory")
	}
	_ = s.db.Audit(ctx, "actor", "review.test", "needle", "192.0.2.2", "details")
	w = storeRequest(s, "GET", "/admin/audit/export.csv?q=needle&action=review.", nil, owner, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "needle") || strings.Contains(w.Body.String(), "coupon.save") {
		t.Fatal("export ignores filters")
	}
}

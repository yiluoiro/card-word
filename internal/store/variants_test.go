package store_test

import (
	"context"
	"github.com/yiluoiro/card-word/internal/store"
	"testing"
)

func TestVariantsPricesStockAndOrderSnapshot(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, 100, 2)
	if err := db.SaveProductVariant(ctx, store.ProductVariant{ProductID: p.ID, Name: "月卡", PriceCents: 1200, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProductVariant(ctx, store.ProductVariant{ProductID: p.ID, Name: "年卡", PriceCents: 9900, Active: true}); err != nil {
		t.Fatal(err)
	}
	variants, err := db.ProductVariants(ctx, p.ID, true)
	if err != nil || len(variants) != 2 || variants[0].Stock != 2 || variants[1].Stock != 0 {
		t.Fatal(variants, err)
	}
	month, year := variants[0], variants[1]
	if _, err = db.AddCards(ctx, p.ID, []string{"unassigned"}, ""); err == nil {
		t.Fatal("variant required")
	}
	if _, err = db.AddVariantCards(ctx, p.ID, year.ID, []string{"YEAR-SECRET"}, ""); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []int64{0, 999999} {
		if _, err = db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, VariantID: variant, Quantity: 1}); err == nil {
			t.Fatal("invalid variant accepted")
		}
	}
	other, err := db.CreateProduct(ctx, store.ProductInput{Name: "other", Slug: "other", PriceCents: 500, Currency: "CNY", Status: store.ProductActive, MinQty: 1, MaxQty: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateOrder(ctx, store.CreateOrderInput{ProductID: other.ID, VariantID: year.ID, Quantity: 1}); err == nil {
		t.Fatal("cross-product variant accepted")
	}
	in := store.CreateOrderInput{ProductID: p.ID, VariantID: year.ID, Quantity: 1, PaymentDriver: "manual", IdempotencyKey: "variant-order-idempotency"}
	result, err := db.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if result.Order.UnitPriceCents != 9900 || result.Order.VariantName != "年卡" {
		t.Fatal(result.Order)
	}
	in.VariantID = month.ID
	if _, err = db.CreateOrder(ctx, in); err == nil {
		t.Fatal("idempotency permits changed variant")
	}
	year.Name = "年度套餐"
	year.PriceCents = 8800
	year.Active = false
	if err = db.SaveProductVariant(ctx, year); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, VariantID: year.ID, Quantity: 1}); err == nil {
		t.Fatal("disabled variant accepted")
	}
	paid, err := db.FulfillOrder(ctx, result.Order.OrderNo, "manual", "year-payment", 9900, "")
	if err != nil || len(paid.Cards) != 1 || paid.Cards[0].Secret != "YEAR-SECRET" {
		t.Fatal(paid, err)
	}
	snapshot, _ := db.OrderByNo(ctx, result.Order.OrderNo)
	if snapshot.VariantName != "年卡" || snapshot.UnitPriceCents != 9900 {
		t.Fatal("snapshot mutated", snapshot)
	}
	variants, _ = db.ProductVariants(ctx, p.ID, false)
	if variants[0].Stock != 2 {
		t.Fatal("wrong variant stock consumed")
	}
	if err = db.DeleteProductVariant(ctx, p.ID, year.ID); err == nil {
		t.Fatal("historical variant deleted")
	}
	rows, total, err := db.AdminList(ctx, "cards", store.AdminFilter{ProductID: p.ID, VariantID: year.ID})
	if err != nil || total != 1 || rows.([]store.CardKey)[0].VariantName != "年度套餐" {
		t.Fatal(rows, total, err)
	}
}
func TestFirstVariantProtectsPendingLegacyOrders(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, 100, 2)
	if _, err := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProductVariant(ctx, store.ProductVariant{ProductID: p.ID, Name: "月卡", PriceCents: 100, Active: true}); err == nil {
		t.Fatal("pending legacy order lost inventory")
	}
	variants, _ := db.ProductVariants(ctx, p.ID, false)
	if len(variants) != 0 {
		t.Fatal("failed variant creation not rolled back")
	}
}

func TestVariantShortStockRetryNeverUsesOtherVariant(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, 100, 1)
	for _, name := range []string{"A", "B"} {
		if err := db.SaveProductVariant(ctx, store.ProductVariant{ProductID: p.ID, Name: name, PriceCents: 100, Active: true}); err != nil {
			t.Fatal(err)
		}
	}
	variants, _ := db.ProductVariants(ctx, p.ID, true)
	a, b := variants[0], variants[1]
	db.AddVariantCards(ctx, p.ID, b.ID, []string{"B-secret"}, "")
	first, err := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, VariantID: a.ID, Quantity: 1, PaymentDriver: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, VariantID: a.ID, Quantity: 1, PaymentDriver: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.FulfillOrder(ctx, first.Order.OrderNo, "manual", "a1", 100, ""); err != nil {
		t.Fatal(err)
	}
	paid, err := db.FulfillOrder(ctx, second.Order.OrderNo, "manual", "a2", 100, "")
	if err != nil || !paid.ShortStock {
		t.Fatal("wrong stock used", paid, err)
	}
	if _, err = db.RetryDelivery(ctx, second.Order.OrderNo); err == nil {
		t.Fatal("retry used another variant")
	}
	db.AddVariantCards(ctx, p.ID, a.ID, []string{"A-restocked"}, "")
	paid, err = db.RetryDelivery(ctx, second.Order.OrderNo)
	if err != nil || len(paid.Cards) != 1 || paid.Cards[0].Secret != "A-restocked" {
		t.Fatal(paid, err)
	}
}

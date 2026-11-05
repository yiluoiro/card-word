package store

import (
	"context"
	"fmt"

	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

var protectedOrderSequence atomic.Uint64

func protectedOrder(t *testing.T, d *DB, amount int64) *Order {
	t.Helper()
	ctx := context.Background()
	slug := fmt.Sprintf("p-%d-%d", time.Now().UnixNano(), protectedOrderSequence.Add(1))
	p, err := d.CreateProduct(ctx, ProductInput{Name: slug, Slug: slug, PriceCents: amount, Currency: "CNY", Status: ProductActive, MinQty: 1, MaxQty: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.AddCards(ctx, p.ID, []string{"KEY"}, ""); err != nil {
		t.Fatal(err)
	}
	result, err := d.CreateOrder(ctx, CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	paid, err := d.FulfillOrder(ctx, result.Order.OrderNo, "manual", "txn-"+slug, amount, "")
	if err != nil {
		t.Fatal(err)
	}
	return paid.Order
}

func singleStoreDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(Options{Path: filepath.Join(t.TempDir(), "store.db"), MasterKey: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

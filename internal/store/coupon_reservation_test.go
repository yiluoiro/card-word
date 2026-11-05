package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/store"
)

func TestLimitedCouponReservesPendingOrders(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, 100, 4)
	c, err := db.CreateCoupon(ctx, &store.Coupon{Code: "ONCE", Kind: store.CouponFixed, Value: 50, MaxUses: 1, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	in := store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual", CouponCode: c.Code}
	var wg sync.WaitGroup
	accepted := make(chan *store.CreateOrderResult, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := db.CreateOrder(ctx, in); err == nil {
				accepted <- r
			}
		}()
	}
	wg.Wait()
	close(accepted)
	if len(accepted) != 1 {
		t.Fatalf("single-use coupon accepted %d simultaneous orders", len(accepted))
	}
	first := <-accepted
	if err := db.CloseOrder(ctx, first.Order.OrderNo); err != nil {
		t.Fatal(err)
	}
	next, err := db.CreateOrder(ctx, in)
	if err != nil {
		t.Fatalf("closed order did not release reservation: %v", err)
	}
	if _, err := db.FulfillOrder(ctx, next.Order.OrderNo, "manual", "coupon-paid", 50, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateOrder(ctx, in); err == nil {
		t.Fatal("paid coupon became available again")
	}
}

func TestExpiredCouponReservationDoesNotBlockNewOrder(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, 100, 2)
	c, err := db.CreateCoupon(ctx, &store.Coupon{Code: "EXPIRE", Kind: store.CouponFixed, Value: 50, MaxUses: 1, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	in := store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual", CouponCode: c.Code, TTL: time.Nanosecond}
	old, err := db.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.TTL = time.Minute
	if _, err := db.CreateOrder(ctx, in); err != nil {
		t.Fatalf("expired reservation not released: %v", err)
	}
	if _, err := db.FulfillOrder(ctx, old.Order.OrderNo, "manual", "late-payment", 50, ""); err == nil {
		t.Fatal("expired reservation was captured")
	}
}

func TestCouponLimitCannotDropBelowReservedUses(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, 100, 2)
	c, err := db.CreateCoupon(ctx, &store.Coupon{Code: "LIMIT", Kind: store.CouponFixed, Value: 10, MaxUses: 2, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual", CouponCode: c.Code}); err != nil {
			t.Fatal(err)
		}
	}
	c.MaxUses = 1
	if err := db.UpdateCoupon(ctx, c); err == nil {
		t.Fatal("limit dropped below committed pending orders")
	}
}

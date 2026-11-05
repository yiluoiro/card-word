package store_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/store"
)

func TestOrderMoneyBoundaries(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, math.MaxInt64, 3)
	if _, err := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 2}); err == nil {
		t.Fatal("overflow accepted")
	}
	for _, percent := range []int64{1, 50, 99, 100} {
		discount, total, err := store.DiscountFor(&store.Coupon{Kind: store.CouponPercent, Value: percent, Active: true}, p.ID, math.MaxInt64, time.Now())
		want := int64(math.MaxInt64)/100*percent + int64(math.MaxInt64)%100*percent/100
		if err != nil || discount != want || total != math.MaxInt64-want {
			t.Fatalf("percent %d: %d %d %v", percent, discount, total, err)
		}
	}
	for _, c := range []*store.Coupon{{Kind: store.CouponPercent, Value: 101, Active: true}, {Kind: store.CouponFixed, Value: -1, Active: true}} {
		if _, _, err := store.DiscountFor(c, p.ID, 100, time.Now()); err == nil {
			t.Fatal("invalid coupon accepted")
		}
	}
	r, err := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r.Order.TotalCents != math.MaxInt64 {
		t.Fatal("maximum safe amount corrupted")
	}
}

func TestOrderIdempotencyConcurrentAndOwnership(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, 100, 1)
	a := mustCustomer(t, db, "alice")
	b := mustCustomer(t, db, "bob")
	// A second writer handle also needs to participate in the serialized check.
	other, err := store.Open(store.Options{Path: db.Path(), MasterKey: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	in := store.CreateOrderInput{ProductID: p.ID, Quantity: 1, CustomerID: a.ID, IdempotencyKey: "shared-idem", PaymentDriver: "manual"}
	const n = 16
	results := make(chan *store.CreateOrderResult, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			handle := db
			if i%2 == 1 {
				handle = other
			}
			r, e := handle.CreateOrder(ctx, in)
			results <- r
			errs <- e
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first *store.Order
	fresh := 0
	for r := range results {
		if first == nil {
			first = r.Order
		}
		if r.Order.ID != first.ID {
			t.Fatal("duplicate order")
		}
		if !r.Replayed {
			fresh++
		} else if r.QueryToken != "" {
			t.Fatal("replay exposed token")
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh=%d", fresh)
	}
	changed := in
	changed.CustomerID = b.ID
	if _, err := db.CreateOrder(ctx, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("cross-customer collision: %v", err)
	}
	changed = in
	changed.Quantity = 2
	if _, err := db.CreateOrder(ctx, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("different request: %v", err)
	}
	if _, err := db.FulfillOrder(ctx, first.OrderNo, "manual", "idem-pay", 100, ""); err != nil {
		t.Fatal(err)
	}
	replay, err := db.CreateOrder(ctx, in)
	if err != nil || !replay.Replayed || replay.Order.ID != first.ID {
		t.Fatalf("replay after stock consumed: %+v %v", replay, err)
	}
	count, err := db.CountOrders(ctx, store.OrderFilter{})
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestPaymentIdentityAndAmounts(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, 100, 3)
	create := func() *store.Order {
		r, e := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual"})
		if e != nil {
			t.Fatal(e)
		}
		return r.Order
	}
	a, b := create(), create()
	for _, amount := range []int64{-100, 0, 99, 101} {
		if _, err := db.FulfillOrder(ctx, a.OrderNo, "manual", "txn", amount, ""); !errors.Is(err, store.ErrInvalidStatus) {
			t.Fatalf("amount %d: %v", amount, err)
		}
	}
	if _, err := db.FulfillOrder(ctx, a.OrderNo, "wrong", "txn", 100, ""); !errors.Is(err, store.ErrInvalidStatus) {
		t.Fatalf("driver: %v", err)
	}
	payments, err := db.ListPayments(ctx, a.ID)
	if err != nil || len(payments) != 0 {
		t.Fatalf("invalid payments persisted: %+v %v", payments, err)
	}
	if _, err := db.FulfillOrder(ctx, a.OrderNo, "manual", "txn", 100, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FulfillOrder(ctx, b.OrderNo, "manual", "txn", 100, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("transaction reuse: %v", err)
	}
	if _, err := db.FulfillOrder(ctx, a.OrderNo, "manual", "different", 100, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("different txn: %v", err)
	}
	if _, err := db.FulfillOrder(ctx, a.OrderNo, "wrong", "txn", 100, ""); err == nil {
		t.Fatal("delivered order skipped provider validation")
	}
	if _, err := db.FulfillOrder(ctx, a.OrderNo, "manual", "txn", 0, ""); err == nil {
		t.Fatal("delivered order skipped amount validation")
	}
	r, err := db.FulfillOrder(ctx, a.OrderNo, "manual", "txn", 100, "")
	if err != nil || !r.AlreadyDone {
		t.Fatalf("replay: %+v %v", r, err)
	}
	payments, err = db.ListPayments(ctx, a.ID)
	if err != nil || len(payments) != 1 {
		t.Fatalf("payments: %+v %v", payments, err)
	}
}

func TestExpiredPendingPaymentAndFreeOrder(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, 0, 2)
	r, err := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`UPDATE orders SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).Unix(), r.Order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.FulfillOrder(ctx, r.Order.OrderNo, "manual", "expired", 0, ""); !errors.Is(err, store.ErrInvalidStatus) {
		t.Fatalf("expired pending: %v", err)
	}
	free, err := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.FulfillOrder(ctx, free.Order.OrderNo, "manual", "free", 0, ""); err != nil {
		t.Fatal(err)
	}
}

func TestShortStockCaptureAndConcurrentRetry(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, 100, 1)
	coupon, err := db.CreateCoupon(ctx, &store.Coupon{Code: "retry", Kind: store.CouponPercent, Value: 10, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	a, err := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual", CouponCode: coupon.Code})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.FulfillOrder(ctx, a.Order.OrderNo, "manual", "stock-first", 100, ""); err != nil {
		t.Fatal(err)
	}
	paid, err := db.FulfillOrder(ctx, b.Order.OrderNo, "manual", "stock-short", 90, "")
	if err != nil || !paid.ShortStock {
		t.Fatalf("short: %+v %v", paid, err)
	}
	if _, err = db.FulfillOrder(ctx, b.Order.OrderNo, "manual", "second-capture", 90, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("recapture: %v", err)
	}
	replay, err := db.FulfillOrder(ctx, b.Order.OrderNo, "manual", "stock-short", 90, "")
	if err != nil || !replay.AlreadyDone || !replay.ShortStock {
		t.Fatalf("short replay: %+v %v", replay, err)
	}
	c, err := db.CouponByID(ctx, coupon.ID)
	if err != nil || c.UsedCount != 1 {
		t.Fatalf("coupon capture: %+v %v", c, err)
	}
	if _, err = db.AddCards(ctx, p.ID, []string{"RETRY-1", "RETRY-2", "RETRY-3", "RETRY-4"}, ""); err != nil {
		t.Fatal(err)
	}
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := db.RetryDelivery(ctx, b.Order.OrderNo); errs <- e }()
	}
	wg.Wait()
	close(errs)
	successes := 0
	for e := range errs {
		if e == nil {
			successes++
		} else if !errors.Is(e, store.ErrInvalidStatus) {
			t.Fatal(e)
		}
	}
	if successes != 1 {
		t.Fatalf("deliveries=%d", successes)
	}
	cards, err := db.OrderCards(ctx, b.Order.ID)
	if err != nil || len(cards) != 1 {
		t.Fatalf("cards: %+v %v", cards, err)
	}
	stock, err := db.StockCount(ctx, p.ID)
	if err != nil || stock != 3 {
		t.Fatalf("stock=%d err=%v", stock, err)
	}
	payments, err := db.ListPayments(ctx, b.Order.ID)
	if err != nil || len(payments) != 1 {
		t.Fatalf("captures: %+v %v", payments, err)
	}
	c, err = db.CouponByID(ctx, coupon.ID)
	if err != nil || c.UsedCount != 1 {
		t.Fatalf("coupon counted twice: %+v %v", c, err)
	}
}

package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

func newDB(t *testing.T) *store.DB {
	t.Helper()
	key, err := cryptoutil.RandomBytes(32)
	if err != nil {
		t.Fatalf("random key: %v", err)
	}
	db, err := store.Open(store.Options{
		Path:         filepath.Join(t.TempDir(), "test.db"),
		ReadPoolSize: 4,
		BusyTimeout:  5 * time.Second,
		MasterKey:    key,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedProduct(t *testing.T, db *store.DB, priceCents int64, cards int) *store.Product {
	t.Helper()
	ctx := context.Background()
	p, err := db.CreateProduct(ctx, store.ProductInput{
		Name:       "测试商品",
		Slug:       "test-product",
		PriceCents: priceCents,
		Currency:   "CNY",
		Status:     store.ProductActive,
		MinQty:     1,
		MaxQty:     10,
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	secrets := make([]string, 0, cards)
	for i := 0; i < cards; i++ {
		secrets = append(secrets, "KEY-"+cryptoutil.Itoa(i))
	}
	if _, err := db.AddCards(ctx, p.ID, secrets, "batch"); err != nil {
		t.Fatalf("add cards: %v", err)
	}
	return p
}

func TestOrderLifecycleIssuesEncryptedCards(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	product := seedProduct(t, db, 1250, 3)

	result, err := db.CreateOrder(ctx, store.CreateOrderInput{
		ProductID:     product.ID,
		Quantity:      2,
		Email:         "buyer@example.com",
		PaymentDriver: "manual",
		TTL:           15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if result.Order.TotalCents != 2500 {
		t.Fatalf("unexpected total: %d", result.Order.TotalCents)
	}
	if result.QueryToken == "" {
		t.Fatal("expected a query token")
	}

	fulfilled, err := db.FulfillOrder(ctx, result.Order.OrderNo, "manual", "txn-1", 2500, "{}")
	if err != nil {
		t.Fatalf("fulfill: %v", err)
	}
	if fulfilled.Order.Status != store.OrderDelivered {
		t.Fatalf("expected delivered, got %s", fulfilled.Order.Status)
	}
	if len(fulfilled.Cards) != 2 {
		t.Fatalf("expected 2 cards, got %d", len(fulfilled.Cards))
	}

	// Cards must be readable afterwards and the stock must drop to 1.
	stock, err := db.StockCount(ctx, product.ID)
	if err != nil {
		t.Fatalf("stock count: %v", err)
	}
	if stock != 1 {
		t.Fatalf("expected stock 1, got %d", stock)
	}
	cards, err := db.OrderCards(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("order cards: %v", err)
	}
	if len(cards) != 2 || cards[0].Secret == "" {
		t.Fatalf("expected decrypted cards, got %+v", cards)
	}

	// Replaying the same transaction must be idempotent.
	again, err := db.FulfillOrder(ctx, result.Order.OrderNo, "manual", "txn-1", 2500, "{}")
	if err != nil {
		t.Fatalf("replay fulfill: %v", err)
	}
	if !again.AlreadyDone {
		t.Fatal("expected duplicate callback to be detected")
	}
	stock, _ = db.StockCount(ctx, product.ID)
	if stock != 1 {
		t.Fatalf("replay consumed extra stock: %d", stock)
	}
}

func TestConcurrentFulfillNeverOversells(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	product := seedProduct(t, db, 100, 5)

	const attempts = 12
	orders := make([]*store.CreateOrderResult, 0, attempts)
	for i := 0; i < attempts; i++ {
		res, err := db.CreateOrder(ctx, store.CreateOrderInput{
			ProductID:     product.ID,
			Quantity:      1,
			PaymentDriver: "manual",
			TTL:           10 * time.Minute,
		})
		if err != nil {
			t.Fatalf("create order %d: %v", i, err)
		}
		orders = append(orders, res)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	delivered := 0
	assigned := map[int64]bool{}
	for i, res := range orders {
		wg.Add(1)
		go func(i int, orderNo string) {
			defer wg.Done()
			result, err := db.FulfillOrder(ctx, orderNo, "manual", "txn-concurrent-"+cryptoutil.Itoa(i), 100, "{}")
			if err != nil {
				t.Logf("fulfill %s: %v", orderNo, err)
				return
			}
			if result.ShortStock || result.Order.Status != store.OrderDelivered {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			delivered++
			for _, c := range result.Cards {
				if assigned[c.ID] {
					t.Errorf("card %d issued twice", c.ID)
				}
				assigned[c.ID] = true
			}
		}(i, res.Order.OrderNo)
	}
	wg.Wait()

	if delivered != 5 {
		t.Fatalf("expected exactly 5 deliveries, got %d", delivered)
	}
	stock, _ := db.StockCount(ctx, product.ID)
	if stock != 0 {
		t.Fatalf("expected zero stock left, got %d", stock)
	}
}

func TestCouponDiscountAndUsage(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	product := seedProduct(t, db, 1000, 5)

	coupon, err := db.CreateCoupon(ctx, &store.Coupon{
		Code:          "save10",
		Kind:          store.CouponPercent,
		Value:         10,
		MaxUses:       1,
		Active:        true,
		MinTotalCents: 500,
	})
	if err != nil {
		t.Fatalf("create coupon: %v", err)
	}
	if coupon.Code != "SAVE10" {
		t.Fatalf("coupon code should be normalized, got %s", coupon.Code)
	}

	res, err := db.CreateOrder(ctx, store.CreateOrderInput{
		ProductID:     product.ID,
		Quantity:      2,
		CouponCode:    "save10",
		PaymentDriver: "manual",
		TTL:           time.Minute,
	})
	if err != nil {
		t.Fatalf("create order with coupon: %v", err)
	}
	if res.Order.DiscountCents != 200 || res.Order.TotalCents != 1800 {
		t.Fatalf("unexpected discount %d total %d", res.Order.DiscountCents, res.Order.TotalCents)
	}
	if _, err := db.FulfillOrder(ctx, res.Order.OrderNo, "manual", "c1", 1800, ""); err != nil {
		t.Fatalf("fulfill: %v", err)
	}
	reloaded, err := db.CouponByCode(ctx, "SAVE10")
	if err != nil {
		t.Fatalf("reload coupon: %v", err)
	}
	if reloaded.UsedCount != 1 {
		t.Fatalf("expected used count 1, got %d", reloaded.UsedCount)
	}
}

func TestExpiredOrderCannotBeFulfilled(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	product := seedProduct(t, db, 100, 2)

	res, err := db.CreateOrder(ctx, store.CreateOrderInput{
		ProductID:     product.ID,
		Quantity:      1,
		PaymentDriver: "manual",
		TTL:           time.Minute,
	})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if err := db.CloseOrder(ctx, res.Order.OrderNo); err != nil {
		t.Fatalf("close order: %v", err)
	}
	if _, err := db.FulfillOrder(ctx, res.Order.OrderNo, "manual", "late-txn", 100, ""); err == nil {
		t.Fatal("expected closed order fulfilment to fail")
	} else if !errors.Is(err, store.ErrInvalidStatus) {
		t.Fatalf("expected invalid status error, got %v", err)
	}
}

func TestOutOfStockPreventsOrder(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	product := seedProduct(t, db, 100, 1)

	if _, err := db.CreateOrder(ctx, store.CreateOrderInput{
		ProductID: product.ID,
		Quantity:  2,
		TTL:       time.Minute,
	}); !errors.Is(err, store.ErrOutOfStock) {
		t.Fatalf("expected out of stock error, got %v", err)
	}
}

func TestAmountMismatchRejected(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	product := seedProduct(t, db, 500, 2)
	res, err := db.CreateOrder(ctx, store.CreateOrderInput{ProductID: product.ID, Quantity: 1, TTL: time.Minute})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if _, err := db.FulfillOrder(ctx, res.Order.OrderNo, "epay", "t1", 1, ""); err == nil {
		t.Fatal("expected amount mismatch rejection")
	}
}

func TestIdempotentOrderCreation(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	product := seedProduct(t, db, 100, 5)
	in := store.CreateOrderInput{
		ProductID:      product.ID,
		Quantity:       1,
		IdempotencyKey: "idem-abc",
		TTL:            time.Minute,
	}
	first, err := db.CreateOrder(ctx, in)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := db.CreateOrder(ctx, in)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if !second.Replayed || second.Order.OrderNo != first.Order.OrderNo {
		t.Fatalf("expected replay of %s, got %s", first.Order.OrderNo, second.Order.OrderNo)
	}
}

package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/store"
)

func mustCustomer(t *testing.T, db *store.DB, name string) *store.Customer {
	t.Helper()
	c, err := db.CreateCustomer(context.Background(), name, "buyer-password-123")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCustomerUniqueAndIndependent(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	c := mustCustomer(t, db, " Alice ")
	if c.ID <= 0 || c.PasswordHash == "buyer-password-123" || c.CreatedAt.IsZero() {
		t.Fatalf("bad customer: %+v", c)
	}
	if _, err := db.CreateCustomer(ctx, "ALICE", "another-password"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	for _, name := range []string{"Alice", " ALICE "} {
		got, err := db.CustomerByUsername(ctx, name)
		if err != nil || got.ID != c.ID {
			t.Fatalf("lookup: %+v %v", got, err)
		}
	}
	if _, err := db.CreateUser(ctx, "alice", "admin-password-123", store.RoleOwner); err != nil {
		t.Fatal(err)
	}
	unicode := mustCustomer(t, db, "Änne")
	got, err := db.CustomerByUsername(ctx, "ÄNNE")
	if err != nil || got.ID != unicode.ID {
		t.Fatalf("unicode lookup: %+v %v", got, err)
	}
	if _, err := db.CreateCustomer(ctx, "änne", "another-password"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("unicode duplicate: %v", err)
	}
}

func TestCustomerSessionReplacementAndPasswordRevocation(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	a := mustCustomer(t, db, "alice")
	b := mustCustomer(t, db, "bob")
	now := time.Now().UTC().Truncate(time.Second)
	session := func(id int64, hash string) *store.CustomerSession {
		return &store.CustomerSession{TokenHash: hash, CustomerID: id, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	}
	for _, s := range []*store.CustomerSession{session(a.ID, "a1"), session(b.ID, "b1"), session(a.ID, "a2")} {
		if err := db.CreateCustomerSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.CustomerSessionByHash(ctx, "a1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old session: %v", err)
	}
	// A conflicting insertion must roll back the preceding deletion.
	if err := db.CreateCustomerSession(ctx, session(a.ID, "b1")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("collision: %v", err)
	}
	if err := db.TouchCustomerSession(ctx, "a2", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	s, err := db.CustomerSessionByHash(ctx, "a2")
	if err != nil || !s.ExpiresAt.Equal(now.Add(time.Hour)) || !s.CreatedAt.Equal(now) || !s.LastSeenAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("touch: %+v %v", s, err)
	}
	if err := db.ChangeCustomerPassword(ctx, a.ID, "stale-hash", "new-password-123"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale password: %v", err)
	}
	if _, err := db.CustomerSessionByHash(ctx, "a2"); err != nil {
		t.Fatal(err)
	}
	if err := db.ChangeCustomerPassword(ctx, a.ID, a.PasswordHash, "new-password-123"); err != nil {
		t.Fatal(err)
	}
	changed, err := db.CustomerByID(ctx, a.ID)
	if err != nil || changed.PasswordHash == a.PasswordHash {
		t.Fatalf("password unchanged: %v", err)
	}
	if _, err := db.CustomerSessionByHash(ctx, "a2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("not revoked: %v", err)
	}
	if _, err := db.CustomerSessionByHash(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteCustomerSession(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CustomerSessionByHash(ctx, "b1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestCustomerOrderOwnership(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	p := seedProduct(t, db, 100, 5)
	a := mustCustomer(t, db, "alice")
	b := mustCustomer(t, db, "bob")
	var owned []int64
	for _, id := range []int64{0, a.ID, b.ID, a.ID} {
		r, err := db.CreateOrder(ctx, store.CreateOrderInput{CustomerID: id, ProductID: p.ID, Quantity: 1, Email: "shared@example.com"})
		if err != nil || r.Order.CustomerID != id {
			t.Fatalf("create: %+v %v", r, err)
		}
		if id == a.ID {
			owned = append(owned, r.Order.ID)
		}
	}
	orders, err := db.ListCustomerOrders(ctx, a.ID, 100, 0)
	if err != nil || len(orders) != 2 || orders[0].ID != owned[1] || orders[1].ID != owned[0] {
		t.Fatalf("ownership: %+v %v", orders, err)
	}
	page, err := db.ListCustomerOrders(ctx, a.ID, 1, 1)
	if err != nil || len(page) != 1 || page[0].ID != owned[0] {
		t.Fatalf("page: %+v %v", page, err)
	}
	guests, err := db.ListCustomerOrders(ctx, 0, 100, 0)
	if err != nil || len(guests) != 0 {
		t.Fatalf("guest listing: %+v %v", guests, err)
	}
	if _, err := db.CreateOrder(ctx, store.CreateOrderInput{CustomerID: 999999, ProductID: p.ID, Quantity: 1}); err == nil {
		t.Fatal("missing customer FK accepted")
	}
}

func TestLegacyCustomerMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(schema)
	start := strings.Index(text, "-- Buyer accounts")
	end := strings.Index(text, "CREATE TABLE IF NOT EXISTS orders (")
	if start < 0 || end < start {
		t.Fatal("schema fixture markers missing")
	}
	text = text[:start] + text[end:]
	text = strings.ReplaceAll(text, "    customer_id      INTEGER REFERENCES customers(id),\n", "")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(text); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`INSERT INTO orders (order_no,product_id,product_name,quantity,unit_price_cents,subtotal_cents,total_cents,email,query_token_hash,created_at,expires_at) VALUES ('LEGACY',1,'old',1,100,100,100,'same@example.com','legacy-hash',1,2)`); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	opts := store.Options{Path: path, MasterKey: make([]byte, 32)}
	db, err := store.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	c := mustCustomer(t, db, "legacy-buyer")
	old, err := db.OrderByNo(ctx, "LEGACY")
	if err != nil || old.CustomerID != 0 || old.Email != "same@example.com" {
		t.Fatalf("legacy order: %+v %v", old, err)
	}
	orders, err := db.ListCustomerOrders(ctx, c.ID, 100, 0)
	if err != nil || len(orders) != 0 {
		t.Fatalf("legacy order claimed: %+v %v", orders, err)
	}
	p := seedProduct(t, db, 100, 1)
	if _, err = db.CreateOrder(ctx, store.CreateOrderInput{CustomerID: c.ID, ProductID: p.ID, Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orders, err = db.ListCustomerOrders(ctx, c.ID, 100, 0)
	if err != nil || len(orders) != 1 {
		t.Fatalf("reopen: %+v %v", orders, err)
	}
	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err = raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_orders_customer'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("index: %d %v", n, err)
	}
	var owner sql.NullInt64
	if err = raw.QueryRow(`SELECT customer_id FROM orders WHERE order_no='LEGACY'`).Scan(&owner); err != nil || owner.Valid {
		t.Fatalf("legacy binding: %+v %v", owner, err)
	}
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

// Order status values.
const (
	OrderPending   = "pending"
	OrderPaid      = "paid"      // payment captured, stock was insufficient
	OrderDelivered = "delivered" // payment captured and cards issued
	OrderClosed    = "closed"
	OrderRefunded  = "refunded"
)

// QueryTokenTTL bounds how long a buyer query credential stays valid. A
// credential that never expires turns every leaked URL into permanent access
// to the purchased card keys.
const QueryTokenTTL = 30 * 24 * time.Hour

// Order is a purchase of quantity units of one product.
type Order struct {
	ID             int64
	OrderNo        string
	ProductID      int64
	ProductName    string
	Quantity       int
	UnitPriceCents int64
	SubtotalCents  int64
	DiscountCents  int64
	TotalCents     int64
	Currency       string
	Email          string
	Contact        string
	Status         string
	PaymentDriver  string
	PaymentTxn     string
	CouponCode     string
	ClientIP       string
	Note           string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	PaidAt         *time.Time
	DeliveredAt    *time.Time
	ClosedAt       *time.Time
}

const orderSelect = `
SELECT id, order_no, product_id, product_name, quantity, unit_price_cents, subtotal_cents,
       discount_cents, total_cents, currency, email, contact, status, payment_driver, payment_txn,
       coupon_code, client_ip, note, created_at, expires_at, paid_at, delivered_at, closed_at
FROM orders`

func scanOrder(row interface{ Scan(...any) error }) (*Order, error) {
	var o Order
	var created, expires int64
	var paid, delivered, closed sql.NullInt64
	if err := row.Scan(&o.ID, &o.OrderNo, &o.ProductID, &o.ProductName, &o.Quantity, &o.UnitPriceCents,
		&o.SubtotalCents, &o.DiscountCents, &o.TotalCents, &o.Currency, &o.Email, &o.Contact, &o.Status,
		&o.PaymentDriver, &o.PaymentTxn, &o.CouponCode, &o.ClientIP, &o.Note,
		&created, &expires, &paid, &delivered, &closed); err != nil {
		return nil, err
	}
	o.CreatedAt, o.ExpiresAt = unixToTime(created), unixToTime(expires)
	o.PaidAt, o.DeliveredAt, o.ClosedAt = nullTime(paid), nullTime(delivered), nullTime(closed)
	return &o, nil
}

// CreateOrderInput describes a checkout attempt.
type CreateOrderInput struct {
	ProductID      int64
	Quantity       int
	Email          string
	Contact        string
	CouponCode     string
	PaymentDriver  string
	ClientIP       string
	IdempotencyKey string
	TTL            time.Duration
}

// CreateOrderResult carries the created order and its query credential.
type CreateOrderResult struct {
	Order      *Order
	QueryToken string
	Replayed   bool
}

// CreateOrder validates stock and coupons, then persists a pending order.
// Stock is not decremented here: cards are only taken when the payment is
// confirmed, so an abandoned checkout never locks inventory.
func (d *DB) CreateOrder(ctx context.Context, in CreateOrderInput) (*CreateOrderResult, error) {
	in.Email = strings.TrimSpace(in.Email)
	if in.Quantity <= 0 {
		return nil, errors.New("store: quantity must be positive")
	}
	if in.TTL <= 0 {
		in.TTL = 15 * time.Minute
	}
	product, err := d.ProductByID(ctx, in.ProductID)
	if err != nil {
		return nil, err
	}
	if product.Status != ProductActive {
		return nil, fmt.Errorf("store: product is not on sale: %w", ErrInvalidStatus)
	}
	if in.Quantity < product.MinQty || in.Quantity > product.MaxQty {
		return nil, fmt.Errorf("store: quantity must be between %d and %d", product.MinQty, product.MaxQty)
	}
	if product.Stock < in.Quantity {
		return nil, ErrOutOfStock
	}
	subtotal := product.PriceCents * int64(in.Quantity)
	discount := int64(0)
	couponCode := ""
	if code := strings.TrimSpace(in.CouponCode); code != "" {
		coupon, err := d.CouponByCode(ctx, code)
		if err != nil {
			return nil, err
		}
		discount, _, err = DiscountFor(coupon, product.ID, subtotal, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		couponCode = coupon.Code
	}
	total := subtotal - discount
	if total < 0 {
		total = 0
	}

	if in.IdempotencyKey != "" {
		if existing, err := d.orderByIdempotency(ctx, in.IdempotencyKey); err == nil {
			return &CreateOrderResult{Order: existing, Replayed: true}, nil
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	queryToken, err := cryptoutil.RandomToken(24)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	orderNo, err := newOrderNo(now)
	if err != nil {
		return nil, err
	}

	var created *Order
	err = d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO orders (order_no, product_id, product_name, quantity, unit_price_cents, subtotal_cents,
				discount_cents, total_cents, currency, email, contact, status, payment_driver, coupon_code,
				query_token_hash, idempotency_key, client_ip, created_at, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			orderNo, product.ID, product.Name, in.Quantity, product.PriceCents, subtotal,
			discount, total, product.Currency, in.Email, in.Contact, OrderPending, in.PaymentDriver,
			couponCode, cryptoutil.TokenHash(queryToken), in.IdempotencyKey, in.ClientIP,
			now.Unix(), now.Add(in.TTL).Unix())
		if err != nil {
			return wrapErr("insert order", err)
		}
		id, _ := res.LastInsertId()
		o, err := scanOrder(tx.QueryRowContext(ctx, orderSelect+` WHERE id = ?`, id))
		if err != nil {
			return wrapErr("reload order", err)
		}
		created = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &CreateOrderResult{Order: created, QueryToken: queryToken}, nil
}

func (d *DB) orderByIdempotency(ctx context.Context, key string) (*Order, error) {
	o, err := scanOrder(d.read.QueryRowContext(ctx, orderSelect+` WHERE idempotency_key = ? ORDER BY id DESC LIMIT 1`, key))
	return o, wrapErr("get order by idempotency key", err)
}

// OrderByNo looks an order up by its public number.
func (d *DB) OrderByNo(ctx context.Context, orderNo string) (*Order, error) {
	o, err := scanOrder(d.read.QueryRowContext(ctx, orderSelect+` WHERE order_no = ?`, strings.TrimSpace(orderNo)))
	return o, wrapErr("get order", err)
}

// OrderByID looks an order up by primary key.
func (d *DB) OrderByID(ctx context.Context, id int64) (*Order, error) {
	o, err := scanOrder(d.read.QueryRowContext(ctx, orderSelect+` WHERE id = ?`, id))
	return o, wrapErr("get order", err)
}

// VerifyOrderQueryToken checks a buyer query credential.
func (d *DB) VerifyOrderQueryToken(ctx context.Context, orderNo, token string) (bool, error) {
	var hash string
	var created int64
	err := d.read.QueryRowContext(ctx,
		`SELECT query_token_hash, created_at FROM orders WHERE order_no = ?`, orderNo).Scan(&hash, &created)
	if err != nil {
		return false, wrapErr("get order token", err)
	}
	if time.Since(unixToTime(created)) > QueryTokenTTL {
		return false, nil
	}
	return cryptoutil.TokenHash(token) == hash, nil
}

// OrderFilter narrows an order listing.
type OrderFilter struct {
	Status string
	Search string
	Email  string
	Limit  int
	Offset int
}

// ListOrders returns orders for the admin console.
func (d *DB) ListOrders(ctx context.Context, f OrderFilter) ([]Order, error) {
	q := orderSelect
	clause, args := ordersWhere(f)
	q += clause
	q += " ORDER BY id DESC"
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q += " LIMIT ? OFFSET ?"
	args = append(args, f.Limit, maxInt(f.Offset, 0))
	rows, err := d.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapErr("list orders", err)
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, wrapErr("scan order", err)
		}
		out = append(out, *o)
	}
	return out, wrapErr("list orders", rows.Err())
}

func ordersWhere(f OrderFilter) (string, []any) {
	var where []string
	var args []any
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Email != "" {
		where = append(where, "email = ?")
		args = append(args, f.Email)
	}
	if f.Search != "" {
		where = append(where, "(order_no LIKE ? OR email LIKE ? OR product_name LIKE ?)")
		like := "%" + f.Search + "%"
		args = append(args, like, like, like)
	}
	if len(where) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// CountOrders returns how many orders match a filter, for pagination.
func (d *DB) CountOrders(ctx context.Context, f OrderFilter) (int, error) {
	clause, args := ordersWhere(f)
	var n int
	err := d.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders`+clause, args...).Scan(&n)
	return n, wrapErr("count orders", err)
}

// DayStat is one bucket of the sales trend chart.
type DayStat struct {
	Date        string
	Orders      int64
	RevenueCents int64
}

// DailyStats aggregates paid orders per day for the last n days, oldest first.
func (d *DB) DailyStats(ctx context.Context, days int) ([]DayStat, error) {
	if days <= 0 || days > 90 {
		days = 7
	}
	rows, err := d.read.QueryContext(ctx, `
		SELECT strftime('%Y-%m-%d', created_at, 'unixepoch') AS day,
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN status IN ('paid','delivered') THEN total_cents ELSE 0 END), 0)
		FROM orders
		WHERE created_at >= ?
		GROUP BY day ORDER BY day ASC`,
		time.Now().UTC().AddDate(0, 0, -days+1).Truncate(24*time.Hour).Unix())
	if err != nil {
		return nil, wrapErr("daily stats", err)
	}
	defer rows.Close()
	byDay := map[string]DayStat{}
	for rows.Next() {
		var s DayStat
		if err := rows.Scan(&s.Date, &s.Orders, &s.RevenueCents); err != nil {
			return nil, wrapErr("scan daily stats", err)
		}
		byDay[s.Date] = s
	}
	if err := rows.Err(); err != nil {
		return nil, wrapErr("daily stats", err)
	}
	out := make([]DayStat, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := time.Now().UTC().AddDate(0, 0, -i).Format("2006-01-02")
		stat, ok := byDay[day]
		if !ok {
			stat = DayStat{Date: day}
		}
		out = append(out, stat)
	}
	return out, nil
}

// OrderStats aggregates counters for the dashboard.
type OrderStats struct {
	TotalOrders   int64
	PaidOrders    int64
	PendingOrders int64
	RevenueCents  int64
	TodayOrders   int64
	TodayRevenue  int64
}

// Stats computes dashboard counters.
func (d *DB) Stats(ctx context.Context) (*OrderStats, error) {
	var s OrderStats
	err := d.read.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN status IN ('paid','delivered') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('paid','delivered') THEN total_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN created_at >= ? AND status IN ('paid','delivered') THEN total_cents ELSE 0 END), 0)
		FROM orders`, startOfTodayUnix(), startOfTodayUnix()).
		Scan(&s.TotalOrders, &s.PaidOrders, &s.PendingOrders, &s.RevenueCents, &s.TodayOrders, &s.TodayRevenue)
	return &s, wrapErr("order stats", err)
}

func startOfTodayUnix() int64 {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()
}

// CloseExpiredOrders closes pending orders past their deadline and returns how
// many were closed.
func (d *DB) CloseExpiredOrders(ctx context.Context) (int64, error) {
	res, err := d.write.ExecContext(ctx,
		`UPDATE orders SET status = 'closed', closed_at = ? WHERE status = 'pending' AND expires_at < ?`,
		nowUnix(), nowUnix())
	if err != nil {
		return 0, wrapErr("close expired orders", err)
	}
	return res.RowsAffected()
}

// CloseOrder closes a single pending order.
func (d *DB) CloseOrder(ctx context.Context, orderNo string) error {
	res, err := d.write.ExecContext(ctx,
		`UPDATE orders SET status = 'closed', closed_at = ? WHERE order_no = ? AND status = 'pending'`,
		nowUnix(), orderNo)
	if err != nil {
		return wrapErr("close order", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInvalidStatus
	}
	return nil
}

// MarkRefunded flags a paid order as refunded without touching the cards: an
// operator decides separately whether the keys stay valid.
func (d *DB) MarkRefunded(ctx context.Context, orderNo, note string) error {
	res, err := d.write.ExecContext(ctx,
		`UPDATE orders SET status = 'refunded', note = ? WHERE order_no = ? AND status IN ('paid','delivered')`,
		note, orderNo)
	if err != nil {
		return wrapErr("refund order", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInvalidStatus
	}
	return nil
}

// SetOrderNote stores an operator note.
func (d *DB) SetOrderNote(ctx context.Context, orderNo, note string) error {
	res, err := d.write.ExecContext(ctx, `UPDATE orders SET note = ? WHERE order_no = ?`, note, orderNo)
	if err != nil {
		return wrapErr("set order note", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FulfillResult reports the outcome of a payment confirmation.
type FulfillResult struct {
	Order       *Order
	Cards       []CardKey
	AlreadyDone bool
	ShortStock  bool
}

// FulfillOrder atomically records a payment and hands out cards. It is the
// single choke point for money-related state changes, so payment providers
// only ever call this method.
func (d *DB) FulfillOrder(ctx context.Context, orderNo, driver, txnID string, amountCents int64, raw string) (*FulfillResult, error) {
	if strings.TrimSpace(orderNo) == "" {
		return nil, errors.New("store: order number required")
	}
	var result FulfillResult
	err := d.tx(ctx, func(tx *sql.Tx) error {
		order, err := scanOrder(tx.QueryRowContext(ctx, orderSelect+` WHERE order_no = ?`, orderNo))
		if err != nil {
			return wrapErr("load order", err)
		}
		if order.Status == OrderDelivered {
			result.Order = order
			result.AlreadyDone = true
			return nil
		}
		if order.Status == OrderPending || order.Status == OrderPaid {
			// continue
		} else if order.Status == OrderClosed {
			return fmt.Errorf("store: order %s is closed, payment needs manual review: %w", orderNo, ErrInvalidStatus)
		} else {
			return fmt.Errorf("store: order %s status %s cannot be fulfilled: %w", orderNo, order.Status, ErrInvalidStatus)
		}
		if amountCents > 0 && amountCents != order.TotalCents {
			return fmt.Errorf("store: amount mismatch for %s: paid %d want %d: %w", orderNo, amountCents, order.TotalCents, ErrInvalidStatus)
		}
		now := nowUnix()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO payments (order_id, driver, txn_id, amount_cents, status, raw, created_at)
			 VALUES (?, ?, ?, ?, 'captured', ?, ?)`,
			order.ID, driver, txnID, amountCents, truncate(raw, 2000), now); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				// The same provider transaction must never settle two orders:
				// that would either duplicate delivery or silently swallow a
				// genuine payment. Only a replay for the same order is benign.
				var existingOrderID int64
				if qerr := tx.QueryRowContext(ctx,
					`SELECT order_id FROM payments WHERE driver = ? AND txn_id = ?`,
					driver, txnID).Scan(&existingOrderID); qerr != nil {
					return wrapErr("inspect duplicate payment", qerr)
				}
				if existingOrderID != order.ID {
					return fmt.Errorf("store: txn %s already settled order %d, refusing order %s: %w",
						txnID, existingOrderID, orderNo, ErrConflict)
				}
				result.Order = order
				result.AlreadyDone = true
				return nil
			}
			return wrapErr("insert payment", err)
		}

		rows, err := tx.QueryContext(ctx,
			`SELECT id, secret FROM card_keys WHERE product_id = ? AND status = 'available' ORDER BY id LIMIT ?`,
			order.ProductID, order.Quantity)
		if err != nil {
			return wrapErr("select stock", err)
		}
		var ids []int64
		secrets := map[int64]string{}
		for rows.Next() {
			var id int64
			var sealed string
			if err := rows.Scan(&id, &sealed); err != nil {
				rows.Close()
				return wrapErr("scan stock", err)
			}
			ids = append(ids, id)
			secrets[id] = sealed
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return wrapErr("select stock", err)
		}

		if len(ids) < order.Quantity {
			// Money arrived but stock ran out: keep the payment, flag the order
			// for manual delivery instead of silently failing the buyer.
			if _, err := tx.ExecContext(ctx,
				`UPDATE orders SET status = 'paid', payment_driver = ?, payment_txn = ?, paid_at = ? WHERE id = ?`,
				driver, txnID, now, order.ID); err != nil {
				return wrapErr("mark paid", err)
			}
			updated, err := scanOrder(tx.QueryRowContext(ctx, orderSelect+` WHERE id = ?`, order.ID))
			if err != nil {
				return wrapErr("reload order", err)
			}
			result.Order = updated
			result.ShortStock = true
			return nil
		}

		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		res, err := tx.ExecContext(ctx,
			`UPDATE card_keys SET status = 'sold', order_id = ?, sold_at = ? WHERE id IN (`+placeholders+`) AND status = 'available'`,
			append([]any{order.ID, now}, toAny(ids)...)...)
		if err != nil {
			return wrapErr("mark cards sold", err)
		}
		affected, _ := res.RowsAffected()
		if int(affected) != len(ids) {
			return fmt.Errorf("store: card allocation raced for order %s: %w", orderNo, ErrOutOfStock)
		}
		var cards []CardKey
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO order_cards (order_id, card_id) VALUES (?, ?)`, order.ID, id); err != nil {
				return wrapErr("link card", err)
			}
			plain, err := cryptoutil.Open(d.key, secrets[id], cardAAD(order.ProductID))
			if err != nil {
				return fmt.Errorf("store: decrypt card %d: %w", id, err)
			}
			sold := unixToTime(now)
			cards = append(cards, CardKey{
				ID: id, ProductID: order.ProductID, Secret: string(plain),
				Status: CardSold, SoldAt: &sold, OrderID: sql.NullInt64{Int64: order.ID, Valid: true},
			})
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE orders SET status = 'delivered', payment_driver = ?, payment_txn = ?, paid_at = ?, delivered_at = ? WHERE id = ?`,
			driver, txnID, now, now, order.ID); err != nil {
			return wrapErr("mark delivered", err)
		}
		if order.CouponCode != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE coupons SET used_count = used_count + 1 WHERE code = ?`, order.CouponCode); err != nil {
				return wrapErr("consume coupon", err)
			}
		}
		updated, err := scanOrder(tx.QueryRowContext(ctx, orderSelect+` WHERE id = ?`, order.ID))
		if err != nil {
			return wrapErr("reload order", err)
		}
		result.Order = updated
		result.Cards = cards
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// RetryDelivery issues cards for an order that was paid but short on stock.
func (d *DB) RetryDelivery(ctx context.Context, orderNo string) (*FulfillResult, error) {
	order, err := d.OrderByNo(ctx, orderNo)
	if err != nil {
		return nil, err
	}
	if order.Status != OrderPaid {
		return nil, ErrInvalidStatus
	}
	var result FulfillResult
	err = d.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, secret FROM card_keys WHERE product_id = ? AND status = 'available' ORDER BY id LIMIT ?`,
			order.ProductID, order.Quantity)
		if err != nil {
			return wrapErr("select stock", err)
		}
		var ids []int64
		secrets := map[int64]string{}
		for rows.Next() {
			var id int64
			var sealed string
			if err := rows.Scan(&id, &sealed); err != nil {
				rows.Close()
				return wrapErr("scan stock", err)
			}
			ids = append(ids, id)
			secrets[id] = sealed
		}
		rows.Close()
		if len(ids) < order.Quantity {
			return ErrOutOfStock
		}
		now := nowUnix()
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		if _, err := tx.ExecContext(ctx,
			`UPDATE card_keys SET status = 'sold', order_id = ?, sold_at = ? WHERE id IN (`+placeholders+`) AND status = 'available'`,
			append([]any{order.ID, now}, toAny(ids)...)...); err != nil {
			return wrapErr("mark cards sold", err)
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT INTO order_cards (order_id, card_id) VALUES (?, ?)`, order.ID, id); err != nil {
				return wrapErr("link card", err)
			}
			plain, err := cryptoutil.Open(d.key, secrets[id], cardAAD(order.ProductID))
			if err != nil {
				return fmt.Errorf("store: decrypt card %d: %w", id, err)
			}
			sold := unixToTime(now)
			result.Cards = append(result.Cards, CardKey{ID: id, ProductID: order.ProductID, Secret: string(plain), Status: CardSold, SoldAt: &sold})
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE orders SET status = 'delivered', delivered_at = ? WHERE id = ?`, now, order.ID); err != nil {
			return wrapErr("mark delivered", err)
		}
		updated, err := scanOrder(tx.QueryRowContext(ctx, orderSelect+` WHERE id = ?`, order.ID))
		if err != nil {
			return wrapErr("reload order", err)
		}
		result.Order = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func toAny(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// PaymentRecord is a captured payment attempt.
type PaymentRecord struct {
	ID          int64
	OrderID     int64
	Driver      string
	TxnID       string
	AmountCents int64
	Status      string
	CreatedAt   time.Time
}

// ListPayments returns captured payments for an order.
func (d *DB) ListPayments(ctx context.Context, orderID int64) ([]PaymentRecord, error) {
	rows, err := d.read.QueryContext(ctx,
		`SELECT id, order_id, driver, txn_id, amount_cents, status, created_at FROM payments WHERE order_id = ? ORDER BY id`, orderID)
	if err != nil {
		return nil, wrapErr("list payments", err)
	}
	defer rows.Close()
	var out []PaymentRecord
	for rows.Next() {
		var p PaymentRecord
		var created int64
		if err := rows.Scan(&p.ID, &p.OrderID, &p.Driver, &p.TxnID, &p.AmountCents, &p.Status, &created); err != nil {
			return nil, wrapErr("scan payment", err)
		}
		p.CreatedAt = unixToTime(created)
		out = append(out, p)
	}
	return out, wrapErr("list payments", rows.Err())
}

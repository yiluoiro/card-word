package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Coupon kinds.
const (
	CouponPercent = "percent"
	CouponFixed   = "fixed"
)

func pendingCouponUses(ctx context.Context, tx *sql.Tx, code string) (int64, error) {
	var count int64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE coupon_code=? AND status='pending' AND expires_at>?`, code, nowUnix()).Scan(&count)
	return count, err
}

// Coupon is a discount code.
type Coupon struct {
	ID            int64
	Code          string
	Kind          string
	Value         int64
	MaxUses       int64
	UsedCount     int64
	MinTotalCents int64
	ProductID     sql.NullInt64
	Active        bool
	ExpiresAt     *time.Time
	CreatedAt     time.Time
}

// CreateCoupon inserts a discount code.
func (d *DB) CreateCoupon(ctx context.Context, c *Coupon) (*Coupon, error) {
	c.Code = strings.ToUpper(strings.TrimSpace(c.Code))
	if c.Code == "" {
		return nil, errors.New("store: coupon code required")
	}
	if c.Kind != CouponPercent && c.Kind != CouponFixed {
		return nil, errors.New("store: coupon kind must be percent or fixed")
	}
	if c.Kind == CouponPercent && (c.Value <= 0 || c.Value > 100) {
		return nil, errors.New("store: percent coupons need a value between 1 and 100")
	}
	if c.Kind == CouponFixed && c.Value <= 0 {
		return nil, errors.New("store: fixed coupons need a positive value")
	}
	var expires any
	if c.ExpiresAt != nil {
		expires = c.ExpiresAt.Unix()
	}
	var productID any
	if c.ProductID.Valid && c.ProductID.Int64 > 0 {
		productID = c.ProductID.Int64
	}
	now := nowUnix()
	res, err := d.write.ExecContext(ctx,
		`INSERT INTO coupons (code, kind, value, max_uses, used_count, min_total_cents, product_id, active, expires_at, created_at)
		 VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		c.Code, c.Kind, c.Value, c.MaxUses, c.MinTotalCents, productID, boolToInt(c.Active), expires, now)
	if err != nil {
		return nil, wrapErr("create coupon", err)
	}
	id, _ := res.LastInsertId()
	return d.CouponByID(ctx, id)
}

// CouponByID fetches a coupon.
func (d *DB) CouponByID(ctx context.Context, id int64) (*Coupon, error) {
	return scanCoupon(d.read.QueryRowContext(ctx,
		`SELECT id, code, kind, value, max_uses, used_count, min_total_cents, product_id, active, expires_at, created_at
		 FROM coupons WHERE id = ?`, id))
}

// CouponByCode resolves an active coupon by its code.
func (d *DB) CouponByCode(ctx context.Context, code string) (*Coupon, error) {
	return scanCoupon(d.read.QueryRowContext(ctx,
		`SELECT id, code, kind, value, max_uses, used_count, min_total_cents, product_id, active, expires_at, created_at
		 FROM coupons WHERE code = ?`, strings.ToUpper(strings.TrimSpace(code))))
}

func scanCoupon(row interface{ Scan(...any) error }) (*Coupon, error) {
	var c Coupon
	var active int
	var expires sql.NullInt64
	var created int64
	if err := row.Scan(&c.ID, &c.Code, &c.Kind, &c.Value, &c.MaxUses, &c.UsedCount, &c.MinTotalCents,
		&c.ProductID, &active, &expires, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, wrapErr("scan coupon", err)
	}
	c.Active = active == 1
	c.ExpiresAt = nullTime(expires)
	c.CreatedAt = unixToTime(created)
	return &c, nil
}

// ListCoupons returns every coupon.
func (d *DB) ListCoupons(ctx context.Context, limit int) ([]Coupon, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := d.read.QueryContext(ctx,
		`SELECT id, code, kind, value, max_uses, used_count, min_total_cents, product_id, active, expires_at, created_at
		 FROM coupons ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, wrapErr("list coupons", err)
	}
	defer rows.Close()
	var out []Coupon
	for rows.Next() {
		var c Coupon
		var active int
		var expires sql.NullInt64
		var created int64
		if err := rows.Scan(&c.ID, &c.Code, &c.Kind, &c.Value, &c.MaxUses, &c.UsedCount, &c.MinTotalCents,
			&c.ProductID, &active, &expires, &created); err != nil {
			return nil, wrapErr("scan coupon", err)
		}
		c.Active = active == 1
		c.ExpiresAt = nullTime(expires)
		c.CreatedAt = unixToTime(created)
		out = append(out, c)
	}
	return out, wrapErr("list coupons", rows.Err())
}

// DeleteCoupon removes a coupon.
func (d *DB) DeleteCoupon(ctx context.Context, id int64) error {
	res, err := d.write.ExecContext(ctx, `DELETE FROM coupons WHERE id = ? AND used_count=0 AND NOT EXISTS(SELECT 1 FROM orders WHERE coupon_code=coupons.code)`, id)
	if err != nil {
		return wrapErr("delete coupon", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DiscountFor validates a coupon against a cart and returns the discount in
// cents together with the resulting total.
func DiscountFor(c *Coupon, productID, subtotalCents int64, now time.Time) (int64, int64, error) {
	if subtotalCents < 0 {
		return 0, subtotalCents, errors.New("store: negative subtotal")
	}
	if c == nil {
		return 0, subtotalCents, nil
	}
	if !c.Active {
		return 0, subtotalCents, fmt.Errorf("store: coupon %s is disabled", c.Code)
	}
	if c.ExpiresAt != nil && now.After(*c.ExpiresAt) {
		return 0, subtotalCents, fmt.Errorf("store: coupon %s expired", c.Code)
	}
	if c.MaxUses > 0 && c.UsedCount >= c.MaxUses {
		return 0, subtotalCents, fmt.Errorf("store: coupon %s is used up", c.Code)
	}
	if c.ProductID.Valid && c.ProductID.Int64 != 0 && c.ProductID.Int64 != productID {
		return 0, subtotalCents, fmt.Errorf("store: coupon %s does not apply to this product", c.Code)
	}
	if subtotalCents < c.MinTotalCents {
		return 0, subtotalCents, fmt.Errorf("store: coupon %s requires at least %d minor units", c.Code, c.MinTotalCents)
	}
	var discount int64
	switch c.Kind {
	case CouponPercent:
		if c.Value <= 0 || c.Value > 100 {
			return 0, subtotalCents, errors.New("store: invalid coupon percentage")
		}
		// Split before multiplying to avoid overflowing monetary values.
		discount = subtotalCents/100*c.Value + subtotalCents%100*c.Value/100
	case CouponFixed:
		if c.Value <= 0 {
			return 0, subtotalCents, errors.New("store: invalid fixed discount")
		}
		discount = c.Value
	default:
		return 0, subtotalCents, errors.New("store: unknown coupon kind")
	}
	if discount > subtotalCents {
		discount = subtotalCents
	}
	return discount, subtotalCents - discount, nil
}

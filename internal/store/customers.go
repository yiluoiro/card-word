package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

// Customer is a buyer account, never an administrative user.
type Customer struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    time.Time
}

// CustomerSession stores only a hash of the buyer's session token.
type CustomerSession struct {
	TokenHash                        string
	CustomerID                       int64
	CreatedAt, LastSeenAt, ExpiresAt time.Time
}

func customerUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func (d *DB) CreateCustomer(ctx context.Context, username, password string) (*Customer, error) {
	username = customerUsername(username)
	if username == "" {
		return nil, errors.New("store: username required")
	}
	hash, err := cryptoutil.HashPassword(password)
	if err != nil {
		return nil, err
	}
	now := nowUnix()
	res, err := d.write.ExecContext(ctx, `INSERT INTO customers (username, password_hash, created_at) VALUES (?, ?, ?)`, username, hash, now)
	if err != nil {
		return nil, wrapErr("create customer", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, wrapErr("customer id", err)
	}
	return &Customer{ID: id, Username: username, PasswordHash: hash, CreatedAt: unixToTime(now)}, nil
}

func scanCustomer(row interface{ Scan(...any) error }) (*Customer, error) {
	var c Customer
	var created int64
	if err := row.Scan(&c.ID, &c.Username, &c.PasswordHash, &created); err != nil {
		return nil, wrapErr("get customer", err)
	}
	c.CreatedAt = unixToTime(created)
	return &c, nil
}

func (d *DB) CustomerByUsername(ctx context.Context, username string) (*Customer, error) {
	return scanCustomer(d.read.QueryRowContext(ctx, `SELECT id, username, password_hash, created_at FROM customers WHERE username = ?`, customerUsername(username)))
}

func (d *DB) CustomerByID(ctx context.Context, id int64) (*Customer, error) {
	return scanCustomer(d.read.QueryRowContext(ctx, `SELECT id, username, password_hash, created_at FROM customers WHERE id = ?`, id))
}

// CreateCustomerSession atomically replaces all previous buyer sessions.
func (d *DB) CreateCustomerSession(ctx context.Context, s *CustomerSession) error {
	if s == nil || s.CustomerID <= 0 || strings.TrimSpace(s.TokenHash) == "" {
		return errors.New("store: customer and session hash required")
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM customer_sessions WHERE customer_id = ?`, s.CustomerID); err != nil {
			return wrapErr("revoke customer sessions", err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO customer_sessions (token_hash, customer_id, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?)`, s.TokenHash, s.CustomerID, s.CreatedAt.Unix(), s.LastSeenAt.Unix(), s.ExpiresAt.Unix())
		return wrapErr("create customer session", err)
	})
}

func (d *DB) CustomerSessionByHash(ctx context.Context, hash string) (*CustomerSession, error) {
	var s CustomerSession
	var created, seen, expires int64
	err := d.read.QueryRowContext(ctx, `SELECT token_hash, customer_id, created_at, last_seen_at, expires_at FROM customer_sessions WHERE token_hash = ?`, hash).Scan(&s.TokenHash, &s.CustomerID, &created, &seen, &expires)
	if err != nil {
		return nil, wrapErr("get customer session", err)
	}
	s.CreatedAt, s.LastSeenAt, s.ExpiresAt = unixToTime(created), unixToTime(seen), unixToTime(expires)
	return &s, nil
}

// TouchCustomerSession never extends the absolute expiry.
func (d *DB) TouchCustomerSession(ctx context.Context, hash string, now time.Time) error {
	_, err := d.write.ExecContext(ctx, `UPDATE customer_sessions SET last_seen_at = ? WHERE token_hash = ?`, now.Unix(), hash)
	return wrapErr("touch customer session", err)
}

func (d *DB) DeleteCustomerSession(ctx context.Context, hash string) error {
	_, err := d.write.ExecContext(ctx, `DELETE FROM customer_sessions WHERE token_hash = ?`, hash)
	return wrapErr("delete customer session", err)
}

// ChangeCustomerPassword compares the previously verified hash inside the same
// transaction as the update and revocation. A stale hash returns ErrConflict.
func (d *DB) ChangeCustomerPassword(ctx context.Context, id int64, oldHash, newPassword string) error {
	hash, err := cryptoutil.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		var current string
		if err := tx.QueryRowContext(ctx, `SELECT password_hash FROM customers WHERE id = ?`, id).Scan(&current); err != nil {
			return wrapErr("get customer password", err)
		}
		if current != oldHash {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE customers SET password_hash = ? WHERE id = ?`, hash, id); err != nil {
			return wrapErr("change customer password", err)
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM customer_sessions WHERE customer_id = ?`, id)
		return wrapErr("revoke customer sessions", err)
	})
}

// ListCustomerOrders only uses the immutable checkout account binding. Guest
// orders and matching email addresses never confer ownership.
func (d *DB) ListCustomerOrders(ctx context.Context, id int64, limit, offset int) ([]Order, error) {
	if id <= 0 {
		return []Order{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.read.QueryContext(ctx, orderSelect+` WHERE customer_id = ? ORDER BY id DESC LIMIT ? OFFSET ?`, id, limit, maxInt(offset, 0))
	if err != nil {
		return nil, wrapErr("list customer orders", err)
	}
	defer rows.Close()
	out := make([]Order, 0)
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, wrapErr("scan customer order", err)
		}
		out = append(out, *o)
	}
	return out, wrapErr("list customer orders", rows.Err())
}

// CustomerOverview intentionally excludes credentials and session details.
type CustomerOverview struct {
	ID                 int64
	Username           string
	CreatedAt          time.Time
	Orders, PaidOrders int
}

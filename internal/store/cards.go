package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

// CardKey is one unit of stock. Secrets are encrypted at rest with
// AES-256-GCM and only decrypted when an order is fulfilled or an admin
// explicitly reveals them.
type CardKey struct {
	ID          int64
	ProductID   int64
	VariantID   int64
	VariantName string
	Masked      string
	Label       string
	Status      string
	OrderID     sql.NullInt64
	SoldAt      *time.Time
	CreatedAt   time.Time
	Secret      string // populated only when decrypted
}

// Card statuses.
const (
	CardAvailable = "available"
	CardSold      = "sold"
	CardDisabled  = "disabled"
)

func cardAAD(productID int64) string { return fmt.Sprintf("card:%d", productID) }

// AddCards stores new card secrets for a product, skipping duplicates and
// blank lines. It returns the number of imported entries.
func (d *DB) AddCards(ctx context.Context, productID int64, secrets []string, label string) (int, error) {
	return d.AddVariantCards(ctx, productID, 0, secrets, label)
}
func (d *DB) AddVariantCards(ctx context.Context, productID, variantID int64, secrets []string, label string) (int, error) {
	if len(secrets) == 0 {
		return 0, nil
	}
	if _, err := d.ProductByID(ctx, productID); err != nil {
		return 0, err
	}
	now := nowUnix()
	imported := 0
	err := d.tx(ctx, func(tx *sql.Tx) error {
		if variantID == 0 {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_variants WHERE product_id=?`, productID).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("请选择卡密所属规格")
			}
		}
		if variantID != 0 {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_variants WHERE id=? AND product_id=?`, variantID, productID).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return ErrNotFound
			}
		}
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO card_keys (product_id, variant_id, secret, masked, label, status, created_at) VALUES (?, ?, ?, ?, ?, 'available', ?)`)
		if err != nil {
			return wrapErr("prepare card insert", err)
		}
		defer stmt.Close()
		seen := map[string]bool{}
		for _, s := range secrets {
			s = strings.TrimSpace(s)
			if s == "" || len(s) > 4096 || seen[s] {
				continue
			}
			seen[s] = true
			sealed, err := cryptoutil.Seal(d.key, []byte(s), cardAAD(productID))
			if err != nil {
				return err
			}
			if _, err := stmt.ExecContext(ctx, productID, variantID, sealed, cryptoutil.MaskSecret(s), label, now); err != nil {
				return wrapErr("insert card", err)
			}
			imported++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return imported, nil
}

// StockCount returns available stock for a product.
func (d *DB) StockCount(ctx context.Context, productID int64) (int, error) {
	var n int
	err := d.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM card_keys WHERE product_id = ? AND status = 'available'`, productID).Scan(&n)
	return n, wrapErr("count stock", err)
}

// CardFilter narrows the card listing.
type CardFilter struct {
	ProductID int64
	Status    string
	Limit     int
	Offset    int
}

func cardWhere(f CardFilter) (string, []any) {
	var where []string
	var args []any
	if f.ProductID > 0 {
		where = append(where, "product_id = ?")
		args = append(args, f.ProductID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if len(where) > 0 {
		return " WHERE " + strings.Join(where, " AND "), args
	}
	return "", args
}

// CountCards counts all matches without decrypting secrets.
func (d *DB) CountCards(ctx context.Context, f CardFilter) (int, error) {
	where, args := cardWhere(f)
	var total int
	err := d.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM card_keys"+where, args...).Scan(&total)
	return total, wrapErr("count cards", err)
}

// ListCards lists card keys without decrypting secrets.
func (d *DB) ListCards(ctx context.Context, f CardFilter) ([]CardKey, error) {
	where, args := cardWhere(f)
	q := `SELECT id, product_id, masked, label, status, order_id, sold_at, created_at,variant_id,COALESCE((SELECT name FROM product_variants WHERE id=card_keys.variant_id),'') FROM card_keys` + where
	q += " ORDER BY id DESC"
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q += " LIMIT ? OFFSET ?"
	args = append(args, f.Limit, maxInt(f.Offset, 0))
	rows, err := d.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapErr("list cards", err)
	}
	defer rows.Close()
	var out []CardKey
	for rows.Next() {
		var c CardKey
		var sold sql.NullInt64
		var created int64
		if err := rows.Scan(&c.ID, &c.ProductID, &c.Masked, &c.Label, &c.Status, &c.OrderID, &sold, &created, &c.VariantID, &c.VariantName); err != nil {
			return nil, wrapErr("scan card", err)
		}
		c.SoldAt = nullTime(sold)
		c.CreatedAt = unixToTime(created)
		out = append(out, c)
	}
	return out, wrapErr("list cards", rows.Err())
}

// RevealCard decrypts a single card secret for an administrator.
func (d *DB) RevealCard(ctx context.Context, id int64) (string, error) {
	var productID int64
	var sealed string
	err := d.read.QueryRowContext(ctx, `SELECT product_id, secret FROM card_keys WHERE id = ?`, id).
		Scan(&productID, &sealed)
	if err != nil {
		return "", wrapErr("get card", err)
	}
	plain, err := cryptoutil.Open(d.key, sealed, cardAAD(productID))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// DisableCard marks a card unusable or restores it.
func (d *DB) SetCardStatus(ctx context.Context, id int64, status string) error {
	switch status {
	case CardAvailable, CardDisabled:
	default:
		return ErrInvalidStatus
	}
	res, err := d.write.ExecContext(ctx, `UPDATE card_keys SET status = ? WHERE id = ? AND status != 'sold' AND order_id IS NULL`, status, id)
	if err != nil {
		return wrapErr("update card", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeUnsoldCards deletes every unused card of a product.
func (d *DB) PurgeUnsoldCards(ctx context.Context, productID int64) (int64, error) {
	res, err := d.write.ExecContext(ctx,
		`DELETE FROM card_keys WHERE product_id = ? AND status = 'available'`, productID)
	if err != nil {
		return 0, wrapErr("purge cards", err)
	}
	return res.RowsAffected()
}

// OrderCards returns the decrypted cards attached to an order.
func (d *DB) OrderCards(ctx context.Context, orderID int64) ([]CardKey, error) {
	rows, err := d.read.QueryContext(ctx,
		`SELECT k.id, k.product_id, k.secret, k.masked, k.label, k.status, k.sold_at
		 FROM order_cards oc JOIN card_keys k ON k.id = oc.card_id
		 WHERE oc.order_id = ? ORDER BY k.id`, orderID)
	if err != nil {
		return nil, wrapErr("list order cards", err)
	}
	defer rows.Close()
	var out []CardKey
	for rows.Next() {
		var c CardKey
		var sealed string
		var sold sql.NullInt64
		if err := rows.Scan(&c.ID, &c.ProductID, &sealed, &c.Masked, &c.Label, &c.Status, &sold); err != nil {
			return nil, wrapErr("scan order card", err)
		}
		c.SoldAt = nullTime(sold)
		plain, err := cryptoutil.Open(d.key, sealed, cardAAD(c.ProductID))
		if err != nil {
			return nil, fmt.Errorf("store: decrypt card %d: %w", c.ID, err)
		}
		c.Secret = string(plain)
		out = append(out, c)
	}
	return out, wrapErr("list order cards", rows.Err())
}

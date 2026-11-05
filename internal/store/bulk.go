package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func idArgs(ids []int64) []any {
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

// SetCardsStatus applies a status to many cards at once.
func (d *DB) SetCardsStatus(ctx context.Context, ids []int64, status string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	switch status {
	case CardAvailable, CardDisabled:
	default:
		return 0, ErrInvalidStatus
	}
	q := `UPDATE card_keys SET status = ? WHERE status != 'sold' AND order_id IS NULL AND id IN (` + placeholders(len(ids)) + `)`
	args := append([]any{status}, idArgs(ids)...)
	res, err := d.write.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, wrapErr("bulk card status", err)
	}
	return res.RowsAffected()
}

// DeleteCards removes unsold cards only: deleting a delivered key would break
// the buyer's order page, so sold rows are skipped by design.
func (d *DB) DeleteCards(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	q := `DELETE FROM card_keys WHERE status <> 'sold' AND order_id IS NULL AND id IN (` + placeholders(len(ids)) + `)`
	res, err := d.write.ExecContext(ctx, q, idArgs(ids)...)
	if err != nil {
		return 0, wrapErr("bulk delete cards", err)
	}
	return res.RowsAffected()
}

// SetProductsStatus applies a status to many products at once.
func (d *DB) SetProductsStatus(ctx context.Context, ids []int64, status string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	switch status {
	case ProductActive, ProductDraft, ProductArchived:
	default:
		return 0, ErrInvalidStatus
	}
	q := `UPDATE products SET status = ?, updated_at = ? WHERE id IN (` + placeholders(len(ids)) + `)`
	args := append([]any{status, nowUnix()}, idArgs(ids)...)
	res, err := d.write.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, wrapErr("bulk product status", err)
	}
	return res.RowsAffected()
}

// ExportCards returns cards with decrypted secrets for an operator export.
// Only administrators reach this path and every call is audited.
func (d *DB) ExportCards(ctx context.Context, f CardFilter) ([]CardKey, error) {
	q := `SELECT id, product_id, secret, masked, label, status, order_id, sold_at, created_at FROM card_keys`
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
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id ASC"
	limit := f.Limit
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	q += " LIMIT ? OFFSET ?"
	args = append(args, limit, maxInt(f.Offset, 0))

	rows, err := d.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapErr("export cards", err)
	}
	defer rows.Close()
	var out []CardKey
	for rows.Next() {
		var c CardKey
		var sealed string
		var sold sql.NullInt64
		var created int64
		if err := rows.Scan(&c.ID, &c.ProductID, &sealed, &c.Masked, &c.Label, &c.Status, &c.OrderID, &sold, &created); err != nil {
			return nil, wrapErr("scan export card", err)
		}
		plain, err := cryptoutil.Open(d.key, sealed, cardAAD(c.ProductID))
		if err != nil {
			return nil, wrapErr("decrypt export card", err)
		}
		c.Secret = string(plain)
		c.SoldAt = nullTime(sold)
		c.CreatedAt = unixToTime(created)
		out = append(out, c)
	}
	return out, wrapErr("export cards", rows.Err())
}

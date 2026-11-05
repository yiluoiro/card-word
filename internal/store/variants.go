package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

type ProductVariant struct {
	ID         int64  `json:"id"`
	ProductID  int64  `json:"product_id"`
	PriceCents int64  `json:"price_cents"`
	Name       string `json:"name"`
	Active     bool   `json:"active"`
	Sort       int    `json:"sort"`
	Stock      int    `json:"stock"`
}

const variantSelect = `SELECT v.id,v.product_id,v.name,v.price_cents,v.active,v.sort,(SELECT COUNT(*) FROM card_keys c WHERE c.product_id=v.product_id AND c.variant_id=v.id AND c.status='available') FROM product_variants v`

func scanVariant(row interface{ Scan(...any) error }) (ProductVariant, error) {
	var v ProductVariant
	err := row.Scan(&v.ID, &v.ProductID, &v.Name, &v.PriceCents, &v.Active, &v.Sort, &v.Stock)
	return v, err
}
func (d *DB) ProductVariants(ctx context.Context, productID int64, active bool) ([]ProductVariant, error) {
	q := variantSelect + ` WHERE v.product_id=?`
	if active {
		q += ` AND v.active=1`
	}
	q += ` ORDER BY v.sort,v.id`
	rows, err := d.read.QueryContext(ctx, q, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProductVariant{}
	for rows.Next() {
		v, err := scanVariant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (d *DB) SaveProductVariant(ctx context.Context, v ProductVariant) error {
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" || utf8.RuneCountInString(v.Name) > 80 || v.PriceCents < 0 || v.PriceCents > 1_000_000_000_00 {
		return errors.New("规格名称最多 80 字，价格须为有效非负金额")
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		if v.ID == 0 {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_variants WHERE product_id=?`, v.ProductID).Scan(&count); err != nil {
				return err
			}
			if count >= 100 {
				return errors.New("每个商品最多 100 个规格")
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO product_variants(product_id,name,price_cents,active,sort) VALUES(?,?,?,?,?)`, v.ProductID, v.Name, v.PriceCents, v.Active, v.Sort)
			if err != nil {
				return err
			}
			// Preserve legacy stock as the first specification; legacy pending orders retain variant 0.
			if count == 0 {
				id, err := res.LastInsertId()
				if err != nil {
					return err
				}
				var pending int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE product_id=? AND variant_id=0 AND status IN ('pending','paid')`, v.ProductID).Scan(&pending); err != nil {
					return err
				}
				if pending > 0 {
					return errors.New("此商品还有默认规格待付款或待补发订单，请先处理后再添加规格")
				}
				_, err = tx.ExecContext(ctx, `UPDATE card_keys SET variant_id=? WHERE product_id=? AND variant_id=0 AND status!='sold'`, id, v.ProductID)
				return err
			}
			return nil
		}
		res, err := tx.ExecContext(ctx, `UPDATE product_variants SET name=?,price_cents=?,active=?,sort=? WHERE id=? AND product_id=?`, v.Name, v.PriceCents, v.Active, v.Sort, v.ID, v.ProductID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
func (d *DB) DeleteProductVariant(ctx context.Context, productID, id int64) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM card_keys WHERE variant_id=?)+(SELECT COUNT(*) FROM orders WHERE variant_id=?)`, id, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errors.New("规格已有库存或订单，请停用以保留记录")
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM product_variants WHERE id=? AND product_id=?`, id, productID)
		if err != nil {
			return err
		}
		n64, _ := res.RowsAffected()
		if n64 == 0 {
			return ErrNotFound
		}
		return nil
	})
}

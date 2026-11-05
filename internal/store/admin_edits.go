package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"net"
	"strings"
)

func (d *DB) UpdateCoupon(ctx context.Context, c *Coupon) error {
	c.Code = strings.ToUpper(strings.TrimSpace(c.Code))
	if c.Code == "" || len(c.Code) > 80 || c.MaxUses < 0 || c.MinTotalCents < 0 || c.Value <= 0 || (c.Kind != CouponPercent && c.Kind != CouponFixed) || (c.Kind == CouponPercent && c.Value > 100) {
		return errors.New("优惠码或金额无效")
	}
	var expires, product any
	if c.ExpiresAt != nil {
		expires = c.ExpiresAt.Unix()
	}
	if c.ProductID.Valid && c.ProductID.Int64 > 0 {
		product = c.ProductID.Int64
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		var used int64
		var code string
		if err := tx.QueryRowContext(ctx, "SELECT used_count,code FROM coupons WHERE id=?", c.ID).Scan(&used, &code); err != nil {
			return err
		}
		reserved, err := pendingCouponUses(ctx, tx, code)
		if err != nil {
			return err
		}
		if c.MaxUses > 0 && (c.MaxUses < used || c.MaxUses-used < reserved) {
			return errors.New("次数上限不能低于已使用和待付款订单占用的次数")
		}
		var linked int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders WHERE coupon_code=?", code).Scan(&linked); err != nil {
			return err
		}
		if (used > 0 || linked > 0) && code != c.Code {
			return errors.New("已使用的优惠码不能更名，可停用后新增")
		}
		return changed(tx.ExecContext(ctx, "UPDATE coupons SET code=?,kind=?,value=?,max_uses=?,min_total_cents=?,product_id=?,active=?,expires_at=? WHERE id=?", c.Code, c.Kind, c.Value, c.MaxUses, c.MinTotalCents, product, c.Active, expires, c.ID))
	})
}
func (d *DB) UpdateToken(ctx context.Context, id int64, in TokenInput) error {
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 100 || len(in.AllowedIPs) > 50 || len(in.Scopes) == 0 {
		return errors.New("请检查名称、权限与来源地址")
	}
	allowed := map[string]bool{"products:read": true, "orders:read": true, "orders:write": true, "cards:read": true, "cards:write": true, "*": true}
	for _, scope := range in.Scopes {
		if !allowed[scope] {
			return errors.New("无效的 API 权限")
		}
	}
	for _, ip := range in.AllowedIPs {
		if net.ParseIP(ip) == nil {
			if _, _, e := net.ParseCIDR(ip); e != nil {
				return errors.New("IP 或 CIDR 格式不正确")
			}
		}
	}
	var exp any
	if in.ExpiresAt != nil {
		exp = in.ExpiresAt.Unix()
	}
	return changed(d.write.ExecContext(ctx, "UPDATE api_tokens SET name=?,scopes=?,allowed_ips=?,expires_at=? WHERE id=? AND revoked_at IS NULL", strings.TrimSpace(in.Name), strings.Join(in.Scopes, ","), strings.Join(in.AllowedIPs, ","), exp, id))
}
func (d *DB) CardMetadata(ctx context.Context, id int64) (CardKey, error) {
	var c CardKey
	var created int64
	var sold sql.NullInt64
	err := d.read.QueryRowContext(ctx, "SELECT id,product_id,masked,label,status,order_id,sold_at,created_at FROM card_keys WHERE id=?", id).Scan(&c.ID, &c.ProductID, &c.Masked, &c.Label, &c.Status, &c.OrderID, &sold, &created)
	c.CreatedAt = unixToTime(created)
	c.SoldAt = nullTime(sold)
	return c, err
}
func (d *DB) EditCard(ctx context.Context, id, product int64, label, secret string) error {
	if len([]rune(label)) > 100 || len(secret) > 4096 {
		return errors.New("标签或卡密过长")
	}
	if secret == "" {
		return changed(d.write.ExecContext(ctx, "UPDATE card_keys SET label=? WHERE id=? AND product_id=? AND status!='sold' AND order_id IS NULL", label, id, product))
	}
	cipher, e := cryptoutil.Seal(d.key, []byte(secret), cardAAD(product))
	if e != nil {
		return e
	}
	return changed(d.write.ExecContext(ctx, "UPDATE card_keys SET label=?,secret=?,masked=? WHERE id=? AND product_id=? AND status!='sold' AND order_id IS NULL", label, cipher, cryptoutil.MaskSecret(secret), id, product))
}

func changed(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

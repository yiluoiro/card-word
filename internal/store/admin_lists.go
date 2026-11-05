package store

import (
	"context"
	"database/sql"
	"errors"

	"strings"
	"time"
)

type AdminFilter struct {
	Search, Status, Kind, From, To, Currency string
	Page, Size                               int
	RecordID                                 int64
	ProductID                                int64
	CustomerID                               int64
	VariantID                                int64
}

func adminSource(kind string) (string, string, string, error) {
	switch kind {
	case "customers":
		return "customers", "SELECT id,username,created_at,(SELECT COUNT(*) FROM orders o WHERE o.customer_id=customers.id),(SELECT COUNT(*) FROM orders o WHERE o.customer_id=customers.id AND o.status IN ('paid','delivered')) FROM customers", "id DESC", nil
	case "categories":
		return "categories", "SELECT id,name,slug,sort,visible,created_at FROM categories", "sort,id", nil
	case "coupons":
		return "coupons", "SELECT id,code,kind,value,max_uses,used_count,min_total_cents,product_id,active,expires_at,created_at FROM coupons", "id DESC", nil
	case "users":
		return "users", "SELECT " + userColumns + " FROM users", "id DESC", nil
	case "tokens":
		return "api_tokens", "SELECT id,name,prefix,scopes,created_at,last_used_at,revoked_at,expires_at,allowed_ips FROM api_tokens", "id DESC", nil
	case "plugins":
		return "plugins", pluginSelect, "id DESC", nil

	case "orders":
		return "orders", orderSelect, "id DESC", nil
	case "audit":
		return "audit_logs", "SELECT id,actor,action,target,ip,detail,created_at FROM audit_logs", "id DESC", nil
	case "cards":
		return "card_keys", "SELECT id,product_id,masked,label,status,order_id,sold_at,created_at,variant_id,COALESCE((SELECT name FROM product_variants WHERE id=card_keys.variant_id),'') FROM card_keys", "id DESC", nil
	}
	return "", "", "", errors.New("unknown list")
}

// Only fixed identifiers enter SQL. Search values are bound, with literal wildcard handling.
func adminWhere(kind string, f AdminFilter) (string, []any) {
	where := []string{"1=1"}
	args := []any{}
	add := func(q string, v ...any) { where = append(where, q); args = append(args, v...) }
	if f.RecordID > 0 {
		if kind == "cases" || kind == "complaints" {
			add("c.id=?", f.RecordID)
		} else {
			add("id=?", f.RecordID)
		}
	}
	column, search := "created_at", ""
	switch kind {
	case "customers":
		search = "username"
		if f.Status == "ordered" {
			add("EXISTS (SELECT 1 FROM orders o WHERE o.customer_id=customers.id)")
		}
		if f.Status == "new" {
			add("NOT EXISTS (SELECT 1 FROM orders o WHERE o.customer_id=customers.id)")
		}
	case "categories":
		search = "name || ' ' || slug"
		if f.Status != "" {
			add("visible=?", f.Status == "visible")
		}
	case "coupons":
		search = "code"
		if f.Status == "enabled" {
			add("active=1 AND (expires_at IS NULL OR expires_at>?)", nowUnix())
		} else if f.Status == "disabled" {
			add("active=0")
		} else if f.Status == "expired" {
			add("expires_at<=?", nowUnix())
		}
		if f.Kind != "" {
			add("kind=?", f.Kind)
		}
	case "users":
		search = "username"
		if f.Status != "" {
			add("disabled=?", f.Status == "disabled")
		}
		if f.Kind != "" {
			add("role=?", f.Kind)
		}
	case "tokens":
		search = "name || ' ' || prefix"
		switch f.Status {
		case "enabled":
			add("revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?)", nowUnix())
		case "revoked":
			add("revoked_at IS NOT NULL")
		case "expired":
			add("revoked_at IS NULL AND expires_at<=?", nowUnix())
		}
		if f.Kind != "" {
			add("(',' || scopes || ',') LIKE ?", "%,"+f.Kind+",%")
		}
	case "plugins":
		search = "name || ' ' || uid"
		column = "installed_at"
		if f.Status != "" {
			add("enabled=?", f.Status == "enabled")
		}
		if f.Kind != "" {
			add("kind=?", f.Kind)
		}

	case "orders":
		if f.CustomerID > 0 {
			add("customer_id=?", f.CustomerID)
		}
		search = "order_no || ' ' || product_name || ' ' || email"
		if f.Status != "" {
			add("status=?", f.Status)
		}
		if f.Currency != "" {
			add("currency=?", f.Currency)
		}
	case "audit":
		search = "actor || ' ' || action || ' ' || target || ' ' || ip"
		if f.Status != "" {
			add("action LIKE ?", f.Status+"%")
		}
	case "cards":
		search = "CAST(id AS TEXT) || ' ' || label || ' ' || masked || ' ' || COALESCE((SELECT name FROM product_variants WHERE id=card_keys.variant_id),'')"
		if f.VariantID > 0 {
			add("variant_id=?", f.VariantID)
		}
		add("product_id=?", f.ProductID)
		if f.Status != "" {
			add("status=?", f.Status)
		}
	}
	if f.Search != "" && search != "" {
		escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(f.Search)
		add("("+search+") LIKE ? ESCAPE '!'", "%"+escaped+"%")
	}
	if column != "" {
		if t, e := time.Parse("2006-01-02", f.From); e == nil {
			add(column+">=?", t.Unix())
		}
		if t, e := time.Parse("2006-01-02", f.To); e == nil {
			add(column+"<?", t.AddDate(0, 0, 1).Unix())
		}
	}
	return " WHERE " + strings.Join(where, " AND "), args
}
func (d *DB) AdminList(ctx context.Context, kind string, f AdminFilter) (any, int, error) {
	table, selectSQL, order, err := adminSource(kind)
	if err != nil {
		return nil, 0, err
	}
	clause, args := adminWhere(kind, f)
	var total int
	if err = d.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Size != 20 && f.Size != 50 && f.Size != 100 && f.Size != 5000 {
		f.Size = 50
	}
	pages := max(1, (total+f.Size-1)/f.Size)
	f.Page = min(max(1, f.Page), pages)
	rows, err := d.read.QueryContext(ctx, selectSQL+clause+" ORDER BY "+order+" LIMIT ? OFFSET ?", append(args, f.Size, (f.Page-1)*f.Size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	// A typed slice is retained even for an empty result, so templates render their empty states.
	switch kind {
	case "customers":
		out := []CustomerOverview{}
		for rows.Next() {
			var c CustomerOverview
			var created int64
			if e := rows.Scan(&c.ID, &c.Username, &created, &c.Orders, &c.PaidOrders); e != nil {
				return nil, 0, e
			}
			c.CreatedAt = unixToTime(created)
			out = append(out, c)
		}
		return out, total, rows.Err()
	case "categories":
		out := []Category{}
		for rows.Next() {
			var c Category
			var created int64
			err = rows.Scan(&c.ID, &c.Name, &c.Slug, &c.Sort, &c.Visible, &created)
			if err != nil {
				return nil, 0, err
			}
			c.CreatedAt = unixToTime(created)
			out = append(out, c)
		}
		return out, total, rows.Err()
	case "coupons":
		out := []Coupon{}
		for rows.Next() {
			c, e := scanCoupon(rows)
			if e != nil {
				return nil, 0, e
			}
			out = append(out, *c)
		}
		return out, total, rows.Err()
	case "users":
		out := []User{}
		for rows.Next() {
			c, e := scanUser(rows)
			if e != nil {
				return nil, 0, e
			}
			out = append(out, *c)
		}
		return out, total, rows.Err()
	case "plugins":
		out := []PluginRecord{}
		for rows.Next() {
			c, e := scanPlugin(rows)
			if e != nil {
				return nil, 0, e
			}
			out = append(out, *c)
		}
		return out, total, rows.Err()

	case "orders":
		out := []Order{}
		for rows.Next() {
			c, e := scanOrder(rows)
			if e != nil {
				return nil, 0, e
			}
			out = append(out, *c)
		}
		return out, total, rows.Err()
	case "tokens":
		out := []APIToken{}
		for rows.Next() {
			var c APIToken
			var scopes, ips string
			var created int64
			var last, revoked, expires sql.NullInt64
			err = rows.Scan(&c.ID, &c.Name, &c.Prefix, &scopes, &created, &last, &revoked, &expires, &ips)
			if err != nil {
				return nil, 0, err
			}
			c.Scopes = splitScopes(scopes)
			c.AllowedIPs = splitScopes(ips)
			c.CreatedAt = unixToTime(created)
			c.LastUsedAt = nullTime(last)
			c.RevokedAt = nullTime(revoked)
			c.ExpiresAt = nullTime(expires)
			out = append(out, c)
		}
		return out, total, rows.Err()

	case "audit":
		out := []AuditEntry{}
		for rows.Next() {
			var c AuditEntry
			var created int64
			err = rows.Scan(&c.ID, &c.Actor, &c.Action, &c.Target, &c.IP, &c.Detail, &created)
			if err != nil {
				return nil, 0, err
			}
			c.CreatedAt = unixToTime(created)
			out = append(out, c)
		}
		return out, total, rows.Err()
	case "cards":
		out := []CardKey{}
		for rows.Next() {
			var c CardKey
			var created int64
			var sold sql.NullInt64
			err = rows.Scan(&c.ID, &c.ProductID, &c.Masked, &c.Label, &c.Status, &c.OrderID, &sold, &created, &c.VariantID, &c.VariantName)
			if err != nil {
				return nil, 0, err
			}
			c.CreatedAt = unixToTime(created)
			c.SoldAt = nullTime(sold)
			out = append(out, c)
		}
		return out, total, rows.Err()
	}
	return nil, 0, errors.New("unhandled list")
}

// AdminCount uses the same predicates as the destination list without loading records.
func (d *DB) AdminCount(ctx context.Context, kind string, f AdminFilter) (int, error) {
	table, _, _, err := adminSource(kind)
	if err != nil {
		return 0, err
	}
	where, args := adminWhere(kind, f)
	var count int
	err = d.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+where, args...).Scan(&count)
	return count, err
}

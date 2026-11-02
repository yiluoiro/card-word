package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
)

// Category groups products in the storefront.
type Category struct {
	ID        int64
	Name      string
	Slug      string
	Sort      int
	Visible   bool
	CreatedAt time.Time
}

// Product is a sellable item backed by a pool of card keys.
type Product struct {
	ID           int64
	CategoryID   sql.NullInt64
	Name         string
	Slug         string
	Summary      string
	Description  string
	PriceCents   int64
	Currency     string
	Status       string
	MinQty       int
	MaxQty       int
	Sort         int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Stock        int
	CategoryName string
}

// Product status values.
const (
	ProductActive   = "active"
	ProductDraft    = "draft"
	ProductArchived = "archived"
)

// Slugify builds an URL friendly slug that keeps unicode letters, so Chinese
// product names still produce readable links.
func Slugify(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var sb strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			sb.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if !lastDash && sb.Len() > 0 {
				sb.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(sb.String(), "-")
	if out == "" {
		out = "item"
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

// ---------- categories ----------

// CreateCategory inserts a category.
func (d *DB) CreateCategory(ctx context.Context, name, slug string, sort int, visible bool) (*Category, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("store: category name required")
	}
	if slug == "" {
		slug = Slugify(name)
	}
	now := nowUnix()
	res, err := d.write.ExecContext(ctx,
		`INSERT INTO categories (name, slug, sort, visible, created_at) VALUES (?, ?, ?, ?, ?)`,
		name, slug, sort, boolToInt(visible), now)
	if err != nil {
		return nil, wrapErr("create category", err)
	}
	id, _ := res.LastInsertId()
	return &Category{ID: id, Name: name, Slug: slug, Sort: sort, Visible: visible, CreatedAt: unixToTime(now)}, nil
}

// UpdateCategory modifies a category.
func (d *DB) UpdateCategory(ctx context.Context, c *Category) error {
	res, err := d.write.ExecContext(ctx,
		`UPDATE categories SET name = ?, slug = ?, sort = ?, visible = ? WHERE id = ?`,
		c.Name, c.Slug, c.Sort, boolToInt(c.Visible), c.ID)
	if err != nil {
		return wrapErr("update category", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCategory removes a category, detaching its products.
func (d *DB) DeleteCategory(ctx context.Context, id int64) error {
	res, err := d.write.ExecContext(ctx, `DELETE FROM categories WHERE id = ?`, id)
	if err != nil {
		return wrapErr("delete category", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListCategories returns categories ordered for display.
func (d *DB) ListCategories(ctx context.Context, onlyVisible bool) ([]Category, error) {
	q := `SELECT id, name, slug, sort, visible, created_at FROM categories`
	if onlyVisible {
		q += ` WHERE visible = 1`
	}
	q += ` ORDER BY sort ASC, id ASC`
	rows, err := d.read.QueryContext(ctx, q)
	if err != nil {
		return nil, wrapErr("list categories", err)
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		var c Category
		var visible int
		var created int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.Sort, &visible, &created); err != nil {
			return nil, wrapErr("scan category", err)
		}
		c.Visible = visible == 1
		c.CreatedAt = unixToTime(created)
		out = append(out, c)
	}
	return out, wrapErr("list categories", rows.Err())
}

// CategoryBySlug resolves a category by slug.
func (d *DB) CategoryBySlug(ctx context.Context, slug string) (*Category, error) {
	var c Category
	var visible int
	var created int64
	err := d.read.QueryRowContext(ctx,
		`SELECT id, name, slug, sort, visible, created_at FROM categories WHERE slug = ?`, slug).
		Scan(&c.ID, &c.Name, &c.Slug, &c.Sort, &visible, &created)
	if err != nil {
		return nil, wrapErr("get category", err)
	}
	c.Visible = visible == 1
	c.CreatedAt = unixToTime(created)
	return &c, nil
}

// ---------- products ----------

const productSelect = `
SELECT p.id, p.category_id, p.name, p.slug, p.summary, p.description, p.price_cents, p.currency,
       p.status, p.min_qty, p.max_qty, p.sort, p.created_at, p.updated_at,
       (SELECT COUNT(*) FROM card_keys c WHERE c.product_id = p.id AND c.status = 'available') AS stock,
       COALESCE(cat.name, '') AS category_name
FROM products p
LEFT JOIN categories cat ON cat.id = p.category_id`

func scanProduct(row interface{ Scan(...any) error }) (*Product, error) {
	var p Product
	var created, updated int64
	if err := row.Scan(&p.ID, &p.CategoryID, &p.Name, &p.Slug, &p.Summary, &p.Description,
		&p.PriceCents, &p.Currency, &p.Status, &p.MinQty, &p.MaxQty, &p.Sort, &created, &updated,
		&p.Stock, &p.CategoryName); err != nil {
		return nil, err
	}
	p.CreatedAt, p.UpdatedAt = unixToTime(created), unixToTime(updated)
	return &p, nil
}

// ProductInput describes a product create/update request.
type ProductInput struct {
	Name        string
	Slug        string
	CategoryID  int64
	Summary     string
	Description string
	PriceCents  int64
	Currency    string
	Status      string
	MinQty      int
	MaxQty      int
	Sort        int
}

// CreateProduct inserts a product.
func (d *DB) CreateProduct(ctx context.Context, in ProductInput) (*Product, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("store: product name required")
	}
	if in.PriceCents < 0 {
		return nil, errors.New("store: price must not be negative")
	}
	if in.Slug == "" {
		in.Slug = Slugify(in.Name)
	}
	if in.Currency == "" {
		in.Currency = "CNY"
	}
	if in.Status == "" {
		in.Status = ProductActive
	}
	if in.MinQty <= 0 {
		in.MinQty = 1
	}
	if in.MaxQty < in.MinQty {
		in.MaxQty = in.MinQty
	}
	now := nowUnix()
	var catID any
	if in.CategoryID > 0 {
		catID = in.CategoryID
	}
	res, err := d.write.ExecContext(ctx,
		`INSERT INTO products (category_id, name, slug, summary, description, price_cents, currency,
			status, min_qty, max_qty, sort, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		catID, strings.TrimSpace(in.Name), in.Slug, in.Summary, in.Description, in.PriceCents,
		in.Currency, in.Status, in.MinQty, in.MaxQty, in.Sort, now, now)
	if err != nil {
		return nil, wrapErr("create product", err)
	}
	id, _ := res.LastInsertId()
	return d.ProductByID(ctx, id)
}

// UpdateProduct updates a product.
func (d *DB) UpdateProduct(ctx context.Context, id int64, in ProductInput) (*Product, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("store: product name required")
	}
	if in.Slug == "" {
		in.Slug = Slugify(in.Name)
	}
	if in.Currency == "" {
		in.Currency = "CNY"
	}
	if in.MinQty <= 0 {
		in.MinQty = 1
	}
	if in.MaxQty < in.MinQty {
		in.MaxQty = in.MinQty
	}
	var catID any
	if in.CategoryID > 0 {
		catID = in.CategoryID
	}
	res, err := d.write.ExecContext(ctx,
		`UPDATE products SET category_id = ?, name = ?, slug = ?, summary = ?, description = ?,
			price_cents = ?, currency = ?, status = ?, min_qty = ?, max_qty = ?, sort = ?, updated_at = ?
		 WHERE id = ?`,
		catID, strings.TrimSpace(in.Name), in.Slug, in.Summary, in.Description, in.PriceCents,
		in.Currency, in.Status, in.MinQty, in.MaxQty, in.Sort, nowUnix(), id)
	if err != nil {
		return nil, wrapErr("update product", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.ProductByID(ctx, id)
}

// DeleteProduct removes a product and its card pool.
func (d *DB) DeleteProduct(ctx context.Context, id int64) error {
	res, err := d.write.ExecContext(ctx, `DELETE FROM products WHERE id = ?`, id)
	if err != nil {
		return wrapErr("delete product", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ProductByID fetches a product including live stock.
func (d *DB) ProductByID(ctx context.Context, id int64) (*Product, error) {
	p, err := scanProduct(d.read.QueryRowContext(ctx, productSelect+` WHERE p.id = ?`, id))
	return p, wrapErr("get product", err)
}

// ProductBySlug fetches a product by slug.
func (d *DB) ProductBySlug(ctx context.Context, slug string) (*Product, error) {
	p, err := scanProduct(d.read.QueryRowContext(ctx, productSelect+` WHERE p.slug = ?`, slug))
	return p, wrapErr("get product", err)
}

// ProductFilter narrows a product listing.
type ProductFilter struct {
	Status     string
	CategoryID int64
	Search     string
	Limit      int
	Offset     int
}

// ListProducts returns products for the storefront or the admin table.
func (d *DB) ListProducts(ctx context.Context, f ProductFilter) ([]Product, error) {
	q := productSelect
	var where []string
	var args []any
	if f.Status != "" {
		where = append(where, "p.status = ?")
		args = append(args, f.Status)
	}
	if f.CategoryID > 0 {
		where = append(where, "p.category_id = ?")
		args = append(args, f.CategoryID)
	}
	if f.Search != "" {
		where = append(where, "(p.name LIKE ? OR p.slug LIKE ?)")
		like := "%" + f.Search + "%"
		args = append(args, like, like)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY p.sort ASC, p.id DESC"
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q += " LIMIT ? OFFSET ?"
	args = append(args, f.Limit, maxInt(f.Offset, 0))
	rows, err := d.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapErr("list products", err)
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, wrapErr("scan product", err)
		}
		out = append(out, *p)
	}
	return out, wrapErr("list products", rows.Err())
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

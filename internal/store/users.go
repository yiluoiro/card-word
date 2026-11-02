package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

// User is an administrative account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

// CountUsers returns the number of accounts.
func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := d.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, wrapErr("count users", err)
}

// CreateUser inserts a new account with an argon2id password hash.
func (d *DB) CreateUser(ctx context.Context, username, password, role string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("store: username required")
	}
	if role == "" {
		role = "admin"
	}
	hash, err := cryptoutil.HashPassword(password)
	if err != nil {
		return nil, err
	}
	now := nowUnix()
	res, err := d.write.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		username, hash, role, now)
	if err != nil {
		return nil, wrapErr("create user", err)
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, PasswordHash: hash, Role: role, CreatedAt: unixToTime(now)}, nil
}

// UpdateUserPassword replaces the password hash of an account.
func (d *DB) UpdateUserPassword(ctx context.Context, id int64, password string) error {
	hash, err := cryptoutil.HashPassword(password)
	if err != nil {
		return err
	}
	res, err := d.write.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return wrapErr("update password", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created int64
	var last sql.NullInt64
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &created, &last); err != nil {
		return nil, err
	}
	u.CreatedAt = unixToTime(created)
	u.LastLoginAt = nullTime(last)
	return &u, nil
}

// UserByUsername looks an account up case-insensitively.
func (d *DB) UserByUsername(ctx context.Context, username string) (*User, error) {
	row := d.read.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, created_at, last_login_at FROM users WHERE username = ?`,
		strings.TrimSpace(username))
	u, err := scanUser(row)
	return u, wrapErr("get user", err)
}

// UserByID fetches an account by primary key.
func (d *DB) UserByID(ctx context.Context, id int64) (*User, error) {
	row := d.read.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, created_at, last_login_at FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	return u, wrapErr("get user", err)
}

// TouchLastLogin records a successful sign-in.
func (d *DB) TouchLastLogin(ctx context.Context, id int64) error {
	_, err := d.write.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, nowUnix(), id)
	return wrapErr("touch last login", err)
}

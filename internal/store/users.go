package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

// Roles, ordered from least to most privileged.
const (
	RoleViewer = "viewer"
	RoleAdmin  = "admin"
	RoleOwner  = "owner"
)

// RoleRank maps a role to a comparable weight. Unknown roles rank lowest.
func RoleRank(role string) int {
	switch role {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// ValidRole reports whether role is one of the known roles.
func ValidRole(role string) bool { return RoleRank(role) > 0 }

// User is an administrative account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	TOTPSecret   string // encrypted at rest
	TOTPEnabled  bool
	Disabled     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

const userColumns = `id, username, password_hash, role, totp_secret, totp_enabled, disabled,
	created_at, last_login_at, updated_at`

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
		role = RoleAdmin
	}
	if !ValidRole(role) {
		return nil, errors.New("store: unknown role")
	}
	hash, err := cryptoutil.HashPassword(password)
	if err != nil {
		return nil, err
	}
	now := nowUnix()
	res, err := d.write.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		username, hash, role, now, now)
	if err != nil {
		return nil, wrapErr("create user", err)
	}
	id, _ := res.LastInsertId()
	return d.UserByID(ctx, id)
}

// UpdateUserPassword replaces the password hash of an account.
func (d *DB) UpdateUserPassword(ctx context.Context, id int64, password string) error {
	hash, err := cryptoutil.HashPassword(password)
	if err != nil {
		return err
	}
	res, err := d.write.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, nowUnix(), id)
	if err != nil {
		return wrapErr("update password", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserRole changes the role of an account.
func (d *DB) SetUserRole(ctx context.Context, id int64, role string) error {
	if !ValidRole(role) {
		return errors.New("store: unknown role")
	}
	res, err := d.write.ExecContext(ctx,
		`UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, role, nowUnix(), id)
	if err != nil {
		return wrapErr("set role", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserDisabled blocks or restores sign-in for an account.
func (d *DB) SetUserDisabled(ctx context.Context, id int64, disabled bool) error {
	res, err := d.write.ExecContext(ctx,
		`UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`, boolToInt(disabled), nowUnix(), id)
	if err != nil {
		return wrapErr("set disabled", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUser removes an account and its sessions.
func (d *DB) DeleteUser(ctx context.Context, id int64) error {
	res, err := d.write.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return wrapErr("delete user", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountOwners returns how many enabled owner accounts exist, so the last owner
// cannot be demoted or deleted.
func (d *DB) CountOwners(ctx context.Context) (int, error) {
	var n int
	err := d.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = ? AND disabled = 0`, RoleOwner).Scan(&n)
	return n, wrapErr("count owners", err)
}

// SetTOTP stores the (already encrypted) TOTP secret and its enabled flag.
func (d *DB) SetTOTP(ctx context.Context, id int64, secret string, enabled bool) error {
	res, err := d.write.ExecContext(ctx,
		`UPDATE users SET totp_secret = ?, totp_enabled = ?, updated_at = ? WHERE id = ?`,
		secret, boolToInt(enabled), nowUnix(), id)
	if err != nil {
		return wrapErr("set totp", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearTOTP disables two-factor authentication for an account.
func (d *DB) ClearTOTP(ctx context.Context, id int64) error {
	return d.SetTOTP(ctx, id, "", false)
}

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created, updated int64
	var last sql.NullInt64
	var totpEnabled, disabled int
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &totpEnabled,
		&disabled, &created, &last, &updated); err != nil {
		return nil, err
	}
	u.TOTPEnabled = totpEnabled == 1
	u.Disabled = disabled == 1
	u.CreatedAt = unixToTime(created)
	u.UpdatedAt = unixToTime(updated)
	u.LastLoginAt = nullTime(last)
	return &u, nil
}

// UserByUsername looks an account up case-insensitively.
func (d *DB) UserByUsername(ctx context.Context, username string) (*User, error) {
	row := d.read.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ?`, strings.TrimSpace(username))
	u, err := scanUser(row)
	return u, wrapErr("get user", err)
}

// UserByID fetches an account by primary key.
func (d *DB) UserByID(ctx context.Context, id int64) (*User, error) {
	row := d.read.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	return u, wrapErr("get user", err)
}

// ListUsers returns every account, owners first.
func (d *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := d.read.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY role DESC, username ASC`)
	if err != nil {
		return nil, wrapErr("list users", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, wrapErr("scan user", err)
		}
		out = append(out, *u)
	}
	return out, wrapErr("list users", err)
}

// TouchLastLogin records a successful sign-in.
func (d *DB) TouchLastLogin(ctx context.Context, id int64) error {
	_, err := d.write.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, nowUnix(), id)
	return wrapErr("touch last login", err)
}

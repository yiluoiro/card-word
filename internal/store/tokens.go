package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

// APIToken is a machine credential for the REST API.
type APIToken struct {
	ID         int64
	Name       string
	Prefix     string
	Scopes     []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// CreateAPIToken issues a token and returns its plaintext exactly once.
func (d *DB) CreateAPIToken(ctx context.Context, name string, scopes []string) (*APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", errors.New("store: token name required")
	}
	secret, err := cryptoutil.RandomToken(32)
	if err != nil {
		return nil, "", err
	}
	plain := "cwk_" + secret
	prefix := plain[:12]
	now := nowUnix()
	res, err := d.write.ExecContext(ctx,
		`INSERT INTO api_tokens (name, prefix, token_hash, scopes, created_at) VALUES (?, ?, ?, ?, ?)`,
		name, prefix, cryptoutil.TokenHash(plain), strings.Join(scopes, ","), now)
	if err != nil {
		return nil, "", wrapErr("create api token", err)
	}
	id, _ := res.LastInsertId()
	return &APIToken{ID: id, Name: name, Prefix: prefix, Scopes: scopes, CreatedAt: unixToTime(now)}, plain, nil
}

// APITokenByPlain resolves a plaintext token and returns it with its scopes.
func (d *DB) APITokenByPlain(ctx context.Context, plain string) (*APIToken, error) {
	var t APIToken
	var scopes string
	var created int64
	var lastUsed, revoked sql.NullInt64
	err := d.read.QueryRowContext(ctx,
		`SELECT id, name, prefix, scopes, created_at, last_used_at, revoked_at
		 FROM api_tokens WHERE token_hash = ?`, cryptoutil.TokenHash(strings.TrimSpace(plain))).
		Scan(&t.ID, &t.Name, &t.Prefix, &scopes, &created, &lastUsed, &revoked)
	if err != nil {
		return nil, wrapErr("get api token", err)
	}
	if revoked.Valid {
		return nil, ErrNotFound
	}
	t.Scopes = splitScopes(scopes)
	t.CreatedAt = unixToTime(created)
	t.LastUsedAt = nullTime(lastUsed)
	return &t, nil
}

func splitScopes(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// TouchAPIToken records token usage.
func (d *DB) TouchAPIToken(ctx context.Context, id int64) error {
	_, err := d.write.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, nowUnix(), id)
	return wrapErr("touch api token", err)
}

// ListAPITokens lists tokens without secrets.
func (d *DB) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := d.read.QueryContext(ctx,
		`SELECT id, name, prefix, scopes, created_at, last_used_at, revoked_at FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, wrapErr("list api tokens", err)
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		var scopes string
		var created int64
		var lastUsed, revoked sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &scopes, &created, &lastUsed, &revoked); err != nil {
			return nil, wrapErr("scan api token", err)
		}
		t.Scopes = splitScopes(scopes)
		t.CreatedAt = unixToTime(created)
		t.LastUsedAt = nullTime(lastUsed)
		t.RevokedAt = nullTime(revoked)
		out = append(out, t)
	}
	return out, wrapErr("list api tokens", rows.Err())
}

// RevokeAPIToken disables a token.
func (d *DB) RevokeAPIToken(ctx context.Context, id int64) error {
	res, err := d.write.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, nowUnix(), id)
	if err != nil {
		return wrapErr("revoke api token", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

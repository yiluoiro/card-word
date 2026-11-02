package store

import (
	"context"
	"database/sql"
	"time"
)

// Session is a server side administrative session. Only the SHA-256 hash of
// the cookie value is stored.
type Session struct {
	ID         string
	UserID     int64
	TokenHash  string
	CSRFToken  string
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// CreateSession stores a new session.
func (d *DB) CreateSession(ctx context.Context, s *Session) error {
	_, err := d.write.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, token_hash, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.UserID, s.TokenHash, s.CSRFToken, s.IP, s.UserAgent,
		s.CreatedAt.Unix(), s.LastSeenAt.Unix(), s.ExpiresAt.Unix())
	return wrapErr("create session", err)
}

// SessionByTokenHash resolves a session by cookie hash.
func (d *DB) SessionByTokenHash(ctx context.Context, hash string) (*Session, error) {
	row := d.read.QueryRowContext(ctx,
		`SELECT id, user_id, token_hash, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at
		 FROM sessions WHERE token_hash = ?`, hash)
	var s Session
	var created, seen, expires int64
	if err := row.Scan(&s.ID, &s.UserID, &s.TokenHash, &s.CSRFToken, &s.IP, &s.UserAgent, &created, &seen, &expires); err != nil {
		return nil, wrapErr("get session", err)
	}
	s.CreatedAt, s.LastSeenAt, s.ExpiresAt = unixToTime(created), unixToTime(seen), unixToTime(expires)
	return &s, nil
}

// TouchSession updates the idle timestamp and absolute expiry.
func (d *DB) TouchSession(ctx context.Context, id string, seen, expires time.Time) error {
	_, err := d.write.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
		seen.Unix(), expires.Unix(), id)
	return wrapErr("touch session", err)
}

// DeleteSession removes a single session (logout).
func (d *DB) DeleteSession(ctx context.Context, id string) error {
	_, err := d.write.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return wrapErr("delete session", err)
}

// DeleteUserSessions removes every session of a user except keepID.
func (d *DB) DeleteUserSessions(ctx context.Context, userID int64, keepID string) error {
	if keepID == "" {
		_, err := d.write.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
		return wrapErr("delete user sessions", err)
	}
	_, err := d.write.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID)
	return wrapErr("delete user sessions", err)
}

// PurgeExpiredSessions deletes sessions past their expiry.
func (d *DB) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := d.write.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, nowUnix())
	if err != nil {
		return 0, wrapErr("purge sessions", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

var _ = sql.ErrNoRows

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

// ChallengeTTL is how long a password-verified login waits for its second
// factor before the operator has to start over.
const ChallengeTTL = 5 * time.Minute

// MaxChallengeAttempts caps guessing against a single challenge.
const MaxChallengeAttempts = 5

// LoginChallenge is a pending second factor step.
type LoginChallenge struct {
	ID        string
	UserID    int64
	Attempts  int
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateChallenge issues a fresh challenge and returns its plaintext token.
func (d *DB) CreateChallenge(ctx context.Context, userID int64) (string, string, error) {
	token, err := cryptoutil.RandomToken(32)
	if err != nil {
		return "", "", err
	}
	id, err := cryptoutil.RandomToken(16)
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	_, err = d.write.ExecContext(ctx,
		`INSERT INTO login_challenges (id, user_id, token_hash, attempts, created_at, expires_at)
		 VALUES (?, ?, ?, 0, ?, ?)`,
		id, userID, cryptoutil.TokenHash(token), now.Unix(), now.Add(ChallengeTTL).Unix())
	if err != nil {
		return "", "", wrapErr("create challenge", err)
	}
	return id, token, nil
}

// ChallengeByToken resolves a live challenge by its plaintext token.
func (d *DB) ChallengeByToken(ctx context.Context, token string) (*LoginChallenge, error) {
	var c LoginChallenge
	var created, expires int64
	err := d.read.QueryRowContext(ctx,
		`SELECT id, user_id, attempts, created_at, expires_at FROM login_challenges WHERE token_hash = ?`,
		cryptoutil.TokenHash(token)).
		Scan(&c.ID, &c.UserID, &c.Attempts, &created, &expires)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, wrapErr("get challenge", err)
	}
	c.CreatedAt, c.ExpiresAt = unixToTime(created), unixToTime(expires)
	return &c, nil
}

// BumpChallengeAttempts increments the counter and returns the new value.
func (d *DB) BumpChallengeAttempts(ctx context.Context, id string) (int, error) {
	if _, err := d.write.ExecContext(ctx,
		`UPDATE login_challenges SET attempts = attempts + 1 WHERE id = ?`, id); err != nil {
		return 0, wrapErr("bump challenge", err)
	}
	var attempts int
	if err := d.read.QueryRowContext(ctx,
		`SELECT attempts FROM login_challenges WHERE id = ?`, id).Scan(&attempts); err != nil {
		return 0, wrapErr("read challenge attempts", err)
	}
	return attempts, nil
}

// DeleteChallenge removes a redeemed or abandoned challenge.
func (d *DB) DeleteChallenge(ctx context.Context, id string) error {
	_, err := d.write.ExecContext(ctx, `DELETE FROM login_challenges WHERE id = ?`, id)
	return wrapErr("delete challenge", err)
}

// PurgeExpiredChallenges cleans up stale challenges.
func (d *DB) PurgeExpiredChallenges(ctx context.Context) (int64, error) {
	res, err := d.write.ExecContext(ctx,
		`DELETE FROM login_challenges WHERE expires_at < ?`, nowUnix())
	if err != nil {
		return 0, wrapErr("purge challenges", err)
	}
	return res.RowsAffected()
}

// ReplaceRecoveryCodes rotates the recovery code set of an account.
func (d *DB) ReplaceRecoveryCodes(ctx context.Context, userID int64, codes []string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
			return wrapErr("clear recovery codes", err)
		}
		for _, code := range codes {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO recovery_codes (user_id, code_hash) VALUES (?, ?)`,
				userID, cryptoutil.TokenHash(cryptoutil.NormalizeRecoveryCode(code))); err != nil {
				return wrapErr("insert recovery code", err)
			}
		}
		return nil
	})
}

// CountRecoveryCodes returns how many unused codes remain.
func (d *DB) CountRecoveryCodes(ctx context.Context, userID int64) (int, error) {
	var n int
	err := d.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&n)
	return n, wrapErr("count recovery codes", err)
}

// ConsumeRecoveryCode burns a code if it matches, returning whether it was valid.
func (d *DB) ConsumeRecoveryCode(ctx context.Context, userID int64, code string) (bool, error) {
	hash := cryptoutil.TokenHash(cryptoutil.NormalizeRecoveryCode(code))
	res, err := d.write.ExecContext(ctx,
		`UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
		nowUnix(), userID, hash)
	if err != nil {
		return false, wrapErr("consume recovery code", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

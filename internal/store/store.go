// Package store implements the persistence layer of card word on top of
// SQLite in WAL mode. It exposes a single writer connection and a pool of
// readers, which is the recommended topology for SQLite under concurrency.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver, no cgo required

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

//go:embed schema.sql
var schemaFS embed.FS

// Sentinel errors returned by the store.
var (
	ErrNotFound      = errors.New("store: not found")
	ErrConflict      = errors.New("store: conflict")
	ErrOutOfStock    = errors.New("store: out of stock")
	ErrInvalidStatus = errors.New("store: invalid status transition")
)

// DB bundles the write connection and the read pool.
type DB struct {
	write *sql.DB
	read  *sql.DB
	path  string
	key   []byte
	stamp int64
}

// Options tunes the database connection.
type Options struct {
	Path         string
	ReadPoolSize int
	BusyTimeout  time.Duration
	MasterKey    []byte
}

// Open prepares the database file, applies the schema and returns a handle.
func Open(opts Options) (*DB, error) {
	if opts.Path == "" {
		return nil, errors.New("store: empty database path")
	}
	if opts.ReadPoolSize <= 0 {
		opts.ReadPoolSize = 8
	}
	if opts.BusyTimeout <= 0 {
		opts.BusyTimeout = 10 * time.Second
	}
	if len(opts.MasterKey) != 32 {
		return nil, errors.New("store: master key must be 32 bytes")
	}
	if dir := filepath.Dir(opts.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("store: create data directory: %w", err)
		}
	}

	db := &DB{path: opts.Path, key: opts.MasterKey, stamp: time.Now().Unix()}

	write, err := openPool(opts.Path, 1, opts.BusyTimeout, true)
	if err != nil {
		return nil, err
	}
	read, err := openPool(opts.Path, opts.ReadPoolSize, opts.BusyTimeout, false)
	if err != nil {
		_ = write.Close()
		return nil, err
	}
	db.write, db.read = write, read

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func openPool(path string, maxConns int, busy time.Duration, writer bool) (*sql.DB, error) {
	q := url.Values{}
	q.Set("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "temp_store(MEMORY)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	q.Add("_pragma", "cache_size(-32000)")
	if writer {
		q.Set("_txlock", "immediate")
	}
	dsn := "file:" + path + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxIdleTime(10 * time.Minute)
	db.SetConnMaxLifetime(time.Hour)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}
	return db, nil
}

func (d *DB) migrate(ctx context.Context) error {
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("store: read schema: %w", err)
	}
	if _, err := d.write.ExecContext(ctx, string(schema)); err != nil {
		return fmt.Errorf("store: apply schema: %w", err)
	}
	// Existing databases created before the account upgrade need additive
	// migrations; CREATE TABLE IF NOT EXISTS does not add columns.
	for _, col := range []struct{ name, ddl string }{
		{"totp_secret", "totp_secret TEXT NOT NULL DEFAULT ''"},
		{"totp_enabled", "totp_enabled INTEGER NOT NULL DEFAULT 0"},
		{"disabled", "disabled INTEGER NOT NULL DEFAULT 0"},
		{"updated_at", "updated_at INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := d.ensureColumn(ctx, "users", col.name, col.ddl); err != nil {
			return err
		}
	}
	// API token hardening columns.
	for _, col := range []struct{ name, ddl string }{
		{"expires_at", "expires_at INTEGER"},
		{"allowed_ips", "allowed_ips TEXT NOT NULL DEFAULT ''"},
	} {
		if err := d.ensureColumn(ctx, "api_tokens", col.name, col.ddl); err != nil {
			return err
		}
	}
	if err := d.ensureColumn(ctx, "products", "cover_path", "cover_path TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := d.write.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
		return fmt.Errorf("store: set user_version: %w", err)
	}
	return nil
}

// ensureColumn adds a column to an existing table when it is missing.
func (d *DB) ensureColumn(ctx context.Context, table, column, ddl string) error {
	rows, err := d.write.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return wrapErr("inspect table "+table, err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return wrapErr("scan table info", err)
		}
		if name == column {
			found = true
		}
	}
	rows.Close()
	if found {
		return nil
	}
	if _, err := d.write.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+ddl); err != nil {
		return wrapErr("add column "+column, err)
	}
	return nil
}

// Close releases both pools.
func (d *DB) Close() error {
	var errs []error
	if d.read != nil {
		errs = append(errs, d.read.Close())
	}
	if d.write != nil {
		errs = append(errs, d.write.Close())
	}
	return errors.Join(errs...)
}

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

// Ping verifies the database is reachable.
func (d *DB) Ping(ctx context.Context) error { return d.read.PingContext(ctx) }

// WALCheckpoint flushes the write-ahead log; useful before backups.
func (d *DB) WALCheckpoint(ctx context.Context) error {
	_, err := d.write.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// tx runs fn inside an immediate write transaction.
func (d *DB) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit tx: %w", err)
	}
	return nil
}

var orderSeq atomic.Int64

// newOrderNo builds a human friendly, sortable, collision resistant order
// number such as CW20261102-8F3KQ2.
func newOrderNo(now time.Time) (string, error) {
	rand, err := cryptoutil.RandomToken(6)
	if err != nil {
		return "", err
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 0, 8)
	for i := 0; i < len(rand) && len(b) < 6; i++ {
		c := rand[i]
		b = append(b, alphabet[int(c)%len(alphabet)])
	}
	for len(b) < 6 {
		b = append(b, 'X')
	}
	seq := orderSeq.Add(1) % 1000
	return fmt.Sprintf("CW%s-%s%03d", now.UTC().Format("20060102"), string(b), seq), nil
}

func nowUnix() int64 { return time.Now().UTC().Unix() }

func unixToTime(v int64) time.Time { return time.Unix(v, 0).UTC() }

func nullTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := unixToTime(v.Int64)
	return &t
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%s: %w", what, ErrConflict)
	}
	return fmt.Errorf("%s: %w", what, err)
}

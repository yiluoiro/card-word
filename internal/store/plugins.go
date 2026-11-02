package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PluginRecord is the persisted state of an installed plugin.
type PluginRecord struct {
	ID          int64
	UID         string
	Name        string
	Version     string
	Kind        string
	Manifest    string
	Dir         string
	Enabled     bool
	SHA256      string
	Signature   string
	Publisher   string
	Permissions string
	Source      string
	Error       string
	InstalledAt time.Time
	UpdatedAt   time.Time
}

// UpsertPlugin stores or replaces a plugin record.
func (d *DB) UpsertPlugin(ctx context.Context, p *PluginRecord) error {
	now := nowUnix()
	_, err := d.write.ExecContext(ctx,
		`INSERT INTO plugins (uid, name, version, kind, manifest, dir, enabled, sha256, signature,
			publisher, permissions, source, error, installed_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(uid) DO UPDATE SET
			name = excluded.name, version = excluded.version, kind = excluded.kind,
			manifest = excluded.manifest, dir = excluded.dir, sha256 = excluded.sha256,
			signature = excluded.signature, publisher = excluded.publisher,
			permissions = excluded.permissions, source = excluded.source, error = excluded.error,
			updated_at = excluded.updated_at`,
		p.UID, p.Name, p.Version, p.Kind, p.Manifest, p.Dir, boolToInt(p.Enabled), p.SHA256,
		p.Signature, p.Publisher, p.Permissions, p.Source, p.Error, now, now)
	return wrapErr("upsert plugin", err)
}

// SetPluginEnabled toggles a plugin.
func (d *DB) SetPluginEnabled(ctx context.Context, uid string, enabled bool) error {
	res, err := d.write.ExecContext(ctx,
		`UPDATE plugins SET enabled = ?, updated_at = ? WHERE uid = ?`, boolToInt(enabled), nowUnix(), uid)
	if err != nil {
		return wrapErr("toggle plugin", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPluginError stores a runtime error message for a plugin.
func (d *DB) SetPluginError(ctx context.Context, uid, msg string) error {
	_, err := d.write.ExecContext(ctx, `UPDATE plugins SET error = ? WHERE uid = ?`, truncate(msg, 1000), uid)
	return wrapErr("set plugin error", err)
}

// DeletePlugin removes a plugin record.
func (d *DB) DeletePlugin(ctx context.Context, uid string) error {
	res, err := d.write.ExecContext(ctx, `DELETE FROM plugins WHERE uid = ?`, uid)
	if err != nil {
		return wrapErr("delete plugin", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = d.write.ExecContext(ctx, `DELETE FROM plugin_settings WHERE plugin_uid = ?`, uid)
	return wrapErr("delete plugin settings", err)
}

const pluginSelect = `SELECT id, uid, name, version, kind, manifest, dir, enabled, sha256, signature,
	publisher, permissions, source, error, installed_at, updated_at FROM plugins`

func scanPlugin(row interface{ Scan(...any) error }) (*PluginRecord, error) {
	var p PluginRecord
	var enabled int
	var installed, updated int64
	if err := row.Scan(&p.ID, &p.UID, &p.Name, &p.Version, &p.Kind, &p.Manifest, &p.Dir, &enabled,
		&p.SHA256, &p.Signature, &p.Publisher, &p.Permissions, &p.Source, &p.Error, &installed, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Enabled = enabled == 1
	p.InstalledAt, p.UpdatedAt = unixToTime(installed), unixToTime(updated)
	return &p, nil
}

// PluginByUID fetches one plugin.
func (d *DB) PluginByUID(ctx context.Context, uid string) (*PluginRecord, error) {
	p, err := scanPlugin(d.read.QueryRowContext(ctx, pluginSelect+` WHERE uid = ?`, uid))
	return p, wrapErr("get plugin", err)
}

// ListPlugins returns all plugins, newest first.
func (d *DB) ListPlugins(ctx context.Context, onlyEnabled bool) ([]PluginRecord, error) {
	q := pluginSelect
	if onlyEnabled {
		q += ` WHERE enabled = 1`
	}
	q += ` ORDER BY name ASC`
	rows, err := d.read.QueryContext(ctx, q)
	if err != nil {
		return nil, wrapErr("list plugins", err)
	}
	defer rows.Close()
	var out []PluginRecord
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, wrapErr("scan plugin", err)
		}
		out = append(out, *p)
	}
	return out, wrapErr("list plugins", rows.Err())
}

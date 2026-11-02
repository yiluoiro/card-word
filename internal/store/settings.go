package store

import (
	"context"
	"database/sql"
	"errors"
)

// Setting keys used by the application.
const (
	SettingSiteName        = "site.name"
	SettingSiteTagline     = "site.tagline"
	SettingSiteNotice      = "site.notice"
	SettingTheme           = "site.theme"
	SettingCurrency        = "site.currency"
	SettingContactEmail    = "site.contact_email"
	SettingPaymentDriver   = "payment.driver"
	SettingMaintenance     = "site.maintenance"
	SettingAllowGuestQuery = "shop.allow_guest_query"
)

// GetSetting returns a setting value or def when unset.
func (d *DB) GetSetting(ctx context.Context, key, def string) (string, error) {
	var v string
	err := d.read.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return def, wrapErr("get setting", err)
	}
	return v, nil
}

// SetSetting upserts a setting.
func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := d.write.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, nowUnix())
	return wrapErr("set setting", err)
}

// AllSettings returns every setting as a map.
func (d *DB) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := d.read.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, wrapErr("list settings", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, wrapErr("scan setting", err)
		}
		out[k] = v
	}
	return out, wrapErr("list settings", rows.Err())
}

// GetPluginSetting reads a namespaced plugin setting.
func (d *DB) GetPluginSetting(ctx context.Context, uid, key, def string) (string, error) {
	var v string
	err := d.read.QueryRowContext(ctx,
		`SELECT value FROM plugin_settings WHERE plugin_uid = ? AND key = ?`, uid, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return def, wrapErr("get plugin setting", err)
	}
	return v, nil
}

// SetPluginSetting upserts a plugin setting.
func (d *DB) SetPluginSetting(ctx context.Context, uid, key, value string) error {
	_, err := d.write.ExecContext(ctx,
		`INSERT INTO plugin_settings (plugin_uid, key, value, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(plugin_uid, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		uid, key, value, nowUnix())
	return wrapErr("set plugin setting", err)
}

// PluginSettings returns all settings of one plugin.
func (d *DB) PluginSettings(ctx context.Context, uid string) (map[string]string, error) {
	rows, err := d.read.QueryContext(ctx,
		`SELECT key, value FROM plugin_settings WHERE plugin_uid = ? ORDER BY key`, uid)
	if err != nil {
		return nil, wrapErr("list plugin settings", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, wrapErr("scan plugin setting", err)
		}
		out[k] = v
	}
	return out, wrapErr("list plugin settings", rows.Err())
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

type Installation struct{ SiteName, Username, Password, Contact string }

var installUsername = regexp.MustCompile(`^[a-zA-Z0-9_]{3,32}$`)

// Install creates the initial owner and defaults in one transaction. Existing
// accounts (including disabled accounts) permanently prevent reinstallation.
func (d *DB) Install(ctx context.Context, in Installation) error {
	in.SiteName = strings.TrimSpace(in.SiteName)
	in.Username = strings.TrimSpace(in.Username)
	in.Contact = strings.TrimSpace(in.Contact)
	if in.SiteName == "" || utf8.RuneCountInString(in.SiteName) > 80 || !installUsername.MatchString(in.Username) || len(in.Password) < 12 || len(in.Password) > 1024 || utf8.RuneCountInString(in.Contact) > 200 {
		return errors.New("请检查站点名称、用户名和密码长度。")
	}
	hash, err := cryptoutil.HashPassword(in.Password)
	if err != nil {
		return err
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
			return err
		}
		var complete string
		err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='installation.completed'`).Scan(&complete)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if count > 0 || complete == "1" {
			return ErrConflict
		}
		now := nowUnix()
		if _, err := tx.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,created_at,updated_at) VALUES(?,?,'owner',?,?)`, in.Username, hash, now, now); err != nil {
			return err
		}
		for key, value := range map[string]string{SettingSiteName: in.SiteName, SettingContactEmail: in.Contact, SettingSiteTagline: "数字商品与礼品卡，付款确认后查看卡密。", SettingCurrency: "CNY", SettingPaymentDriver: "manual", "seo.enabled": "0", "installation.completed": "1"} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, value, now); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(actor,action,target,ip,detail,created_at) VALUES('system','installation.complete',?,'','initial owner created',?)`, in.Username, now)
		return err
	})
}

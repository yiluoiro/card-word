package store

import (
	"context"
	"time"
)

// AuditEntry is an immutable record of a privileged action.
type AuditEntry struct {
	ID        int64
	Actor     string
	Action    string
	Target    string
	IP        string
	Detail    string
	CreatedAt time.Time
}

// Audit appends an audit record. Failures are the caller's problem: sensitive
// operations must not continue silently when the trail cannot be written.
func (d *DB) Audit(ctx context.Context, actor, action, target, ip, detail string) error {
	_, err := d.write.ExecContext(ctx,
		`INSERT INTO audit_logs (actor, action, target, ip, detail, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		actor, action, truncate(target, 200), ip, truncate(detail, 2000), nowUnix())
	return wrapErr("write audit log", err)
}

// ListAudit returns the newest audit entries, optionally filtered by action.
func (d *DB) ListAudit(ctx context.Context, action string, limit, offset int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, actor, action, target, ip, detail, created_at FROM audit_logs`
	args := []any{}
	if action != "" {
		q += ` WHERE action LIKE ?`
		args = append(args, action+"%")
	}
	q += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := d.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapErr("list audit", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var created int64
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Target, &e.IP, &e.Detail, &created); err != nil {
			return nil, wrapErr("scan audit", err)
		}
		e.CreatedAt = unixToTime(created)
		out = append(out, e)
	}
	return out, wrapErr("list audit", rows.Err())
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8Boundary(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func utf8Boundary(b byte) bool { return b&0xC0 != 0x80 }

// CountAudit returns the number of audit rows matching an action prefix.
func (d *DB) CountAudit(ctx context.Context, action string) (int, error) {
	q := `SELECT COUNT(*) FROM audit_logs`
	var args []any
	if action != "" {
		q += ` WHERE action LIKE ?`
		args = append(args, action+"%")
	}
	var n int
	err := d.read.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, wrapErr("count audit", err)
}

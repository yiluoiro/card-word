package store

import (
	"context"
	"time"
)

// PeriodStats uses the same UTC creation-time and currency scope as order exports.
type PeriodStats struct {
	Orders, Revenue, Completed, Closed int64
}

func (d *DB) PeriodStats(ctx context.Context, currency string, from, until time.Time) (PeriodStats, error) {
	var s PeriodStats
	err := d.read.QueryRowContext(ctx, `
 SELECT COUNT(*),
 COALESCE(SUM(CASE WHEN status IN ('paid','delivered') THEN total_cents ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN status='delivered' THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN status='closed' THEN 1 ELSE 0 END),0)
 FROM orders WHERE currency=? AND created_at>=? AND created_at<? `,
		currency, from.Unix(), until.Unix()).Scan(&s.Orders, &s.Revenue, &s.Completed, &s.Closed)
	return s, wrapErr("period statistics", err)
}

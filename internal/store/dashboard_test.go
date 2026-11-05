package store

import (
	"context"
	"testing"
	"time"
)

func TestPeriodStatsBoundariesCurrencyAndStatus(t *testing.T) {
	d := singleStoreDB(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	for _, row := range []struct {
		offset           int64
		currency, status string
		amount           int64
	}{
		{-1, "CNY", "delivered", 10000}, {0, "CNY", "delivered", 1000}, {3600, "CNY", "pending", 500},
		{7200, "CNY", "paid", 2000}, {10800, "CNY", "closed", 500}, {14400, "CNY", "refunded", 700},
		{18000, "USD", "delivered", 99999}, {86400, "CNY", "delivered", 20000},
	} {
		o := protectedOrder(t, d, row.amount)
		if _, err := d.write.ExecContext(ctx, `UPDATE orders SET created_at=?,currency=?,status=? WHERE id=?`, start.Unix()+row.offset, row.currency, row.status, o.ID); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.PeriodStats(ctx, "CNY", start, end)
	if err != nil || got.Orders != 5 || got.Revenue != 3000 || got.Completed != 1 || got.Closed != 1 {
		t.Fatal(got, err)
	}
	empty, err := d.PeriodStats(ctx, "EUR", start, end)
	if err != nil || empty != (PeriodStats{}) {
		t.Fatal(empty, err)
	}
}

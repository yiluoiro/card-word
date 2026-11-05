package store

import (
	"context"
	"testing"
)

func TestSingleStoreSchemaExcludesPlatform(t *testing.T) {
	d := singleStoreDB(t)
	for _, table := range []string{"merchants", "merchant_events", "merchant_slugs", "merchant_phones", "merchant_applications", "merchant_documents", "merchant_identity_requests", "sms_attempts", "guarantee_categories", "merchant_guarantees", "guarantee_payments", "platform_money_claims", "settlement_receipts", "withdrawals", "withdrawal_allocations", "support_cases", "support_messages", "market_releases"} {
		var n int
		if err := d.read.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("platform table exists: %s", table)
		}
	}
	order := protectedOrder(t, d, 1000)
	if order.Status != OrderDelivered {
		t.Fatalf("direct-store delivery failed: %s", order.Status)
	}
}

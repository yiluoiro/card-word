package httpapi

import (
	"net/url"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/store"
)

func TestDashboardPeriodBounds(t *testing.T) {
	now := time.Date(2026, 11, 4, 23, 0, 0, 0, time.FixedZone("Tokyo", 9*3600))
	for _, tc := range []struct{ query, from, to string }{
		{"", "2026-11-01", "2026-11-04"},
		{"period=last_month", "2026-10-01", "2026-10-31"},
		{"period=7d", "2026-10-29", "2026-11-04"},
		{"period=30d", "2026-10-06", "2026-11-04"},
		{"period=custom&from=2024-02-28&to=2024-02-29", "2024-02-28", "2024-02-29"},
		{"period=month&custom=1&from=2024-02-28&to=2024-02-29", "2024-02-28", "2024-02-29"},
	} {
		q, _ := url.ParseQuery(tc.query)
		p, err := parseDashboardPeriod(q, now)
		if err != nil || p.From != tc.from || p.To != tc.to || !p.End.After(p.Start) {
			t.Fatalf("%s: %+v %v", tc.query, p, err)
		}
	}
	for _, query := range []string{
		"period=invalid",
		"period=custom&from=2026-11-01&to=2026-11-05",
		"period=custom&from=2026-11-03&to=2026-11-01",
		"period=custom&from=2024-02-30&to=2024-03-01",
		"period=custom&from=2024-01-01&to=2026-01-01",
		"period=custom",
	} {
		q, _ := url.ParseQuery(query)
		if _, err := parseDashboardPeriod(q, now); err == nil {
			t.Fatalf("accepted %s", query)
		}
	}
}
func TestDashboardChartRealZeroAndPeak(t *testing.T) {
	days := []store.DayStat{{Date: "2026-11-01"}, {Date: "2026-11-02", Orders: 120, RevenueCents: 2500}}
	c := buildDashboardChart(days, "USD")
	if len(c.Points) != 2 || c.Points[0].Y != 204 || c.Points[1].Y != 24 || c.Ticks[0].Value != 120 || c.Points[1].Revenue != formatMoney(2500, "USD") {
		t.Fatal(c)
	}
	if periodChange(0, 0) != "与上一周期持平" || periodChange(5, 0) != "上一周期为 0" || periodChange(5, 10) != "较上一周期 -50.0%" {
		t.Fatal("invalid comparison")
	}
}

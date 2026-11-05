package payment

import (
	"context"
	"testing"
)

func TestMoneyToCentsStrict(t *testing.T) {
	for input, want := range map[string]int64{"0": 0, "0.01": 1, "12.3": 1230, "12.34": 1234, "92233720368547758.07": 9223372036854775807} {
		got, err := moneyToCents(input)
		if err != nil || got != want {
			t.Errorf("%q: got %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "-0.01", "-1.50", "+1", "1.", "1.001", "1.23junk", "1.-1", "1e2", "92233720368547758.08", "9223372036854775807"} {
		if got, err := moneyToCents(input); err == nil {
			t.Errorf("accepted %q as %d", input, got)
		}
	}
}

func TestEpayCallbackIdentity(t *testing.T) {
	e, err := NewEpay(EpayConfig{GatewayURL: "https://pay.example", PID: "merchant", Key: "test-only"})
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]string{"pid": "merchant", "out_trade_no": "order", "trade_no": "txn", "money": "1.00", "trade_status": "TRADE_SUCCESS"}
	for _, missing := range []string{"", "pid", "out_trade_no", "trade_no"} {
		p := map[string]string{}
		for k, v := range base {
			p[k] = v
		}
		if missing != "" {
			delete(p, missing)
		}
		p["sign"] = epaySign(p, e.cfg.Key)
		_, err := e.VerifyCallback(context.Background(), p, nil)
		if (err != nil) != (missing != "") {
			t.Errorf("missing %q: %v", missing, err)
		}
	}
}

func TestEpayCreateExactAmount(t *testing.T) {
	e, _ := NewEpay(EpayConfig{GatewayURL: "https://pay.example", PID: "merchant", Key: "test-only"})
	a, err := e.Create(context.Background(), Request{AmountCents: 9223372036854775807, Currency: "CNY"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Fields["money"] != "92233720368547758.07" {
		t.Fatal(a.Fields["money"])
	}
}

func TestEpayRejectsUnsupportedCurrency(t *testing.T) {
	e, err := NewEpay(EpayConfig{GatewayURL: "https://pay.example", PID: "merchant", Key: "test-only"})
	if err != nil {
		t.Fatal(err)
	}
	for _, currency := range []string{"USD", "EUR", "JPY", ""} {
		if a, err := e.Create(context.Background(), Request{AmountCents: 100, Currency: currency}); err == nil || a != nil {
			t.Fatalf("currency %q was submitted as yuan", currency)
		}
	}
}

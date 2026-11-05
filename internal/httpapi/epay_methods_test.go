package httpapi

import (
	"context"

	"net/url"

	"testing"

	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/store"
)

func TestEpayMultipleMethodsConfigurationAndSettlement(t *testing.T) {
	for _, protocol := range []string{"v1", "v2"} {
		t.Run(protocol, func(t *testing.T) {
			s := securityServer(t)
			ctx := context.Background()
			owner, csrf := storeAdmin(t, s, store.RoleOwner)
			_, pri, pub := commerceKeys(t)
			f := url.Values{"provider": {"epay"}, "enabled": {"1"}, "protocol": {protocol}, "gateway": {"https://pay.example.test"}, "pid": {"123"}, "key": {"test-key"}, "private_key": {pri}, "public_key": {pub}, "type": {"wxpay", "alipay", "wxpay", "qqpay", "usdt", "paypal"}}
			save := func() {
				t.Helper()
				w := storeRequest(s, "POST", "/admin/payments", f, owner, csrf)
				if w.Code != 303 {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			save()
			value, _ := s.db.GetSetting(ctx, "payment.epay.methods", "")
			if value != "alipay,wxpay,qqpay,usdt,paypal" {
				t.Fatal("methods not canonical", value)
			}
			options := s.availableDriversForCurrency("CNY")
			found := 0
			for _, o := range options {
				if o["Provider"] == "epay" {
					found++
					if o["Name"] != "epay:"+o["Method"] {
						t.Fatal(o)
					}
				}
			}
			if found != 5 {
				t.Fatal(options)
			}
			for _, code := range []string{"alipay", "wxpay", "qqpay", "usdt", "paypal"} {
				provider, method, ok := s.checkoutPayment("CNY", "epay:"+code)
				if !ok || provider != "epay" || method != code {
					t.Fatal(provider, method, ok)
				}
				driver, _ := s.findDriver(provider)
				a, err := driver.Create(ctx, payment.Request{Method: method, Currency: "CNY", AmountCents: 100, OrderNo: "test"})
				if err != nil || a.Fields["type"] != code {
					t.Fatal(a, err)
				}
			}
			if _, _, ok := s.checkoutPayment("CNY", "epay:unknown"); ok {
				t.Fatal("invalid method selectable")
			}
			if _, _, ok := s.checkoutPayment("USD", "epay:alipay"); ok {
				t.Fatal("currency restriction bypassed")
			}
			if provider, method, ok := s.checkoutPayment("CNY", "epay"); !ok || provider != "epay" || method != "alipay" {
				t.Fatal("legacy API driver lost default")
			}
			for _, bad := range [][]string{nil, {"alipay", "evil"}} {
				f["type"] = bad
				save()
				v, _ := s.db.GetSetting(ctx, "payment.epay.methods", "")
				if v != value {
					t.Fatal("invalid selection partially saved", v)
				}
			}
			p := securityProduct(t, s)
			if _, err := s.db.AddCards(ctx, p.ID, []string{"usdt-test-card", "paypal-test-card"}, "extra"); err != nil {
				t.Fatal(err)
			}
			for _, channel := range []string{"wxpay", "usdt", "paypal"} {
				f["type"] = []string{"alipay", channel}
				save()
				in := store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "epay", PaymentMethod: channel, IdempotencyKey: "epay-method-test-" + channel}
				result, err := s.db.CreateOrder(ctx, in)
				if err != nil {
					t.Fatal(err)
				}
				in.PaymentMethod = "alipay"
				if _, err = s.db.CreateOrder(ctx, in); err == nil {
					t.Fatal("idempotent order changed payment method")
				}
				f["type"] = []string{"alipay"}
				save()
				if _, _, ok := s.checkoutPayment("CNY", "epay:"+channel); ok {
					t.Fatal("unselected method offered")
				}
				order, _ := s.db.OrderByNo(ctx, result.Order.OrderNo)
				if order.PaymentMethod != channel {
					t.Fatal("pending order method overwritten")
				}
				driver, _ := s.findDriver("epay")
				a, err := driver.Create(ctx, payment.Request{Method: order.PaymentMethod, Currency: order.Currency, AmountCents: order.TotalCents, OrderNo: order.OrderNo})
				if err != nil || a.Fields["type"] != channel {
					t.Fatal("old order no longer payable", a, err)
				}
				cb := &payment.Callback{Driver: "epay", Method: "alipay", OrderNo: order.OrderNo, TxnID: "method-test-" + channel, AmountCents: 100, Paid: true}
				if err = s.completePayment(ctx, cb, ""); err == nil {
					t.Fatal("mismatched callback method accepted")
				}
				cb.Method = channel
				for i := 0; i < 2; i++ {
					if err = s.completePayment(ctx, cb, ""); err != nil {
						t.Fatal(err)
					}
				}
			}
			f["type"] = []string{"cashier", "qqpay"}
			save()
			driver, _ := s.findDriver("epay")
			a, err := driver.Create(ctx, payment.Request{Method: "cashier", Currency: "CNY", AmountCents: 100, OrderNo: "cashier"})
			if err != nil || a.Fields["type"] != "" {
				t.Fatal("cashier forces a channel", err)
			}
			if _, err = driver.Create(ctx, payment.Request{Method: "unknown", Currency: "CNY", AmountCents: 100}); err == nil {
				t.Fatal("unknown method submitted")
			}
		})
	}
}

// The provider name stays stable for callback transaction deduplication.

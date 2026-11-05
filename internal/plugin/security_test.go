package plugin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yiluoiro/card-word/internal/payment"
)

type securityTransport func(*http.Request) (*http.Response, error)

func (f securityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHookPermissions(t *testing.T) {
	for _, event := range []string{"order.paid", "product.updated"} {
		for _, allowed := range []bool{false, true} {
			var calls atomic.Int32
			rt := &Runtime{Manifest: Manifest{Runtime: RuntimeSpec{Type: "external", BaseURL: "https://plugin.example"}, Hooks: []string{event}, Permissions: []string{PermHTTPOutbound}}}
			if allowed {
				perm := PermOrderRead
				if event == "product.updated" {
					perm = PermProductRead
				}
				rt.Manifest.Permissions = append(rt.Manifest.Permissions, perm)
			}
			rt.Client = &http.Client{Transport: securityTransport(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			h := NewHost(nil, nil, nil)
			h.active["test"] = rt
			h.CallHook(context.Background(), event, map[string]any{"private": "data"})
			want := int32(0)
			if allowed {
				want = 1
			}
			if calls.Load() != want {
				t.Errorf("%s allowed=%v: calls=%d", event, allowed, calls.Load())
			}
		}
	}
}

func TestHealthCheckNoRedirect(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	h := NewHost(nil, nil, nil)
	err := h.checkHealth(context.Background(), &Manifest{Runtime: RuntimeSpec{Type: "external", BaseURL: source.URL, HealthPath: "/health"}, Permissions: []string{PermHTTPOutbound}})
	if err == nil || calls.Load() != 0 {
		t.Fatalf("redirect followed or accepted: calls=%d err=%v", calls.Load(), err)
	}
}

func TestExternalManifestPermission(t *testing.T) {
	m := Manifest{Schema: ManifestSchema, UID: "test.plugin", Name: "Test", Version: "1.0.0", Kind: KindExtension, Runtime: RuntimeSpec{Type: "external", BaseURL: "https://plugin.example"}}
	if err := m.Validate(); err == nil {
		t.Fatal("external runtime accepted without outbound permission")
	}
	m.Permissions = []string{PermHTTPOutbound}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	m.Runtime.HealthPath = "@127.0.0.1/"
	if err := m.Validate(); err == nil {
		t.Fatal("authority-changing health path accepted")
	}
}

func TestPaymentProviderPermission(t *testing.T) {
	h := NewHost(nil, nil, nil)
	rt := &Runtime{UID: "test.plugin", Manifest: Manifest{Kind: KindPayment, Runtime: RuntimeSpec{Type: "external", BaseURL: "https://plugin.example"}, Permissions: []string{PermHTTPOutbound}}}
	h.active[rt.UID] = rt
	if len(h.PaymentDrivers()) != 0 {
		t.Fatal("unprivileged payment provider registered")
	}
	if _, err := (&paymentAdapter{host: h, rt: rt}).Create(context.Background(), payment.Request{}); err == nil {
		t.Fatal("unprivileged provider invoked")
	}
}

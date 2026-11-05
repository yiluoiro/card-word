package market

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
)

type securityTransport func(*http.Request) (*http.Response, error)

func (f securityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMarketRejectsUnsafeTargets(t *testing.T) {
	for _, target := range []string{"https://127.0.0.1/index", "https://[::1]/index", "https://[::ffff:127.0.0.1]/index", "https://10.0.0.1/index", "https://169.254.169.254/index", "https://100.100.100.200/index", "https://user:pass@example.com/index", "http://example.com/index"} {
		c := NewClient(nil)
		calls := 0
		c.HTTP.Transport = securityTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"plugins":[]}`))}, nil
		})
		if _, err := c.List(context.Background(), target); err == nil || calls != 0 {
			t.Errorf("unsafe target %s: err=%v calls=%d", target, err, calls)
		}
	}
}

func TestMarketRejectsRedirect(t *testing.T) {
	c := NewClient(nil)
	calls := 0
	c.HTTP.Transport = securityTransport(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"http://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"plugins":[]}`))}, nil
	})
	if _, err := c.List(context.Background(), "https://market.example/index"); err == nil || calls != 1 {
		t.Fatalf("redirect accepted: err=%v calls=%d", err, calls)
	}
}

func TestDownloadDigest(t *testing.T) {
	c := NewClient(nil)
	c.HTTP.Transport = securityTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("package"))}, nil
	})
	sum := sha256.Sum256([]byte("package"))
	for _, digest := range []string{"", "invalid", strings.Repeat("0", 64), hex.EncodeToString(sum[:])} {
		_, err := c.Download(context.Background(), Entry{DownloadURL: "https://market.example/package", SHA256: digest}, 100)
		if (err == nil) != (digest == hex.EncodeToString(sum[:])) {
			t.Errorf("digest %q: %v", digest, err)
		}
	}
}

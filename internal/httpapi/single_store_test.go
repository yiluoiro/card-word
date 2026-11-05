package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/market"
	"github.com/yiluoiro/card-word/internal/plugin"
	"github.com/yiluoiro/card-word/internal/store"
)

func TestSingleStoreRoutesAndNavigation(t *testing.T) {
	s := singleStoreServer(t)
	owner, csrf := storeAdmin(t, s, store.RoleOwner)
	for _, path := range []string{"/merchant", "/merchant/apply", "/merchant/activate", "/merchant/identity/start", "/merchant/phone/send", "/merchant/guarantee", "/stores", "/s/demo/", "/s/demo/admin", "/platform", "/platform/identity", "/platform/sms", "/platform/protection", "/platform/merchants/1/review", "/platform/operations/1", "/admin/wallet", "/admin/complaints", "/reports/new/1", "/order/example/complaint", "/admin/market/releases", "/admin/market/releases/1/publish", "/market/index.json", "/market/packages/1/package.zip"} {
		for _, method := range []string{"GET", "POST"} {
			w := storeRequest(s, method, path, nil, owner, csrf)
			if w.Code != http.StatusNotFound {
				t.Fatalf("removed route %s %s returned %d: %s", method, path, w.Code, w.Body.String())
			}
		}
	}
	for _, path := range []string{"/", "/admin", "/admin/products", "/admin/orders", "/admin/plugins", "/admin/payments", "/admin/settings", "/admin/appearance", "/admin/users", "/admin/seo", "/admin/announcements"} {
		w := storeRequest(s, "GET", path, nil, owner, "")
		if w.Code != 200 {
			t.Fatalf("single-store route %s returned %d: %s", path, w.Code, w.Body.String())
		}
		for _, link := range []string{`href="/platform`, `href="/merchant`, `href="/stores`, `href="/admin/market/releases`, `href="/admin/wallet`, `href="/admin/complaints`} {
			if strings.Contains(w.Body.String(), link) {
				t.Fatalf("%s exposes %s", path, link)
			}
		}
	}
	for _, style := range []string{"studio", "minimal", "editorial", "default"} {
		form := url.Values{"style": {style}, "accent": {"blue"}, "eyebrow": {"数字商品"}, "headline": {"欢迎选购"}, "catalog_title": {"商品目录"}, "catalog_description": {"查看商品说明"}}
		w := storeRequest(s, "POST", "/admin/appearance", form, owner, csrf)
		if w.Code != 303 || strings.Contains(w.Header().Get("Location"), "err=") {
			t.Fatalf("appearance save: %d %s", w.Code, w.Body.String())
		}
		w = storeRequest(s, "GET", "/", nil, nil, "")
		if w.Code != 200 {
			t.Fatalf("appearance %s: %d %s", style, w.Code, w.Body.String())
		}
		if style == "default" && !strings.Contains(w.Body.String(), "reference-hero") {
			t.Fatal("original storefront layout missing")
		}
	}
}

type singleStoreTransport func(*http.Request) (*http.Response, error)

func (f singleStoreTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSingleStoreRemoteMarketSignedInstall(t *testing.T) {
	s := singleStoreServer(t)
	owner, csrf := storeAdmin(t, s, store.RoleOwner)
	s.cfg.MarketURL = "https://zinvmeng.com/market/index.json"
	s.cfg.MarketAllowInstall = true
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	f, err := z.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(f, `{"schema":"cardword.plugin/v1","uid":"remote-example","name":"Remote Example","version":"1.0.0","kind":"extension","runtime":{"type":"none"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	pkg := archive.Bytes()
	entry := market.Entry{UID: "remote-example", Name: "Remote Example", Version: "1.0.0", Kind: "extension", DownloadURL: "https://zinvmeng.com/market/packages/example.zip", SHA256: cryptoutil.SHA256Hex(pkg), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, pkg))}
	index, err := json.Marshal(market.Index{Schema: "cardword.market/v1", Plugins: []market.Entry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	broken, corrupt := false, false
	s.market = market.NewClient([]market.Entry{{UID: "must-not-display", Name: "Builtin"}})
	s.market.HTTP.Transport = singleStoreTransport(func(r *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, index
		switch r.URL.String() {
		case s.cfg.MarketURL:
			if broken {
				status = 503
				body = []byte("unavailable")
			}
		case entry.DownloadURL:
			body = pkg
			if corrupt {
				body = []byte("tampered")
			}
		default:
			t.Fatalf("unexpected request: %s", r.URL)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	s.installer = plugin.NewInstaller(t.TempDir(), true, []ed25519.PublicKey{pub})
	s.plugins = plugin.NewHost(s.db, s.installer, s.log)
	entries, err := s.marketEntries(context.Background())
	if err != nil || len(entries) != 1 || entries[0].UID != entry.UID {
		t.Fatalf("remote-only catalogue: %v %v", entries, err)
	}
	corrupt = true
	storeRequest(s, "POST", "/admin/market/install", url.Values{"uid": {entry.UID}}, owner, csrf)
	if records, _ := s.db.ListPlugins(context.Background(), false); len(records) != 0 {
		t.Fatal("tampered package installed")
	}
	corrupt = false
	w := storeRequest(s, "POST", "/admin/market/install", url.Values{"uid": {entry.UID}}, owner, csrf)
	if w.Code != 303 {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	records, err := s.db.ListPlugins(context.Background(), false)
	if err != nil || len(records) != 1 || records[0].Enabled || records[0].Signature == "" {
		t.Fatalf("signed installation must start disabled: %v %v", records, err)
	}
	broken = true
	w = storeRequest(s, "GET", "/admin/plugins", nil, owner, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Remote Example") {
		t.Fatal("remote outage broke installed-app management")
	}
}

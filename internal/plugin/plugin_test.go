package plugin_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/yiluoiro/card-word/internal/plugin"
)

func buildPackage(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

const validManifest = `{
  "schema": "cardword.plugin/v1",
  "uid": "com.example.hello",
  "name": "Hello Theme",
  "version": "1.0.0",
  "author": "Example",
  "kind": "theme",
  "runtime": {"type": "static"},
  "static": {"index": "index.html"},
  "permissions": ["theme.override"],
  "hooks": []
}`

func TestInstallStaticPlugin(t *testing.T) {
	dir := t.TempDir()
	inst := plugin.NewInstaller(dir, false, nil)
	pkg := buildPackage(t, map[string]string{
		"manifest.json": validManifest,
		"index.html":    "<h1>hello</h1>",
	})

	installed, err := inst.Install(context.Background(), pkg, "", "upload")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if installed.Manifest.UID != "com.example.hello" {
		t.Fatalf("unexpected uid %s", installed.Manifest.UID)
	}
	if _, err := os.Stat(filepath.Join(dir, "com.example.hello", "index.html")); err != nil {
		t.Fatalf("expected plugin files on disk: %v", err)
	}
}

func TestInstallRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	inst := plugin.NewInstaller(dir, false, nil)
	pkg := buildPackage(t, map[string]string{
		"manifest.json":  validManifest,
		"../../evil.txt": "pwned",
	})
	if _, err := inst.Install(context.Background(), pkg, "", "upload"); err == nil {
		t.Fatal("expected traversal entry to be rejected")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); err == nil {
		t.Fatal("traversal file escaped the plugin directory")
	}
}

func TestInstallRejectsUnknownPermission(t *testing.T) {
	dir := t.TempDir()
	inst := plugin.NewInstaller(dir, false, nil)
	manifest := `{
      "schema": "cardword.plugin/v1",
      "uid": "com.example.bad",
      "name": "Bad",
      "version": "1.0.0",
      "kind": "extension",
      "runtime": {"type": "none"},
      "permissions": ["database.root"]
    }`
	pkg := buildPackage(t, map[string]string{"manifest.json": manifest})
	if _, err := inst.Install(context.Background(), pkg, "", "upload"); err == nil {
		t.Fatal("expected unknown permission to be rejected")
	}
}

func TestExternalRuntimeRequiresOutboundPermission(t *testing.T) {
	dir := t.TempDir()
	inst := plugin.NewInstaller(dir, false, nil)
	manifest := `{
      "schema": "cardword.plugin/v1",
      "uid": "com.example.pay",
      "name": "Pay",
      "version": "1.0.0",
      "kind": "payment",
      "runtime": {"type": "external", "base_url": "https://pay.example.com"},
      "payment": {"title": "Pay"},
      "permissions": ["payment.provider"]
    }`
	pkg := buildPackage(t, map[string]string{"manifest.json": manifest})
	if _, err := inst.Install(context.Background(), pkg, "", "upload"); err == nil {
		t.Fatal("expected missing http.outbound permission to be rejected")
	}
}

func TestSignedPackagePolicy(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	dir := t.TempDir()
	pkg := buildPackage(t, map[string]string{
		"manifest.json": validManifest,
		"index.html":    "<h1>signed</h1>",
	})

	// Signature required, nothing supplied.
	strict := plugin.NewInstaller(dir, true, []ed25519.PublicKey{pub})
	if _, err := strict.Install(context.Background(), pkg, "", "upload"); err == nil {
		t.Fatal("expected unsigned package to be rejected")
	}

	// Wrong signature.
	if _, err := strict.Install(context.Background(), pkg, base64.StdEncoding.EncodeToString([]byte("bogus")), "upload"); err == nil {
		t.Fatal("expected invalid signature to be rejected")
	}

	// Valid signature.
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, pkg))
	installed, err := strict.Install(context.Background(), pkg, sig, "market")
	if err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if !installed.Signed {
		t.Fatal("expected package to be marked signed")
	}
}

func TestManifestValidation(t *testing.T) {
	cases := map[string]string{
		"bad schema":   `{"schema":"v0","uid":"com.a.b","name":"x","version":"1.0.0","kind":"theme","runtime":{"type":"none"}}`,
		"bad uid":      `{"schema":"cardword.plugin/v1","uid":"BAD UID","name":"x","version":"1.0.0","kind":"theme","runtime":{"type":"none"}}`,
		"bad version":  `{"schema":"cardword.plugin/v1","uid":"com.a.b","name":"x","version":"one","kind":"theme","runtime":{"type":"none"}}`,
		"bad kind":     `{"schema":"cardword.plugin/v1","uid":"com.a.b","name":"x","version":"1.0.0","kind":"weird","runtime":{"type":"none"}}`,
		"unknown hook": `{"schema":"cardword.plugin/v1","uid":"com.a.b","name":"x","version":"1.0.0","kind":"theme","runtime":{"type":"none"},"hooks":["order.exploded"]}`,
	}
	for name, doc := range cases {
		if _, err := plugin.ParseManifest([]byte(doc)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	if _, err := plugin.ParseManifest([]byte(validManifest)); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

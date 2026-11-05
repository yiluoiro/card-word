package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestSignedPluginCannotBypassOutboundPermission(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, allowed := range []bool{false, true} {
		m := Manifest{Schema: ManifestSchema, UID: "test.signed", Name: "Test", Version: "1.0.0", Kind: KindExtension, Runtime: RuntimeSpec{Type: "external", BaseURL: "https://plugin.example"}}
		if allowed {
			m.Permissions = []string{PermHTTPOutbound}
		}
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.Create("manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, buf.Bytes()))
		installer := NewInstaller(t.TempDir(), true, []ed25519.PublicKey{pub})
		_, err = installer.Install(context.Background(), buf.Bytes(), sig, "test")
		if (err == nil) != allowed {
			t.Errorf("allowed=%v: %v", allowed, err)
		}
	}
}

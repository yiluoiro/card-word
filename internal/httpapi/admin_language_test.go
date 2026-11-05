package httpapi

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestAdminLanguageCatalogBundle(t *testing.T) {
	source, err := staticFS.ReadFile("web/static/admin-translations.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := staticFS.ReadFile("web/static/admin-translations.js")
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]string
	if err = json.Unmarshal(source, &a); err != nil {
		t.Fatal(err)
	}
	raw := bytes.TrimSuffix(bytes.TrimSpace(bytes.TrimPrefix(bundle, []byte("window.cardwordAdminMessages = "))), []byte(";"))
	if err = json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatal("catalogue and browser bundle differ")
	}
	for key, value := range a {
		if value == "" || b[key] != value {
			t.Fatalf("missing translation: %s", key)
		}
	}
}

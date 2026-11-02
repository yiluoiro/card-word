package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

// TestCardSecretsAreEncryptedAtRest is a white box check: it reads the raw
// column value to prove card keys are never persisted in clear text.
func TestCardSecretsAreEncryptedAtRest(t *testing.T) {
	key, err := cryptoutil.RandomBytes(32)
	if err != nil {
		t.Fatalf("random key: %v", err)
	}
	db, err := Open(Options{
		Path:         filepath.Join(t.TempDir(), "enc.db"),
		ReadPoolSize: 2,
		BusyTimeout:  5 * time.Second,
		MasterKey:    key,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	product, err := db.CreateProduct(ctx, ProductInput{Name: "加密测试", Slug: "enc-test", PriceCents: 100})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	secret := "PLAINTEXT-CARD-12345"
	if _, err := db.AddCards(ctx, product.ID, []string{secret}, ""); err != nil {
		t.Fatalf("add cards: %v", err)
	}

	var stored string
	if err := db.read.QueryRowContext(ctx, `SELECT secret FROM card_keys LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("read stored secret: %v", err)
	}
	if strings.Contains(stored, secret) {
		t.Fatalf("card secret stored in clear text: %s", stored)
	}
	if stored == "" {
		t.Fatal("stored secret is empty")
	}

	var id int64
	if err := db.read.QueryRowContext(ctx, `SELECT id FROM card_keys LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("read id: %v", err)
	}
	plain, err := db.RevealCard(ctx, id)
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if plain != secret {
		t.Fatalf("expected %q after decryption, got %q", secret, plain)
	}

	// A wrong master key must not decrypt the row.
	otherKey, _ := cryptoutil.RandomBytes(32)
	db2, err := Open(Options{
		Path:         db.Path(),
		ReadPoolSize: 1,
		BusyTimeout:  time.Second,
		MasterKey:    otherKey,
	})
	if err != nil {
		t.Fatalf("open second handle: %v", err)
	}
	defer db2.Close()
	if _, err := db2.RevealCard(ctx, id); err == nil {
		t.Fatal("expected decryption with a foreign master key to fail")
	}
}

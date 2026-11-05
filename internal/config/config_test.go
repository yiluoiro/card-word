package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersionDoesNotGenerateFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "unused")
	cfg, err := Load([]string{"--version", "--data", dir})
	if err != nil || !cfg.ShowVersion {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("version created data directory")
	}
}

func TestCommunityDefaultMarket(t *testing.T) {
	t.Setenv("CARDWORD_MARKET_URL", "")
	cfg, err := Load([]string{"--data", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MarketURL != "https://zinvmeng.com/market/index.json" {
		t.Fatal(cfg.MarketURL)
	}
}
func TestBaseURLDefaultsAndValidation(t *testing.T) {
	t.Setenv("CARDWORD_BASE_URL", "")
	for _, address := range []string{"127.0.0.1:8766", ":8080", "[::1]:8080"} {
		cfg, err := Load([]string{"--addr", address, "--data", t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if address == "127.0.0.1:8766" && cfg.BaseURL != "http://127.0.0.1:8766" {
			t.Fatal(cfg.BaseURL)
		}
	}
	for _, origin := range []string{"https://", "https://user:pass@example.com", "https://example.com/path", "https://example.com?x=1", "https://example.com#x"} {
		if _, err := Load([]string{"--data", t.TempDir(), "--base-url", origin}); err == nil {
			t.Fatalf("accepted %s", origin)
		}
	}
}

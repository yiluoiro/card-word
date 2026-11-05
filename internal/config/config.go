// Package config loads runtime configuration from environment variables and
// command line flags, validating it before the server starts.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	Addr     string
	BaseURL  string
	DataDir  string
	DBPath   string
	Env      string
	LogLevel string

	// MasterKey encrypts card secrets and other at-rest sensitive values.
	MasterKey []byte

	// Bootstrap administrator, only applied when the database has no users.
	AdminUser     string
	AdminPassword string

	SessionTTL     time.Duration
	SessionIdle    time.Duration
	OrderTTL       time.Duration
	MaxBodyBytes   int64
	RateLimitRPS   float64
	RateLimitBurst int
	DBBusyTimeout  time.Duration
	ReadPoolSize   int

	TrustedProxies []string

	// Plugin security.
	RequirePluginSignature bool
	PluginPublicKeys       []string
	PluginDir              string

	// Marketplace.
	MarketURL          string
	MarketAllowInstall bool

	// Outbound HTTP.
	OutboundTimeout time.Duration

	ShowVersion bool
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer: %w", key, err)
	}
	return n, nil
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(env(key, ""))
	switch v {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a duration such as 24h: %w", key, err)
	}
	return d, nil
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Load builds a Config from flags (server run mode) and the environment.
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("cardword", flag.ContinueOnError)
	addr := fs.String("addr", env("CARDWORD_ADDR", ":8080"), "listen address")
	dataDir := fs.String("data", env("CARDWORD_DATA_DIR", "./data"), "data directory")
	baseURL := fs.String("base-url", env("CARDWORD_BASE_URL", ""), "public base URL, e.g. https://shop.example.com")
	envName := fs.String("env", env("CARDWORD_ENV", "production"), "environment: production or development")
	logLevel := fs.String("log-level", env("CARDWORD_LOG_LEVEL", "info"), "log level: debug, info, warn, error")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *showVersion {
		return &Config{ShowVersion: true}, nil
	}

	cfg := &Config{
		Addr:        *addr,
		DataDir:     *dataDir,
		BaseURL:     strings.TrimRight(*baseURL, "/"),
		Env:         strings.ToLower(*envName),
		LogLevel:    strings.ToLower(*logLevel),
		ShowVersion: *showVersion,
	}
	if cfg.Env != "development" && cfg.Env != "production" {
		return nil, fmt.Errorf("config: env must be production or development, got %q", cfg.Env)
	}

	cfg.DBPath = env("CARDWORD_DB_PATH", filepath.Join(cfg.DataDir, "cardword.db"))
	cfg.PluginDir = env("CARDWORD_PLUGIN_DIR", filepath.Join(cfg.DataDir, "plugins"))
	cfg.AdminUser = env("CARDWORD_ADMIN_USER", "admin")
	cfg.AdminPassword = env("CARDWORD_ADMIN_PASSWORD", "")
	cfg.MarketURL = env("CARDWORD_MARKET_URL", "https://zinvmeng.com/market/index.json")
	cfg.MarketAllowInstall = envBool("CARDWORD_MARKET_ALLOW_INSTALL", true)
	cfg.RequirePluginSignature = envBool("CARDWORD_PLUGIN_REQUIRE_SIGNATURE", false)
	cfg.PluginPublicKeys = splitList(env("CARDWORD_PLUGIN_PUBLIC_KEYS", ""))
	cfg.TrustedProxies = splitList(env("CARDWORD_TRUSTED_PROXIES", ""))

	var err error
	if cfg.SessionTTL, err = envDuration("CARDWORD_SESSION_TTL", 12*time.Hour); err != nil {
		return nil, err
	}
	if cfg.SessionIdle, err = envDuration("CARDWORD_SESSION_IDLE", 2*time.Hour); err != nil {
		return nil, err
	}
	if cfg.OrderTTL, err = envDuration("CARDWORD_ORDER_TTL", 15*time.Minute); err != nil {
		return nil, err
	}
	if cfg.DBBusyTimeout, err = envDuration("CARDWORD_DB_BUSY_TIMEOUT", 10*time.Second); err != nil {
		return nil, err
	}
	if cfg.OutboundTimeout, err = envDuration("CARDWORD_OUTBOUND_TIMEOUT", 20*time.Second); err != nil {
		return nil, err
	}
	maxBody, err := envInt("CARDWORD_MAX_BODY_BYTES", 8<<20)
	if err != nil {
		return nil, err
	}
	cfg.MaxBodyBytes = int64(maxBody)
	rps, err := envInt("CARDWORD_RATE_LIMIT_RPS", 20)
	if err != nil {
		return nil, err
	}
	cfg.RateLimitRPS = float64(rps)
	burst, err := envInt("CARDWORD_RATE_LIMIT_BURST", 60)
	if err != nil {
		return nil, err
	}
	cfg.RateLimitBurst = burst
	pool, err := envInt("CARDWORD_DB_READ_POOL", maxInt(4, cryptoutil.NumCPU()*2))
	if err != nil {
		return nil, err
	}
	cfg.ReadPoolSize = pool
	if cfg.BaseURL == "" {
		host, port, err := net.SplitHostPort(cfg.Addr)
		if err != nil {
			return nil, fmt.Errorf("config: invalid listen address: %w", err)
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		cfg.BaseURL = "http://" + net.JoinHostPort(host, port)
	}
	public, err := url.Parse(cfg.BaseURL)
	if err != nil || (public.Scheme != "http" && public.Scheme != "https") || public.Hostname() == "" || public.User != nil || public.RawQuery != "" || public.Fragment != "" || public.Path != "" {
		return nil, errors.New("config: base-url must be an HTTP(S) origin without credentials, path, query or fragment")
	}

	key := env("CARDWORD_MASTER_KEY", "")
	if key == "" {
		if cfg.Env == "production" {
			// A production deployment without an explicit key would silently
			// lose the ability to decrypt card secrets after a restart, so we
			// generate one file-backed key and keep going.
			generated, err := ensureKeyFile(filepath.Join(cfg.DataDir, "master.key"))
			if err != nil {
				return nil, err
			}
			cfg.MasterKey = generated
		} else {
			generated, err := ensureKeyFile(filepath.Join(cfg.DataDir, "master.key"))
			if err != nil {
				return nil, err
			}
			cfg.MasterKey = generated
		}
	} else {
		parsed, err := cryptoutil.ParseKey(key)
		if err != nil {
			return nil, fmt.Errorf("config: CARDWORD_MASTER_KEY: %w", err)
		}
		cfg.MasterKey = parsed
	}

	if cfg.RequirePluginSignature && len(cfg.PluginPublicKeys) == 0 {
		return nil, errors.New("config: plugin signature is required but no public keys are configured")
	}
	return cfg, nil
}

func ensureKeyFile(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		key, err := cryptoutil.ParseKey(string(b))
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", path, err)
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("config: read key file: %w", err)
	}
	key, err := cryptoutil.RandomBytes(32)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("config: create data dir: %w", err)
	}
	encoded := cryptoutil.Base64URL(key)
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return nil, fmt.Errorf("config: write key file: %w", err)
	}
	return key, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// IsDevelopment reports whether the server runs in development mode.
func (c *Config) IsDevelopment() bool { return c.Env == "development" }

// Secure reports whether cookies should carry the Secure attribute.
func (c *Config) Secure() bool { return strings.HasPrefix(c.BaseURL, "https://") }

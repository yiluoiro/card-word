// Command cardword starts the card word server: a lightweight, modular
// digital goods delivery platform with a plugin marketplace.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/yiluoiro/card-word/internal/config"
	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/httpapi"
	"github.com/yiluoiro/card-word/internal/market"
	"github.com/yiluoiro/card-word/internal/notify"
	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/plugin"
	"github.com/yiluoiro/card-word/internal/store"
	"github.com/yiluoiro/card-word/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cardword: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return err
	}
	if cfg.ShowVersion {
		fmt.Printf("card word %s (%s, built %s)\n", version.Version, version.Commit, version.Date)
		return nil
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)
	logger.Info("starting card word", "version", version.Version, "env", cfg.Env, "addr", cfg.Addr)

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	db, err := store.Open(store.Options{
		Path:         cfg.DBPath,
		ReadPoolSize: cfg.ReadPoolSize,
		BusyTimeout:  cfg.DBBusyTimeout,
		MasterKey:    cfg.MasterKey,
	})
	if err != nil {
		return err
	}
	defer db.Close()
	logger.Info("database ready", "path", db.Path())

	if err := bootstrapAdmin(context.Background(), cfg, db, logger); err != nil {
		return err
	}

	// Plugins.
	publicKeys, err := plugin.ParsePublicKeys(cfg.PluginPublicKeys)
	if err != nil {
		return err
	}
	installer := plugin.NewInstaller(cfg.PluginDir, cfg.RequirePluginSignature, publicKeys)
	host := plugin.NewHost(db, installer, logger)
	if err := host.Reload(context.Background()); err != nil {
		logger.Warn("plugin reload failed", "error", err)
	}

	payments := payment.NewRegistry()
	notifier := notify.NewDispatcher(cfg.OutboundTimeout, func(format string, args ...any) {
		logger.Warn(fmt.Sprintf(format, args...))
	})
	marketClient := market.NewClient(market.Builtin())

	tmpl, err := httpapi.Templates()
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}

	srv, err := httpapi.New(httpapi.Deps{
		Config:    cfg,
		DB:        db,
		Logger:    logger,
		Plugins:   host,
		Installer: installer,
		Market:    marketClient,
		Payments:  payments,
		Notifier:  notifier,
		Templates: tmpl,
	})
	if err != nil {
		return err
	}
	// Load payment and mail configuration from the settings table.
	httpapi.LoadIntegrations(srv, context.Background())

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go housekeeping(ctx, db, logger)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.Addr, "base_url", cfg.BaseURL)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		return err
	}
	if err := db.WALCheckpoint(shutdownCtx); err != nil {
		logger.Warn("wal checkpoint failed", "error", err)
	}
	logger.Info("stopped cleanly")
	return nil
}

func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.IsDevelopment() {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

// bootstrapAdmin creates the first operator account exactly once.
func bootstrapAdmin(ctx context.Context, cfg *config.Config, db *store.DB, logger *slog.Logger) error {
	count, err := db.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	password := cfg.AdminPassword
	generated := false
	if password == "" {
		password, err = cryptoutil.RandomToken(18)
		if err != nil {
			return err
		}
		generated = true
	}
	user, err := db.CreateUser(ctx, cfg.AdminUser, password, "admin")
	if err != nil {
		return fmt.Errorf("create bootstrap admin: %w", err)
	}
	if err := db.Audit(ctx, "system", "auth.bootstrap", user.Username, "", "initial administrator created"); err != nil {
		logger.Warn("audit failed during bootstrap", "error", err)
	}
	if generated {
		path := filepath.Join(cfg.DataDir, "initial-admin.txt")
		note := fmt.Sprintf("username: %s\npassword: %s\n\n请登录后立即修改密码，并删除本文件。\n", user.Username, password)
		if err := os.WriteFile(path, []byte(note), 0o600); err != nil {
			logger.Warn("cannot write initial credentials file", "error", err)
		}
		logger.Warn("generated initial administrator credentials",
			"username", user.Username, "password_file", path)
	}
	// Seed a usable storefront so a fresh install is not empty.
	if err := seed(ctx, db); err != nil {
		logger.Warn("seed failed", "error", err)
	}
	return nil
}

func seed(ctx context.Context, db *store.DB) error {
	if err := db.SetSetting(ctx, store.SettingSiteName, "Card Word"); err != nil {
		return err
	}
	_ = db.SetSetting(ctx, store.SettingSiteTagline, "即时自动发卡 · 安全 · 轻量")
	_ = db.SetSetting(ctx, store.SettingCurrency, "CNY")
	_ = db.SetSetting(ctx, store.SettingPaymentDriver, "manual")

	category, err := db.CreateCategory(ctx, "默认分类", "default", 0, true)
	if err != nil {
		return err
	}
	product, err := db.CreateProduct(ctx, store.ProductInput{
		Name:        "示例会员卡（可删除）",
		Slug:        "demo-card",
		CategoryID:  category.ID,
		Summary:     "这是一条演示商品，用于验证下单与自动发货流程。",
		Description: "下单并确认支付后，系统会从卡密池中自动取出一条卡密并展示给买家。",
		PriceCents:  990,
		Currency:    "CNY",
		Status:      store.ProductActive,
		MinQty:      1,
		MaxQty:      5,
	})
	if err != nil {
		return err
	}
	_, err = db.AddCards(ctx, product.ID, []string{"DEMO-KEY-0001-AAAA", "DEMO-KEY-0002-BBBB", "DEMO-KEY-0003-CCCC"}, "seed")
	return err
}

// housekeeping runs the periodic maintenance loop.
func housekeeping(ctx context.Context, db *store.DB, logger *slog.Logger) {
	orderTicker := time.NewTicker(time.Minute)
	sessionTicker := time.NewTicker(30 * time.Minute)
	defer orderTicker.Stop()
	defer sessionTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-orderTicker.C:
			if n, err := db.CloseExpiredOrders(ctx); err != nil {
				logger.Warn("close expired orders failed", "error", err)
			} else if n > 0 {
				logger.Info("closed expired orders", "count", n)
			}
		case <-sessionTicker.C:
			if n, err := db.PurgeExpiredSessions(ctx); err != nil {
				logger.Warn("purge sessions failed", "error", err)
			} else if n > 0 {
				logger.Debug("purged sessions", "count", n)
			}
		}
	}
}

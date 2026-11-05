// Command cardword starts the card word server: a lightweight, modular
// digital goods delivery platform with a plugin marketplace.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/yiluoiro/card-word/internal/config"
	"github.com/yiluoiro/card-word/internal/httpapi"
	"github.com/yiluoiro/card-word/internal/market"
	"github.com/yiluoiro/card-word/internal/notify"
	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/plugin"
	"github.com/yiluoiro/card-word/internal/store"
	"github.com/yiluoiro/card-word/internal/update"
	"github.com/yiluoiro/card-word/internal/version"
)

func main() {
	if err := run(); err != nil {
		if errors.Is(err, errUpdateRestart) {
			os.Exit(restartExit)
		}
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
	if os.Getenv("CARDWORD_INTERNAL_WORKER") != "1" {
		return supervise(cfg)
	}
	updates := update.New(cfg.DataDir, version.Version, cfg.MasterKey)
	restart := make(chan struct{}, 1)

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
	marketClient := market.NewClient(nil)

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
		Updates:   updates,
		Restart: func() {
			select {
			case restart <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		return err
	}
	// Load payment and mail configuration from the settings table.
	httpapi.LoadIntegrations(srv, context.Background())
	handler, err := srv.WithInstallation(srv.Handler())
	if err != nil {
		return fmt.Errorf("prepare installation: %w", err)
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// EOF also shuts down an orphaned worker if the launcher is terminated.
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); stop() }()

	var background sync.WaitGroup
	background.Add(1)
	defer func() { stop(); background.Wait() }()
	go func() {
		defer background.Done()
		housekeeping(ctx, db, logger)
	}()

	errCh := make(chan error, 1)
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	if os.Getenv("CARDWORD_INTERNAL_TRIAL") == "true" {
		if err := updates.Confirm(); err != nil {
			return err
		}
	}
	go func() {
		logger.Info("http server listening", "addr", cfg.Addr, "base_url", cfg.BaseURL)
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	updating := false
	select {
	case <-restart:
		updating = true
		stop()
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
	if updating {
		return errUpdateRestart
	}
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
		// Databases created before roles existed only have 'admin' accounts.
		// Promote the oldest one so the instance always has an owner.
		if owners, err := db.CountOwners(ctx); err == nil && owners == 0 {
			if users, err := db.ListUsers(ctx); err == nil && len(users) > 0 {
				oldest := users[0]
				for _, u := range users {
					if u.ID < oldest.ID {
						oldest = u
					}
				}
				if err := db.SetUserRole(ctx, oldest.ID, store.RoleOwner); err != nil {
					logger.Warn("failed to promote initial owner", "error", err)
				} else {
					logger.Info("promoted oldest account to owner", "username", oldest.Username)
				}
			}
		}
		return nil
	}
	password := cfg.AdminPassword
	complete, err := db.GetSetting(ctx, "installation.completed", "")
	if err != nil {
		return err
	}
	if complete == "1" || password == "" {
		return nil
	}
	if err := db.Install(ctx, store.Installation{SiteName: "Card Word", Username: cfg.AdminUser, Password: password}); err != nil {
		return fmt.Errorf("create bootstrap admin: %w", err)
	}
	logger.Info("initial administrator created from environment", "username", cfg.AdminUser)
	return nil
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

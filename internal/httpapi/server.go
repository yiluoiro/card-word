// Package httpapi wires the HTTP surface of card word: the storefront, the
// admin console, the REST API and the plugin endpoints.
package httpapi

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yiluoiro/card-word/internal/config"
	"github.com/yiluoiro/card-word/internal/market"
	"github.com/yiluoiro/card-word/internal/notify"
	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/plugin"
	"github.com/yiluoiro/card-word/internal/store"
	"github.com/yiluoiro/card-word/internal/version"
)

// Server holds every dependency the HTTP handlers need.
type Server struct {
	cfg       *config.Config
	db        *store.DB
	log       *slog.Logger
	plugins   *plugin.Host
	installer *plugin.Installer
	market    *market.Client
	payments  *payment.Registry
	notifier  *notify.Dispatcher

	tmpl      *template.Template
	mux       *http.ServeMux
	limiter   *rateLimiter
	authLimit *rateLimiter
	startedAt time.Time

	settingsMu sync.RWMutex
	settings   map[string]string

	dummyMu     sync.Mutex
	cachedDummy string
}

// Deps bundles the collaborators passed to New.
type Deps struct {
	Config    *config.Config
	DB        *store.DB
	Logger    *slog.Logger
	Plugins   *plugin.Host
	Installer *plugin.Installer
	Market    *market.Client
	Payments  *payment.Registry
	Notifier  *notify.Dispatcher
	Templates *template.Template
}

// New builds the server and registers all routes.
func New(d Deps) (*Server, error) {
	if d.Config == nil || d.DB == nil {
		return nil, errors.New("httpapi: config and database are required")
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		cfg:       d.Config,
		db:        d.DB,
		log:       log,
		plugins:   d.Plugins,
		installer: d.Installer,
		market:    d.Market,
		payments:  d.Payments,
		notifier:  d.Notifier,
		tmpl:      d.Templates,
		mux:       http.NewServeMux(),
		startedAt: time.Now(),
		settings:  map[string]string{},
	}
	if s.payments == nil {
		s.payments = payment.NewRegistry()
	}
	s.limiter = newRateLimiter(d.Config.RateLimitRPS, d.Config.RateLimitBurst)
	s.authLimit = newRateLimiter(0.2, 8) // ~12 login attempts per minute
	if err := s.reloadSettings(context.Background()); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

// Handler returns the fully wrapped handler stack.
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = s.csrfProtect(h)
	h = s.bodyLimit(h)
	h = s.sessionMiddleware(h)
	h = s.rateLimit(h)
	h = s.securityHeaders(h)
	h = s.accessLog(h)
	h = s.recoverer(h)
	h = s.requestID(h)
	return h
}

func (s *Server) routes() {
	// Public storefront.
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /p/{slug}", s.handleProduct)
	s.mux.HandleFunc("POST /checkout", s.handleCheckout)
	s.mux.HandleFunc("GET /order/{no}", s.handleOrderView)
	s.mux.HandleFunc("GET /order/{no}/status", s.handleOrderStatus)
	s.mux.HandleFunc("GET /order/{no}/pay", s.handlePay)
	s.mux.HandleFunc("GET /query", s.handleQueryForm)
	s.mux.HandleFunc("POST /query", s.handleQuerySubmit)
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /readyz", s.handleReady)
	s.mux.HandleFunc("GET /version", s.handleVersion)

	// Static assets and plugin assets.
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler()))
	s.mux.HandleFunc("GET /assets/plugins/{uid}/{path...}", s.handlePluginAsset)

	// Payment webhooks (public, signature verified).
	s.mux.HandleFunc("POST /pay/notify/{driver}", s.handlePaymentNotify)
	s.mux.HandleFunc("GET /pay/return/{driver}", s.handlePaymentReturn)
	s.mux.HandleFunc("POST /api/plugins/{uid}/notify", s.handlePluginNotify)

	// Machine API.
	s.mux.HandleFunc("GET /api/v1/ping", s.handleAPIPing)
	s.mux.HandleFunc("GET /api/v1/products", s.handleAPIProducts)
	s.mux.HandleFunc("GET /api/v1/products/{slug}", s.handleAPIProduct)
	s.mux.HandleFunc("GET /api/v1/orders/{no}", s.handleAPIOrder)
	s.mux.HandleFunc("POST /api/v1/orders", s.handleAPICreateOrder)
	s.mux.HandleFunc("POST /api/v1/orders/{no}/close", s.handleAPICloseOrder)
	s.mux.HandleFunc("POST /api/v1/cards", s.handleAPIAddCards)

	// Public plugin listings (marketplace preview) and downloads are admin
	// only, see below.

	// Admin.
	s.mux.HandleFunc("GET /admin/login", s.handleLoginForm)
	s.mux.HandleFunc("POST /admin/login", s.handleLoginSubmit)
	s.mux.HandleFunc("POST /admin/logout", s.handleLogout)
	s.mux.Handle("GET /admin", s.admin(s.handleDashboard))
	s.mux.Handle("GET /admin/products", s.admin(s.handleAdminProducts))
	s.mux.Handle("POST /admin/products", s.admin(s.handleAdminProductSave))
	s.mux.Handle("GET /admin/products/{id}", s.admin(s.handleAdminProductForm))
	s.mux.Handle("POST /admin/products/{id}/delete", s.admin(s.handleAdminProductDelete))
	s.mux.Handle("GET /admin/products/{id}/cards", s.admin(s.handleAdminCards))
	s.mux.Handle("POST /admin/products/{id}/cards", s.admin(s.handleAdminCardsImport))
	s.mux.Handle("POST /admin/cards/{id}/disable", s.admin(s.handleAdminCardDisable))
	s.mux.Handle("POST /admin/cards/{id}/reveal", s.admin(s.handleAdminCardReveal))
	s.mux.Handle("GET /admin/categories", s.admin(s.handleAdminCategories))
	s.mux.Handle("POST /admin/categories", s.admin(s.handleAdminCategorySave))
	s.mux.Handle("POST /admin/categories/{id}/delete", s.admin(s.handleAdminCategoryDelete))
	s.mux.Handle("GET /admin/orders", s.admin(s.handleAdminOrders))
	s.mux.Handle("GET /admin/orders/{no}", s.admin(s.handleAdminOrder))
	s.mux.Handle("POST /admin/orders/{no}/fulfill", s.admin(s.handleAdminFulfill))
	s.mux.Handle("POST /admin/orders/{no}/retry", s.admin(s.handleAdminRetry))
	s.mux.Handle("POST /admin/orders/{no}/close", s.admin(s.handleAdminCloseOrder))
	s.mux.Handle("POST /admin/orders/{no}/refund", s.admin(s.handleAdminRefund))
	s.mux.Handle("GET /admin/coupons", s.admin(s.handleAdminCoupons))
	s.mux.Handle("POST /admin/coupons", s.admin(s.handleAdminCouponCreate))
	s.mux.Handle("POST /admin/coupons/{id}/delete", s.admin(s.handleAdminCouponDelete))
	s.mux.Handle("GET /admin/plugins", s.admin(s.handleAdminPlugins))
	s.mux.Handle("POST /admin/plugins/upload", s.admin(s.handleAdminPluginUpload))
	s.mux.Handle("POST /admin/plugins/{uid}/enable", s.admin(s.handleAdminPluginEnable))
	s.mux.Handle("POST /admin/plugins/{uid}/disable", s.admin(s.handleAdminPluginDisable))
	s.mux.Handle("POST /admin/plugins/{uid}/delete", s.admin(s.handleAdminPluginDelete))
	s.mux.Handle("POST /admin/plugins/{uid}/settings", s.admin(s.handleAdminPluginSettings))
	s.mux.Handle("POST /admin/market/install", s.admin(s.handleAdminMarketInstall))
	s.mux.Handle("GET /admin/settings", s.admin(s.handleAdminSettings))
	s.mux.Handle("POST /admin/settings", s.admin(s.handleAdminSettingsSave))
	s.mux.Handle("GET /admin/tokens", s.admin(s.handleAdminTokens))
	s.mux.Handle("POST /admin/tokens", s.admin(s.handleAdminTokenCreate))
	s.mux.Handle("POST /admin/tokens/{id}/revoke", s.admin(s.handleAdminTokenRevoke))
	s.mux.Handle("GET /admin/audit", s.admin(s.handleAdminAudit))
	s.mux.HandleFunc("GET /admin/plugins/{uid}/console", s.handlePluginConsole)
}

// ---------- settings cache ----------

func (s *Server) reloadSettings(ctx context.Context) error {
	all, err := s.db.AllSettings(ctx)
	if err != nil {
		return err
	}
	s.settingsMu.Lock()
	s.settings = all
	s.settingsMu.Unlock()
	return nil
}

func (s *Server) setting(key, def string) string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	if v, ok := s.settings[key]; ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

// ---------- health ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "uptime_s": int(time.Since(s.startedAt).Seconds())})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "error": "database"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "card word",
		"version": version.Version,
		"commit":  version.Commit,
		"built":   version.Date,
	})
}

// clientIP resolves the caller address, honouring trusted proxies only.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if len(s.cfg.TrustedProxies) > 0 {
		for _, p := range s.cfg.TrustedProxies {
			if p == host || p == "*" {
				if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
					parts := strings.Split(xff, ",")
					candidate := strings.TrimSpace(parts[len(parts)-1])
					if net.ParseIP(candidate) != nil {
						return candidate
					}
				}
				if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" && net.ParseIP(ip) != nil {
					return ip
				}
				break
			}
		}
	}
	return host
}

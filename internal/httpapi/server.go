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
	"github.com/yiluoiro/card-word/internal/update"
	"github.com/yiluoiro/card-word/internal/version"
)

// Server holds every dependency the HTTP handlers need.
type Server struct {
	updates   *update.Manager
	restart   func()
	cfg       *config.Config
	db        *store.DB
	log       *slog.Logger
	plugins   *plugin.Host
	installer *plugin.Installer
	market    *market.Client
	payments  *payment.Registry
	notifier  *notify.Dispatcher

	tmpl               *template.Template
	mux                *http.ServeMux
	limiter            *rateLimiter
	authLimit          *rateLimiter
	captchaLimit       *rateLimiter
	publicCaptchaLimit *rateLimiter
	supportLimit       *rateLimiter
	captchas           captchaStore
	startedAt          time.Time

	settingsMu sync.RWMutex
	settings   map[string]string

	dummyMu     sync.Mutex
	cachedDummy string

	guard      *loginGuard
	apiLimiter *rateLimiter
}

// Deps bundles the collaborators passed to New.
type Deps struct {
	Updates   *update.Manager
	Restart   func()
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
		updates: d.Updates, restart: d.Restart,
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
		guard:     newLoginGuard(),
	}
	if s.payments == nil {
		s.payments = payment.NewRegistry()
	}
	s.limiter = newRateLimiter(d.Config.RateLimitRPS, d.Config.RateLimitBurst)
	s.authLimit = newRateLimiter(0.2, 8) // ~12 login attempts per minute
	s.publicCaptchaLimit = newRateLimiter(0.3, 20)
	s.captchaLimit = newRateLimiter(0.2, 8)
	s.supportLimit = newRateLimiter(0.5, 20)
	// Machine clients get their own budget keyed by token id, so one noisy
	// integration cannot exhaust the shared IP bucket.
	s.apiLimiter = newRateLimiter(30, 120)
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
	h = s.customerMiddleware(h)
	h = s.sessionMiddleware(h)
	h = s.rateLimit(h)
	h = s.securityHeaders(h)
	h = s.accessLog(h)
	h = s.recoverer(h)
	h = s.requestID(h)

	return h
}

func (s *Server) routes() {
	s.mux.Handle("GET /admin/update", s.staff(store.RoleViewer, s.handleUpdateCheck))
	s.mux.Handle("POST /admin/update/repository", s.staff(store.RoleOwner, s.handleUpdateRepository))
	s.mux.Handle("POST /admin/update/install", s.staff(store.RoleOwner, s.handleUpdateInstall))
	s.mux.Handle("POST /admin/update/restart", s.staff(store.RoleOwner, s.handleUpdateRestart))
	s.mux.Handle("GET /admin/announcements", s.staff(store.RoleViewer, s.handleAnnouncements))
	s.mux.Handle("POST /admin/announcements", s.staff(store.RoleAdmin, s.handleAnnouncementSave))
	s.mux.Handle("POST /admin/announcements/{id}/delete", s.staff(store.RoleAdmin, s.handleAnnouncementDelete))
	s.mux.Handle("GET /admin/appearance", s.staff(store.RoleOwner, s.handleAppearance))
	s.mux.Handle("POST /admin/appearance", s.staff(store.RoleOwner, s.handleAppearanceSave))
	s.mux.Handle("POST /admin/appearance/preview", s.staff(store.RoleOwner, s.handleAppearancePreview))
	// Buyers have separate accounts, cookies and authorization from operators.
	s.mux.HandleFunc("GET /account", s.customerOnly(s.handleCustomerAccount))
	s.mux.HandleFunc("GET /account/login", s.handleCustomerAuthForm)
	s.mux.HandleFunc("GET /account/register", s.handleCustomerAuthForm)
	s.mux.HandleFunc("POST /account/login", s.handleCustomerAuth)
	s.mux.HandleFunc("POST /account/register", s.handleCustomerAuth)
	s.mux.HandleFunc("POST /account/logout", s.customerOnly(s.handleCustomerLogout))
	s.mux.HandleFunc("POST /account/password", s.customerOnly(s.handleCustomerPassword))
	// Public storefront.
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /p/{slug}", s.handleProduct)
	s.mux.HandleFunc("POST /checkout", s.handleCheckout)
	s.mux.HandleFunc("GET /order/{no}", s.handleOrderView)
	s.mux.HandleFunc("GET /order/{no}/status", s.handleOrderStatus)
	s.mux.HandleFunc("GET /order/{no}/pay", s.handlePay)
	s.mux.HandleFunc("POST /captcha", s.handleFormCaptcha)
	s.mux.HandleFunc("POST /order/{no}/pay", s.handlePay)
	s.mux.HandleFunc("POST /order/{no}/verify", s.handleOrderView)
	s.mux.HandleFunc("GET /query", s.handleQueryForm)
	s.mux.HandleFunc("POST /query", s.handleQuerySubmit)
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /readyz", s.handleReady)
	s.mux.HandleFunc("GET /version", s.handleVersion)
	s.mux.HandleFunc("GET /sitemap.xml", s.handleSitemap)
	s.mux.HandleFunc("GET /robots.txt", s.handleRobots)

	// Static assets and plugin assets.
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler()))
	s.mux.HandleFunc("GET /media/{name}", s.handleMedia)
	s.mux.HandleFunc("GET /assets/plugins/{uid}/{path...}", s.handlePluginAsset)

	// Payment webhooks (public, signature verified).
	s.mux.HandleFunc("POST /pay/notify/{driver}", s.handlePaymentNotify)
	s.mux.HandleFunc("GET /pay/notify/{driver}", s.handlePaymentNotify)
	s.mux.Handle("GET /admin/payments", s.staff(store.RoleAdmin, s.handlePaymentsConfig))
	s.mux.Handle("POST /admin/payments", s.staff(store.RoleAdmin, s.handlePaymentsSave))
	s.mux.HandleFunc("GET /pay/return/{driver}", s.handlePaymentReturn)
	s.mux.HandleFunc("POST /api/plugins/{uid}/notify", s.handlePluginNotify)

	// Machine API.
	s.mux.HandleFunc("GET /api/v1/ping", s.handleAPIPing)
	s.mux.HandleFunc("GET /api/v1/categories", s.handleAPICategories)
	s.mux.HandleFunc("GET /api/v1/products", s.handleAPIProducts)
	s.mux.HandleFunc("GET /api/v1/products/{slug}", s.handleAPIProduct)
	s.mux.HandleFunc("GET /api/v1/orders/{no}", s.handleAPIOrder)
	s.mux.HandleFunc("POST /api/v1/orders", s.handleAPICreateOrder)
	s.mux.HandleFunc("POST /api/v1/orders/{no}/close", s.handleAPICloseOrder)
	s.mux.HandleFunc("POST /api/v1/cards", s.handleAPIAddCards)

	// Public plugin listings (marketplace preview) and downloads are admin
	// only, see below.

	// Admin.
	s.mux.Handle("GET /admin/seo", s.staff(store.RoleViewer, s.handleSEOSettings))
	s.mux.Handle("POST /admin/seo", s.staff(store.RoleAdmin, s.handleSEOSettingsSave))
	s.mux.Handle("GET /admin/lists/{kind}/{id}", s.staff(store.RoleAdmin, s.handleListEdit))
	s.mux.Handle("POST /admin/lists/{kind}/{id}", s.staff(store.RoleAdmin, s.handleListSave))
	s.mux.HandleFunc("GET /admin/login", s.handleLoginForm)
	s.mux.HandleFunc("POST /admin/login", s.handleLoginSubmit)
	s.mux.HandleFunc("POST /admin/captcha", s.handleCaptchaRefresh)
	s.mux.HandleFunc("GET /admin/login/2fa", s.handleLoginTwoFactorForm)
	s.mux.HandleFunc("POST /admin/login/2fa", s.handleLoginTwoFactorSubmit)
	s.mux.HandleFunc("POST /admin/logout", s.handleLogout)
	s.mux.Handle("GET /admin", s.staff(store.RoleViewer, s.handleDashboard))
	s.mux.Handle("GET /admin/customers", s.staff(store.RoleViewer, s.handleAdminCustomers))
	s.mux.Handle("GET /admin/products", s.staff(store.RoleViewer, s.handleAdminProducts))
	s.mux.Handle("POST /admin/products", s.staff(store.RoleAdmin, s.handleAdminProductSave))
	s.mux.Handle("POST /admin/markdown/preview", s.staff(store.RoleAdmin, s.handleMarkdownPreview))
	s.mux.Handle("POST /admin/markdown/image", s.staff(store.RoleAdmin, s.handleMarkdownImage))
	s.mux.Handle("POST /admin/products/bulk", s.staff(store.RoleAdmin, s.handleAdminProductsBulk))
	s.mux.Handle("GET /admin/products/{id}", s.staff(store.RoleViewer, s.handleAdminProductForm))
	s.mux.Handle("POST /admin/products/{id}/delete", s.staff(store.RoleAdmin, s.handleAdminProductDelete))
	s.mux.Handle("GET /admin/products/{id}/cards", s.staff(store.RoleViewer, s.handleAdminCards))
	s.mux.Handle("POST /admin/products/{id}/cards", s.staff(store.RoleAdmin, s.handleAdminCardsImport))
	s.mux.Handle("POST /admin/products/{id}/cards/bulk", s.staff(store.RoleAdmin, s.handleAdminCardsBulk))
	s.mux.Handle("GET /admin/products/{id}/cards/export.csv", s.staff(store.RoleAdmin, s.handleAdminCardsExport))
	s.mux.Handle("POST /admin/products/{id}/cover", s.staff(store.RoleAdmin, s.handleProductCoverUpload))
	s.mux.Handle("POST /admin/products/{id}/variants", s.staff(store.RoleAdmin, s.handleProductVariant))
	s.mux.Handle("POST /admin/cards/{id}/disable", s.staff(store.RoleAdmin, s.handleAdminCardDisable))
	s.mux.Handle("POST /admin/cards/{id}/reveal", s.staff(store.RoleAdmin, s.handleAdminCardReveal))
	s.mux.Handle("GET /admin/categories", s.admin(s.handleAdminCategories))
	s.mux.Handle("POST /admin/categories", s.admin(s.handleAdminCategorySave))
	s.mux.Handle("POST /admin/categories/{id}/delete", s.admin(s.handleAdminCategoryDelete))
	s.mux.Handle("GET /admin/orders", s.staff(store.RoleViewer, s.handleAdminOrders))
	s.mux.Handle("GET /admin/orders/export.csv", s.staff(store.RoleAdmin, s.handleAdminOrdersExport))
	s.mux.Handle("GET /admin/orders/{no}", s.staff(store.RoleViewer, s.handleAdminOrder))
	s.mux.Handle("POST /admin/orders/{no}/fulfill", s.staff(store.RoleAdmin, s.handleAdminFulfill))
	s.mux.Handle("POST /admin/orders/{no}/retry", s.staff(store.RoleAdmin, s.handleAdminRetry))
	s.mux.Handle("POST /admin/orders/{no}/close", s.staff(store.RoleAdmin, s.handleAdminCloseOrder))
	s.mux.Handle("POST /admin/orders/{no}/refund", s.staff(store.RoleAdmin, s.handleAdminRefund))
	s.mux.Handle("POST /admin/orders/{no}/note", s.staff(store.RoleAdmin, s.handleAdminOrderNote))
	s.mux.Handle("GET /admin/coupons", s.staff(store.RoleViewer, s.handleAdminCoupons))
	s.mux.Handle("POST /admin/coupons", s.staff(store.RoleAdmin, s.handleAdminCouponCreate))
	s.mux.Handle("POST /admin/coupons/{id}/delete", s.staff(store.RoleAdmin, s.handleAdminCouponDelete))
	s.mux.Handle("GET /admin/plugins", s.staff(store.RoleViewer, s.handleAdminPlugins))
	s.mux.Handle("POST /admin/plugins/upload", s.staff(store.RoleAdmin, s.handleAdminPluginUpload))
	s.mux.Handle("POST /admin/plugins/{uid}/enable", s.staff(store.RoleAdmin, s.handleAdminPluginEnable))
	s.mux.Handle("POST /admin/plugins/{uid}/disable", s.staff(store.RoleAdmin, s.handleAdminPluginDisable))
	s.mux.Handle("POST /admin/plugins/{uid}/delete", s.staff(store.RoleAdmin, s.handleAdminPluginDelete))
	s.mux.Handle("POST /admin/plugins/{uid}/settings", s.staff(store.RoleAdmin, s.handleAdminPluginSettings))
	s.mux.Handle("POST /admin/market/install", s.staff(store.RoleAdmin, s.handleAdminMarketInstall))
	s.mux.Handle("GET /admin/settings", s.staff(store.RoleViewer, s.handleAdminSettings))
	s.mux.Handle("POST /admin/settings/icon", s.staff(store.RoleAdmin, s.handleSiteIcon))
	s.mux.Handle("POST /admin/settings", s.staff(store.RoleAdmin, s.handleAdminSettingsSave))
	s.mux.Handle("GET /admin/tokens", s.staff(store.RoleViewer, s.handleAdminTokens))
	s.mux.Handle("POST /admin/tokens", s.staff(store.RoleAdmin, s.handleAdminTokenCreate))
	s.mux.Handle("POST /admin/tokens/{id}/revoke", s.staff(store.RoleAdmin, s.handleAdminTokenRevoke))
	s.mux.Handle("GET /admin/audit", s.staff(store.RoleViewer, s.handleAdminAudit))
	s.mux.Handle("GET /admin/audit/export.csv", s.staff(store.RoleAdmin, s.handleAdminAuditExport))
	s.mux.Handle("POST /admin/settings/test-email", s.staff(store.RoleAdmin, s.handleAdminTestEmail))
	s.mux.Handle("GET /admin/account", s.staff(store.RoleViewer, s.handleAdminAccount))
	s.mux.Handle("POST /admin/account/password", s.admin(s.handleAdminChangePassword))
	s.mux.Handle("GET /admin/users", s.staff(store.RoleOwner, s.handleAdminUsers))
	s.mux.Handle("POST /admin/users", s.staff(store.RoleOwner, s.handleAdminUserCreate))
	s.mux.Handle("POST /admin/users/{id}/role", s.staff(store.RoleOwner, s.handleAdminUserRole))
	s.mux.Handle("POST /admin/users/{id}/disable", s.staff(store.RoleOwner, s.handleAdminUserDisable))
	s.mux.Handle("POST /admin/users/{id}/delete", s.staff(store.RoleOwner, s.handleAdminUserDelete))
	s.mux.Handle("POST /admin/account/2fa/start", s.admin(s.handleTOTPStart))
	s.mux.Handle("POST /admin/account/2fa/enable", s.admin(s.handleTOTPEnable))
	s.mux.Handle("POST /admin/account/2fa/disable", s.admin(s.handleTOTPDisable))
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
	w.Header().Set("Cache-Control", "no-store")
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

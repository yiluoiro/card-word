package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/store"
)

// Runtime is a live, enabled plugin instance.
type Runtime struct {
	UID      string
	Manifest Manifest
	Record   store.PluginRecord
	Secret   []byte
	Client   *http.Client
	Static   http.Handler
}

// Host owns the plugin lifecycle and mediates all plugin calls.
type Host struct {
	store     *store.DB
	installer *Installer
	log       *slog.Logger
	mu        sync.RWMutex
	active    map[string]*Runtime
}

// NewHost builds a host. The caller must call Reload before serving traffic.
func NewHost(db *store.DB, installer *Installer, log *slog.Logger) *Host {
	if log == nil {
		log = slog.Default()
	}
	return &Host{store: db, installer: installer, log: log, active: map[string]*Runtime{}}
}

// Reload rebuilds the active runtime table from the database.
func (h *Host) Reload(ctx context.Context) error {
	records, err := h.store.ListPlugins(ctx, true)
	if err != nil {
		return err
	}
	next := make(map[string]*Runtime, len(records))
	for _, rec := range records {
		m, err := ParseManifest([]byte(rec.Manifest))
		if err != nil {
			h.log.Warn("plugin: skipping plugin with invalid manifest", "uid", rec.UID, "error", err)
			_ = h.store.SetPluginError(ctx, rec.UID, err.Error())
			continue
		}
		rt, err := h.buildRuntime(ctx, rec, m)
		if err != nil {
			h.log.Warn("plugin: failed to activate", "uid", rec.UID, "error", err)
			_ = h.store.SetPluginError(ctx, rec.UID, err.Error())
			continue
		}
		next[rec.UID] = rt
	}
	h.mu.Lock()
	h.active = next
	h.mu.Unlock()
	return nil
}

func (h *Host) buildRuntime(ctx context.Context, rec store.PluginRecord, m *Manifest) (*Runtime, error) {
	rt := &Runtime{UID: rec.UID, Manifest: *m, Record: rec}
	secret, err := h.store.GetPluginSetting(ctx, rec.UID, "_secret", "")
	if err != nil {
		return nil, err
	}
	if secret == "" {
		token, err := cryptoutil.RandomToken(32)
		if err != nil {
			return nil, err
		}
		secret = token
		if err := h.store.SetPluginSetting(ctx, rec.UID, "_secret", secret); err != nil {
			return nil, err
		}
	}
	rt.Secret = []byte(secret)

	switch m.Runtime.Type {
	case "external":
		timeout := time.Duration(m.Runtime.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		rt.Client = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        32,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     60 * time.Second,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				// A plugin must never redirect the core to another origin.
				return http.ErrUseLastResponse
			},
		}
	case "static":
		dir := filepath.Join(h.installer.Dir, rec.UID)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("plugin: missing directory %s", dir)
		}
		rt.Static = http.FileServer(http.Dir(dir))
	}
	return rt, nil
}

// RuntimeLookup returns an active runtime.
func (h *Host) RuntimeLookup(uid string) (*Runtime, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	rt, ok := h.active[uid]
	return rt, ok
}

// Active returns every active runtime.
func (h *Host) Active() []*Runtime {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*Runtime, 0, len(h.active))
	for _, rt := range h.active {
		out = append(out, rt)
	}
	return out
}

// Install stores a package and registers it as disabled.
func (h *Host) Install(ctx context.Context, pkg []byte, signature, source string) (*Manifest, error) {
	installed, err := h.installer.Install(ctx, pkg, signature, source)
	if err != nil {
		return nil, err
	}
	manifestJSON, err := json.Marshal(installed.Manifest)
	if err != nil {
		return nil, err
	}
	rec := &store.PluginRecord{
		UID:         installed.Manifest.UID,
		Name:        installed.Manifest.Name,
		Version:     installed.Manifest.Version,
		Kind:        installed.Manifest.Kind,
		Manifest:    string(manifestJSON),
		Dir:         installed.Dir,
		Enabled:     false,
		SHA256:      installed.SHA256,
		Signature:   installed.Signature,
		Publisher:   installed.Manifest.Author,
		Permissions: strings.Join(installed.Manifest.Permissions, ","),
		Source:      source,
	}
	if err := h.store.UpsertPlugin(ctx, rec); err != nil {
		_ = h.installer.Uninstall(installed.Manifest.UID)
		return nil, err
	}
	return installed.Manifest, nil
}

// Enable turns a plugin on and activates it.
func (h *Host) Enable(ctx context.Context, uid string) error {
	rec, err := h.store.PluginByUID(ctx, uid)
	if err != nil {
		return err
	}
	m, err := ParseManifest([]byte(rec.Manifest))
	if err != nil {
		return err
	}
	if m.Runtime.Type == "external" && m.Runtime.HealthPath != "" {
		if err := h.checkHealth(ctx, m); err != nil {
			_ = h.store.SetPluginError(ctx, uid, err.Error())
			return err
		}
	}
	if err := h.store.SetPluginEnabled(ctx, uid, true); err != nil {
		return err
	}
	if err := h.Reload(ctx); err != nil {
		return err
	}
	h.CallHook(ctx, "plugin.enabled", map[string]any{"plugin_uid": uid})
	return nil
}

// Disable turns a plugin off.
func (h *Host) Disable(ctx context.Context, uid string) error {
	if err := h.store.SetPluginEnabled(ctx, uid, false); err != nil {
		return err
	}
	return h.Reload(ctx)
}

// Uninstall removes a plugin from disk and from the database.
func (h *Host) Uninstall(ctx context.Context, uid string) error {
	if err := h.Disable(ctx, uid); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err := h.installer.Uninstall(uid); err != nil {
		return err
	}
	return h.store.DeletePlugin(ctx, uid)
}

func (h *Host) checkHealth(ctx context.Context, m *Manifest) error {
	if !m.HasPermission(PermHTTPOutbound) || !validEndpointPath(m.Runtime.HealthPath) {
		return errors.New("plugin: health check not permitted")
	}
	url := m.Runtime.BaseURL + m.Runtime.HealthPath
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(req)
	if err != nil {
		return fmt.Errorf("plugin: health check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("plugin: health check returned %d", resp.StatusCode)
	}
	return nil
}

// CallHook fans an event out to subscribed plugins. Failures are logged and
// never block or fail the caller's business transaction.
func (h *Host) CallHook(ctx context.Context, event string, payload map[string]any) {
	h.mu.RLock()
	targets := make([]*Runtime, 0, len(h.active))
	for _, rt := range h.active {
		permission := hookPermission(event)
		if KnownHooks[event] && rt.Manifest.Runtime.Type == "external" && rt.Manifest.HasPermission(PermHTTPOutbound) && rt.Manifest.Subscribes(event) && (permission == "" || rt.Manifest.HasPermission(permission)) {
			targets = append(targets, rt)
		}
	}
	h.mu.RUnlock()
	if len(targets) == 0 {
		return
	}
	body, err := json.Marshal(map[string]any{
		"event":       event,
		"occurred_at": time.Now().UTC().Format(time.RFC3339),
		"payload":     payload,
	})
	if err != nil {
		h.log.Warn("plugin: encode hook payload", "event", event, "error", err)
		return
	}
	var wg sync.WaitGroup
	for _, rt := range targets {
		wg.Add(1)
		go func(rt *Runtime) {
			defer wg.Done()
			callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if err := h.post(callCtx, rt, "/hook", body); err != nil {
				h.log.Warn("plugin: hook failed", "uid", rt.UID, "event", event, "error", err)
				_ = h.store.SetPluginError(ctx, rt.UID, err.Error())
			}
		}(rt)
	}
	wg.Wait()
}

func (h *Host) post(ctx context.Context, rt *Runtime, path string, body []byte) error {
	if !rt.Manifest.HasPermission(PermHTTPOutbound) || !validEndpointPath(path) {
		return errors.New("plugin: outbound request not permitted")
	}
	if rt.Client == nil {
		return errors.New("plugin: runtime has no http client")
	}
	url := rt.Manifest.Runtime.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ts := fmt.Sprint(time.Now().Unix())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cardword-core")
	req.Header.Set("X-Cardword-Plugin", rt.UID)
	req.Header.Set("X-Cardword-Timestamp", ts)
	req.Header.Set("X-Cardword-Signature", cryptoutil.HMACSHA256Hex(rt.Secret, ts+"."+string(body)))
	resp, err := rt.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("plugin: %s returned %d", path, resp.StatusCode)
	}
	return nil
}

// SignCallback returns the signature a plugin must send to the core.
func (h *Host) SignCallback(uid string, body []byte) (string, error) {
	rt, ok := h.RuntimeLookup(uid)
	if !ok {
		return "", store.ErrNotFound
	}
	return cryptoutil.HMACSHA256Hex(rt.Secret, string(body)), nil
}

// PaymentCallbackPayload is the JSON a payment plugin posts back to the core.
type PaymentCallbackPayload struct {
	OrderNo     string `json:"order_no"`
	TxnID       string `json:"txn_id"`
	AmountCents int64  `json:"amount_cents"`
	Paid        bool   `json:"paid"`
	Raw         string `json:"raw,omitempty"`
}

// VerifyPaymentCallback authenticates and decodes a plugin callback.
func (h *Host) VerifyPaymentCallback(ctx context.Context, uid string, body []byte, signature string) (*payment.Callback, error) {
	rt, ok := h.RuntimeLookup(uid)
	if !ok {
		return nil, store.ErrNotFound
	}
	if !rt.Manifest.HasPermission(PermPaymentProvider) {
		return nil, errors.New("plugin: does not hold the payment.provider permission")
	}
	if !cryptoutil.VerifyHMACSHA256Hex(rt.Secret, string(body), signature) {
		return nil, errors.New("plugin: invalid callback signature")
	}
	var payload PaymentCallbackPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("plugin: decode callback: %w", err)
	}
	return &payment.Callback{
		Driver:      PaymentDriverName(uid),
		TxnID:       payload.TxnID,
		OrderNo:     payload.OrderNo,
		AmountCents: payload.AmountCents,
		Paid:        payload.Paid,
		Raw:         string(body),
	}, nil
}

// PaymentDriverName is the stable driver name for a plugin provider.
func PaymentDriverName(uid string) string { return "plugin:" + uid }

// PaymentDrivers returns adapters for enabled payment plugins.
func (h *Host) PaymentDrivers() []payment.Driver {
	var out []payment.Driver
	for _, rt := range h.Active() {
		if rt.Manifest.Kind == KindPayment && rt.Manifest.Runtime.Type == "external" && rt.Manifest.HasPermission(PermPaymentProvider) && rt.Manifest.HasPermission(PermHTTPOutbound) {
			out = append(out, &paymentAdapter{host: h, rt: rt})
		}
	}
	return out
}

type paymentAdapter struct {
	host *Host
	rt   *Runtime
}

// Name implements payment.Driver.
func (a *paymentAdapter) Name() string { return PaymentDriverName(a.rt.UID) }

// Title implements payment.Driver.
func (a *paymentAdapter) Title() string {
	if a.rt.Manifest.Payment != nil && a.rt.Manifest.Payment.Title != "" {
		return a.rt.Manifest.Payment.Title
	}
	return a.rt.Manifest.Name
}

// Create implements payment.Driver.
func (a *paymentAdapter) Create(ctx context.Context, req payment.Request) (*payment.Action, error) {
	if !a.rt.Manifest.HasPermission(PermPaymentProvider) {
		return nil, errors.New("plugin: payment provider not permitted")
	}
	createPath := "/payment/create"
	if a.rt.Manifest.Payment != nil && a.rt.Manifest.Payment.CreatePath != "" {
		createPath = a.rt.Manifest.Payment.CreatePath
	}
	body, err := json.Marshal(map[string]any{
		"order_no":     req.OrderNo,
		"subject":      req.Subject,
		"amount_cents": req.AmountCents,
		"currency":     req.Currency,
		"notify_url":   req.NotifyURL,
		"return_url":   req.ReturnURL,
		"email":        req.Email,
	})
	if err != nil {
		return nil, err
	}
	if err := a.host.post(ctx, a.rt, createPath, body); err != nil {
		return nil, err
	}
	// The plugin responded successfully; we cannot read the body twice, so the
	// adapter falls back to a redirect to the plugin checkout page.
	return &payment.Action{
		Kind: "redirect",
		URL:  a.rt.Manifest.Runtime.BaseURL + "/payment/checkout?order_no=" + req.OrderNo,
	}, nil
}

// VerifyCallback implements payment.Driver.
func (a *paymentAdapter) VerifyCallback(context.Context, map[string]string, []byte) (*payment.Callback, error) {
	return nil, errors.New("plugin: callbacks are handled by the plugin notify endpoint")
}

// RefundURL implements payment.Driver.
func (a *paymentAdapter) RefundURL(string) string { return a.rt.Manifest.Runtime.BaseURL }

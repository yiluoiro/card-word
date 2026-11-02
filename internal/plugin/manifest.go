// Package plugin implements the card word plugin system: signed ZIP packages,
// isolated runtimes and the hook bus used to extend the shop.
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ManifestSchema is the only accepted manifest dialect.
const ManifestSchema = "cardword.plugin/v1"

// Plugin kinds.
const (
	KindPayment   = "payment"
	KindNotifier  = "notifier"
	KindTheme     = "theme"
	KindExtension = "extension"
)

// Permission names a plugin may request. Anything outside this list is
// rejected at install time, which keeps the blast radius of a plugin small and
// auditable.
const (
	PermPaymentProvider = "payment.provider"
	PermNotifierSender  = "notifier.sender"
	PermThemeOverride   = "theme.override"
	PermOrderRead       = "order.read"
	PermProductRead     = "product.read"
	PermHTTPOutbound    = "http.outbound"
	PermAdminPage       = "admin.page"
)

var allowedPermissions = map[string]bool{
	PermPaymentProvider: true,
	PermNotifierSender:  true,
	PermThemeOverride:   true,
	PermOrderRead:       true,
	PermProductRead:     true,
	PermHTTPOutbound:    true,
	PermAdminPage:       true,
}

// KnownHooks lists the lifecycle events a plugin can subscribe to.
var KnownHooks = map[string]bool{
	"order.created":   true,
	"order.paid":      true,
	"order.delivered": true,
	"order.closed":    true,
	"product.updated": true,
	"plugin.enabled":  true,
}

var (
	uidRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}[a-z0-9]$`)
	versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)
)

// RuntimeSpec describes how to reach a plugin.
type RuntimeSpec struct {
	// Type is "external" (HTTP service), "static" (files only) or "none".
	Type       string `json:"type"`
	BaseURL    string `json:"base_url,omitempty"`
	HealthPath string `json:"health_path,omitempty"`
	TimeoutMS  int    `json:"timeout_ms,omitempty"`
}

// Setting is a configuration field exposed in the admin UI.
type Setting struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"` // text, secret, number, boolean, select
	Required    bool   `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
	Options     []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"options,omitempty"`
}

// PaymentSpec declares a payment provider contribution.
type PaymentSpec struct {
	Title string `json:"title"`
	// CreatePath is appended to the runtime base URL, default /payment/create.
	CreatePath string `json:"create_path,omitempty"`
}

// StaticSpec declares a theme or admin page contribution.
type StaticSpec struct {
	Index string `json:"index"`
}

// Manifest is a plugin package descriptor.
type Manifest struct {
	Schema      string       `json:"schema"`
	UID         string       `json:"uid"`
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Author      string       `json:"author"`
	Description string       `json:"description"`
	Homepage    string       `json:"homepage,omitempty"`
	Kind        string       `json:"kind"`
	License     string       `json:"license,omitempty"`
	Runtime     RuntimeSpec  `json:"runtime"`
	Permissions []string     `json:"permissions"`
	Hooks       []string     `json:"hooks"`
	Settings    []Setting    `json:"settings,omitempty"`
	Payment     *PaymentSpec `json:"payment,omitempty"`
	Static      *StaticSpec  `json:"static,omitempty"`
}

// Validate normalises and checks the manifest.
func (m *Manifest) Validate() error {
	m.Schema = strings.TrimSpace(m.Schema)
	if m.Schema != ManifestSchema {
		return fmt.Errorf("plugin: unsupported manifest schema %q, expected %q", m.Schema, ManifestSchema)
	}
	m.UID = strings.ToLower(strings.TrimSpace(m.UID))
	if !uidRe.MatchString(m.UID) {
		return errors.New("plugin: uid must be 3-64 chars of a-z 0-9 . _ - and start/end alphanumeric")
	}
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" || len(m.Name) > 80 {
		return errors.New("plugin: name is required (max 80 chars)")
	}
	m.Version = strings.TrimSpace(m.Version)
	if !versionRe.MatchString(m.Version) {
		return errors.New("plugin: version must look like 1.2.3")
	}
	switch m.Kind {
	case KindPayment, KindNotifier, KindTheme, KindExtension:
	default:
		return fmt.Errorf("plugin: unknown kind %q", m.Kind)
	}
	switch m.Runtime.Type {
	case "external":
		if !strings.HasPrefix(m.Runtime.BaseURL, "https://") && !strings.HasPrefix(m.Runtime.BaseURL, "http://") {
			return errors.New("plugin: external runtime needs an http(s) base_url")
		}
		m.Runtime.BaseURL = strings.TrimRight(m.Runtime.BaseURL, "/")
		if m.Runtime.TimeoutMS <= 0 || m.Runtime.TimeoutMS > 30000 {
			m.Runtime.TimeoutMS = 5000
		}
	case "static":
		if m.Static == nil || strings.TrimSpace(m.Static.Index) == "" {
			return errors.New("plugin: static runtime needs static.index")
		}
	case "none", "":
		m.Runtime.Type = "none"
	default:
		return fmt.Errorf("plugin: unknown runtime type %q", m.Runtime.Type)
	}
	if m.Kind == KindPayment {
		if m.Payment == nil {
			m.Payment = &PaymentSpec{Title: m.Name}
		}
		if m.Payment.Title == "" {
			m.Payment.Title = m.Name
		}
		if m.Runtime.Type != "external" {
			return errors.New("plugin: payment plugins must use the external runtime")
		}
	}
	dedupPerms := make([]string, 0, len(m.Permissions))
	seen := map[string]bool{}
	for _, p := range m.Permissions {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		if !allowedPermissions[p] {
			return fmt.Errorf("plugin: permission %q is not in the allow list", p)
		}
		seen[p] = true
		dedupPerms = append(dedupPerms, p)
	}
	m.Permissions = dedupPerms
	hooks := make([]string, 0, len(m.Hooks))
	seenHooks := map[string]bool{}
	for _, h := range m.Hooks {
		h = strings.TrimSpace(h)
		if h == "" || seenHooks[h] {
			continue
		}
		if !KnownHooks[h] {
			return fmt.Errorf("plugin: unknown hook %q", h)
		}
		seenHooks[h] = true
		hooks = append(hooks, h)
	}
	m.Hooks = hooks
	for i := range m.Settings {
		s := &m.Settings[i]
		s.Key = strings.TrimSpace(s.Key)
		if !uidRe.MatchString(s.Key) {
			return fmt.Errorf("plugin: setting key %q is invalid", s.Key)
		}
		switch s.Type {
		case "text", "secret", "number", "boolean", "select":
		default:
			return fmt.Errorf("plugin: setting %s has unknown type %q", s.Key, s.Type)
		}
	}
	if len(m.Description) > 2000 {
		m.Description = m.Description[:2000]
	}
	return nil
}

// HasPermission reports whether the plugin declared a permission.
func (m *Manifest) HasPermission(p string) bool {
	for _, v := range m.Permissions {
		if v == p {
			return true
		}
	}
	return false
}

// Subscribes reports whether the plugin listens to a hook.
func (m *Manifest) Subscribes(hook string) bool {
	for _, v := range m.Hooks {
		if v == hook {
			return true
		}
	}
	return false
}

// ParseManifest decodes and validates a manifest document.
func ParseManifest(data []byte) (*Manifest, error) {
	if len(data) > 256*1024 {
		return nil, errors.New("plugin: manifest too large")
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("plugin: parse manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

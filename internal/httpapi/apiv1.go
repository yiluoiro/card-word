package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

// requireAPIToken authenticates machine callers via bearer tokens and returns
// the token record so handlers can apply finer grained checks.
func (s *Server) requireAPIToken(w http.ResponseWriter, r *http.Request, scope string) (*store.APIToken, bool) {
	header := r.Header.Get("Authorization")
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if token == "" || token == header {
		token = strings.TrimSpace(r.Header.Get("X-API-Key"))
	}
	if token == "" {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing API token")
		return nil, false
	}
	record, err := s.db.APITokenByPlain(r.Context(), token)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "invalid API token")
		return nil, false
	}
	if scope != "" && !hasScope(record.Scopes, scope) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "token lacks scope "+scope)
		return nil, false
	}
	// Optional source address allow-list (single IP or CIDR).
	if len(record.AllowedIPs) > 0 && !ipAllowed(s.clientIP(r), record.AllowedIPs) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "token is not allowed from this address")
		return nil, false
	}
	// Per-token budget so one integration cannot starve the others.
	if !s.apiLimiter.allow("token:" + strconv.FormatInt(record.ID, 10)) {
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "token rate limit exceeded")
		return nil, false
	}
	_ = s.db.TouchAPIToken(r.Context(), record.ID)
	return record, true
}

// ipAllowed matches an address against exact IPs or CIDR ranges.
func ipAllowed(addr string, allow []string) bool {
	parsed := net.ParseIP(strings.TrimSpace(addr))
	if parsed == nil {
		return false
	}
	for _, entry := range allow {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, network, err := net.ParseCIDR(entry); err == nil && network.Contains(parsed) {
				return true
			}
			continue
		}
		if ip := net.ParseIP(entry); ip != nil && ip.Equal(parsed) {
			return true
		}
	}
	return false
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want || s == "*" {
			return true
		}
	}
	return false
}

func (s *Server) handleAPIPing(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAPIToken(w, r, ""); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "card word"})
}

func (s *Server) handleAPIProducts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAPIToken(w, r, "products:read"); !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	limit := int(parseInt64(r.URL.Query().Get("limit")))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	offset := int(parseInt64(r.URL.Query().Get("offset")))
	filter := store.ProductFilter{Status: store.ProductActive, Search: q, Limit: limit, Offset: offset}
	if category != "" {
		c, err := s.db.CategoryBySlug(r.Context(), category)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "not_found", "category not found")
			return
		}
		filter.CategoryID = c.ID
	}
	products, err := s.db.ListProducts(r.Context(), filter)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "cannot list products")
		return
	}
	out := make([]map[string]any, 0, len(products))
	for _, p := range products {
		out = append(out, productJSON(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"data": out,
		"meta": map[string]any{"limit": limit, "offset": offset, "count": len(out)},
	})
}

// handleAPICategories lists visible categories for catalogue sync.
func (s *Server) handleAPICategories(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAPIToken(w, r, "products:read"); !ok {
		return
	}
	categories, err := s.db.ListCategories(r.Context(), true)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "cannot list categories")
		return
	}
	out := make([]map[string]any, 0, len(categories))
	for _, c := range categories {
		out = append(out, map[string]any{
			"id":   c.ID,
			"name": c.Name,
			"slug": c.Slug,
			"sort": c.Sort,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": out})
}

func (s *Server) handleAPIProduct(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAPIToken(w, r, "products:read"); !ok {
		return
	}
	product, err := s.db.ProductBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "product not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": productJSON(*product)})
}

func productJSON(p store.Product) map[string]any {
	return map[string]any{
		"id":          p.ID,
		"name":        p.Name,
		"slug":        p.Slug,
		"summary":     p.Summary,
		"price_cents": p.PriceCents,
		"currency":    p.Currency,
		"stock":       p.Stock,
		"min_qty":     p.MinQty,
		"max_qty":     p.MaxQty,
		"status":      p.Status,
	}
}

func (s *Server) handleAPIOrder(w http.ResponseWriter, r *http.Request) {
	// Reading an order must not imply reading the purchased secrets: card keys
	// are only returned when the token also holds cards:read.
	record, ok := s.requireAPIToken(w, r, "orders:read")
	if !ok {
		return
	}
	order, err := s.db.OrderByNo(r.Context(), r.PathValue("no"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "order not found")
		return
	}
	payload := orderJSON(*order)
	if order.Status == store.OrderDelivered && hasScope(record.Scopes, "cards:read") {
		if cards, err := s.db.OrderCards(r.Context(), order.ID); err == nil {
			secrets := make([]string, 0, len(cards))
			for _, c := range cards {
				secrets = append(secrets, c.Secret)
			}
			payload["cards"] = secrets
		}
	} else if order.Status == store.OrderDelivered {
		payload["cards_redacted"] = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": payload})
}

func orderJSON(o store.Order) map[string]any {
	return map[string]any{
		"order_no":       o.OrderNo,
		"product_id":     o.ProductID,
		"product_name":   o.ProductName,
		"quantity":       o.Quantity,
		"total_cents":    o.TotalCents,
		"currency":       o.Currency,
		"status":         o.Status,
		"email":          o.Email,
		"created_at":     o.CreatedAt,
		"expires_at":     o.ExpiresAt,
		"paid_at":        o.PaidAt,
		"delivered_at":   o.DeliveredAt,
		"payment_driver": o.PaymentDriver,
	}
}

type apiCreateOrderRequest struct {
	ProductID   int64  `json:"product_id"`
	ProductSlug string `json:"product_slug"`
	Quantity    int    `json:"quantity"`
	Email       string `json:"email"`
	Coupon      string `json:"coupon"`
	Driver      string `json:"driver"`
}

type apiCreateOrderResponse struct {
	OrderNo    string `json:"order_no"`
	QueryToken string `json:"query_token"`
	TotalCents int64  `json:"total_cents"`
	Currency   string `json:"currency"`
	PayURL     string `json:"pay_url"`
	ExpiresAt  string `json:"expires_at"`
}

func (s *Server) handleAPICreateOrder(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAPIToken(w, r, "orders:write"); !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "cannot read body")
		return
	}
	var req apiCreateOrderRequest
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.ProductID == 0 && req.ProductSlug != "" {
		product, err := s.db.ProductBySlug(r.Context(), req.ProductSlug)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "not_found", "product not found")
			return
		}
		req.ProductID = product.ID
	}
	if req.Driver == "" {
		req.Driver = s.setting(store.SettingPaymentDriver, "manual")
	}
	if _, ok := s.findDriver(req.Driver); !ok {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "unknown payment driver")
		return
	}
	result, err := s.db.CreateOrder(r.Context(), store.CreateOrderInput{
		ProductID:      req.ProductID,
		Quantity:       req.Quantity,
		Email:          strings.TrimSpace(req.Email),
		CouponCode:     strings.TrimSpace(req.Coupon),
		PaymentDriver:  req.Driver,
		ClientIP:       s.clientIP(r),
		IdempotencyKey: apiIdempotencyKey(r, body),
		TTL:            s.cfg.OrderTTL,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOutOfStock):
			writeAPIError(w, http.StatusConflict, "out_of_stock", "not enough stock")
		case errors.Is(err, store.ErrNotFound):
			writeAPIError(w, http.StatusNotFound, "not_found", "product or coupon not found")
		default:
			writeAPIError(w, http.StatusBadRequest, "bad_request", err.Error())
		}
		return
	}
	order := result.Order
	s.hook(r.Context(), "order.created", map[string]any{
		"order_no":    order.OrderNo,
		"product_id":  order.ProductID,
		"quantity":    order.Quantity,
		"total_cents": order.TotalCents,
		"email":       order.Email,
	})
	s.audit(r, "order.create.api", order.OrderNo, "via api token")
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true,
		"data": apiCreateOrderResponse{
			OrderNo:    order.OrderNo,
			QueryToken: result.QueryToken,
			TotalCents: order.TotalCents,
			Currency:   order.Currency,
			PayURL:     s.cfg.BaseURL + "/order/" + order.OrderNo + "/pay?token=" + result.QueryToken,
			ExpiresAt:  order.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		},
	})
}

func (s *Server) handleAPICloseOrder(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAPIToken(w, r, "orders:write"); !ok {
		return
	}
	orderNo := r.PathValue("no")
	if err := s.db.CloseOrder(r.Context(), orderNo); err != nil {
		writeAPIError(w, http.StatusConflict, "conflict", "order cannot be closed")
		return
	}
	s.audit(r, "order.close.api", orderNo, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type apiAddCardsRequest struct {
	ProductID int64    `json:"product_id"`
	Secrets   []string `json:"secrets"`
	Label     string   `json:"label"`
}

func (s *Server) handleAPIAddCards(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAPIToken(w, r, "cards:write"); !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "cannot read body")
		return
	}
	var req apiAddCardsRequest
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if len(req.Secrets) == 0 || len(req.Secrets) > 5000 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "secrets must contain 1-5000 entries")
		return
	}
	imported, err := s.db.AddCards(r.Context(), req.ProductID, req.Secrets, req.Label)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	s.audit(r, "card.import.api", strconv.FormatInt(req.ProductID, 10), "imported="+strconv.Itoa(imported))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"imported": imported}})
}

// handlePluginNotify receives payment callbacks from payment plugins.
func (s *Server) handlePluginNotify(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		http.Error(w, "plugin subsystem disabled", http.StatusServiceUnavailable)
		return
	}
	uid := r.PathValue("uid")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	signature := r.Header.Get("X-Cardword-Signature")
	if signature == "" {
		signature = r.URL.Query().Get("signature")
	}
	cb, err := s.plugins.VerifyPaymentCallback(r.Context(), uid, body, signature)
	if err != nil {
		s.log.Warn("plugin notify rejected", "uid", uid, "error", err)
		http.Error(w, "invalid callback", http.StatusUnauthorized)
		return
	}
	if !cb.Paid {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": true})
		return
	}
	s.completePayment(r.Context(), cb, s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiIdempotencyKey prefers an explicit Idempotency-Key header so a robot can
// retry safely without resending an identical body.
func apiIdempotencyKey(r *http.Request, body []byte) string {
	if v := strings.TrimSpace(r.Header.Get("Idempotency-Key")); v != "" {
		return "api:" + cryptoutil.TokenHash("idem:" + v)[:24]
	}
	return "api:" + cryptoutil.TokenHash(string(body))[:24]
}

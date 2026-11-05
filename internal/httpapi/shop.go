package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	qrcode "github.com/skip2/go-qrcode"

	"html/template"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/notify"
	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/store"
)

func notifyMessage(to, subject, body string) notify.Message {
	return notify.Message{To: to, Subject: subject, Body: body}
}

func (s *Server) maintenanceBlocked(w http.ResponseWriter, r *http.Request) bool {
	if s.setting(store.SettingMaintenance, "0") == "1" {
		s.renderError(w, r, http.StatusServiceUnavailable, "站点正在维护中，请稍后再访问。")
		return true
	}
	return false
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if s.maintenanceBlocked(w, r) {
		return
	}
	ctx := r.Context()
	categories, err := s.db.ListCategories(ctx, true)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载分类失败。")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	catSlug := strings.TrimSpace(r.URL.Query().Get("cat"))
	filter := store.ProductFilter{Status: store.ProductActive, Search: query, Limit: 24}
	var activeCat *store.Category
	if catSlug != "" {
		if c, err := s.db.CategoryBySlug(ctx, catSlug); err == nil {
			activeCat = c
			filter.CategoryID = c.ID
		}
	}
	total, err := s.db.CountProducts(ctx, filter)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载商品失败。")
		return
	}
	filters := url.Values{}
	if query != "" {
		filters.Set("q", query)
	}
	if activeCat != nil {
		filters.Set("cat", activeCat.Slug)
	}
	pagination := paginate(r.URL.Query().Get("page"), total, filter.Limit, "/", "products", filters)
	filter.Offset = pagination.Offset
	products, err := s.db.ListProducts(ctx, filter)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载商品失败。")
		return
	}
	s.render(w, r, "home", map[string]any{
		"Title":      s.setting(store.SettingSiteName, "Card Word"),
		"Categories": categories,
		"Products":   products,
		"BrandCards": storefrontBrands,
		"Search":     query,
		"ActiveCat":  activeCat,
		"Pagination": pagination,
	})
}

func (s *Server) handleProduct(w http.ResponseWriter, r *http.Request) {
	if s.maintenanceBlocked(w, r) {
		return
	}
	slug := r.PathValue("slug")
	product, err := s.db.ProductBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, r, http.StatusNotFound, "商品不存在或已下架。")
		return
	}
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载商品失败。")
		return
	}
	if product.Status != store.ProductActive {
		s.renderError(w, r, http.StatusNotFound, "商品不存在或已下架。")
		return
	}
	s.renderProduct(w, r, product, http.StatusOK, "", nil)
}

// Keep failed checkout values in the response body, never in the URL or storage.
func (s *Server) renderProduct(w http.ResponseWriter, r *http.Request, product *store.Product, status int, message string, form url.Values) {
	if product.Status != store.ProductActive {
		s.renderError(w, r, http.StatusNotFound, "商品不存在或已下架。")
		return
	}
	var related []store.Product
	if product.CategoryID.Valid {
		if list, err := s.db.ListProducts(r.Context(), store.ProductFilter{
			Status:     store.ProductActive,
			CategoryID: product.CategoryID.Int64,
			Limit:      6,
		}); err == nil {
			for _, p := range list {
				if p.ID == product.ID {
					continue
				}
				related = append(related, p)
				if len(related) == 3 {
					break
				}
			}
		}
	}
	idem, err := cryptoutil.RandomToken(24)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载购买表单失败，请重试。")
		return
	}
	quantity := product.MinQty
	if form != nil {
		if previous := strings.TrimSpace(form.Get("idem")); previous != "" {
			idem = previous
		}
		if n, err := strconv.Atoi(form.Get("quantity")); err == nil && n > 0 {
			quantity = n
		}
	}
	variants, variantErr := s.db.ProductVariants(r.Context(), product.ID, true)
	if variantErr != nil {
		s.renderError(w, r, 500, "加载规格失败。")
		return
	}
	selectedID := parseInt64(form.Get("variant_id"))
	if form == nil {
		selectedID = parseInt64(r.URL.Query().Get("variant_id"))
	}
	if len(variants) > 0 {
		selected := variants[0]
		if selectedID == 0 {
			for _, v := range variants {
				if v.Stock >= product.MinQty {
					selected = v
					break
				}
			}
		} else {
			for _, v := range variants {
				if v.ID == selectedID {
					selected = v
					break
				}
			}
		}
		selectedID = selected.ID
		copyProduct := *product
		copyProduct.PriceCents = selected.PriceCents
		copyProduct.Stock = selected.Stock
		product = &copyProduct
	}
	maxQty := min(product.MaxQty, product.Stock)
	blockedReason := ""
	drivers := s.availableDriversForCurrency(product.Currency)
	if len(drivers) == 0 {
		blockedReason = "暂无可用支付方式，请联系商家"
	}

	scope := ""
	availableStock := maxQty
	for _, v := range variants {
		if v.Stock > availableStock {
			availableStock = v.Stock
		}
	}
	purchaseReady := product.Status == store.ProductActive && blockedReason == ""
	if product.Status == store.ProductActive && availableStock >= product.MinQty && blockedReason == "" {
		scope = fmt.Sprintf("checkout:%d", product.ID)
	}
	s.renderChallengeForm(w, r, status, "product", scope, map[string]any{
		"IdempotencyKey": idem,
		"PurchaseReady":  purchaseReady,
		"Variants":       variants, "SelectedVariant": selectedID,
		"MaxQuantity":           maxQty,
		"CanBuy":                product.Status == store.ProductActive && maxQty >= product.MinQty && blockedReason == "",
		"PurchaseBlockedReason": blockedReason,
		"Error":                 message,
		"PurchaseQuantity":      quantity,
		"PurchaseEmail":         form.Get("email"),
		"PurchaseContact":       form.Get("contact"),
		"PurchaseCoupon":        form.Get("coupon"),
		"PurchaseDriver":        form.Get("driver"),
		"PurchaseAgreed":        form.Get("agree") != "",
		"CurrencySymbol":        currencySymbol(product.Currency),
		"EmailNotifications":    s.setting("smtp.host", "") != "" && s.setting("smtp.from", "") != "",
		"OrderTTLMinutes":       int(s.cfg.OrderTTL.Minutes()),
		"Title":                 product.Name,
		"Product":               product,
		"Drivers":               drivers,
		"Currency":              product.Currency,
		"Related":               related,
	})
}

// availableDrivers lists the drivers the operator enabled, plus plugin drivers.
func (s *Server) availableDrivers() []map[string]string {
	selected := s.setting(store.SettingPaymentDriver, "manual")

	out := make([]map[string]string, 0, 4)
	for _, d := range s.allDrivers() {
		name := d.Name()
		configSource := s

		if (name == "alipay" || name == "wechatpay" || name == "epay") && configSource.setting("payment."+name+".enabled", "") == "0" {
			continue
		}
		if selected != "" && selected != "all" {
			allowed := strings.Split(selected, ",")
			found := false
			for _, a := range allowed {
				if strings.TrimSpace(a) == name {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		title, icon := d.Title(), "wallet"
		if gateway, ok := d.(interface{ PayType() string }); ok && name == "epay" {
			methods := storedEpayMethods(configSource.setting("payment.epay.methods", ""), gateway.PayType())
			for _, code := range methods {
				for _, o := range epayOptions {
					if code == o.Code {
						out = append(out, map[string]string{"Name": "epay:" + code, "Provider": "epay", "Method": code, "Title": o.Label, "Icon": o.Icon})
						break
					}
				}
			}
			continue
		}
		channel := name
		switch channel {
		case "manual":
			title, icon = "人工付款", "receipt"
		case "alipay":
			title, icon = "支付宝", "alipay"
		case "wxpay", "wechatpay":
			title, icon = "微信支付", "wechat"
		case "qqpay":
			title, icon = "QQ 钱包", "qq"
		}
		out = append(out, map[string]string{"Name": name, "Provider": name, "Title": title, "Icon": icon})
	}
	for _, option := range out {
		if option["Provider"] == "epay" {
			for _, other := range out {
				if other["Provider"] != "epay" && other["Icon"] == option["Icon"] {
					option["Via"] = "易支付"
					break
				}
			}
		}
	}
	return out
}

// Resolve only advertised options. Old API clients can still request the default EPay method.
func (s *Server) checkoutPayment(currency, choice string) (string, string, bool) {
	for _, o := range s.availableDriversForCurrency(currency) {
		if choice == "" || choice == o["Name"] || (choice == "epay" && o["Provider"] == "epay") {
			return o["Provider"], o["Method"], true
		}
	}
	return "", "", false
}

func (s *Server) allDrivers() []payment.Driver {

	drivers := s.payments.List()
	if s.plugins != nil {
		for _, d := range s.plugins.PaymentDrivers() {
			found := false
			for _, existing := range drivers {
				if existing.Name() == d.Name() {
					found = true
					break
				}
			}
			if !found {
				drivers = append(drivers, d)
			}
		}
	}
	return drivers
}

func (s *Server) availableDriversForCurrency(currency string) []map[string]string {
	var out []map[string]string
	for _, option := range s.availableDrivers() {
		if driver, ok := s.findDriver(option["Provider"]); ok && payment.SupportsCurrency(driver, currency) {
			out = append(out, option)
		}
	}
	return out
}

func (s *Server) findDriver(name string) (payment.Driver, bool) {
	for _, d := range s.allDrivers() {
		if d.Name() == name {
			return d, true
		}
	}
	return nil, false
}

func (s *Server) handleCheckout(w http.ResponseWriter, r *http.Request) {
	if s.maintenanceBlocked(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	ctx := r.Context()
	productID := parseInt64(r.FormValue("product_id"))
	quantity := int(parseInt64(r.FormValue("quantity")))
	email := strings.TrimSpace(r.FormValue("email"))
	contact := strings.TrimSpace(r.FormValue("contact"))
	coupon := strings.TrimSpace(r.FormValue("coupon"))
	driverName := strings.TrimSpace(r.FormValue("driver"))
	agreed := r.FormValue("agree") != ""

	if productID == 0 {
		s.renderError(w, r, http.StatusBadRequest, "缺少商品信息。")
		return
	}
	product, err := s.db.ProductByID(ctx, productID)
	if err != nil || product.Status != store.ProductActive {
		s.renderError(w, r, http.StatusNotFound, "商品不存在。")
		return
	}
	fail := func(message string) {
		// Stock may have changed while the order transaction was running.
		if current, err := s.db.ProductByID(ctx, productID); err == nil {
			product = current
		}
		s.renderProduct(w, r, product, http.StatusUnprocessableEntity, message, r.PostForm)
	}
	if !s.formVerified(r, fmt.Sprintf("checkout:%d", product.ID)) {
		fail(captchaRetry)
		return
	}
	if quantity <= 0 {
		quantity = product.MinQty
	}
	if email != "" {
		if _, err := mail.ParseAddress(email); err != nil {
			fail("邮箱格式不正确，请检查后重试。其他信息已保留。")
			return
		}
	}
	if !agreed {
		fail("请先阅读并同意购买条款。其他信息已保留。")
		return
	}
	driverName, method, allowedDriver := s.checkoutPayment(product.Currency, driverName)
	if !allowedDriver {
		fail("所选支付方式暂不可用，请重新选择。其他信息已保留。")
		return
	}
	idem := strings.TrimSpace(r.FormValue("idem"))
	if idem == "" {
		idem, _ = cryptoutil.RandomToken(16)
	}
	result, err := s.db.CreateOrder(ctx, store.CreateOrderInput{
		CustomerID:     s.checkoutCustomerID(r),
		ProductID:      productID,
		VariantID:      parseInt64(r.FormValue("variant_id")),
		Quantity:       quantity,
		Email:          email,
		Contact:        contact,
		CouponCode:     coupon,
		PaymentDriver:  driverName,
		PaymentMethod:  method,
		ClientIP:       s.clientIP(r),
		IdempotencyKey: s.checkoutIdempotencyKey(r, idem),
		TTL:            s.cfg.OrderTTL,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOutOfStock):
			fail("库存不足，请减少购买数量。其他信息已保留。")
		case errors.Is(err, store.ErrNotFound):
			fail("优惠码不存在，请检查或清空优惠码后重试。其他信息已保留。")
		case errors.Is(err, store.ErrInvalidStatus):
			fail("商品当前不可购买，请返回列表选择其他商品。")
		default:
			s.log.Warn("create order failed", "error", err)
			fail("下单未完成，请检查购买数量和优惠码后重试；若仍失败，请联系商家。已填信息保留。")
		}
		return
	}
	order := result.Order
	if result.Replayed {
		if s.orderCredentialValid(r, order.OrderNo) || (order.CustomerID > 0 && order.CustomerID == s.checkoutCustomerID(r)) {
			redirect(w, r, "/order/"+order.OrderNo)
			return
		}
		redirect(w, r, "/query?ok="+urlEscape("订单已创建，请使用订单号与查询凭证访问。"))
		return
	}
	s.hook(ctx, "order.created", map[string]any{
		"order_no":     order.OrderNo,
		"product_id":   order.ProductID,
		"product_name": order.ProductName,
		"quantity":     order.Quantity,
		"total_cents":  order.TotalCents,
		"currency":     order.Currency,
		"email":        order.Email,
	})
	s.audit(r, "order.create", order.OrderNo, fmt.Sprintf("product=%d qty=%d total=%d", order.ProductID, order.Quantity, order.TotalCents))
	s.issueReadGrant(w, r, order.OrderNo)
	redirect(w, r, "/order/"+order.OrderNo+"?token="+urlEscape(result.QueryToken))
}

// A short browser- and order-bound grant lets status polling reuse the
// successful purchase or lookup without repeatedly asking the buyer.
func (s *Server) readGrantName(no string) string {
	return s.cookieName("cw_read_" + cryptoutil.TokenHash(no)[:16])
}
func (s *Server) issueReadGrant(w http.ResponseWriter, r *http.Request, no string) {
	exp := strconv.FormatInt(time.Now().Add(orderCredentialTTL).Unix(), 10)
	sig := cryptoutil.HMACSHA256Hex(s.cfg.MasterKey, "order-read:"+no+"|"+exp+"|"+s.captchaBinding(r))
	http.SetCookie(w, &http.Cookie{Name: s.readGrantName(no), Value: exp + "." + sig, Path: "/", MaxAge: int(orderCredentialTTL.Seconds()), HttpOnly: true, Secure: s.cfg.Secure(), SameSite: http.SameSiteLaxMode})
}
func (s *Server) readGrantValid(r *http.Request, no string) bool {
	c, err := r.Cookie(s.readGrantName(no))
	if err != nil {
		return false
	}
	exp, sig, ok := strings.Cut(c.Value, ".")
	n, err := strconv.ParseInt(exp, 10, 64)
	return ok && err == nil && time.Now().Unix() < n && cryptoutil.VerifyHMACSHA256Hex(s.cfg.MasterKey, "order-read:"+no+"|"+exp+"|"+s.captchaBinding(r), sig)
}
func (s *Server) orderChallenge(w http.ResponseWriter, r *http.Request, no, kind, token string) {
	if !publicScope.MatchString(kind + ":" + no) {
		http.NotFound(w, r)
		return
	}
	title, action := "验证后查看订单", "/order/"+no+"/verify"
	if kind == "pay" {
		title, action = "验证后继续支付", "/order/"+no+"/pay"
	}
	status, msg := 200, ""
	if r.Method == http.MethodPost {
		status, msg = 422, captchaRetry
	}
	s.renderChallengeForm(w, r, status, "order_challenge", kind+":"+no, map[string]any{"Title": title, "FormAction": action, "Token": token, "Error": msg})
}
func (s *Server) handleOrderView(w http.ResponseWriter, r *http.Request) {
	orderNo := r.PathValue("no")
	token := r.FormValue("token")
	verified := s.readGrantValid(r, orderNo)
	if !verified && token != "" && (r.Method != "POST" || !s.formVerified(r, "read:"+orderNo)) {
		s.orderChallenge(w, r, orderNo, "read", token)
		return
	}
	order, cards, ok := s.loadBuyerOrder(w, r, orderNo, token, verified)
	if !ok {
		return
	}
	if !verified {
		if token == "" && (r.Method != "POST" || !s.formVerified(r, "read:"+orderNo)) {
			s.orderChallenge(w, r, orderNo, "read", token)
			return
		}
		s.issueReadGrant(w, r, orderNo)
		redirect(w, r, "/order/"+orderNo)
		return
	}
	s.render(w, r, "order", map[string]any{
		"Title":   "订单 " + order.OrderNo,
		"Order":   order,
		"Cards":   cards,
		"Token":   token,
		"Drivers": s.availableDrivers(),
	})
}

func (s *Server) handleOrderStatus(w http.ResponseWriter, r *http.Request) {
	orderNo := r.PathValue("no")
	token := r.URL.Query().Get("token")
	if !s.readGrantValid(r, orderNo) {
		writeAPIError(w, 403, "captcha_required", "请重新验证后查询订单")
		return
	}
	order, cards, ok := s.loadOrderForBuyer(w, r, orderNo, token)
	if !ok {
		return
	}
	secrets := make([]string, 0, len(cards))
	for _, c := range cards {
		secrets = append(secrets, c.Secret)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"order_no":     order.OrderNo,
		"status":       order.Status,
		"status_text":  statusText(order.Status),
		"total_cents":  order.TotalCents,
		"quantity":     order.Quantity,
		"cards":        secrets,
		"expires_at":   order.ExpiresAt,
		"delivered_at": order.DeliveredAt,
	})
}

func (s *Server) loadOrderForBuyer(w http.ResponseWriter, r *http.Request, orderNo, token string) (*store.Order, []store.CardKey, bool) {
	return s.loadBuyerOrder(w, r, orderNo, token, true)
}
func (s *Server) loadBuyerOrder(w http.ResponseWriter, r *http.Request, orderNo, token string, includeCards bool) (*store.Order, []store.CardKey, bool) {
	ctx := r.Context()
	authorized := false
	if customer := s.currentCustomer(r); customer != nil {
		if order, err := s.db.OrderByNo(ctx, orderNo); err == nil && order.CustomerID == customer.ID {
			authorized = true
		}
	}
	if token != "" {
		if valid, err := s.db.VerifyOrderQueryToken(ctx, orderNo, token); err == nil && valid {
			authorized = true
			// Exchange the long lived link credential for a short lived cookie
			// so the following navigation no longer carries it in the URL.
			s.issueOrderCredential(w, orderNo)
		}
	}
	if !authorized && s.orderCredentialValid(r, orderNo) {
		authorized = true

		// Reissue on the canonical path without extending the signed expiry.

	}
	if !authorized {
		s.renderError(w, r, http.StatusForbidden, "需要订单查询凭证，请通过订单查询页访问。")
		return nil, nil, false
	}
	order, err := s.db.OrderByNo(ctx, orderNo)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "订单不存在。")
		return nil, nil, false
	}
	var cards []store.CardKey
	if includeCards && order.Status == store.OrderDelivered {
		cards, err = s.db.OrderCards(ctx, order.ID)
		if err != nil {
			s.log.Error("load order cards failed", "order", orderNo, "error", err)
		}
	}
	return order, cards, true
}

func (s *Server) handleQueryForm(w http.ResponseWriter, r *http.Request) {
	s.renderChallengeForm(w, r, 200, "query", "query", map[string]any{"Title": "订单查询"})
}

func (s *Server) handleQuerySubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	orderNo := strings.TrimSpace(r.FormValue("order_no"))
	token := strings.TrimSpace(r.FormValue("token"))
	fail := func(status int, msg string) {
		s.renderChallengeForm(w, r, status, "query", "query", map[string]any{"Title": "订单查询", "Error": msg, "OrderNo": orderNo})
	}
	if !s.formVerified(r, "query") {
		fail(422, captchaRetry)
		return
	}
	if orderNo == "" || token == "" {
		fail(http.StatusBadRequest, "请输入订单号与查询凭证。")
		return
	}
	valid, err := s.db.VerifyOrderQueryToken(r.Context(), orderNo, token)
	if err != nil || !valid {
		fail(http.StatusForbidden, "订单号或查询凭证不正确，或凭证已过期。")
		return
	}
	s.issueReadGrant(w, r, orderNo)
	s.issueOrderCredential(w, orderNo)
	redirect(w, r, "/order/"+orderNo)
}

// orderCredentialTTL bounds the short lived cookie issued after a buyer proves
// ownership of an order. The query token itself stays out of subsequent URLs.
const orderCredentialTTL = 30 * time.Minute

const cookieOrderCredential = "cw_ord"

func (s *Server) orderCredentialKey() []byte {
	return []byte(cryptoutil.HMACSHA256Hex(s.cfg.MasterKey, "order-credential-key"))
}

func (s *Server) issueOrderCredential(w http.ResponseWriter, orderNo string) {
	expires := time.Now().Add(orderCredentialTTL).Unix()
	payload := fmt.Sprintf("%s|%d", orderNo, expires)
	sig := cryptoutil.HMACSHA256Hex(s.orderCredentialKey(), payload)
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(cookieOrderCredential + "_" + cryptoutil.TokenHash(orderNo)[:16]),
		Value:    url.QueryEscape(payload + "|" + sig),
		Path:     "/",
		MaxAge:   int(orderCredentialTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.Secure(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) orderCredentialValid(r *http.Request, orderNo string) bool {
	c, err := r.Cookie(s.cookieName(cookieOrderCredential + "_" + cryptoutil.TokenHash(orderNo)[:16]))
	if err != nil {
		c, err = r.Cookie(s.cookieName(cookieOrderCredential))
	} // Existing signed links remain usable after the cookie-name migration.
	if err != nil || c.Value == "" {
		return false
	}
	raw, err := url.QueryUnescape(c.Value)
	if err != nil {
		return false
	}
	parts := strings.Split(raw, "|")
	if len(parts) != 3 {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return false
	}
	if parts[0] != orderNo {
		return false
	}
	return cryptoutil.VerifyHMACSHA256Hex(s.orderCredentialKey(), parts[0]+"|"+parts[1], parts[2])
}

func (s *Server) handlePay(w http.ResponseWriter, r *http.Request) {
	if s.maintenanceBlocked(w, r) {
		return
	}
	orderNo := r.PathValue("no")
	token := r.FormValue("token")
	if r.Method != "POST" || !s.formVerified(r, "pay:"+orderNo) {
		s.orderChallenge(w, r, orderNo, "pay", token)
		return
	}
	order, _, ok := s.loadBuyerOrder(w, r, orderNo, token, false)
	if !ok {
		return
	}
	s.issueReadGrant(w, r, orderNo)
	if order.Status != store.OrderPending {
		redirect(w, r, "/order/"+order.OrderNo+"?token="+urlEscape(token))
		return
	}
	if time.Now().UTC().After(order.ExpiresAt) {
		_ = s.db.CloseOrder(r.Context(), order.OrderNo)
		s.renderError(w, r, http.StatusGone, "订单已超时关闭，请重新下单。")
		return
	}
	driver, found := s.findDriver(order.PaymentDriver)
	if !found || !payment.SupportsCurrency(driver, order.Currency) {
		s.renderError(w, r, http.StatusBadRequest, "支付方式不可用，请联系站长。")
		return
	}
	action, err := driver.Create(r.Context(), payment.Request{
		OrderNo:     order.OrderNo,
		Subject:     order.ProductName,
		AmountCents: order.TotalCents,
		Currency:    order.Currency,
		NotifyURL:   s.cfg.BaseURL + "/pay/notify/" + driver.Name(),
		ReturnURL:   s.cfg.BaseURL + "/pay/return/" + driver.Name() + "?order_no=" + order.OrderNo,
		ClientIP:    s.clientIP(r),
		Email:       order.Email,
		ExpiresAt:   order.ExpiresAt,
		Method:      order.PaymentMethod,
	})
	if err != nil {
		s.log.Warn("payment create failed", "driver", driver.Name(), "error", err)
		s.renderError(w, r, http.StatusBadGateway, "支付发起失败，请稍后重试或联系站长。")
		return
	}
	if action == nil || (action.Kind != "form" && action.Kind != "redirect" && action.Kind != "inline" && action.Kind != "qr") {
		s.renderError(w, r, http.StatusBadGateway, "支付服务返回了无效响应。")
		return
	}
	if action.Kind == "form" || action.Kind == "redirect" {
		target, err := url.Parse(action.URL)
		if err != nil || target.Hostname() == "" || target.User != nil || strings.ContainsAny(target.Host, ";*\\\t\r\n ") || (target.Scheme != "https" && target.Scheme != "http") {
			s.renderError(w, r, http.StatusBadGateway, "支付服务返回了无效地址。")
			return
		}
		if action.Kind == "form" {
			// Only this handoff page may submit to the selected provider origin.
			origin := target.Scheme + "://" + target.Host
			w.Header().Set("Content-Security-Policy", strings.Replace(w.Header().Get("Content-Security-Policy"), "form-action 'self'", "form-action 'self' "+origin, 1))
		}
	}
	var qrImage template.URL
	if action.Kind == "qr" {
		png, err := qrcode.Encode(action.URL, qrcode.Medium, 256)
		if err != nil {
			s.renderError(w, r, 502, "支付二维码生成失败。")
			return
		}
		qrImage = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
	}
	s.render(w, r, "pay", map[string]any{
		"Title":   "支付订单 " + order.OrderNo,
		"Order":   order,
		"Action":  action,
		"QRImage": qrImage,
		"Token":   token,
	})
}

// handlePaymentNotify processes provider webhooks.
func (s *Server) handlePaymentNotify(w http.ResponseWriter, r *http.Request) {
	driverName := r.PathValue("driver")
	driver, found := s.findDriver(driverName)
	if !found {
		http.Error(w, "unknown driver", http.StatusNotFound)
		return
	}
	var cb *payment.Callback
	var err error
	if signed, ok := driver.(interface {
		VerifyNotification(context.Context, http.Header, []byte) (*payment.Callback, error)
	}); ok {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if e != nil {
			http.Error(w, "bad request", 400)
			return
		}
		cb, err = signed.VerifyNotification(r.Context(), r.Header, body)
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		params := map[string]string{}
		for k, values := range r.Form {
			if len(values) != 1 {
				http.Error(w, "ambiguous parameter", 400)
				return
			}
			params[k] = values[0]
		}
		cb, err = driver.VerifyCallback(r.Context(), params, []byte(r.Form.Encode()))
	}

	if err != nil || cb == nil || cb.Driver != driverName {
		s.log.Warn("payment callback rejected", "driver", driverName, "error", err)
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	if !cb.Paid {
		s.log.Info("payment callback not paid", "driver", driverName, "order", cb.OrderNo)
		if driverName == "wechatpay" {
			w.WriteHeader(http.StatusNoContent)
		} else {
			_, _ = w.Write([]byte("success"))
		}
		return
	}
	ip := s.clientIP(r)
	if err := s.completePayment(r.Context(), cb, ip); err != nil {
		http.Error(w, "payment processing failed", http.StatusInternalServerError)
		return
	}
	if driverName == "wechatpay" {
		w.WriteHeader(http.StatusNoContent)
	} else {
		_, _ = w.Write([]byte("success"))
	}
}

func (s *Server) handlePaymentReturn(w http.ResponseWriter, r *http.Request) {
	orderNo := r.URL.Query().Get("order_no")
	if orderNo == "" {
		redirect(w, r, "/query")
		return
	}
	if s.readGrantValid(r, orderNo) && s.orderCredentialValid(r, orderNo) {
		redirect(w, r, "/order/"+url.PathEscape(orderNo))
		return
	}
	redirect(w, r, "/query")
}

// completePayment is the single place where money turns into stock.
func (s *Server) completePayment(ctx context.Context, cb *payment.Callback, ip string) error {
	if cb == nil {
		return errors.New("empty payment callback")
	}
	driver, found := s.findDriver(cb.Driver)
	if !found {
		return errors.New("payment driver is no longer available; manual review required")
	}
	if _, limited := driver.(interface{ SupportsCurrency(string) bool }); limited {
		order, err := s.db.OrderByNo(ctx, cb.OrderNo)
		if err != nil {
			return err
		}
		if cb.Driver == "epay" && order.PaymentMethod != "" && order.PaymentMethod != "cashier" && cb.Method != order.PaymentMethod {
			return errors.New("易支付通知渠道与订单不匹配")
		}
		if !payment.SupportsCurrency(driver, order.Currency) {
			return errors.New("payment currency is unsupported; manual review required")
		}
	}
	result, err := s.db.FulfillOrder(ctx, cb.OrderNo, cb.Driver, cb.TxnID, cb.AmountCents, cb.Raw)
	if err != nil {
		s.log.Error("fulfill failed", "order", cb.OrderNo, "driver", cb.Driver, "error", err)
		return err
	}
	if result.AlreadyDone {
		s.log.Info("duplicate payment callback ignored", "order", cb.OrderNo, "driver", cb.Driver)
		return nil
	}
	order := result.Order
	detail := fmt.Sprintf("driver=%s txn=%s amount=%d", cb.Driver, cb.TxnID, cb.AmountCents)
	if err := s.db.Audit(ctx, "system", "order.paid", order.OrderNo, ip, detail); err != nil {
		s.log.Warn("audit failed for payment", "error", err)
	}
	s.hook(ctx, "order.paid", map[string]any{
		"order_no":     order.OrderNo,
		"product_name": order.ProductName,
		"total_cents":  order.TotalCents,
		"email":        order.Email,
		"driver":       cb.Driver,
	})
	if result.ShortStock {
		s.log.Error("paid order is short on stock", "order", order.OrderNo)
		return nil
	}
	s.hook(ctx, "order.delivered", map[string]any{
		"order_no": order.OrderNo,
		"cards":    len(result.Cards),
		"email":    order.Email,
	})
	s.sendDeliveryMail(ctx, order, result.Cards)
	return nil
}

func (s *Server) sendDeliveryMail(ctx context.Context, order *store.Order, cards []store.CardKey) {
	if s.notifier == nil || order.Email == "" {
		return
	}
	var sb strings.Builder
	sb.WriteString("您好，您的订单已自动发货。\n\n")
	fmt.Fprintf(&sb, "订单号: %s\n商品: %s × %d\n金额: %s\n\n卡密:\n", order.OrderNo, order.ProductName, order.Quantity, formatMoney(order.TotalCents, order.Currency))
	for _, c := range cards {
		sb.WriteString(c.Secret + "\n")
	}
	sb.WriteString("\n请妥善保管卡密，已售出卡密不支持退换。\n")
	s.notifier.SendAsync(notifyMessage(order.Email, "【发货成功】"+order.ProductName, sb.String()))
}

func (s *Server) hook(ctx context.Context, event string, payload map[string]any) {
	if s.plugins == nil {
		return
	}
	// Detach from the request context so a client timeout cannot cancel hooks.
	s.plugins.CallHook(context.WithoutCancel(ctx), event, payload)
}

func parseInt64(v string) int64 {
	out, err := strconv.ParseUint(strings.TrimSpace(v), 10, 62)
	if err != nil {
		return 0
	}
	return int64(out)
}

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
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
	filter := store.ProductFilter{Status: store.ProductActive, Search: query, Limit: 200}
	var activeCat *store.Category
	if catSlug != "" {
		if c, err := s.db.CategoryBySlug(ctx, catSlug); err == nil {
			activeCat = c
			filter.CategoryID = c.ID
		}
	}
	products, err := s.db.ListProducts(ctx, filter)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载商品失败。")
		return
	}
	s.render(w, r, "home", map[string]any{
		"Title":      s.setting(store.SettingSiteName, "Card Word"),
		"Categories": categories,
		"Products":   products,
		"Search":     query,
		"ActiveCat":  activeCat,
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
	s.render(w, r, "product", map[string]any{
		"Title":    product.Name,
		"Product":  product,
		"Drivers":  s.availableDrivers(),
		"Currency": product.Currency,
	})
}

// availableDrivers lists the drivers the operator enabled, plus plugin drivers.
func (s *Server) availableDrivers() []map[string]string {
	selected := s.setting(store.SettingPaymentDriver, "manual")
	out := make([]map[string]string, 0, 4)
	for _, d := range s.allDrivers() {
		name := d.Name()
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
		out = append(out, map[string]string{"Name": name, "Title": d.Title()})
	}
	if len(out) == 0 {
		out = append(out, map[string]string{"Name": "manual", "Title": "人工确认"})
	}
	return out
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
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "商品不存在。")
		return
	}
	backURL := "/p/" + product.Slug
	if quantity <= 0 {
		quantity = product.MinQty
	}
	if email != "" {
		if _, err := mail.ParseAddress(email); err != nil {
			flashRedirect(w, r, backURL, "", "邮箱格式不正确。")
			return
		}
	}
	if !agreed {
		flashRedirect(w, r, backURL, "", "请先阅读并同意购买条款。")
		return
	}
	if driverName == "" {
		drivers := s.availableDrivers()
		if len(drivers) > 0 {
			driverName = drivers[0]["Name"]
		}
	}
	if _, ok := s.findDriver(driverName); !ok {
		flashRedirect(w, r, backURL, "", "支付方式不可用。")
		return
	}
	idem := strings.TrimSpace(r.FormValue("idem"))
	if idem == "" {
		idem, _ = cryptoutil.RandomToken(16)
	}
	result, err := s.db.CreateOrder(ctx, store.CreateOrderInput{
		ProductID:      productID,
		Quantity:       quantity,
		Email:          email,
		Contact:        contact,
		CouponCode:     coupon,
		PaymentDriver:  driverName,
		ClientIP:       s.clientIP(r),
		IdempotencyKey: "idem:" + idem,
		TTL:            s.cfg.OrderTTL,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOutOfStock):
			flashRedirect(w, r, backURL, "", "库存不足，请减少购买数量。")
		case errors.Is(err, store.ErrNotFound):
			flashRedirect(w, r, backURL, "", "优惠码不存在。")
		case errors.Is(err, store.ErrInvalidStatus):
			flashRedirect(w, r, backURL, "", "商品当前不可购买。")
		default:
			s.log.Warn("create order failed", "error", err)
			flashRedirect(w, r, backURL, "", err.Error())
		}
		return
	}
	order := result.Order
	if result.Replayed {
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
	redirect(w, r, "/order/"+order.OrderNo+"?token="+urlEscape(result.QueryToken))
}

func (s *Server) handleOrderView(w http.ResponseWriter, r *http.Request) {
	orderNo := r.PathValue("no")
	token := r.URL.Query().Get("token")
	order, cards, ok := s.loadOrderForBuyer(w, r, orderNo, token)
	if !ok {
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
	ctx := r.Context()
	if token == "" {
		s.renderError(w, r, http.StatusForbidden, "缺少查询凭证，请通过订单查询页访问。")
		return nil, nil, false
	}
	valid, err := s.db.VerifyOrderQueryToken(ctx, orderNo, token)
	if err != nil || !valid {
		s.renderError(w, r, http.StatusForbidden, "订单号或查询凭证不正确。")
		return nil, nil, false
	}
	order, err := s.db.OrderByNo(ctx, orderNo)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "订单不存在。")
		return nil, nil, false
	}
	var cards []store.CardKey
	if order.Status == store.OrderDelivered {
		cards, err = s.db.OrderCards(ctx, order.ID)
		if err != nil {
			s.log.Error("load order cards failed", "order", orderNo, "error", err)
		}
	}
	return order, cards, true
}

func (s *Server) handleQueryForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "query", map[string]any{"Title": "订单查询"})
}

func (s *Server) handleQuerySubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	orderNo := strings.TrimSpace(r.FormValue("order_no"))
	token := strings.TrimSpace(r.FormValue("token"))
	if orderNo == "" || token == "" {
		s.renderError(w, r, http.StatusBadRequest, "请输入订单号与查询凭证。")
		return
	}
	redirect(w, r, "/order/"+orderNo+"?token="+urlEscape(token))
}

func (s *Server) handlePay(w http.ResponseWriter, r *http.Request) {
	orderNo := r.PathValue("no")
	token := r.URL.Query().Get("token")
	order, _, ok := s.loadOrderForBuyer(w, r, orderNo, token)
	if !ok {
		return
	}
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
	if !found {
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
	})
	if err != nil {
		s.log.Warn("payment create failed", "driver", driver.Name(), "error", err)
		s.renderError(w, r, http.StatusBadGateway, "支付发起失败，请稍后重试或联系站长。")
		return
	}
	s.render(w, r, "pay", map[string]any{
		"Title":  "支付订单 " + order.OrderNo,
		"Order":  order,
		"Action": action,
		"Token":  token,
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	params := map[string]string{}
	for k := range r.Form {
		params[k] = r.FormValue(k)
	}
	body := []byte(r.URL.RawQuery + "&" + r.PostForm.Encode())
	cb, err := driver.VerifyCallback(r.Context(), params, body)
	if err != nil {
		s.log.Warn("payment callback rejected", "driver", driverName, "error", err)
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	if !cb.Paid {
		s.log.Info("payment callback not paid", "driver", driverName, "order", cb.OrderNo)
		_, _ = w.Write([]byte("success"))
		return
	}
	ip := s.clientIP(r)
	s.completePayment(r.Context(), cb, ip)
	_, _ = w.Write([]byte("success"))
}

func (s *Server) handlePaymentReturn(w http.ResponseWriter, r *http.Request) {
	orderNo := r.URL.Query().Get("order_no")
	if orderNo == "" {
		redirect(w, r, "/query")
		return
	}
	order, err := s.db.OrderByNo(r.Context(), orderNo)
	if err != nil {
		redirect(w, r, "/query")
		return
	}
	if order.Status == store.OrderPending {
		s.render(w, r, "pay_pending", map[string]any{
			"Title": "支付处理中",
			"Order": order,
		})
		return
	}
	redirect(w, r, "/query?ok="+urlEscape("支付已完成，请使用订单号与查询凭证查看卡密。"))
}

// completePayment is the single place where money turns into stock.
func (s *Server) completePayment(ctx context.Context, cb *payment.Callback, ip string) {
	result, err := s.db.FulfillOrder(ctx, cb.OrderNo, cb.Driver, cb.TxnID, cb.AmountCents, cb.Raw)
	if err != nil {
		s.log.Error("fulfill failed", "order", cb.OrderNo, "driver", cb.Driver, "error", err)
		return
	}
	if result.AlreadyDone {
		s.log.Info("duplicate payment callback ignored", "order", cb.OrderNo, "driver", cb.Driver)
		return
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
		return
	}
	s.hook(ctx, "order.delivered", map[string]any{
		"order_no": order.OrderNo,
		"cards":    len(result.Cards),
		"email":    order.Email,
	})
	s.sendDeliveryMail(ctx, order, result.Cards)
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
	var out int64
	for _, c := range strings.TrimSpace(v) {
		if c < '0' || c > '9' {
			return 0
		}
		out = out*10 + int64(c-'0')
		if out > 1<<62 {
			return 0
		}
	}
	return out
}

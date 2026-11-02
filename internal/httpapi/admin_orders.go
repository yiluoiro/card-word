package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/store"
)

func nullInt64(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: true}
}

func (s *Server) handleAdminOrders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	const pageSize = 50
	page := int(parseInt64(r.URL.Query().Get("page")))
	if page < 1 {
		page = 1
	}
	filter := store.OrderFilter{Status: status, Search: search}
	total, err := s.db.CountOrders(ctx, filter)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载订单失败。")
		return
	}
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	filter.Limit = pageSize
	filter.Offset = (page - 1) * pageSize
	orders, err := s.db.ListOrders(ctx, filter)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载订单失败。")
		return
	}
	queryBase := ""
	if status != "" {
		queryBase += "status=" + urlEscape(status) + "&"
	}
	if search != "" {
		queryBase += "q=" + urlEscape(search) + "&"
	}
	s.render(w, r, "admin_orders", map[string]any{
		"Title":      "订单管理",
		"Orders":     orders,
		"Status":     status,
		"Search":     search,
		"Page":       page,
		"TotalPages": pages,
		"Total":      total,
		"QueryBase":  queryBase,
	})
}

func (s *Server) handleAdminOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orderNo := r.PathValue("no")
	order, err := s.db.OrderByNo(ctx, orderNo)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "订单不存在。")
		return
	}
	cards, err := s.db.OrderCards(ctx, order.ID)
	if err != nil {
		s.log.Warn("load order cards failed", "order", orderNo, "error", err)
	}
	payments, err := s.db.ListPayments(ctx, order.ID)
	if err != nil {
		s.log.Warn("load order payments failed", "order", orderNo, "error", err)
	}
	s.render(w, r, "admin_order", map[string]any{
		"Title":    "订单 " + order.OrderNo,
		"Order":    order,
		"Cards":    cards,
		"Payments": payments,
	})
}

// handleAdminFulfill lets an operator confirm an offline payment.
func (s *Server) handleAdminFulfill(w http.ResponseWriter, r *http.Request) {
	orderNo := r.PathValue("no")
	user := s.currentUser(r)
	actor := "admin"
	if user != nil {
		actor = user.Username
	}
	result, err := s.db.FulfillOrder(r.Context(), orderNo, "manual", "admin:"+actor, 0, "confirmed by "+actor)
	if err != nil {
		s.audit(r, "order.fulfill.failed", orderNo, err.Error())
		flashRedirect(w, r, "/admin/orders/"+orderNo, "", "确认失败："+err.Error())
		return
	}
	if result.ShortStock {
		s.audit(r, "order.fulfill.short_stock", orderNo, "")
		flashRedirect(w, r, "/admin/orders/"+orderNo, "", "库存不足，请先补充卡密后重试补发。")
		return
	}
	if result.AlreadyDone {
		flashRedirect(w, r, "/admin/orders/"+orderNo, "", "该订单已处理。")
		return
	}
	s.audit(r, "order.fulfill", orderNo, "manual confirmation")
	s.hook(r.Context(), "order.paid", map[string]any{"order_no": orderNo, "driver": "manual", "total_cents": result.Order.TotalCents})
	s.hook(r.Context(), "order.delivered", map[string]any{"order_no": orderNo, "cards": len(result.Cards)})
	s.sendDeliveryMail(r.Context(), result.Order, result.Cards)
	flashRedirect(w, r, "/admin/orders/"+orderNo, "已确认并发货", "")
}

func (s *Server) handleAdminRetry(w http.ResponseWriter, r *http.Request) {
	orderNo := r.PathValue("no")
	result, err := s.db.RetryDelivery(r.Context(), orderNo)
	if err != nil {
		if errors.Is(err, store.ErrOutOfStock) {
			flashRedirect(w, r, "/admin/orders/"+orderNo, "", "库存不足，无法补发。")
			return
		}
		flashRedirect(w, r, "/admin/orders/"+orderNo, "", "补发失败："+err.Error())
		return
	}
	s.audit(r, "order.retry", orderNo, "cards issued")
	s.hook(r.Context(), "order.delivered", map[string]any{"order_no": orderNo, "cards": len(result.Cards)})
	s.sendDeliveryMail(r.Context(), result.Order, result.Cards)
	flashRedirect(w, r, "/admin/orders/"+orderNo, "已补发", "")
}

// handleAdminOrderNote stores an operator note on an order.
func (s *Server) handleAdminOrderNote(w http.ResponseWriter, r *http.Request) {
	orderNo := r.PathValue("no")
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if len(note) > 1000 {
		flashRedirect(w, r, "/admin/orders/"+orderNo, "", "备注过长（最多 1000 字）。")
		return
	}
	if err := s.db.SetOrderNote(r.Context(), orderNo, note); err != nil {
		flashRedirect(w, r, "/admin/orders/"+orderNo, "", "保存失败。")
		return
	}
	s.audit(r, "order.note", orderNo, note)
	flashRedirect(w, r, "/admin/orders/"+orderNo, "备注已保存", "")
}

func (s *Server) handleAdminCloseOrder(w http.ResponseWriter, r *http.Request) {
	orderNo := r.PathValue("no")
	if err := s.db.CloseOrder(r.Context(), orderNo); err != nil {
		flashRedirect(w, r, "/admin/orders/"+orderNo, "", "关闭失败：订单状态不允许。")
		return
	}
	s.audit(r, "order.close", orderNo, "")
	s.hook(r.Context(), "order.closed", map[string]any{"order_no": orderNo})
	flashRedirect(w, r, "/admin/orders/"+orderNo, "订单已关闭", "")
}

func (s *Server) handleAdminRefund(w http.ResponseWriter, r *http.Request) {
	orderNo := r.PathValue("no")
	note := "refunded by operator"
	if err := r.ParseForm(); err == nil {
		if v := strings.TrimSpace(r.FormValue("note")); v != "" {
			note = v
		}
	}
	if err := s.db.MarkRefunded(r.Context(), orderNo, note); err != nil {
		flashRedirect(w, r, "/admin/orders/"+orderNo, "", "退款标记失败：订单状态不允许。")
		return
	}
	s.audit(r, "order.refund", orderNo, note)
	flashRedirect(w, r, "/admin/orders/"+orderNo, "已标记为退款", "")
}

// ---------- coupons ----------

func (s *Server) handleAdminCoupons(w http.ResponseWriter, r *http.Request) {
	coupons, err := s.db.ListCoupons(r.Context(), 200)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载优惠码失败。")
		return
	}
	products, _ := s.db.ListProducts(r.Context(), store.ProductFilter{Limit: 200})
	s.render(w, r, "admin_coupons", map[string]any{
		"Title":    "优惠码",
		"Coupons":  coupons,
		"Products": products,
	})
}

func (s *Server) handleAdminCouponCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	coupon := &store.Coupon{
		Code:          strings.TrimSpace(r.FormValue("code")),
		Kind:          strings.TrimSpace(r.FormValue("kind")),
		MaxUses:       parseInt64(r.FormValue("max_uses")),
		MinTotalCents: parsePriceCents(r.FormValue("min_total")),
		Active:        true,
	}
	if coupon.Kind == store.CouponPercent {
		coupon.Value = parseInt64(r.FormValue("percent"))
	} else {
		coupon.Value = parsePriceCents(r.FormValue("amount"))
	}
	if productID := parseInt64(r.FormValue("product_id")); productID > 0 {
		coupon.ProductID = nullInt64(productID)
	}
	if exp := strings.TrimSpace(r.FormValue("expires_at")); exp != "" {
		if t, err := time.Parse("2006-01-02", exp); err == nil {
			coupon.ExpiresAt = &t
		}
	}
	if _, err := s.db.CreateCoupon(r.Context(), coupon); err != nil {
		flashRedirect(w, r, "/admin/coupons", "", "创建失败："+err.Error())
		return
	}
	s.audit(r, "coupon.create", coupon.Code, "")
	flashRedirect(w, r, "/admin/coupons", "优惠码已创建", "")
}

func (s *Server) handleAdminCouponDelete(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	if err := s.db.DeleteCoupon(r.Context(), id); err != nil {
		flashRedirect(w, r, "/admin/coupons", "", "删除失败。")
		return
	}
	s.audit(r, "coupon.delete", "", "")
	flashRedirect(w, r, "/admin/coupons", "优惠码已删除", "")
}

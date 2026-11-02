package httpapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/yiluoiro/card-word/internal/store"
)

const exportRowLimit = 5000

// handleAdminOrdersExport streams the filtered order list as CSV. Exports
// include buyer contact details, so they are limited to admins and audited.
func (s *Server) handleAdminOrdersExport(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	search := r.URL.Query().Get("q")
	orders, err := s.db.ListOrders(r.Context(), store.OrderFilter{
		Status: status,
		Search: search,
		Limit:  exportRowLimit,
	})
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "导出失败。")
		return
	}

	filename := fmt.Sprintf("orders-%s.csv", time.Now().Format("20060102-1504"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	// UTF-8 BOM keeps Excel from mangling Chinese headers.
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{
		"订单号", "商品", "数量", "单价", "小计", "优惠", "应付", "货币",
		"状态", "邮箱", "联系方式", "支付方式", "交易号", "创建时间", "支付时间", "发货时间",
	})
	for _, o := range orders {
		_ = cw.Write([]string{
			o.OrderNo, o.ProductName, strconv.Itoa(o.Quantity),
			moneyShort(o.UnitPriceCents), moneyShort(o.SubtotalCents),
			moneyShort(o.DiscountCents), moneyShort(o.TotalCents), o.Currency,
			statusText(o.Status), o.Email, o.Contact, o.PaymentDriver, o.PaymentTxn,
			o.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			timeOrDash(o.PaidAt), timeOrDash(o.DeliveredAt),
		})
	}
	s.audit(r, "order.export", "", fmt.Sprintf("rows=%d status=%q search=%q", len(orders), status, search))
}

func timeOrDash(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

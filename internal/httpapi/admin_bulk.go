package httpapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/yiluoiro/card-word/internal/store"
)

const bulkMaxIDs = 500

func parseIDList(values []string) []int64 {
	out := make([]int64, 0, len(values))
	seen := map[int64]bool{}
	for _, v := range values {
		id := parseInt64(v)
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= bulkMaxIDs {
			break
		}
	}
	return out
}

// handleAdminProductsBulk sets the status of many products at once.
func (s *Server) handleAdminProductsBulk(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	ids := parseIDList(r.Form["ids"])
	status := strings.TrimSpace(r.FormValue("status"))
	if len(ids) == 0 {
		flashRedirect(w, r, "/admin/products", "", "请先勾选商品。")
		return
	}
	n, err := s.db.SetProductsStatus(r.Context(), ids, status)
	if err != nil {
		flashRedirect(w, r, "/admin/products", "", "批量操作失败："+err.Error())
		return
	}
	s.audit(r, "product.bulk_status", "", fmt.Sprintf("status=%s count=%d", status, n))
	flashRedirect(w, r, "/admin/products", "已更新 "+strconv.FormatInt(n, 10)+" 个商品", "")
}

// handleAdminCardsBulk disables, restores or deletes selected cards.
func (s *Server) handleAdminCardsBulk(w http.ResponseWriter, r *http.Request) {
	productID := parseInt64(r.PathValue("id"))
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	back := "/admin/products/" + strconv.FormatInt(productID, 10) + "/cards"
	ids := parseIDList(r.Form["ids"])
	if len(ids) == 0 {
		flashRedirect(w, r, back, "", "请先勾选卡密。")
		return
	}
	for _, id := range ids {
		card, err := s.db.CardMetadata(r.Context(), id)
		if err != nil || card.ProductID != productID {
			s.renderError(w, r, 422, "勾选项不属于当前商品，请刷新后重试。")
			return
		}
	}
	action := strings.TrimSpace(r.FormValue("action"))
	switch action {
	case "disable":
		n, err := s.db.SetCardsStatus(r.Context(), ids, store.CardDisabled)
		if err != nil {
			flashRedirect(w, r, back, "", "操作失败："+err.Error())
			return
		}
		s.audit(r, "card.bulk_disable", strconv.FormatInt(productID, 10), fmt.Sprintf("count=%d", n))
		flashRedirect(w, r, back, "已停用 "+strconv.FormatInt(n, 10)+" 条卡密", "")
	case "enable":
		n, err := s.db.SetCardsStatus(r.Context(), ids, store.CardAvailable)
		if err != nil {
			flashRedirect(w, r, back, "", "操作失败："+err.Error())
			return
		}
		s.audit(r, "card.bulk_enable", strconv.FormatInt(productID, 10), fmt.Sprintf("count=%d", n))
		flashRedirect(w, r, back, "已启用 "+strconv.FormatInt(n, 10)+" 条卡密", "")
	case "delete":
		n, err := s.db.DeleteCards(r.Context(), ids)
		if err != nil {
			flashRedirect(w, r, back, "", "删除失败："+err.Error())
			return
		}
		s.audit(r, "card.bulk_delete", strconv.FormatInt(productID, 10), fmt.Sprintf("count=%d", n))
		flashRedirect(w, r, back, "已删除 "+strconv.FormatInt(n, 10)+" 条未售出卡密（已售出的会保留）", "")
	default:
		flashRedirect(w, r, back, "", "未知的批量操作。")
	}
}

// handleAdminCardsExport streams decrypted cards of one product as CSV.
func (s *Server) handleAdminCardsExport(w http.ResponseWriter, r *http.Request) {
	productID := parseInt64(r.PathValue("id"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	product, err := s.db.ProductByID(r.Context(), productID)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "商品不存在。")
		return
	}
	view := listView(r, "cards", "")
	view.Filter.Size = exportRowLimit
	view.Filter.Page = 1
	raw, _, err := s.db.AdminList(r.Context(), "cards", view.Filter)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "导出失败。")
		return
	}
	cards := raw.([]store.CardKey)
	for i := range cards {
		secret, e := s.db.RevealCard(r.Context(), cards[i].ID)
		if e != nil {
			s.renderError(w, r, 500, "解密失败。")
			return
		}
		cards[i].Secret = secret
	}
	filename := "cards-" + product.Slug + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"ID", "卡密", "标签", "规格", "状态", "售出时间", "创建时间"})
	for _, c := range cards {
		_ = cw.Write(csvTextRow([]string{
			strconv.FormatInt(c.ID, 10), c.Secret, c.Label, c.VariantName, statusText(c.Status),
			timeOrDash(c.SoldAt), c.CreatedAt.Local().Format("2006-01-02 15:04:05"),
		}))
	}
	s.audit(r, "card.export", product.Slug, fmt.Sprintf("rows=%d status=%q", len(cards), status))
}

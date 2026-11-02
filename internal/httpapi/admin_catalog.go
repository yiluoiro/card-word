package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

// ---------- products ----------

func (s *Server) handleAdminProducts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	products, err := s.db.ListProducts(ctx, store.ProductFilter{Search: search, Limit: 200})
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载商品失败。")
		return
	}
	cats, err := s.db.ListCategories(ctx, false)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载分类失败。")
		return
	}
	s.render(w, r, "admin_products", map[string]any{
		"Title":      "商品管理",
		"Products":   products,
		"Categories": cats,
		"Search":     search,
	})
}

func (s *Server) handleAdminProductForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var product *store.Product
	id := parseInt64(r.PathValue("id"))
	if id > 0 {
		p, err := s.db.ProductByID(ctx, id)
		if err != nil {
			s.renderError(w, r, http.StatusNotFound, "商品不存在。")
			return
		}
		product = p
	}
	cats, _ := s.db.ListCategories(ctx, false)
	s.render(w, r, "admin_product_form", map[string]any{
		"Title":      "编辑商品",
		"Product":    product,
		"Categories": cats,
	})
}

func (s *Server) handleAdminProductSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	ctx := r.Context()
	id := parseInt64(r.FormValue("id"))
	priceCents := parsePriceCents(r.FormValue("price"))
	if priceCents < 0 {
		flashRedirect(w, r, "/admin/products", "", "价格格式不正确。")
		return
	}
	in := store.ProductInput{
		Name:        strings.TrimSpace(r.FormValue("name")),
		Slug:        strings.TrimSpace(r.FormValue("slug")),
		CategoryID:  parseInt64(r.FormValue("category_id")),
		Summary:     strings.TrimSpace(r.FormValue("summary")),
		Description: strings.TrimSpace(r.FormValue("description")),
		PriceCents:  priceCents,
		Currency:    orDefault(strings.TrimSpace(r.FormValue("currency")), "CNY"),
		Status:      orDefault(strings.TrimSpace(r.FormValue("status")), store.ProductActive),
		MinQty:      int(parseInt64(r.FormValue("min_qty"))),
		MaxQty:      int(parseInt64(r.FormValue("max_qty"))),
		Sort:        int(parseInt64(r.FormValue("sort"))),
	}
	if in.Name == "" {
		flashRedirect(w, r, "/admin/products", "", "商品名称不能为空。")
		return
	}
	if id > 0 {
		if _, err := s.db.UpdateProduct(ctx, id, in); err != nil {
			flashRedirect(w, r, "/admin/products", "", "保存失败："+err.Error())
			return
		}
		s.audit(r, "product.update", in.Name, "")
		s.hook(ctx, "product.updated", map[string]any{"product_id": id, "name": in.Name})
		flashRedirect(w, r, "/admin/products", "商品已保存", "")
		return
	}
	product, err := s.db.CreateProduct(ctx, in)
	if err != nil {
		flashRedirect(w, r, "/admin/products", "", "创建失败："+err.Error())
		return
	}
	s.audit(r, "product.create", product.Name, "")
	flashRedirect(w, r, "/admin/products", "商品已创建，请导入卡密", "")
}

func (s *Server) handleAdminProductDelete(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	if err := s.db.DeleteProduct(r.Context(), id); err != nil {
		flashRedirect(w, r, "/admin/products", "", "删除失败。")
		return
	}
	s.audit(r, "product.delete", cryptoutil.Itoa(int(id)), "")
	flashRedirect(w, r, "/admin/products", "商品已删除", "")
}

// ---------- cards ----------

func (s *Server) handleAdminCards(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	productID := parseInt64(r.PathValue("id"))
	product, err := s.db.ProductByID(ctx, productID)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "商品不存在。")
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	cards, err := s.db.ListCards(ctx, store.CardFilter{ProductID: productID, Status: status, Limit: 200})
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载卡密失败。")
		return
	}
	stock, _ := s.db.StockCount(ctx, productID)
	s.render(w, r, "admin_cards", map[string]any{
		"Title":   "卡密库存 · " + product.Name,
		"Product": product,
		"Cards":   cards,
		"Stock":   stock,
		"Status":  status,
	})
}

func (s *Server) handleAdminCardsImport(w http.ResponseWriter, r *http.Request) {
	productID := parseInt64(r.PathValue("id"))
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	raw := r.FormValue("secrets")
	if strings.TrimSpace(raw) == "" {
		flashRedirect(w, r, "/admin/products/"+cryptoutil.Itoa(int(productID))+"/cards", "", "请输入至少一条卡密。")
		return
	}
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	if len(lines) > 5000 {
		flashRedirect(w, r, "/admin/products/"+cryptoutil.Itoa(int(productID))+"/cards", "", "单次最多导入 5000 条。")
		return
	}
	imported, err := s.db.AddCards(r.Context(), productID, lines, label)
	if err != nil {
		flashRedirect(w, r, "/admin/products/"+cryptoutil.Itoa(int(productID))+"/cards", "", "导入失败："+err.Error())
		return
	}
	s.audit(r, "card.import", cryptoutil.Itoa(int(productID)), "imported="+cryptoutil.Itoa(imported))
	s.hook(r.Context(), "product.updated", map[string]any{"product_id": productID, "imported": imported})
	flashRedirect(w, r, "/admin/products/"+cryptoutil.Itoa(int(productID))+"/cards", "已导入 "+cryptoutil.Itoa(imported)+" 条卡密", "")
}

func (s *Server) handleAdminCardDisable(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	if err := s.db.SetCardStatus(r.Context(), id, store.CardDisabled); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			flashRedirect(w, r, "/admin/products", "", "卡密不存在。")
			return
		}
		flashRedirect(w, r, "/admin/products", "", "操作失败。")
		return
	}
	s.audit(r, "card.disable", cryptoutil.Itoa(int(id)), "")
	flashRedirect(w, r, "/admin/products", "卡密已停用", "")
}

func (s *Server) handleAdminCardReveal(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	secret, err := s.db.RevealCard(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "卡密不存在或解密失败")
		return
	}
	s.audit(r, "card.reveal", cryptoutil.Itoa(int(id)), "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "secret": secret})
}

// parsePriceCents converts "12.30" into 1230 minor units.
func parsePriceCents(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	neg := strings.HasPrefix(v, "-")
	v = strings.TrimPrefix(v, "-")
	parts := strings.SplitN(v, ".", 2)
	units := parseInt64(parts[0])
	var cents int64
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 2 {
			frac = frac[:2]
		}
		for len(frac) < 2 {
			frac += "0"
		}
		cents = parseInt64(frac)
	}
	total := units*100 + cents
	if neg {
		total = -total
	}
	return total
}

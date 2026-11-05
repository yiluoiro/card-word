package httpapi

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
)

// ---------- products ----------

func (s *Server) handleAdminProducts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != store.ProductActive && status != store.ProductDraft && status != store.ProductArchived {
		status = ""
	}
	categoryID := parseInt64(r.URL.Query().Get("category_id"))
	stock := choice(r.URL.Query().Get("stock"), choices("low:库存不足 empty:缺货 ready:充足"))
	size := int(parseInt64(r.URL.Query().Get("size")))
	if size != 20 && size != 50 && size != 100 {
		size = 50
	}
	filter := store.ProductFilter{Search: search, Status: status, CategoryID: categoryID, Stock: stock, Limit: size}
	total, err := s.db.CountProducts(ctx, filter)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载商品失败。")
		return
	}
	filters := url.Values{"size": {strconv.Itoa(size)}}
	if stock != "" {
		filters.Set("stock", stock)
	}
	if search != "" {
		filters.Set("q", search)
	}
	if status != "" {
		filters.Set("status", status)
	}
	if categoryID > 0 {
		filters.Set("category_id", strconv.FormatInt(categoryID, 10))
	}
	pagination := paginate(r.URL.Query().Get("page"), total, filter.Limit, "/admin/products", "", filters)
	filter.Offset = pagination.Offset
	products, err := s.db.ListProducts(ctx, filter)
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
		"Stock":      stock,
		"Size":       size,
		"Status":     status,
		"CategoryID": categoryID,
		"Pagination": pagination,
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
		p.PriceCents = p.BasePriceCents
		product = p
	}
	cats, _ := s.db.ListCategories(ctx, false)
	variants, _ := s.db.ProductVariants(ctx, id, false)
	s.render(w, r, "admin_product_form", map[string]any{
		"ListReturn": listReturn(r.URL.Query().Get("return_to"), "/admin/products"),
		"Variants":   variants,
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
	fail := func(message string) {
		product := &store.Product{
			ID: id, Name: in.Name, Slug: in.Slug, CategoryID: nullInt64(in.CategoryID),
			Summary: in.Summary, Description: in.Description, PriceCents: in.PriceCents,
			Currency: in.Currency, Status: in.Status, MinQty: in.MinQty, MaxQty: in.MaxQty, Sort: in.Sort,
		}
		if id > 0 {
			if saved, err := s.db.ProductByID(ctx, id); err == nil {
				product.CoverPath = saved.CoverPath
			}
		}
		cats, _ := s.db.ListCategories(ctx, false)
		s.renderStatus(w, r, http.StatusUnprocessableEntity, "admin_product_form", map[string]any{
			"Title": "编辑商品", "Product": product, "Categories": cats,
			"Error": message, "Submitted": true, "Form": r.PostForm,
		})
	}
	if strings.TrimSpace(r.FormValue("price")) == "" || priceCents < 0 {
		fail("价格格式不正确，请输入非负金额，最多两位小数。")
		return
	}
	if len(in.Summary) > maxMarkdownBytes || len(in.Description) > maxMarkdownBytes {
		fail("简介和详细说明各不能超过 100 KB，请缩短内容后重试。")
		return
	}
	if in.Name == "" {
		fail("商品名称不能为空。")
		return
	}
	if id > 0 {
		if _, err := s.db.UpdateProduct(ctx, id, in); err != nil {
			fail("保存失败：" + err.Error())
			return
		}
		s.audit(r, "product.update", in.Name, "")
		s.hook(ctx, "product.updated", map[string]any{"product_id": id, "name": in.Name})
		flashRedirect(w, r, "/admin/products", "商品已保存", "")
		return
	}
	product, err := s.db.CreateProduct(ctx, in)
	if err != nil {
		fail("创建失败：" + err.Error())
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
	cards, view, ok := s.adminList(w, r, s.db, "cards", "")
	if !ok {
		return
	}
	stock, _ := s.db.StockCount(ctx, productID)
	variants, _ := s.db.ProductVariants(ctx, productID, false)
	view.Variants = variants
	s.render(w, r, "admin_cards", map[string]any{
		"Title":    "卡密库存 · " + product.Name,
		"Product":  product,
		"Cards":    cards,
		"Stock":    stock,
		"Variants": variants,
		"Status":   view.Filter.Status, "Pagination": view.Pagination, "List": view,
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
	imported, err := s.db.AddVariantCards(r.Context(), productID, parseInt64(r.FormValue("variant_id")), lines, label)
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

// parsePriceCents converts an unsigned decimal into minor units, returning -1
// on malformed input or overflow. Empty optional monetary fields mean zero.
func parsePriceCents(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	parts := strings.SplitN(v, ".", 2)
	if parts[0] == "" {
		return -1
	}
	for _, ch := range parts[0] {
		if ch < '0' || ch > '9' {
			return -1
		}
	}
	units, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return -1
	}
	var cents int64
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) == 0 || len(frac) > 2 {
			return -1
		}
		for _, ch := range frac {
			if ch < '0' || ch > '9' {
				return -1
			}
		}
		for len(frac) < 2 {
			frac += "0"
		}
		cents = parseInt64(frac)
	}
	if units > (math.MaxInt64-cents)/100 {
		return -1
	}
	return units*100 + cents
}

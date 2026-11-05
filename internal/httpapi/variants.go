package httpapi

import (
	"fmt"
	"github.com/yiluoiro/card-word/internal/store"
	"net/http"
)

func (s *Server) handleProductVariant(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, 400, "表单格式错误。")
		return
	}
	productID := parseInt64(r.PathValue("id"))
	id := parseInt64(r.FormValue("variant_id"))
	back := fmt.Sprintf("/admin/products/%d", productID)
	var err error
	if r.FormValue("action") == "delete" {
		err = s.db.DeleteProductVariant(r.Context(), productID, id)
	} else {
		price := parsePriceCents(r.FormValue("price"))
		if r.FormValue("price") == "" {
			price = -1
		}
		err = s.db.SaveProductVariant(r.Context(), store.ProductVariant{ID: id, ProductID: productID, Name: r.FormValue("name"), PriceCents: price, Active: r.FormValue("active") == "1", Sort: int(parseInt64(r.FormValue("sort")))})
	}
	if err != nil {
		flashRedirect(w, r, back, "", "规格保存失败："+err.Error())
		return
	}
	s.audit(r, "product.variant", fmt.Sprint(productID), fmt.Sprint(id))
	flashRedirect(w, r, back, "规格已更新。", "")
}

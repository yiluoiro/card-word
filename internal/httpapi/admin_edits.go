package httpapi

import (
	"fmt"
	"github.com/yiluoiro/card-word/internal/store"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type editField struct {
	Name, Label, Value, Type string
	Required                 bool
}

func (s *Server) handleListEdit(w http.ResponseWriter, r *http.Request) {
	s.renderListEdit(w, r, 200, "")
}
func (s *Server) renderListEdit(w http.ResponseWriter, r *http.Request, status int, message string) {
	kind := r.PathValue("kind")
	id := parseInt64(r.PathValue("id"))
	var fields []editField
	title, action, back := "", "", "/admin/"+kind
	add := func(name, label, value, typ string, required bool) {
		fields = append(fields, editField{name, label, value, typ, required})
	}
	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	switch kind {
	case "categories":
		raw, _, err := s.db.AdminList(r.Context(), "categories", store.AdminFilter{RecordID: id})
		if err != nil {
			s.renderError(w, r, 500, "读取失败")
			return
		}
		var found bool
		for _, c := range raw.([]store.Category) {
			if c.ID == id {
				found = true
				add("name", "名称", c.Name, "text", true)
				add("slug", "别名", c.Slug, "text", true)
				add("sort", "排序", fmt.Sprint(c.Sort), "number", true)
				v := ""
				if c.Visible {
					v = "1"
				}
				add("visible", "前台可见", v, "checkbox", false)
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		title, action = "编辑分类", "/admin/categories"
	case "coupons":
		c, err := s.db.CouponByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		title, action = "编辑优惠码", "/admin/coupons"
		add("code", "优惠码", c.Code, "text", true)
		add("kind", "类型（percent 或 fixed）", c.Kind, "text", true)
		if c.Kind == "percent" {
			add("percent", "折扣百分比", num(c.Value), "number", false)
			add("amount", "固定金额（切换类型时填写）", "", "text", false)
		} else {
			add("percent", "折扣百分比（切换类型时填写）", "", "number", false)
			add("amount", "固定金额", moneyShort(c.Value), "text", false)
		}
		add("min_total", "最低消费", moneyShort(c.MinTotalCents), "text", true)
		add("max_uses", "最多使用次数，0 为不限", num(c.MaxUses), "number", true)
		add("product_id", "限定商品 ID，0 为全部", num(c.ProductID.Int64), "number", true)
		exp := ""
		if c.ExpiresAt != nil {
			exp = c.ExpiresAt.Format("2006-01-02")
		}
		add("expires_at", "过期日（UTC 零点失效）", exp, "date", false)
		v := ""
		if c.Active {
			v = "1"
		}
		add("active", "启用", v, "checkbox", false)
	case "tokens":
		raw, _, err := s.db.AdminList(r.Context(), "tokens", store.AdminFilter{RecordID: id})
		if err != nil {
			s.renderError(w, r, 500, "读取失败")
			return
		}
		found := false
		for _, c := range raw.([]store.APIToken) {
			if c.ID == id && c.RevokedAt == nil {
				found = true
				add("name", "名称", c.Name, "text", true)
				add("scopes", "权限（逗号分隔）", strings.Join(c.Scopes, ","), "text", true)
				add("allowed_ips", "来源 IP / CIDR（逗号分隔）", strings.Join(c.AllowedIPs, ","), "text", false)
				exp := ""
				if c.ExpiresAt != nil {
					exp = c.ExpiresAt.UTC().Format("2006-01-02T15:04")
				}
				add("expires_at", "有效期（UTC），留空长期有效", exp, "datetime-local", false)
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		title, action = "编辑 API 令牌", "/admin/lists/tokens/"+num(id)
	case "cards":
		product := parseInt64(r.FormValue("product_id"))
		c, err := s.db.CardMetadata(r.Context(), id)
		if err != nil || c.ProductID != product || c.Status == "sold" || c.OrderID.Valid {
			http.NotFound(w, r)
			return
		}
		back = "/admin/products/" + num(product) + "/cards"
		title, action = "编辑未售出卡密", "/admin/lists/cards/"+num(id)
		add("product_id", "商品", num(product), "hidden", true)
		add("label", "标签", c.Label, "text", false)
		add("secret", "替换卡密（留空保留原值）", "", "password", false)
	default:
		http.NotFound(w, r)
		return
	}
	ret := r.URL.Query().Get("return_to")
	if r.Method == "POST" {
		ret = r.PostFormValue("list_return")
		for i := range fields {
			if fields[i].Type != "hidden" && fields[i].Type != "password" {
				fields[i].Value = r.PostFormValue(fields[i].Name)
			}
		}
	}
	back = listReturn(ret, back)
	s.renderStatus(w, r, status, "admin_list_edit", map[string]any{"Title": title, "Error": message, "Fields": fields, "ID": id, "FormAction": action, "Back": back, "ListReturn": back})
}
func (s *Server) handleListSave(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	kind := r.PathValue("kind")
	var err error
	back := "/admin/" + kind
	switch kind {
	case "tokens":
		in := store.TokenInput{Name: strings.TrimSpace(r.FormValue("name")), Scopes: splitEditList(r.FormValue("scopes")), AllowedIPs: splitEditList(r.FormValue("allowed_ips"))}
		if raw := r.FormValue("expires_at"); raw != "" {
			t, e := time.Parse("2006-01-02T15:04", raw)
			if e != nil {
				s.renderListEdit(w, r, 422, "有效期格式不正确。")
				return
			}
			in.ExpiresAt = &t
		}
		err = s.db.UpdateToken(r.Context(), id, in)
	case "cards":
		product := parseInt64(r.FormValue("product_id"))
		back = "/admin/products/" + strconv.FormatInt(product, 10) + "/cards"
		err = s.db.EditCard(r.Context(), id, product, strings.TrimSpace(r.FormValue("label")), strings.TrimSpace(r.FormValue("secret")))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderListEdit(w, r, 422, "保存失败，请检查输入；已售出的卡密和已撤销的令牌不能修改。")
		return
	}
	s.audit(r, kind+".update", strconv.FormatInt(id, 10), "")
	flashRedirect(w, r, back, "修改已保存。", "")
}
func splitEditList(v string) []string {
	return strings.FieldsFunc(v, func(c rune) bool { return c == ',' || c == ' ' || c == '\n' })
}

func (s *Server) editOrRedirect(w http.ResponseWriter, r *http.Request, kind, message string) {
	if id := parseInt64(r.FormValue("id")); id > 0 {
		r.SetPathValue("id", strconv.FormatInt(id, 10))
		r.SetPathValue("kind", kind)
		s.renderListEdit(w, r, 422, message)
		return
	}
	flashRedirect(w, r, "/admin/"+kind, "", message)
}

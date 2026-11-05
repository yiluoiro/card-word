package httpapi

import (
	"github.com/yiluoiro/card-word/internal/store"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type listChoice struct{ Value, Label string }
type listSpec struct {
	Hint, States, Kinds string
	Dates, Currency     bool
}

var adminSpecs = map[string]listSpec{
	"customers":  {"买家用户名", "ordered:已下单 new:未下单", "", true, false},
	"categories": {"名称或别名", "visible:前台可见 hidden:隐藏", "", true, false},
	"coupons":    {"优惠码", "enabled:启用 disabled:停用 expired:已过期", "percent:百分比 fixed:固定金额", true, false},
	"users":      {"用户名", "enabled:正常 disabled:停用", "owner:所有者 admin:管理员 viewer:只读", true, false},
	"tokens":     {"名称或前缀", "enabled:有效 revoked:已撤销 expired:已过期", "", true, false},
	"plugins":    {"插件名称或标识", "enabled:启用 disabled:停用", "payment:支付 notifier:通知 theme:主题 extension:扩展", true, false},
	"market":     {"名称、作者或插件标识", "", "payment:支付 notifier:通知 theme:主题 extension:扩展", false, false},
	"orders":     {"订单号、商品或邮箱", "", "", true, true},
}

func init() {
	adminSpecs["orders"] = listSpec{"订单号、商品或邮箱", "pending:待支付 paid:待补发 delivered:已发货 closed:已关闭 refunded:已退款", "", true, true}
	adminSpecs["cards"] = listSpec{"编号、标签或掩码", "available:可售 sold:已售 disabled:停用", "", true, false}
	adminSpecs["audit"] = listSpec{"操作者、动作、对象或来源IP", "", "", true, false}

}

type adminListView struct {
	Prefix, Hint, Path, Anchor, Reset, ExportURL string
	Audit                                        bool
	Variants                                     []store.ProductVariant
	Dates, Currency                              bool
	States, Kinds                                []listChoice
	Other                                        url.Values
	Filter                                       store.AdminFilter
	Pagination                                   listPagination
}

func choices(raw string) []listChoice {
	var out []listChoice
	for _, p := range strings.Fields(raw) {
		v, l, _ := strings.Cut(p, ":")
		out = append(out, listChoice{v, l})
	}
	return out
}
func choice(v string, options []listChoice) string {
	for _, o := range options {
		if o.Value == v {
			return v
		}
	}
	return ""
}
func listView(r *http.Request, kind, prefix string) adminListView {
	spec := adminSpecs[kind]
	v := adminListView{Hint: spec.Hint, Dates: spec.Dates, Currency: spec.Currency, States: choices(spec.States), Kinds: choices(spec.Kinds), Prefix: prefix, Anchor: strings.TrimSuffix(prefix, "_"), Path: r.URL.Path, Other: url.Values{}}
	v.Audit = kind == "audit"
	q := r.URL.Query()
	get := func(k string) string { return strings.TrimSpace(q.Get(prefix + k)) }
	v.Filter = store.AdminFilter{Search: get("q"), Status: choice(get("status"), v.States), Kind: choice(get("kind"), v.Kinds)}
	if spec.Dates {
		v.Filter.From = get("from")
		v.Filter.To = get("to")
	}
	if spec.Currency {
		v.Filter.Currency = strings.ToUpper(get("currency"))
	}
	if v.Audit {
		v.Filter.Status = get("action")
	}
	if len(v.Filter.Status) > 100 {
		v.Filter.Status = ""
	}
	if len(v.Filter.Currency) > 8 {
		v.Filter.Currency = ""
	}
	if kind == "orders" && prefix == "" {
		if id := parseInt64(get("customer_id")); id > 0 {
			v.Filter.CustomerID = id
			v.Other.Set("customer_id", strconv.FormatInt(id, 10))
		}
	}
	v.Filter.ProductID = parseInt64(r.PathValue("id"))
	if kind == "cards" {
		v.Filter.VariantID = parseInt64(get("variant_id"))
	}
	if len([]rune(v.Filter.Search)) > 200 {
		v.Filter.Search = string([]rune(v.Filter.Search)[:200])
	}
	for _, date := range []*string{&v.Filter.From, &v.Filter.To} {
		if _, err := time.Parse("2006-01-02", *date); err != nil {
			*date = ""
		}
	}
	v.Filter.Page, _ = strconv.Atoi(get("page"))
	v.Filter.Size, _ = strconv.Atoi(get("size"))
	if v.Filter.Size != 20 && v.Filter.Size != 100 {
		v.Filter.Size = 50
	}
	for other := range adminSpecs {
		if prefix == "" || other+"_" == prefix {
			continue
		}
		for _, key := range []string{"q", "status", "kind", "from", "to", "currency", "size", "page"} {
			k := other + "_" + key
			if val := q.Get(k); val != "" && len(val) <= 200 {
				v.Other.Set(k, val)
			}
		}
	}
	v.Reset = (&url.URL{Path: v.Path, RawQuery: v.Other.Encode(), Fragment: v.Anchor}).String()
	return v
}
func (v *adminListView) finish(total int) {
	q := url.Values{}
	for k, vs := range v.Other {
		q[k] = vs
	}
	f := v.Filter
	if f.VariantID > 0 {
		q.Set("variant_id", strconv.FormatInt(f.VariantID, 10))
	}
	for k, val := range map[string]string{"q": f.Search, "status": f.Status, "kind": f.Kind, "from": f.From, "to": f.To, "currency": f.Currency} {
		if val != "" {
			q.Set(v.Prefix+k, val)
		}
	}
	if f.Size != 50 {
		q.Set(v.Prefix+"size", strconv.Itoa(f.Size))
	}
	if v.Audit {
		q.Del(v.Prefix + "status")
		if f.Status != "" {
			q.Set(v.Prefix+"action", f.Status)
		}
	}
	v.ExportURL = (&url.URL{Path: v.Path + "/export.csv", RawQuery: q.Encode()}).String()
	v.Pagination = paginateKey(strconv.Itoa(f.Page), total, f.Size, v.Path, v.Anchor, q, v.Prefix+"page")
}
func (s *Server) adminList(w http.ResponseWriter, r *http.Request, db *store.DB, kind, prefix string) (any, adminListView, bool) {
	v := listView(r, kind, prefix)
	if kind == "orders" && prefix == "orders_" {
		v.States = nil
		v.Filter.Status = store.OrderPending
	}
	rows, total, err := db.AdminList(r.Context(), kind, v.Filter)
	if err != nil {
		s.log.Error("admin list failed", "list", kind, "error", err)
		s.renderError(w, r, 500, "加载列表失败，请重试。")
		return nil, v, false
	}
	v.finish(total)
	return rows, v, true
}

// Preserve only list filters, never arbitrary destinations or credentials.
func listReturn(raw, path string) string {
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.Path != path {
		return path
	}
	q := url.Values{}
	allowed := map[string]bool{"q": true, "status": true, "kind": true, "from": true, "to": true, "currency": true, "page": true, "size": true, "action": true, "category_id": true, "stock": true, "customer_id": true, "variant_id": true}
	for k, vs := range u.Query() {
		key := k
		for prefix := range adminSpecs {
			key = strings.TrimPrefix(key, prefix+"_")
		}
		if allowed[key] && len(vs) > 0 && len(vs[0]) <= 200 {
			q.Set(k, vs[0])
		}
	}
	return (&url.URL{Path: path, RawQuery: q.Encode()}).String()
}

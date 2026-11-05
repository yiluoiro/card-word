package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/yiluoiro/card-word/internal/store"
	"golang.org/x/net/html"
)

func TestPaginationBoundsAndFilterURLs(t *testing.T) {
	for _, tc := range []struct {
		raw                            string
		total, page, pages, start, end int
	}{
		{"", 0, 1, 1, 0, 0}, {"-9", 205, 1, 5, 1, 50}, {"nonsense", 205, 1, 5, 1, 50},
		{strings.Repeat("9", 100), 205, 1, 5, 1, 50}, {"99999", 205, 5, 5, 201, 205},
		{"4", 200, 4, 4, 151, 200}, {"2", 205, 2, 5, 51, 100},
	} {
		p := paginate(tc.raw, tc.total, 50, "/", "products", url.Values{"q": {"中文 & + #"}, "cat": {"tools"}})
		if p.Page != tc.page || p.Pages != tc.pages || p.Start != tc.start || p.End != tc.end {
			t.Fatalf("%+v: %+v", tc, p)
		}
		if p.Offset < 0 {
			t.Fatal("negative offset")
		}
		for _, link := range []string{p.FirstURL, p.PreviousURL, p.NextURL, p.LastURL} {
			if link == "" {
				continue
			}
			u, err := url.Parse(link)
			if err != nil {
				t.Fatal(err)
			}
			if u.Path != "/" || u.Fragment != "products" || u.Query().Get("q") != "中文 & + #" || u.Query().Get("cat") != "tools" {
				t.Fatalf("lost filter: %s", link)
			}
		}
	}
}

func paginationNext(body string) string {
	tokens := html.NewTokenizer(strings.NewReader(body))
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken:
			t := tokens.Token()
			if t.Data != "a" {
				continue
			}
			var rel, href string
			for _, a := range t.Attr {
				if a.Key == "rel" {
					rel = a.Val
				}
				if a.Key == "href" {
					href = a.Val
				}
			}
			if rel == "next" {
				return href
			}
		}
	}
}

func TestCatalogPaginationBeyondTwoHundred(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	u := securityUser(t, s, store.RoleViewer)
	cookie := securitySession(t, s, u)
	category, err := s.db.CreateCategory(ctx, "分页分类", "paging", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.db.CreateCategory(ctx, "其他分类", "other", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	const keyword = "分页 & 专区"
	for i := 0; i < 214; i++ {
		catID, status, name := category.ID, store.ProductActive, fmt.Sprintf("%s %03d", keyword, i)
		if i >= 205 && i < 210 {
			status = store.ProductDraft
		}
		if i >= 210 && i < 213 {
			catID = other.ID
		}
		if i == 213 {
			name = "不匹配的名称"
		}
		_, err := s.db.CreateProduct(ctx, store.ProductInput{Name: name, Slug: fmt.Sprintf("paging-%03d", i), CategoryID: catID, Status: status, PriceCents: 100, Sort: i % 4})
		if err != nil {
			t.Fatal(err)
		}
	}
	f := store.ProductFilter{CategoryID: category.ID, Status: store.ProductActive, Search: keyword, Limit: 500}
	want, err := s.db.ListProducts(ctx, f)
	if err != nil || len(want) != 205 {
		t.Fatalf("list: %d %v", len(want), err)
	}
	f.Limit = 1
	f.Offset = 200
	if total, err := s.db.CountProducts(ctx, f); err != nil || total != 205 {
		t.Fatalf("count: %d %v", total, err)
	}
	get := func(path string, admin bool) string {
		t.Helper()
		// Browsers keep fragments locally; they are never part of HTTP requests.
		requestURL, err := url.Parse(path)
		if err != nil {
			t.Fatal(err)
		}
		requestURL.Fragment = ""
		path = requestURL.String()
		r := httptest.NewRequest("GET", path, nil)
		if admin {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	for _, admin := range []bool{false, true} {
		path, size, categoryKey := "/", 24, "cat"
		filters := url.Values{"q": {keyword}, "cat": {category.Slug}}
		pattern := regexp.MustCompile(`href="/p/(paging-[0-9]+)"`)
		if admin {
			path, size, categoryKey = "/admin/products", 50, "category_id"
			filters = url.Values{"q": {keyword}, "category_id": {strconv.FormatInt(category.ID, 10)}, "status": {store.ProductActive}}
			pattern = regexp.MustCompile(`data-product-id="([0-9]+)"`)
		}
		seen := 0
		page := 1
		link := path + "?" + filters.Encode()
		for link != "" {
			body := get(link, admin)
			matches := pattern.FindAllStringSubmatch(body, -1)
			count := min(size, 205-seen)
			if len(matches) != count {
				t.Fatalf("admin=%v page=%d got=%d want=%d", admin, page, len(matches), count)
			}
			if !strings.Contains(adminVisibleText(body), "共 205 条") {
				t.Fatal("count shows page size instead of total")
			}
			for _, match := range matches {
				expected := want[seen].Slug
				if admin {
					expected = strconv.FormatInt(want[seen].ID, 10)
				}
				if match[1] != expected {
					t.Fatalf("unstable ordering at %d: %s want %s", seen, match[1], expected)
				}
				seen++
			}
			link = paginationNext(body)
			if link != "" {
				parsed, _ := url.Parse(link)
				if parsed.Query().Get("q") != keyword || parsed.Query().Get(categoryKey) != filters.Get(categoryKey) {
					t.Fatal("filter lost during navigation")
				}
			}
			page++
			if page > 20 {
				t.Fatal("pagination loop")
			}
		}
		if seen != 205 {
			t.Fatalf("only visited %d products", seen)
		}
		filters.Set("page", "999999")
		last := get(path+"?"+filters.Encode(), admin)
		if paginationNext(last) != "" || len(pattern.FindAllStringSubmatch(last, -1)) != 205%size {
			t.Fatal("out-of-range page not clamped")
		}
		filters.Set("q", "no-results")
		filters.Set("page", "3")
		empty := get(path+"?"+filters.Encode(), admin)
		if !strings.Contains(adminVisibleText(empty), "共 0 条") || paginationNext(empty) != "" || len(pattern.FindAllStringSubmatch(empty, -1)) != 0 {
			t.Fatal("invalid empty result pagination")
		}
	}
	if body := get("/admin/products?status=draft&category_id="+strconv.FormatInt(category.ID, 10), true); !strings.Contains(adminVisibleText(body), "共 5 条") {
		t.Fatal("draft status filter ignored")
	}
}

func TestCardPaginationPreservesStatusAndProduct(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	u := securityUser(t, s, store.RoleViewer)
	cookie := securitySession(t, s, u)
	p, err := s.db.CreateProduct(ctx, store.ProductInput{Name: "库存测试", Slug: "inventory", Status: store.ProductActive, MaxQty: 3})
	if err != nil {
		t.Fatal(err)
	}
	secrets := make([]string, 210)
	for i := range secrets {
		secrets[i] = fmt.Sprintf("PAGINATION-SECRET-%04d-PRIVATE", i)
	}
	if _, err := s.db.AddCards(ctx, p.ID, secrets, "paging"); err != nil {
		t.Fatal(err)
	}
	all, err := s.db.ListCards(ctx, store.CardFilter{ProductID: p.ID, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	order, err := s.db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 3, PaymentDriver: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.FulfillOrder(ctx, order.Order.OrderNo, "manual", "pagination-test-payment", order.Order.TotalCents, ""); err != nil {
		t.Fatal(err)
	}
	all, err = s.db.ListCards(ctx, store.CardFilter{ProductID: p.ID, Status: store.CardAvailable, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.db.SetCardStatus(ctx, all[i].ID, store.CardDisabled); err != nil {
			t.Fatal(err)
		}
	}
	other, err := s.db.CreateProduct(ctx, store.ProductInput{Name: "其他库存", Slug: "other-inventory"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.AddCards(ctx, other.ID, []string{"OTHER-SECRET"}, ""); err != nil {
		t.Fatal(err)
	}
	filter := store.CardFilter{ProductID: p.ID, Status: store.CardAvailable, Limit: 1, Offset: 200}
	if total, err := s.db.CountCards(ctx, filter); err != nil || total != 205 {
		t.Fatalf("count %d %v", total, err)
	}
	filter.Limit = 500
	filter.Offset = 0
	want, err := s.db.ListCards(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/admin/products/%d/cards", p.ID)
	pattern := regexp.MustCompile(`data-card-id="([0-9]+)"`)
	get := func(link string) string {
		t.Helper()
		r := httptest.NewRequest("GET", link, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d", link, w.Code)
		}
		return w.Body.String()
	}
	link := path + "?status=available"
	seen := 0
	for link != "" {
		body := get(link)
		matches := pattern.FindAllStringSubmatch(body, -1)
		if len(matches) != min(50, 205-seen) || !strings.Contains(adminVisibleText(body), "共 205 条") {
			t.Fatal("invalid inventory page size/count")
		}
		for _, match := range matches {
			if match[1] != strconv.FormatInt(want[seen].ID, 10) || want[seen].Secret != "" {
				t.Fatal("unstable order or revealed secret")
			}
			seen++
		}
		for _, secret := range secrets {
			if strings.Contains(body, secret) {
				t.Fatal("plaintext card exposed")
			}
		}
		link = paginationNext(body)
		if link != "" {
			parsed, _ := url.Parse(link)
			if parsed.Path != path || parsed.Query().Get("status") != "available" {
				t.Fatal("inventory filter lost")
			}
		}
		if seen > 205 {
			t.Fatal("pagination repeated")
		}
	}
	if seen != 205 {
		t.Fatalf("only visited %d cards", seen)
	}
	if body := get(path + "?status=sold&page=99"); !strings.Contains(adminVisibleText(body), "共 3 条") || len(pattern.FindAllStringSubmatch(body, -1)) != 3 {
		t.Fatal("sold filter or page clamp incorrect")
	}
	if body := get(path + "?status=disabled"); !strings.Contains(adminVisibleText(body), "共 2 条") {
		t.Fatal("disabled filter ignored")
	}
	r := httptest.NewRequest("GET", path+"?page=5", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("pagination bypassed authentication")
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/store"
	"golang.org/x/net/html"
)

func TestMarkdownFormattingAndSafeExcerpt(t *testing.T) {
	source := "## 使用说明\n\n**会员** 与 *权益* ~~旧版~~\n\n- 第一步\n- 第二步\n\n> 提示\n\n```go\nfmt.Println(1)\n```\n\n|功能|说明|\n|---|---|\n|下载|不限次数|\n\n[指南](https://example.com)\n\n![封面](/media/cover.png)"
	got := string(renderMarkdown(source))
	for _, want := range []string{"<h2>使用说明</h2>", "<strong>会员</strong>", "<em>权益</em>", "<del>旧版</del>", "<ul>", "<blockquote>", "<pre><code>", "<table>", `href="https://example.com"`, `src="/media/cover.png"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if got := markdownExcerpt("**会员** 与 [指南](https://example.com) &amp; 权益"); got != "会员 与 指南 & 权益" {
		t.Fatalf("excerpt: %q", got)
	}
	if got := markdownExcerpt("first\nsecond\n\n![封面](/media/cover.png)"); got != "first second 封面" {
		t.Fatalf("excerpt boundaries: %q", got)
	}
	if got := markdownExcerpt(strings.Repeat("汉", 141)); len([]rune(got)) != 141 || !strings.HasSuffix(got, "…") {
		t.Fatal("excerpt must truncate on rune boundaries")
	}
	if !strings.Contains(string(renderMarkdown("第一行\n第二行")), "<br>") {
		t.Fatal("plain text line breaks lost")
	}
}

func TestMarkdownRejectsExecutableMarkup(t *testing.T) {
	for _, source := range []string{
		`<script>alert(1)</script><img src=x onerror=alert(1)>`,
		`[click](javascript:alert%281%29)`, `[click](jav&#x61;script:alert(1))`,
		`![x](data:image/svg+xml;base64,PHN2Zy8+)`, `![x](https://example.com/tracker.png)`,
		`<iframe srcdoc="<script>alert(1)</script>"></iframe>`,
		`<svg onload=alert(1)><a href="javascript:alert(1)">x</a></svg>`,
		`[x](vbscript:msgbox(1))`, `</textarea><script>alert(1)</script>`,
	} {
		doc, err := html.Parse(strings.NewReader(string(renderMarkdown(source))))
		if err != nil {
			t.Fatal(err)
		}
		var walk func(*html.Node)
		walk = func(n *html.Node) {
			if n.Type == html.ElementNode {
				switch n.Data {
				case "script", "style", "iframe", "svg", "object", "input", "form":
					t.Fatalf("unsafe element %s for %q", n.Data, source)
				}
				for _, a := range n.Attr {
					if strings.HasPrefix(a.Key, "on") || a.Key == "style" || a.Key == "srcdoc" {
						t.Fatalf("unsafe attribute %v", a)
					}
					if a.Key == "href" && strings.Contains(strings.ToLower(a.Val), "script:") {
						t.Fatalf("unsafe URL %q", a.Val)
					}
					if a.Key == "src" && !strings.HasPrefix(a.Val, "/media/") {
						t.Fatalf("unexpected image URL %q", a.Val)
					}
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(doc)
	}
}

func TestMarkdownPreviewAuthenticationAndLimits(t *testing.T) {
	s := securityServer(t)
	for _, role := range []string{store.RoleViewer, store.RoleAdmin} {
		u := securityUser(t, s, role)
		cookie := securitySession(t, s, u)
		sess, err := s.db.SessionByTokenHash(context.Background(), cryptoutil.TokenHash(cookie.Value))
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			source, csrf string
			want         int
		}{
			{"**预览**", sess.CSRFToken, http.StatusOK},
			{"**预览**", "wrong-token", http.StatusForbidden},
			{strings.Repeat("a", maxMarkdownBytes+1), sess.CSRFToken, http.StatusRequestEntityTooLarge},
		} {
			r := securityRequest(nil, "POST", "/admin/markdown/preview", url.Values{"source": {tc.source}})
			r.AddCookie(cookie)
			r.Header.Set("X-CSRF-Token", tc.csrf)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			want := tc.want
			if role == store.RoleViewer {
				want = http.StatusForbidden
			}
			if w.Code != want {
				t.Fatalf("%s: status %d want %d: %s", role, w.Code, want, w.Body.String())
			}
			if want == http.StatusOK {
				var data struct {
					HTML string `json:"html"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
					t.Fatal(err)
				}
				if data.HTML != string(renderMarkdown(tc.source)) || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("preview differs from storefront")
				}
			}
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, securityRequest(nil, "POST", "/admin/markdown/preview", url.Values{"source": {"secret"}}))
	if w.Code == http.StatusOK {
		t.Fatal("anonymous preview allowed")
	}
}

func TestProductMarkdownSaveRenderAndFailureRecovery(t *testing.T) {
	s := securityServer(t)
	u := securityUser(t, s, store.RoleAdmin)
	form := url.Values{"name": {"Markdown 商品"}, "slug": {"markdown-product"}, "price": {"12.30"}, "min_qty": {"1"}, "max_qty": {"5"}, "summary": {"**年度会员** 与 [指南](https://example.com)"}, "description": {"## 使用说明\n\n- 开通权益\n- 保存凭证"}}
	save := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.handleAdminProductSave(w, securityRequest(u, "POST", "/admin/products", form))
		return w
	}
	if w := save(); w.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	p, err := s.db.ProductBySlug(context.Background(), "markdown-product")
	if err != nil {
		t.Fatal(err)
	}
	if p.Summary != form.Get("summary") || p.Description != form.Get("description") {
		t.Fatal("markdown source not preserved")
	}
	for _, path := range []string{"/p/markdown-product", "/"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		if path == "/" {
			if strings.Contains(w.Body.String(), "**年度会员**") || !strings.Contains(w.Body.String(), "年度会员 与 指南") {
				t.Fatal("home excerpt not plain")
			}
		} else if !strings.Contains(w.Body.String(), "<strong>年度会员</strong>") || !strings.Contains(w.Body.String(), "<h2>使用说明</h2>") {
			t.Fatal("detail missing rendered Markdown")
		}
	}
	form.Set("id", strconv.FormatInt(p.ID, 10))
	form.Set("price", "12.oops")
	form.Set("summary", "**未保存的文案**\n下一行")
	if w := save(); w.Code != 422 || !strings.Contains(w.Body.String(), "未保存的文案") || !strings.Contains(w.Body.String(), `value="12.oops"`) {
		t.Fatalf("lost failed form: %d %s", w.Code, w.Body.String())
	}
	saved, _ := s.db.ProductByID(context.Background(), p.ID)
	if saved.Summary != p.Summary || saved.PriceCents != p.PriceCents {
		t.Fatal("invalid save mutated product")
	}
	form.Set("price", "13.50")
	if w := save(); w.Code != 303 {
		t.Fatalf("retry save: %d", w.Code)
	}
	form.Del("id")
	form.Set("name", "重复别名时保留内容")
	if w := save(); w.Code != 422 || !strings.Contains(w.Body.String(), "重复别名时保留内容") || !strings.Contains(w.Body.String(), "新建商品") {
		t.Fatalf("duplicate slug: %d", w.Code)
	}
	form.Set("slug", "oversized")
	form.Set("description", strings.Repeat("x", maxMarkdownBytes+1))
	if w := save(); w.Code != 422 {
		t.Fatalf("oversized Markdown accepted: %d", w.Code)
	}
}

func TestParsePriceCentsStrict(t *testing.T) {
	for input, want := range map[string]int64{"": 0, "0": 0, " 12.3 ": 1230, "001.01": 101, "92233720368547758.07": math.MaxInt64, "92233720368547758.08": -1, "999999999999999999999": -1, "oops": -1, "1.2.3": -1, "1.234": -1, "1.x": -1, ".50": -1, "1.": -1, "-1": -1, "+1": -1} {
		if got := parsePriceCents(input); got != want {
			t.Errorf("%q: got %d want %d", input, got, want)
		}
	}
}

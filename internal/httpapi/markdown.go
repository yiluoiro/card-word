package httpapi

import (
	"bytes"
	"html/template"
	"net/http"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"golang.org/x/net/html"
)

const maxMarkdownBytes = 100 * 1024

var productMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.Linkify),
	goldmark.WithRendererOptions(gmhtml.WithHardWraps()),
)

// Build once, then only read: policies and the renderer are shared by requests.
var markdownPolicy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "strong", "em", "del", "blockquote", "ul", "ol", "li",
		"h1", "h2", "h3", "h4", "h5", "h6", "pre", "code", "hr",
		"table", "thead", "tbody", "tr", "th", "td")
	p.AllowStandardURLs()
	p.RequireNoFollowOnLinks(true)
	p.AllowAttrs("href", "title").OnElements("a")
	// Match the existing CSP: only uploaded, same-site media can be embedded.
	p.AllowAttrs("src").Matching(regexp.MustCompile(`^/media/[a-zA-Z0-9_.-]+$`)).OnElements("img")
	p.AllowAttrs("alt", "title").OnElements("img")
	p.AllowAttrs("align").Matching(regexp.MustCompile(`^(left|center|right)$`)).OnElements("th", "td")
	p.AllowAttrs("start").Matching(regexp.MustCompile(`^[0-9]+$`)).OnElements("ol")
	return p
}()

// Only sanitized output crosses the html/template trust boundary. Raw HTML is
// disabled in goldmark and then an explicit element/attribute allowlist is applied.
func renderMarkdown(source string) template.HTML {
	var out bytes.Buffer
	if err := productMarkdown.Convert([]byte(source), &out); err != nil {
		return template.HTML(template.HTMLEscapeString(source))
	}
	return template.HTML(markdownPolicy.SanitizeBytes(out.Bytes()))
}

// Product cards are links themselves, so use a plain excerpt without nested links.
func markdownExcerpt(source string) string {
	var plain strings.Builder
	tokens := html.NewTokenizer(strings.NewReader(string(renderMarkdown(source))))
extract:
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			break extract
		case html.TextToken:
			plain.Write(tokens.Text())
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			token := tokens.Token()
			switch token.Data {
			case "p", "br", "li", "blockquote", "pre", "h1", "h2", "h3", "h4", "h5", "h6", "th", "td":
				plain.WriteByte(' ')
			case "img":
				for _, attr := range token.Attr {
					if attr.Key == "alt" {
						plain.WriteString(attr.Val)
					}
				}
			}
		}
	}
	text := []rune(strings.Join(strings.Fields(plain.String()), " "))
	if len(text) > 140 {
		return string(text[:140]) + "…"
	}
	return string(text)
}

func (s *Server) handleMarkdownPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 4*maxMarkdownBytes)
	if err := r.ParseForm(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_form", "预览内容过大或格式不正确。")
		return
	}
	source := r.FormValue("source")
	if len(source) > maxMarkdownBytes {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "too_large", "每段 Markdown 内容不能超过 100 KB。")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": string(renderMarkdown(source))})
}

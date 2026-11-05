package httpapi

import (
	"net/url"
	"strconv"
)

type listPagination struct {
	Page, Pages, Total, Start, End, Offset  int
	Path, Anchor, PageField                 string
	Filters                                 url.Values
	FirstURL, PreviousURL, NextURL, LastURL string
}

// Clamp before multiplying so untrusted page numbers cannot overflow offsets.
// Only normalized filter values supplied by the handler are carried forward.
func paginate(raw string, total, size int, path, anchor string, filters url.Values) listPagination {
	return paginateKey(raw, total, size, path, anchor, filters, "page")
}
func paginateKey(raw string, total, size int, path, anchor string, filters url.Values, pageField string) listPagination {
	pages := 1
	if total > 0 {
		pages = (total-1)/size + 1
	}
	page, err := strconv.Atoi(raw)
	if err != nil || page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	p := listPagination{PageField: pageField, Page: page, Pages: pages, Total: total, Offset: (page - 1) * size, Path: path, Anchor: anchor, Filters: filters}
	if total > 0 {
		p.Start = p.Offset + 1
		p.End = p.Offset + min(size, total-p.Offset)
	}
	link := func(n int) string {
		query := make(url.Values)
		for key, values := range filters {
			query[key] = append([]string(nil), values...)
		}
		query.Set(pageField, strconv.Itoa(n))
		return (&url.URL{Path: path, RawQuery: query.Encode(), Fragment: anchor}).String()
	}
	if page > 1 {
		p.FirstURL = link(1)
		p.PreviousURL = link(page - 1)
	}
	if page < pages {
		p.NextURL = link(page + 1)
		p.LastURL = link(pages)
	}
	return p
}

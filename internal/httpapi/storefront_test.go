package httpapi_test

import (
	"strings"
	"testing"
)

func TestReferenceStorefrontAssetsAndDiscovery(t *testing.T) {
	h := newHarness(t)
	h.seedProduct(t)
	_, body := h.get("/")
	for _, marker := range []string{"reference-hero", "/static/reference.css", "/static/storefront.js", `id="products"`, `id="faq"`, `id="contact"`, `data-language="zh"`} {
		if !strings.Contains(body, marker) {
			t.Fatalf("missing storefront feature %q", marker)
		}
	}
	if strings.Count(body, `class="brand-card"`) != 6 {
		t.Fatal("expected six brand discovery links")
	}
	if strings.Contains(body, `href="/merchant"`) {
		t.Fatal("platform link in single store")
	}
	if response, _ := h.get("/merchant"); response.StatusCode != 404 {
		t.Fatal("platform route is available")
	}
	for _, query := range []string{"OpenAI", "Google%20Play", "Steam", "PlayStation", "Amazon", "iTunes"} {
		if !strings.Contains(body, `href="/?q=`+query+`#products"`) {
			t.Fatalf("brand link does not search real inventory: %s", query)
		}
	}
	for _, asset := range []string{"reference.css", "storefront.js", "reference/brand.svg", "reference/hero-cards.webp", "reference/openai.webp", "reference/google-play.webp", "reference/steam.webp", "reference/playstation.webp", "reference/amazon.webp", "reference/itunes.webp"} {
		response, content := h.get("/static/" + asset)
		if response.StatusCode != 200 || len(content) == 0 {
			t.Fatalf("missing asset %s", asset)
		}
		if strings.HasSuffix(asset, ".webp") && (!strings.HasPrefix(content, "RIFF") || !strings.Contains(response.Header.Get("Content-Type"), "image/webp")) {
			t.Fatalf("invalid image %s", asset)
		}
	}
	_, filtered := h.get("/?q=OpenAI")
	if !strings.Contains(filtered, "No matching cards yet") {
		t.Fatal("brand discovery must not invent inventory")
	}
}

package httpapi

import (
	"html/template"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderFailureDoesNotCommitPartialHTML(t *testing.T) {
	s := &Server{tmpl: template.Must(template.New("broken").Parse(`partial-secret{{.Missing.Name}}`)), log: slog.New(slog.NewTextHandler(io.Discard, nil)), settings: map[string]string{}}
	w := httptest.NewRecorder()
	s.render(w, httptest.NewRequest("GET", "/", nil), "broken", map[string]any{"Missing": 123})
	if w.Code != 500 || strings.Contains(w.Body.String(), "partial-secret") {
		t.Fatalf("partial response committed: %d %s", w.Code, w.Body.String())
	}
}

func TestParseInt64RejectsOverflow(t *testing.T) {
	for _, v := range []string{"4611686018427387904", "18446744073709551617", "-1", "1.5", "garbage"} {
		if got := parseInt64(v); got != 0 {
			t.Errorf("%s became %d", v, got)
		}
	}
	if parseInt64(" 123 ") != 123 {
		t.Fatal("valid value rejected")
	}
}

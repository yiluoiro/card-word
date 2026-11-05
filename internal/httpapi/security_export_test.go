package httpapi

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yiluoiro/card-word/internal/store"
)

func TestCSVFormulaMarkersAndPlainText(t *testing.T) {
	for _, value := range []string{"=1", "+1", "-1", "@SUM(1)", "\t=1", "\r=1", "\n=1", "  =1", "\ufeff=1", "＝1", "＋1", "－1", "＠SUM(1)"} {
		if got := csvTextRow([]string{value})[0]; got != "'"+value {
			t.Errorf("unescaped %q: %q", value, got)
		}
	}
	for _, value := range []string{"", "card-123", "0123", "a,b", "'already-text", "普通文本"} {
		if got := csvTextRow([]string{value})[0]; got != value {
			t.Errorf("changed plain text %q: %q", value, got)
		}
	}
}

func TestCSVExportsTreatUntrustedCellsAsText(t *testing.T) {
	s := securityServer(t)
	ctx := context.Background()
	p := securityProduct(t, s)
	if _, err := s.db.AddCards(ctx, p.ID, []string{"=1+1"}, "@SUM(1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.CreateOrder(ctx, store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: "manual", Contact: "  =1+1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Audit(ctx, "=1+1", "test", "@SUM(1)", "127.0.0.1", "\t=1+1"); err != nil {
		t.Fatal(err)
	}
	u := securityUser(t, s, store.RoleAdmin)
	cookie := securitySession(t, s, u)
	for _, path := range []string{"/admin/orders/export.csv", "/admin/audit/export.csv", fmt.Sprintf("/admin/products/%d/cards/export.csv", p.ID)} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("export status %d: %s", w.Code, w.Body.String())
		}
		rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\ufeff"))).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows[1:] {
			for _, cell := range row {
				if strings.Contains(cell, "=1+1") {
					found = true
					if !strings.HasPrefix(cell, "'") {
						t.Errorf("unescaped formula in %s: %q", path, cell)
					}
				}
			}
		}
		if !found {
			t.Fatalf("test fixture missing in %s", path)
		}
	}
	cards, err := s.db.ListCards(ctx, store.CardFilter{ProductID: p.ID, Status: "available", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range cards {
		secret, err := s.db.RevealCard(ctx, card.ID)
		if err != nil {
			t.Fatal(err)
		}
		if secret == "=1+1" {
			return
		}
	}
	t.Fatal("export changed the stored card secret")
}

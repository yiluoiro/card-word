package httpapi_test

import (
	"context"
	"fmt"
	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/store"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yiluoiro/card-word/internal/httpapi"
)

func TestCaptchaRefreshRotatesCookieThroughMiddleware(t *testing.T) {
	h := newHarness(t)
	resp, body := h.get("/admin/login")
	if resp.StatusCode != 200 || !strings.Contains(body, `data:image/png;base64,`) || strings.Contains(body, "ZgotmplZ") {
		t.Fatal("login must render the generated raster image")
	}
	base, _ := url.Parse(h.server.URL)
	token := ""
	for _, cookie := range h.client.Jar.Cookies(base) {
		if cookie.Name == "cw_cap" {
			token = cookie.Value
		}
	}
	if token == "" {
		t.Fatal("no challenge cookie")
	}
	refresh := h.postForm("/admin/captcha", nil)
	if refresh.StatusCode != 200 || !strings.HasPrefix(refresh.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("refresh through CSRF middleware: %d", refresh.StatusCode)
	}
	if httpapi.CaptchaAnswerForTest(h.app, token) != "" {
		t.Fatal("refresh left the previous challenge live")
	}
	if h.captcha() == "" {
		t.Fatal("fresh challenge missing")
	}
	for _, cookie := range refresh.Cookies() {
		if cookie.Name == "cw_cap" && (cookie.Value == token || !cookie.HttpOnly || cookie.MaxAge != 180) {
			t.Fatal("invalid refresh cookie")
		}
	}
}

func rawBuyerPost(t *testing.T, h *harness, path string, v url.Values) (int, string) {
	t.Helper()
	v.Set("csrf", h.csrf())
	resp, err := h.client.PostForm(h.server.URL+path, v)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}
func TestProtectedFormsRejectMissingAndCrossPurpose(t *testing.T) {
	h := newHarness(t)
	p := h.seedProduct(t)
	h.get("/")
	for _, path := range []string{"/account/login", "/account/register", "/checkout", "/query"} {
		v := url.Values{"username": {"script_user"}, "password": {"long-passphrase-for-test"}, "confirm_password": {"long-passphrase-for-test"}, "product_id": {fmt.Sprint(p.ID)}, "quantity": {"1"}, "agree": {"1"}}
		if status, _ := rawBuyerPost(t, h, path, v); status != 422 {
			t.Fatalf("unverified %s: %d", path, status)
		}
	}
	if n, err := h.db.CountOrders(context.Background(), store.OrderFilter{}); err != nil || n != 0 {
		t.Fatal("unverified checkout created order")
	}
	v := h.solved("account/login", url.Values{"username": {"script_user"}, "password": {"long-passphrase-for-test"}, "confirm_password": {"long-passphrase-for-test"}})
	if status, _ := rawBuyerPost(t, h, "/account/register", v); status != 422 {
		t.Fatal("cross-purpose challenge accepted")
	}
	if status, _ := rawBuyerPost(t, h, "/account/login", v); status != 422 {
		t.Fatal("challenge survived wrong-purpose attempt")
	}
}
func TestOrderReadAndPaymentChallengeBoundaries(t *testing.T) {
	h := newHarness(t)
	p := h.seedProduct(t)
	driver := &reviewPaymentDriver{action: &payment.Action{Kind: "inline"}}
	h.payments.Register(driver)
	order, err := h.db.CreateOrder(context.Background(), store.CreateOrderInput{ProductID: p.ID, Quantity: 1, PaymentDriver: driver.Name(), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	no := order.Order.OrderNo
	path := "/order/" + no
	token := order.QueryToken
	resp, body := h.get(path + "?token=" + token)
	if resp.StatusCode != 200 || !strings.Contains(body, "captcha_id") || strings.Contains(body, p.Name) {
		t.Fatal("direct link leaked order before challenge")
	}
	if resp, _ = h.get(path + "/status?token=" + token); resp.StatusCode != 403 {
		t.Fatal("unverified status")
	}
	vals := h.solved("read:"+no, url.Values{"token": {token}})
	if status, _ := rawBuyerPost(t, h, path+"/verify", vals); status != 303 {
		t.Fatalf("verified lookup %d", status)
	}
	if resp, _ = h.get(path + "/status"); resp.StatusCode != 200 {
		t.Fatal("verified polling denied")
	}
	for _, method := range []string{"GET", "HEAD"} {
		r, _ := http.NewRequest(method, h.server.URL+path+"/pay", nil)
		resp, err := h.client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if st, _ := rawBuyerPost(t, h, path+"/pay", url.Values{}); st != 422 {
		t.Fatal("submitted without challenge")
	}
	wrong := h.solved("pay:DIFFERENT", url.Values{})
	if st, _ := rawBuyerPost(t, h, path+"/pay", wrong); st != 422 || driver.calls != 0 {
		t.Fatal("driver called before verification")
	}
	pay := h.solved("pay:"+no, url.Values{})
	if st, _ := rawBuyerPost(t, h, path+"/pay", pay); st != 200 || driver.calls != 1 {
		t.Fatal("verified payment did not start")
	}
	if st, _ := rawBuyerPost(t, h, path+"/pay", pay); st != 422 || driver.calls != 1 {
		t.Fatal("payment replay reached driver")
	}
}

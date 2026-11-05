package httpapi_test

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func postUXForm(t *testing.T, h *harness, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	form = h.solveForForm(path, form)
	form.Set("csrf", h.csrf())
	r, err := h.client.PostForm(h.server.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return r, string(body)
}

func TestLoginFailuresRemainRetryable(t *testing.T) {
	h := newHarness(t)
	for _, badCaptcha := range []bool{true, false} {
		h.get("/admin/login")
		code := h.captcha()
		status := http.StatusUnauthorized
		if badCaptcha {
			code = "!!!!!!"
			status = http.StatusUnprocessableEntity
		}
		resp, body := postUXForm(t, h, "/admin/login", url.Values{
			"username": {"returning_operator"}, "password": {"do-not-reflect-password"}, "captcha": {code},
		})
		if resp.StatusCode != status || !strings.Contains(body, `action="/admin/login"`) || !strings.Contains(body, `value="returning_operator"`) {
			t.Fatalf("login retry form missing: %d", resp.StatusCode)
		}
		if strings.Contains(body, "do-not-reflect-password") || h.captcha() == "" || !strings.Contains(body, `role="alert"`) {
			t.Fatal("retry must preserve username, rotate captcha, announce the error and omit the password")
		}
	}
}

func TestBuyerAuthFailurePreservesOnlyUsername(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/account/login", "/account/register"} {
		h.get(path)
		resp, body := postUXForm(t, h, path, url.Values{"username": {"returning_buyer"}, "password": {"do-not-reflect-password"}, "confirm_password": {"mismatch"}})
		if resp.StatusCode < 400 || !strings.Contains(body, `value="returning_buyer"`) || strings.Contains(body, "do-not-reflect-password") {
			t.Fatal("buyer retry lost username or reflected password")
		}
	}
}

func TestCheckoutFailurePreservesInputAndCanRecover(t *testing.T) {
	h := newHarness(t)
	p := h.seedProduct(t)
	h.get("/p/" + p.Slug)
	form := url.Values{"product_id": {strconv.FormatInt(p.ID, 10)}, "quantity": {"2"}, "driver": {"manual"}, "agree": {"1"}, "idem": {"recover-checkout"}, "email": {"buyer@example.test"}, "contact": {`<script>alert("contact")</script>`}, "coupon": {"MISSING-COUPON"}}
	resp, body := postUXForm(t, h, "/checkout", form)
	if resp.StatusCode != http.StatusUnprocessableEntity || resp.Header.Get("Location") != "" {
		t.Fatal("validation must rerender without putting private fields in a redirect URL")
	}
	for _, value := range []string{`name="quantity" value="2"`, `value="buyer@example.test"`, `value="MISSING-COUPON"`, `value="recover-checkout"`, `name="agree" value="1" checked`, `data-purchase-total`, `优惠码不存在`} {
		if !strings.Contains(body, value) {
			t.Fatalf("lost checkout field: %s", value)
		}
	}
	if strings.Contains(body, `<script>alert("contact")</script>`) {
		t.Fatal("unescaped form content")
	}
	form.Set("coupon", "")
	resp, _ = postUXForm(t, h, "/checkout", form)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/order/") {
		t.Fatal("corrected checkout could not complete")
	}
}

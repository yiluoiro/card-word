package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"math/big"
	mrand "math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

const (
	captchaTTL      = 5 * time.Minute
	captchaLength   = 4
	captchaCookie   = "cw_cap"
	captchaAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/1/I
)

// captchaKey derives a signing key so the captcha token is stateless and
// cannot be forged or edited on the client.
func (s *Server) captchaKey() []byte {
	return []byte(cryptoutil.HMACSHA256Hex(s.cfg.MasterKey, "captcha-key"))
}

func randomCaptchaCode() (string, error) {
	b := make([]byte, captchaLength)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(captchaAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = captchaAlphabet[n.Int64()]
	}
	return string(b), nil
}

// issueCaptcha returns a signed token plus the code to draw.
func (s *Server) issueCaptcha() (token, code string, err error) {
	code, err = randomCaptchaCode()
	if err != nil {
		return "", "", err
	}
	expires := time.Now().Add(captchaTTL).Unix()
	payload := fmt.Sprintf("%s|%d", code, expires)
	sig := cryptoutil.HMACSHA256Hex(s.captchaKey(), payload)
	token = base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
	return token, code, nil
}

// verifyCaptcha checks signature, expiry and the user's answer.
func (s *Server) verifyCaptcha(token, input string) bool {
	if token == "" || strings.TrimSpace(input) == "" {
		return false
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	if !cryptoutil.VerifyHMACSHA256Hex(s.captchaKey(), string(raw), parts[1]) {
		return false
	}
	fields := strings.SplitN(string(raw), "|", 2)
	if len(fields) != 2 {
		return false
	}
	expires, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(input), fields[0])
}

// captchaSVG renders the code as inline SVG: no fonts, no libraries, and the
// characters are jittered and rotated so naive OCR does not work.
func captchaSVG(code string) template.HTML {
	palette := []string{"#4f46e5", "#7c3aed", "#0e7490", "#0f766e", "#b45309"}
	var sb strings.Builder
	sb.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 136 44" width="136" height="44" role="img" aria-label="验证码">`)
	sb.WriteString(`<rect width="136" height="44" rx="10" fill="var(--surface-3)"/>`)
	for i := 0; i < 4; i++ {
		x1 := mrand.Intn(136)
		y1 := mrand.Intn(44)
		x2 := mrand.Intn(136)
		y2 := mrand.Intn(44)
		sb.WriteString(fmt.Sprintf(
			`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s" stroke-opacity="0.28" stroke-width="1.4"/>`,
			x1, y1, x2, y2, palette[mrand.Intn(len(palette))]))
	}
	for i, ch := range code {
		x := 18 + i*28
		y := 30 + mrand.Intn(7) - 3
		rotate := mrand.Intn(26) - 13
		size := 21 + mrand.Intn(4)
		sb.WriteString(fmt.Sprintf(
			`<text x="%d" y="%d" fill="%s" font-family="ui-monospace,Menlo,Consolas,monospace" font-size="%d" font-weight="700" transform="rotate(%d %d %d)">%c</text>`,
			x, y, palette[mrand.Intn(len(palette))], size, rotate, x, y, ch))
	}
	sb.WriteString(`</svg>`)
	return template.HTML(sb.String())
}

func (s *Server) setCaptchaCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(captchaCookie),
		Value:    token,
		Path:     "/admin",
		MaxAge:   int(captchaTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.Secure(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCaptchaCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(captchaCookie),
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.Secure(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) captchaFromRequest(r *http.Request) string {
	c, err := r.Cookie(s.cookieName(captchaCookie))
	if err != nil {
		return ""
	}
	return c.Value
}

package httpapi

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

const (
	captchaTTL      = 3 * time.Minute
	captchaLength   = 6
	captchaCookie   = "cw_cap"
	captchaAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	captchaCapacity = 4096
)

type captchaChallenge struct {
	answer, binding, scope string
	expires                time.Time
}

// Bounded per-shop state. A restart invalidates outstanding challenges.
type captchaStore struct {
	mu      sync.Mutex
	entries map[string]captchaChallenge
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
func (s *Server) captchaDigest(token, code string) string {
	return cryptoutil.HMACSHA256Hex(s.cfg.MasterKey, "captcha-answer:"+token+":"+strings.ToUpper(strings.TrimSpace(code)))
}
func (s *Server) captchaBinding(r *http.Request) string {
	csrf, _ := r.Context().Value(ctxCSRF).(string)
	return cryptoutil.HMACSHA256Hex(s.cfg.MasterKey, "captcha-browser:"+s.clientIP(r)+"\x00"+r.UserAgent()+"\x00"+csrf)
}
func (s *Server) issueCaptcha(r *http.Request) (token, code string, err error) {
	return s.issueBoundCaptcha("admin", s.captchaBinding(r), s.captchaFromRequest(r))
}
func (s *Server) issueBoundCaptcha(scope, binding, previous string) (token, code string, err error) {
	code, err = randomCaptchaCode()
	if err != nil {
		return
	}
	token, err = cryptoutil.RandomToken(32)
	if err != nil {
		return
	}
	now := time.Now()
	s.captchas.mu.Lock()
	defer s.captchas.mu.Unlock()
	if s.captchas.entries == nil {
		s.captchas.entries = make(map[string]captchaChallenge)
	}
	for key, c := range s.captchas.entries {
		if !now.Before(c.expires) {
			delete(s.captchas.entries, key)
		}
	}
	if k := cryptoutil.TokenHash(previous); previous != "" && s.captchas.entries[k].scope == scope && s.captchas.entries[k].binding == binding {
		delete(s.captchas.entries, k)
	}
	if len(s.captchas.entries) >= captchaCapacity {
		return "", "", errors.New("captcha capacity reached")
	}
	s.captchas.entries[cryptoutil.TokenHash(token)] = captchaChallenge{answer: s.captchaDigest(token, code), binding: binding, scope: scope, expires: now.Add(captchaTTL)}
	return
}
func (s *Server) verifyCaptcha(r *http.Request, input string) bool {
	return s.consumeCaptcha("admin", s.captchaBinding(r), s.captchaFromRequest(r), input)
}
func (s *Server) consumeCaptcha(scope, binding, token, input string) bool {
	if len(token) != 43 {
		return false
	}
	key := cryptoutil.TokenHash(token)
	s.captchas.mu.Lock()
	c, ok := s.captchas.entries[key]
	delete(s.captchas.entries, key)
	s.captchas.mu.Unlock()
	// Consume atomically on every attempt, even an incorrect or empty answer.
	if !ok || !time.Now().Before(c.expires) || c.binding != binding || c.scope != scope || len(strings.TrimSpace(input)) != captchaLength {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.answer), []byte(s.captchaDigest(token, input))) == 1
}

// Raster glyphs avoid text nodes, embedded answers and font metadata.
var captchaGlyphs = map[byte][7]byte{
	'A': {14, 17, 17, 31, 17, 17, 17}, 'B': {30, 17, 17, 30, 17, 17, 30}, 'C': {14, 17, 16, 16, 16, 17, 14},
	'D': {30, 17, 17, 17, 17, 17, 30}, 'E': {31, 16, 16, 30, 16, 16, 31}, 'F': {31, 16, 16, 30, 16, 16, 16},
	'G': {14, 17, 16, 23, 17, 17, 15}, 'H': {17, 17, 17, 31, 17, 17, 17}, 'J': {7, 2, 2, 2, 18, 18, 12},
	'K': {17, 18, 20, 24, 20, 18, 17}, 'L': {16, 16, 16, 16, 16, 16, 31}, 'M': {17, 27, 21, 21, 17, 17, 17},
	'N': {17, 25, 25, 21, 19, 19, 17}, 'P': {30, 17, 17, 30, 16, 16, 16}, 'Q': {14, 17, 17, 17, 21, 18, 13},
	'R': {30, 17, 17, 30, 20, 18, 17}, 'S': {15, 16, 16, 14, 1, 1, 30}, 'T': {31, 4, 4, 4, 4, 4, 4},
	'U': {17, 17, 17, 17, 17, 17, 14}, 'V': {17, 17, 17, 17, 17, 10, 4}, 'W': {17, 17, 17, 21, 21, 21, 10},
	'X': {17, 17, 10, 4, 10, 17, 17}, 'Y': {17, 17, 10, 4, 4, 4, 4}, 'Z': {31, 1, 2, 4, 8, 16, 31},
	'2': {14, 17, 1, 2, 4, 8, 31}, '3': {30, 1, 1, 14, 1, 1, 30}, '4': {2, 6, 10, 18, 31, 2, 2},
	'5': {31, 16, 16, 30, 1, 1, 30}, '6': {14, 16, 16, 30, 17, 17, 14}, '7': {31, 1, 2, 4, 8, 8, 8},
	'8': {14, 17, 17, 14, 17, 17, 14}, '9': {14, 17, 17, 15, 1, 1, 14},
}

func captchaPNG(code string) (string, error) {
	const width, height = 240, 76
	seed := make([]byte, 256)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			n := uint8((x*13 + y*7 + int(seed[(x+y)%256])) % 13)
			img.SetRGBA(x, y, color.RGBA{237 + n, 240 + n, 242 + n, 255})
		}
	}
	for i := 0; i < 5; i++ {
		for x := 0; x < width; x++ {
			y := int(float64(seed[i]%height) + 7*math.Sin(float64(x)/24+float64(seed[i+8])))
			img.SetRGBA(x, y, color.RGBA{173, 192, 185, 255})
		}
	}
	palette := []color.RGBA{{31, 65, 66, 255}, {43, 65, 104, 255}, {95, 49, 80, 255}, {45, 87, 66, 255}}
	// Interleave two randomly positioned outlined decoys with six solid glyphs.
	first := int(seed[112] % 8)
	second := (first + 1 + int(seed[113]%7)) % 8
	next := 0
	for i := 0; i < 8; i++ {
		decoy := i == first || i == second
		ch := captchaAlphabet[int(seed[120+i])%len(captchaAlphabet)]
		if !decoy {
			ch = code[next]
			next++
		}
		glyph := captchaGlyphs[ch]
		angle := (float64(seed[32+i]%27) - 13) * math.Pi / 180
		cx, cy := float64(15+i*30), float64(37+int(seed[48+i]%11)-5)
		mask := [76][30]bool{}
		for y := 0; y < height; y++ {
			for x := 0; x < 30; x++ {
				dx, dy := float64(x+i*30)-cx, float64(y)-cy
				gx := (dx*math.Cos(angle)+dy*math.Sin(angle)+1.4*math.Sin(dy/9+float64(seed[64+i])))/4 + 2.5
				gy := (-dx*math.Sin(angle)+dy*math.Cos(angle))/4.8 + 3.5
				mask[y][x] = gx >= 0 && gx < 5 && gy >= 0 && gy < 7 && glyph[int(gy)]&(1<<uint(4-int(gx))) != 0
			}
		}
		for y := 1; y < height-1; y++ {
			for x := 1; x < 29; x++ {
				if mask[y][x] && (!decoy || !mask[y-1][x] || !mask[y+1][x] || !mask[y][x-1] || !mask[y][x+1]) {
					img.SetRGBA(i*30+x, y, palette[int(seed[80+i])%len(palette)])
				}
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes()), nil
}
func (s *Server) captchaImage(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.captchaLimit.allow(s.clientIP(r)) {
		w.Header().Set("Retry-After", "5")
		s.renderError(w, r, 429, "验证码刷新过于频繁，请稍后再试。")
		return "", false
	}
	token, code, err := s.issueCaptcha(r)
	var src string
	if err == nil {
		src, err = captchaPNG(code)
	}
	if err != nil {
		s.log.Error("captcha generation failed", "error", err)
		s.renderError(w, r, 503, "验证服务暂不可用，请稍后再试。")
		return "", false
	}
	s.setCaptchaCookie(w, token)
	return src, true
}
func (s *Server) handleCaptchaRefresh(w http.ResponseWriter, r *http.Request) {
	if src, ok := s.captchaImage(w, r); ok {
		writeJSON(w, 200, map[string]any{"image": src, "expires_in": int(captchaTTL.Seconds())})
	}
}
func (s *Server) setCaptchaCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(captchaCookie), Value: token, Path: "/", MaxAge: int(captchaTTL.Seconds()), HttpOnly: true, Secure: s.cfg.Secure(), SameSite: http.SameSiteStrictMode})
}
func (s *Server) clearCaptchaCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(captchaCookie), Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.Secure(), SameSite: http.SameSiteStrictMode})
}
func (s *Server) captchaFromRequest(r *http.Request) string {
	c, err := r.Cookie(s.cookieName(captchaCookie))
	if err != nil {
		return ""
	}
	return c.Value
}

const captchaRetry = "验证码有误或已失效，请按新图重新输入。"

var publicScope = regexp.MustCompile(`^(account/(login|register)|query|checkout:[0-9]{1,19}|(read|pay):[A-Za-z0-9-]{1,80})$`)

func (s *Server) formVerified(r *http.Request, scope string) bool {
	return s.consumeCaptcha(scope, s.captchaBinding(r), r.PostFormValue("captcha_id"), r.PostFormValue("captcha"))
}

type formChallenge struct {
	ID    string       `json:"id"`
	Image template.URL `json:"image"`
	Scope string       `json:"scope"`
	TTL   int          `json:"expires_in"`
}

func (s *Server) makeChallenge(w http.ResponseWriter, r *http.Request, scope, binding, previous string) (formChallenge, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.publicCaptchaLimit.allow(s.clientIP(r)) {
		w.Header().Set("Retry-After", "5")
		s.challengeError(w, r, 429, "rate_limited", "验证码请求过于频繁，请稍后再试。")
		return formChallenge{}, false
	}
	id, code, err := s.issueBoundCaptcha(scope, binding, previous)
	var src string
	if err == nil {
		src, err = captchaPNG(code)
	}
	if err != nil {
		s.challengeError(w, r, 503, "captcha_unavailable", "验证服务暂不可用，请稍后重试。")
		return formChallenge{}, false
	}
	return formChallenge{id, template.URL(src), scope, int(captchaTTL.Seconds())}, true
}
func (s *Server) handleFormCaptcha(w http.ResponseWriter, r *http.Request) {
	scope := r.PostFormValue("scope")
	if !publicScope.MatchString(scope) {
		writeAPIError(w, 400, "invalid_scope", "无效的验证用途")
		return
	}
	c, ok := s.makeChallenge(w, r, scope, s.captchaBinding(r), r.PostFormValue("captcha_id"))
	if ok {
		writeJSON(w, 200, c)
	}
}
func (s *Server) renderChallengeForm(w http.ResponseWriter, r *http.Request, status int, name, scope string, data map[string]any) {
	if scope != "" {
		c, ok := s.makeChallenge(w, r, scope, s.captchaBinding(r), "")
		if !ok {
			return
		}
		data["Challenge"] = c
	}
	s.renderStatus(w, r, status, name, data)
}

// API challenges bind to an authenticated integration and its exact operation;
// these clients need not maintain the storefront's CSRF cookie.
func (s *Server) apiVerified(w http.ResponseWriter, r *http.Request, id int64, operation string) bool {
	binding := cryptoutil.HMACSHA256Hex(s.cfg.MasterKey, "api-captcha:"+strconv.FormatInt(id, 10)+"|"+s.clientIP(r)+"|"+r.UserAgent())
	scope := "api:" + operation
	if s.consumeCaptcha(scope, binding, r.Header.Get("X-Captcha-ID"), r.Header.Get("X-Captcha-Answer")) {
		return true
	}
	c, ok := s.makeChallenge(w, r, scope, binding, "")
	if ok {
		writeJSON(w, 428, map[string]any{"ok": false, "error": map[string]string{"code": "captcha_required", "message": "输入六个实心字符，跳过空心字符"}, "challenge": c})
	}
	return false
}

func (s *Server) challengeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	if r.URL.Path == "/captcha" || strings.HasPrefix(r.URL.Path, "/api/v1/") {
		writeAPIError(w, status, code, msg)
		return
	}
	s.renderError(w, r, status, msg)
}

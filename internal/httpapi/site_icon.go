package httpapi

import (
	"bytes"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const siteIconSetting = "site.icon"

func (s *Server) siteIconURL() string {
	name := s.setting(siteIconSetting, "")
	if strings.HasPrefix(name, "site-icon-") && filepath.Base(name) == name && !strings.ContainsAny(name, "/\\") && strings.HasSuffix(name, ".png") {
		return "/media/" + name
	}
	return "/static/reference/brand.svg"
}

func (s *Server) handleSiteIcon(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/settings"
	if err := r.ParseMultipartForm(3 << 20); err != nil && err != http.ErrNotMultipart {
		s.renderError(w, r, 422, "请选择不超过 2 MB 的 PNG 或 JPG 图片。")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	name := ""
	if r.FormValue("action") != "reset" {
		file, _, err := r.FormFile("icon")
		if err != nil {
			s.renderError(w, r, 422, "请选择站点图标。")
			return
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
		if err != nil || len(raw) > 2<<20 {
			s.renderError(w, r, 422, "图片不能超过 2 MB。")
			return
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil || (format != "png" && format != "jpeg") || cfg.Width != cfg.Height || cfg.Width < 16 || cfg.Width > 2048 {
			s.renderError(w, r, 422, "请使用 16–2048 像素的正方形 PNG 或 JPG 图片。")
			return
		}
		img, _, err := image.Decode(bytes.NewReader(raw))
		if err != nil {
			s.renderError(w, r, 422, "图片无法读取，请重新选择。")
			return
		}
		size := min(cfg.Width, 512)
		normalized := image.NewNRGBA(image.Rect(0, 0, size, size))
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				normalized.Set(x, y, img.At(img.Bounds().Min.X+x*cfg.Width/size, img.Bounds().Min.Y+y*cfg.Height/size))
			}
		}
		var output bytes.Buffer
		if err := png.Encode(&output, normalized); err != nil {
			s.renderError(w, r, 500, "处理图标失败。")
			return
		}
		name = "site-icon-" + randomHex(16) + ".png"
		if err := os.WriteFile(filepath.Join(s.uploadDir(), name), output.Bytes(), 0644); err != nil {
			s.renderError(w, r, 500, "保存图标失败。")
			return
		}
	}
	old := s.siteIconURL()
	if err := s.db.SetSettings(r.Context(), map[string]string{siteIconSetting: name}); err != nil {
		if name != "" {
			_ = os.Remove(filepath.Join(s.uploadDir(), name))
		}
		s.renderError(w, r, 500, "保存图标设置失败。")
		return
	}
	if err := s.reloadSettings(r.Context()); err != nil {
		s.renderError(w, r, 500, "图标已保存，请刷新页面。")
		return
	}
	if strings.HasPrefix(old, "/media/site-icon-") && filepath.Base(old) != name {
		_ = os.Remove(filepath.Join(s.uploadDir(), filepath.Base(old)))
	}
	s.audit(r, "site.icon.update", name, "")
	flashRedirect(w, r, back, "站点图标已更新。", "")
}

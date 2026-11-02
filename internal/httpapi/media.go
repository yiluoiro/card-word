package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxCoverBytes = 2 << 20

func (s *Server) uploadDir() string {
	dir := filepath.Join(s.cfg.DataDir, "uploads")
	_ = os.MkdirAll(dir, 0o750)
	return dir
}

// handleProductCoverUpload stores a product cover image and points the product
// at it. The type is decided from magic bytes, never from the client filename.
func (s *Server) handleProductCoverUpload(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	back := "/admin/products/" + r.PathValue("id")
	product, err := s.db.ProductByID(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "商品不存在。")
		return
	}
	if err := r.ParseMultipartForm(maxCoverBytes + 1<<20); err != nil {
		flashRedirect(w, r, back, "", "上传失败：请选择小于 2MB 的图片。")
		return
	}
	file, _, err := r.FormFile("cover")
	if err != nil {
		flashRedirect(w, r, back, "", "请选择图片文件。")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCoverBytes+1))
	if err != nil || len(data) > maxCoverBytes {
		flashRedirect(w, r, back, "", "图片超过 2MB 限制。")
		return
	}
	ext, ok := coverExt(data)
	if !ok {
		flashRedirect(w, r, back, "", "仅支持 PNG / JPEG / WebP 图片。")
		return
	}
	name := fmt.Sprintf("%d-%s.%s", product.ID, randomHex(8), ext)
	if err := os.WriteFile(filepath.Join(s.uploadDir(), name), data, 0o644); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "保存图片失败。")
		return
	}
	old := product.CoverPath
	if err := s.db.SetProductCover(r.Context(), product.ID, name); err != nil {
		_ = os.Remove(filepath.Join(s.uploadDir(), name))
		s.renderError(w, r, http.StatusInternalServerError, "更新商品失败。")
		return
	}
	if old != "" && old != name {
		_ = os.Remove(filepath.Join(s.uploadDir(), filepath.Base(old)))
	}
	s.audit(r, "product.cover", product.Slug, name)
	flashRedirect(w, r, back, "封面已更新", "")
}

func coverExt(data []byte) (string, bool) {
	switch http.DetectContentType(data) {
	case "image/png":
		return "png", true
	case "image/jpeg":
		return "jpg", true
	case "image/webp":
		return "webp", true
	}
	return "", false
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(b)
}

// handleMedia serves uploaded images. Filenames are random per upload, so the
// response can be cached immutably.
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.uploadDir(), name)
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

package httpapi

import (
	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Uploaded Markdown images share the same per-shop media directory as covers.
func (s *Server) handleMarkdownImage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, maxCoverBytes+64*1024)
	if err := r.ParseMultipartForm(maxCoverBytes + 64*1024); err != nil {
		writeAPIError(w, 413, "too_large", "图片不能超过 2 MB。")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		writeAPIError(w, 400, "missing_image", "请选择图片。")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCoverBytes+1))
	if err != nil || len(data) > maxCoverBytes {
		writeAPIError(w, 413, "too_large", "图片不能超过 2 MB。")
		return
	}
	ext, ok := coverExt(data)
	if !ok {
		writeAPIError(w, 422, "invalid_image", "仅支持 PNG、JPEG 和 WebP 图片。")
		return
	}
	token, err := cryptoutil.RandomToken(18)
	if err != nil {
		writeAPIError(w, 500, "upload_failed", "保存图片失败。")
		return
	}
	name := "md-" + token + "." + ext
	path := filepath.Join(s.uploadDir(), name)
	fileOut, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		writeAPIError(w, 500, "upload_failed", "保存图片失败。")
		return
	}
	_, writeErr := fileOut.Write(data)
	closeErr := fileOut.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		writeAPIError(w, 500, "upload_failed", "保存图片失败。")
		return
	}
	s.audit(r, "markdown.image", name, "")
	// Store site-relative media paths.
	writeJSON(w, 201, map[string]string{"url": "/media/" + name})
}

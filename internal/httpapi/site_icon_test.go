package httpapi

import (
	"bytes"

	"mime/multipart"
	"net/http"
	"net/http/httptest"

	"testing"
)

func iconRequest(t *testing.T, s *Server, path string, cookie *http.Cookie, csrf string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	writer := multipart.NewWriter(&b)
	_ = writer.WriteField("csrf", csrf)
	file, err := writer.CreateFormFile("icon", "untrusted.svg")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(data)
	_ = writer.Close()
	r := httptest.NewRequest("POST", path, &b)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

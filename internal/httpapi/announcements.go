package httpapi

import (
	"fmt"
	"github.com/yiluoiro/card-word/internal/store"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) handleAnnouncements(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	status := r.URL.Query().Get("status")
	audience := r.URL.Query().Get("audience")
	mode := r.URL.Query().Get("mode")
	_, total, err := s.db.AnnouncementPage(r.Context(), q, status, audience, mode, 1, 0)
	if err != nil {
		s.renderError(w, r, 500, "读取公告失败。")
		return
	}
	page := paginate(r.URL.Query().Get("page"), total, 20, "/admin/announcements", "", url.Values{"q": {q}, "status": {status}, "audience": {audience}, "mode": {mode}})
	rows, _, err := s.db.AnnouncementPage(r.Context(), q, status, audience, mode, 20, page.Offset)
	if err != nil {
		s.renderError(w, r, 500, "读取公告失败。")
		return
	}
	edit := store.Announcement{Status: "draft", Audience: "all", Mode: "silent"}
	if id := parseInt64(r.URL.Query().Get("edit")); id > 0 {
		edit, err = s.db.AnnouncementByID(r.Context(), id)
		if err != nil {
			s.renderError(w, r, 404, "公告不存在。")
			return
		}
	}
	s.render(w, r, "admin_announcements", map[string]any{"Title": "公告管理", "Announcements": rows, "Editing": edit, "Pagination": page, "Search": q, "Status": status, "Audience": audience, "Mode": mode})
}

func (s *Server) handleAnnouncementSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, 400, "表单格式错误。")
		return
	}
	a := store.Announcement{ID: parseInt64(r.FormValue("id")), Title: r.FormValue("title"), Body: r.FormValue("body"), Status: r.FormValue("status"), Audience: r.FormValue("audience"), Mode: r.FormValue("mode")}
	id, err := s.db.SaveAnnouncement(r.Context(), a)
	if err != nil {
		s.renderStatus(w, r, 422, "admin_announcement_edit", map[string]any{"Title": "编辑公告", "Editing": a, "Error": err.Error()})
		return
	}
	s.audit(r, "announcement.save", fmt.Sprint(id), a.Status)
	flashRedirect(w, r, "/admin/announcements", "公告已保存。", "")
}
func (s *Server) handleAnnouncementDelete(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	if err := s.db.DeleteAnnouncement(r.Context(), id); err != nil {
		flashRedirect(w, r, "/admin/announcements", "", "删除失败。")
		return
	}
	s.audit(r, "announcement.delete", fmt.Sprint(id), "")
	flashRedirect(w, r, "/admin/announcements", "公告已删除。", "")
}

type announcementDisplay struct {
	store.Announcement
	HTML template.HTML
}

func (s *Server) visibleAnnouncements(r *http.Request) []announcementDisplay {
	if s.db == nil || r.URL.Path == "/install" || strings.HasPrefix(r.URL.Path, "/admin") {
		return nil
	}
	logged := s.currentCustomer(r) != nil
	rows, err := s.db.Announcements(r.Context(), "", "published", "", "")
	if err != nil {
		return nil
	}
	out := []announcementDisplay{}
	for _, a := range rows {
		if a.Audience == "all" || (a.Audience == "guest" && !logged) || (a.Audience == "user" && logged) {
			a.Source = "site"
			out = append(out, announcementDisplay{a, renderMarkdown(a.Body)})
		}
	}
	return out
}

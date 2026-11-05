package httpapi

import "net/http"

func (s *Server) updateEnabled(w http.ResponseWriter) bool {
	w.Header().Set("Cache-Control", "no-store")
	if s.updates == nil || s.restart == nil {
		writeJSON(w, 503, map[string]string{"error": "更新服务未启用。"})
		return false
	}
	return true
}
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !s.updateEnabled(w) {
		return
	}
	v, e := s.updates.Check(r.Context(), r.URL.Query().Get("refresh") == "1")
	if e != nil {
		writeJSON(w, 500, map[string]string{"error": e.Error()})
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) handleUpdateRepository(w http.ResponseWriter, r *http.Request) {
	if !s.updateEnabled(w) {
		return
	}
	repo := r.PostFormValue("repository")
	if e := s.updates.Bind(repo); e != nil {
		writeJSON(w, 400, map[string]string{"error": e.Error()})
		return
	}
	s.audit(r, "system.update.repository", repo, "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if !s.updateEnabled(w) {
		return
	}
	v := r.PostFormValue("version")
	if e := s.updates.Start(v, r.PostFormValue("repository")); e != nil {
		writeJSON(w, 409, map[string]string{"error": e.Error()})
		return
	}
	s.audit(r, "system.update.download", v, "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) handleUpdateRestart(w http.ResponseWriter, r *http.Request) {
	if !s.updateEnabled(w) {
		return
	}
	v := r.PostFormValue("version")
	if e := s.updates.PrepareRestart(v); e != nil {
		writeJSON(w, 409, map[string]string{"error": e.Error()})
		return
	}
	s.audit(r, "system.update.restart", v, "")
	writeJSON(w, 202, map[string]bool{"ok": true})
	s.restart()
}

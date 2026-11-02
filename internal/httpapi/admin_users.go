package httpapi

import (
	"net/http"
	"strings"

	"github.com/yiluoiro/card-word/internal/store"
)

// handleAdminUsers lists the team. Owner only.
func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.db.ListUsers(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载账号失败。")
		return
	}
	s.render(w, r, "admin_users", map[string]any{
		"Title": "团队与权限",
		"Users": users,
		"Roles": []string{store.RoleOwner, store.RoleAdmin, store.RoleViewer},
	})
}

// handleAdminUserCreate adds a teammate.
func (s *Server) handleAdminUserCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	role := strings.TrimSpace(r.FormValue("role"))
	if username == "" {
		flashRedirect(w, r, "/admin/users", "", "用户名不能为空。")
		return
	}
	if len(password) < 12 {
		flashRedirect(w, r, "/admin/users", "", "初始密码至少 12 个字符。")
		return
	}
	if !store.ValidRole(role) {
		flashRedirect(w, r, "/admin/users", "", "角色不合法。")
		return
	}
	user, err := s.db.CreateUser(r.Context(), username, password, role)
	if err != nil {
		flashRedirect(w, r, "/admin/users", "", "创建失败："+err.Error())
		return
	}
	s.audit(r, "user.create", user.Username, "role="+role)
	flashRedirect(w, r, "/admin/users", "已创建 "+user.Username+"，请通过安全渠道告知初始密码", "")
}

// handleAdminUserRole changes a role while protecting the last owner.
func (s *Server) handleAdminUserRole(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	me := s.currentUser(r)
	target, err := s.db.UserByID(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		flashRedirect(w, r, "/admin/users", "", "账号不存在。")
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	if !store.ValidRole(role) {
		flashRedirect(w, r, "/admin/users", "", "角色不合法。")
		return
	}
	if me != nil && target.ID == me.ID {
		flashRedirect(w, r, "/admin/users", "", "不能修改自己的角色，请让其他所有者操作。")
		return
	}
	if target.Role == store.RoleOwner && role != store.RoleOwner {
		if owners, _ := s.db.CountOwners(r.Context()); owners <= 1 {
			flashRedirect(w, r, "/admin/users", "", "这是最后一个所有者，不能降级。")
			return
		}
	}
	if err := s.db.SetUserRole(r.Context(), target.ID, role); err != nil {
		flashRedirect(w, r, "/admin/users", "", "修改失败。")
		return
	}
	s.audit(r, "user.role", target.Username, target.Role+" -> "+role)
	flashRedirect(w, r, "/admin/users", "角色已更新", "")
}

// handleAdminUserDisable blocks or restores an account.
func (s *Server) handleAdminUserDisable(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	me := s.currentUser(r)
	target, err := s.db.UserByID(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		flashRedirect(w, r, "/admin/users", "", "账号不存在。")
		return
	}
	if me != nil && target.ID == me.ID {
		flashRedirect(w, r, "/admin/users", "", "不能停用自己的账号。")
		return
	}
	disable := r.FormValue("disabled") != ""
	if disable && target.Role == store.RoleOwner {
		if owners, _ := s.db.CountOwners(r.Context()); owners <= 1 {
			flashRedirect(w, r, "/admin/users", "", "这是最后一个所有者，不能停用。")
			return
		}
	}
	if err := s.db.SetUserDisabled(r.Context(), target.ID, disable); err != nil {
		flashRedirect(w, r, "/admin/users", "", "操作失败。")
		return
	}
	if disable {
		_ = s.db.DeleteUserSessions(r.Context(), target.ID, "")
	}
	state := "启用"
	if disable {
		state = "停用"
	}
	s.audit(r, "user.disable", target.Username, "disabled="+boolText(disable))
	flashRedirect(w, r, "/admin/users", "已"+state+" "+target.Username, "")
}

// handleAdminUserDelete removes an account.
func (s *Server) handleAdminUserDelete(w http.ResponseWriter, r *http.Request) {
	me := s.currentUser(r)
	target, err := s.db.UserByID(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		flashRedirect(w, r, "/admin/users", "", "账号不存在。")
		return
	}
	if me != nil && target.ID == me.ID {
		flashRedirect(w, r, "/admin/users", "", "不能删除自己的账号。")
		return
	}
	if target.Role == store.RoleOwner {
		if owners, _ := s.db.CountOwners(r.Context()); owners <= 1 {
			flashRedirect(w, r, "/admin/users", "", "这是最后一个所有者，不能删除。")
			return
		}
	}
	if err := s.db.DeleteUser(r.Context(), target.ID); err != nil {
		flashRedirect(w, r, "/admin/users", "", "删除失败。")
		return
	}
	s.audit(r, "user.delete", target.Username, "")
	flashRedirect(w, r, "/admin/users", "已删除 "+target.Username, "")
}

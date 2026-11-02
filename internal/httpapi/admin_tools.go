package httpapi

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yiluoiro/card-word/internal/notify"
)

// handleAdminAuditExport streams the audit trail as CSV.
func (s *Server) handleAdminAuditExport(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	entries, err := s.db.ListAudit(r.Context(), action, exportRowLimit, 0)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "导出失败。")
		return
	}
	filename := fmt.Sprintf("audit-%s.csv", time.Now().Format("20060102-1504"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"时间", "操作者", "动作", "对象", "IP", "详情"})
	for _, e := range entries {
		_ = cw.Write([]string{
			e.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			e.Actor, e.Action, e.Target, e.IP, e.Detail,
		})
	}
	s.audit(r, "audit.export", "", fmt.Sprintf("rows=%d action=%q", len(entries), action))
}

// handleAdminTestEmail sends a one-off message using the saved SMTP settings,
// so operators can verify credentials without waiting for a real order.
func (s *Server) handleAdminTestEmail(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	ctx := r.Context()
	to := strings.TrimSpace(r.FormValue("to"))
	if to == "" {
		flashRedirect(w, r, "/admin/settings", "", "请输入收件邮箱。")
		return
	}
	host := mustSetting(ctx, s.db, "smtp.host", "")
	from := mustSetting(ctx, s.db, "smtp.from", "")
	if host == "" || from == "" {
		flashRedirect(w, r, "/admin/settings", "", "请先填写并保存 SMTP 主机与发件人地址。")
		return
	}
	port := int(parseInt64(orDefault(mustSetting(ctx, s.db, "smtp.port", "587"), "587")))
	sender := notify.NewSMTP(notify.SMTPConfig{
		Host:       host,
		Port:       port,
		Username:   mustSetting(ctx, s.db, "smtp.username", ""),
		Password:   mustSetting(ctx, s.db, "smtp.password", ""),
		From:       from,
		FromName:   mustSetting(ctx, s.db, "smtp.from_name", "Card Word"),
		UseTLS:     mustSetting(ctx, s.db, "smtp.tls", "1") == "1",
		SkipVerify: mustSetting(ctx, s.db, "smtp.insecure", "0") == "1",
	})
	if sender == nil {
		flashRedirect(w, r, "/admin/settings", "", "SMTP 配置不完整。")
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err := sender.Send(sendCtx, notify.Message{
		To:      to,
		Subject: "card word SMTP 测试",
		Body: "如果你收到这封邮件，说明发件配置可用。\n\n" +
			"站点: " + s.setting("site.name", "Card Word") + "\n" +
			"时间: " + time.Now().Format(time.RFC3339) + "\n",
	})
	if err != nil {
		s.audit(r, "settings.test_email.failed", to, err.Error())
		flashRedirect(w, r, "/admin/settings", "", "发送失败："+err.Error())
		return
	}
	s.audit(r, "settings.test_email", to, "smtp test delivered")
	flashRedirect(w, r, "/admin/settings", "测试邮件已发送至 "+to, "")
}

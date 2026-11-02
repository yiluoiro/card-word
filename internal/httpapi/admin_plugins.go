package httpapi

import (
	"io"
	"net/http"
	"strings"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/market"
	"github.com/yiluoiro/card-word/internal/plugin"
)

func (s *Server) handleAdminPlugins(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	records, err := s.db.ListPlugins(ctx, false)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载插件失败。")
		return
	}
	installed := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		m, err := plugin.ParseManifest([]byte(rec.Manifest))
		if err != nil {
			installed = append(installed, map[string]any{
				"UID": rec.UID, "Name": rec.Name, "Version": rec.Version, "Kind": rec.Kind,
				"Enabled": rec.Enabled, "Error": err.Error(), "Broken": true,
			})
			continue
		}
		view := pluginViewDecorator(*m, rec)
		settings, _ := s.db.PluginSettings(ctx, rec.UID)
		view["Values"] = settings
		installed = append(installed, view)
	}
	var marketEntries []any
	var marketErr string
	if s.market != nil {
		entries, err := s.market.List(ctx, s.cfg.MarketURL)
		if err != nil {
			marketErr = err.Error()
		}
		for _, e := range entries {
			marketEntries = append(marketEntries, e)
		}
	}
	s.render(w, r, "admin_plugins", map[string]any{
		"Title":             "应用商城",
		"Installed":         installed,
		"Market":            marketEntries,
		"MarketError":       marketErr,
		"AllowInstall":      s.cfg.MarketAllowInstall,
		"SignatureRequired": s.cfg.RequirePluginSignature,
		"MarketURL":         s.cfg.MarketURL,
	})
}

func (s *Server) handleAdminPluginUpload(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		flashRedirect(w, r, "/admin/plugins", "", "插件子系统未启用。")
		return
	}
	if err := r.ParseMultipartForm(plugin.MaxPackageBytes + 4096); err != nil {
		flashRedirect(w, r, "/admin/plugins", "", "上传失败：请使用小于 8MB 的 ZIP 包。")
		return
	}
	file, header, err := r.FormFile("package")
	if err != nil {
		flashRedirect(w, r, "/admin/plugins", "", "请选择插件包。")
		return
	}
	defer file.Close()
	if header.Size > plugin.MaxPackageBytes {
		flashRedirect(w, r, "/admin/plugins", "", "插件包超过 8MB 限制。")
		return
	}
	pkg, err := io.ReadAll(io.LimitReader(file, plugin.MaxPackageBytes+1))
	if err != nil {
		flashRedirect(w, r, "/admin/plugins", "", "读取插件包失败。")
		return
	}
	signature := strings.TrimSpace(r.FormValue("signature"))
	manifest, err := s.plugins.Install(r.Context(), pkg, signature, "upload")
	if err != nil {
		s.audit(r, "plugin.install.failed", header.Filename, err.Error())
		flashRedirect(w, r, "/admin/plugins", "", "安装失败："+err.Error())
		return
	}
	s.audit(r, "plugin.install", manifest.UID, "version="+manifest.Version+" signed="+boolText(signature != ""))
	flashRedirect(w, r, "/admin/plugins", "插件 "+manifest.Name+" 已安装，请检查权限后启用", "")
}

func (s *Server) handleAdminPluginEnable(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if s.plugins == nil {
		flashRedirect(w, r, "/admin/plugins", "", "插件子系统未启用。")
		return
	}
	if err := s.plugins.Enable(r.Context(), uid); err != nil {
		s.audit(r, "plugin.enable.failed", uid, err.Error())
		flashRedirect(w, r, "/admin/plugins", "", "启用失败："+err.Error())
		return
	}
	s.audit(r, "plugin.enable", uid, "")
	flashRedirect(w, r, "/admin/plugins", "插件已启用", "")
}

func (s *Server) handleAdminPluginDisable(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if s.plugins == nil {
		flashRedirect(w, r, "/admin/plugins", "", "插件子系统未启用。")
		return
	}
	if err := s.plugins.Disable(r.Context(), uid); err != nil {
		flashRedirect(w, r, "/admin/plugins", "", "停用失败："+err.Error())
		return
	}
	s.audit(r, "plugin.disable", uid, "")
	flashRedirect(w, r, "/admin/plugins", "插件已停用", "")
}

func (s *Server) handleAdminPluginDelete(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if s.plugins == nil {
		flashRedirect(w, r, "/admin/plugins", "", "插件子系统未启用。")
		return
	}
	if err := s.plugins.Uninstall(r.Context(), uid); err != nil {
		flashRedirect(w, r, "/admin/plugins", "", "卸载失败："+err.Error())
		return
	}
	s.audit(r, "plugin.uninstall", uid, "")
	flashRedirect(w, r, "/admin/plugins", "插件已卸载", "")
}

func (s *Server) handleAdminPluginSettings(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	rec, err := s.db.PluginByUID(r.Context(), uid)
	if err != nil {
		flashRedirect(w, r, "/admin/plugins", "", "插件不存在。")
		return
	}
	manifest, err := plugin.ParseManifest([]byte(rec.Manifest))
	if err != nil {
		flashRedirect(w, r, "/admin/plugins", "", "插件清单无效。")
		return
	}
	for _, setting := range manifest.Settings {
		value := strings.TrimSpace(r.FormValue("setting_" + setting.Key))
		if setting.Type == "boolean" {
			value = "0"
			if r.FormValue("setting_"+setting.Key) != "" {
				value = "1"
			}
		}
		if setting.Type == "secret" && (value == "" || strings.HasPrefix(value, "••")) {
			continue
		}
		if err := s.db.SetPluginSetting(r.Context(), uid, setting.Key, value); err != nil {
			flashRedirect(w, r, "/admin/plugins", "", "保存失败："+err.Error())
			return
		}
	}
	s.audit(r, "plugin.settings", uid, "")
	flashRedirect(w, r, "/admin/plugins", "插件配置已保存", "")
}

func (s *Server) handleAdminMarketInstall(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil || s.market == nil {
		flashRedirect(w, r, "/admin/plugins", "", "商城不可用。")
		return
	}
	if !s.cfg.MarketAllowInstall {
		flashRedirect(w, r, "/admin/plugins", "", "当前配置禁止从商城安装。")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "表单格式错误。")
		return
	}
	uid := strings.TrimSpace(r.FormValue("uid"))
	entries, err := s.market.List(r.Context(), s.cfg.MarketURL)
	if err != nil && len(entries) == 0 {
		flashRedirect(w, r, "/admin/plugins", "", "商城索引不可用。")
		return
	}
	var chosen *market.Entry
	for i := range entries {
		if entries[i].UID == uid {
			chosen = &entries[i]
			break
		}
	}
	if chosen == nil {
		flashRedirect(w, r, "/admin/plugins", "", "商城条目不存在。")
		return
	}
	pkg, err := s.market.Download(r.Context(), *chosen, plugin.MaxPackageBytes)
	if err != nil {
		flashRedirect(w, r, "/admin/plugins", "", "下载失败："+err.Error())
		return
	}
	if chosen.SHA256 != "" && !strings.EqualFold(cryptoutil.SHA256Hex(pkg), chosen.SHA256) {
		s.audit(r, "plugin.install.failed", uid, "checksum mismatch")
		flashRedirect(w, r, "/admin/plugins", "", "校验失败：安装包哈希与索引不一致。")
		return
	}
	manifest, err := s.plugins.Install(r.Context(), pkg, chosen.Signature, "market")
	if err != nil {
		flashRedirect(w, r, "/admin/plugins", "", "安装失败："+err.Error())
		return
	}
	s.audit(r, "plugin.install", manifest.UID, "source=market")
	flashRedirect(w, r, "/admin/plugins", "插件 "+manifest.Name+" 已安装", "")
}

// handlePluginAsset serves files of static plugins from the plugin directory.
func (s *Server) handlePluginAsset(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if s.plugins == nil {
		http.NotFound(w, r)
		return
	}
	rt, ok := s.plugins.RuntimeLookup(uid)
	if !ok || rt.Static == nil {
		http.NotFound(w, r)
		return
	}
	rest := r.PathValue("path")
	if rest == "" {
		rest = "/"
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + strings.TrimPrefix(rest, "/")
	rt.Static.ServeHTTP(w, r2)
}

// handlePluginConsole opens a plugin's admin console.
func (s *Server) handlePluginConsole(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		redirect(w, r, "/admin/login")
		return
	}
	uid := r.PathValue("uid")
	if s.plugins == nil {
		http.NotFound(w, r)
		return
	}
	rt, ok := s.plugins.RuntimeLookup(uid)
	if !ok {
		s.renderError(w, r, http.StatusNotFound, "插件未启用。")
		return
	}
	// Hand off to the plugin's own console page. The core never proxies
	// credentials, so the plugin authenticates its own operators.
	target := rt.Manifest.Runtime.BaseURL
	if rt.Manifest.Runtime.Type == "static" && rt.Manifest.Static != nil {
		target = "/assets/plugins/" + uid + "/" + strings.TrimPrefix(rt.Manifest.Static.Index, "/")
	}
	if target == "" {
		s.renderError(w, r, http.StatusBadRequest, "该插件没有控制台。")
		return
	}
	s.audit(r, "plugin.console", uid, "")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

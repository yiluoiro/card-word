package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

type shopAppearance struct{ Style, Accent, Eyebrow, Headline, CatalogTitle, CatalogDescription string }
type styleOption struct{ Key, Name, Description string }

var shopStyles = []styleOption{{"default", "经典商店", "与原版一致的礼品卡商城首页。"}, {"studio", "品牌工作室", "分栏首屏、品牌色背景与三列商品卡片。"}, {"minimal", "极简目录", "侧栏分类、搜索与紧凑商品列表，快速查找。"}, {"editorial", "独立杂志", "纸感底色、衬线大标题与错落编排的商品橱窗。"}}
var shopAccents = []styleOption{{"blue", "蓝色", ""}, {"violet", "紫色", ""}, {"emerald", "松石绿", ""}, {"amber", "暖金色", ""}}

const ctxAppearance ctxKey = "shop-appearance"

func (s *Server) appearance(r *http.Request) shopAppearance {
	if a, ok := r.Context().Value(ctxAppearance).(shopAppearance); ok {
		return a
	}
	a := shopAppearance{s.setting("shop.style", "default"), s.setting("shop.accent", "blue"), s.setting("shop.eyebrow", "数字商品"), s.setting("shop.headline", "欢迎选购"), s.setting("shop.catalog_title", "在售商品"), s.setting("shop.catalog_description", "选购前，请确认商品说明和使用限制。")}
	if !appearanceChoice(a.Style, shopStyles) {
		a.Style = "default"
	}
	if !appearanceChoice(a.Accent, shopAccents) {
		a.Accent = "blue"
	}
	return a
}
func appearanceChoice(value string, choices []styleOption) bool {
	for _, c := range choices {
		if c.Key == value {
			return true
		}
	}
	return false
}
func parseAppearance(r *http.Request) (shopAppearance, error) {
	a := shopAppearance{r.FormValue("style"), r.FormValue("accent"), strings.TrimSpace(r.FormValue("eyebrow")), strings.TrimSpace(r.FormValue("headline")), strings.TrimSpace(r.FormValue("catalog_title")), strings.TrimSpace(r.FormValue("catalog_description"))}
	if !appearanceChoice(a.Style, shopStyles) || !appearanceChoice(a.Accent, shopAccents) {
		return a, errors.New("请选择有效的店铺模板与主色。")
	}
	for _, field := range []struct {
		v string
		n int
	}{{a.Eyebrow, 40}, {a.Headline, 80}, {a.CatalogTitle, 40}, {a.CatalogDescription, 160}} {
		if field.v == "" || utf8.RuneCountInString(field.v) > field.n {
			return a, errors.New("请填写首页文案，并检查字段长度。")
		}
	}
	return a, nil
}
func (s *Server) handleAppearance(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "shop_appearance", map[string]any{"Title": "店铺装修", "Appearance": s.appearance(r), "Styles": shopStyles, "Accents": shopAccents})
}
func (s *Server) handleAppearanceSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, 400, "表单格式错误。")
		return
	}
	a, err := parseAppearance(r)
	if err != nil {
		s.renderStatus(w, r, 422, "shop_appearance", map[string]any{"Title": "店铺装修", "Appearance": a, "Styles": shopStyles, "Accents": shopAccents, "Error": err.Error()})
		return
	}
	if err := s.db.SetSettings(r.Context(), map[string]string{"shop.style": a.Style, "shop.accent": a.Accent, "shop.eyebrow": a.Eyebrow, "shop.headline": a.Headline, "shop.catalog_title": a.CatalogTitle, "shop.catalog_description": a.CatalogDescription}); err != nil {
		s.renderError(w, r, 500, "保存装修设置失败。")
		return
	}
	if err := s.reloadSettings(r.Context()); err != nil {
		s.renderError(w, r, 500, "设置已保存，但刷新失败，请重试。")
		return
	}
	s.audit(r, "shop.appearance.update", a.Style, "accent="+a.Accent)
	flashRedirect(w, r, "/admin/appearance", "装修已发布到店铺。", "")
}
func (s *Server) handleAppearancePreview(w http.ResponseWriter, r *http.Request) {
	a, err := parseAppearance(r)
	if err != nil {
		s.renderError(w, r, 422, err.Error())
		return
	}
	child := r.Clone(context.WithValue(r.Context(), ctxAppearance, a))
	child.URL.Path = "/"
	w.Header().Set("Cache-Control", "no-store")
	s.handleHome(w, child)
}

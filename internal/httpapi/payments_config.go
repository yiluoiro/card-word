package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
	"github.com/yiluoiro/card-word/internal/payment"
	"github.com/yiluoiro/card-word/internal/store"
)

type paymentField struct {
	Options                       []epayOption
	Key, Label, Hint, Value       string
	Secret, Multiline, Configured bool
}

type epayOption struct {
	Code, Label, Icon string
	Checked           bool
}

var epayOptions = []epayOption{{"alipay", "支付宝", "alipay", false}, {"wxpay", "微信支付", "wechat", false}, {"qqpay", "QQ 钱包", "qq", false}, {"usdt", "USDT", "usdt", false}, {"paypal", "PayPal", "paypal", false}, {"cashier", "网关收银台", "wallet", false}}

func selectedEpayMethods(raw []string) ([]string, error) {
	seen := map[string]bool{}
	for _, value := range raw {
		valid := false
		for _, o := range epayOptions {
			if value == o.Code {
				valid = true
				break
			}
		}
		if !valid {
			return nil, errors.New("不支持的易支付方式。")
		}
		seen[value] = true
	}
	var out []string
	for _, o := range epayOptions {
		if seen[o.Code] {
			out = append(out, o.Code)
		}
	}
	return out, nil
}
func storedEpayMethods(csv, legacy string) []string {
	if csv == "" {
		if legacy == "" {
			legacy = "cashier"
		}
		csv = legacy
	}
	methods, _ := selectedEpayMethods(strings.Split(csv, ","))
	return methods
}

type paymentConfigCard struct {
	Name, Title, Icon, Description string
	Enabled                        bool
	Fields                         []paymentField
}

func paymentCards() []paymentConfigCard {
	return []paymentConfigCard{
		{Name: "alipay", Title: "支付宝官方支付", Icon: "alipay", Description: "支持电脑网站支付与手机网站支付，使用 RSA2 公钥模式。", Fields: []paymentField{
			{Key: "appid", Label: "应用 APPID"}, {Key: "seller_id", Label: "收款账号 PID", Hint: "支付宝商户的 2088 开头账号 ID"}, {Key: "mode", Label: "支付产品", Hint: "page：电脑网站支付；wap：手机网站支付", Value: "page"},
			{Key: "private_key", Label: "应用私钥", Secret: true, Multiline: true}, {Key: "public_key", Label: "支付宝公钥", Multiline: true},
		}},
		{Name: "wechatpay", Title: "微信官方支付", Icon: "wechat", Description: "API v3 Native 扫码支付。请在微信商户平台开通 Native 支付并启用微信支付公钥模式。", Fields: []paymentField{
			{Key: "appid", Label: "应用 APPID"}, {Key: "mchid", Label: "微信商户号"}, {Key: "serial_no", Label: "商户 API 证书序列号"},
			{Key: "private_key", Label: "商户 API 私钥", Secret: true, Multiline: true}, {Key: "api_key", Label: "API v3 密钥", Secret: true},
			{Key: "public_key_id", Label: "微信支付公钥 ID", Hint: "PUB_KEY_ID_ 开头"}, {Key: "public_key", Label: "微信支付公钥", Multiline: true},
		}},
		{Name: "epay", Title: "易支付", Icon: "wallet", Description: "支持 Novapis V2 RSA 接口，同时兼容 V1 MD5。网关地址填写域名根地址，不附加接口路径。", Fields: []paymentField{
			{Key: "protocol", Label: "接口版本", Value: "v2"}, {Key: "gateway", Label: "网关地址", Value: "https://pay.novapis.com"}, {Key: "pid", Label: "商户 PID"}, {Key: "type", Label: "支付方式", Value: "alipay"},
			{Key: "private_key", Label: "V2 商户私钥", Secret: true, Multiline: true}, {Key: "public_key", Label: "V2 平台公钥", Multiline: true}, {Key: "key", Label: "V1 商户密钥", Secret: true},
		}},
	}
}
func paymentSecret(key string) bool {
	return strings.HasSuffix(key, ".private_key") || strings.HasSuffix(key, ".api_key") || key == "payment.epay.key"
}
func (s *Server) paymentValues(ctx context.Context) (map[string]string, error) {
	all, err := s.db.AllSettings(ctx)
	if err != nil {
		return nil, err
	}
	for k, v := range all {
		if paymentSecret(k) && strings.HasPrefix(v, "enc:v1:") {
			plain, e := cryptoutil.Open(s.cfg.MasterKey, strings.TrimPrefix(v, "enc:v1:"), "payment-setting:"+k)
			if e != nil {
				return nil, errors.New("支付密钥读取失败，请检查主密钥")
			}
			all[k] = string(plain)
		}
	}
	return all, nil
}
func configuredPayment(name string, v map[string]string) (payment.Driver, error) {
	g := func(k string) string { return v["payment."+name+"."+k] }
	switch name {
	case "alipay":
		return payment.NewAlipay(payment.AlipayConfig{AppID: g("appid"), SellerID: g("seller_id"), PrivateKey: g("private_key"), PublicKey: g("public_key"), Mode: g("mode")})
	case "wechatpay":
		return payment.NewWeChat(payment.WeChatConfig{AppID: g("appid"), MchID: g("mchid"), SerialNo: g("serial_no"), PrivateKey: g("private_key"), PublicKey: g("public_key"), APIKey: g("api_key"), PublicKeyID: g("public_key_id")})
	case "epay":
		if g("protocol") == "v2" {
			return payment.NewEpayV2(payment.EpayV2Config{GatewayURL: g("gateway"), PID: g("pid"), PayType: g("type"), PrivateKey: g("private_key"), PublicKey: g("public_key")})
		}
		return payment.NewEpay(payment.EpayConfig{GatewayURL: g("gateway"), PID: g("pid"), Key: g("key"), PayType: g("type")})
	}
	return nil, errors.New("未知支付方式")
}
func (s *Server) reloadBuiltinPayments(ctx context.Context) {
	values, err := s.paymentValues(ctx)
	if err != nil {
		s.log.Error("payment settings unavailable")
		for _, name := range []string{"alipay", "wechatpay", "epay"} {
			s.payments.Unregister(name)
		}
		return
	}
	for _, name := range []string{"alipay", "wechatpay", "epay"} {
		enabled := values["payment."+name+".enabled"] == "1"
		if name == "epay" && values["payment.epay.enabled"] == "" {
			enabled = values["payment.epay.gateway"] != "" && values["payment.epay.key"] != ""
		}
		driver, err := configuredPayment(name, values)
		if err != nil {
			s.payments.Unregister(name)
			if enabled {
				s.log.Warn("payment driver configuration invalid", "driver", name)
			}
			continue
		}
		s.payments.Register(driver)
	}
}
func (s *Server) handlePaymentsConfig(w http.ResponseWriter, r *http.Request) {

	values, err := s.db.AllSettings(r.Context())
	if err != nil {
		s.renderError(w, r, 500, "读取支付配置失败。")
		return
	}
	cards := paymentCards()
	for i := range cards {
		c := &cards[i]
		prefix := "payment." + c.Name + "."
		c.Enabled = values[prefix+"enabled"] == "1" || (c.Name == "epay" && values[prefix+"enabled"] == "" && values[prefix+"key"] != "")
		for j := range c.Fields {
			f := &c.Fields[j]
			if v, ok := values[prefix+f.Key]; ok {
				f.Value = v
			}
			if c.Name == "epay" && f.Key == "type" {
				legacy := f.Value
				if legacy == "" && values[prefix+"protocol"] != "v2" {
					legacy = "alipay"
				}
				selected := storedEpayMethods(values[prefix+"methods"], legacy)
				if csv, exists := values[prefix+"methods"]; exists && csv == "" {
					selected = nil
				}
				for _, option := range epayOptions {
					for _, code := range selected {
						if code == option.Code {
							option.Checked = true
						}
					}
					f.Options = append(f.Options, option)
				}
			}
			if c.Name == "epay" && f.Key == "protocol" && values[prefix+"protocol"] == "" && values[prefix+"key"] != "" {
				f.Value = "v1"
			}
			f.Configured = values[prefix+f.Key] != ""
			if f.Secret {
				f.Value = ""
			}
		}
	}
	s.render(w, r, "admin_payments", map[string]any{"Title": "支付配置", "PaymentCards": cards, "CallbackBase": s.cfg.BaseURL})
}
func (s *Server) handlePaymentsSave(w http.ResponseWriter, r *http.Request) {

	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, 400, "表单格式错误。")
		return
	}
	name := r.FormValue("provider")
	var card *paymentConfigCard
	for _, c := range paymentCards() {
		if c.Name == name {
			copy := c
			card = &copy
			break
		}
	}
	if card == nil {
		s.renderError(w, r, 400, "未知支付方式。")
		return
	}
	fail := func(message string) { flashRedirect(w, r, "/admin/payments", "", message) }
	values, err := s.paymentValues(r.Context())
	if err != nil {
		fail(err.Error())
		return
	}
	prefix := "payment." + name + "."
	updates := map[string]string{}
	for _, f := range card.Fields {
		if name == "epay" && f.Key == "type" {
			continue
		}
		key := prefix + f.Key
		v := strings.TrimSpace(r.FormValue(f.Key))
		if len(v) > 16384 {
			fail("配置内容过长。")
			return
		}
		if f.Secret && v == "" {
			v = values[key]
		}
		values[key] = v
		updates[key] = v
	}
	enabled := "0"
	if r.FormValue("enabled") == "1" {
		enabled = "1"
	}
	updates[prefix+"enabled"] = enabled
	if name == "epay" {
		if values[prefix+"protocol"] != "v1" && values[prefix+"protocol"] != "v2" {
			fail("请选择有效的易支付版本。")
			return
		}
		u, e := url.Parse(values[prefix+"gateway"])
		if enabled == "1" && (e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "") {
			fail("易支付网关必须为 HTTPS 地址。")
			return
		}
		raw := r.PostForm["type"]
		if len(raw) == 1 && raw[0] == "" {
			raw = []string{"cashier"}
		} // Previous single-select form.
		methods, e := selectedEpayMethods(raw)
		if e != nil {
			fail(e.Error())
			return
		}
		if enabled == "1" && len(methods) == 0 {
			fail("启用易支付前，请至少选择一种支付方式。")
			return
		}
		updates[prefix+"methods"] = strings.Join(methods, ",")
		// Keep the previous single-channel default intact for pre-upgrade pending orders.
		if _, exists := values[prefix+"type"]; !exists && len(methods) > 0 {
			legacy := methods[0]
			if legacy == "cashier" {
				legacy = ""
			}
			updates[prefix+"type"] = legacy
			values[prefix+"type"] = legacy
		}
	}
	if enabled == "1" {
		if _, err = configuredPayment(name, values); err != nil {
			fail(err.Error())
			return
		}
	}
	for key, v := range updates {
		if paymentSecret(key) && v != "" {
			sealed, e := cryptoutil.Seal(s.cfg.MasterKey, []byte(v), "payment-setting:"+key)
			if e != nil {
				fail("密钥保存失败。")
				return
			}
			updates[key] = "enc:v1:" + sealed
		}
	}
	// Enabling a provider also makes it available in checkout, retaining the existing selection.
	selected := values[store.SettingPaymentDriver]
	if enabled == "1" && selected != "all" {
		names := strings.Split(selected, ",")
		found := false
		for _, n := range names {
			if strings.TrimSpace(n) == name {
				found = true
			}
		}
		if !found {
			updates[store.SettingPaymentDriver] = strings.Trim(selected+","+name, ",")
		}
	}
	if err = s.db.SetSettings(r.Context(), updates); err != nil {
		fail("保存失败，请重试。")
		return
	}
	_ = s.reloadSettings(r.Context())
	s.reloadIntegrations(r.Context())
	s.audit(r, "payment.configure", name, "configuration updated")
	flashRedirect(w, r, "/admin/payments", "支付配置已保存。", "")
}

package security

import (
	"net/http"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/httpjson"
	"github.com/leeska/Lodestar/internal/module"
)

const base = "/api/v1/admin/security"

func fail(w http.ResponseWriter, err error) {
	if typed, ok := action.AsError(err); ok {
		httpjson.Error(w, typed.Status, typed.Message)
	} else {
		httpjson.Fail(w, err)
	}
}
func (m *Module) Routes(routes module.Routes) {
	routes.Admin("GET "+base+"/settings", func(w http.ResponseWriter, r *http.Request) {
		config, err := m.Settings(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, config)
	})
	routes.Admin("PUT "+base+"/settings", func(w http.ResponseWriter, r *http.Request) {
		var input Settings
		if !httpjson.Decode(w, r, &input) {
			return
		}
		if err := m.SaveSettings(r.Context(), input); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
	routes.Admin("GET "+base+"/accounts", func(w http.ResponseWriter, r *http.Request) {
		items, err := m.Accounts(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"accounts": items, "omitted_assets": !allows(r.Context(), "assets", action.Read)})
	})
	routes.Admin("PUT "+base+"/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		var input Profile
		if !httpjson.Decode(w, r, &input) {
			return
		}
		input.AssetID = r.PathValue("id")
		profile, err := m.SaveProfile(r.Context(), input)
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, profile)
	})
	routes.Admin("GET "+base+"/risks", func(w http.ResponseWriter, r *http.Request) {
		risks, err := m.Risks(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, risks)
	})
	routes.Admin("POST "+base+"/password-check", func(w http.ResponseWriter, r *http.Request) {
		count, err := m.CheckPasswords(r.Context(), true)
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]int{"checked": count})
	})
}
func (*Module) Actions() []action.Action {
	return []action.Action{
		action.R("security.accounts", action.Read, "GET", base+"/accounts", "账号安全清单", "复用账户资产；还需要 assets 读取权限，返回 2FA、恢复方式位置、最后人工检查及 HIBP 历史结果，不返回密码、恢复码或哈希。", nil),
		action.R("security.risks", action.Read, "GET", base+"/risks", "域名与证书风险", "分别需要 assets / certs 读取权限；未检查的 DNS 为 unknown。", nil),
		action.R("security.settings", action.Read, "GET", base+"/settings", "安全检查设置", "HIBP 和 DNS 对外查询默认关闭，登录异常提醒默认开启。", nil),
		action.R("security.update_account", action.Write, "PUT", base+"/accounts/{id}", "更新账号安全记录", "body.two_factor(enabled/disabled/unknown), recovery_location(仅位置), review_now, monitor_password。首次启用密码检查还需要 assets Danger 权限。", map[string]any{"body": action.Body("账户安全元数据")}),
		action.R("security.update_settings", action.Danger, "PUT", base+"/settings", "修改安全检查设置", "明确授权对外查询。启用 HIBP 还需 assets Danger 权限，启用 DNS 需 assets 读取权限；只检查逐个同意的账户。", map[string]any{"body": action.Body("password_checks, dns_checks, login_alerts 布尔值")}),
		action.R("security.check_passwords", action.Danger, "POST", base+"/password-check", "立即检查已选账号密码", "需先启用 HIBP、逐账户勾选并有 assets Danger 权限；仅发送 SHA-1 前五位到固定 HIBP 端点，不发原文、完整哈希或邮箱。", nil),
	}
}

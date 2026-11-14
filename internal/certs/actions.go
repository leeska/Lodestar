package certs

import "github.com/leeska/Lodestar/internal/action"

// Actions are the certificate capabilities.
func (m *Module) Actions() []action.Action {
	id := action.Str("证书 id，来自 certs.list")
	cert := action.Body("证书：domains（域名列表，可含 *.example.com）、dns_id（DNS 账号 id）、targets（下发目标：node_id、dir、reload 命令）；字段与 certs.list 返回的一致。修改时传完整对象。")
	dns := action.Body("DNS 账号：name、provider（目前 cloudflare）、token。读取时 token 不回显。")
	return []action.Action{
		action.R("certs.list", action.Read, "GET", "/api/v1/admin/certs", "证书列表", "列出证书：域名、状态、到期时间、下发目标、DNS 账号与是否用测试环境。", nil),
		action.R("certs.create", action.Write, "POST", "/api/v1/admin/certs", "新建证书", "新建证书并开始签发（DNS 验证）。", map[string]any{"body": cert}),
		action.R("certs.update", action.Write, "PUT", "/api/v1/admin/certs/{id}", "修改证书", "修改证书的域名或下发目标。", map[string]any{"id": id, "body": cert}),
		action.R("certs.delete", action.Danger, "DELETE", "/api/v1/admin/certs/{id}", "删除证书", "删除证书记录（节点上已下发的文件不删）。", map[string]any{"id": id}),
		action.R("certs.renew", action.Write, "POST", "/api/v1/admin/certs/{id}/renew", "立即续期", "立即重新签发证书。", map[string]any{"id": id}),
		action.R("certs.deploy", action.Write, "POST", "/api/v1/admin/certs/{id}/deploy", "立即下发", "把证书立即下发到所有目标节点并执行重载命令。", map[string]any{"id": id}),
		action.R("certs.create_dns", action.Write, "POST", "/api/v1/admin/certs/dns", "添加 DNS 账号", "添加签发用的 DNS 账号。", map[string]any{"body": dns}),
		action.R("certs.update_dns", action.Write, "PUT", "/api/v1/admin/certs/dns/{id}", "修改 DNS 账号", "修改 DNS 账号；token 留空表示不改。", map[string]any{"id": action.Str("DNS 账号 id"), "body": dns}),
		action.R("certs.delete_dns", action.Danger, "DELETE", "/api/v1/admin/certs/dns/{id}", "删除 DNS 账号", "删除 DNS 账号。", map[string]any{"id": action.Str("DNS 账号 id")}),
		action.R("certs.set_staging", action.Write, "PUT", "/api/v1/admin/certs/settings", "切换测试环境", "切换是否用 Let's Encrypt 测试环境签发。", map[string]any{"body": action.Body("{\"staging\": true 或 false}")}),
	}
}

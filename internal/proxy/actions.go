package proxy

import "github.com/leeska/Lodestar/internal/action"

const base = "/api/v1/admin/proxy"

// Actions are the proxy and forward capabilities for outside agents.
func (m *Module) Actions() []action.Action {
	id := action.Str("id，来自 proxy.overview")
	inbound := action.Body("入站：node_id、name、kernel（xray / sing-box）、protocol、port、enabled、address、user_ids（可用的使用者 id），以及 reality / ss 等协议参数；字段与 proxy.overview 返回的 inbounds[] 一致。修改时传完整对象。")
	forward := action.Body("端口转发：name、enabled、user_id、node_id（入口节点）、port、network（tcp / udp / both）、target、target_port，经另一台节点出去时填 exit；字段与 proxy.overview 返回的 forwards[] 一致。修改时传完整对象。")
	source := action.Body("外部订阅：name、url、enabled、prefix（节点名前缀）、user_ids（给哪些使用者）。")
	template := action.Body("规则模板：name、rules（模板正文）。")
	return []action.Action{
		action.R("proxy.overview", action.Read, "GET", base, "代理总览",
			"读取代理模块的全部内容：入站、端口转发、外部订阅、规则模板、默认模板、各节点内核与同步状态。", nil),
		action.R("proxy.create_inbound", action.Write, "POST", base+"/inbounds", "新建入站", "在节点上新建一个代理入站，保存后自动下发到节点。", map[string]any{"body": inbound}),
		action.R("proxy.update_inbound", action.Write, "PUT", base+"/inbounds/{id}", "修改入站", "修改入站，保存后自动下发到节点。", map[string]any{"id": id, "body": inbound}),
		action.R("proxy.delete_inbound", action.Danger, "DELETE", base+"/inbounds/{id}", "删除入站", "删除入站并从节点上撤掉，使用者通过它的连接会断开。", map[string]any{"id": id}),
		action.R("proxy.inbound_links", action.Read, "GET", base+"/inbounds/{id}/links", "入站分享链接", "读取入站给每个使用者生成的分享链接（含凭据，不要贴到公开场合）。", map[string]any{"id": id}),
		action.R("proxy.create_forward", action.Write, "POST", base+"/forwards", "新建端口转发", "新建端口转发，保存后自动下发。", map[string]any{"body": forward}),
		action.R("proxy.update_forward", action.Write, "PUT", base+"/forwards/{id}", "修改端口转发", "修改端口转发，保存后自动下发。", map[string]any{"id": id, "body": forward}),
		action.R("proxy.delete_forward", action.Danger, "DELETE", base+"/forwards/{id}", "删除端口转发", "删除端口转发，正在用的连接会断开。", map[string]any{"id": id}),
		action.R("proxy.create_source", action.Write, "POST", base+"/sources", "添加外部订阅", "添加一个外部订阅地址，节点会并入使用者的订阅。", map[string]any{"body": source}),
		action.R("proxy.update_source", action.Write, "PUT", base+"/sources/{id}", "修改外部订阅", "修改外部订阅。", map[string]any{"id": id, "body": source}),
		action.R("proxy.delete_source", action.Danger, "DELETE", base+"/sources/{id}", "删除外部订阅", "删除外部订阅。", map[string]any{"id": id}),
		action.R("proxy.refresh_source", action.Write, "POST", base+"/sources/{id}/refresh", "刷新外部订阅", "立即重新拉取外部订阅。", map[string]any{"id": id}),
		action.R("proxy.create_template", action.Write, "POST", base+"/templates", "新建规则模板", "新建订阅规则模板。", map[string]any{"body": template}),
		action.R("proxy.update_template", action.Write, "PUT", base+"/templates/{id}", "修改规则模板", "修改订阅规则模板（内置模板不能改）。", map[string]any{"id": id, "body": template}),
		action.R("proxy.delete_template", action.Danger, "DELETE", base+"/templates/{id}", "删除规则模板", "删除规则模板。", map[string]any{"id": id}),
		action.R("proxy.set_default_template", action.Write, "PUT", base+"/settings", "设默认规则模板", "设置订阅默认使用的规则模板。", map[string]any{"body": action.Body("{\"default_template\": 模板 id}")}),
		action.R("proxy.sync", action.Write, "POST", base+"/sync", "立即同步代理", "立即把代理配置重新下发到所有节点。", nil),
	}
}

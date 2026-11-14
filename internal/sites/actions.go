package sites

import "github.com/leeska/Lodestar/internal/action"

// Actions are the website capabilities.
func (m *Module) Actions() []action.Action {
	id := action.Str("网站 id，来自 sites.list")
	site := action.Body("网站：node_id、name、enabled、domains、kind（proxy 反代 / static 静态 / redirect 跳转）、upstream（反代目标）、websocket、root（静态目录）、redirect_to / redirect_code / keep_path、tls（none / auto / cert / custom）、cert_id、cert_path / key_path、force_https；字段与 sites.list 返回的一致。修改时传完整对象。")
	return []action.Action{
		action.R("sites.list", action.Read, "GET", "/api/v1/admin/sites", "网站列表", "列出面板管理的 nginx 网站与各节点的同步状态。", nil),
		action.R("sites.node", action.Read, "GET", "/api/v1/admin/sites/nodes/{node}", "节点上的 nginx 站点", "读取节点上 nginx 实际加载的全部站点（含不归面板管的）。", map[string]any{"node": action.Str("节点 id")}),
		action.R("sites.create", action.Write, "POST", "/api/v1/admin/sites", "新建网站", "新建网站，nginx -t 通过后才重载。", map[string]any{"body": site}),
		action.R("sites.update", action.Write, "PUT", "/api/v1/admin/sites/{id}", "修改网站", "修改网站，nginx -t 通过后才重载。", map[string]any{"id": id, "body": site}),
		action.R("sites.delete", action.Danger, "DELETE", "/api/v1/admin/sites/{id}", "删除网站", "删除网站并从节点的 nginx 里撤掉。", map[string]any{"id": id}),
		action.R("sites.sync", action.Write, "POST", "/api/v1/admin/sites/sync", "立即同步网站", "立即把网站配置重新下发到所有节点。", nil),
	}
}

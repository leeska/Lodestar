package users

import "github.com/leeska/Lodestar/internal/action"

// Actions are the capabilities for the people who use proxies and forwards.
func (m *Module) Actions() []action.Action {
	id := action.Str("使用者 id，来自 users.list")
	user := action.Body("使用者：name、note、enabled、traffic_limit（每周期流量额度，字节，0 不限）、reset_day（每月几号清零，1–28，0 不清零）、expires_at（RFC 3339 时间，空串不过期）；字段与 users.list 返回的一致。修改时传完整对象。")
	return []action.Action{
		action.R("users.list", action.Read, "GET", "/api/v1/admin/users", "使用者列表", "列出代理与转发的使用者：额度、已用流量、到期时间、是否启用、订阅地址。", nil),
		action.R("users.create", action.Write, "POST", "/api/v1/admin/users", "新建使用者", "新建一个使用者，会生成独立的订阅地址。", map[string]any{"body": user}),
		action.R("users.update", action.Write, "PUT", "/api/v1/admin/users/{id}", "修改使用者", "修改使用者的额度、到期时间或启用状态。", map[string]any{"id": id, "body": user}),
		action.R("users.delete", action.Danger, "DELETE", "/api/v1/admin/users/{id}", "删除使用者", "删除使用者，他的订阅立即失效。", map[string]any{"id": id}),
		action.R("users.rotate_token", action.Danger, "POST", "/api/v1/admin/users/{id}/token", "更换订阅地址", "给使用者换一个订阅地址，旧地址立即失效，他需要重新导入。", map[string]any{"id": id}),
		action.R("users.reset_traffic", action.Write, "POST", "/api/v1/admin/users/{id}/reset", "清零已用流量", "把使用者本周期的已用流量清零。", map[string]any{"id": id}),
	}
}

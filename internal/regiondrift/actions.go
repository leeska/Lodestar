package regiondrift

import "github.com/leeska/Lodestar/internal/action"

// Actions are the region drift capabilities.
func (m *Module) Actions() []action.Action {
	id := action.Str("id，来自 regiondrift.status")
	return []action.Action{
		action.R("regiondrift.status", action.Read, "GET", "/api/v1/admin/ipguard", "地区偏移检测", "读取地区偏移检测的任务、各节点的检测设置、最近结果与浏览器状态。", nil),
		action.R("regiondrift.create_task", action.Write, "POST", "/api/v1/admin/ipguard/tasks", "新建地区偏移任务", "新建地区偏移检测任务。", map[string]any{"body": action.Body("任务：字段与 regiondrift.status 返回的 tasks[] 一致。")}),
		action.R("regiondrift.update_task", action.Write, "PUT", "/api/v1/admin/ipguard/tasks/{id}", "修改地区偏移任务", "修改地区偏移检测任务。", map[string]any{"id": id, "body": action.Body("任务：字段与 regiondrift.status 返回的 tasks[] 一致。")}),
		action.R("regiondrift.delete_task", action.Danger, "DELETE", "/api/v1/admin/ipguard/tasks/{id}", "删除地区偏移任务", "删除地区偏移检测任务。", map[string]any{"id": id}),
		action.R("regiondrift.run_task", action.Write, "POST", "/api/v1/admin/ipguard/tasks/{id}/run", "立即检测", "立即执行一次地区偏移检测。", map[string]any{"id": id}),
	}
}

package httpapi

import "github.com/leeska/Lodestar/internal/action"

// coreActions are the capabilities of the core panel that the MCP package's
// tools do not already cover.
func coreActions() []action.Action {
	return []action.Action{
		action.R("overview.get", action.Read, "GET", "/api/v1/admin/overview", "总览",
			"读取总览：每个模块一张卡片（数值、状态 good / warn / bad、需要处理的条目）。想知道「现在有什么要处理」先看这个。", nil),
		action.R("audit.list", action.Read, "GET", "/api/v1/admin/audit", "操作记录",
			"读取审计日志：谁、从哪里、做了什么、结果。", map[string]any{
				"limit":  action.Int("条数，默认 50，最多 500"),
				"before": action.Int("只看这个 id 之前的（翻页）"),
				"node":   action.Str("只看这个节点的"),
			}, "limit", "before", "node"),
		action.R("tasks.health", action.Read, "GET", "/api/v1/admin/tasks/health", "任务健康", "读取每个探测任务最近是否正常执行。", nil),
		action.R("tasks.run_now", action.Write, "POST", "/api/v1/admin/tasks/{id}/run", "立即执行任务", "让探测任务立即执行一次。", map[string]any{"id": action.Str("任务 id，来自 tasks.list")}),
		action.R("agents.upgrade_status", action.Read, "GET", "/api/v1/admin/agents/upgrade", "Agent 升级状态", "读取可升级到的 Agent 版本与正在进行的升级。", nil),
		action.R("agents.upgrade", action.Write, "POST", "/api/v1/admin/agents/upgrade", "升级 Agent", "让这些节点的 Agent 升级到主控提供的版本。",
			map[string]any{"body": action.Body("{\"ids\": [节点 id]}")}),
		action.R("themes.list", action.Read, "GET", "/api/v1/admin/themes", "主题与附加包", "列出已安装的主题包与附加包。", nil),
	}
}

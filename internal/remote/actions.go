package remote

import "github.com/leeska/Lodestar/internal/action"

// Actions are the remote management capabilities.
func (m *Module) Actions() []action.Action {
	return []action.Action{
		action.R("remote.nodes", action.Read, "GET", "/api/v1/admin/remote", "可远程管理的节点", "列出开启了远程管理的节点、是否在线、具备哪些能力。", nil),
		action.R("remote.exec", action.Danger, "POST", "/api/v1/admin/remote/exec", "在节点上执行命令",
			"以 root 在节点上执行一条 shell 命令，返回输出与退出码。会写审计日志。只在用户明确要求时用，先把命令原样告诉用户。",
			map[string]any{"body": action.Body("{\"node_id\": 节点 id, \"command\": 命令, \"timeout_seconds\": 超时秒数（可选）}")}),
	}
}

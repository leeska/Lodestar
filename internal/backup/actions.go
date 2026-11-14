package backup

import "github.com/leeska/Lodestar/internal/action"

// Actions are the backup capabilities. Changing the plan is Danger: it can
// replace the passphrase.
func (m *Module) Actions() []action.Action {
	return []action.Action{
		action.R("backups.get", action.Read, "GET", "/api/v1/admin/backups", "备份计划与记录", "读取备份计划（时间、保留份数、存放节点、是否已设口令）与最近的备份记录。", nil),
		action.R("backups.update", action.Danger, "PUT", "/api/v1/admin/backups", "修改备份计划",
			"修改备份计划。passphrase 非空会替换备份口令（之前的备份仍只能用旧口令打开）；口令应由用户自己设置，不要替用户编造。",
			map[string]any{"body": action.Body("{\"enabled\": bool, \"hour\": UTC 小时 0–23, \"keep\": 每个节点保留份数, \"passphrase\": 新口令或空串, \"targets\": [{\"node_id\": 节点 id, \"dir\": 目录或空串}]}")}),
		action.R("backups.run", action.Write, "POST", "/api/v1/admin/backups/run", "立即备份", "立即做一次备份。", nil),
	}
}

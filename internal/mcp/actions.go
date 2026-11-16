package mcp

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strings"

	"github.com/leeska/Lodestar/internal/action"
)

// legacyActions names the tools this package has always had in the action
// registry's "<module>.<verb>" form; the old tool name stays as the alias.
// danger marks the ones that cannot simply be undone.
var legacyActions = map[string]struct {
	name   string
	danger bool
	title  string
}{
	"block_checks":         {name: "blocks.checks", title: "查看封锁检测"},
	"list_nodes":           {name: "nodes.list", title: "列出节点"},
	"list_tasks":           {name: "tasks.list", title: "列出任务"},
	"recent_events":        {name: "events.list", title: "最近事件"},
	"node_history":         {name: "nodes.history", title: "节点历史"},
	"node_network_history": {name: "nodes.network_history", title: "节点网络历史"},
	"node_observations":    {name: "nodes.observations", title: "节点观测"},
	"latency_samples":      {name: "nodes.latency_samples", title: "延迟样本"},
	"create_node":          {name: "nodes.create", title: "添加节点"},
	"update_node":          {name: "nodes.update", title: "修改节点"},
	"delete_node":          {name: "nodes.delete", danger: true, title: "删除节点"},
	"batch_update_nodes":   {name: "nodes.batch_update", title: "批量修改节点"},
	"rotate_node_token":    {name: "nodes.rotate_token", danger: true, title: "轮换节点令牌"},
	"correct_node_traffic": {name: "nodes.correct_traffic", title: "校正节点流量"},
	"rename_group":         {name: "nodes.rename_group", title: "重命名分组"},
	"create_task":          {name: "tasks.create", title: "新建探测任务"},
	"update_task":          {name: "tasks.update", title: "修改探测任务"},
	"delete_task":          {name: "tasks.delete", danger: true, title: "删除探测任务"},
	"control_task":         {name: "tasks.control", title: "启停探测任务"},
	"task_runs":            {name: "tasks.runs", title: "任务运行记录"},
	"list_catalog":         {name: "tasks.catalog", title: "探测目标目录"},
	"list_alert_rules":     {name: "alerts.list_rules", title: "列出告警规则"},
	"create_alert_rule":    {name: "alerts.create_rule", title: "新建告警规则"},
	"update_alert_rule":    {name: "alerts.update_rule", title: "修改告警规则"},
	"delete_alert_rule":    {name: "alerts.delete_rule", danger: true, title: "删除告警规则"},
	"get_settings":         {name: "settings.get", title: "查看面板设置"},
	"update_settings":      {name: "settings.update", title: "修改通知与面板设置"},
	"test_notification":    {name: "settings.test_notification", title: "发送测试通知"},
	"list_plugins":         {name: "plugins.list", title: "列出插件"},
}

// LegacyActions turns this package's tools into registry actions. Their
// handlers run the same code the tools always ran.
func (s *Server) LegacyActions() []action.Action {
	out := make([]action.Action, 0, len(tools))
	for _, t := range tools {
		def, ok := legacyActions[t.Name]
		if !ok {
			panic("mcp: tool without an action name: " + t.Name)
		}
		risk := action.Read
		if readOnly, _ := t.Annotations["readOnlyHint"].(bool); !readOnly {
			risk = action.Write
		}
		if def.danger {
			risk = action.Danger
		}
		// 面板、Skill 与确认卡片显示中文名；MCP 的 annotations.title 保持原样。
		title := def.title
		if title == "" {
			title, _ = t.Annotations["title"].(string)
		}
		name := t.Name
		out = append(out, action.Action{
			Name: def.name, Title: title, Description: t.Description, Input: t.InputSchema, Risk: risk, Alias: name,
			Handler: func(ctx context.Context, in json.RawMessage) (any, error) {
				res, rerr, _ := s.callTool(ctx, call{Name: name, Arguments: in})
				if rerr != nil {
					return nil, action.Invalid("%s", rerr.Message)
				}
				if isErr, _ := res["isError"].(bool); isErr {
					return nil, &action.Error{Status: http.StatusUnprocessableEntity, Message: resultText(res)}
				}
				return res["structuredContent"], nil
			},
		})
	}
	return out
}

func resultText(res map[string]any) string {
	parts, _ := res["content"].([]map[string]any)
	var b strings.Builder
	for _, p := range parts {
		if t, ok := p["text"].(string); ok {
			b.WriteString(t)
		}
	}
	return b.String()
}

// confirmProperty is added to every Danger tool's schema.
var confirmProperty = map[string]any{
	"type":        "boolean",
	"description": "高风险操作的确认。先向用户说明要做的事并得到同意，再传 true；不传时只返回将要做什么，不执行。",
}

// actionTools is tools/list built from the registry.
func (s *Server) actionTools(ctx context.Context) []tool {
	actions := s.Actions(ctx)
	out := make([]tool, 0, len(actions))
	for _, a := range actions {
		input := maps.Clone(a.Input)
		if a.Risk == action.Danger {
			props := map[string]any{}
			if p, ok := input["properties"].(map[string]any); ok {
				props = maps.Clone(p)
			}
			props["confirm"] = confirmProperty
			input["properties"] = props
		}
		desc := a.Description
		switch a.Risk {
		case action.Write:
			desc += "（会修改数据，立即生效）"
		case action.Danger:
			desc += "（高风险：删除或不可撤销，必须先得到用户同意并传 confirm: true）"
		}
		title := a.Title
		if title == "" {
			title = a.Name
		}
		out = append(out, tool{
			Name: a.ToolName(), Description: desc, InputSchema: input,
			Annotations: annotations(title, a.Risk == action.Read, a.Risk == action.Danger, a.Risk == action.Read),
		})
	}
	return out
}

// callAction is tools/call through the registry: the tool is looked up among
// the actions the caller may use, confirm is taken out of the arguments, and
// a refused or failing call comes back as a tool error the model can read.
func (s *Server) callAction(ctx context.Context, params call) (map[string]any, *rpcError, int) {
	if params.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call requires a tool name"}, http.StatusOK
	}
	var found *action.Action
	for _, a := range s.Actions(ctx) {
		if a.ToolName() == params.Name || a.Name == params.Name || (a.Alias != "" && a.Alias == params.Name) {
			found = &a
			break
		}
	}
	if found == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + params.Name}, http.StatusOK
	}
	in := params.Arguments
	confirm := false
	if found.Risk == action.Danger && len(in) > 0 {
		var args map[string]json.RawMessage
		if json.Unmarshal(in, &args) == nil {
			if raw, ok := args["confirm"]; ok {
				json.Unmarshal(raw, &confirm)
				delete(args, "confirm")
				in, _ = json.Marshal(args)
			}
		}
	}
	out, err := s.Run(ctx, found.Name, in, confirm)
	if err != nil {
		if e, ok := action.AsError(err); ok && e.Confirm != nil {
			detail, _ := json.MarshalIndent(e.Confirm, "", "  ")
			return toolError(e.Message + "\n" + string(detail)), nil, 0
		}
		return toolError(err.Error()), nil, 0
	}
	payload, ok := out.(map[string]any)
	if !ok {
		raw, err := json.Marshal(out)
		if err == nil && json.Unmarshal(raw, &payload) == nil && payload != nil {
			ok = true
		}
	}
	if !ok {
		payload = map[string]any{"result": out}
	}
	return toolResult(payload), nil, 0
}

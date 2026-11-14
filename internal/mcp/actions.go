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
}{
	"block_checks":         {name: "blocks.checks"},
	"list_nodes":           {name: "nodes.list"},
	"list_tasks":           {name: "tasks.list"},
	"recent_events":        {name: "events.list"},
	"node_history":         {name: "nodes.history"},
	"node_network_history": {name: "nodes.network_history"},
	"node_observations":    {name: "nodes.observations"},
	"latency_samples":      {name: "nodes.latency_samples"},
	"create_node":          {name: "nodes.create"},
	"update_node":          {name: "nodes.update"},
	"delete_node":          {name: "nodes.delete", danger: true},
	"batch_update_nodes":   {name: "nodes.batch_update"},
	"rotate_node_token":    {name: "nodes.rotate_token", danger: true},
	"correct_node_traffic": {name: "nodes.correct_traffic"},
	"rename_group":         {name: "nodes.rename_group"},
	"create_task":          {name: "tasks.create"},
	"update_task":          {name: "tasks.update"},
	"delete_task":          {name: "tasks.delete", danger: true},
	"control_task":         {name: "tasks.control"},
	"task_runs":            {name: "tasks.runs"},
	"list_catalog":         {name: "tasks.catalog"},
	"list_alert_rules":     {name: "alerts.list_rules"},
	"create_alert_rule":    {name: "alerts.create_rule"},
	"update_alert_rule":    {name: "alerts.update_rule"},
	"delete_alert_rule":    {name: "alerts.delete_rule", danger: true},
	"get_settings":         {name: "settings.get"},
	"update_settings":      {name: "settings.update"},
	"test_notification":    {name: "settings.test_notification"},
	"list_plugins":         {name: "plugins.list"},
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
		title, _ := t.Annotations["title"].(string)
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

package httpapi

// 「Agent 接入」：访问密钥管理、OpenAPI 文档和给外部 agent 下载的 Skill 包，都从能力注册表生成。

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/apikey"
	"github.com/leeska/Lodestar/internal/mcp"
	"github.com/leeska/Lodestar/internal/model"
)

// moduleNames are the Chinese names shown for each module.
var moduleNames = map[string]string{
	"overview": "总览", "audit": "操作记录", "events": "告警事件", "alerts": "告警规则", "nodes": "节点",
	"tasks": "探测任务", "blocks": "被墙检测", "settings": "设置与通知", "plugins": "插件", "themes": "主题",
	"agents": "Agent", "proxy": "代理与转发", "users": "使用者", "remote": "远程管理", "docker": "Docker",
	"certs": "证书", "sites": "网站", "backups": "备份", "regiondrift": "地区偏移",
}

func moduleName(m string) string {
	if n, ok := moduleNames[m]; ok {
		return n
	}
	return m
}

func (a *API) keyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/api-keys", a.admin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		keys := []apikey.Key{}
		if a.Keys != nil {
			var err error
			if keys, err = a.Keys.List(r.Context()); err != nil {
				fail(w, err)
				return
			}
		}
		modules := []map[string]string{}
		for _, m := range a.actions.Modules() {
			modules = append(modules, map[string]string{"id": m, "name": moduleName(m)})
		}
		base := mcp.BaseURL(r)
		write(w, 200, map[string]any{
			"keys": keys, "modules": modules, "base_url": base,
			"mcp_url": base + "/mcp", "openapi_url": base + "/api/v1/openapi.json",
			"actions": viewActions(a.actions.All()),
		})
	}))
	mux.HandleFunc("POST /api/v1/admin/api-keys", a.admin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if a.Keys == nil {
			write(w, 503, map[string]string{"error": "访问密钥不可用"})
			return
		}
		var spec apikey.Spec
		if !decode(w, r, &spec) {
			return
		}
		if err := apikey.Validate(spec, a.actions.Modules()); err != nil {
			actionError(w, err)
			return
		}
		if spec.ExpiresAt != 0 && spec.ExpiresAt <= time.Now().Unix() {
			write(w, 400, map[string]string{"error": "过期时间要在将来"})
			return
		}
		key, secret, err := a.Keys.Create(r.Context(), spec)
		if err != nil {
			fail(w, err)
			return
		}
		a.audit(r, model.AuditEntry{Action: "apikey.create", Detail: fmt.Sprintf("%s（%s）", key.Name, key.MaxRisk), Status: 200})
		write(w, 200, map[string]any{"key": key, "secret": secret})
	}))
	mux.HandleFunc("POST /api/v1/admin/api-keys/{id}/revoke", a.admin(func(w http.ResponseWriter, r *http.Request) {
		if a.Keys == nil {
			http.NotFound(w, r)
			return
		}
		respond(w, a.Keys.Revoke(r.Context(), r.PathValue("id")))
	}))
	mux.HandleFunc("DELETE /api/v1/admin/api-keys/{id}", a.admin(func(w http.ResponseWriter, r *http.Request) {
		if a.Keys == nil {
			http.NotFound(w, r)
			return
		}
		err := a.Keys.Delete(r.Context(), r.PathValue("id"))
		if errors.Is(err, model.ErrNotFound) {
			write(w, 409, map[string]string{"error": "只能删除已吊销的密钥"})
			return
		}
		respond(w, err)
	}))
	mux.HandleFunc("GET /api/v1/admin/agent-kit/skill.zip", a.admin(func(w http.ResponseWriter, r *http.Request) {
		data, err := skillZip(mcp.BaseURL(r), a.actions.All())
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="lodestar-skill.zip"`)
		w.Write(data)
	}))
}

// openAPI describes the action API for the given actions.
func openAPI(base string, actions []action.Action) map[string]any {
	errorSchema := map[string]any{"$ref": "#/components/schemas/Error"}
	errorResponse := func(description string) map[string]any {
		return map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": errorSchema}}}
	}
	paths := map[string]any{
		"/api/v1/actions": map[string]any{"get": map[string]any{
			"operationId": "list_actions", "summary": "列出这把密钥能用的能力",
			"responses": map[string]any{"200": map[string]any{"description": "能力清单"}, "401": errorResponse("密钥无效")},
		}},
	}
	for _, a := range actions {
		op := map[string]any{
			"operationId": a.ToolName(),
			"summary":     a.Title,
			"description": a.Description,
			"tags":        []string{moduleName(a.Module())},
			"x-risk":      a.Risk.String(),
			"requestBody": map[string]any{"required": false, "content": map[string]any{"application/json": map[string]any{"schema": a.Input}}},
			"responses": map[string]any{
				"200": map[string]any{"description": "执行结果", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{
					"type": "object", "properties": map[string]any{"result": map[string]any{}}}}}},
				"400": errorResponse("参数不对"),
				"401": errorResponse("密钥无效"),
				"403": errorResponse("这把密钥没有权限"),
				"428": errorResponse("高风险操作需要 confirm=true；error.confirm 说明将要做什么"),
			},
		}
		if a.Risk == action.Danger {
			op["parameters"] = []map[string]any{{"name": "confirm", "in": "query", "required": false,
				"description": "高风险操作的确认：先向用户说明并得到同意，再传 true", "schema": map[string]any{"type": "boolean"}}}
		}
		paths["/api/v1/actions/"+a.Name] = map[string]any{"post": op}
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{"title": "Lodestar", "version": model.Version,
			"description": "Lodestar 个人中枢的能力接口。每个能力是 POST /api/v1/actions/<name>，请求体是参数对象，结果在 result 里。"},
		"servers":  []map[string]any{{"url": base}},
		"security": []map[string]any{{"bearer": []string{}}},
		"components": map[string]any{
			"securitySchemes": map[string]any{"bearer": map[string]any{"type": "http", "scheme": "bearer", "description": "访问密钥 ls_…，在后台「Agent 接入」创建"}},
			"schemas": map[string]any{"Error": map[string]any{"type": "object", "properties": map[string]any{
				"error":   map[string]any{"type": "string"},
				"confirm": map[string]any{"type": "object", "description": "高风险操作未执行时，说明将要做什么"},
			}}},
		},
		"paths": paths,
	}
}

// skillZip is the Skill package for outside agents: SKILL.md, the full
// action reference and a small shell helper.
func skillZip(base string, actions []action.Action) ([]byte, error) {
	byModule := map[string][]action.Action{}
	modules := []string{}
	for _, a := range actions {
		if _, ok := byModule[a.Module()]; !ok {
			modules = append(modules, a.Module())
		}
		byModule[a.Module()] = append(byModule[a.Module()], a)
	}
	sort.Strings(modules)

	var skill strings.Builder
	fmt.Fprintf(&skill, `---
name: lodestar
description: Operate Lodestar, the user's self-hosted personal hub at %[1]s (servers and VPS nodes, costs and expiry, network probes, alerts, proxies and their users, port forwards, Docker, TLS certificates, nginx websites, backups, remote commands). Use whenever the user asks about their servers, nodes, VPS, alerts, proxy users or subscriptions, certificates, containers, websites or backups, or mentions Lodestar / 面板 / 节点 / 主控.
---

# Lodestar

Lodestar 是用户自己部署的个人中枢，地址 %[1]s。通过它的能力接口读写，不要去登录后台网页。

## 认证

访问密钥放在环境变量 `+"`LODESTAR_API_KEY`"+`（形如 `+"`ls_…`"+`），请求头 `+"`Authorization: Bearer $LODESTAR_API_KEY`"+`。
密钥有权限档（read / write / danger）和可用模块的限制，超出的调用会返回 403。不要把密钥、节点 token、订阅地址、分享链接贴进对话。

## 调用

- 列出能用的能力：`+"`GET %[1]s/api/v1/actions`"+`
- 执行：`+"`POST %[1]s/api/v1/actions/<能力名>`"+`，请求体是参数对象，结果在 `+"`result`"+` 里，出错时读 `+"`error`"+`。
- 也可以用 `+"`scripts/lodestar.sh <能力名> '<JSON 参数>'`"+`（需要 curl）。
- 支持 MCP 的客户端可以直接接 `+"`%[1]s/mcp`"+`（同一把密钥），工具名是能力名把点换成下划线。
- 完整参数见 `+"`reference/actions.md`"+`，OpenAPI 在 `+"`%[1]s/api/v1/openapi.json`"+`。

## 规则

1. 先读后写：不确定 id 时先用列表类能力查（nodes.list、tasks.list、proxy.overview、users.list……）。
2. 想知道「现在有什么要处理」，先调 overview.get。
3. write 级的能力立即生效：执行前用一句话告诉用户要改什么。
4. danger 级的能力（删除、换密钥或订阅地址、在节点上执行命令、改备份口令）：不带 confirm 调用只会返回 428 和将要做的事。
   把它原样说给用户，得到明确同意后再加 `+"`?confirm=true`"+`（MCP 里是参数 `+"`confirm: true`"+`）重新调用。用户没同意就不要加。
5. 备份口令、DNS token 这类密码由用户自己提供，不要替用户编造。

## 能力一览

`, base)
	for _, m := range modules {
		fmt.Fprintf(&skill, "### %s（%s）\n\n", moduleName(m), m)
		for _, a := range byModule[m] {
			fmt.Fprintf(&skill, "- `%s` [%s] %s\n", a.Name, a.Risk, a.Title)
		}
		skill.WriteString("\n")
	}

	var ref strings.Builder
	ref.WriteString("# Lodestar 能力参考\n\n每个能力：`POST /api/v1/actions/<名字>`，请求体是下面的参数对象。danger 级要加 `?confirm=true`（先得到用户同意）。\n\n")
	for _, m := range modules {
		fmt.Fprintf(&ref, "## %s（%s）\n\n", moduleName(m), m)
		for _, a := range byModule[m] {
			schema, _ := json.MarshalIndent(a.Input, "", "  ")
			fmt.Fprintf(&ref, "### %s — %s [%s]\n\n%s\n\n```json\n%s\n```\n\n", a.Name, a.Title, a.Risk, a.Description, schema)
		}
	}

	script := fmt.Sprintf(`#!/bin/sh
# Lodestar 能力接口的小工具。
#   lodestar.sh list                       列出能用的能力
#   lodestar.sh <能力名> ['<JSON 参数>']     执行，例如 lodestar.sh nodes.history '{"node_id":"…","hours":24}'
#   lodestar.sh <能力名> '<JSON>' --confirm  执行高风险能力（先得到用户同意）
set -eu
BASE="${LODESTAR_URL:-%s}"
[ -n "${LODESTAR_API_KEY:-}" ] || { echo "请先设置环境变量 LODESTAR_API_KEY" >&2; exit 2; }
[ $# -ge 1 ] || { sed -n '2,5p' "$0" >&2; exit 2; }
if [ "$1" = list ]; then
  exec curl -fsS -H "Authorization: Bearer $LODESTAR_API_KEY" "$BASE/api/v1/actions"
fi
NAME="$1"; BODY="${2:-}"; Q=""
[ -n "$BODY" ] || BODY='{}' 
[ "${3:-}" = "--confirm" ] && Q="?confirm=true"
curl -sS -X POST -H "Authorization: Bearer $LODESTAR_API_KEY" -H "Content-Type: application/json" \
  --data "$BODY" "$BASE/api/v1/actions/$NAME$Q"
echo
`, base)

	readme := fmt.Sprintf("# Lodestar Skill\n\n把整个 lodestar 目录放进 agent 的 skills 目录（Claude Code：`~/.claude/skills/lodestar/`），\n再把访问密钥设成环境变量 `LODESTAR_API_KEY`。\n\nMCP 方式（Claude Code）：\n\n```sh\nclaude mcp add --transport http lodestar %[1]s/mcp --header \"Authorization: Bearer $LODESTAR_API_KEY\"\n```\n\nCodex（~/.codex/config.toml）：\n\n```toml\n[mcp_servers.lodestar]\nurl = \"%[1]s/mcp\"\nbearer_token_env_var = \"LODESTAR_API_KEY\"\n```\n", base)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := []struct {
		name string
		mode uint32
		body string
	}{
		{"lodestar/SKILL.md", 0644, skill.String()},
		{"lodestar/reference/actions.md", 0644, ref.String()},
		{"lodestar/scripts/lodestar.sh", 0755, script},
		{"lodestar/README.md", 0644, readme},
	}
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: time.Now()}
		h.SetMode(0o644)
		if f.mode == 0755 {
			h.SetMode(0o755)
		}
		wr, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := wr.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

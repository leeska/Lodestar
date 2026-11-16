package automation

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/httpjson"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/timeline"
)

const base = "/api/v1/admin/automation"

var tokenPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func fail(w http.ResponseWriter, err error) {
	if typed, ok := action.AsError(err); ok {
		httpjson.Error(w, typed.Status, typed.Message)
	} else {
		httpjson.Fail(w, err)
	}
}
func (m *Module) Routes(routes module.Routes) {
	routes.Admin("GET "+base+"/rules", func(w http.ResponseWriter, r *http.Request) {
		rules, err := m.List(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"rules": rules})
	})
	routes.Admin("GET "+base+"/runs", func(w http.ResponseWriter, r *http.Request) {
		runs, err := m.Runs(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"runs": runs})
	})
	for _, method := range []string{"POST", "PUT"} {
		path := base + "/rules"
		if method == "PUT" {
			path += "/{id}"
		}
		routes.Admin(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			var rule Rule
			if !httpjson.Decode(w, r, &rule) {
				return
			}
			rule.ID = r.PathValue("id")
			saved, err := m.Save(r.Context(), rule)
			if err != nil {
				fail(w, err)
				return
			}
			httpjson.Write(w, 200, saved)
		})
	}
	routes.Admin("DELETE "+base+"/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.Delete(r.Context(), r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
	routes.Admin("POST "+base+"/rules/{id}/rotate", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		release, err := m.acquire(id)
		if err != nil {
			fail(w, err)
			return
		}
		defer release()
		rule, err := m.rule(r.Context(), id)
		if err != nil {
			fail(w, err)
			return
		}
		actor, _ := action.ActorFrom(r.Context())
		if _, err := m.target(actor, rule.Action); err != nil {
			fail(w, err)
			return
		}
		token := model.Secret()
		if _, err := m.DB.ExecContext(r.Context(), `UPDATE automation_rules SET hook_hash=? WHERE id=?`, hashToken(token), id); err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]string{"webhook_token": token})
	})
	routes.Admin("POST "+base+"/runs/{id}/details", func(w http.ResponseWriter, r *http.Request) {
		details, err := m.Details(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, details)
	})
	routes.Admin("POST "+base+"/runs/{id}/confirm", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Approve *bool `json:"approve"`
		}
		if !httpjson.Decode(w, r, &input) {
			return
		}
		if input.Approve == nil {
			httpjson.Error(w, 400, "必须明确提供 approve")
			return
		}
		if err := m.Confirm(r.Context(), r.PathValue("id"), *input.Approve); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
	routes.Public("POST /api/v1/hooks/{token}", func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		if !tokenPattern.MatchString(token) {
			httpjson.Error(w, 404, "链接不可用")
			return
		}
		var id string
		if err := m.DB.QueryRowContext(r.Context(), `SELECT id FROM automation_rules WHERE hook_hash=?`, hashToken(token)).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				httpjson.Error(w, 404, "链接不可用")
			} else {
				httpjson.Error(w, 503, "暂不可用")
			}
			return
		}
		release, err := m.acquire(id)
		if err != nil {
			httpjson.Error(w, 429, "请稍后重试")
			return
		}
		defer release()
		rule, err := m.rule(r.Context(), id)
		if err != nil || !rule.Enabled || rule.Trigger != "webhook" {
			httpjson.Error(w, 404, "链接不可用")
			return
		}
		if rule.LastAttempt > 0 && m.now().Unix()-rule.LastAttempt < int64(rule.Cooldown) {
			httpjson.Error(w, 429, "规则冷却中")
			return
		}
		var payload map[string]any
		if !httpjson.DecodeLimit(w, r, &payload, 32<<10) {
			return
		}
		if payload == nil {
			httpjson.Error(w, 400, "内容须为 JSON 对象")
			return
		}
		raw, _ := json.Marshal(payload)
		key := "webhook:" + model.Secret()[:24]
		if err := m.attempt(r.Context(), rule, key, timeline.Entry{Source: "webhook", Kind: "webhook", Level: "info", Title: "Webhook", At: m.now().Unix(), Data: raw}); err != nil {
			httpjson.Error(w, 503, "暂不可用")
			return
		}
		httpjson.Write(w, 202, map[string]any{"accepted": true})
	})
}
func (*Module) Actions() []action.Action {
	return []action.Action{
		action.R("automation.rules", action.Read, "GET", base+"/rules", "自动化规则", "只返回配置与参数是否存在，不返回加密参数或 Webhook token。", nil),
		action.R("automation.runs", action.Read, "GET", base+"/runs", "自动化执行记录", "状态、动作与触发引用；结果和参数需单独授权查看。", nil),
		action.R("automation.create", action.Write, "POST", base+"/rules", "创建自动化规则", "body: name, enabled, trigger(timeline/cron/webhook), cron, timezone, sources/kinds/levels, conditions[{field,op,value}], action, cooldown(秒≥60), set_args(JSON对象)。需要同时拥有目标能力权限。", map[string]any{"body": action.Body("规则配置；set_args 支持 {{source}} / {{data.field}}，整个值是模板时保留字段类型。")}),
		action.R("automation.update", action.Write, "PUT", base+"/rules/{id}", "修改自动化规则", "完整配置；省略 set_args 保留原参数。", map[string]any{"body": action.Body("规则配置")}),
		action.R("automation.delete", action.Danger, "DELETE", base+"/rules/{id}", "删除自动化规则", "删除规则并取消尚未处理的确认项，保留执行审计。", nil),
		action.R("automation.rotate_hook", action.Danger, "POST", base+"/rules/{id}/rotate", "轮换 Webhook 地址", "原地址立即失效，新 token 仅返回一次。", nil),
		action.R("automation.run_details", action.Danger, "POST", base+"/runs/{id}/details", "查看受保护执行详情", "参数和结果可能含秘密；还需要目标能力权限。", nil),
		action.R("automation.confirm", action.Danger, "POST", base+"/runs/{id}/confirm", "确认或拒绝自动化危险动作", "先读取 run_details 核对冻结参数；body.approve 必须为明确的 true / false。创建者权限与确认者目标能力权限均会检查，一次消费。", map[string]any{"body": action.Body("approve: true / false")}),
	}
}

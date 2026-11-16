package todo

import (
	"net/http"
	"strconv"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/httpjson"
	"github.com/leeska/Lodestar/internal/module"
)

const base = "/api/v1/admin/todo"

func fail(w http.ResponseWriter, err error) {
	if typed, ok := action.AsError(err); ok {
		httpjson.Error(w, typed.Status, typed.Message)
	} else {
		httpjson.Fail(w, err)
	}
}

func (m *Module) Routes(routes module.Routes) {
	routes.Admin("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		var before int64
		if raw := r.URL.Query().Get("before"); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value < 0 {
				httpjson.Error(w, 400, "截止时间须为 Unix 秒")
				return
			}
			before = value
		}
		list, err := m.List(r.Context(), r.URL.Query().Get("state"), before)
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"items": list})
	})
	routes.Admin("POST "+base+"/from-ref", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Ref string `json:"ref"`
		}
		if !httpjson.Decode(w, r, &input) {
			return
		}
		item, err := m.FromRef(r.Context(), input.Ref)
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, item)
	})
	for _, method := range []string{"POST", "PUT"} {
		path := base
		if method == "PUT" {
			path += "/{id}"
		}
		routes.Admin(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			var item Item
			if !httpjson.Decode(w, r, &item) {
				return
			}
			item.ID = r.PathValue("id")
			saved, err := m.Save(r.Context(), item)
			if err != nil {
				fail(w, err)
				return
			}
			httpjson.Write(w, 200, saved)
		})
	}
	routes.Admin("POST "+base+"/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Done *bool `json:"done"`
		}
		if !httpjson.Decode(w, r, &input) {
			return
		}
		done := input.Done == nil || *input.Done
		item, err := m.Complete(r.Context(), r.PathValue("id"), done)
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, item)
	})
	routes.Admin("DELETE "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.Delete(r.Context(), r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
}

func (*Module) Actions() []action.Action {
	id := action.Str("待办 id")
	body := action.Body("title（1–160 字）、note、due_at（Unix 秒，0 为不设）、priority（0–3，3 最高）、ref、source；修改时先读取并保留未改字段，完成状态由 complete 修改")
	return []action.Action{
		action.R("todo.list", action.Read, "GET", base, "待办清单", "列出待办，最多 500 项。", map[string]any{"state": action.Str("open / done / all，默认 open"), "before": action.Int("可选截止时间 Unix 秒")}, "state", "before"),
		action.R("todo.create", action.Write, "POST", base, "创建待办", "创建一个未完成的待办。", map[string]any{"body": body}),
		action.R("todo.update", action.Write, "PUT", base+"/{id}", "修改待办", "修改标题、时间、优先级与备注。", map[string]any{"id": id, "body": body}),
		action.R("todo.complete", action.Write, "POST", base+"/{id}/complete", "完成或重新打开待办", "body.done 默认为 true，false 为重新打开。", map[string]any{"id": id, "body": action.Body("done 布尔值")}),
		action.R("todo.delete", action.Danger, "DELETE", base+"/{id}", "删除待办", "永久删除待办，不能恢复。", map[string]any{"id": id}),
		action.R("todo.from_ref", action.Write, "POST", base+"/from-ref", "转成待办", "从资产、告警或情报创建；已有同来源的未完成待办时返回原项。", map[string]any{"body": action.Body("ref: asset:id / event:id / intel:id")}),
	}
}

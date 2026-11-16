package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/httpjson"
	"github.com/leeska/Lodestar/internal/module"
)

const base = "/api/v1/admin/assistant"

func (m *Module) Routes(routes module.Routes) {
	routes.Admin("GET "+base+"/settings", func(w http.ResponseWriter, r *http.Request) {
		config, err := m.Settings(r.Context())
		if err != nil {
			answer(w, err)
			return
		}
		httpjson.Write(w, 200, config)
	})
	routes.Admin("PUT "+base+"/settings", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Settings
			Key *string `json:"key"`
		}
		if !httpjson.Decode(w, r, &input) {
			return
		}
		if err := m.SaveSettings(r.Context(), input.Settings, input.Key); err != nil {
			answer(w, err)
			return
		}
		config, err := m.Settings(r.Context())
		if err != nil {
			answer(w, err)
			return
		}
		httpjson.Write(w, 200, config)
	})
	routes.Admin("GET "+base+"/conversations", func(w http.ResponseWriter, r *http.Request) {
		actor, err := m.actor(r.Context())
		if err != nil {
			answer(w, err)
			return
		}
		rows, err := m.DB.QueryContext(r.Context(), `SELECT id,title,created_at,updated_at FROM assistant_conversations WHERE owner=? ORDER BY updated_at DESC,id DESC LIMIT 100`, actor.Name)
		if err != nil {
			answer(w, err)
			return
		}
		defer rows.Close()
		list := []Conversation{}
		for rows.Next() {
			var conversation Conversation
			if err := rows.Scan(&conversation.ID, &conversation.Title, &conversation.CreatedAt, &conversation.UpdatedAt); err != nil {
				answer(w, err)
				return
			}
			list = append(list, conversation)
		}
		if err := rows.Err(); err != nil {
			answer(w, err)
			return
		}
		rows.Close()
		for index := range list {
			title, err := m.open(r.Context(), list[index].Title, prefix(list[index].ID)+"title")
			if err != nil {
				answer(w, err)
				return
			}
			list[index].Title = title
		}
		httpjson.Write(w, 200, map[string]any{"conversations": list})
	})
	routes.Admin("GET "+base+"/conversations/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, err := m.actor(r.Context())
		if err != nil {
			answer(w, err)
			return
		}
		conversation, err := m.conversation(r.Context(), r.PathValue("id"), actor.Name)
		if err != nil {
			answer(w, err)
			return
		}
		if run := m.live(conversation.ID, actor.Name); run != nil {
			conversation.Running, conversation.RunAfter = true, run.after
		}
		httpjson.Write(w, 200, conversation)
	})
	routes.Admin("GET "+base+"/conversations/{id}/live", func(w http.ResponseWriter, r *http.Request) {
		actor, err := m.actor(r.Context())
		if err != nil {
			answer(w, err)
			return
		}
		run := m.live(r.PathValue("id"), actor.Name)
		if run == nil {
			w.WriteHeader(204)
			return
		}
		follow(w, r, run)
	})
	routes.Admin("POST "+base+"/conversations/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		actor, err := m.actor(r.Context())
		if err != nil {
			answer(w, err)
			return
		}
		m.Stop(r.PathValue("id"), actor.Name)
		w.WriteHeader(204)
	})
	for _, method := range []string{"PATCH", "DELETE"} {
		routes.Admin(method+" "+base+"/conversations/{id}", func(w http.ResponseWriter, r *http.Request) {
			actor, err := m.actor(r.Context())
			if err != nil {
				answer(w, err)
				return
			}
			release, err := m.acquire(r.PathValue("id"))
			if err != nil {
				answer(w, err)
				return
			}
			defer release()
			conversation, err := m.conversation(r.Context(), r.PathValue("id"), actor.Name)
			if err != nil {
				answer(w, err)
				return
			}
			if r.Method == "DELETE" {
				_, err = m.DB.ExecContext(r.Context(), `DELETE FROM assistant_conversations WHERE id=? AND owner=?`, conversation.ID, actor.Name)
				if err == nil {
					err = m.Vault.DeletePrefix(r.Context(), prefix(conversation.ID))
				}
			} else {
				var input struct {
					Title string `json:"title"`
				}
				if !httpjson.Decode(w, r, &input) {
					return
				}
				input.Title = strings.TrimSpace(input.Title)
				if input.Title == "" || len(input.Title) > 200 {
					answer(w, action.Invalid("会话名称不能为空且不能超过 200 字节"))
					return
				}
				title, sealErr := m.seal(r.Context(), prefix(conversation.ID)+"title", input.Title)
				if sealErr != nil {
					answer(w, sealErr)
					return
				}
				_, err = m.DB.ExecContext(r.Context(), `UPDATE assistant_conversations SET title=? WHERE id=? AND owner=?`, title, conversation.ID, actor.Name)
			}
			if err != nil {
				answer(w, err)
				return
			}
			w.WriteHeader(204)
		})
	}
	routes.Admin("POST "+base+"/chat", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ID      string `json:"conversation_id"`
			Message string `json:"message"`
		}
		if !httpjson.Decode(w, r, &input) {
			return
		}
		id, fresh := input.ID, input.ID == ""
		if fresh {
			id = newID()
		}
		m.begin(w, r, id, func(ctx context.Context, emit Emit) error { return m.chat(ctx, id, fresh, input.Message, emit) })
	})
	routes.Admin("POST "+base+"/confirm", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ID      string `json:"conversation_id"`
			CallID  string `json:"call_id"`
			Approve *bool  `json:"approve"`
		}
		if !httpjson.Decode(w, r, &input) {
			return
		}
		if input.Approve == nil || input.CallID == "" || input.ID == "" {
			answer(w, action.Invalid("请明确选择执行或拒绝"))
			return
		}
		m.begin(w, r, input.ID, func(ctx context.Context, emit Emit) error {
			return m.Confirm(ctx, input.ID, input.CallID, *input.Approve, emit)
		})
	})
}

// begin starts a turn in the background and streams its events. The turn
// keeps running when the browser goes away.
func (m *Module) begin(w http.ResponseWriter, r *http.Request, id string, work func(context.Context, Emit) error) {
	actor, err := m.actor(r.Context())
	if err != nil {
		answer(w, err)
		return
	}
	if len(id) > 64 {
		answer(w, action.Invalid("会话 id 无效"))
		return
	}
	run, err := m.start(r.Context(), id, actor.Name, work)
	if err != nil {
		answer(w, err)
		return
	}
	follow(w, r, run)
}

// follow streams every event of run from its start until it ends or the
// client goes away; leaving does not stop the run.
func follow(w http.ResponseWriter, r *http.Request, run *liveRun) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	controller.SetWriteDeadline(time.Now().Add(runLimit + time.Minute))
	w.WriteHeader(200)
	if controller.Flush() != nil {
		return
	}
	index := 0
	for {
		events, done, changed := run.next(index)
		for _, event := range events {
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data); err != nil {
				return
			}
		}
		index += len(events)
		if len(events) > 0 && controller.Flush() != nil {
			return
		}
		if done {
			return
		}
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
	}
}

func answer(w http.ResponseWriter, err error) {
	status := 500
	if typed, ok := action.AsError(err); ok {
		status = typed.Status
	}
	httpjson.Error(w, status, safeError(err))
}

func (m *Module) Actions() []action.Action {
	return []action.Action{{
		Name: "assistant.ask", Title: "问助理", Description: "向助理问一句话。只可使用当前访问密钥允许的能力；高风险操作会暂停，须在后台由该调用者明确确认。不会扩大密钥权限。",
		Risk: action.Write, Input: action.Schema(map[string]any{"message": action.Str("问题或要求，最多 32 KB")}, "message"),
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var input struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return nil, action.Invalid("请求参数无效")
			}
			result := map[string]any{}
			err := m.Chat(ctx, "", input.Message, func(event string, value any) error {
				switch event {
				case "conversation":
					result["conversation_id"] = value.(map[string]string)["id"]
				case "text":
					text, _ := result["text"].(string)
					result["text"] = text + value.(map[string]string)["text"]
				case "confirm":
					result["pending"] = value
				}
				return nil
			})
			return result, err
		},
	}}
}

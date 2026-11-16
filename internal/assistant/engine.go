package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/vault"
)

type Emit func(string, any) error

func (m *Module) actor(ctx context.Context) (action.Actor, error) {
	actor, ok := action.ActorFrom(ctx)
	if !ok {
		return actor, &action.Error{Status: 401, Message: "需要已认证的调用者"}
	}
	if !strings.HasPrefix(actor.Name, "assistant:") {
		actor.Name = "assistant:" + actor.Name
	}
	return actor, nil
}

func (m *Module) Chat(ctx context.Context, id, text string, emit Emit) error {
	actor, err := m.actor(ctx)
	if err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > maxText {
		return action.Invalid("消息不能为空且不能超过 32 KB")
	}
	fresh := id == ""
	if fresh {
		id = newID()
	}
	release, err := m.acquire(id)
	if err != nil {
		return err
	}
	defer release()
	conversation, err := m.conversation(ctx, id, actor.Name)
	if typed, ok := action.AsError(err); ok && typed.Status == 404 && fresh {
		var exists int
		if err := m.DB.QueryRowContext(ctx, `SELECT count(*) FROM assistant_conversations WHERE id=?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return typed
		}
		now := time.Now().Unix()
		conversation = &Conversation{ID: id, Title: clipped(text, 80), CreatedAt: now, UpdatedAt: now, owner: actor.Name}
		title, err := m.seal(ctx, prefix(id)+"title", conversation.Title)
		if err != nil {
			return err
		}
		if _, err = m.DB.ExecContext(ctx, `INSERT INTO assistant_conversations(id,owner,title,created_at,updated_at) VALUES(?,?,?,?,?)`, id, actor.Name, title, now, now); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if conversation.Pending != nil {
		return &action.Error{Status: 409, Message: "请先处理这个会话中待确认的操作"}
	}
	conversation.state.Queue = nil
	conversation.state.Used = 0
	if err := m.repair(ctx, conversation); err != nil {
		return err
	}
	if err := m.append(ctx, conversation, Message{Role: "user", Content: text}); err != nil {
		return err
	}
	if err := emit("conversation", map[string]string{"id": id}); err != nil {
		return err
	}
	return m.run(action.WithActor(ctx, actor), conversation, emit)
}

func (m *Module) Confirm(ctx context.Context, id, callID string, approve bool, emit Emit) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	actor, err := m.actor(ctx)
	if err != nil {
		return err
	}
	release, err := m.acquire(id)
	if err != nil {
		return err
	}
	defer release()
	conversation, err := m.conversation(ctx, id, actor.Name)
	if err != nil {
		return err
	}
	if conversation.Pending == nil || conversation.CallID != callID {
		return &action.Error{Status: 409, Message: "这项确认已失效，请刷新会话"}
	}
	ctx = action.WithActor(ctx, actor)
	if err := m.repair(ctx, conversation); err != nil {
		return err
	}
	_, secret, err := m.gateway(ctx)
	if err != nil {
		return err
	}
	call := conversation.state.Queue[0]
	act, ok := m.registry.Get(call.Name)
	if !ok || !actor.Allows(act) {
		return &action.Error{Status: 403, Message: "当前调用者无权执行这项能力"}
	}
	conversation.state.Queue = conversation.state.Queue[1:]
	if err := m.saveState(ctx, conversation); err != nil {
		return err
	}
	var result any = map[string]string{"error": "用户拒绝了这项操作"}
	if approve {
		result, err = m.execute(ctx, call.Name, call.Arguments, true)
		if err != nil {
			result = map[string]string{"error": safeError(err)}
		}
	}
	if err := m.toolResult(ctx, conversation, call, result, secret, emit); err != nil {
		return err
	}
	if err := emit("conversation", map[string]string{"id": id}); err != nil {
		return err
	}
	return m.run(ctx, conversation, emit)
}

func (m *Module) gateway(ctx context.Context) (Settings, string, error) {
	config, err := m.Settings(ctx)
	if err != nil {
		return config, "", err
	}
	if config.URL == "" || config.Model == "" {
		return config, "", action.Invalid("请先在面板设置中配置助理网关和模型")
	}
	secret, err := m.Vault.Get(ctx, secretID)
	if errors.Is(err, vault.ErrNotFound) {
		err = nil
	}
	return config, secret, err
}

func (m *Module) run(ctx context.Context, conversation *Conversation, emit Emit) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	config, secret, err := m.gateway(ctx)
	if err != nil {
		return err
	}
	actor, _ := action.ActorFrom(ctx)
	for {
		for len(conversation.state.Queue) > 0 {
			call := conversation.state.Queue[0]
			act, exists := m.registry.Get(call.Name)
			if exists && actor.Allows(act) && act.Risk == action.Danger {
				confirmation := action.Confirmation{Action: act.Name, Title: act.Title, Description: act.Description, Input: call.Arguments}
				if err := emit("confirm", map[string]any{"conversation_id": conversation.ID, "call_id": call.ID, "confirmation": confirmation}); err != nil {
					return err
				}
				return nil
			}
			conversation.state.Queue = conversation.state.Queue[1:]
			if err := m.saveState(ctx, conversation); err != nil {
				return err
			}
			var result any
			if !exists || strings.HasPrefix(act.Name, "assistant.") {
				result = map[string]string{"error": "没有这项可用能力"}
			} else {
				result, err = m.execute(ctx, call.Name, call.Arguments, false)
				if err != nil {
					result = map[string]string{"error": safeError(err)}
				}
			}
			if err := m.toolResult(ctx, conversation, call, result, secret, emit); err != nil {
				return err
			}
		}
		reply, err := m.complete(ctx, config, secret, conversation.Messages, actor, conversation.state.Used, emit)
		if err != nil {
			return err
		}
		reply.Content = clipped(redact(reply.Content, secret), maxText)
		for index := range reply.Calls {
			call := &reply.Calls[index]
			call.ID = redact(call.ID, secret)
			call.Name = redact(call.Name, secret)
			if act, ok := m.registry.Get(call.Name); ok {
				call.Name = act.Name
			}
			call.Arguments = cleanJSON(json.RawMessage(call.Arguments), secret)
			if call.ID == "" {
				call.ID = newID()
			}
		}
		if len(reply.Calls) > maxCalls-conversation.state.Used {
			return action.Invalid("本轮工具调用已达到 10 次上限，请另发消息继续")
		}
		conversation.state.Queue = reply.Calls
		conversation.state.Used += len(reply.Calls)
		if err := m.append(ctx, conversation, reply); err != nil {
			return err
		}
		for _, call := range reply.Calls {
			if err := emit("tool_call", call); err != nil {
				return err
			}
		}
		if len(reply.Calls) == 0 {
			return emit("done", map[string]string{"conversation_id": conversation.ID})
		}
	}
}

func (m *Module) toolResult(ctx context.Context, conversation *Conversation, call Call, result any, secret string, emit Emit) error {
	text := clipped(string(cleanJSON(result, secret)), maxText)
	message := Message{Role: "tool", CallID: call.ID, Content: text}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := m.append(saveCtx, conversation, message); err != nil {
		return err
	}
	return emit("tool_result", map[string]string{"call_id": call.ID, "content": text})
}

func (m *Module) repair(ctx context.Context, conversation *Conversation) error {
	resolved := map[string]bool{}
	for _, message := range conversation.Messages {
		if message.Role == "tool" {
			resolved[message.CallID] = true
		}
	}
	for _, call := range conversation.state.Queue {
		resolved[call.ID] = true
	}
	for _, message := range append([]Message(nil), conversation.Messages...) {
		for _, call := range message.Calls {
			if !resolved[call.ID] {
				if err := m.append(ctx, conversation, Message{Role: "tool", CallID: call.ID, Content: `{"error":"上次执行被中断，结果未知，请先检查实际状态，不能假定成功或重试"}`}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func safeError(err error) string {
	if typed, ok := action.AsError(err); ok {
		return typed.Message
	}
	if errors.Is(err, context.Canceled) {
		return "请求已停止，已执行的操作不会回滚"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "助理响应超时，请稍后重试"
	}
	return "助理处理失败，请检查网关配置或服务状态"
}

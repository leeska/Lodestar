package assistant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/vault"
)

const secretID = "assistant:gateway:key"
const maxText = 32 << 10
const maxCalls = 10

type Settings struct {
	Protocol string `json:"protocol"`
	URL      string `json:"url"`
	Model    string `json:"model"`
	Prompt   string `json:"prompt"`
	HasKey   bool   `json:"has_key"`
}

type Call struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Message struct {
	ID      int64  `json:"id,omitempty"`
	Role    string `json:"role"`
	Content string `json:"content"`
	Calls   []Call `json:"calls,omitempty"`
	CallID  string `json:"call_id,omitempty"`
	At      int64  `json:"at,omitempty"`
}

type turnState struct {
	Queue []Call `json:"queue"`
	Used  int    `json:"used"`
}

type Conversation struct {
	ID        string               `json:"id"`
	Title     string               `json:"title"`
	CreatedAt int64                `json:"created_at"`
	UpdatedAt int64                `json:"updated_at"`
	Messages  []Message            `json:"messages,omitempty"`
	Pending   *action.Confirmation `json:"pending,omitempty"`
	CallID    string               `json:"call_id,omitempty"`
	// Running is true while a turn of this conversation runs in the
	// background; RunAfter is the last message saved before it started.
	Running  bool  `json:"running,omitempty"`
	RunAfter int64 `json:"run_after,omitempty"`
	state    turnState
	owner    string
}

type Module struct {
	DB       *sql.DB
	Vault    *vault.Vault
	Client   HTTPClient
	registry *action.Registry
	execute  func(context.Context, string, json.RawMessage, bool) (any, error)
	mu       sync.Mutex
	active   map[string]bool
	runs     map[string]*liveRun
}

func (*Module) ID() string { return "assistant" }

func (*Module) Migrations() []storage.Migration {
	return []storage.Migration{{Module: "assistant", Version: 1, Name: "assistant settings and conversations", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`CREATE TABLE assistant_settings(id INTEGER PRIMARY KEY CHECK(id=1), config TEXT NOT NULL);
CREATE TABLE assistant_conversations(id TEXT PRIMARY KEY, owner TEXT NOT NULL, title TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, state TEXT NOT NULL DEFAULT '{}');
CREATE INDEX assistant_owner ON assistant_conversations(owner, updated_at);
CREATE TABLE assistant_messages(id INTEGER PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES assistant_conversations(id) ON DELETE CASCADE, role TEXT NOT NULL, content TEXT NOT NULL, at INTEGER NOT NULL);
CREATE INDEX assistant_messages_conversation ON assistant_messages(conversation_id, id);`)
		return err
	}}, {Module: "assistant", Version: 2, Name: "encrypted conversation payloads", Up: func(*storage.MigrationContext) error { return nil }}}
}

func (m *Module) BindActions(registry *action.Registry, execute func(context.Context, string, json.RawMessage, bool) (any, error)) {
	m.registry, m.execute = registry, execute
}

func (m *Module) Settings(ctx context.Context) (Settings, error) {
	config := Settings{Protocol: "openai"}
	var raw string
	err := m.DB.QueryRowContext(ctx, `SELECT config FROM assistant_settings WHERE id=1`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return config, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			return config, err
		}
	}
	secrets, err := m.Vault.Has(ctx, secretID)
	config.HasKey = secrets[""]
	return config, err
}

func (m *Module) SaveSettings(ctx context.Context, config Settings, key *string) error {
	parsed, err := url.Parse(config.URL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return action.Invalid("网关地址须为不含账号、查询参数的 HTTP 或 HTTPS 地址")
	}
	if config.Protocol != "openai" && config.Protocol != "anthropic" {
		return action.Invalid("请选择 OpenAI 或 Anthropic 协议")
	}
	config.Model = strings.TrimSpace(config.Model)
	if config.Model == "" || len(config.Model) > 200 || len(config.Prompt) > 8000 || len(config.URL) > 2000 {
		return action.Invalid("请填写模型名；模型名、提示词或地址过长")
	}
	if key != nil && (len(*key) > 4096 || strings.ContainsAny(*key, "\r\n")) {
		return action.Invalid("网关密钥格式无效")
	}
	config.HasKey = false
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if key != nil {
		if err := m.Vault.Put(ctx, secretID, *key); err != nil {
			return err
		}
	}
	_, err = m.DB.ExecContext(ctx, `INSERT INTO assistant_settings(id,config) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config`, raw)
	return err
}

func (m *Module) acquire(id string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		m.active = map[string]bool{}
	}
	if m.active[id] {
		return nil, &action.Error{Status: 409, Message: "这个会话正在处理，请稍后再试"}
	}
	m.active[id] = true
	return func() { m.mu.Lock(); delete(m.active, id); m.mu.Unlock() }, nil
}

func (m *Module) conversation(ctx context.Context, id, owner string) (*Conversation, error) {
	conversation := &Conversation{owner: owner}
	var raw string
	err := m.DB.QueryRowContext(ctx, `SELECT id,title,created_at,updated_at,state FROM assistant_conversations WHERE id=? AND owner=?`, id, owner).
		Scan(&conversation.ID, &conversation.Title, &conversation.CreatedAt, &conversation.UpdatedAt, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &action.Error{Status: 404, Message: "会话不存在"}
	}
	if err != nil {
		return nil, err
	}
	conversation.Title, err = m.open(ctx, conversation.Title, prefix(id)+"title")
	if err != nil {
		return nil, err
	}
	raw, err = m.open(ctx, raw, prefix(id)+"state")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(raw), &conversation.state); err != nil {
		return nil, err
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT id,content,at FROM (SELECT id,content,at FROM assistant_messages WHERE conversation_id=? ORDER BY id DESC LIMIT 120) ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	type savedMessage struct {
		id      int64
		content string
		at      int64
	}
	saved := []savedMessage{}
	for rows.Next() {
		var item savedMessage
		if err := rows.Scan(&item.id, &item.content, &item.at); err != nil {
			rows.Close()
			return nil, err
		}
		saved = append(saved, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	conversation.Messages = []Message{}
	for _, item := range saved {
		raw, err := m.open(ctx, item.content, prefix(id)+"message:")
		if err != nil {
			return nil, err
		}
		var message Message
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			return nil, err
		}
		message.ID, message.At = item.id, item.at
		conversation.Messages = append(conversation.Messages, message)
	}
	if len(conversation.state.Queue) > 0 {
		call := conversation.state.Queue[0]
		if act, ok := m.registry.Get(call.Name); ok && act.NeedsConfirm() {
			conversation.Pending = &action.Confirmation{Action: act.Name, Title: act.Title, Description: act.Description, Input: call.Arguments}
			conversation.CallID = call.ID
		}
	}
	return conversation, nil
}

func (m *Module) append(ctx context.Context, conversation *Conversation, message Message) error {
	message.At = time.Now().Unix()
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	sealed, err := m.seal(ctx, prefix(conversation.ID)+"message:"+newID(), string(raw))
	if err != nil {
		return err
	}
	result, err := m.DB.ExecContext(ctx, `INSERT INTO assistant_messages(conversation_id,role,content,at) VALUES(?,?,?,?)`, conversation.ID, message.Role, sealed, message.At)
	if err != nil {
		return err
	}
	message.ID, err = result.LastInsertId()
	if err != nil {
		return err
	}
	conversation.Messages = append(conversation.Messages, message)
	return m.saveState(ctx, conversation)
}

func (m *Module) saveState(ctx context.Context, conversation *Conversation) error {
	raw, err := json.Marshal(conversation.state)
	if err != nil {
		return err
	}
	sealed, err := m.seal(ctx, prefix(conversation.ID)+"state", string(raw))
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `UPDATE assistant_conversations SET state=?,updated_at=? WHERE id=?`, sealed, time.Now().Unix(), conversation.ID)
	return err
}

func clipped(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "\n[内容已截断]"
}

func redact(value, secret string) string {
	if secret != "" {
		return strings.ReplaceAll(value, secret, "[密钥已隐藏]")
	}
	return value
}

func cleanJSON(value any, secret string) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"error":"结果无法编码"}`)
	}
	if secret != "" {
		encoded, _ := json.Marshal(secret)
		raw = []byte(strings.ReplaceAll(string(raw), string(encoded[1:len(encoded)-1]), "[密钥已隐藏]"))
	}
	return raw
}

func newID() string { return model.Secret()[:32] }

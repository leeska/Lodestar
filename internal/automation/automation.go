package automation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/timeline"
	"github.com/leeska/Lodestar/internal/vault"
	"github.com/robfig/cron/v3"
)

type Condition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

type Definition struct {
	Name       string      `json:"name"`
	Enabled    bool        `json:"enabled"`
	Trigger    string      `json:"trigger"`
	Cron       string      `json:"cron"`
	Timezone   string      `json:"timezone"`
	Sources    []string    `json:"sources"`
	Kinds      []string    `json:"kinds"`
	Levels     []string    `json:"levels"`
	Conditions []Condition `json:"conditions"`
	Action     string      `json:"action"`
	Cooldown   int         `json:"cooldown"`
}

type Rule struct {
	Definition
	ID           string           `json:"id"`
	SetArgs      *json.RawMessage `json:"set_args,omitempty"`
	HasArgs      bool             `json:"has_args"`
	WebhookToken string           `json:"webhook_token,omitempty"`
	LastAttempt  int64            `json:"last_attempt"`
	argsKey      string
	actor        action.Actor
	cursor       timeline.Cursor
}

type Module struct {
	DB       *sql.DB
	Vault    *vault.Vault
	Timeline *timeline.Module
	Events   interface {
		RecordEvent(context.Context, model.Event) error
		Audit(context.Context, model.AuditEntry) error
	}
	Now      func() time.Time
	registry *action.Registry
	execute  func(context.Context, string, json.RawMessage, bool) (any, error)
	resolve  func(context.Context, action.Actor) (action.Actor, error)
	edit     sync.Mutex
	mu       sync.Mutex
	busy     map[string]bool
}

func (*Module) ID() string { return "automation" }
func (*Module) Migrations() []storage.Migration {
	return []storage.Migration{{Module: "automation", Version: 1, Name: "rules and durable claims", Up: func(ctx *storage.MigrationContext) error {
		_, err := ctx.Tx.Exec(`CREATE TABLE automation_rules(id TEXT PRIMARY KEY,config TEXT NOT NULL,actor TEXT NOT NULL,args_key TEXT NOT NULL,hook_hash TEXT NOT NULL UNIQUE,last_attempt INTEGER NOT NULL DEFAULT 0,cursor TEXT NOT NULL);
		CREATE TABLE automation_runs(id TEXT PRIMARY KEY,rule_id TEXT NOT NULL,rule_name TEXT NOT NULL,trigger_key TEXT NOT NULL,action TEXT NOT NULL,actor TEXT NOT NULL,status TEXT NOT NULL,at INTEGER NOT NULL,finished_at INTEGER NOT NULL DEFAULT 0,UNIQUE(rule_id,trigger_key));
		CREATE INDEX automation_runs_at ON automation_runs(at);
		CREATE INDEX automation_runs_status ON automation_runs(status);`)
		return err
	}}}
}
func (m *Module) BindActions(registry *action.Registry, execute func(context.Context, string, json.RawMessage, bool) (any, error)) {
	m.registry, m.execute = registry, execute
}
func (m *Module) BindActorResolver(resolve func(context.Context, action.Actor) (action.Actor, error)) {
	m.resolve = resolve
}
func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
func (m *Module) acquire(id string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy == nil {
		m.busy = map[string]bool{}
	}
	if m.busy[id] {
		return nil, &action.Error{Status: 409, Message: "规则正在处理，请稍后重试"}
	}
	m.busy[id] = true
	return func() { m.mu.Lock(); delete(m.busy, id); m.mu.Unlock() }, nil
}
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (m *Module) rule(ctx context.Context, id string) (Rule, error) {
	var rule Rule
	var config, actor, cursor string
	err := m.DB.QueryRowContext(ctx, `SELECT id,config,actor,args_key,last_attempt,cursor FROM automation_rules WHERE id=?`, id).Scan(&rule.ID, &config, &actor, &rule.argsKey, &rule.LastAttempt, &cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return rule, &action.Error{Status: 404, Message: "没有这条规则"}
	}
	if err != nil {
		return rule, err
	}
	if err = json.Unmarshal([]byte(config), &rule.Definition); err != nil {
		return rule, err
	}
	if err = json.Unmarshal([]byte(actor), &rule.actor); err != nil {
		return rule, err
	}
	err = json.Unmarshal([]byte(cursor), &rule.cursor)
	rule.HasArgs = rule.argsKey != ""
	return rule, err
}
func (m *Module) List(ctx context.Context) ([]Rule, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id FROM automation_rules ORDER BY rowid DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Rule{}
	for _, id := range ids {
		rule, err := m.rule(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, nil
}
func schedule(rule Rule) (cron.Schedule, error) {
	if _, err := time.LoadLocation(rule.Timezone); err != nil {
		return nil, action.Invalid("时区不正确")
	}
	if len(strings.Fields(rule.Cron)) != 5 || len(rule.Cron) > 100 {
		return nil, action.Invalid("cron 必须是五段：分 时 日 月 周")
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	spec, err := parser.Parse("CRON_TZ=" + rule.Timezone + " " + rule.Cron)
	if err != nil {
		return nil, action.Invalid("cron 表达式不正确")
	}
	return spec, nil
}
func (m *Module) target(actor action.Actor, name string) (action.Action, error) {
	if m.registry == nil {
		return action.Action{}, action.Invalid("能力注册表尚未就绪")
	}
	target, ok := m.registry.Get(name)
	if !ok {
		return target, action.Invalid("没有这个能力")
	}
	if target.Module() == "automation" {
		return target, action.Invalid("自动化不能调用自身的管理能力")
	}
	if !actor.Allows(target) {
		return target, &action.Error{Status: 403, Message: "调用者没有目标能力权限"}
	}
	return target, nil
}
func (m *Module) Save(ctx context.Context, input Rule) (Rule, error) {
	actor, ok := action.ActorFrom(ctx)
	if !ok {
		return Rule{}, &action.Error{Status: 401, Message: "需要已认证的调用者"}
	}
	m.edit.Lock()
	defer m.edit.Unlock()
	fresh := input.ID == ""
	if fresh {
		input.ID = model.Secret()[:24]
	}
	release, err := m.acquire(input.ID)
	if err != nil {
		return Rule{}, err
	}
	defer release()
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 160 || len(input.Conditions) > 20 || len(input.Sources) > 30 || len(input.Kinds) > 30 || len(input.Levels) > 4 {
		return Rule{}, action.Invalid("规则名称或筛选条件超出限制")
	}
	if input.Cooldown == 0 {
		input.Cooldown = 60
	}
	if input.Cooldown < 60 || input.Cooldown > 604800 {
		return Rule{}, action.Invalid("冷却时间为 60–604800 秒")
	}
	if input.Timezone == "" {
		input.Timezone = "UTC"
	}
	switch input.Trigger {
	case "timeline", "webhook":
	case "cron":
		if _, err := schedule(input); err != nil {
			return Rule{}, err
		}
	default:
		return Rule{}, action.Invalid("触发方式为 timeline / cron / webhook")
	}
	for _, condition := range input.Conditions {
		if !validCondition(condition) {
			return Rule{}, action.Invalid("条件字段或比较方式不正确")
		}
	}
	target, err := m.target(actor, input.Action)
	if err != nil {
		return Rule{}, err
	}
	input.Action = target.Name
	if input.Trigger == "timeline" && !actor.Allows(action.Action{Name: "timeline.list", Risk: action.Read}) {
		return Rule{}, &action.Error{Status: 403, Message: "时间线触发还需要 timeline 读取权限"}
	}
	old := Rule{}
	if !fresh {
		old, err = m.rule(ctx, input.ID)
		if err != nil {
			return Rule{}, err
		}
		if _, err := m.target(actor, old.Action); err != nil {
			return Rule{}, err
		}
	} else {
		var count int
		if err := m.DB.QueryRowContext(ctx, `SELECT count(*) FROM automation_rules`).Scan(&count); err != nil {
			return Rule{}, err
		}
		if count >= 100 {
			return Rule{}, action.Invalid("最多保存 100 条规则")
		}
	}
	argsKey := old.argsKey
	newKey := ""
	if input.SetArgs != nil {
		if len(*input.SetArgs) > 32<<10 || !object(*input.SetArgs) {
			return Rule{}, action.Invalid("参数须为不超过 32 KB 的 JSON 对象")
		}
		newKey = "automation:args:" + model.Secret()
		if err := m.Vault.Put(ctx, newKey, string(*input.SetArgs)); err != nil {
			return Rule{}, err
		}
		argsKey = newKey
	}
	if argsKey == "" {
		return Rule{}, action.Invalid("首次保存需要 set_args")
	}
	cleanup := true
	defer func() {
		if cleanup && newKey != "" {
			_ = m.Vault.Delete(context.WithoutCancel(ctx), newKey)
		}
	}()
	cursor := old.cursor
	if fresh || old.Trigger != input.Trigger {
		cursor, err = m.Timeline.Watermark(ctx)
		if err != nil {
			return Rule{}, err
		}
	}
	config, _ := json.Marshal(input.Definition)
	policy, _ := json.Marshal(actor)
	watermark, _ := json.Marshal(cursor)
	token := ""
	if fresh {
		token = model.Secret()
		_, err = m.DB.ExecContext(ctx, `INSERT INTO automation_rules(id,config,actor,args_key,hook_hash,cursor) VALUES(?,?,?,?,?,?)`, input.ID, string(config), string(policy), argsKey, hashToken(token), string(watermark))
	} else {
		_, err = m.DB.ExecContext(ctx, `UPDATE automation_rules SET config=?,actor=?,args_key=?,cursor=? WHERE id=?`, string(config), string(policy), argsKey, string(watermark), input.ID)
	}
	if err != nil {
		return Rule{}, err
	}
	cleanup = false
	if newKey != "" && old.argsKey != "" {
		_ = m.Vault.Delete(context.WithoutCancel(ctx), old.argsKey)
	}
	saved, err := m.rule(ctx, input.ID)
	if saved.Trigger == "webhook" {
		saved.WebhookToken = token
	}
	return saved, err
}
func (m *Module) Delete(ctx context.Context, id string) error {
	m.edit.Lock()
	defer m.edit.Unlock()
	release, err := m.acquire(id)
	if err != nil {
		return err
	}
	defer release()
	rule, err := m.rule(ctx, id)
	if err != nil {
		return err
	}
	actor, ok := action.ActorFrom(ctx)
	if !ok {
		return &action.Error{Status: 401, Message: "需要已认证的调用者"}
	}
	if _, err := m.target(actor, rule.Action); err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `DELETE FROM automation_rules WHERE id=?`, id)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `UPDATE automation_runs SET status='cancelled',finished_at=? WHERE rule_id=? AND status='pending'`, m.now().Unix(), id)
	_ = m.Vault.Delete(context.WithoutCancel(ctx), rule.argsKey)
	return err
}
func (m *Module) Jobs() []module.Job {
	return []module.Job{{Name: "automation.tick", Title: "自动化触发检查", Every: time.Minute, Run: m.Tick}}
}
func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	var count, pending int
	if err := m.DB.QueryRowContext(ctx, `SELECT count(*) FROM automation_rules WHERE json_extract(config,'$.enabled')=1`).Scan(&count); err != nil {
		return nil, err
	}
	if err := m.DB.QueryRowContext(ctx, `SELECT count(*) FROM automation_runs WHERE status='pending'`).Scan(&pending); err != nil {
		return nil, err
	}
	tone := ""
	if pending > 0 {
		tone = "warn"
	}
	return []module.Card{{ID: "automation", Title: "自动化", Page: "automation", Value: strings.TrimSpace(fmtCount(count) + " 条规则"), Note: fmtCount(pending) + " 项待确认", Tone: tone}}, nil
}

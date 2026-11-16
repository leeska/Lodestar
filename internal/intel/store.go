package intel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/connector"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/timeline"
	"github.com/leeska/Lodestar/internal/vault"
)

type Source struct {
	ID           string          `json:"id"`
	Connector    string          `json:"connector"`
	Name         string          `json:"name"`
	Config       json.RawMessage `json:"config"`
	Enabled      bool            `json:"enabled"`
	EveryMinutes int             `json:"every_minutes"`
	LastFetch    int64           `json:"last_fetch"`
	LastError    string          `json:"last_error"`
	HasSecret    bool            `json:"has_secret"`
	SetSecret    *string         `json:"set_secret,omitempty"`
	cache        connector.Cache
}

type Item struct {
	ID          int64  `json:"id"`
	SourceID    string `json:"source_id"`
	SourceName  string `json:"source_name"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	At          int64  `json:"at"`
	AddedAt     int64  `json:"added_at"`
	Body        string `json:"body"`
	Read        bool   `json:"read"`
	Starred     bool   `json:"starred"`
	Disposition string `json:"disposition"`
}

type Rule struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Keywords []string `json:"keywords"`
	Sources  []string `json:"sources"`
	Action   string   `json:"action"`
}

type Query struct {
	Source  string
	Q       string
	Before  int64
	After   int64
	Unread  bool
	Starred bool
	Limit   int
}

type Module struct {
	DB       *sql.DB
	Vault    *vault.Vault
	Timeline *timeline.Module
	Events   interface {
		RecordEvent(context.Context, model.Event) error
	}
	Now    func() time.Time
	mu     sync.Mutex
	active map[string]bool
}

func (*Module) ID() string { return "intel" }

func (*Module) Migrations() []storage.Migration {
	return []storage.Migration{{Module: "intel", Version: 1, Name: "intelligence sources items and rules", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`CREATE TABLE intel_sources(id TEXT PRIMARY KEY,connector TEXT NOT NULL,name TEXT NOT NULL,config TEXT NOT NULL,enabled INTEGER NOT NULL,every_minutes INTEGER NOT NULL,last_fetch INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',cache TEXT NOT NULL DEFAULT '{}');
CREATE TABLE intel_items(id INTEGER PRIMARY KEY,source_id TEXT NOT NULL REFERENCES intel_sources(id) ON DELETE CASCADE,key TEXT NOT NULL,title TEXT NOT NULL,url TEXT NOT NULL,at INTEGER NOT NULL,added_at INTEGER NOT NULL,body TEXT NOT NULL,read INTEGER NOT NULL DEFAULT 0,starred INTEGER NOT NULL DEFAULT 0,disposition TEXT NOT NULL DEFAULT 'brief',UNIQUE(source_id,key));
CREATE INDEX intel_items_added ON intel_items(added_at,id);
CREATE TABLE intel_rules(id INTEGER PRIMARY KEY,name TEXT NOT NULL,keywords TEXT NOT NULL,sources TEXT NOT NULL,action TEXT NOT NULL);`)
		return err
	}}}
}

func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Module) Sources(ctx context.Context) ([]Source, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id,connector,name,config,enabled,every_minutes,last_fetch,last_error,cache FROM intel_sources ORDER BY last_fetch,id`)
	if err != nil {
		return nil, err
	}
	list := []Source{}
	for rows.Next() {
		var source Source
		var config, cache string
		if err := rows.Scan(&source.ID, &source.Connector, &source.Name, &config, &source.Enabled, &source.EveryMinutes, &source.LastFetch, &source.LastError, &cache); err != nil {
			rows.Close()
			return nil, err
		}
		source.Config = json.RawMessage(config)
		if err := json.Unmarshal([]byte(cache), &source.cache); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, source)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	secrets, err := m.Vault.Has(ctx, "intel:")
	if err != nil {
		return nil, err
	}
	for index := range list {
		list[index].HasSecret = secrets[list[index].ID+":token"]
	}
	return list, nil
}

func (m *Module) SaveSource(ctx context.Context, source Source) (Source, error) {
	source.Name = strings.TrimSpace(source.Name)
	if source.Name == "" || len([]rune(source.Name)) > 80 || source.EveryMinutes < 1 || source.EveryMinutes > 10080 {
		return source, action.Invalid("源名称须为 1–80 字，间隔须为 1–10080 分钟")
	}
	if _, ok := connector.Get(source.Connector); !ok {
		return source, action.Invalid("未知的连接器")
	}
	config, err := validateConfig(source.Connector, source.Config)
	if err != nil {
		return source, action.Invalid("%s", err.Error())
	}
	source.Config, err = json.Marshal(config)
	if err != nil {
		return source, err
	}
	if source.SetSecret != nil && (len(*source.SetSecret) > 4096 || strings.ContainsAny(*source.SetSecret, "\r\n")) {
		return source, action.Invalid("凭据格式无效")
	}
	if source.Connector != "github_release" && source.SetSecret != nil && *source.SetSecret != "" {
		return source, action.Invalid("只有 GitHub 源使用独立访问令牌")
	}
	if source.ID == "" {
		source.ID = model.Secret()[:12]
		release, ok := m.enter(source.ID)
		if !ok {
			return source, &action.Error{Status: 409, Message: "源正在处理，请重试"}
		}
		defer release()
		result, err := m.DB.ExecContext(ctx, `INSERT INTO intel_sources(id,connector,name,config,enabled,every_minutes) SELECT ?,?,?,?,?,? WHERE (SELECT count(*) FROM intel_sources)<100`, source.ID, source.Connector, source.Name, string(source.Config), source.Enabled, source.EveryMinutes)
		if err != nil {
			return source, err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return source, action.Invalid("最多保存 100 个源")
		}
	} else {
		release, ok := m.enter(source.ID)
		if !ok {
			return source, &action.Error{Status: 409, Message: "这个源正在抓取，请稍后再修改"}
		}
		defer release()
		old, err := m.source(ctx, source.ID)
		if err != nil {
			return source, err
		}
		changed := old.Connector != source.Connector || string(old.Config) != string(source.Config)
		cache, _ := json.Marshal(old.cache)
		last := old.LastFetch
		lastError := old.LastError
		if changed {
			cache = []byte("{}")
			last = 0
			lastError = ""
		}
		if source.SetSecret != nil || old.Enabled != source.Enabled {
			last = 0
			lastError = ""
		}
		result, err := m.DB.ExecContext(ctx, `UPDATE intel_sources SET connector=?,name=?,config=?,enabled=?,every_minutes=?,last_fetch=?,cache=?,last_error=? WHERE id=?`, source.Connector, source.Name, string(source.Config), source.Enabled, source.EveryMinutes, last, string(cache), lastError, source.ID)
		if err != nil {
			return source, err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return source, &action.Error{Status: 404, Message: "源不存在"}
		}
		if old.Connector != source.Connector && source.SetSecret == nil {
			if err := m.Vault.Delete(ctx, "intel:"+source.ID+":token"); err != nil {
				return source, err
			}
		}
	}
	if source.SetSecret != nil {
		if err := m.Vault.Put(ctx, "intel:"+source.ID+":token", *source.SetSecret); err != nil {
			return source, err
		}
	}
	source.SetSecret = nil
	all, err := m.Sources(ctx)
	for _, current := range all {
		if current.ID == source.ID {
			return current, err
		}
	}
	return source, err
}

func (m *Module) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id,name,keywords,sources,action FROM intel_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Rule{}
	for rows.Next() {
		var rule Rule
		var keywords, sources string
		if err := rows.Scan(&rule.ID, &rule.Name, &keywords, &sources, &rule.Action); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(keywords), &rule.Keywords); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(sources), &rule.Sources); err != nil {
			return nil, err
		}
		list = append(list, rule)
	}
	return list, rows.Err()
}

func (m *Module) SaveRule(ctx context.Context, rule Rule) (Rule, error) {
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" || len([]rune(rule.Name)) > 80 || len(rule.Keywords) > 20 || len(rule.Sources) > 100 || (rule.Action != "notify" && rule.Action != "brief" && rule.Action != "ignore") {
		return rule, action.Invalid("规则名称、关键词、源范围或动作无效")
	}
	for index, keyword := range rule.Keywords {
		rule.Keywords[index] = strings.TrimSpace(keyword)
		if rule.Keywords[index] == "" || len(keyword) > 300 {
			return rule, action.Invalid("关键词不能为空或过长")
		}
	}
	for _, id := range rule.Sources {
		var count int
		if err := m.DB.QueryRowContext(ctx, `SELECT count(*) FROM intel_sources WHERE id=?`, id).Scan(&count); err != nil {
			return rule, err
		}
		if count != 1 {
			return rule, action.Invalid("规则包含不存在的源")
		}
	}
	if rule.Keywords == nil {
		rule.Keywords = []string{}
	}
	if rule.Sources == nil {
		rule.Sources = []string{}
	}
	keywords, _ := json.Marshal(rule.Keywords)
	sources, _ := json.Marshal(rule.Sources)
	if rule.ID == 0 {
		result, err := m.DB.ExecContext(ctx, `INSERT INTO intel_rules(name,keywords,sources,action) VALUES(?,?,?,?)`, rule.Name, string(keywords), string(sources), rule.Action)
		if err != nil {
			return rule, err
		}
		rule.ID, err = result.LastInsertId()
		return rule, err
	}
	result, err := m.DB.ExecContext(ctx, `UPDATE intel_rules SET name=?,keywords=?,sources=?,action=? WHERE id=?`, rule.Name, string(keywords), string(sources), rule.Action, rule.ID)
	if err != nil {
		return rule, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return rule, &action.Error{Status: 404, Message: "规则不存在"}
	}
	return rule, nil
}

func (m *Module) Items(ctx context.Context, query Query) ([]Item, error) {
	if query.Limit <= 0 || query.Limit > 200 {
		query.Limit = 80
	}
	where := []string{"1=1"}
	args := []any{}
	if query.Source != "" {
		where = append(where, "i.source_id=?")
		args = append(args, query.Source)
	}
	if query.Q != "" {
		where = append(where, "(instr(lower(i.title),lower(?))>0 OR instr(lower(i.body),lower(?))>0)")
		args = append(args, query.Q, query.Q)
	}
	if query.Before > 0 {
		where = append(where, "i.id<?")
		args = append(args, query.Before)
	}
	if query.After > 0 {
		where = append(where, "i.added_at>=?")
		args = append(args, query.After)
	}
	if query.Unread {
		where = append(where, "i.read=0")
	}
	if query.Starred {
		where = append(where, "i.starred=1")
	}
	args = append(args, query.Limit)
	rows, err := m.DB.QueryContext(ctx, `SELECT i.id,i.source_id,s.name,i.title,i.url,i.at,i.added_at,i.body,i.read,i.starred,i.disposition FROM intel_items i JOIN intel_sources s ON s.id=i.source_id WHERE `+strings.Join(where, " AND ")+` ORDER BY i.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Item{}
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.SourceID, &item.SourceName, &item.Title, &item.URL, &item.At, &item.AddedAt, &item.Body, &item.Read, &item.Starred, &item.Disposition); err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	return list, rows.Err()
}

func disposition(source string, item connector.Item, rules []Rule) string {
	for _, rule := range rules {
		if len(rule.Sources) > 0 {
			matched := false
			for _, id := range rule.Sources {
				if id == source {
					matched = true
				}
			}
			if !matched {
				continue
			}
		}
		if len(rule.Keywords) > 0 {
			matched := false
			text := strings.ToLower(item.Title + "\n" + item.Body)
			for _, keyword := range rule.Keywords {
				if strings.Contains(text, strings.ToLower(keyword)) {
					matched = true
				}
			}
			if !matched {
				continue
			}
		}
		return rule.Action
	}
	return "brief"
}

func (m *Module) Brief(ctx context.Context, now time.Time) (string, string, error) {
	items, err := m.Items(ctx, Query{After: now.Add(-24 * time.Hour).Unix(), Limit: 200})
	if err != nil {
		return "intel", "", err
	}
	lines := []string{}
	for _, item := range items {
		if item.Disposition == "brief" {
			lines = append(lines, "- "+item.SourceName+"："+item.Title+" "+item.URL)
		}
		if len(lines) >= 30 {
			break
		}
	}
	return "intel", strings.Join(lines, "\n"), nil
}

func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	var total, enabled, failed int
	err := m.DB.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(enabled),0),coalesce(sum(CASE WHEN enabled=1 AND last_error<>'' THEN 1 ELSE 0 END),0) FROM intel_sources`).Scan(&total, &enabled, &failed)
	if err != nil {
		return nil, err
	}
	card := module.Card{ID: "intel", Title: "情报", Page: "intel", Value: fmt.Sprintf("%d 个源", total), Note: fmt.Sprintf("%d 个启用", enabled)}
	if failed > 0 {
		card.Tone = "warn"
		card.Note = fmt.Sprintf("%d 个源抓取失败", failed)
	}
	return []module.Card{card}, nil
}

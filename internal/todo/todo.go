package todo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
)

type Item struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Note      string `json:"note"`
	DueAt     int64  `json:"due_at"`
	DoneAt    int64  `json:"done_at"`
	Priority  int    `json:"priority"`
	Ref       string `json:"ref"`
	Source    string `json:"source"`
	CreatedAt int64  `json:"created_at"`
}

type Module struct {
	DB     *sql.DB
	Events interface {
		RecordEvent(context.Context, model.Event) error
	}
	Calendar interface {
		TodayText(context.Context, time.Time) (string, error)
	}
	Now func() time.Time
	mu  sync.Mutex
}

func (*Module) ID() string { return "todo" }

func (*Module) Migrations() []storage.Migration {
	return []storage.Migration{{Module: "todo", Version: 1, Name: "todos and reminder state", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`CREATE TABLE todos(id TEXT PRIMARY KEY,title TEXT NOT NULL,note TEXT NOT NULL DEFAULT '',due_at INTEGER NOT NULL DEFAULT 0,
done_at INTEGER NOT NULL DEFAULT 0,priority INTEGER NOT NULL DEFAULT 0,ref TEXT NOT NULL DEFAULT '',source TEXT NOT NULL DEFAULT 'manual',
created_at INTEGER NOT NULL,reminded_stage INTEGER NOT NULL DEFAULT 0);
CREATE INDEX todos_due ON todos(done_at,due_at); CREATE INDEX todos_ref ON todos(ref);`)
		return err
	}}}
}

func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

const columns = "id,title,note,due_at,done_at,priority,ref,source,created_at"

func scan(row interface{ Scan(...any) error }) (Item, error) {
	var item Item
	err := row.Scan(&item.ID, &item.Title, &item.Note, &item.DueAt, &item.DoneAt, &item.Priority, &item.Ref, &item.Source, &item.CreatedAt)
	return item, err
}

func (m *Module) List(ctx context.Context, state string, before int64) ([]Item, error) {
	if state != "" && state != "open" && state != "done" && state != "all" {
		return nil, action.Invalid("状态须为 open、done 或 all")
	}
	where, args := "1=1", []any{}
	if state == "" || state == "open" {
		where += " AND done_at=0"
	}
	if state == "done" {
		where += " AND done_at>0"
	}
	if before > 0 {
		where += " AND due_at>0 AND due_at<=?"
		args = append(args, before)
	}
	rows, err := m.DB.QueryContext(ctx, "SELECT "+columns+" FROM todos WHERE "+where+" ORDER BY CASE WHEN due_at=0 THEN 1 ELSE 0 END,due_at,priority DESC,created_at DESC,id LIMIT 500", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Item{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	return list, rows.Err()
}

func (m *Module) Get(ctx context.Context, id string) (Item, error) {
	item, err := scan(m.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM todos WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, &action.Error{Status: 404, Message: "待办不存在"}
	}
	return item, err
}

func (m *Module) Save(ctx context.Context, item Item) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.save(ctx, item)
}

func (m *Module) save(ctx context.Context, item Item) (Item, error) {
	item.Title = strings.TrimSpace(item.Title)
	if item.Title == "" || utf8.RuneCountInString(item.Title) > 160 {
		return item, action.Invalid("标题须为 1–160 个字")
	}
	if utf8.RuneCountInString(item.Note) > 4000 || len(item.Ref) > 200 {
		return item, action.Invalid("备注或来源引用太长")
	}
	if item.DueAt < 0 || item.DueAt > 253402300799 || item.Priority < 0 || item.Priority > 3 {
		return item, action.Invalid("截止时间或优先级无效（0–3）")
	}
	if item.Source == "" {
		item.Source = "manual"
	}
	if len(item.Source) > 40 {
		return item, action.Invalid("来源太长")
	}
	if item.ID == "" {
		item.ID, item.CreatedAt, item.DoneAt = model.Secret()[:16], m.now().Unix(), 0
		_, err := m.DB.ExecContext(ctx, `INSERT INTO todos(id,title,note,due_at,priority,ref,source,created_at) VALUES(?,?,?,?,?,?,?,?)`, item.ID, item.Title, item.Note, item.DueAt, item.Priority, item.Ref, item.Source, item.CreatedAt)
		return item, err
	}
	old, err := m.Get(ctx, item.ID)
	if err != nil {
		return item, err
	}
	_, err = m.DB.ExecContext(ctx, `UPDATE todos SET title=?,note=?,due_at=?,priority=?,ref=?,source=?,reminded_stage=CASE WHEN due_at<>? THEN 0 ELSE reminded_stage END WHERE id=?`,
		item.Title, item.Note, item.DueAt, item.Priority, item.Ref, item.Source, item.DueAt, item.ID)
	item.CreatedAt, item.DoneAt = old.CreatedAt, old.DoneAt
	return item, err
}

func (m *Module) Complete(ctx context.Context, id string, done bool) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.Get(ctx, id)
	if err != nil {
		return item, err
	}
	at := int64(0)
	if done {
		at = item.DoneAt
		if at == 0 {
			at = m.now().Unix()
		}
	}
	if _, err = m.DB.ExecContext(ctx, `UPDATE todos SET done_at=?,reminded_stage=CASE WHEN done_at>0 AND ?=0 THEN 0 ELSE reminded_stage END WHERE id=?`, at, at, id); err != nil {
		return item, err
	}
	item.DoneAt = at
	return item, nil
}

func (m *Module) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	result, err := m.DB.ExecContext(ctx, `DELETE FROM todos WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return &action.Error{Status: 404, Message: "待办不存在"}
	}
	return nil
}

func (m *Module) FromRef(ctx context.Context, ref string) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kind, id, ok := strings.Cut(ref, ":")
	if !ok || id == "" || len(ref) > 200 {
		return Item{}, action.Invalid("来源引用须为 asset:id、event:id 或 intel:id")
	}
	if kind != "asset" && kind != "event" && kind != "intel" {
		return Item{}, action.Invalid("不支持这种来源")
	}
	readModule := map[string]string{"asset": "assets", "event": "events", "intel": "intel"}[kind]
	if actor, ok := action.ActorFrom(ctx); ok && !actor.Allows(action.Action{Name: readModule + ".list", Risk: action.Read}) {
		return Item{}, &action.Error{Status: 403, Message: "访问密钥没有读取来源模块的权限"}
	}
	existing, err := scan(m.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM todos WHERE ref=? AND done_at=0 ORDER BY created_at DESC LIMIT 1", ref))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Item{}, err
	}
	item := Item{Ref: ref, Source: kind, Priority: 1}
	switch kind {
	case "asset":
		var expires string
		err = m.DB.QueryRowContext(ctx, `SELECT name,note,expires_on FROM assets WHERE id=?`, id).Scan(&item.Title, &item.Note, &expires)
		if expires != "" {
			if due, parseErr := time.ParseInLocation("2006-01-02", expires, m.now().Location()); parseErr == nil {
				item.DueAt = due.Add(9 * time.Hour).Unix()
			}
		}
		item.Title = "处理资产：" + item.Title
	case "event":
		err = m.DB.QueryRowContext(ctx, `SELECT node_name,message FROM events WHERE id=?`, id).Scan(&item.Title, &item.Note)
		item.Title = "处理提醒：" + item.Title
	case "intel":
		err = m.DB.QueryRowContext(ctx, `SELECT title,body FROM intel_items WHERE id=?`, id).Scan(&item.Title, &item.Note)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return item, &action.Error{Status: 404, Message: "来源对象不存在"}
	}
	if err != nil {
		return item, err
	}
	if title := []rune(item.Title); len(title) > 160 {
		item.Title = string(title[:160])
	}
	if note := []rune(item.Note); len(note) > 4000 {
		item.Note = string(note[:4000])
	}
	return m.save(ctx, item)
}

func reminderStage(due, now int64) int {
	switch {
	case due <= 0:
		return 0
	case due <= now:
		return 3
	case due <= now+3600:
		return 2
	case due <= now+86400:
		return 1
	default:
		return 0
	}
}

func (m *Module) Remind(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().Unix()
	rows, err := m.DB.QueryContext(ctx, "SELECT "+columns+` FROM todos WHERE done_at=0 AND due_at>0 AND
((reminded_stage<1 AND due_at<=?) OR (reminded_stage<2 AND due_at<=?) OR (reminded_stage<3 AND due_at<=?))
ORDER BY due_at,id LIMIT 200`, now+86400, now+3600, now)
	if err != nil {
		return err
	}
	list := []Item{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			rows.Close()
			return err
		}
		list = append(list, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range list {
		var old int
		if err := m.DB.QueryRowContext(ctx, `SELECT reminded_stage FROM todos WHERE id=?`, item.ID).Scan(&old); err != nil {
			return err
		}
		stage := reminderStage(item.DueAt, m.now().Unix())
		if stage <= old {
			continue
		}
		if m.Events == nil {
			continue
		}
		message := fmt.Sprintf("「%s」截止 %s", item.Title, time.Unix(item.DueAt, 0).In(m.now().Location()).Format("2006-01-02 15:04"))
		if err := m.Events.RecordEvent(ctx, model.Event{NodeID: "todo:" + item.ID, NodeName: item.Title, Kind: model.EventTodo, Active: true, Message: message}); err != nil {
			return err
		}
		if _, err := m.DB.ExecContext(ctx, `UPDATE todos SET reminded_stage=? WHERE id=?`, stage, item.ID); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) Jobs() []module.Job {
	return []module.Job{{Name: "todo.remind", Title: "待办到期提醒", Every: 5 * time.Minute, Run: m.Remind}}
}

func (m *Module) Brief(ctx context.Context, now time.Time) (string, string, error) {
	end := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	list, err := m.List(ctx, "open", end.Unix()-1)
	if err != nil {
		return "today", "", err
	}
	lines := []string{}
	if m.Calendar != nil {
		text, err := m.Calendar.TodayText(ctx, now)
		if err != nil {
			return "today", "", err
		}
		lines = append(lines, text)
	}
	empty := len(list) == 0
	for _, item := range list {
		if len(lines) >= 20 {
			break
		}
		lines = append(lines, fmt.Sprintf("待办：%s（%s）", item.Title, time.Unix(item.DueAt, 0).In(now.Location()).Format("01-02 15:04")))
	}
	if empty {
		lines = append(lines, "今天没有到期或逾期的待办")
	}
	return "today", strings.Join(lines, "\n"), nil
}

func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	var open, overdue int
	err := m.DB.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(CASE WHEN due_at>0 AND due_at<=? THEN 1 ELSE 0 END),0) FROM todos WHERE done_at=0`, m.now().Unix()).Scan(&open, &overdue)
	card := module.Card{ID: "todo", Title: "待办", Page: "todo", Value: fmt.Sprintf("%d 项未完成", open), Note: fmt.Sprintf("%d 项逾期", overdue)}
	if overdue > 0 {
		card.Tone = "warn"
	}
	return []module.Card{card}, err
}

// Package timeline is the one place where everything that happened can be
// read in order: alert events, audit entries, and the entries modules emit
// themselves (asset reminders, job failures, later intelligence and briefs).
//
// It does not copy events or audit entries: List merges the three tables in
// one query, so their history is there from day one and nothing is written
// twice. Modules write their own entries with Emit.
package timeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/httpjson"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
)

// Levels of an entry.
const (
	Info = "info"
	Good = "good"
	Warn = "warn"
	Bad  = "bad"
)

// RetentionDays bounds the module's own entries; events and audit keep their
// own retention.
const RetentionDays = 400

// Entry is one thing that happened.
type Entry struct {
	// ID is "<source>:<row id>", unique across the sources.
	ID string `json:"id"`
	At int64  `json:"at"`
	// Source is "event", "audit" or the module that emitted it ("assets", "jobs").
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Level  string `json:"level"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	// Ref names the object: a node id, "asset:<id>" and so on.
	Ref    string          `json:"ref,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
	Origin string          `json:"origin,omitempty"`
}

// Query selects entries newest first.
type Query struct {
	// Before is a Unix time: only entries older than it (0 = now).
	Before int64
	Limit  int
	// Sources limits the sources; empty is all.
	Sources []string
	Ref     string
}

// Module stores the module entries and answers the merged list.
type Module struct {
	DB *sql.DB
}

func (m *Module) ID() string { return "timeline" }

func (m *Module) Migrations() []storage.Migration {
	return []storage.Migration{{Module: "timeline", Version: 1, Name: "entries", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`CREATE TABLE timeline(id INTEGER PRIMARY KEY, at INTEGER NOT NULL, source TEXT NOT NULL, kind TEXT NOT NULL,
			level TEXT NOT NULL DEFAULT 'info', title TEXT NOT NULL, body TEXT NOT NULL DEFAULT '', ref TEXT NOT NULL DEFAULT '', data TEXT NOT NULL DEFAULT '');
			CREATE INDEX timeline_at ON timeline(at);
			CREATE INDEX timeline_ref ON timeline(ref, at);`)
		return err
	}}, {Module: "timeline", Version: 2, Name: "action origin", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`ALTER TABLE timeline ADD COLUMN origin TEXT NOT NULL DEFAULT ''`)
		return err
	}}}
}

// Emit records an entry (At defaults to now).
func (m *Module) Emit(ctx context.Context, e Entry) error {
	if e.At == 0 {
		e.At = time.Now().Unix()
	}
	if e.Level == "" {
		e.Level = Info
	}
	data := ""
	if len(e.Data) > 0 {
		data = string(e.Data)
	}
	_, err := m.DB.ExecContext(ctx, `INSERT INTO timeline(at, source, kind, level, title, body, ref, data, origin) VALUES(?,?,?,?,?,?,?,?,?)`,
		e.At, e.Source, e.Kind, e.Level, e.Title, e.Body, e.Ref, data, action.OriginFrom(ctx))
	return err
}

// Prune drops module entries older than RetentionDays.
func (m *Module) Prune(ctx context.Context) error {
	_, err := m.DB.ExecContext(ctx, `DELETE FROM timeline WHERE at < ?`, time.Now().AddDate(0, 0, -RetentionDays).Unix())
	return err
}

// eventKinds names the alert kinds for the merged list.
var eventKinds = map[string]string{
	"offline": "离线", "online": "恢复上线", "expiry": "即将到期", "traffic": "流量", "throttle": "降速",
	"cpu": "CPU", "memory": "内存", "disk": "磁盘", "renewal": "续费", "load": "负载",
	"cert": "证书", "backup": "备份", "asset": "资产到期", "intel": "情报更新",
	"todo": "待办提醒", "automation": "自动化待确认", "security": "安全提醒",
}

// List merges events, audit entries and module entries, newest first.
func (m *Module) List(ctx context.Context, q Query) ([]Entry, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	if q.Before <= 0 {
		q.Before = time.Now().Unix() + 1
	}
	want := func(source string) bool {
		if len(q.Sources) == 0 {
			return true
		}
		for _, s := range q.Sources {
			if s == source || (s == "modules" && source != "event" && source != "audit") {
				return true
			}
		}
		return false
	}
	parts, args := []string{}, []any{}
	if want("event") {
		where := "at < ?"
		args = append(args, q.Before)
		if q.Ref != "" {
			where += " AND node_id = ?"
			args = append(args, q.Ref)
		}
		parts = append(parts, `SELECT 'event:'||id, at, 'event', kind, CASE WHEN active THEN 'bad' ELSE 'good' END,
			node_name, message, node_id, '' FROM events WHERE `+where)
	}
	if want("audit") {
		where := "at < ?"
		args = append(args, q.Before)
		if q.Ref != "" {
			where += " AND node_id = ?"
			args = append(args, q.Ref)
		}
		parts = append(parts, `SELECT 'audit:'||id, at, 'audit', action, CASE WHEN status >= 400 THEN 'warn' ELSE 'info' END,
			actor, detail, node_id, '' FROM audit_log WHERE `+where)
	}
	modules := []string{}
	for _, s := range q.Sources {
		if s != "event" && s != "audit" && s != "modules" {
			modules = append(modules, s)
		}
	}
	if len(q.Sources) == 0 || want("modules") || len(modules) > 0 {
		where := "at < ?"
		args = append(args, q.Before)
		if len(modules) > 0 && !want("modules") {
			where += " AND source IN (" + strings.TrimSuffix(strings.Repeat("?,", len(modules)), ",") + ")"
			for _, s := range modules {
				args = append(args, s)
			}
		}
		if q.Ref != "" {
			where += " AND ref = ?"
			args = append(args, q.Ref)
		}
		parts = append(parts, `SELECT source||':'||id, at, source, kind, level, title, body, ref, data FROM timeline WHERE `+where)
	}
	if len(parts) == 0 {
		return []Entry{}, nil
	}
	args = append(args, q.Limit)
	rows, err := m.DB.QueryContext(ctx, strings.Join(parts, " UNION ALL ")+" ORDER BY 2 DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var data string
		if err := rows.Scan(&e.ID, &e.At, &e.Source, &e.Kind, &e.Level, &e.Title, &e.Body, &e.Ref, &data); err != nil {
			return nil, err
		}
		if data != "" {
			e.Data = json.RawMessage(data)
		}
		switch e.Source {
		case "event":
			// 事件：标题是「节点名 · 类型」，内容是告警消息。
			if k, ok := eventKinds[e.Kind]; ok {
				e.Title = fmt.Sprintf("%s · %s", e.Title, k)
			}
		case "audit":
			// 审计：标题是动作，内容写操作者。
			who := e.Title
			e.Title = e.Kind
			if e.Body != "" {
				e.Body = who + " · " + e.Body
			} else {
				e.Body = who
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func parseSources(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// Routes: GET /api/v1/admin/timeline?before=&limit=&sources=a,b&ref=
func (m *Module) Routes(r module.Routes) {
	r.Admin("GET /api/v1/admin/timeline", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		limit, _ := strconv.Atoi(q.Get("limit"))
		list, err := m.List(req.Context(), Query{Before: before, Limit: limit, Sources: parseSources(q.Get("sources")), Ref: q.Get("ref")})
		if err != nil {
			httpjson.Error(w, 500, err.Error())
			return
		}
		httpjson.Write(w, 200, map[string]any{"entries": list})
	})
}

// Actions: reading the timeline.
func (m *Module) Actions() []action.Action {
	return []action.Action{
		action.R("timeline.list", action.Read, "GET", "/api/v1/admin/timeline", "时间线",
			"按时间倒序读取发生过的一切：告警事件（event）、操作记录（audit）和各模块写的条目（资产到期提醒、定时任务失败等）。想知道「最近发生了什么」用这个。",
			map[string]any{
				"before":  action.Int("只看这个 Unix 时间之前的（翻页用上一页最后一条的 at）"),
				"limit":   action.Int("条数，默认 100，最多 500"),
				"sources": action.Str("只看这些来源，逗号分隔：event、audit、modules（全部模块条目）或模块名如 assets"),
				"ref":     action.Str("只看和这个对象有关的：节点 id 或 asset:<资产 id>"),
			}, "before", "limit", "sources", "ref"),
	}
}

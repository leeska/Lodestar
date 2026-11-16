package brief

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/assets"
	"github.com/leeska/Lodestar/internal/jobs"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/notify"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/timeline"
)

var Sections = []string{"overview", "upcoming", "events", "nodes", "jobs", "intel", "today"}

type Settings struct {
	Enabled  bool     `json:"enabled"`
	Time     string   `json:"time"`
	Timezone string   `json:"timezone"`
	Sections []string `json:"sections"`
	UseModel bool     `json:"use_model"`
}

type Document struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	ModelUsed bool   `json:"model_used"`
}

type Extra interface {
	Brief(context.Context, time.Time) (string, string, error)
}

type Module struct {
	DB       *sql.DB
	Assets   *assets.Module
	Timeline *timeline.Module
	JobsMod  *jobs.Module
	Nodes    interface {
		Nodes(context.Context, time.Time) ([]model.NodeView, error)
	}
	Channels notify.Channels
	Sender   interface {
		SendSummary(context.Context, notify.Config, notify.Summary) error
	}
	Rewrite func(context.Context, string) (string, error)
	Extras  []Extra
	Now     func() time.Time
	cards   func(context.Context) []module.Card
	mu      sync.Mutex
}

func (*Module) ID() string { return "brief" }

func (*Module) Migrations() []storage.Migration {
	return []storage.Migration{{Module: "brief", Version: 1, Name: "brief settings and delivery history", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`CREATE TABLE brief_settings(id INTEGER PRIMARY KEY CHECK(id=1),config TEXT NOT NULL);
CREATE TABLE brief_runs(id TEXT PRIMARY KEY,day TEXT NOT NULL,at INTEGER NOT NULL,status TEXT NOT NULL,body TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '');`)
		return err
	}}}
}

func (m *Module) BindOverview(cards func(context.Context) []module.Card) { m.cards = cards }

func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Module) Settings(ctx context.Context) (Settings, error) {
	config := Settings{Time: "08:00", Timezone: time.Local.String(), Sections: []string{"overview", "upcoming", "events", "nodes", "jobs", "intel", "today"}, UseModel: true}
	var raw string
	err := m.DB.QueryRowContext(ctx, `SELECT config FROM brief_settings WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	err = json.Unmarshal([]byte(raw), &config)
	return config, err
}

func (m *Module) Save(ctx context.Context, config Settings) error {
	if _, err := time.Parse("15:04", config.Time); err != nil || len(config.Time) != 5 {
		return action.Invalid("简报时间须为 HH:MM")
	}
	if _, err := time.LoadLocation(config.Timezone); err != nil {
		return action.Invalid("时区无效，请填写 Asia/Shanghai 等 IANA 时区")
	}
	if len(config.Sections) == 0 || len(config.Sections) > len(Sections) {
		return action.Invalid("请至少选择一个简报部分")
	}
	for _, section := range config.Sections {
		if !slices.Contains(Sections, section) {
			return action.Invalid("未知的简报部分")
		}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `INSERT INTO brief_settings(id,config) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config`, string(raw))
	return err
}

func (m *Module) Preview(ctx context.Context) (Document, error) {
	config, err := m.Settings(ctx)
	if err != nil {
		return Document{}, err
	}
	zone, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return Document{}, action.Invalid("简报时区无效")
	}
	now := m.now().In(zone)
	document := Document{Title: now.Format("2006-01-02") + " 每日简报"}
	var body strings.Builder
	body.WriteString(document.Title + "\n")
	add := func(title string, lines []string) {
		if len(lines) > 0 {
			body.WriteString("\n" + title + "\n" + strings.Join(lines, "\n") + "\n")
		}
	}
	want := func(section string) bool { return slices.Contains(config.Sections, section) }
	if want("overview") && m.cards != nil {
		lines := []string{}
		for _, card := range m.cards(ctx) {
			if card.Tone == "warn" || card.Tone == "bad" {
				lines = append(lines, "- "+card.Title+"："+card.Value+"；"+card.Note)
				for _, item := range card.Items {
					if item.Tone == "warn" || item.Tone == "bad" {
						lines = append(lines, "  "+item.Label+"："+item.Note)
					}
				}
			}
		}
		if len(lines) == 0 {
			lines = append(lines, "没有需要处理的总览警告")
		}
		add("需要处理", lines)
	}
	if want("upcoming") && m.Assets != nil {
		view := *m.Assets
		view.Now = func() time.Time { return now }
		items, err := view.Upcoming(ctx, 30)
		if err != nil {
			return document, err
		}
		lines := []string{}
		for _, item := range items[:min(len(items), 40)] {
			lines = append(lines, fmt.Sprintf("- %s：%s，到期剩余 %d 天", item.Name, item.ExpiresOn, item.DaysLeft))
		}
		if len(lines) == 0 {
			lines = append(lines, "30 天内没有到期项目")
		}
		add("即将到期", lines)
	}
	if want("events") && m.Timeline != nil {
		entries, err := m.Timeline.List(ctx, timeline.Query{Limit: 500, Before: now.Unix() + 1})
		if err != nil {
			return document, err
		}
		lines := []string{}
		for _, entry := range entries {
			if entry.At >= now.Add(-24*time.Hour).Unix() && (entry.Level == timeline.Bad || entry.Level == timeline.Warn) && entry.Source != "brief" {
				lines = append(lines, "- "+entry.Title+"："+entry.Body)
			}
			if len(lines) >= 30 {
				break
			}
		}
		if len(lines) == 0 {
			lines = append(lines, "过去 24 小时没有警告或故障记录")
		}
		add("最近事件", lines)
	}
	if want("nodes") && m.Nodes != nil {
		nodes, err := m.Nodes.Nodes(ctx, now)
		if err != nil {
			return document, err
		}
		online := 0
		for _, node := range nodes {
			if node.Online {
				online++
			}
		}
		add("节点", []string{fmt.Sprintf("%d / %d 台在线", online, len(nodes))})
	}
	if want("jobs") && m.JobsMod != nil {
		states, err := m.JobsMod.States(ctx)
		if err != nil {
			return document, err
		}
		lines := []string{}
		for _, state := range states {
			if state.LastError != "" {
				lines = append(lines, "- "+state.Title+"："+state.LastError)
			}
		}
		if len(lines) == 0 {
			lines = append(lines, "没有失败的定时任务")
		}
		add("后台任务", lines)
	}
	for _, extra := range m.Extras {
		key, text, err := extra.Brief(ctx, now)
		if err != nil {
			add("补充信息", []string{"部分数据读取失败"})
		} else if want(key) && text != "" {
			add(map[string]string{"intel": "关注的情报", "today": "今天"}[key], []string{text})
		}
	}
	document.Body = truncate(body.String(), 24000)
	if config.UseModel && m.Rewrite != nil {
		rewriteCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		text, err := m.Rewrite(rewriteCtx, document.Body)
		cancel()
		if err == nil && strings.TrimSpace(text) != "" {
			document.Body, document.ModelUsed = truncate(text, 24000), true
		}
	}
	return document, nil
}

func (m *Module) Send(ctx context.Context, scheduled bool) (Document, error) {
	if !m.mu.TryLock() {
		return Document{}, &action.Error{Status: 409, Message: "简报正在生成或发送"}
	}
	defer m.mu.Unlock()
	config, err := m.Settings(ctx)
	if err != nil {
		return Document{}, err
	}
	zone, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return Document{}, action.Invalid("简报时区无效")
	}
	now := m.now().In(zone)
	if scheduled && (!config.Enabled || now.Format("15:04") < config.Time) {
		return Document{}, nil
	}
	channel, err := m.Channels.NotificationConfig(ctx)
	if err != nil {
		return Document{}, err
	}
	if channel.Channel == "" || channel.Channel == "none" {
		return Document{}, action.Invalid("请先在告警与通知中配置通知出口")
	}
	id, day := "manual:"+model.Secret()[:16], now.Format("2006-01-02")
	if scheduled {
		id = "daily:" + day
	}
	claim, err := m.DB.ExecContext(ctx, `INSERT OR IGNORE INTO brief_runs(id,day,at,status) VALUES(?,?,?,'running')`, id, day, now.Unix())
	if err != nil {
		return Document{}, err
	}
	count, err := claim.RowsAffected()
	if err != nil || count == 0 {
		return Document{}, err
	}
	document, err := m.Preview(ctx)
	if err == nil && m.Timeline != nil {
		err = m.Timeline.Emit(ctx, timeline.Entry{At: now.Unix(), Source: "brief", Kind: "brief.daily", Level: timeline.Info, Title: document.Title, Body: document.Body, Ref: "brief:" + id})
	}
	if err == nil {
		err = m.Sender.SendSummary(ctx, channel, notify.Summary{Title: document.Title, Body: document.Body, At: now.Unix()})
	}
	status, message := "sent", ""
	if err != nil {
		status, message = "failed", "简报生成或投递失败，请检查通知出口与后台任务"
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, saveErr := m.DB.ExecContext(saveCtx, `UPDATE brief_runs SET status=?,body=?,last_error=? WHERE id=?`, status, document.Body, message, id); saveErr != nil {
		return document, saveErr
	}
	if err != nil {
		return document, errors.New(message)
	}
	return document, nil
}

func (m *Module) Jobs() []module.Job {
	return []module.Job{{Name: "brief.daily", Title: "每日简报", Every: time.Hour, Run: func(ctx context.Context) error { _, err := m.Send(ctx, true); return err }}}
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "\n[内容已截断]"
	}
	return text
}

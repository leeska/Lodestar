package calendar

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/connector"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/safefetch"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/vault"
)

type Source struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Timezone     string  `json:"timezone"`
	Enabled      bool    `json:"enabled"`
	EveryMinutes int     `json:"every_minutes"`
	LastFetch    int64   `json:"last_fetch"`
	LastError    string  `json:"last_error"`
	HasURL       bool    `json:"has_url"`
	SetURL       *string `json:"set_url,omitempty"`
	cache        connector.Cache
}

type Event struct {
	ID         string `json:"id"`
	SourceID   string `json:"source_id"`
	SourceName string `json:"source_name"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	StartAt    int64  `json:"start_at"`
	EndAt      int64  `json:"end_at"`
	AllDay     bool   `json:"all_day"`
	Location   string `json:"location"`
}

type Module struct {
	DB      *sql.DB
	Vault   *vault.Vault
	Now     func() time.Time
	Fetcher connector.Connector
	mu      sync.Mutex
	active  map[string]bool
}

func (*Module) ID() string { return "calendar" }
func (*Module) Migrations() []storage.Migration {
	return []storage.Migration{{Module: "calendar", Version: 1, Name: "private calendar sources and read-only events", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`CREATE TABLE calendar_sources(id TEXT PRIMARY KEY,name TEXT NOT NULL,timezone TEXT NOT NULL,enabled INTEGER NOT NULL,
every_minutes INTEGER NOT NULL,last_fetch INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',cache TEXT NOT NULL DEFAULT '{}');
CREATE TABLE calendar_events(id TEXT PRIMARY KEY,source_id TEXT NOT NULL REFERENCES calendar_sources(id) ON DELETE CASCADE,
title TEXT NOT NULL,body TEXT NOT NULL,start_at INTEGER NOT NULL,end_at INTEGER NOT NULL,all_day INTEGER NOT NULL,location TEXT NOT NULL);
CREATE INDEX calendar_range ON calendar_events(start_at,end_at);`)
		return err
	}}}
}
func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
func (m *Module) enter(id string) (func(), bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		m.active = map[string]bool{}
	}
	if m.active[id] {
		return nil, false
	}
	m.active[id] = true
	return func() { m.mu.Lock(); delete(m.active, id); m.mu.Unlock() }, true
}
func urlKey(id string) string { return "calendar:" + id + ":url" }
func (m *Module) Sources(ctx context.Context) ([]Source, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id,name,timezone,enabled,every_minutes,last_fetch,last_error,cache FROM calendar_sources ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Source{}
	for rows.Next() {
		var source Source
		var cache string
		if err := rows.Scan(&source.ID, &source.Name, &source.Timezone, &source.Enabled, &source.EveryMinutes, &source.LastFetch, &source.LastError, &cache); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(cache), &source.cache); err != nil {
			return nil, err
		}
		list = append(list, source)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	available, err := m.Vault.Has(ctx, "calendar:")
	if err != nil {
		return nil, err
	}
	for index := range list {
		list[index].HasURL = available[list[index].ID+":url"]
	}
	return list, nil
}
func (m *Module) source(ctx context.Context, id string) (Source, error) {
	list, err := m.Sources(ctx)
	if err != nil {
		return Source{}, err
	}
	for _, source := range list {
		if source.ID == id {
			return source, nil
		}
	}
	return Source{}, &action.Error{Status: 404, Message: "日历来源不存在"}
}
func (m *Module) SaveSource(ctx context.Context, source Source) (Source, error) {
	source.Name = strings.TrimSpace(source.Name)
	if source.Name == "" || utf8.RuneCountInString(source.Name) > 80 {
		return source, action.Invalid("日历名称须为 1–80 个字")
	}
	if _, err := time.LoadLocation(source.Timezone); err != nil {
		return source, action.Invalid("请填写有效 IANA 时区")
	}
	if source.EveryMinutes == 0 {
		source.EveryMinutes = 30
	}
	if source.EveryMinutes < 5 || source.EveryMinutes > 10080 {
		return source, action.Invalid("同步间隔须为 5–10080 分钟")
	}
	if source.SetURL != nil && *source.SetURL != "" {
		if _, err := safefetch.Validate(*source.SetURL); err != nil {
			return source, action.Invalid("请输入不含账号、片段的公网 HTTP/HTTPS 日历地址")
		}
	}
	if source.ID == "" {
		if source.SetURL == nil || *source.SetURL == "" {
			return source, action.Invalid("新日历需要私有 ics 地址")
		}
		source.ID = model.Secret()[:16]
		source.LastFetch, source.LastError = 0, ""
		release, _ := m.enter(source.ID)
		defer release()
		result, err := m.DB.ExecContext(ctx, `INSERT INTO calendar_sources(id,name,timezone,enabled,every_minutes) SELECT ?,?,?,0,? WHERE (SELECT count(*) FROM calendar_sources)<20`, source.ID, source.Name, source.Timezone, source.EveryMinutes)
		if err != nil {
			return source, err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return source, action.Invalid("最多保存 20 个日历")
		}
	} else {
		release, ok := m.enter(source.ID)
		if !ok {
			return source, &action.Error{Status: 409, Message: "日历正在同步，请稍后修改"}
		}
		defer release()
		old, err := m.source(ctx, source.ID)
		if err != nil {
			return source, err
		}
		source.cache, source.LastFetch, source.LastError = old.cache, old.LastFetch, old.LastError
		if old.Timezone != source.Timezone || source.SetURL != nil {
			source.cache = connector.Cache{}
			source.LastFetch = 0
			source.LastError = ""
		}
		if old.Enabled != source.Enabled {
			source.LastFetch = 0
		}
	}
	if source.SetURL != nil {
		var err error
		if *source.SetURL == "" {
			source.Enabled = false
			err = m.Vault.Delete(ctx, urlKey(source.ID))
		} else {
			err = m.Vault.Put(ctx, urlKey(source.ID), *source.SetURL)
		}
		if err != nil {
			return source, err
		}
	}
	cache, _ := json.Marshal(source.cache)
	if _, err := m.DB.ExecContext(ctx, `UPDATE calendar_sources SET name=?,timezone=?,enabled=?,every_minutes=?,last_fetch=?,last_error=?,cache=? WHERE id=?`, source.Name, source.Timezone, source.Enabled, source.EveryMinutes, source.LastFetch, source.LastError, string(cache), source.ID); err != nil {
		return source, err
	}
	return m.source(ctx, source.ID)
}
func (m *Module) Delete(ctx context.Context, id string) error {
	release, ok := m.enter(id)
	if !ok {
		return &action.Error{Status: 409, Message: "日历正在同步，请稍后删除"}
	}
	defer release()
	if _, err := m.source(ctx, id); err != nil {
		return err
	}
	if _, err := m.DB.ExecContext(ctx, `DELETE FROM calendar_sources WHERE id=?`, id); err != nil {
		return err
	}
	return m.Vault.Delete(ctx, urlKey(id))
}
func (m *Module) Range(ctx context.Context, from, to int64) ([]Event, error) {
	if from < 0 || to <= from || to-from > 366*86400+3600 {
		return nil, action.Invalid("日程范围须为 1 秒至 366 天")
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT e.id,e.source_id,s.name,e.title,e.body,e.start_at,e.end_at,e.all_day,e.location FROM calendar_events e JOIN calendar_sources s ON s.id=e.source_id WHERE s.enabled=1 AND e.start_at<? AND e.end_at>? ORDER BY e.start_at,e.id LIMIT 2000`, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Event{}
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.SourceID, &event.SourceName, &event.Title, &event.Body, &event.StartAt, &event.EndAt, &event.AllDay, &event.Location); err != nil {
			return nil, err
		}
		list = append(list, event)
	}
	return list, rows.Err()
}
func (m *Module) FetchOne(ctx context.Context, id string) error {
	release, ok := m.enter(id)
	if !ok {
		return &action.Error{Status: 409, Message: "这个日历正在同步"}
	}
	defer release()
	source, err := m.source(ctx, id)
	if err != nil {
		return err
	}
	secret, err := m.Vault.Get(ctx, urlKey(id))
	if err != nil {
		return errors.New("无法读取日历地址")
	}
	config, _ := json.Marshal(map[string]string{"timezone": source.Timezone})
	provider := m.Fetcher
	if provider == nil {
		provider = &icalSource{Now: m.now}
	}
	var result connector.Result
	if conditional, ok := provider.(connector.Conditional); ok {
		result, err = conditional.FetchCached(ctx, config, secret, source.cache)
	} else {
		result.Items, err = provider.Fetch(ctx, config, secret)
	}
	if err == nil && !result.NotModified {
		var transaction *sql.Tx
		transaction, err = m.DB.BeginTx(ctx, nil)
		if err == nil {
			defer transaction.Rollback()
			_, err = transaction.ExecContext(ctx, `DELETE FROM calendar_events WHERE source_id=?`, id)
			for _, item := range result.Items {
				if err != nil {
					break
				}
				var details occurrence
				if json.Unmarshal(item.Data, &details) != nil || details.EndAt <= item.At.Unix() {
					err = errors.New("日历条目格式无效")
					break
				}
				_, err = transaction.ExecContext(ctx, `INSERT INTO calendar_events(id,source_id,title,body,start_at,end_at,all_day,location) VALUES(?,?,?,?,?,?,?,?)`, id+":"+item.Key, id, item.Title, item.Body, item.At.Unix(), details.EndAt, details.AllDay, details.Location)
			}
			if err == nil {
				cache, _ := json.Marshal(result.Cache)
				_, err = transaction.ExecContext(ctx, `UPDATE calendar_sources SET cache=? WHERE id=?`, string(cache), id)
			}
			if err == nil {
				err = transaction.Commit()
			} else {
				transaction.Rollback()
			}
		}
	}
	message := ""
	if err != nil {
		message = "同步失败，保留上次成功的日程"
		var typed *action.Error
		if errors.As(err, &typed) {
			message = typed.Message
		}
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, saveError := m.DB.ExecContext(saveCtx, `UPDATE calendar_sources SET last_fetch=?,last_error=? WHERE id=?`, m.now().Unix(), message, id)
	if err != nil {
		return errors.New(message)
	}
	return saveError
}
func (m *Module) Sync(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	sources, err := m.Sources(ctx)
	if err != nil {
		return err
	}
	queue := make(chan Source, len(sources))
	for _, source := range sources {
		if source.Enabled && source.HasURL && source.LastFetch+int64(source.EveryMinutes)*60 <= m.now().Unix() {
			queue <- source
		}
	}
	close(queue)
	var workers sync.WaitGroup
	var failures int
	var lock sync.Mutex
	for range 4 {
		workers.Go(func() {
			for source := range queue {
				if ctx.Err() != nil {
					return
				}
				if m.FetchOne(ctx, source.ID) != nil {
					lock.Lock()
					failures++
					lock.Unlock()
				}
			}
		})
	}
	workers.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if failures > 0 {
		return fmt.Errorf("%d 个日历同步失败，请查看来源状态", failures)
	}
	return nil
}
func (m *Module) Jobs() []module.Job {
	return []module.Job{{Name: "calendar.sync", Title: "只读日历同步", Every: time.Minute, Run: m.Sync}}
}
func (m *Module) TodayText(ctx context.Context, now time.Time) (string, error) {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	list, err := m.Range(ctx, start.Unix(), start.AddDate(0, 0, 1).Unix())
	if err != nil {
		return "", err
	}
	lines := []string{}
	for _, event := range list {
		if len(lines) >= 20 {
			break
		}
		when := time.Unix(event.StartAt, 0).In(now.Location()).Format("15:04")
		if event.AllDay {
			when = "全天"
		}
		lines = append(lines, fmt.Sprintf("日程 %s：%s", when, event.Title))
	}
	if len(lines) == 0 {
		return "今天没有日程", nil
	}
	return strings.Join(lines, "\n"), nil
}

package intel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/connector"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/timeline"
	"github.com/leeska/Lodestar/internal/vault"
)

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

func (m *Module) source(ctx context.Context, id string) (Source, error) {
	sources, err := m.Sources(ctx)
	if err != nil {
		return Source{}, err
	}
	for _, source := range sources {
		if source.ID == id {
			return source, nil
		}
	}
	return Source{}, &action.Error{Status: 404, Message: "源不存在"}
}

func (m *Module) Fetch(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	if err := m.deliverPending(ctx); err != nil {
		return err
	}
	sources, err := m.Sources(ctx)
	if err != nil {
		return err
	}
	queue := make(chan string, 20)
	now := m.now()
	for _, source := range sources {
		if source.Enabled && now.Unix()-source.LastFetch >= int64(source.EveryMinutes*60) && len(queue) < 20 {
			queue <- source.ID
		}
	}
	close(queue)
	failures := make(chan error, 20)
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range queue {
				if err := ctx.Err(); err != nil {
					return
				}
				if err := m.FetchOne(ctx, id, true); err != nil {
					failures <- err
				}
			}
		}()
	}
	workers.Wait()
	close(failures)
	count := 0
	for range failures {
		count++
	}
	if _, err := m.DB.ExecContext(ctx, `DELETE FROM intel_items WHERE id IN (SELECT id FROM intel_items WHERE starred=0 AND added_at<? LIMIT 500)`, now.AddDate(0, 0, -90).Unix()); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("%d 个情报源抓取失败，请在情报页查看原因", count)
	}
	return nil
}

func (m *Module) FetchOne(ctx context.Context, id string, scheduled bool) error {
	release, ok := m.enter(id)
	if !ok {
		if scheduled {
			return nil
		}
		return &action.Error{Status: 409, Message: "这个源正在抓取，请稍后再试"}
	}
	defer release()
	source, err := m.source(ctx, id)
	if err != nil {
		return err
	}
	now := m.now()
	if scheduled && (!source.Enabled || now.Unix()-source.LastFetch < int64(source.EveryMinutes*60)) {
		return nil
	}
	secret, err := m.Vault.Get(ctx, "intel:"+source.ID+":token")
	if errors.Is(err, vault.ErrNotFound) {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("该源凭据读取失败")
	}
	kind, ok := connector.Get(source.Connector)
	if !ok {
		return action.Invalid("连接器不可用")
	}
	var result connector.Result
	if conditional, ok := kind.(connector.Conditional); ok {
		result, err = conditional.FetchCached(ctx, source.Config, secret, source.cache)
	} else {
		result.Items, err = kind.Fetch(ctx, source.Config, secret)
		result.Cache = source.cache
	}
	fetchError := ""
	if err != nil {
		fetchError = textLimit(err.Error(), 200)
	}
	if err == nil && !result.NotModified {
		err = m.ingest(ctx, source, result.Items, now)
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	message := ""
	if err != nil {
		message = "抓取或保存失败；请检查源地址、格式、网络和凭据"
		if fetchError != "" {
			message = fetchError
		}
	}
	cache := source.cache
	if err == nil {
		cache = result.Cache
	}
	encoded, _ := json.Marshal(cache)
	if _, saveErr := m.DB.ExecContext(saveCtx, `UPDATE intel_sources SET last_fetch=?,last_error=?,cache=? WHERE id=?`, now.Unix(), message, string(encoded), source.ID); saveErr != nil {
		return saveErr
	}
	if err != nil {
		return errors.New(message)
	}
	return nil
}

func (m *Module) ingest(ctx context.Context, source Source, items []connector.Item, now time.Time) error {
	rules, err := m.Rules(ctx)
	if err != nil {
		return err
	}
	for _, item := range items[:min(50, len(items))] {
		if item.Key == "" || len(item.Key) > 300 {
			continue
		}
		item.Title, item.Body = textLimit(item.Title, 300), textLimit(item.Body, 8000)
		if item.Title == "" {
			item.Title = "未命名条目"
		}
		item.URL = feedURL("", item.URL)
		if item.At.IsZero() {
			item.At = now
		}
		action := disposition(source.ID, item, rules)
		state := action
		if state == "notify" {
			state = "notify_pending"
		}
		result, err := m.DB.ExecContext(ctx, `INSERT OR IGNORE INTO intel_items(source_id,key,title,url,at,added_at,body,disposition) VALUES(?,?,?,?,?,?,?,?)`,
			source.ID, item.Key, item.Title, item.URL, item.At.Unix(), now.Unix(), item.Body, state)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			continue
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if m.Timeline != nil && action != "ignore" {
			if err := m.Timeline.Emit(ctx, timeline.Entry{At: now.Unix(), Source: "intel", Kind: "intel.received", Level: timeline.Info, Title: source.Name + " · " + item.Title, Body: item.URL, Ref: fmt.Sprintf("intel:%d", id)}); err != nil {
				return err
			}
		}
	}
	return m.deliverPending(ctx)
}

func (m *Module) deliverPending(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.DB.QueryContext(ctx, `SELECT i.id,s.name,i.title,i.url,i.body FROM intel_items i JOIN intel_sources s ON s.id=i.source_id WHERE disposition='notify_pending' ORDER BY i.id LIMIT 50`)
	if err != nil {
		return err
	}
	pending := []Item{}
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.SourceName, &item.Title, &item.URL, &item.Body); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range pending {
		if m.Events == nil {
			return fmt.Errorf("情报通知服务不可用")
		}
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := m.Events.RecordEvent(saveCtx, model.Event{NodeID: fmt.Sprintf("intel:%d", item.ID), NodeName: item.Title, Kind: model.EventIntel, Message: item.SourceName + "\n" + textLimit(item.Body, 1500) + "\n" + item.URL, At: m.now().Unix()})
		if err == nil {
			_, err = m.DB.ExecContext(saveCtx, `UPDATE intel_items SET disposition='notify' WHERE id=?`, item.ID)
		}
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) Jobs() []module.Job {
	return []module.Job{{Name: "intel.fetch", Title: "抓取情报与清理旧条目", Every: time.Minute, Run: m.Fetch}}
}

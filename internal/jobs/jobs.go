// Package jobs runs the modules' periodic work (module.Job): each job on its
// own interval, one run at a time, with its last outcome stored so the panel
// and agents can see whether background work is healthy. A job that starts
// failing writes one entry to the timeline; recovering writes another.
package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/httpjson"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/timeline"
)

// State is a job's last outcome.
type State struct {
	Name      string `json:"name"`
	Title     string `json:"title"`
	Every     int64  `json:"every_seconds"`
	LastStart int64  `json:"last_start,omitempty"`
	LastEnd   int64  `json:"last_end,omitempty"`
	LastError string `json:"last_error,omitempty"`
	Runs      int64  `json:"runs"`
	Failures  int64  `json:"failures"`
	Running   bool   `json:"running"`
	NextRun   int64  `json:"next_run,omitempty"`
}

// Module is the scheduler.
type Module struct {
	DB       *sql.DB
	Timeline *timeline.Module
	// FirstDelay is how long after start a job first runs (default 1 minute,
	// so a restart does not stampede).
	FirstDelay time.Duration

	mu      sync.Mutex
	jobs    []module.Job
	running map[string]bool
	next    map[string]time.Time
	kick    map[string]chan struct{}
}

func (m *Module) ID() string { return "jobs" }

func (m *Module) Migrations() []storage.Migration {
	return []storage.Migration{{Module: "jobs", Version: 1, Name: "job state", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`CREATE TABLE job_state(name TEXT PRIMARY KEY, last_start INTEGER NOT NULL DEFAULT 0, last_end INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '', runs INTEGER NOT NULL DEFAULT 0, failures INTEGER NOT NULL DEFAULT 0)`)
		return err
	}}}
}

// Jobs of the scheduler itself: pruning the timeline.
func (m *Module) Jobs() []module.Job {
	if m.Timeline == nil {
		return nil
	}
	return []module.Job{{Name: "timeline.prune", Title: "清理过期的时间线条目", Every: 24 * time.Hour, Run: m.Timeline.Prune}}
}

// Run schedules the jobs until ctx ends.
func (m *Module) Run(ctx context.Context, list []module.Job) {
	m.mu.Lock()
	m.jobs = list
	m.running, m.next, m.kick = map[string]bool{}, map[string]time.Time{}, map[string]chan struct{}{}
	for _, j := range list {
		m.kick[j.Name] = make(chan struct{}, 1)
	}
	m.mu.Unlock()
	delay := m.FirstDelay
	if delay == 0 {
		delay = time.Minute
	}
	var wg sync.WaitGroup
	for _, j := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.loop(ctx, j, delay)
		}()
	}
	wg.Wait()
}

func (m *Module) loop(ctx context.Context, j module.Job, delay time.Duration) {
	wait := delay
	for {
		m.mu.Lock()
		m.next[j.Name] = time.Now().Add(wait)
		kick := m.kick[j.Name]
		m.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-kick:
			timer.Stop()
		}
		m.runOnce(ctx, j)
		wait = j.Every
	}
}

func (m *Module) runOnce(ctx context.Context, j module.Job) {
	m.mu.Lock()
	m.running[j.Name] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.running[j.Name] = false
		m.mu.Unlock()
	}()
	start := time.Now()
	var prev string
	m.DB.QueryRowContext(ctx, `SELECT last_error FROM job_state WHERE name = ?`, j.Name).Scan(&prev)
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		runCtx, cancel := context.WithTimeout(ctx, max(j.Every, time.Minute))
		defer cancel()
		return j.Run(runCtx)
	}()
	if ctx.Err() != nil {
		return
	}
	message, failed := "", 0
	if err != nil {
		message, failed = err.Error(), 1
		slog.Warn("job failed", "job", j.Name, "error", err)
	}
	m.DB.ExecContext(context.WithoutCancel(ctx), `INSERT INTO job_state(name, last_start, last_end, last_error, runs, failures) VALUES(?,?,?,?,1,?)
		ON CONFLICT(name) DO UPDATE SET last_start = excluded.last_start, last_end = excluded.last_end, last_error = excluded.last_error,
		runs = runs + 1, failures = failures + excluded.failures`, j.Name, start.Unix(), time.Now().Unix(), message, failed)
	if m.Timeline == nil {
		return
	}
	switch {
	case message != "" && prev == "":
		m.Timeline.Emit(context.WithoutCancel(ctx), timeline.Entry{Source: "jobs", Kind: "job.failed", Level: timeline.Bad, Title: j.Title + " 失败", Body: message, Ref: "job:" + j.Name})
	case message == "" && prev != "":
		m.Timeline.Emit(context.WithoutCancel(ctx), timeline.Entry{Source: "jobs", Kind: "job.recovered", Level: timeline.Good, Title: j.Title + " 恢复正常", Ref: "job:" + j.Name})
	}
}

// States lists every job with its last outcome.
func (m *Module) States(ctx context.Context) ([]State, error) {
	stored := map[string]State{}
	rows, err := m.DB.QueryContext(ctx, `SELECT name, last_start, last_end, last_error, runs, failures FROM job_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s State
		if err := rows.Scan(&s.Name, &s.LastStart, &s.LastEnd, &s.LastError, &s.Runs, &s.Failures); err != nil {
			return nil, err
		}
		stored[s.Name] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []State{}
	for _, j := range m.jobs {
		s := stored[j.Name]
		s.Name, s.Title, s.Every, s.Running = j.Name, j.Title, int64(j.Every/time.Second), m.running[j.Name]
		if t, ok := m.next[j.Name]; ok {
			s.NextRun = t.Unix()
		}
		out = append(out, s)
	}
	return out, nil
}

// ErrUnknownJob is a name that is not scheduled.
var ErrUnknownJob = errors.New("没有这个定时任务")

// RunNow starts a job at once (if it is not running already).
func (m *Module) RunNow(name string) error {
	m.mu.Lock()
	kick, ok := m.kick[name]
	m.mu.Unlock()
	if !ok {
		return ErrUnknownJob
	}
	select {
	case kick <- struct{}{}:
	default:
	}
	return nil
}

func (m *Module) Routes(r module.Routes) {
	r.Admin("GET /api/v1/admin/jobs", func(w http.ResponseWriter, req *http.Request) {
		list, err := m.States(req.Context())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"jobs": list})
	})
	r.Admin("POST /api/v1/admin/jobs/{name}/run", func(w http.ResponseWriter, req *http.Request) {
		if err := m.RunNow(req.PathValue("name")); err != nil {
			httpjson.Error(w, 404, err.Error())
			return
		}
		httpjson.Write(w, 200, map[string]any{"started": true})
	})
}

func (m *Module) Actions() []action.Action {
	return []action.Action{
		action.R("jobs.list", action.Read, "GET", "/api/v1/admin/jobs", "定时任务", "列出后台定时任务：间隔、上次运行时间、上次的错误、累计次数与失败次数、下次运行时间。", nil),
		action.R("jobs.run", action.Write, "POST", "/api/v1/admin/jobs/{name}/run", "立即运行定时任务", "让一个定时任务马上运行一次。", map[string]any{"name": action.Str("任务名，来自 jobs.list")}),
	}
}

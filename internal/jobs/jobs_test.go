package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/timeline"
)

func TestJobStateAndTimelineTransitions(t *testing.T) {
	tl := &timeline.Module{}
	m := &Module{Timeline: tl}
	s, err := storage.OpenWith(filepath.Join(t.TempDir(), "j.db"), module.Migrations([]module.Module{tl, m}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m.DB, tl.DB = s.DB(), s.DB()
	ctx := context.Background()
	fail := true
	job := module.Job{Name: "x.check", Title: "检查", Every: time.Hour, Run: func(context.Context) error {
		if fail {
			return errors.New("boom")
		}
		return nil
	}}
	m.jobs, m.running = []module.Job{job}, map[string]bool{}
	m.runOnce(ctx, job)
	m.runOnce(ctx, job)
	fail = false
	m.runOnce(ctx, job)
	states, _ := m.States(ctx)
	if len(states) != 1 || states[0].Runs != 3 || states[0].Failures != 2 || states[0].LastError != "" {
		t.Fatalf("state: %+v", states)
	}
	entries, _ := tl.List(ctx, timeline.Query{Sources: []string{"jobs"}})
	if len(entries) != 2 || entries[0].Kind != "job.recovered" || entries[1].Kind != "job.failed" {
		t.Fatalf("timeline: %+v", entries)
	}
	// A panicking job is a failure, not a crash.
	m.runOnce(ctx, module.Job{Name: "x.panic", Title: "崩溃", Every: time.Hour, Run: func(context.Context) error { panic("bad") }})
}

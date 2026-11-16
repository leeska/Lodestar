package todo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/assets"
	"github.com/leeska/Lodestar/internal/intel"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
)

func fixture(t *testing.T) (*Module, *storage.Store) {
	t.Helper()
	mod := &Module{}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "todo.db"), module.Migrations([]module.Module{&assets.Module{}, &intel.Module{}, mod}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	mod.DB, mod.Events = store.DB(), store
	return mod, store
}
func TestReminderStagesCompletionReopenAndReschedule(t *testing.T) {
	mod, store := fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	mod.Now = func() time.Time { return now }
	item, err := mod.Save(ctx, Item{Title: "续费", DueAt: now.Add(25 * time.Hour).Unix(), Priority: 2})
	if err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		if err := mod.Remind(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		store.DB().QueryRow(`SELECT count(*) FROM events WHERE kind='todo'`).Scan(&count)
		if count != want {
			t.Fatalf("events=%d want=%d", count, want)
		}
	}
	check(0)
	now = now.Add(time.Hour)
	check(1)
	check(1)
	now = now.Add(23 * time.Hour)
	check(2)
	completed, err := mod.Complete(ctx, item.ID, true)
	if err != nil || completed.DoneAt == 0 {
		t.Fatal(completed, err)
	}
	check(2)
	again, err := mod.Complete(ctx, item.ID, true)
	if err != nil || again.DoneAt != completed.DoneAt {
		t.Fatal("completion must be idempotent", again, err)
	}
	if _, err := mod.Complete(ctx, item.ID, false); err != nil {
		t.Fatal(err)
	}
	check(3)
	now = now.Add(time.Hour + time.Second)
	check(4)
	item.DueAt = now.Add(48 * time.Hour).Unix()
	if _, err := mod.Save(ctx, item); err != nil {
		t.Fatal(err)
	}
	check(4)
	list, err := mod.List(ctx, "open", now.Add(24*time.Hour).Unix())
	if err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
	if err := mod.Delete(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.Get(ctx, item.ID); err == nil {
		t.Fatal("deleted item still exists")
	}
}
func TestFromRefUsesSnapshotAndReusesOpenTodo(t *testing.T) {
	mod, store := fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	mod.Now = func() time.Time { return now }
	_, err := store.DB().Exec(`INSERT INTO assets(id,kind,name,note,expires_on,created_at,updated_at) VALUES('domain','domain','example.com','需要续费','2026-11-20',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	first, err := mod.FromRef(ctx, "asset:domain")
	if err != nil {
		t.Fatal(err)
	}
	if first.Note != "需要续费" || first.DueAt != time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).Unix() {
		t.Fatal(first)
	}
	second, err := mod.FromRef(ctx, "asset:domain")
	if err != nil || second.ID != first.ID {
		t.Fatal("duplicate conversion", second, err)
	}
	mod.Complete(ctx, first.ID, true)
	third, err := mod.FromRef(ctx, "asset:domain")
	if err != nil || third.ID == first.ID {
		t.Fatal(third, err)
	}
	if _, err := mod.FromRef(ctx, "vault:password"); err == nil {
		t.Fatal("unapproved reference kind accepted")
	}
	if _, err := mod.FromRef(ctx, "event:99999"); err == nil {
		t.Fatal("missing event accepted")
	}
	_, text, err := mod.Brief(ctx, time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC))
	if err != nil || !strings.Contains(text, "example.com") {
		t.Fatal(text, err)
	}
}
func TestAlreadyRemindedBacklogDoesNotStarveOtherItems(t *testing.T) {
	mod, store := fixture(t)
	ctx := context.Background()
	now := time.Now()
	mod.Now = func() time.Time { return now }
	_, err := store.DB().Exec(`WITH RECURSIVE seq(value) AS (SELECT 1 UNION ALL SELECT value+1 FROM seq WHERE value<501)
INSERT INTO todos(id,title,due_at,created_at,reminded_stage) SELECT 'old-'||value,'旧待办',1,1,3 FROM seq`)
	if err != nil {
		t.Fatal(err)
	}
	item, err := mod.Save(ctx, Item{Title: "新提醒", DueAt: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := mod.Remind(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	store.DB().QueryRow(`SELECT count(*) FROM events WHERE node_id=?`, "todo:"+item.ID).Scan(&count)
	if count != 1 {
		t.Fatal("a previously reminded backlog blocked the new reminder", count)
	}
}

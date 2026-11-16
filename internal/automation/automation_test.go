package automation

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/timeline"
	"github.com/leeska/Lodestar/internal/vault"
)

func fixture(t *testing.T) (*Module, *storage.Store, *[]string) {
	t.Helper()
	stream := &timeline.Module{}
	mod := &Module{}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "automation.db"), module.Migrations([]module.Module{&vault.Module{}, stream, mod}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	secrets, err := vault.New(store.DB(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	mod.DB, mod.Vault, mod.Timeline, mod.Events = store.DB(), secrets, stream, store
	stream.DB = store.DB()
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	mod.Now = func() time.Time { return now }
	calls := []string{}
	registry := action.NewRegistry(action.Action{Name: "todo.create", Risk: action.Write, Handler: func(context.Context, json.RawMessage) (any, error) { return nil, nil }}, action.Action{Name: "todo.delete", Risk: action.Danger, Handler: func(context.Context, json.RawMessage) (any, error) { return nil, nil }})
	mod.BindActions(registry, func(ctx context.Context, name string, args json.RawMessage, confirm bool) (any, error) {
		actor, ok := action.ActorFrom(ctx)
		if !ok || !strings.HasPrefix(actor.Name, "automation:") || action.OriginFrom(ctx) != "automation" {
			t.Error("missing provenance", actor)
		}
		if name == "todo.delete" && !confirm {
			t.Error("danger executed without confirmation")
		}
		calls = append(calls, name+":"+string(args))
		if err := stream.Emit(ctx, timeline.Entry{Source: "todo", Kind: "generated", Title: "自动化动作产生的条目"}); err != nil {
			t.Error(err)
		}
		if err := store.RecordEvent(ctx, model.Event{Kind: model.EventTodo, Message: "generated"}); err != nil {
			t.Error(err)
		}
		return map[string]any{"secret": "OUTPUT_SECRET_SENTINEL"}, nil
	})
	return mod, store, &calls
}
func owner() context.Context { return action.WithActor(context.Background(), action.Owner("owner")) }
func saveRule(t *testing.T, mod *Module, trigger, target, args string) Rule {
	t.Helper()
	raw := json.RawMessage(args)
	rule, err := mod.Save(owner(), Rule{Definition: Definition{Name: "处理提醒", Enabled: true, Trigger: trigger, Cron: "0 9 * * *", Timezone: "UTC", Action: target, Cooldown: 60}, SetArgs: &raw})
	if err != nil {
		t.Fatal(err)
	}
	return rule
}
func TestDangerFrozenEncryptedAndConsumedAcrossRestart(t *testing.T) {
	mod, store, calls := fixture(t)
	rule := saveRule(t, mod, "cron", "todo.delete", `{"id":"ORIGINAL_SECRET"}`)
	if err := mod.Tick(owner()); err != nil {
		t.Fatal(err)
	}
	runs, err := mod.Runs(owner())
	if err != nil || len(runs) != 1 || runs[0].Status != "pending" || len(*calls) != 0 {
		t.Fatal(runs, err, *calls)
	}
	mod.Now = func() time.Time { return time.Date(2026, 11, 15, 9, 1, 0, 0, time.UTC) }
	replacement := json.RawMessage(`{"id":"DIFFERENT_SECRET"}`)
	rule.SetArgs = &replacement
	if _, err := mod.Save(owner(), rule); err != nil {
		t.Fatal(err)
	}
	restarted := &Module{DB: mod.DB, Vault: mod.Vault, Timeline: mod.Timeline, Events: mod.Events, registry: mod.registry, execute: mod.execute, Now: mod.Now}
	if err := restarted.Confirm(owner(), runs[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0], "ORIGINAL_SECRET") {
		t.Fatal("confirmation substituted mutable rule args", *calls)
	}
	if err := restarted.Confirm(owner(), runs[0].ID, true); err == nil {
		t.Fatal("confirmation replayed")
	}
	details, err := restarted.Details(owner(), runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(details)
	if !strings.Contains(string(encoded), "OUTPUT_SECRET_SENTINEL") {
		t.Fatal("encrypted result unavailable", details)
	}
	for _, table := range []string{"automation_rules", "automation_runs", "timeline", "audit_log", "events"} {
		rows, err := store.DB().Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for index := range values {
				dest[index] = &values[index]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(values)
			if strings.Contains(string(raw), "ORIGINAL_SECRET") || strings.Contains(string(raw), "DIFFERENT_SECRET") || strings.Contains(string(raw), "OUTPUT_SECRET_SENTINEL") {
				t.Fatal("plaintext leaked", table, string(raw))
			}
		}
		rows.Close()
	}
}
func TestTimelineOriginDedupCooldownAndNoHistoricalReplay(t *testing.T) {
	mod, _, calls := fixture(t)
	if err := mod.Timeline.Emit(owner(), timeline.Entry{Source: "intel", Kind: "new", Title: "historical"}); err != nil {
		t.Fatal(err)
	}
	saveRule(t, mod, "timeline", "todo.create", `{"body":{"title":"{{title}}","ref":"{{ref}}"}}`)
	if err := mod.Tick(owner()); err != nil || len(*calls) != 0 {
		t.Fatal(err, *calls)
	}
	if err := mod.Timeline.Emit(owner(), timeline.Entry{Source: "intel", Kind: "new", Title: `<script>"hello"</script>`, Ref: "intel:4"}); err != nil {
		t.Fatal(err)
	}
	if err := mod.Tick(owner()); err != nil || len(*calls) != 1 {
		t.Fatal(err, *calls)
	}
	mod.Now = func() time.Time { return time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC) }
	if err := mod.Tick(owner()); err != nil || len(*calls) != 1 {
		t.Fatal("origin loop", err, *calls)
	}
	if err := mod.Tick(owner()); err != nil || len(*calls) != 1 {
		t.Fatal("duplicate claim", err, *calls)
	}
}
func TestRevocationCreatorAndApproverScopes(t *testing.T) {
	mod, _, calls := fixture(t)
	limited := action.WithActor(context.Background(), action.Actor{Name: "key:limited", KeyID: "key1", MaxRisk: action.Write, Modules: []string{"automation"}})
	raw := json.RawMessage(`{"id":"task"}`)
	if _, err := mod.Save(limited, Rule{Definition: Definition{Name: "bad", Trigger: "cron", Cron: "0 9 * * *", Timezone: "UTC", Action: "todo.delete"}, SetArgs: &raw}); err == nil {
		t.Fatal("scope escalation")
	}
	creator := action.Actor{Name: "key:full", KeyID: "key2", MaxRisk: action.Danger, Modules: []string{"automation", "todo"}}
	ctx := action.WithActor(context.Background(), creator)
	rule, err := mod.Save(ctx, Rule{Definition: Definition{Name: "review", Enabled: true, Trigger: "cron", Cron: "0 9 * * *", Timezone: "UTC", Action: "todo.delete"}, SetArgs: &raw})
	if err != nil {
		t.Fatal(err)
	}
	mod.resolve = func(context.Context, action.Actor) (action.Actor, error) { return creator, nil }
	if err := mod.Tick(owner()); err != nil {
		t.Fatal(err)
	}
	runs, _ := mod.Runs(owner())
	approver := action.WithActor(context.Background(), action.Actor{Name: "key:automation-only", MaxRisk: action.Danger, Modules: []string{"automation"}})
	if err := mod.Confirm(approver, runs[0].ID, true); err == nil {
		t.Fatal("approver scope bypass")
	}
	mod.resolve = func(context.Context, action.Actor) (action.Actor, error) {
		return action.Actor{}, errors.New("revoked")
	}
	if err := mod.Confirm(owner(), runs[0].ID, true); err == nil || len(*calls) != 0 {
		t.Fatal("revoked creator retained authority", err, *calls)
	}
	if err := mod.Confirm(owner(), runs[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if err := mod.Delete(owner(), rule.ID); err != nil {
		t.Fatal(err)
	}
}
func TestTypedTemplatesConditionsAndCron(t *testing.T) {
	if numeric(json.Number("1e999999999")) != nil {
		t.Fatal("unbounded exponent accepted")
	}
	entry := timeline.Entry{Source: "webhook", Data: json.RawMessage(`{"id":"a\"b","count":5,"active":true}`)}
	args, err := renderArgs(`{"id":"{{data.id}}","count":"{{data.count}}","active":"{{data.active}}","note":"值 {{data.id}}"}`, entry)
	if err != nil || !strings.Contains(string(args), `"count":5`) || !strings.Contains(string(args), `"active":true`) {
		t.Fatal(string(args), err)
	}
	if _, err := renderArgs(`{"id":"{{data.missing}}"}`, entry); err == nil {
		t.Fatal("missing field accepted")
	}
	if !matches(Rule{Definition: Definition{Conditions: []Condition{{Field: "data.count", Op: "gte", Value: 5.0}}}}, entry) {
		t.Fatal("numeric condition failed")
	}
	if matches(Rule{Definition: Definition{Conditions: []Condition{{Field: "data.count", Op: "lt", Value: 5.0}}}}, entry) {
		t.Fatal("numeric condition false positive")
	}
	rule := Rule{Definition: Definition{Timezone: "America/New_York", Cron: "30 1 * * *"}}
	spec, err := schedule(rule)
	if err != nil || !spec.Next(time.Date(2026, 11, 1, 5, 29, 0, 0, time.UTC)).Equal(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) {
		t.Fatal(spec, err)
	}
	rule.Cron = "@every 1s"
	if _, err := schedule(rule); err == nil {
		t.Fatal("dense custom schedule accepted")
	}
}

package intel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/connector"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/timeline"
	"github.com/leeska/Lodestar/internal/vault"
)

var fakeIDs atomic.Int32

type fakeConnector struct {
	id  string
	run func(context.Context, json.RawMessage) ([]connector.Item, error)
}

func (fake *fakeConnector) ID() string        { return fake.id }
func (*fakeConnector) Name() string           { return "测试源" }
func (*fakeConnector) Schema() map[string]any { return map[string]any{"type": "object"} }
func (fake *fakeConnector) Fetch(ctx context.Context, config json.RawMessage, _ string) ([]connector.Item, error) {
	return fake.run(ctx, config)
}

func fixture(t *testing.T) (*Module, *storage.Store) {
	t.Helper()
	mod := &Module{}
	history := &timeline.Module{}
	secretsModule := &vault.Module{}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "intel.db"), module.Migrations([]module.Module{history, secretsModule, mod}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	secrets, err := vault.New(store.DB(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	history.DB = store.DB()
	mod.DB, mod.Vault, mod.Timeline, mod.Events = store.DB(), secrets, history, store
	return mod, store
}

func registerFake(run func(context.Context, json.RawMessage) ([]connector.Item, error)) string {
	id := fmt.Sprintf("fixture_%d", fakeIDs.Add(1))
	connector.Register(&fakeConnector{id: id, run: run})
	return id
}

func TestIngestDeduplicatesRoutesRulesAndPreservesStars(t *testing.T) {
	mod, store := fixture(t)
	ctx := context.Background()
	kind := registerFake(func(context.Context, json.RawMessage) ([]connector.Item, error) { return nil, nil })
	source, err := mod.SaveSource(ctx, Source{Name: "关注", Connector: kind, Config: json.RawMessage(`{"url":"https://example.com/feed"}`), EveryMinutes: 15})
	if err != nil {
		t.Fatal(err)
	}
	mod.SaveRule(ctx, Rule{Name: "不要噪声", Keywords: []string{"noise"}, Action: "ignore"})
	mod.SaveRule(ctx, Rule{Name: "发布提醒", Keywords: []string{"release"}, Sources: []string{source.ID}, Action: "notify"})
	now := time.Now()
	items := []connector.Item{{Key: "one", Title: "New RELEASE", URL: "https://example.com/one", At: now}, {Key: "two", Title: "普通更新", At: now}, {Key: "three", Title: "noise", At: now}}
	for range 2 {
		if err := mod.ingest(ctx, source, items, now); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := mod.Items(ctx, Query{})
	if err != nil || len(saved) != 3 {
		t.Fatalf("items=%+v %v", saved, err)
	}
	var eventCount int
	store.DB().QueryRow(`SELECT count(*) FROM events WHERE kind='intel'`).Scan(&eventCount)
	if eventCount != 1 {
		t.Fatalf("duplicate event count=%d", eventCount)
	}
	_, summary, err := mod.Brief(ctx, now)
	if err != nil || !strings.Contains(summary, "普通更新") || strings.Contains(summary, "RELEASE") || strings.Contains(summary, "noise") {
		t.Fatalf("brief=%s err=%v", summary, err)
	}
	if _, err := store.DB().Exec(`UPDATE intel_items SET added_at=?`, now.AddDate(0, 0, -91).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE intel_items SET starred=1 WHERE key='two'`); err != nil {
		t.Fatal(err)
	}
	if err := mod.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err = mod.Items(ctx, Query{})
	if err != nil || len(saved) != 1 || saved[0].Title != "普通更新" {
		t.Fatalf("star retention: %+v %v", saved, err)
	}
}

func TestOneSourceFailureDoesNotStopOtherSources(t *testing.T) {
	mod, _ := fixture(t)
	ctx := context.Background()
	kind := registerFake(func(_ context.Context, raw json.RawMessage) ([]connector.Item, error) {
		if strings.Contains(string(raw), "bad") {
			return nil, errors.New("temporary source failure")
		}
		return []connector.Item{{Key: "ok", Title: "正常条目", At: time.Now()}}, nil
	})
	for _, name := range []string{"bad", "good"} {
		if _, err := mod.SaveSource(ctx, Source{Name: name, Connector: kind, Config: json.RawMessage(fmt.Sprintf(`{"url":"https://example.com/%s"}`, name)), Enabled: true, EveryMinutes: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := mod.Fetch(ctx); err == nil {
		t.Fatal("aggregate job should expose failed source")
	}
	items, err := mod.Items(ctx, Query{})
	if err != nil || len(items) != 1 || items[0].Title != "正常条目" {
		t.Fatalf("healthy source was blocked: %+v %v", items, err)
	}
	sources, _ := mod.Sources(ctx)
	for _, source := range sources {
		if source.Name == "bad" && (source.LastError == "" || source.LastFetch == 0) {
			t.Fatal("source failure was not recorded")
		}
		if source.Name == "good" && source.LastError != "" {
			t.Fatal("source error leaked to another source")
		}
	}
}

func TestSourceTokensAreEncryptedAndNeverReturned(t *testing.T) {
	mod, store := fixture(t)
	ctx := context.Background()
	secret := "fixture-private-github-token"
	source, err := mod.SaveSource(ctx, Source{Name: "GitHub", Connector: "github_release", Config: json.RawMessage(`{"repository":"owner/repo"}`), EveryMinutes: 15, SetSecret: &secret})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(source)
	if !source.HasSecret || source.SetSecret != nil || strings.Contains(string(raw), secret) {
		t.Fatal("source read exposed token")
	}
	var config, ciphertext string
	store.DB().QueryRow(`SELECT config FROM intel_sources WHERE id=?`, source.ID).Scan(&config)
	store.DB().QueryRow(`SELECT hex(data) FROM vault WHERE id=?`, "intel:"+source.ID+":token").Scan(&ciphertext)
	if strings.Contains(config, secret) || ciphertext == "" {
		t.Fatal("token was not stored in vault")
	}
	source.Name = "改名"
	source, err = mod.SaveSource(ctx, source)
	value, readErr := mod.Vault.Get(ctx, "intel:"+source.ID+":token")
	if err != nil || readErr != nil || value != secret || !source.HasSecret {
		t.Fatal("metadata update lost token")
	}
}

func TestSourceCannotChangeDuringFetch(t *testing.T) {
	mod, _ := fixture(t)
	started := make(chan struct{})
	kind := registerFake(func(ctx context.Context, _ json.RawMessage) ([]connector.Item, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	source, err := mod.SaveSource(context.Background(), Source{Name: "阻塞源", Connector: kind, Config: json.RawMessage(`{"url":"https://example.com/feed"}`), EveryMinutes: 15})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- mod.FetchOne(ctx, source.ID, false) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fetch did not start")
	}
	source.Name = "不能在抓取中改"
	if _, err := mod.SaveSource(context.Background(), source); err == nil {
		t.Fatal("source changed under an in-flight fetch")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled fetch did not exit")
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/apikey"
	"github.com/leeska/Lodestar/internal/assets"
	"github.com/leeska/Lodestar/internal/assistant"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/vault"
	"github.com/leeska/Lodestar/web"
)

type assistantFixture struct {
	*actionFixture
	mod    *assistant.Module
	assets *assets.Module
	api    *API
}

func newAssistantFixture(t *testing.T, gateway http.HandlerFunc) *assistantFixture {
	t.Helper()
	gatewayServer := httptest.NewServer(gateway)
	t.Cleanup(gatewayServer.Close)
	mod := &assistant.Module{}
	assetModule := &assets.Module{}
	vaultModule := &vault.Module{}
	modules := []module.Module{vaultModule, assetModule, mod}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "assistant.db"), module.Migrations(modules))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	secrets, err := vault.New(store.DB(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	vaultModule.Vault = secrets
	mod.DB, mod.Vault = store.DB(), secrets
	assetModule.Store = &assets.Store{DB: store.DB(), Vault: secrets}
	assetModule.Nodes, assetModule.Events = store, store
	admin := model.Secret()
	keys := &apikey.Store{DB: store.DB()}
	api := &API{Store: store, AdminToken: admin, Keys: keys, Modules: modules, Assets: web.Handler()}
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	key := "gateway-private-key-DO-NOT-LEAK"
	if err := mod.SaveSettings(context.Background(), assistant.Settings{Protocol: "openai", URL: gatewayServer.URL + "/v1", Model: "test"}, &key); err != nil {
		t.Fatal(err)
	}
	return &assistantFixture{&actionFixture{t: t, store: store, keys: keys, server: server, admin: admin}, mod, assetModule, api}
}

func gatewayText(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": text}}}})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
}

func gatewayCall(w http.ResponseWriter, id, name, args string) {
	w.Header().Set("Content-Type", "text/event-stream")
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
		map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]string{"name": name, "arguments": args}},
	}}}}})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
}

func (f *assistantFixture) stream(path string, input any) string {
	f.t.Helper()
	body, _ := json.Marshal(input)
	req, _ := http.NewRequest("POST", f.server.URL+"/api/v1/admin/assistant"+path, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+f.admin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := f.server.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		f.t.Fatalf("SSE response: %d %v %s", resp.StatusCode, resp.Header, raw)
	}
	return string(raw)
}

func streamEvent(t *testing.T, raw, event string) map[string]any {
	t.Helper()
	for _, block := range strings.Split(raw, "\n\n") {
		if strings.HasPrefix(block, "event: "+event+"\n") {
			var result map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.SplitN(block, "\n", 2)[1], "data: ")), &result); err != nil {
				t.Fatal(err)
			}
			return result
		}
	}
	t.Fatalf("event %q missing: %s", event, raw)
	return nil
}

func TestAssistantMultiRoundToolsAndPrivateSettings(t *testing.T) {
	var rounds atomic.Int32
	f := newAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer gateway-private-key-DO-NOT-LEAK" {
			t.Error("gateway authentication or path")
		}
		switch rounds.Add(1) {
		case 1:
			gatewayCall(w, "upcoming", "assets_upcoming", `{"days":30}`)
		case 2:
			gatewayCall(w, "create", "assets_create", `{"body":{"kind":"domain","name":"leeska.net","expires_on":"2027-11-26"}}`)
		default:
			gatewayText(w, "已登记，密钥 gateway-private-key-DO-NOT-LEAK")
		}
	})
	raw := f.stream("/chat", map[string]string{"message": "看一下到期资产，再登记 leeska.net"})
	streamEvent(t, raw, "done")
	if rounds.Load() != 3 || strings.Contains(raw, "gateway-private-key") {
		t.Fatalf("rounds=%d secret leaked=%v", rounds.Load(), strings.Contains(raw, "gateway-private-key"))
	}
	_, settings := f.do("GET", "/api/v1/admin/assistant/settings", f.admin, "")
	encoded, _ := json.Marshal(settings)
	if settings["has_key"] != true || strings.Contains(string(encoded), "gateway-private-key") {
		t.Fatalf("settings exposed secret: %s", encoded)
	}
	items, err := f.assets.Store.List(context.Background(), assets.Filter{}, time.Now())
	if err != nil || len(items) != 1 || items[0].ExpiresOn != "2027-11-26" {
		t.Fatalf("tool mutation: %+v %v", items, err)
	}
	conversation := streamEvent(t, raw, "conversation")["id"].(string)
	_, saved := f.do("GET", "/api/v1/admin/assistant/conversations/"+conversation, f.admin, "")
	encoded, _ = json.Marshal(saved)
	if strings.Contains(string(encoded), "gateway-private-key") {
		t.Fatal("secret leaked into saved history")
	}
	entries, err := f.store.AuditLog(context.Background(), 0, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Action == "assets.save" && entry.Actor == "assistant:api-key" {
			found = true
		}
	}
	if !found {
		t.Fatalf("assistant actor missing from audit: %+v", entries)
	}
}

func TestAssistantDangerConfirmationSurvivesRestartAndCannotReplay(t *testing.T) {
	var assetID string
	f := newAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"tool_call_id"`) {
			gatewayText(w, "已处理")
		} else {
			gatewayCall(w, "delete-call", "assets_delete", fmt.Sprintf(`{"id":%q}`, assetID))
		}
	})
	asset, err := f.assets.Store.Save(context.Background(), assets.Asset{Kind: "domain", Name: "test.example"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assetID = asset.ID
	for _, approve := range []bool{false, true} {
		raw := f.stream("/chat", map[string]string{"message": "删除测试资产"})
		pending := streamEvent(t, raw, "confirm")
		id := pending["conversation_id"].(string)
		if _, err := f.assets.Store.Get(context.Background(), assetID, time.Now()); err != nil {
			t.Fatal("danger executed before confirmation")
		}
		restarted := &assistant.Module{DB: f.mod.DB, Vault: f.mod.Vault}
		restarted.BindActions(f.api.Actions(), f.api.runAction)
		ctx := action.WithActor(context.Background(), action.Owner("api-key"))
		if err := restarted.Confirm(ctx, id, "delete-call", approve, func(string, any) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := restarted.Confirm(ctx, id, "delete-call", approve, func(string, any) error { return nil }); err == nil {
			t.Fatal("replayed confirmation executed")
		}
		if !approve {
			if _, err := f.assets.Store.Get(context.Background(), assetID, time.Now()); err != nil {
				t.Fatal("rejected danger deleted asset")
			}
		}
	}
	if _, err := f.assets.Store.Get(context.Background(), assetID, time.Now()); err == nil {
		t.Fatal("approved danger was not executed")
	}
}

func TestAssistantAskKeepsCallerScopeAndRejectsRecursiveCalls(t *testing.T) {
	var rounds atomic.Int32
	f := newAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var request map[string]any
		json.Unmarshal(raw, &request)
		for _, entry := range request["tools"].([]any) {
			name := entry.(map[string]any)["function"].(map[string]any)["name"].(string)
			if strings.HasPrefix(name, "nodes_") || name == "assets_delete" || name == "assistant_ask" {
				t.Errorf("unauthorized tool advertised: %s", name)
			}
		}
		switch rounds.Add(1) {
		case 1:
			gatewayCall(w, "escape", "nodes_create", `{"name":"forbidden"}`)
		case 2:
			gatewayCall(w, "recursion", "assistant_ask", `{"message":"again"}`)
		default:
			gatewayText(w, "无权操作")
		}
	})
	key := f.key(apikey.Spec{Name: "limited", MaxRisk: action.Write, Modules: []string{"assistant", "assets"}})
	status, out := f.do("POST", "/api/v1/actions/assistant.ask", key, `{"message":"查看资产"}`)
	if status != 200 || rounds.Load() != 3 {
		t.Fatalf("ask: %d %+v rounds=%d", status, out, rounds.Load())
	}
	nodes, _ := f.store.Nodes(context.Background(), time.Now())
	if len(nodes) != 0 {
		t.Fatal("assistant escalated a scoped key")
	}
	id := out["result"].(map[string]any)["conversation_id"].(string)
	ctx := action.WithActor(context.Background(), action.Owner("other"))
	if err := f.mod.Chat(ctx, id, "读取别人的会话", func(string, any) error { return nil }); err == nil {
		t.Fatal("another actor read conversation")
	}
}

func TestAssistantStreamCancellationReachesGateway(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	f := newAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": waiting\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(canceled)
	})
	ctx, cancel := context.WithCancel(action.WithActor(context.Background(), action.Owner("owner")))
	finished := make(chan error, 1)
	go func() {
		finished <- f.mod.Chat(ctx, "", "等待", func(string, any) error { return nil })
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway never started")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway request leaked after cancellation")
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("chat goroutine leaked")
	}
}

func TestAssistantEncryptsConversationPayloadsAndUpgradesLegacyRows(t *testing.T) {
	f := newAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		gatewayCall(w, "private-call", "assets_delete", `{"id":"private-target-value"}`)
	})
	raw := f.stream("/chat", map[string]string{"message": "private-question-value"})
	id := streamEvent(t, raw, "conversation")["id"].(string)
	var title, state, message string
	if err := f.store.DB().QueryRow(`SELECT title,state FROM assistant_conversations WHERE id=?`, id).Scan(&title, &state); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().QueryRow(`SELECT content FROM assistant_messages WHERE conversation_id=? ORDER BY id LIMIT 1`, id).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(title+state+message, "private-question-value") || strings.Contains(state, "private-target-value") {
		t.Fatal("conversation payload or pending arguments persisted as plaintext")
	}
	if _, err := f.store.DB().Exec(`INSERT INTO assistant_conversations(id,owner,title,created_at,updated_at,state) VALUES('legacy','assistant:api-key','legacy-private-title',1,1,'{}');
INSERT INTO assistant_messages(conversation_id,role,content,at) VALUES('legacy','user','{"role":"user","content":"legacy-private-message"}',1)`); err != nil {
		t.Fatal(err)
	}
	if err := f.mod.ProtectHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.mod.ProtectHistory(context.Background()); err != nil {
		t.Fatal("encryption upgrade was not idempotent")
	}
	_, conversation := f.do("GET", "/api/v1/admin/assistant/conversations/legacy", f.admin, "")
	if conversation["title"] != "legacy-private-title" {
		t.Fatalf("legacy title not recovered: %+v", conversation)
	}
	f.store.DB().QueryRow(`SELECT content FROM assistant_messages WHERE conversation_id='legacy'`).Scan(&message)
	if strings.Contains(message, "legacy-private-message") {
		t.Fatal("legacy message remained plaintext")
	}
	status, _ := f.do("DELETE", "/api/v1/admin/assistant/conversations/"+id, f.admin, "")
	if status != 204 {
		t.Fatal("conversation deletion failed")
	}
	var count int
	f.store.DB().QueryRow(`SELECT count(*) FROM vault WHERE id LIKE ?`, "assistant:conversation:"+id+":%").Scan(&count)
	if count != 0 {
		t.Fatal("deleted conversation retained vault ciphertext")
	}
}

func TestAssistantKeepsRunningAfterThePageLeaves(t *testing.T) {
	release := make(chan struct{})
	var rounds atomic.Int32
	f := newAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if rounds.Add(1) == 1 {
			<-release
		}
		gatewayText(w, "后台完成")
	})
	body, _ := json.Marshal(map[string]string{"message": "离开页面后继续"})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/api/v1/admin/assistant/chat", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+f.admin)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4096)
	read, _ := resp.Body.Read(buffer)
	id := streamEvent(t, string(buffer[:read]), "conversation")["id"].(string)
	cancel()
	resp.Body.Close()
	status, conversation := f.do("GET", "/api/v1/admin/assistant/conversations/"+id, f.admin, "")
	if status != 200 || conversation["running"] != true {
		t.Fatalf("run not reported as running: %d %+v", status, conversation)
	}
	close(release)
	if err := f.mod.Wait(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	_, conversation = f.do("GET", "/api/v1/admin/assistant/conversations/"+id, f.admin, "")
	messages := conversation["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	if conversation["running"] == true || last["content"] != "后台完成" {
		t.Fatalf("background run did not finish and save: %+v", conversation)
	}
	req, _ = http.NewRequest("GET", f.server.URL+"/api/v1/admin/assistant/conversations/"+id+"/live", nil)
	req.Header.Set("Authorization", "Bearer "+f.admin)
	resp, err = f.server.Client().Do(req)
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("finished run still attachable: %v %v", err, resp)
	}
	resp.Body.Close()
}

func TestAssistantStopEndpointCancelsBackgroundRun(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	f := newAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": waiting\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(canceled)
	})
	result := make(chan string, 1)
	go func() { result <- f.stream("/chat", map[string]string{"message": "等待"}) }()
	<-started
	var id string
	f.store.DB().QueryRow(`SELECT id FROM assistant_conversations`).Scan(&id)
	if status, _ := f.do("POST", "/api/v1/admin/assistant/conversations/"+id+"/stop", f.admin, ""); status != 204 {
		t.Fatalf("stop: %d", status)
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not reach the gateway")
	}
	select {
	case raw := <-result:
		streamEvent(t, raw, "error")
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not end after stop")
	}
}

func TestAssistantPausesInfrastructureWrites(t *testing.T) {
	f := newAssistantFixture(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"tool_call_id"`) {
			gatewayText(w, "已处理")
		} else {
			gatewayCall(w, "settings-call", "settings_update", `{"channel":"webhook","webhook":"https://attacker.example/hook"}`)
		}
	})
	raw := f.stream("/chat", map[string]string{"message": "读一下情报"})
	pending := streamEvent(t, raw, "confirm")
	if pending["call_id"] != "settings-call" {
		t.Fatalf("settings.update was not paused: %s", raw)
	}
	if strings.Contains(raw, "event: tool_result") || strings.Contains(raw, "attacker.example/hook\"}\n") {
		t.Fatalf("settings.update ran before confirmation: %s", raw)
	}
	for _, name := range []string{"settings.update", "nodes.update", "docker.save_compose", "automation.create", "sites.create"} {
		if act, ok := f.api.Actions().Get(name); ok && !act.NeedsConfirm() {
			t.Errorf("%s does not need confirmation", name)
		}
	}
	for _, name := range []string{"todo.create", "finance.add_transaction", "settings.test_notification"} {
		if act, ok := f.api.Actions().Get(name); ok && act.NeedsConfirm() {
			t.Errorf("%s should run directly", name)
		}
	}
}

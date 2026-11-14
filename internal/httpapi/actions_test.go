package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/apikey"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/users"
	"github.com/leeska/Lodestar/web"
)

type actionFixture struct {
	t      *testing.T
	store  *storage.Store
	keys   *apikey.Store
	server *httptest.Server
	admin  string
}

func newActionFixture(t *testing.T) *actionFixture {
	t.Helper()
	usersMod := &users.Module{}
	mods := []module.Module{usersMod}
	s, err := storage.OpenWith(filepath.Join(t.TempDir(), "actions.db"), module.Migrations(mods))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	usersMod.Store = &users.Store{DB: s.DB()}
	admin := model.Secret()
	keys := &apikey.Store{DB: s.DB()}
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler(), Keys: keys, Modules: mods}).Handler())
	t.Cleanup(server.Close)
	return &actionFixture{t: t, store: s, keys: keys, server: server, admin: admin}
}

func (f *actionFixture) key(spec apikey.Spec) string {
	f.t.Helper()
	_, secret, err := f.keys.Create(context.Background(), spec)
	if err != nil {
		f.t.Fatal(err)
	}
	return secret
}

func (f *actionFixture) do(method, path, token, body string) (int, map[string]any) {
	f.t.Helper()
	req, _ := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.server.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func actionNames(out map[string]any) []string {
	names := []string{}
	list, _ := out["actions"].([]any)
	for _, a := range list {
		names = append(names, a.(map[string]any)["name"].(string))
	}
	return names
}

func TestActionKeysLimitRiskAndModules(t *testing.T) {
	f := newActionFixture(t)
	read := f.key(apikey.Spec{Name: "reader", MaxRisk: action.Read})
	code, out := f.do("GET", "/api/v1/actions", read, "")
	names := actionNames(out)
	if code != 200 || !slices.Contains(names, "nodes.list") || !slices.Contains(names, "overview.get") || slices.Contains(names, "nodes.create") || slices.Contains(names, "users.delete") {
		t.Fatalf("read key list: %d %v", code, names)
	}
	if code, out := f.do("POST", "/api/v1/actions/nodes.create", read, `{"name":"x"}`); code != 403 {
		t.Fatalf("read key wrote: %d %v", code, out)
	}
	if code, out := f.do("POST", "/api/v1/actions/overview.get", read, ``); code != 200 || out["result"] == nil {
		t.Fatalf("route-backed read: %d %v", code, out)
	}

	usersOnly := f.key(apikey.Spec{Name: "users-bot", MaxRisk: action.Write, Modules: []string{"users"}})
	if code, _ := f.do("POST", "/api/v1/actions/nodes.list", usersOnly, ``); code != 403 {
		t.Fatalf("module limit ignored: %d", code)
	}
	code, out = f.do("POST", "/api/v1/actions/users.create", usersOnly, `{"body":{"name":"friend","enabled":true}}`)
	if code != 200 {
		t.Fatalf("route-backed write: %d %v", code, out)
	}
	entries, err := f.store.AuditLog(context.Background(), 0, 20, "")
	if err != nil || len(entries) == 0 || entries[0].Actor != "key:users-bot" || entries[0].Action != "POST /api/v1/admin/users" {
		t.Fatalf("route-backed write not audited as the key: %+v %v", entries, err)
	}
	if code, out := f.do("POST", "/api/v1/actions/users.create", usersOnly, `{"body":{"name":""},"extra":1}`); code != 400 {
		t.Fatalf("unknown argument accepted: %d %v", code, out)
	}
}

func TestDangerActionsNeedConfirmation(t *testing.T) {
	f := newActionFixture(t)
	full := f.key(apikey.Spec{Name: "full", MaxRisk: action.Danger})
	code, out := f.do("POST", "/api/v1/actions/nodes.create", full, `{"name":"to-delete"}`)
	if code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	id := out["result"].(map[string]any)["id"].(string)
	code, out = f.do("POST", "/api/v1/actions/nodes.delete", full, `{"node_id":"`+id+`"}`)
	confirm, _ := out["confirm"].(map[string]any)
	if code != 428 || confirm["action"] != "nodes.delete" {
		t.Fatalf("danger without confirm: %d %v", code, out)
	}
	if nodes, _ := f.store.Nodes(context.Background(), time.Now()); len(nodes) != 1 {
		t.Fatal("node deleted without confirmation")
	}
	if code, out := f.do("POST", "/api/v1/actions/nodes.delete?confirm=true", full, `{"node_id":"`+id+`"}`); code != 200 {
		t.Fatalf("confirmed delete: %d %v", code, out)
	}
	if nodes, _ := f.store.Nodes(context.Background(), time.Now()); len(nodes) != 0 {
		t.Fatal("confirmed delete did nothing")
	}
	entries, _ := f.store.AuditLog(context.Background(), 0, 20, "")
	if len(entries) == 0 || entries[0].Actor != "key:full" || entries[0].Action != "action nodes.delete" {
		t.Fatalf("handler action not audited: %+v", entries)
	}
}

func TestRevokedExpiredAndForeignCallers(t *testing.T) {
	f := newActionFixture(t)
	now := time.Now()
	f.keys.Now = func() time.Time { return now }
	k, secret, _ := f.keys.Create(context.Background(), apikey.Spec{Name: "short", MaxRisk: action.Read, ExpiresAt: now.Add(time.Hour).Unix()})
	if code, _ := f.do("GET", "/api/v1/actions", secret, ""); code != 200 {
		t.Fatal("fresh key refused")
	}
	now = now.Add(2 * time.Hour)
	if code, _ := f.do("GET", "/api/v1/actions", secret, ""); code != 401 {
		t.Fatal("expired key accepted")
	}
	now = now.Add(-2 * time.Hour)
	f.keys.Revoke(context.Background(), k.ID)
	if code, _ := f.do("GET", "/api/v1/actions", secret, ""); code != 401 {
		t.Fatal("revoked key accepted")
	}
	for _, token := range []string{"", f.admin, "ls_000000000000_nope", "garbage"} {
		if code, _ := f.do("POST", "/api/v1/actions/nodes.list", token, ""); code != 401 {
			t.Fatalf("token %q: %d", token, code)
		}
	}
	// Access keys never reach the panel's own admin endpoints.
	live := f.key(apikey.Spec{Name: "live", MaxRisk: action.Danger})
	if code, _ := f.do("GET", "/api/v1/admin/nodes", live, ""); code != 401 {
		t.Fatalf("access key reached the admin API: %d", code)
	}
}

func TestMCPToolsComeFromTheRegistry(t *testing.T) {
	f := newActionFixture(t)
	read := f.key(apikey.Spec{Name: "reader", MaxRisk: action.Read})
	full := f.key(apikey.Spec{Name: "full", MaxRisk: action.Danger})
	rpc := func(token, method, params string) map[string]any {
		t.Helper()
		_, out := f.do("POST", "/mcp", token, `{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`)
		result, _ := out["result"].(map[string]any)
		if result == nil {
			t.Fatalf("%s: %v", method, out)
		}
		return result
	}
	tools := func(token string) []string {
		names := []string{}
		for _, tool := range rpc(token, "tools/list", `{}`)["tools"].([]any) {
			names = append(names, tool.(map[string]any)["name"].(string))
		}
		return names
	}
	if names := tools(read); !slices.Contains(names, "nodes_list") || slices.Contains(names, "nodes_delete") {
		t.Fatalf("read key tools: %v", names)
	}
	if names := tools(full); !slices.Contains(names, "nodes_delete") || !slices.Contains(names, "users_create") {
		t.Fatalf("full key tools: %v", names)
	}
	// Old tool names still resolve.
	if res := rpc(read, "tools/call", `{"name":"list_nodes","arguments":{}}`); res["isError"] == true {
		t.Fatalf("legacy name: %v", res)
	}
	res := rpc(full, "tools/call", `{"name":"nodes_delete","arguments":{"node_id":"nope"}}`)
	text, _ := json.Marshal(res)
	if res["isError"] != true || !strings.Contains(string(text), "confirm") {
		t.Fatalf("danger tool ran without confirm: %s", text)
	}
	if _, out := f.do("POST", "/mcp", read, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nodes_create","arguments":{"name":"x"}}}`); out["error"] == nil {
		t.Fatalf("read key could reach a write tool: %v", out)
	}
}

func TestOpenAPIAndSkillCoverEveryAction(t *testing.T) {
	f := newActionFixture(t)
	full := f.key(apikey.Spec{Name: "full", MaxRisk: action.Danger})
	code, doc := f.do("GET", "/api/v1/openapi.json", full, "")
	paths, _ := doc["paths"].(map[string]any)
	if code != 200 || paths["/api/v1/actions/nodes.list"] == nil || paths["/api/v1/actions/users.delete"] == nil {
		t.Fatalf("openapi: %d %d paths", code, len(paths))
	}
	req, _ := http.NewRequest("GET", f.server.URL+"/api/v1/admin/agent-kit/skill.zip", nil)
	req.Header.Set("Authorization", "Bearer "+f.admin)
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(resp.StatusCode, err)
	}
	files := map[string]string{}
	for _, file := range zr.File {
		r, _ := file.Open()
		body, _ := io.ReadAll(r)
		r.Close()
		files[file.Name] = string(body)
	}
	skill, ref := files["lodestar/SKILL.md"], files["lodestar/reference/actions.md"]
	if !strings.HasPrefix(skill, "---\nname: lodestar\n") || !strings.Contains(skill, f.server.URL) || files["lodestar/scripts/lodestar.sh"] == "" {
		t.Fatalf("skill: %q", skill[:min(200, len(skill))])
	}
	_, list := f.do("GET", "/api/v1/actions", full, "")
	for _, name := range actionNames(list) {
		if !strings.Contains(skill, "`"+name+"`") || !strings.Contains(ref, "### "+name+" ") {
			t.Fatalf("%s missing from the skill", name)
		}
	}
}

func TestLegacyMCPKeyBecomesAccessKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	old, err := storage.OpenWith(path, storage.CoreMigrations()[:2])
	if err != nil {
		t.Fatal(err)
	}
	legacy := "lodestar_mcp_" + model.Secret()
	value, _ := json.Marshal(map[string]any{"hash": model.Hash(legacy), "created_at": time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)})
	if err := old.SetSetting(context.Background(), "mcp_api_key", string(value)); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key, err := (&apikey.Store{DB: s.DB()}).Authenticate(context.Background(), legacy)
	if err != nil || key.ID != "mcp" || key.MaxRisk != action.Danger || key.CreatedAt != time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("legacy key: %+v %v", key, err)
	}
	if v, _ := s.Setting(context.Background(), "mcp_api_key"); v != "" {
		t.Fatal("old setting left behind")
	}
}

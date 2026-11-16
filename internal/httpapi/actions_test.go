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
	"github.com/leeska/Lodestar/internal/assets"
	"github.com/leeska/Lodestar/internal/automation"
	"github.com/leeska/Lodestar/internal/brief"
	"github.com/leeska/Lodestar/internal/calendar"
	"github.com/leeska/Lodestar/internal/certs"
	"github.com/leeska/Lodestar/internal/finance"
	"github.com/leeska/Lodestar/internal/intel"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/security"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/timeline"
	"github.com/leeska/Lodestar/internal/todo"
	"github.com/leeska/Lodestar/internal/users"
	"github.com/leeska/Lodestar/internal/vault"
	"github.com/leeska/Lodestar/web"
)

type actionFixture struct {
	t        *testing.T
	store    *storage.Store
	keys     *apikey.Store
	server   *httptest.Server
	admin    string
	security *security.Module
}

func newActionFixture(t *testing.T) *actionFixture {
	t.Helper()
	usersMod := &users.Module{}
	briefMod := &brief.Module{}
	intelMod := &intel.Module{}
	vaultMod := &vault.Module{}
	todoMod, calendarMod := &todo.Module{}, &calendar.Module{}
	financeMod := &finance.Module{}
	automationMod, timelineMod := &automation.Module{}, &timeline.Module{}
	assetsMod, securityMod := &assets.Module{}, &security.Module{}
	mods := []module.Module{usersMod, vaultMod, timelineMod, assetsMod, briefMod, intelMod, todoMod, calendarMod, financeMod, automationMod, securityMod}
	migrations := append(module.Migrations(mods), certs.Migrations()...)
	s, err := storage.OpenWith(filepath.Join(t.TempDir(), "actions.db"), migrations)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	usersMod.Store = &users.Store{DB: s.DB()}
	briefMod.DB = s.DB()
	secrets, err := vault.New(s.DB(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	vaultMod.Vault = secrets
	intelMod.DB, intelMod.Vault = s.DB(), secrets
	todoMod.DB, todoMod.Events, todoMod.Calendar = s.DB(), s, calendarMod
	calendarMod.DB, calendarMod.Vault = s.DB(), secrets
	financeMod.DB = s.DB()
	timelineMod.DB = s.DB()
	automationMod.DB, automationMod.Vault, automationMod.Timeline, automationMod.Events = s.DB(), secrets, timelineMod, s
	assetsMod.Store = &assets.Store{DB: s.DB(), Vault: secrets}
	securityMod.DB, securityMod.Assets, securityMod.Certs, securityMod.Events = s.DB(), assetsMod.Store, &certs.Store{DB: s.DB()}, s
	admin := model.Secret()
	keys := &apikey.Store{DB: s.DB()}
	server := httptest.NewServer((&API{Store: s, AdminToken: admin, Assets: web.Handler(), Keys: keys, Modules: mods}).Handler())
	t.Cleanup(server.Close)
	return &actionFixture{t: t, store: s, keys: keys, server: server, admin: admin, security: securityMod}
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

func TestTodoAndCalendarRouteBackedActions(t *testing.T) {
	fixture := newActionFixture(t)
	key := fixture.key(apikey.Spec{Name: "planner", MaxRisk: action.Write, Modules: []string{"todo", "calendar"}})
	if status, _ := fixture.do("POST", "/api/v1/actions/todo.from_ref", key, `{"body":{"ref":"event:1"}}`); status != 403 {
		t.Fatal("source conversion bypassed module scope", status)
	}
	status, output := fixture.do("POST", "/api/v1/actions/todo.create", key, `{"body":{"title":"检查备份","priority":2}}`)
	if status != 200 {
		t.Fatal(status, output)
	}
	id := output["result"].(map[string]any)["id"].(string)
	body, _ := json.Marshal(map[string]any{"id": id, "body": map[string]any{"done": true}})
	if status, output = fixture.do("POST", "/api/v1/actions/todo.complete", key, string(body)); status != 200 || output["result"].(map[string]any)["done_at"].(float64) == 0 {
		t.Fatal(status, output)
	}
	status, output = fixture.do("POST", "/api/v1/actions/calendar.range", key, `{"from":1794700800,"to":1794787200}`)
	if status != 200 || output["result"].(map[string]any)["events"] == nil {
		t.Fatal("calendar query arguments not forwarded", status, output)
	}
	if status, output = fixture.do("POST", "/api/v1/actions/calendar.add_source", key, `{"body":{"name":"私有日历","timezone":"UTC","enabled":false,"set_url":"https://example.com/private-calendar-token.ics"}}`); status != 200 {
		t.Fatal(status, output)
	}
	encoded, _ := json.Marshal(output)
	if strings.Contains(string(encoded), "private-calendar-token") {
		t.Fatal("calendar URL returned through action API")
	}
	if status, _ = fixture.do("GET", "/api/v1/admin/calendar/sources", key, ""); status != 401 {
		t.Fatal("action key entered admin routes", status)
	}
}

func TestFinanceActionsPreserveDecimalAmountsAndAllowBoundedCSV(t *testing.T) {
	fixture := newActionFixture(t)
	key := fixture.key(apikey.Spec{Name: "bookkeeper", MaxRisk: action.Write, Modules: []string{"finance"}})
	status, output := fixture.do("POST", "/api/v1/actions/finance.add_account", key, `{"body":{"name":"本地记账","kind":"bank","currency":"CNY","opening_balance":"0.10"}}`)
	if status != 200 {
		t.Fatal(status, output)
	}
	id := output["result"].(map[string]any)["id"].(string)
	body, _ := json.Marshal(map[string]any{"body": map[string]any{"account_id": id, "amount": "0.20"}})
	if status, output = fixture.do("POST", "/api/v1/actions/finance.add_transaction", key, string(body)); status != 200 {
		t.Fatal(status, output)
	}
	if status, output = fixture.do("POST", "/api/v1/actions/finance.accounts", key, "{}"); status != 200 || output["result"].(map[string]any)["net_worth"] != "0.30" {
		t.Fatal(status, output)
	}
	csvData := "date,amount,note\n" + strings.Repeat("2026-11-15,-0.01,"+strings.Repeat("x", 1500)+"\n", 200)
	body, _ = json.Marshal(map[string]any{"body": map[string]any{"account_id": id, "csv": csvData, "columns": map[string]string{"at": "date", "amount": "amount", "note": "note"}}})
	if len(body) <= 256<<10 {
		t.Fatal("fixture must exercise the default request limit")
	}
	if status, output = fixture.do("POST", "/api/v1/actions/finance.csv_preview", key, string(body)); status != 200 || output["result"].(map[string]any)["total"].(float64) != 200 {
		t.Fatal("bounded large CSV not supported", status, output)
	}
	if status, output = fixture.do("POST", "/api/v1/actions/finance.recurring", key, "{}"); status != 200 || len(output["result"].(map[string]any)["items"].([]any)) != 0 {
		t.Fatal("finance scope exposed other modules", status, output)
	}
	if status, _ := fixture.do("GET", "/api/v1/admin/finance/accounts", key, ""); status != 401 {
		t.Fatal("finance key accessed admin routes", status)
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

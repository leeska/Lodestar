package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/apikey"
)

func TestAutomationWebhookPrivacyAndDangerApproval(t *testing.T) {
	fixture := newActionFixture(t)
	key := fixture.key(apikey.Spec{Name: "automation-owner", MaxRisk: action.Danger, Modules: []string{"automation", "todo"}})
	status, out := fixture.do("POST", "/api/v1/actions/todo.create", key, `{"body":{"title":"待删除"}}`)
	if status != 200 {
		t.Fatal(status, out)
	}
	todoID := out["result"].(map[string]any)["id"].(string)
	body, _ := json.Marshal(map[string]any{"body": map[string]any{"name": "确认规则", "enabled": true, "trigger": "webhook", "action": "todo.delete", "set_args": map[string]any{"id": todoID}}})
	status, out = fixture.do("POST", "/api/v1/actions/automation.create", key, string(body))
	if status != 200 {
		t.Fatal(status, out)
	}
	rule := out["result"].(map[string]any)
	token := rule["webhook_token"].(string)
	if status, out = fixture.do("POST", "/api/v1/hooks/"+token, "", `{"private":"HOOK_SECRET"}`); status != 202 {
		t.Fatal(status, out)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), todoID) || strings.Contains(string(encoded), "HOOK_SECRET") {
		t.Fatal("public webhook leaked action data", out)
	}
	status, out = fixture.do("POST", "/api/v1/actions/automation.runs", key, "{}")
	if status != 200 {
		t.Fatal(status, out)
	}
	runs := out["result"].(map[string]any)["runs"].([]any)
	if len(runs) != 1 {
		t.Fatal(runs)
	}
	run := runs[0].(map[string]any)
	if run["status"] != "pending" {
		t.Fatal(run)
	}
	confirm, _ := json.Marshal(map[string]any{"id": run["id"], "body": map[string]bool{"approve": true}})
	if status, _ = fixture.do("POST", "/api/v1/actions/automation.confirm", key, string(confirm)); status != 428 {
		t.Fatal("danger lacked outer confirmation", status)
	}
	status, out = fixture.do("POST", "/api/v1/actions/automation.confirm?confirm=true", key, string(confirm))
	if status != 200 {
		t.Fatal(status, out)
	}
	status, out = fixture.do("POST", "/api/v1/actions/automation.confirm?confirm=true", key, string(confirm))
	if status != 409 {
		t.Fatal("replayed danger", status, out)
	}
	if status, _ = fixture.do("POST", "/api/v1/hooks/"+token, "", `{}`); status != 429 {
		t.Fatal("missing webhook cooldown", status)
	}
	limited := fixture.key(apikey.Spec{Name: "automation-only", MaxRisk: action.Write, Modules: []string{"automation"}})
	if status, _ = fixture.do("POST", "/api/v1/actions/automation.create", limited, string(body)); status != 403 {
		t.Fatal("automation escalated target scope", status)
	}
}

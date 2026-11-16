package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leeska/Lodestar/internal/account"
	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/apikey"
	"github.com/leeska/Lodestar/internal/module"
)

func TestSecurityCapabilitiesScopeAndDefaultExternalChecks(t *testing.T) {
	fixture := newActionFixture(t)
	owner := fixture.key(apikey.Spec{Name: "security-owner", MaxRisk: action.Danger, Modules: []string{"assets", "security"}})
	status, out := fixture.do("POST", "/api/v1/actions/assets.create", owner, `{"body":{"name":"仅本地测试","kind":"account","set_secrets":{"password":"PASSWORD_LOCAL_SENTINEL","recovery":"RECOVERY_LOCAL_SENTINEL"}}}`)
	if status != 200 {
		t.Fatal(status, out)
	}
	id := out["result"].(map[string]any)["id"].(string)
	body, _ := json.Marshal(map[string]any{"id": id, "body": map[string]any{"two_factor": "enabled", "recovery_location": "纸质保险柜", "review_now": true}})
	if status, out = fixture.do("POST", "/api/v1/actions/security.update_account", owner, string(body)); status != 200 {
		t.Fatal(status, out)
	}
	if status, out = fixture.do("POST", "/api/v1/actions/security.accounts", owner, "{}"); status != 200 {
		t.Fatal(status, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "PASSWORD_LOCAL_SENTINEL") || strings.Contains(string(raw), "RECOVERY_LOCAL_SENTINEL") {
		t.Fatal("secret exposed", string(raw))
	}
	limited := fixture.key(apikey.Spec{Name: "security-only", MaxRisk: action.Danger, Modules: []string{"security"}})
	if status, out = fixture.do("POST", "/api/v1/actions/security.accounts", limited, "{}"); status != 200 || len(out["result"].(map[string]any)["accounts"].([]any)) != 0 {
		t.Fatal("source scope bypass", status, out)
	}
	if status, _ = fixture.do("POST", "/api/v1/actions/security.update_settings?confirm=true", limited, `{"body":{"password_checks":true,"login_alerts":true}}`); status != 403 {
		t.Fatal("secret opt-in scope bypass", status)
	}
	if status, out = fixture.do("POST", "/api/v1/actions/security.settings", owner, "{}"); status != 200 || out["result"].(map[string]any)["password_checks"] != false || out["result"].(map[string]any)["dns_checks"] != false {
		t.Fatal("external checks not default off", status, out)
	}
	if status, _ = fixture.do("POST", "/api/v1/actions/security.check_passwords", owner, "{}"); status != 428 {
		t.Fatal("missing explicit confirmation", status)
	}
	status, out = fixture.do("GET", "/api/v1/admin/overview", fixture.admin, "")
	if status != 200 {
		t.Fatal(status, out)
	}
	found := false
	for _, raw := range out["cards"].([]any) {
		card := raw.(map[string]any)
		if card["id"] == "security" {
			found = true
			if card["value"] == "未授权" || !strings.Contains(card["note"].(string), "1 个账户") {
				t.Fatal("authenticated overview lost owner context", card)
			}
		}
	}
	if !found {
		t.Fatal("security card missing")
	}
}
func TestLoginSecurityHookUsesExistingSignedDevice(t *testing.T) {
	fixture := newActionFixture(t)
	dir := t.TempDir()
	if err := account.Save(dir, "admin", "security-test-password"); err != nil {
		t.Fatal(err)
	}
	handler := (&API{Store: fixture.store, AdminToken: fixture.admin, AccountDir: dir, Assets: http.NotFoundHandler(), Modules: []module.Module{fixture.security}}).Handler()
	login := func(password string, device *http.Cookie) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
		request := httptest.NewRequest("POST", "https://probe.example/api/v1/login", strings.NewReader(string(body)))
		request.Header.Set("Origin", "https://probe.example")
		request.Header.Set("Content-Type", "application/json")
		if device != nil {
			request.AddCookie(device)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := login("incorrect", nil); response.Code != 401 {
		t.Fatal(response.Code)
	}
	response := login("security-test-password", nil)
	if response.Code != 204 {
		t.Fatal(response.Code)
	}
	var device *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == deviceCookie {
			device = cookie
		}
	}
	if device == nil {
		t.Fatal("existing device cookie missing")
	}
	if response := login("security-test-password", device); response.Code != 204 {
		t.Fatal(response.Code)
	}
	var count int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM events WHERE kind='security'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("failed/new-device/known-device classification", count, err)
	}
}

package security

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/assets"
	"github.com/leeska/Lodestar/internal/certs"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/vault"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (fn roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }
func fixture(t *testing.T) (*Module, *storage.Store) {
	t.Helper()
	mod := &Module{}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "security.db"), module.Migrations([]module.Module{&vault.Module{}, &assets.Module{}, &certs.Module{}, mod}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	secrets, err := vault.New(store.DB(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	mod.DB, mod.Assets, mod.Certs, mod.Events = store.DB(), &assets.Store{DB: store.DB(), Vault: secrets}, &certs.Store{DB: store.DB()}, store
	mod.Now = func() time.Time { return time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC) }
	return mod, store
}
func owner() context.Context { return action.WithActor(context.Background(), action.Owner("owner")) }
func addAccount(t *testing.T, mod *Module) assets.Asset {
	t.Helper()
	item, err := mod.Assets.Save(owner(), assets.Asset{Kind: "account", Name: "测试账号", Fields: map[string]string{"provider": "本地测试"}, SetSecrets: map[string]string{"password": "PRIVATE_PASSWORD_LOCAL_ONLY", "recovery": "RECOVERY_CODE_MUST_REMAIN"}}, mod.now())
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func TestPasswordPrivacyWeeklyErrorsAndSecretVersion(t *testing.T) {
	mod, store := fixture(t)
	asset := addAccount(t, mod)
	calls := 0
	count := int64(7)
	fail := false
	hash := sha1.Sum([]byte("PRIVATE_PASSWORD_LOCAL_ONLY"))
	text := strings.ToUpper(hex.EncodeToString(hash[:]))
	mod.PasswordClient = &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Scheme != "https" || request.URL.Host != "api.pwnedpasswords.com" || request.URL.Path != "/range/"+text[:5] || request.URL.RawQuery != "" || request.Body != nil || request.Header.Get("Add-Padding") != "true" {
			t.Error("privacy boundary violated", request.URL)
		}
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Error("credentials attached")
		}
		status := 200
		if fail {
			status = 503
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(text[5:] + ":" + stringCount(count) + "\r\n" + strings.Repeat("0", 35) + ":0\r\n")), Header: make(http.Header)}, nil
	})}
	profile, err := mod.SaveProfile(owner(), Profile{AssetID: asset.ID, TwoFactor: "enabled", RecoveryLocation: "纸质保险柜", MonitorPassword: true, ReviewNow: true})
	if err != nil || profile.LastReview != mod.now().Unix() {
		t.Fatal(profile, err)
	}
	if _, err := mod.CheckPasswords(owner(), false); err != nil || calls != 0 {
		t.Fatal("default made external request", err, calls)
	}
	if err := mod.SaveSettings(owner(), Settings{PasswordChecks: true, LoginAlerts: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.CheckPasswords(owner(), false); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	if _, err := mod.CheckPasswords(owner(), false); err != nil || calls != 1 {
		t.Fatal("weekly dedup failed", err, calls)
	}
	accounts, err := mod.Accounts(owner())
	if err != nil || accounts[0].BreachCount != 7 || accounts[0].Outdated {
		t.Fatal(accounts, err)
	}
	fail = true
	if _, err := mod.CheckPasswords(owner(), true); err != nil {
		t.Fatal(err)
	}
	accounts, _ = mod.Accounts(owner())
	if !accounts[0].CheckError || accounts[0].BreachCount != 7 {
		t.Fatal("failure cleared evidence", accounts)
	}
	recovery, err := mod.Assets.Secret(owner(), asset.ID, "recovery")
	if err != nil || recovery != "RECOVERY_CODE_MUST_REMAIN" {
		t.Fatal("metadata replaced recovery secret", err)
	}
	if err := mod.Assets.Vault.Put(owner(), "asset:"+asset.ID+":password", "CHANGED_PASSWORD"); err != nil {
		t.Fatal(err)
	}
	accounts, _ = mod.Accounts(owner())
	if !accounts[0].Outdated {
		t.Fatal("changed password inherited old result", accounts)
	}
	fail = false
	count = 0
	newHash := sha1.Sum([]byte("CHANGED_PASSWORD"))
	text = strings.ToUpper(hex.EncodeToString(newHash[:]))
	if _, err := mod.CheckPasswords(owner(), true); err != nil {
		t.Fatal(err)
	}
	accounts, _ = mod.Accounts(owner())
	if accounts[0].BreachCount != 0 || accounts[0].Outdated {
		t.Fatal(accounts)
	}
	var events int
	if err := store.DB().QueryRow(`SELECT count(*) FROM events WHERE kind='security'`).Scan(&events); err != nil || events != 2 {
		t.Fatal("breach/recovery events", events, err)
	}
	if err := mod.Assets.Vault.Delete(owner(), "asset:"+asset.ID+":password"); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.CheckPasswords(owner(), true); err != nil {
		t.Fatal(err)
	}
}
func stringCount(count int64) string {
	if count == 7 {
		return "7"
	}
	return "0"
}
func TestMalformedRangeAndRedirectNeverReportClean(t *testing.T) {
	for _, body := range []string{"", strings.Repeat("A", 35) + ":-1", "not a range", strings.Repeat("Z", 35) + ":1"} {
		client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		if _, err := passwordCount(owner(), client, "local test"); err == nil {
			t.Fatal("invalid response accepted", body)
		}
	}
	mod, _ := fixture(t)
	calls := 0
	mod.PasswordClient = &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://example.com/not-allowed"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})}
	if _, err := passwordCount(owner(), mod.passwordClient(), "local test"); err == nil || calls != 1 {
		t.Fatal("redirect followed", calls, err)
	}
}

func TestPasswordChangeDuringRequestDiscardsStaleResult(t *testing.T) {
	mod, store := fixture(t)
	asset := addAccount(t, mod)
	if _, err := mod.SaveProfile(owner(), Profile{AssetID: asset.ID, TwoFactor: "enabled", MonitorPassword: true}); err != nil {
		t.Fatal(err)
	}
	if err := mod.SaveSettings(owner(), Settings{PasswordChecks: true}); err != nil {
		t.Fatal(err)
	}
	digest := sha1.Sum([]byte("PRIVATE_PASSWORD_LOCAL_ONLY"))
	hash := strings.ToUpper(hex.EncodeToString(digest[:]))
	mod.PasswordClient = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		if err := mod.Assets.Vault.Put(owner(), "asset:"+asset.ID+":password", "NEW_PASSWORD_DURING_REQUEST"); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(hash[5:] + ":7"))}, nil
	})}
	if _, err := mod.CheckPasswords(owner(), true); err != nil {
		t.Fatal(err)
	}
	profile, err := mod.profile(owner(), asset.ID)
	if err != nil || profile.CheckedAt != 0 || profile.BreachCount != -1 {
		t.Fatal("stale result persisted", profile, err)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM events WHERE kind='security'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("stale result sent alert", count, err)
	}
}
func TestSourceScopesAndServerOwnedReviewFields(t *testing.T) {
	mod, _ := fixture(t)
	asset := addAccount(t, mod)
	limited := action.WithActor(context.Background(), action.Actor{Name: "key:security", MaxRisk: action.Danger, Modules: []string{"security"}})
	accounts, err := mod.Accounts(limited)
	if err != nil || len(accounts) != 0 {
		t.Fatal("cross-scope account exposure", accounts, err)
	}
	if err := mod.SaveSettings(limited, Settings{PasswordChecks: true}); err == nil {
		t.Fatal("secret-check scope bypass")
	}
	if _, err := mod.SaveProfile(limited, Profile{AssetID: asset.ID, TwoFactor: "enabled"}); err == nil {
		t.Fatal("cross-scope profile write")
	}
	readAssets := action.WithActor(context.Background(), action.Actor{Name: "key:metadata", MaxRisk: action.Write, Modules: []string{"security", "assets"}})
	if _, err := mod.SaveProfile(readAssets, Profile{AssetID: asset.ID, TwoFactor: "enabled", MonitorPassword: true}); err == nil {
		t.Fatal("secret opt-in bypass")
	}
	profile, err := mod.SaveProfile(readAssets, Profile{AssetID: asset.ID, TwoFactor: "enabled", LastReview: 9999999999, CheckedAt: 9999999999, BreachCount: 0})
	if err != nil || profile.LastReview != 0 || profile.CheckedAt != 0 || profile.BreachCount != -1 {
		t.Fatal("client forged server-owned fields", profile, err)
	}
}
func TestDNSUnknownPrivateFailuresAndCertAllowlist(t *testing.T) {
	mod, _ := fixture(t)
	certificate, _ := json.Marshal(certs.Cert{ID: "cert", Domains: []string{"example.com"}, KeyPEM: "PRIVATE_KEY_SENTINEL", NotAfter: mod.now().Add(10 * 24 * time.Hour).Unix(), Status: certs.StatusValid})
	if _, err := mod.DB.Exec(`INSERT INTO certs(id,value,created_at) VALUES(?,?,?)`, "cert", string(certificate), mod.now().Unix()); err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"public.example.com", "private.example.com", "missing.example.com"} {
		if _, err := mod.Assets.Save(owner(), assets.Asset{Kind: "domain", Name: domain, ExpiresOn: "2026-11-20"}, mod.now()); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	mod.LookupIP = func(ctx context.Context, domain string) ([]net.IPAddr, error) {
		calls++
		switch domain {
		case "private.example.com":
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		case "missing.example.com":
			return nil, errors.New("missing")
		default:
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		}
	}
	if err := mod.CheckDNS(owner()); err != nil || calls != 0 {
		t.Fatal(err, calls)
	}
	if err := mod.SaveSettings(owner(), Settings{DNSChecks: true}); err != nil {
		t.Fatal(err)
	}
	if err := mod.CheckDNS(owner()); err != nil || calls != 3 {
		t.Fatal(err, calls)
	}
	if err := mod.CheckDNS(owner()); err != nil || calls != 3 {
		t.Fatal("DNS cache ignored", err, calls)
	}
	risks, err := mod.Risks(owner())
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, risk := range risks["domains"].([]DomainRisk) {
		statuses[risk.Domain] = risk.DNSStatus
		if risk.DaysLeft == nil || *risk.DaysLeft != 5 {
			t.Fatal(risk)
		}
	}
	if statuses["public.example.com"] != "ok" || statuses["private.example.com"] != "private" || statuses["missing.example.com"] != "error" {
		t.Fatal(statuses)
	}
	raw, _ := json.Marshal(risks)
	if strings.Contains(string(raw), "PRIVATE_KEY_SENTINEL") || strings.Contains(string(raw), "key_pem") {
		t.Fatal("certificate secret leaked", string(raw))
	}
}
func TestLoginDedupKnownDeviceAndRestart(t *testing.T) {
	mod, store := fixture(t)
	if err := mod.ObserveLogin(owner(), "owner", "source", true, true); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if err := mod.ObserveLogin(owner(), "owner", "source", false, false); err != nil {
			t.Fatal(err)
		}
	}
	restarted := &Module{DB: mod.DB, Events: store, Now: mod.Now}
	if err := restarted.ObserveLogin(owner(), "owner", "source", false, false); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ObserveLogin(owner(), "owner", "other", true, false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM events WHERE kind='security'`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if (model.Event{Kind: model.EventSecurity}).IsNodeEvent() {
		t.Fatal("security event treated as node")
	}
}

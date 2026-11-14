package assets

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/vault"
)

type events struct{ list []model.Event }

func (e *events) RecordEvent(_ context.Context, ev model.Event) error {
	e.list = append(e.list, ev)
	return nil
}

func fixture(t *testing.T) (*Module, *events, *vault.Vault, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	mods := []module.Module{&vault.Module{}, &Module{}}
	s, err := storage.OpenWith(filepath.Join(dir, "a.db"), module.Migrations(mods))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	key, err := vault.LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := vault.New(s.DB(), key)
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.Local)
	ev := &events{}
	m := &Module{Store: &Store{DB: s.DB(), Vault: v}, Events: ev, Now: func() time.Time { return now }}
	return m, ev, v, &now
}

func TestSecretsLiveInTheVault(t *testing.T) {
	m, _, v, now := fixture(t)
	ctx := context.Background()
	a, err := m.Store.Save(ctx, Asset{Kind: "account", Name: "GitHub", Fields: map[string]string{"username": "me"}, SetSecrets: map[string]string{"password": "hunter2"}}, *now)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Secrets["password"] || a.Fields["username"] != "me" {
		t.Fatalf("saved: %+v", a)
	}
	var raw string
	m.Store.DB.QueryRow(`SELECT fields FROM assets WHERE id = ?`, a.ID).Scan(&raw)
	var sealed []byte
	m.Store.DB.QueryRow(`SELECT data FROM vault`).Scan(&sealed)
	if strings.Contains(raw, "hunter2") || strings.Contains(string(sealed), "hunter2") {
		t.Fatal("secret stored in clear")
	}
	if got, _ := m.Store.Secret(ctx, a.ID, "password"); got != "hunter2" {
		t.Fatalf("secret: %q", got)
	}
	// Absent keys keep the secret; "" clears it.
	a.SetSecrets = nil
	a.Name = "GitHub 主号"
	if a, _ = m.Store.Save(ctx, a, *now); !a.Secrets["password"] {
		t.Fatal("secret lost on a plain edit")
	}
	a.SetSecrets = map[string]string{"password": ""}
	if a, _ = m.Store.Save(ctx, a, *now); a.Secrets["password"] {
		t.Fatal("secret not cleared")
	}
	// A secret field cannot be smuggled through fields, nor a field of another kind.
	if _, err := m.Store.Save(ctx, Asset{Kind: "account", Name: "x", Fields: map[string]string{"password": "p"}}, *now); err == nil {
		t.Fatal("secret accepted as a plain field")
	}
	if _, err := m.Store.Save(ctx, Asset{Kind: "domain", Name: "x", SetSecrets: map[string]string{"number": "1"}}, *now); err == nil {
		t.Fatal("foreign secret accepted")
	}
	a.SetSecrets = map[string]string{"recovery": "codes"}
	a, _ = m.Store.Save(ctx, a, *now)
	if err := m.Store.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := v.Has(ctx, "asset:"+a.ID+":"); len(left) != 0 {
		t.Fatal("secrets left after delete")
	}
}

func TestRemindersOncePerStageAndAfterRenewal(t *testing.T) {
	m, ev, _, now := fixture(t)
	ctx := context.Background()
	a, err := m.Store.Save(ctx, Asset{Kind: "domain", Name: "leeska.net", ExpiresOn: "2026-12-20"}, *now)
	if err != nil {
		t.Fatal(err)
	}
	step := func(at string) int {
		t.Helper()
		*now, _ = time.ParseInLocation("2006-01-02 15:04", at, time.Local)
		before := len(ev.list)
		if err := m.Remind(ctx); err != nil {
			t.Fatal(err)
		}
		return len(ev.list) - before
	}
	if n := step("2026-11-15 10:00"); n != 0 {
		t.Fatal("reminded 35 days ahead")
	}
	if n := step("2026-11-21 10:00"); n != 1 || !ev.list[0].Active || ev.list[0].Kind != model.EventAsset {
		t.Fatalf("30-day reminder: %d %+v", n, ev.list)
	}
	if n := step("2026-11-22 10:00"); n != 0 {
		t.Fatal("same stage twice")
	}
	if n := step("2026-12-14 10:00"); n != 1 {
		t.Fatal("7-day reminder missing")
	}
	if n := step("2026-12-21 09:00"); n != 1 || !strings.Contains(ev.list[len(ev.list)-1].Message, "已于") {
		t.Fatalf("expired reminder: %+v", ev.list[len(ev.list)-1])
	}
	a.ExpiresOn = "2027-12-20"
	m.Store.Save(ctx, a, *now)
	if n := step("2026-12-21 10:00"); n != 1 || ev.list[len(ev.list)-1].Active {
		t.Fatalf("renewal not announced: %+v", ev.list[len(ev.list)-1])
	}
	if n := step("2026-12-22 10:00"); n != 0 {
		t.Fatal("reminded again after renewal")
	}
	up, err := m.Upcoming(ctx, 400)
	if err != nil || len(up) != 1 || up[0].DaysLeft != 363 {
		t.Fatalf("upcoming: %+v %v", up, err)
	}
}

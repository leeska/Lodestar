package brief

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/notify"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/timeline"
)

type fakeChannel struct{}

func (fakeChannel) NotificationConfig(context.Context) (notify.Config, error) {
	return notify.Config{Channel: "webhook", Webhook: "https://example.com/hook"}, nil
}

type fakeSender struct {
	calls int
	fail  bool
}

func (s *fakeSender) SendSummary(context.Context, notify.Config, notify.Summary) error {
	s.calls++
	if s.fail {
		return errors.New("secret URL must not leak")
	}
	return nil
}

func fixture(t *testing.T) (*Module, *fakeSender) {
	t.Helper()
	m := &Module{}
	tl := &timeline.Module{}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "brief.db"), module.Migrations([]module.Module{tl, m}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m.DB, tl.DB, m.Timeline = store.DB(), store.DB(), tl
	sender := &fakeSender{}
	m.Channels, m.Sender = fakeChannel{}, sender
	return m, sender
}

func TestDailyOnceAcrossRestartsAndTimezone(t *testing.T) {
	m, sender := fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 7, 59, 0, 0, time.UTC)
	m.Now = func() time.Time { return now }
	if err := m.Save(ctx, Settings{Enabled: true, Time: "16:00", Timezone: "Asia/Singapore", Sections: []string{"events"}}); err != nil {
		t.Fatal(err)
	}
	m.Send(ctx, true)
	if sender.calls != 0 {
		t.Fatal("sent before the local scheduled time")
	}
	now = now.Add(time.Minute)
	if _, err := m.Send(ctx, true); err != nil {
		t.Fatal(err)
	}
	m.Send(ctx, true)
	restarted := &Module{DB: m.DB, Timeline: m.Timeline, Channels: m.Channels, Sender: sender, Now: m.Now}
	restarted.Send(ctx, true)
	if sender.calls != 1 {
		t.Fatalf("sent %d times in a local day", sender.calls)
	}
	now = now.Add(24 * time.Hour)
	restarted.Send(ctx, true)
	if sender.calls != 2 {
		t.Fatal("did not send the next day")
	}
	entries, err := m.Timeline.List(ctx, timeline.Query{Sources: []string{"brief"}, Before: now.Add(time.Hour).Unix()})
	if err != nil || len(entries) != 2 {
		t.Fatalf("timeline=%d err=%v", len(entries), err)
	}
}

func TestModelFailureFallsBackAndDeliveryFailureIsNotRepeated(t *testing.T) {
	m, sender := fixture(t)
	ctx := context.Background()
	m.Now = func() time.Time { return time.Date(2026, 11, 15, 16, 0, 0, 0, time.UTC) }
	m.Rewrite = func(context.Context, string) (string, error) { return "", errors.New("gateway unavailable") }
	m.BindOverview(func(context.Context) []module.Card {
		return []module.Card{{Title: "备份", Tone: "warn", Value: "未配置", Note: "需要设置异地备份"}}
	})
	if err := m.Save(ctx, Settings{Enabled: true, Time: "08:00", Timezone: "UTC", UseModel: true, Sections: []string{"overview"}}); err != nil {
		t.Fatal(err)
	}
	document, err := m.Preview(ctx)
	if err != nil || document.ModelUsed || !strings.Contains(document.Body, "需要设置异地备份") {
		t.Fatalf("fallback: %+v %v", document, err)
	}
	sender.fail = true
	if _, err := m.Send(ctx, true); err == nil || strings.Contains(err.Error(), "secret URL") {
		t.Fatal("delivery failure missing or leaked raw destination")
	}
	sender.fail = false
	m.Send(ctx, true)
	if sender.calls != 1 {
		t.Fatal("ambiguous delivery was automatically replayed")
	}
	if _, err := m.Send(ctx, false); err != nil || sender.calls != 2 {
		t.Fatal("explicit send could not retry")
	}
	m.Rewrite = func(context.Context, string) (string, error) { return "整理后的简报", nil }
	document, err = m.Preview(ctx)
	if err != nil || !document.ModelUsed || document.Body != "整理后的简报" {
		t.Fatal("configured model rewrite unused")
	}
}

func TestBriefRejectsInvalidSchedule(t *testing.T) {
	m, _ := fixture(t)
	for _, config := range []Settings{
		{Time: "25:00", Timezone: "UTC", Sections: []string{"nodes"}},
		{Time: "08:00", Timezone: "invalid/timezone", Sections: []string{"nodes"}},
		{Time: "08:00", Timezone: "UTC", Sections: []string{"unknown"}},
	} {
		if err := m.Save(context.Background(), config); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestDailyRepeatedDSTHourIsNotSentTwice(t *testing.T) {
	m, sender := fixture(t)
	now := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	m.Now = func() time.Time { return now }
	ctx := context.Background()
	if err := m.Save(ctx, Settings{Enabled: true, Time: "01:00", Timezone: "America/New_York", Sections: []string{"events"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Send(ctx, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err := m.Send(ctx, true); err != nil || sender.calls != 1 {
		t.Fatalf("repeated local hour sent twice: calls=%d err=%v", sender.calls, err)
	}
}

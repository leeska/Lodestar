package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leeska/Lodestar/internal/connector"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/vault"
)

func document(body string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Lodestar Tests//EN\r\n" + strings.ReplaceAll(strings.TrimSpace(body), "\n", "\r\n") + "\r\nEND:VCALENDAR\r\n")
}
func TestRecurringEventsRespectDSTExceptionsAndCancellation(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	body := document(`BEGIN:VEVENT
UID:meeting
DTSTART;TZID=America/New_York:20261031T090000
DTEND;TZID=America/New_York:20261031T100000
RRULE:FREQ=DAILY;COUNT=4
EXDATE;TZID=America/New_York:20261101T090000
SUMMARY:原会议
END:VEVENT
BEGIN:VEVENT
UID:meeting
RECURRENCE-ID;TZID=America/New_York:20261102T090000
DTSTART;TZID=America/New_York:20261102T110000
DTEND;TZID=America/New_York:20261102T120000
SUMMARY:改期会议
END:VEVENT
BEGIN:VEVENT
UID:meeting
RECURRENCE-ID;TZID=America/New_York:20261103T090000
STATUS:CANCELLED
END:VEVENT`)
	from := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	list, err := parseCalendar(context.Background(), body, location, from, from.AddDate(0, 0, 7))
	if err != nil || len(list) != 2 {
		t.Fatalf("events=%+v err=%v", list, err)
	}
	if list[0].At.UTC().Hour() != 13 || list[1].At.UTC().Hour() != 16 || list[1].Title != "改期会议" {
		t.Fatal(list)
	}
	var details occurrence
	json.Unmarshal(list[1].Data, &details)
	if details.EndAt-list[1].At.Unix() != 3600 {
		t.Fatal(details)
	}
	repeated, err := parseCalendar(context.Background(), body, location, from, from.AddDate(0, 0, 7))
	if err != nil || repeated[1].Key != list[1].Key {
		t.Fatal("unstable occurrence key", repeated, err)
	}
}
func TestAllDayRecurrenceUsesLocalDaysAcrossDST(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	body := document(`BEGIN:VEVENT
UID:day
DTSTART;VALUE=DATE:20261101
DTEND;VALUE=DATE:20261102
RRULE:FREQ=DAILY;COUNT=2
SUMMARY:全天
END:VEVENT`)
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, location)
	list, err := parseCalendar(context.Background(), body, location, from, from.AddDate(0, 0, 3))
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	for index, item := range list {
		var details occurrence
		json.Unmarshal(item.Data, &details)
		want := int64(86400)
		if index == 0 {
			want = 90000
		}
		if !details.AllDay || details.EndAt-item.At.Unix() != want {
			t.Fatal(index, item, details)
		}
	}
}
func TestDenseRecurrenceAndMalformedCalendarFailSafely(t *testing.T) {
	from := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	for _, body := range []string{
		"BEGIN:VEVENT\nUID:one\nDTSTART:20261115T090000Z\nRRULE:FREQ=SECONDLY\nEND:VEVENT",
		"BEGIN:VEVENT\nUID:one\nDTSTART:private-secret-value\nEND:VEVENT",
	} {
		_, err := parseCalendar(context.Background(), document(body), time.UTC, from, from.AddDate(0, 0, 7))
		if err == nil || strings.Contains(err.Error(), "private-secret-value") {
			t.Fatal("invalid input accepted or echoed", err)
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (transport roundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}
func TestDailyWindowRefreshAndConditionalRequests(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	requests := 0
	source := &icalSource{Now: func() time.Time { return now }}
	source.Client = &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 2 {
			if request.Header.Get("If-None-Match") != "same" {
				t.Fatal("conditional header missing")
			}
			return &http.Response{StatusCode: 304, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if request.Header.Get("If-None-Match") != "" {
			t.Fatal("daily expansion must refresh unchanged ICS")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Etag": []string{"same"}}, Body: io.NopCloser(strings.NewReader(string(document("BEGIN:VEVENT\nUID:one\nDTSTART:20261115T090000Z\nSUMMARY:测试\nEND:VEVENT"))))}, nil
	})}
	config := json.RawMessage(`{"timezone":"UTC"}`)
	first, err := source.FetchCached(context.Background(), config, "https://example.com/private.ics", connector.Cache{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.FetchCached(context.Background(), config, "https://example.com/private.ics", first.Cache)
	if err != nil || !second.NotModified {
		t.Fatal(second, err)
	}
	now = now.AddDate(0, 0, 1)
	if _, err := source.FetchCached(context.Background(), config, "https://example.com/private.ics", second.Cache); err != nil {
		t.Fatal(err)
	}
}

type fakeSource struct {
	items []connector.Item
	err   error
}

func (*fakeSource) ID() string             { return "fixture" }
func (*fakeSource) Name() string           { return "fixture" }
func (*fakeSource) Schema() map[string]any { return nil }
func (source *fakeSource) Fetch(context.Context, json.RawMessage, string) ([]connector.Item, error) {
	return source.items, source.err
}
func TestPrivateURLAndFailedSyncPreservePreviousSnapshot(t *testing.T) {
	ctx := context.Background()
	mod := &Module{}
	store, err := storage.OpenWith(filepath.Join(t.TempDir(), "calendar.db"), module.Migrations([]module.Module{&vault.Module{}, mod}))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mod.DB = store.DB()
	mod.Vault, err = vault.New(store.DB(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	privateURL := "https://example.com/never-expose-private-token.ics"
	source, err := mod.SaveSource(ctx, Source{Name: "我的日历", Timezone: "UTC", Enabled: true, SetURL: &privateURL, LastFetch: 253402300799, LastError: "client-injected-error"})
	if err != nil || !source.HasURL {
		t.Fatal(source, err)
	}
	if source.LastFetch != 0 || source.LastError != "" {
		t.Fatal("client changed read-only source state", source)
	}
	encoded, _ := json.Marshal(source)
	if strings.Contains(string(encoded), "private-token") || source.SetURL != nil {
		t.Fatal("URL returned by source API")
	}
	var plaintextCount int
	if err := store.DB().QueryRow(`SELECT count(*) FROM vault WHERE CAST(data AS TEXT) LIKE '%never-expose%'`).Scan(&plaintextCount); err != nil {
		t.Fatal(err)
	}
	if plaintextCount != 0 {
		t.Fatal("URL stored unencrypted")
	}
	start := time.Now().Truncate(time.Hour)
	data, _ := json.Marshal(occurrence{EndAt: start.Add(time.Hour).Unix()})
	fake := &fakeSource{items: []connector.Item{{Key: "one", Title: "会议", At: start, Data: data}}}
	mod.Fetcher = fake
	if err := mod.FetchOne(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	list, err := mod.Range(ctx, start.Unix(), start.Add(24*time.Hour).Unix())
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	fake.err = errors.New(privateURL)
	if err := mod.FetchOne(ctx, source.ID); err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatal("failed sync leaked URL", err)
	}
	list, err = mod.Range(ctx, start.Unix(), start.Add(24*time.Hour).Unix())
	if err != nil || len(list) != 1 {
		t.Fatal("last successful snapshot lost", list, err)
	}
	fake.err = nil
	fake.items = nil
	if err := mod.FetchOne(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = mod.Range(ctx, start.Unix(), start.Add(24*time.Hour).Unix())
	if len(list) != 0 {
		t.Fatal("removed event retained")
	}
	if err := mod.Delete(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	available, err := mod.Vault.Has(ctx, "calendar:")
	if err != nil || len(available) != 0 {
		t.Fatal("private URL retained after deletion", available, err)
	}
}

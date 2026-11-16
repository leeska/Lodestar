package intel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/leeska/Lodestar/internal/connector"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (run roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return run(request) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestRSSAndAtomNormalizeStableItems(t *testing.T) {
	feeds := []string{
		`<rss version="2.0"><channel><item><guid>stable</guid><title>更新</title><link>/entry</link><pubDate>Sun, 15 Nov 2026 08:00:00 +0800</pubDate><description><![CDATA[<b>正文</b>]]></description></item></channel></rss>`,
		`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>stable</id><title>更新</title><link rel="alternate" href="/entry"/><published>2026-11-15T08:00:00+08:00</published><summary>正文</summary></entry></feed>`,
	}
	for _, feed := range feeds {
		items, err := parseFeed([]byte(feed), "https://example.com/feed")
		if err != nil || len(items) != 1 || items[0].URL != "https://example.com/entry" || items[0].Body != "正文" || items[0].At.UTC().Hour() != 0 {
			t.Fatalf("feed parsing: %+v %v", items, err)
		}
		again, _ := parseFeed([]byte(feed), "https://example.com/feed")
		if again[0].Key != items[0].Key {
			t.Fatal("feed item keys were not stable")
		}
	}
	if _, err := parseFeed([]byte(`<!DOCTYPE rss [<!ENTITY xxe SYSTEM "http://127.0.0.1/private">]><rss><channel><item><title>&xxe;</title></item></channel></rss>`), "https://example.com"); err == nil {
		t.Fatal("external entity was accepted")
	}
}

func TestConditionalFetchAndWebpageChangeDetection(t *testing.T) {
	body := `<html><span id="price">$5</span></html>`
	notModified := false
	transport := roundTrip(func(request *http.Request) (*http.Response, error) {
		if notModified {
			if request.Header.Get("If-None-Match") != `"etag"` {
				t.Error("ETag was not sent")
			}
			return response(304, ""), nil
		}
		result := response(200, body)
		result.Header.Set("ETag", `"etag"`)
		return result, nil
	})
	source := &sourceConnector{kind: "webpage", client: &http.Client{Transport: transport}}
	config := json.RawMessage(`{"url":"https://example.com/price","pattern":"price\">([^<]+)"}`)
	first, err := source.FetchCached(context.Background(), config, "", connector.Cache{})
	if err != nil || len(first.Items) != 1 || first.Items[0].Body != "$5" {
		t.Fatalf("initial page: %+v %v", first, err)
	}
	same, err := source.FetchCached(context.Background(), config, "", first.Cache)
	if err != nil || len(same.Items) != 0 {
		t.Fatal("unchanged extracted content produced an item")
	}
	body = `<html><span id="price">$7</span><p>other change</p></html>`
	changed, err := source.FetchCached(context.Background(), config, "", first.Cache)
	if err != nil || len(changed.Items) != 1 || changed.Items[0].Body != "$7" {
		t.Fatal("page change was missed")
	}
	notModified = true
	cached, err := source.FetchCached(context.Background(), config, "", changed.Cache)
	if err != nil || !cached.NotModified || cached.Cache.Fingerprint != changed.Cache.Fingerprint {
		t.Fatal("304 lost the source fingerprint")
	}
}

func TestGitHubFetchUsesFixedOriginAndNeverEchoesToken(t *testing.T) {
	source := &sourceConnector{kind: "github_release", client: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "api.github.com" || request.URL.Path != "/repos/owner/repo/releases" || request.Header.Get("Authorization") != "Bearer fake-private-token" || request.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Error("GitHub request contract")
		}
		return response(401, `{"message":"fake-private-token must never be echoed"}`), nil
	})}}
	_, err := source.Fetch(context.Background(), json.RawMessage(`{"repository":"owner/repo"}`), "fake-private-token")
	if err == nil || strings.Contains(err.Error(), "fake-private-token") || !strings.Contains(err.Error(), "401") {
		t.Fatal("upstream error leaked its body or lost its status")
	}
	items, err := parseReleases([]byte(`[{"id":42,"tag_name":"v1.2.3","html_url":"https://github.com/owner/repo/releases/tag/v1.2.3","published_at":"2026-11-15T00:00:00Z","body":"release notes"},{"id":43,"draft":true}]`))
	if err != nil || len(items) != 1 || items[0].Key != "release:42" {
		t.Fatalf("release parsing: %+v %v", items, err)
	}
}

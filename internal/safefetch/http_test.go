package safefetch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestRedirectsDoNotForwardPrivateSourceReferrers(t *testing.T) {
	original, _ := http.NewRequest("GET", "https://calendar.example/private-calendar-token.ics", nil)
	redirected, _ := http.NewRequest("GET", "https://cdn.example/calendar.ics", nil)
	redirected.Header.Set("Referer", original.URL.String())
	if err := Client().CheckRedirect(redirected, []*http.Request{original}); err != nil {
		t.Fatal(err)
	}
	if redirected.Header.Get("Referer") != "" {
		t.Fatal("private source URL forwarded as Referer")
	}
	original.Header.Set("Authorization", "Bearer fixture-only")
	if err := Client().CheckRedirect(redirected, []*http.Request{original}); err == nil {
		t.Fatal("cross-host credential redirect allowed")
	}
}

type resolver []netip.Addr

func (r resolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) { return r, nil }

func TestPublicAddressAndURLBoundaries(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "100.100.100.200", "169.254.169.254", "192.168.1.1", "198.18.0.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2001:db8::1"} {
		if Public(netip.MustParseAddr(value)) {
			t.Errorf("unsafe address accepted: %s", value)
		}
	}
	for _, value := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !Public(netip.MustParseAddr(value)) {
			t.Errorf("public address rejected: %s", value)
		}
	}
	for _, value := range []string{"file:///etc/passwd", "http://user:pass@example.com/", "http://127.0.0.1/", "http://[::1]/"} {
		if _, err := Validate(value); err == nil {
			t.Errorf("unsafe URL accepted: %s", value)
		}
	}
}

func TestDNSAnswersAreValidatedAndConnectionIsPinned(t *testing.T) {
	calls := []string{}
	connect := func(_ context.Context, _ string, endpoint string) (net.Conn, error) {
		calls = append(calls, endpoint)
		return nil, errors.New("test does not connect")
	}
	_, err := Dial(context.Background(), "tcp", "feed.example:443", resolver{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("10.0.0.1")}, connect)
	if err == nil || len(calls) != 0 {
		t.Fatal("mixed public/private DNS answers were dialed")
	}
	Dial(context.Background(), "tcp", "feed.example:443", resolver{netip.MustParseAddr("1.1.1.1")}, connect)
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "1.1.1.1:") {
		t.Fatalf("connection was not pinned to validated address: %v", calls)
	}
}

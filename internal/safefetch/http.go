package safefetch

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var denied = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001:db8::/32"),
}

func Public(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || address.Zone() != "" || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range denied {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func Validate(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Fragment != "" || len(raw) > 4000 {
		return nil, fmt.Errorf("请输入不含账号和片段的 HTTP 或 HTTPS 地址")
	}
	if address, err := netip.ParseAddr(parsed.Hostname()); err == nil && !Public(address) {
		return nil, fmt.Errorf("不允许抓取内网、保留或本机地址")
	}
	return parsed, nil
}

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

func Dial(ctx context.Context, network, endpoint string, resolver Resolver, connect func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return nil, fmt.Errorf("无效的目标地址")
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("目标域名解析失败")
	}
	for _, address := range addresses {
		if !Public(address) {
			return nil, fmt.Errorf("目标解析到了内网或保留地址，已拒绝连接")
		}
	}
	for _, address := range addresses {
		connection, err := connect(ctx, network, net.JoinHostPort(address.String(), port))
		if err == nil {
			return connection, nil
		}
	}
	return nil, fmt.Errorf("目标连接失败")
}

func Client() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, endpoint string) (net.Conn, error) {
			return Dial(ctx, network, endpoint, net.DefaultResolver, dialer.DialContext)
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   2,
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: transport, CheckRedirect: func(request *http.Request, previous []*http.Request) error {
		request.Header.Del("Referer")
		if len(previous) >= 3 {
			return fmt.Errorf("重定向过多")
		}
		if _, err := Validate(request.URL.String()); err != nil {
			return err
		}
		original := previous[0]
		if original.Header.Get("Authorization") != "" && !strings.EqualFold(request.URL.Host, original.URL.Host) {
			return fmt.Errorf("拒绝把凭据转发到其他站点")
		}
		if original.URL.Scheme == "https" && request.URL.Scheme != "https" {
			return fmt.Errorf("拒绝从 HTTPS 降级")
		}
		return nil
	}}
}

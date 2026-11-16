package intel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/leeska/Lodestar/internal/connector"
	"github.com/leeska/Lodestar/internal/safefetch"
)

var publicHTTP = safefetch.Client()
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9_.-]{1,100}$`)
var markupPattern = regexp.MustCompile(`<[^>]*>`)

type sourceConfig struct {
	URL        string `json:"url,omitempty"`
	Repository string `json:"repository,omitempty"`
	Pattern    string `json:"pattern,omitempty"`
}

type sourceConnector struct {
	kind   string
	client *http.Client
}

func init() {
	for _, kind := range []string{"rss", "github_release", "webpage"} {
		connector.Register(&sourceConnector{kind: kind})
	}
}

func (s *sourceConnector) ID() string { return s.kind }
func (s *sourceConnector) Name() string {
	return map[string]string{"rss": "RSS / Atom", "github_release": "GitHub Release", "webpage": "网页变化"}[s.kind]
}
func (s *sourceConnector) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"url":        map[string]string{"type": "string", "description": "RSS 或网页的 HTTP / HTTPS 地址"},
		"repository": map[string]string{"type": "string", "description": "GitHub 仓库 owner/repo"},
		"pattern":    map[string]string{"type": "string", "description": "网页提取正则，可选；第一捕获组作为内容"},
	}}
}
func (s *sourceConnector) Fetch(ctx context.Context, config json.RawMessage, secret string) ([]connector.Item, error) {
	result, err := s.FetchCached(ctx, config, secret, connector.Cache{})
	return result.Items, err
}

func validateConfig(kind string, raw json.RawMessage) (sourceConfig, error) {
	var config sourceConfig
	if kind == "ical" {
		return config, fmt.Errorf("私有日历请在日程页管理")
	}
	if err := json.Unmarshal(raw, &config); err != nil || len(raw) > 8000 {
		return config, fmt.Errorf("源配置不是有效的 JSON")
	}
	if kind == "github_release" {
		if !repositoryPattern.MatchString(config.Repository) || strings.HasSuffix(config.Repository, "/.") || strings.HasSuffix(config.Repository, "/..") {
			return config, fmt.Errorf("仓库须为 owner/repo")
		}
	} else if _, err := safefetch.Validate(config.URL); err != nil {
		return config, err
	}
	if len(config.Pattern) > 2000 {
		return config, fmt.Errorf("提取正则过长")
	}
	if config.Pattern != "" {
		if _, err := regexp.Compile(config.Pattern); err != nil {
			return config, fmt.Errorf("提取正则无效")
		}
	}
	return config, nil
}

func (s *sourceConnector) FetchCached(ctx context.Context, raw json.RawMessage, secret string, cache connector.Cache) (connector.Result, error) {
	result := connector.Result{Cache: cache}
	config, err := validateConfig(s.kind, raw)
	if err != nil {
		return result, err
	}
	endpoint := config.URL
	if s.kind == "github_release" {
		endpoint = "https://api.github.com/repos/" + config.Repository + "/releases?per_page=50"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return result, fmt.Errorf("源请求创建失败")
	}
	req.Header.Set("User-Agent", "Lodestar")
	if cache.ETag != "" {
		req.Header.Set("If-None-Match", cache.ETag)
	}
	if cache.Modified != "" {
		req.Header.Set("If-Modified-Since", cache.Modified)
	}
	if s.kind == "github_release" {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
	}
	client := s.client
	if client == nil {
		client = publicHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("抓取失败，请检查地址、网络或是否被安全策略阻止")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 304 {
		result.NotModified = true
		return result, nil
	}
	if resp.StatusCode != 200 {
		return result, fmt.Errorf("源返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return result, fmt.Errorf("源内容读取失败或超过 1 MB")
	}
	result.Cache.ETag, result.Cache.Modified = resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	if len(result.Cache.ETag) > 2000 {
		result.Cache.ETag = ""
	}
	if len(result.Cache.Modified) > 200 {
		result.Cache.Modified = ""
	}
	switch s.kind {
	case "rss":
		result.Items, err = parseFeed(body, config.URL)
	case "github_release":
		result.Items, err = parseReleases(body)
	case "webpage":
		text := string(body)
		if config.Pattern != "" {
			match := regexp.MustCompile(config.Pattern).FindStringSubmatch(text)
			if len(match) == 0 {
				return result, fmt.Errorf("页面中没有匹配提取正则的内容")
			}
			text = match[0]
			if len(match) > 1 {
				text = match[1]
			}
		}
		text = strings.Join(strings.Fields(markupPattern.ReplaceAllString(text, "")), " ")
		if text == "" {
			return result, fmt.Errorf("提取内容为空")
		}
		fingerprint := digest(text)
		result.Cache.Fingerprint = fingerprint
		if fingerprint != cache.Fingerprint {
			title := "网页内容变化"
			if cache.Fingerprint == "" {
				title = "网页初始内容"
			}
			result.Items = []connector.Item{{Key: fingerprint + ":" + time.Now().UTC().Format("20060102T150405.000000000"), Title: title, URL: config.URL, At: time.Now(), Body: textLimit(text, 8000)}}
		}
	}
	if err != nil {
		return result, err
	}
	if len(result.Items) > 50 {
		result.Items = result.Items[:50]
	}
	return result, nil
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func textLimit(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func feedURL(base, link string) string {
	root, err := url.Parse(base)
	if err != nil {
		return ""
	}
	target, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return ""
	}
	target = root.ResolveReference(target)
	if (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
		return ""
	}
	target.User = nil
	return textLimit(target.String(), 4000)
}

func feedTime(value string) time.Time {
	for _, layout := range []string{time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, "Mon, 2 Jan 2006 15:04:05 -0700", "2006-01-02"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return time.Now()
}

func parseFeed(body []byte, base string) ([]connector.Item, error) {
	var feed struct {
		XMLName xml.Name
		Channel struct {
			Items []struct {
				ID          string `xml:"guid"`
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				Description string `xml:"description"`
				Content     string `xml:"encoded"`
				Date        string `xml:"pubDate"`
			} `xml:"item"`
		} `xml:"channel"`
		Entries []struct {
			ID      string `xml:"id"`
			Title   string `xml:"title"`
			Updated string `xml:"updated"`
			Date    string `xml:"published"`
			Body    string `xml:"content"`
			Summary string `xml:"summary"`
			Links   []struct {
				Href string `xml:"href,attr"`
				Rel  string `xml:"rel,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("RSS / Atom XML 无法解析")
	}
	items := []connector.Item{}
	switch feed.XMLName.Local {
	case "rss":
		for _, entry := range feed.Channel.Items {
			content := entry.Description
			if entry.Content != "" {
				content = entry.Content
			}
			key := entry.ID
			if key == "" {
				key = entry.Link
			}
			if key == "" {
				key = entry.Title + content
			}
			items = append(items, connector.Item{Key: digest(key), Title: textLimit(entry.Title, 300), URL: feedURL(base, entry.Link), At: feedTime(entry.Date), Body: textLimit(markupPattern.ReplaceAllString(content, ""), 8000)})
		}
	case "feed":
		for _, entry := range feed.Entries {
			link := ""
			for _, candidate := range entry.Links {
				if candidate.Rel == "" || candidate.Rel == "alternate" {
					link = candidate.Href
					break
				}
			}
			content := entry.Body
			if content == "" {
				content = entry.Summary
			}
			date := entry.Date
			if date == "" {
				date = entry.Updated
			}
			key := entry.ID
			if key == "" {
				key = link + entry.Title
			}
			items = append(items, connector.Item{Key: digest(key), Title: textLimit(entry.Title, 300), URL: feedURL(base, link), At: feedTime(date), Body: textLimit(markupPattern.ReplaceAllString(content, ""), 8000)})
		}
	default:
		return nil, fmt.Errorf("地址没有返回 RSS 或 Atom")
	}
	return items, nil
}

func parseReleases(body []byte) ([]connector.Item, error) {
	var releases []struct {
		ID    int64     `json:"id"`
		Name  string    `json:"name"`
		Tag   string    `json:"tag_name"`
		URL   string    `json:"html_url"`
		At    time.Time `json:"published_at"`
		Body  string    `json:"body"`
		Draft bool      `json:"draft"`
	}
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("GitHub Release 返回格式无效")
	}
	items := []connector.Item{}
	for _, release := range releases {
		if release.Draft {
			continue
		}
		title := release.Name
		if title == "" {
			title = release.Tag
		}
		items = append(items, connector.Item{Key: fmt.Sprintf("release:%d", release.ID), Title: textLimit(title, 300), URL: feedURL("https://github.com", release.URL), At: release.At, Body: textLimit(release.Body, 8000)})
	}
	return items, nil
}

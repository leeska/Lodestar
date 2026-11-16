package calendar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"
	"github.com/leeska/Lodestar/internal/connector"
	"github.com/leeska/Lodestar/internal/safefetch"
	"github.com/teambition/rrule-go"
)

type occurrence struct {
	UID      string `json:"uid"`
	EndAt    int64  `json:"end_at"`
	AllDay   bool   `json:"all_day"`
	Location string `json:"location"`
}

type icalSource struct {
	Client *http.Client
	Now    func() time.Time
}

func init()                      { connector.Register(&icalSource{}) }
func (*icalSource) ID() string   { return "ical" }
func (*icalSource) Name() string { return "只读 iCal 日历" }
func (*icalSource) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"timezone": map[string]any{"type": "string"}}}
}
func (source *icalSource) Fetch(ctx context.Context, config json.RawMessage, secret string) ([]connector.Item, error) {
	result, err := source.FetchCached(ctx, config, secret, connector.Cache{})
	return result.Items, err
}

func (source *icalSource) FetchCached(ctx context.Context, config json.RawMessage, secret string, cache connector.Cache) (connector.Result, error) {
	result := connector.Result{Cache: cache}
	if _, err := safefetch.Validate(secret); err != nil {
		return result, errors.New("日历地址无效或不是公网 HTTP/HTTPS 地址")
	}
	var input struct {
		Timezone string `json:"timezone"`
	}
	if json.Unmarshal(config, &input) != nil {
		return result, errors.New("日历时区配置无效")
	}
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return result, errors.New("日历时区无效")
	}
	now := time.Now()
	if source.Now != nil {
		now = source.Now()
	}
	day := now.In(location).Format("2006-01-02")
	if cache.Fingerprint != day {
		cache.ETag, cache.Modified = "", ""
	}
	request, err := http.NewRequestWithContext(ctx, "GET", secret, nil)
	if err != nil {
		return result, errors.New("日历地址无效")
	}
	request.Header.Set("User-Agent", "Lodestar-Calendar/1")
	request.Header.Set("Accept", "text/calendar")
	if cache.ETag != "" {
		request.Header.Set("If-None-Match", cache.ETag)
	}
	if cache.Modified != "" {
		request.Header.Set("If-Modified-Since", cache.Modified)
	}
	client := source.Client
	if client == nil {
		client = safefetch.Client()
	}
	response, err := client.Do(request)
	if err != nil {
		return result, errors.New("读取日历失败，请检查公网地址和网络")
	}
	defer response.Body.Close()
	if response.StatusCode == 304 {
		result.NotModified = true
		return result, nil
	}
	if response.StatusCode != 200 {
		return result, fmt.Errorf("日历服务器返回 HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return result, errors.New("读取日历内容失败")
	}
	if len(body) > 1<<20 {
		return result, errors.New("日历文件超过 1 MB")
	}
	items, err := parseCalendar(ctx, body, location, now.AddDate(0, 0, -30), now.AddDate(0, 0, 180))
	if err != nil {
		return result, err
	}
	result.Items = items
	result.Cache.Fingerprint = day
	result.Cache.ETag, result.Cache.Modified = response.Header.Get("ETag"), response.Header.Get("Last-Modified")
	if len(result.Cache.ETag) > 2000 {
		result.Cache.ETag = ""
	}
	if len(result.Cache.Modified) > 200 {
		result.Cache.Modified = ""
	}
	return result, nil
}

func propertyText(event ical.Event, name string) string {
	value, _ := event.Props.Text(name)
	return value
}

func calendarDateList(event ical.Event, name string, location *time.Location) ([]time.Time, error) {
	out := []time.Time{}
	for _, property := range event.Props.Values(name) {
		for _, value := range strings.Split(property.Value, ",") {
			property.Value = value
			date, err := property.DateTime(location)
			if err != nil {
				return nil, errors.New("日历包含不支持的日期或时区")
			}
			out = append(out, date)
		}
	}
	return out, nil
}

func eventSpan(event ical.Event, location *time.Location) (time.Time, time.Duration, int, bool, error) {
	start, err := event.DateTimeStart(location)
	if err != nil || start.IsZero() {
		return start, 0, 0, false, errors.New("日历条目缺少有效开始时间")
	}
	end, err := event.DateTimeEnd(location)
	if err != nil {
		return start, 0, 0, false, errors.New("日历结束时间无效")
	}
	property := event.Props.Get("DTSTART")
	allDay := property.ValueType() == ical.ValueDate || len(property.Value) == 8
	days := 0
	if allDay {
		if event.Props.Get("DTEND") == nil && event.Props.Get("DURATION") == nil {
			end = start.AddDate(0, 0, 1)
		}
		startDate := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		endDate := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
		days = int(endDate.Sub(startDate) / (24 * time.Hour))
	}
	duration := end.Sub(start)
	if duration < 0 || duration > 366*24*time.Hour || days < 0 {
		return start, 0, 0, false, errors.New("日历持续时间无效或超过一年")
	}
	if duration == 0 {
		duration = time.Second
	}
	return start, duration, days, allDay, nil
}

func parseCalendar(ctx context.Context, body []byte, location *time.Location, from, to time.Time) ([]connector.Item, error) {
	document, err := ical.NewDecoder(bytes.NewReader(body)).Decode()
	if err != nil {
		return nil, errors.New("无法解析 iCal 文件")
	}
	events := document.Events()
	if len(events) > 5000 {
		return nil, errors.New("日历条目超过 5000 条")
	}
	masters := map[string]ical.Event{}
	overrides := map[string]map[int64]ical.Event{}
	for _, event := range events {
		uid := propertyText(event, "UID")
		if uid == "" || len(uid) > 1000 {
			return nil, errors.New("日历条目缺少有效 UID")
		}
		if property := event.Props.Get("RECURRENCE-ID"); property != nil {
			if property.Params.Get("RANGE") != "" {
				return nil, errors.New("暂不支持修改后续全部重复日程（RANGE）")
			}
			at, err := property.DateTime(location)
			if err != nil {
				return nil, errors.New("重复日程例外时间无效")
			}
			if overrides[uid] == nil {
				overrides[uid] = map[int64]ical.Event{}
			}
			overrides[uid][at.Unix()] = event
		} else {
			if _, exists := masters[uid]; exists {
				return nil, errors.New("日历包含重复的主条目 UID")
			}
			masters[uid] = event
		}
	}
	output := []connector.Item{}
	seen := map[string]bool{}
	budget := 50000
	appendEvent := func(event ical.Event, uid string, at time.Time, duration time.Duration, days int, allDay bool, key int64, fallback string) error {
		if strings.EqualFold(propertyText(event, "STATUS"), "CANCELLED") {
			return nil
		}
		end := at.Add(duration)
		if allDay && days > 0 {
			end = at.AddDate(0, 0, days)
		}
		if !at.Before(to) || !end.After(from) {
			return nil
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", uid, key)))
		stable := hex.EncodeToString(digest[:16])
		if seen[stable] {
			return nil
		}
		seen[stable] = true
		title := propertyText(event, "SUMMARY")
		if title == "" {
			title = fallback
		}
		if title == "" {
			title = "未命名日程"
		}
		if len([]rune(title)) > 300 {
			title = string([]rune(title)[:300])
		}
		note := propertyText(event, "DESCRIPTION")
		if len([]rune(note)) > 4000 {
			note = string([]rune(note)[:4000])
		}
		place := propertyText(event, "LOCATION")
		if len([]rune(place)) > 500 {
			place = string([]rune(place)[:500])
		}
		data, _ := json.Marshal(occurrence{UID: uid, EndAt: end.Unix(), AllDay: allDay, Location: place})
		output = append(output, connector.Item{Key: stable, Title: title, At: at, Body: note, Data: data})
		if len(output) > 2000 {
			return errors.New("同步窗口内超过 2000 条日程")
		}
		return nil
	}
	for uid, event := range masters {
		if err := ctx.Err(); err != nil {
			return nil, errors.New("日历同步已超时")
		}
		if strings.EqualFold(propertyText(event, "STATUS"), "CANCELLED") {
			continue
		}
		start, duration, days, allDay, err := eventSpan(event, location)
		if err != nil {
			return nil, err
		}
		exceptions, err := calendarDateList(event, "EXDATE", start.Location())
		if err != nil {
			return nil, err
		}
		excluded := map[int64]bool{}
		for _, date := range exceptions {
			excluded[date.Unix()] = true
		}
		dates, err := calendarDateList(event, "RDATE", start.Location())
		if err != nil {
			return nil, err
		}
		dates = append(dates, start)
		if property := event.Props.Get("RRULE"); property != nil {
			options, err := rrule.StrToROptionInLocation(property.Value, start.Location())
			if err != nil {
				return nil, errors.New("重复日程规则无效")
			}
			if options.Freq > rrule.DAILY || options.Interval > 366 || max(1, len(options.Byhour))*max(1, len(options.Byminute))*max(1, len(options.Bysecond)) > 64 {
				return nil, errors.New("重复日程过于密集；仅支持每日、每周、每月、每年规则")
			}
			options.Dtstart = start
			if options.Until.IsZero() || options.Until.After(to) {
				options.Until = to
			}
			rule, err := rrule.NewRRule(*options)
			if err != nil {
				return nil, errors.New("重复日程规则无效")
			}
			iterator := rule.Iterator()
			for {
				budget--
				if budget < 0 {
					return nil, errors.New("重复日程计算超过安全上限")
				}
				if budget%100 == 0 && ctx.Err() != nil {
					return nil, errors.New("日历同步已超时")
				}
				date, ok := iterator()
				if !ok || !date.Before(to) {
					break
				}
				if date.Add(duration).After(from) {
					dates = append(dates, date)
				}
			}
		}
		for _, date := range dates {
			key := date.Unix()
			if _, exists := overrides[uid][key]; exists {
				continue
			}
			if excluded[key] {
				continue
			}
			if err := appendEvent(event, uid, date, duration, days, allDay, key, ""); err != nil {
				return nil, err
			}
		}
	}
	for uid, exceptions := range overrides {
		for key, event := range exceptions {
			if strings.EqualFold(propertyText(event, "STATUS"), "CANCELLED") {
				continue
			}
			fallback := ""
			if master, exists := masters[uid]; exists {
				if strings.EqualFold(propertyText(master, "STATUS"), "CANCELLED") {
					continue
				}
				fallback = propertyText(master, "SUMMARY")
			}
			start, duration, days, allDay, err := eventSpan(event, location)
			if err != nil {
				return nil, err
			}
			if err := appendEvent(event, uid, start, duration, days, allDay, key, fallback); err != nil {
				return nil, err
			}
		}
	}
	slices.SortFunc(output, func(first, second connector.Item) int {
		if first.At.Before(second.At) {
			return -1
		}
		if first.At.After(second.At) {
			return 1
		}
		return strings.Compare(first.Key, second.Key)
	})
	return output, nil
}

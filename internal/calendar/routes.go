package calendar

import (
	"net/http"
	"strconv"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/httpjson"
	"github.com/leeska/Lodestar/internal/module"
)

const base = "/api/v1/admin/calendar"

func fail(w http.ResponseWriter, err error) {
	if typed, ok := action.AsError(err); ok {
		httpjson.Error(w, typed.Status, typed.Message)
	} else {
		httpjson.Fail(w, err)
	}
}
func (m *Module) Routes(routes module.Routes) {
	routes.Admin("GET "+base+"/sources", func(w http.ResponseWriter, r *http.Request) {
		list, err := m.Sources(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"sources": list})
	})
	for _, method := range []string{"POST", "PUT"} {
		path := base + "/sources"
		if method == "PUT" {
			path += "/{id}"
		}
		routes.Admin(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			var source Source
			if !httpjson.Decode(w, r, &source) {
				return
			}
			source.ID = r.PathValue("id")
			saved, err := m.SaveSource(r.Context(), source)
			if err != nil {
				fail(w, err)
				return
			}
			httpjson.Write(w, 200, saved)
		})
	}
	routes.Admin("DELETE "+base+"/sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.Delete(r.Context(), r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
	routes.Admin("POST "+base+"/sources/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		if err := m.FetchOne(r.Context(), r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]bool{"synced": true})
	})
	routes.Admin("GET "+base+"/range", func(w http.ResponseWriter, r *http.Request) {
		from, err := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		if err != nil {
			httpjson.Error(w, 400, "from 须为 Unix 秒")
			return
		}
		to, err := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
		if err != nil {
			httpjson.Error(w, 400, "to 须为 Unix 秒")
			return
		}
		list, err := m.Range(r.Context(), from, to)
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"events": list})
	})
	routes.Admin("GET "+base+"/today", func(w http.ResponseWriter, r *http.Request) {
		now := m.now()
		if zone := r.URL.Query().Get("timezone"); zone != "" {
			location, err := time.LoadLocation(zone)
			if err != nil {
				httpjson.Error(w, 400, "时区无效")
				return
			}
			now = now.In(location)
		}
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		list, err := m.Range(r.Context(), start.Unix(), start.AddDate(0, 0, 1).Unix())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"events": list})
	})
}
func (*Module) Actions() []action.Action {
	id := action.Str("日历来源 id")
	body := action.Body("name、timezone（IANA）、enabled、every_minutes（5–10080）；set_url 为私有 HTTP/HTTPS ics 地址，存保险箱，不传保留，空字符串清除并暂停")
	return []action.Action{
		action.R("calendar.today", action.Read, "GET", base+"/today", "今天的日程", "按指定时区读取今天的日程。", map[string]any{"timezone": action.Str("IANA 时区，默认服务本地时区")}, "timezone"),
		action.R("calendar.range", action.Read, "GET", base+"/range", "指定范围日程", "读取与时间范围重叠的日程，最多 366 天。", map[string]any{"from": action.Int("开始 Unix 秒"), "to": action.Int("结束 Unix 秒，开区间")}, "from", "to"),
		action.R("calendar.sources", action.Read, "GET", base+"/sources", "日历来源", "列出来源和状态，不返回私有地址。", nil),
		action.R("calendar.add_source", action.Write, "POST", base+"/sources", "添加只读日历", "添加私有 ics 订阅。", map[string]any{"body": body}),
		action.R("calendar.update_source", action.Write, "PUT", base+"/sources/{id}", "修改只读日历", "修改来源设置，不写入远端日历。", map[string]any{"id": id, "body": body}),
		action.R("calendar.delete_source", action.Danger, "DELETE", base+"/sources/{id}", "删除日历来源", "删除订阅、凭据和本地同步日程，不能恢复。", map[string]any{"id": id}),
		action.R("calendar.sync", action.Write, "POST", base+"/sources/{id}/sync", "立即同步日历", "只读抓取一次日历，失败保留上次日程。", map[string]any{"id": id}),
	}
}

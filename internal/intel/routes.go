package intel

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/connector"
	"github.com/leeska/Lodestar/internal/httpjson"
	"github.com/leeska/Lodestar/internal/module"
)

const base = "/api/v1/admin/intel"

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
		kinds := []map[string]any{}
		for _, kind := range connector.All() {
			if kind.ID() == "ical" {
				continue
			}
			kinds = append(kinds, map[string]any{"id": kind.ID(), "name": kind.Name(), "schema": kind.Schema()})
		}
		httpjson.Write(w, 200, map[string]any{"sources": list, "connectors": kinds})
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
		id := r.PathValue("id")
		release, ok := m.enter(id)
		if !ok {
			httpjson.Error(w, 409, "这个源正在抓取，请稍后再删除")
			return
		}
		defer release()
		result, err := m.DB.ExecContext(r.Context(), `DELETE FROM intel_sources WHERE id=?`, id)
		if err != nil {
			fail(w, err)
			return
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			httpjson.Error(w, 404, "源不存在")
			return
		}
		if err := m.Vault.Delete(r.Context(), "intel:"+id+":token"); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	})
	routes.Admin("POST "+base+"/sources/{id}/fetch", func(w http.ResponseWriter, r *http.Request) {
		if err := m.FetchOne(r.Context(), r.PathValue("id"), false); err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]bool{"fetched": true})
	})
	routes.Admin("GET "+base+"/items", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		var before, after int64
		var limit int
		var err error
		if query.Get("before") != "" {
			before, err = strconv.ParseInt(query.Get("before"), 10, 64)
		}
		if err == nil && query.Get("after") != "" {
			after, err = strconv.ParseInt(query.Get("after"), 10, 64)
		}
		if err == nil && query.Get("limit") != "" {
			limit, err = strconv.Atoi(query.Get("limit"))
		}
		if err != nil || before < 0 || after < 0 || len(query.Get("q")) > 600 {
			httpjson.Error(w, 400, "筛选参数无效")
			return
		}
		items, err := m.Items(r.Context(), Query{Source: query.Get("source"), Q: strings.TrimSpace(query.Get("q")), Before: before, After: after, Unread: query.Get("unread") == "1", Starred: query.Get("starred") == "1", Limit: limit})
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"items": items})
	})
	routes.Admin("POST "+base+"/items/{id}/mark", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpjson.Error(w, 400, "条目 id 无效")
			return
		}
		var input struct {
			Read    *bool `json:"read"`
			Starred *bool `json:"starred"`
		}
		if !httpjson.Decode(w, r, &input) {
			return
		}
		if input.Read == nil && input.Starred == nil {
			httpjson.Error(w, 400, "请选择已读或星标状态")
			return
		}
		result, err := m.DB.ExecContext(r.Context(), `UPDATE intel_items SET read=coalesce(?,read),starred=coalesce(?,starred) WHERE id=?`, input.Read, input.Starred, id)
		if err != nil {
			fail(w, err)
			return
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			httpjson.Error(w, 404, "条目不存在")
			return
		}
		w.WriteHeader(204)
	})
	routes.Admin("GET "+base+"/rules", func(w http.ResponseWriter, r *http.Request) {
		list, err := m.Rules(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"rules": list})
	})
	for _, method := range []string{"POST", "PUT"} {
		path := base + "/rules"
		if method == "PUT" {
			path += "/{id}"
		}
		routes.Admin(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			var rule Rule
			if !httpjson.Decode(w, r, &rule) {
				return
			}
			rule.ID = 0
			if r.Method == "PUT" {
				id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
				if err != nil || id <= 0 {
					httpjson.Error(w, 400, "规则 id 无效")
					return
				}
				rule.ID = id
			}
			saved, err := m.SaveRule(r.Context(), rule)
			if err != nil {
				fail(w, err)
				return
			}
			httpjson.Write(w, 200, saved)
		})
	}
	routes.Admin("DELETE "+base+"/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpjson.Error(w, 400, "规则 id 无效")
			return
		}
		result, err := m.DB.ExecContext(r.Context(), `DELETE FROM intel_rules WHERE id=?`, id)
		if err != nil {
			fail(w, err)
			return
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			httpjson.Error(w, 404, "规则不存在")
			return
		}
		w.WriteHeader(204)
	})
}

func (*Module) Actions() []action.Action {
	id := action.Str("源或条目的 id，来自列表")
	ruleID := action.Int("规则 id")
	source := action.Body("connector: rss / github_release / webpage，name，enabled，every_minutes（1–10080）；config 中 rss 用 url，github_release 用 repository（owner/repo），webpage 用 url 和可选 pattern（正则，第一捕获组为内容）；set_secret 为可选 GitHub token，留空删除、不传保留")
	rule := action.Body("name、keywords（字符串数组，任意一个命中）、sources（源 id 数组，空为全部）、action（notify / brief / ignore）；按规则 id 从小到大，第一个匹配生效")
	return []action.Action{
		action.R("intel.sources", action.Read, "GET", base+"/sources", "情报源", "列出情报源与连接器，不返回令牌。", nil),
		action.R("intel.items", action.Read, "GET", base+"/items", "情报条目", "按关键词、源、时间、未读和星标筛选。", map[string]any{"source": id, "q": action.Str("关键词"), "after": action.Int("采集时间下界，Unix 秒"), "before": action.Int("条目 id 上界，用于翻页"), "unread": action.Str("1 表示只看未读"), "starred": action.Str("1 表示只看星标"), "limit": action.Int("最多 200")}, "source", "q", "after", "before", "unread", "starred", "limit"),
		action.R("intel.add_source", action.Write, "POST", base+"/sources", "添加情报源", "添加一个情报源；启用后由后台周期抓取。", map[string]any{"body": source}),
		action.R("intel.update_source", action.Write, "PUT", base+"/sources/{id}", "修改情报源", "替换源配置；先读取并保留未修改字段，令牌不传则保留。", map[string]any{"id": id, "body": source}),
		action.R("intel.delete_source", action.Danger, "DELETE", base+"/sources/{id}", "删除情报源", "删除源、凭据和全部条目（包含星标），不能恢复。", map[string]any{"id": id}),
		action.R("intel.fetch", action.Write, "POST", base+"/sources/{id}/fetch", "立即抓取", "使用该源的出口规则立即抓取一次。", map[string]any{"id": id}),
		action.R("intel.mark", action.Write, "POST", base+"/items/{id}/mark", "标记条目", "修改 read 或 starred 布尔状态。", map[string]any{"id": action.Int("条目 id"), "body": action.Body("read 或 starred 布尔值，至少一项")}),
		action.R("intel.rules", action.Read, "GET", base+"/rules", "情报规则", "列出关键词与推送规则。", nil),
		action.R("intel.add_rule", action.Write, "POST", base+"/rules", "添加情报规则", "创建关键词规则，空关键词匹配全部。", map[string]any{"body": rule}),
		action.R("intel.update_rule", action.Write, "PUT", base+"/rules/{id}", "修改情报规则", "替换一条关键词规则。", map[string]any{"id": ruleID, "body": rule}),
		action.R("intel.delete_rule", action.Danger, "DELETE", base+"/rules/{id}", "删除情报规则", "删除规则不能恢复，不改变已有条目的处置状态。", map[string]any{"id": ruleID}),
	}
}

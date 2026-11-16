package httpapi

// 能力注册表的 HTTP 一侧：访问密钥认证、/api/v1/actions、/mcp、OpenAPI，以及把「走路由」的能力
// 转成一次进程内的后台请求（同一套校验与审计）。见 internal/action。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/apikey"
	"github.com/leeska/Lodestar/internal/mcp"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
)

type sourceKey struct{}

// internalKey marks an in-process request made for a route-backed action; its
// value is the actor. Only runAction sets it, so it cannot come from outside.
type internalKey struct{}

func internalActor(r *http.Request) (action.Actor, bool) {
	a, ok := r.Context().Value(internalKey{}).(action.Actor)
	return a, ok
}

// buildActions is the registry: the MCP package's tools, the core catalog and
// every module's own actions.
func (a *API) buildActions(probe *mcp.Server) *action.Registry {
	list := append(probe.LegacyActions(), coreActions()...)
	for _, m := range a.Modules {
		if p, ok := m.(module.Actioner); ok {
			list = append(list, p.Actions()...)
		}
	}
	return action.NewRegistry(list...)
}

// Actions is the registry (tests and the Skill use it).
func (a *API) Actions() *action.Registry { return a.actions }

// actionAuth lets in a caller of the action API: an access key, the admin
// token, or the signed-in owner's browser session (writes from the browser
// must be same-origin). The caller and the client address go into the
// context.
func (a *API) actionAuth(next http.HandlerFunc) http.HandlerFunc { return a.keyAuth(next, true) }

// keyAuth is actionAuth; session false refuses the browser session (MCP is
// for agents with their own key only).
func (a *API) keyAuth(next http.HandlerFunc, session bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var actor action.Actor
		if !sameOrigin(r) {
			write(w, 403, map[string]string{"error": "请求来源不受信任"})
			return
		}
		// 带 Bearer 的只认访问密钥：数据目录里的旧管理密钥不能用在这里，外部 agent 一律用能单独吊销的密钥。
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
			if a.Keys == nil {
				write(w, 401, map[string]string{"error": "访问密钥无效、已吊销或已过期"})
				return
			} else {
				key, err := a.Keys.Authenticate(r.Context(), token)
				if errors.Is(err, apikey.ErrUnknown) {
					write(w, 401, map[string]string{"error": err.Error()})
					return
				}
				if err != nil {
					fail(w, err)
					return
				}
				a.Keys.Touch(context.WithoutCancel(r.Context()), key.ID, requestSource(r))
				actor = key.Actor()
			}
		} else if session && r.Header.Get("Authorization") == "" && a.isAdmin(r) {
			actor = action.Owner(a.actor(r))
		} else {
			w.Header().Set("WWW-Authenticate", `Bearer realm="lodestar"`)
			write(w, 401, map[string]string{"error": "需要访问密钥：Authorization: Bearer ls_…"})
			return
		}
		ctx := action.WithActor(r.Context(), actor)
		ctx = context.WithValue(ctx, sourceKey{}, requestSource(r))
		next(w, r.WithContext(ctx))
	}
}

// runAction is the one way an action runs: the caller is checked against the
// action (risk, module, confirmation), then the handler or the route runs.
func (a *API) runAction(ctx context.Context, name string, in json.RawMessage, confirm bool) (any, error) {
	act, ok := a.actions.Get(name)
	if !ok {
		return nil, &action.Error{Status: 404, Message: "没有这个能力：" + name}
	}
	actor, ok := action.ActorFrom(ctx)
	if !ok {
		return nil, &action.Error{Status: 401, Message: "没有调用者"}
	}
	if err := action.Check(actor, act, in, confirm); err != nil {
		return nil, err
	}
	if act.Route != nil {
		// 路由那边的后台写操作自己会记审计（调用者取自 internalKey）。
		return a.routeAction(ctx, actor, act, in)
	}
	out, err := act.Handler(ctx, in)
	if act.Risk >= action.Write {
		status := 200
		if err != nil {
			status = 500
			if e, ok := action.AsError(err); ok {
				status = e.Status
			}
		}
		source, _ := ctx.Value(sourceKey{}).(string)
		if auditErr := a.Store.Audit(context.WithoutCancel(ctx), model.AuditEntry{Actor: actor.Name, Source: source, Action: "action " + act.Name, Status: status}); auditErr != nil {
			return out, err
		}
	}
	return out, err
}

// bufferedResponse collects an in-process response.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }
func (b *bufferedResponse) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}
func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = 200
	}
	return b.body.Write(p)
}

// routeAction fills the route from the input and sends it through the panel's
// own handler as the actor.
func (a *API) routeAction(ctx context.Context, actor action.Actor, act action.Action, in json.RawMessage) (any, error) {
	fields := map[string]json.RawMessage{}
	if len(in) > 0 && string(in) != "null" {
		if err := json.Unmarshal(in, &fields); err != nil {
			return nil, action.Invalid("参数要是一个 JSON 对象")
		}
	}
	text := func(key string) (string, bool, error) {
		raw, ok := fields[key]
		if !ok {
			return "", false, nil
		}
		delete(fields, key)
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s, true, nil
		}
		var n json.Number
		if json.Unmarshal(raw, &n) == nil {
			return n.String(), true, nil
		}
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			return fmt.Sprint(b), true, nil
		}
		return "", true, action.Invalid("%s 要是字符串或数字", key)
	}
	route := act.Route
	path := route.Path
	for {
		start := strings.Index(path, "{")
		if start < 0 {
			break
		}
		end := strings.Index(path[start:], "}")
		key := path[start+1 : start+end]
		value, ok, err := text(key)
		if err != nil {
			return nil, err
		}
		if !ok || value == "" {
			return nil, action.Invalid("缺少 %s", key)
		}
		path = path[:start] + url.PathEscape(value) + path[start+end+1:]
	}
	query := url.Values{}
	for _, key := range route.Query {
		value, ok, err := text(key)
		if err != nil {
			return nil, err
		}
		if ok {
			query.Set(key, value)
		}
	}
	var body []byte
	switch {
	case route.Method == http.MethodGet || route.Method == http.MethodDelete || route.Body == "-":
		if len(fields) > 0 {
			for key := range fields {
				return nil, action.Invalid("不认识的参数 %s", key)
			}
		}
	case route.Body != "":
		body = fields[route.Body]
		delete(fields, route.Body)
		for key := range fields {
			return nil, action.Invalid("不认识的参数 %s", key)
		}
	default:
		body, _ = json.Marshal(fields)
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, internalKey{}, actor), route.Method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if source, ok := ctx.Value(sourceKey{}).(string); ok {
		req.RemoteAddr = source
	}
	res := &bufferedResponse{header: http.Header{}}
	a.root.ServeHTTP(res, req)
	if res.status == 0 {
		res.status = 200
	}
	var out any
	if res.body.Len() > 0 {
		if strings.HasPrefix(res.header.Get("Content-Type"), "application/json") {
			if err := json.Unmarshal(res.body.Bytes(), &out); err != nil {
				return nil, err
			}
		} else {
			out = res.body.String()
		}
	}
	if res.status >= 400 {
		message := http.StatusText(res.status)
		if m, ok := out.(map[string]any); ok {
			if e, ok := m["error"].(string); ok && e != "" {
				message = e
			}
		} else if s, ok := out.(string); ok && s != "" {
			message = strings.TrimSpace(s)
		}
		return nil, &action.Error{Status: res.status, Message: message}
	}
	if out == nil {
		out = map[string]any{"ok": true}
	}
	return out, nil
}

// actionError writes err the way the action API answers.
func actionError(w http.ResponseWriter, err error) {
	if e, ok := action.AsError(err); ok {
		write(w, e.Status, e)
		return
	}
	if errors.Is(err, model.ErrNotFound) {
		write(w, 404, map[string]string{"error": err.Error()})
		return
	}
	fail(w, err)
}

type actionView struct {
	Name        string         `json:"name"`
	Module      string         `json:"module"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Risk        action.Risk    `json:"risk"`
	Input       map[string]any `json:"input"`
	Tool        string         `json:"mcp_tool"`
}

func viewActions(list []action.Action) []actionView {
	out := make([]actionView, 0, len(list))
	for _, a := range list {
		out = append(out, actionView{Name: a.Name, Module: a.Module(), Title: a.Title, Description: a.Description, Risk: a.Risk, Input: a.Input, Tool: a.ToolName()})
	}
	return out
}

func (a *API) actionRoutes(mux *http.ServeMux, probe *mcp.Server) {
	probe.Actions = func(ctx context.Context) []action.Action {
		actor, _ := action.ActorFrom(ctx)
		return a.actions.For(actor)
	}
	probe.Run = a.runAction
	mux.HandleFunc("POST /mcp", a.keyAuth(probe.ServeHTTP, false))
	mux.HandleFunc("GET /api/v1/actions", a.actionAuth(func(w http.ResponseWriter, r *http.Request) {
		actor, _ := action.ActorFrom(r.Context())
		modules := actor.Modules
		if modules == nil {
			modules = []string{}
		}
		write(w, 200, map[string]any{
			"actor":   map[string]any{"name": actor.Name, "max_risk": actor.MaxRisk, "modules": modules},
			"actions": viewActions(a.actions.For(actor)),
		})
	}))
	mux.HandleFunc("POST /api/v1/actions/{name}", a.actionAuth(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(1 << 20)
		switch r.PathValue("name") {
		case "finance.csv_columns", "finance.csv_preview", "finance.csv_import":
			limit = 8 << 20
		}
		in, err := readBody(r, limit)
		if err != nil {
			actionError(w, action.Invalid("%v", err))
			return
		}
		confirm := r.URL.Query().Get("confirm") == "true" || r.URL.Query().Get("confirm") == "1"
		out, err := a.runAction(r.Context(), r.PathValue("name"), in, confirm)
		if err != nil {
			actionError(w, err)
			return
		}
		write(w, 200, map[string]any{"result": out})
	}))
	mux.HandleFunc("GET /api/v1/openapi.json", a.actionAuth(func(w http.ResponseWriter, r *http.Request) {
		actor, _ := action.ActorFrom(r.Context())
		write(w, 200, openAPI(mcp.BaseURL(r), a.actions.For(actor)))
	}))
}

func readBody(r *http.Request, limit int64) (json.RawMessage, error) {
	var buf bytes.Buffer
	n, err := buf.ReadFrom(http.MaxBytesReader(nil, r.Body, limit))
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(buf.Bytes()) {
		return nil, errors.New("请求体不是合法的 JSON")
	}
	return buf.Bytes(), nil
}

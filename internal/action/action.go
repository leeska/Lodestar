// Package action is the capability registry: every thing Lodestar can do for
// an outside agent is declared here once, with a name, a description, a JSON
// Schema for its input and a risk level. The HTTP API (/api/v1/actions), the
// MCP endpoint, the OpenAPI document, the downloadable Skill and later the
// panel's own assistant are all generated from the same list, so they cannot
// drift apart.
//
// An action either has a Go Handler or a Route: a route-backed action is run by
// sending an in-process request to the panel's own admin endpoint, so it gets
// exactly the validation and audit logging the panel has.
package action

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Risk says what an action can break. A key carries the highest risk it may
// use; Danger actions also need an explicit confirmation on every call.
type Risk int

const (
	// Read only reads.
	Read Risk = iota
	// Write changes something that can be changed back.
	Write
	// Danger deletes, rotates credentials, runs commands or does anything
	// else that cannot simply be undone.
	Danger
)

var riskNames = []string{"read", "write", "danger"}

func (r Risk) String() string {
	if r < Read || r > Danger {
		return "unknown"
	}
	return riskNames[r]
}

// ParseRisk reads "read", "write" or "danger".
func ParseRisk(s string) (Risk, bool) {
	i := slices.Index(riskNames, s)
	return Risk(i), i >= 0
}

func (r Risk) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

func (r *Risk) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, ok := ParseRisk(s)
	if !ok {
		return fmt.Errorf("unknown risk %q", s)
	}
	*r = v
	return nil
}

// Route sends the action to an admin endpoint of the panel. Path may hold
// {name} segments filled from the input of the same name; Query lists input
// fields sent as query parameters. For a method with a body, Body names the
// input field sent as the body ("" sends every other field as the body
// object, "-" sends none).
type Route struct {
	Method string
	Path   string
	Query  []string
	Body   string
}

// Action is one capability.
type Action struct {
	// Name is "<module>.<verb>", lower case: nodes.list, proxy.create_inbound.
	Name        string
	Title       string
	Description string
	// Input is a JSON Schema object for the arguments.
	Input map[string]any
	Risk  Risk
	// Confirm makes the panel's own agents (the assistant and automation
	// rules) pause for the owner's confirmation even below Danger: changes to
	// infrastructure, exposure or where data is sent should not follow from
	// text a model read in a feed or tool result.
	Confirm bool
	// Alias is the tool name older MCP clients used; it still resolves.
	Alias   string
	Handler func(ctx context.Context, in json.RawMessage) (any, error)
	Route   *Route
}

// NeedsConfirm reports whether an agent-initiated call must wait for the
// owner: every Danger action, and the Write actions marked Confirm.
func (a Action) NeedsConfirm() bool { return a.Risk == Danger || a.Confirm }

// Module is the part of the name before the dot.
func (a Action) Module() string { m, _, _ := strings.Cut(a.Name, "."); return m }

// ToolName is the MCP tool name: the dot becomes an underscore, which every
// client accepts.
func (a Action) ToolName() string { return strings.ReplaceAll(a.Name, ".", "_") }

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// Registry is the list of actions, in a stable order.
type Registry struct {
	list   []Action
	byName map[string]int
}

// NewRegistry checks the actions and indexes them by name, MCP tool name and
// alias. It panics on a malformed or duplicate name: that is a programming
// error caught by the tests.
func NewRegistry(actions ...Action) *Registry {
	r := &Registry{byName: map[string]int{}}
	sorted := slices.Clone(actions)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, a := range sorted {
		if !namePattern.MatchString(a.Name) {
			panic("action: bad name " + a.Name)
		}
		if (a.Handler == nil) == (a.Route == nil) {
			panic("action: " + a.Name + " needs exactly one of Handler and Route")
		}
		if a.Input == nil {
			a.Input = Schema(nil)
		}
		for _, key := range []string{a.Name, a.ToolName(), a.Alias} {
			if key == "" {
				continue
			}
			if _, dup := r.byName[key]; dup {
				panic("action: duplicate name " + key)
			}
			r.byName[key] = len(r.list)
		}
		r.list = append(r.list, a)
	}
	return r
}

// Get finds an action by name, MCP tool name or alias.
func (r *Registry) Get(name string) (Action, bool) {
	i, ok := r.byName[name]
	if !ok {
		return Action{}, false
	}
	return r.list[i], true
}

// All is every action in name order.
func (r *Registry) All() []Action { return slices.Clone(r.list) }

// For is the actions actor may call.
func (r *Registry) For(actor Actor) []Action {
	out := []Action{}
	for _, a := range r.list {
		if actor.Allows(a) {
			out = append(out, a)
		}
	}
	return out
}

// Modules is every module name, sorted.
func (r *Registry) Modules() []string {
	seen := []string{}
	for _, a := range r.list {
		if !slices.Contains(seen, a.Module()) {
			seen = append(seen, a.Module())
		}
	}
	return seen
}

// Actor is who calls: the owner signed in to the panel, or an access key with
// its limits.
type Actor struct {
	// Name goes into the audit log: the username, or "key:<name>".
	Name string
	// KeyID is the access key's id; empty for the owner.
	KeyID   string
	MaxRisk Risk
	// Modules limits the key to these modules; empty means all.
	Modules []string
}

// Owner is the signed-in administrator: everything is allowed.
func Owner(name string) Actor { return Actor{Name: name, MaxRisk: Danger} }

// Allows reports whether the actor may call a.
func (actor Actor) Allows(a Action) bool {
	return a.Risk <= actor.MaxRisk && (len(actor.Modules) == 0 || slices.Contains(actor.Modules, a.Module()))
}

type actorKey struct{}

// WithActor puts the caller into ctx.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom is the caller in ctx.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// Error is an answer the caller should read: bad input, a refused call, a
// confirmation that is still needed. Status is the HTTP status the API uses.
type Error struct {
	Status  int    `json:"-"`
	Message string `json:"error"`
	// Confirm is set when a Danger action was called without confirm: it
	// describes what would happen.
	Confirm *Confirmation `json:"confirm,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Confirmation describes a Danger call that was not run.
type Confirmation struct {
	Action      string          `json:"action"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Input       json.RawMessage `json:"input"`
}

// Invalid is a 400 with message.
func Invalid(format string, args ...any) *Error {
	return &Error{Status: http.StatusBadRequest, Message: fmt.Sprintf(format, args...)}
}

// AsError unwraps an *Error.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// Check is the gate every call passes: the actor may call the action, and a
// Danger action is confirmed. confirm is the caller's explicit yes.
func Check(actor Actor, a Action, in json.RawMessage, confirm bool) error {
	if a.Risk > actor.MaxRisk {
		return &Error{Status: http.StatusForbidden, Message: fmt.Sprintf("这把密钥最高只能执行「%s」级的能力，%s 是「%s」级", actor.MaxRisk, a.Name, a.Risk)}
	}
	if !actor.Allows(a) {
		return &Error{Status: http.StatusForbidden, Message: fmt.Sprintf("这把密钥不能使用 %s 模块", a.Module())}
	}
	if a.Risk == Danger && !confirm {
		if len(in) == 0 {
			in = json.RawMessage("{}")
		}
		return &Error{Status: http.StatusPreconditionRequired,
			Message: "这是高风险操作，没有执行。先向用户说明下面要做的事并得到同意，再带上 confirm: true 重新调用。",
			Confirm: &Confirmation{Action: a.Name, Title: a.Title, Description: a.Description, Input: in}}
	}
	return nil
}

// Decode reads the input into v, rejecting unknown fields so a misspelled
// argument fails loudly.
func Decode(in json.RawMessage, v any) error {
	if len(in) == 0 || string(in) == "null" {
		in = json.RawMessage("{}")
	}
	d := json.NewDecoder(bytes.NewReader(in))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return Invalid("参数不对：%v", err)
	}
	return nil
}

// Schema is a JSON Schema object with these properties.
func Schema(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	s := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// Open is a schema that accepts any object: for bodies whose shape the
// description explains.
func Open(description string) map[string]any {
	return map[string]any{"type": "object", "description": description, "additionalProperties": true}
}

// Str, Int and Bool are property schemas.
func Str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func Int(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}
func Bool(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

// R declares a route-backed action. Path parameters ({id}) become required
// string inputs unless props already describes them. A "body" property is
// sent as the request body; without one a POST/PUT/PATCH sends no body.
// query lists props sent as query parameters.
func R(name string, risk Risk, method, path, title, description string, props map[string]any, query ...string) Action {
	if props == nil {
		props = map[string]any{}
	}
	required := []string{}
	rest := path
	for {
		start := strings.Index(rest, "{")
		if start < 0 {
			break
		}
		end := strings.Index(rest[start:], "}")
		key := rest[start+1 : start+end]
		if _, ok := props[key]; !ok {
			props[key] = Str(key)
		}
		required = append(required, key)
		rest = rest[start+end+1:]
	}
	route := &Route{Method: method, Path: path, Query: query, Body: "-"}
	if _, ok := props["body"]; ok {
		route.Body = "body"
		required = append(required, "body")
	}
	return Action{Name: name, Risk: risk, Title: title, Description: description, Input: Schema(props, required...), Route: route}
}

// Body is the "body" property: an object described in words.
func Body(description string) map[string]any { return Open(description) }

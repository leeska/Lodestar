package assets

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/httpjson"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
)

// Module is the assets ledger.
type Module struct {
	Store *Store
	// Nodes are the servers in the panel: their billing expiry joins the
	// upcoming list (their reminders stay with the node alert rules).
	Nodes interface {
		Nodes(context.Context, time.Time) ([]model.NodeView, error)
	}
	Events interface {
		RecordEvent(context.Context, model.Event) error
	}
	// Now is the clock; nil means time.Now in local time.
	Now func() time.Time
}

func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Module) ID() string                      { return "assets" }
func (m *Module) Migrations() []storage.Migration { return migrations() }

// Upcoming is one thing that expires: an asset or a server.
type Upcoming struct {
	Source    string  `json:"source"` // "asset" or "node"
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	ExpiresOn string  `json:"expires_on"`
	DaysLeft  int     `json:"days_left"`
	Cost      float64 `json:"cost"`
	Currency  string  `json:"currency"`
	Period    string  `json:"period"`
	AutoRenew bool    `json:"auto_renew,omitempty"`
}

// Upcoming lists assets and servers that expire within days (already expired
// ones included), soonest first.
func (m *Module) Upcoming(ctx context.Context, days int) ([]Upcoming, error) {
	now := m.now()
	list, err := m.Store.List(ctx, Filter{}, now)
	if err != nil {
		return nil, err
	}
	out := []Upcoming{}
	for _, a := range list {
		if a.DaysLeft == nil || *a.DaysLeft > days {
			continue
		}
		kind, _ := KindByID(a.Kind)
		out = append(out, Upcoming{Source: "asset", ID: a.ID, Name: a.Name, Kind: kind.Name, ExpiresOn: a.ExpiresOn, DaysLeft: *a.DaysLeft,
			Cost: a.Cost, Currency: a.Currency, Period: a.Period, AutoRenew: a.AutoRenew})
	}
	if m.Nodes != nil {
		nodes, err := m.Nodes.Nodes(ctx, now)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if n.Billing.ExpiresAt == "" {
				continue
			}
			t, err := time.Parse(time.RFC3339, n.Billing.ExpiresAt)
			if err != nil {
				continue
			}
			on := t.In(now.Location()).Format(dateLayout)
			d, _ := daysLeft(on, now)
			if d > days {
				continue
			}
			out = append(out, Upcoming{Source: "node", ID: n.ID, Name: n.Name, Kind: "服务器", ExpiresOn: on, DaysLeft: d,
				Cost: n.Billing.Price, Currency: n.Billing.Currency, Period: n.Billing.PricePeriod})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DaysLeft < out[j].DaysLeft })
	return out, nil
}

// stage is how far a reminder has got for one expiry date: 1 the first
// notice (RemindDays ahead), 2 a week, 3 a day, 4 expired.
func stage(daysLeft, remind int) int {
	if remind == 0 {
		remind = 30
	}
	switch {
	case daysLeft < 0:
		return 4
	case daysLeft <= 1:
		return 3
	case daysLeft <= 7:
		return 2
	case daysLeft <= remind:
		return 1
	}
	return 0
}

// Remind sends the reminders that are due: one per stage per expiry date. An
// asset renewed to a later date starts over and gets a recovery event.
func (m *Module) Remind(ctx context.Context) error {
	now := m.now()
	rows, err := m.Store.DB.QueryContext(ctx, `SELECT id, kind, name, expires_on, remind_days, reminded_for, reminded_stage FROM assets WHERE status = 'active'`)
	if err != nil {
		return err
	}
	type row struct {
		id, kind, name, expires, remindedFor string
		remind, stage                        int
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.kind, &r.name, &r.expires, &r.remind, &r.remindedFor, &r.stage); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	for _, r := range list {
		kind, _ := KindByID(r.kind)
		event := model.Event{NodeID: "asset:" + r.id, NodeName: kind.Name + " " + r.name, Kind: model.EventAsset}
		if r.remindedFor != "" && r.remindedFor != r.expires && r.stage > 0 {
			// 到期日改了（续费了或去掉了）：发一条恢复，重新开始。
			event.Message = fmt.Sprintf("「%s」已续期", r.name)
			if r.expires != "" {
				event.Message += "，新到期日 " + r.expires
			}
			if err := m.record(ctx, event); err != nil {
				return err
			}
			if _, err := m.Store.DB.ExecContext(ctx, `UPDATE assets SET reminded_for = ?, reminded_stage = 0 WHERE id = ?`, r.expires, r.id); err != nil {
				return err
			}
			r.stage = 0
		}
		if r.expires == "" {
			continue
		}
		d, ok := daysLeft(r.expires, now)
		if !ok {
			continue
		}
		s := stage(d, r.remind)
		if s <= r.stage || s == 0 {
			continue
		}
		event.Active = true
		switch {
		case d < 0:
			event.Message = fmt.Sprintf("「%s」已于 %s 到期（过了 %d 天）", r.name, r.expires, -d)
		case d == 0:
			event.Message = fmt.Sprintf("「%s」今天到期（%s）", r.name, r.expires)
		default:
			event.Message = fmt.Sprintf("「%s」还有 %d 天到期（%s）", r.name, d, r.expires)
		}
		if err := m.record(ctx, event); err != nil {
			return err
		}
		if _, err := m.Store.DB.ExecContext(ctx, `UPDATE assets SET reminded_for = ?, reminded_stage = ? WHERE id = ?`, r.expires, s, r.id); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) record(ctx context.Context, e model.Event) error {
	if m.Events == nil {
		return nil
	}
	return m.Events.RecordEvent(ctx, e)
}

// Jobs: the daily reminder (run hourly so a changed date is noticed soon;
// each stage is sent once).
func (m *Module) Jobs() []module.Job {
	return []module.Job{{Name: "assets.remind", Title: "资产到期提醒", Every: time.Hour, Run: m.Remind}}
}

// Overview is the assets card: how many, what expires within 30 days.
func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	list, err := m.Store.List(ctx, Filter{}, m.now())
	if err != nil {
		return nil, err
	}
	card := module.Card{ID: "assets", Title: "资产", Page: "assets", Value: strconv.Itoa(len(list))}
	soon, expired := 0, 0
	for _, a := range list {
		if a.DaysLeft == nil || *a.DaysLeft > 30 {
			continue
		}
		item := module.CardItem{Label: a.Name}
		if *a.DaysLeft < 0 {
			expired++
			item.Note, item.Tone = fmt.Sprintf("已过期 %d 天", -*a.DaysLeft), "bad"
		} else {
			soon++
			item.Note, item.Tone = fmt.Sprintf("%d 天后到期", *a.DaysLeft), "warn"
		}
		if len(card.Items) < module.MaxCardItems {
			card.Items = append(card.Items, item)
		}
	}
	switch {
	case len(list) == 0:
		card.Note = "还没有登记资产"
	case expired > 0:
		card.Tone, card.Note = "bad", fmt.Sprintf("%d 项已过期，%d 项 30 天内到期", expired, soon)
	case soon > 0:
		card.Tone, card.Note = "warn", fmt.Sprintf("%d 项 30 天内到期", soon)
	default:
		card.Tone, card.Note = "good", "30 天内没有到期的"
	}
	return []module.Card{card}, nil
}

const base = "/api/v1/admin/assets"

func (m *Module) Routes(r module.Routes) {
	r.Admin("GET "+base, func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		list, err := m.Store.List(req.Context(), Filter{Kind: q.Get("kind"), Tag: q.Get("tag"), Query: q.Get("q"), Archived: q.Get("archived") == "1" || q.Get("archived") == "true"}, m.now())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"assets": list, "kinds": Kinds, "periods": Periods, "currencies": model.PriceUnits})
	})
	r.Admin("GET "+base+"/upcoming", func(w http.ResponseWriter, req *http.Request) {
		days, err := strconv.Atoi(req.URL.Query().Get("days"))
		if err != nil || days <= 0 {
			days = 60
		}
		list, err := m.Upcoming(req.Context(), min(days, 3650))
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]any{"days": days, "items": list})
	})
	r.Admin("GET "+base+"/{id}", func(w http.ResponseWriter, req *http.Request) {
		a, err := m.Store.Get(req.Context(), req.PathValue("id"), m.now())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		httpjson.Write(w, 200, a)
	})
	save := func(w http.ResponseWriter, req *http.Request, id string) {
		var a Asset
		if !httpjson.Decode(w, req, &a) {
			return
		}
		a.ID = id
		out, err := m.Store.Save(req.Context(), a, m.now())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		r.Audit(req, model.AuditEntry{Action: "assets.save", Detail: fmt.Sprintf("%s（%s）", out.Name, out.Kind), Status: 200})
		httpjson.Write(w, 200, out)
	}
	r.Admin("POST "+base, func(w http.ResponseWriter, req *http.Request) { save(w, req, "") })
	r.Admin("PUT "+base+"/{id}", func(w http.ResponseWriter, req *http.Request) { save(w, req, req.PathValue("id")) })
	r.Admin("DELETE "+base+"/{id}", func(w http.ResponseWriter, req *http.Request) {
		httpjson.Respond(w, m.Store.Delete(req.Context(), req.PathValue("id")))
	})
	// 查看敏感字段用 POST：后台的写操作都会进审计日志，谁在什么时候看过哪一项都有记录。
	r.Admin("POST "+base+"/{id}/reveal", func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			Field string `json:"field"`
		}
		if !httpjson.Decode(w, req, &in) {
			return
		}
		id := req.PathValue("id")
		value, err := m.Store.Secret(req.Context(), id, in.Field)
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		r.Audit(req, model.AuditEntry{Action: "assets.reveal", Detail: id + " · " + in.Field, Status: 200})
		w.Header().Set("Cache-Control", "no-store")
		httpjson.Write(w, 200, map[string]string{"field": in.Field, "value": value})
	})
}

func kindList() string {
	parts := []string{}
	for _, k := range Kinds {
		fields := []string{}
		for _, f := range k.Fields {
			name := f.Key
			if f.Secret {
				name += "（敏感）"
			}
			fields = append(fields, name)
		}
		parts = append(parts, fmt.Sprintf("%s=%s[%s]", k.ID, k.Name, strings.Join(fields, ", ")))
	}
	return strings.Join(parts, "；")
}

func (m *Module) Actions() []action.Action {
	id := action.Str("资产 id，来自 assets.list")
	asset := action.Body("资产：kind（类型 id）、name、tags（标签数组）、fields（该类型的普通字段，键见 assets.kinds）、set_secrets（敏感字段：值为新内容，空串表示删除，不传表示不变）、" +
		"cost、currency（" + strings.Join(model.PriceUnits, " / ") + "）、period（" + strings.Join(Periods, " / ") + "）、expires_on（YYYY-MM-DD，空串表示不过期）、auto_renew、remind_days（提前几天开始提醒，默认 30）、note、status（active / archived）。修改时传完整对象。")
	return []action.Action{
		action.R("assets.kinds", action.Read, "GET", base, "资产类型",
			"读取资产类型和每种类型的字段（同时返回资产列表）。内置类型："+kindList()+"。", nil),
		action.R("assets.list", action.Read, "GET", base, "资产列表",
			"列出登记的资产（域名、订阅服务、设备、证件、账户、保险等，不含面板节点）：费用、周期、到期日、剩余天数、标签、普通字段；敏感字段只告诉你有没有存。",
			map[string]any{"kind": action.Str("只看这种类型"), "tag": action.Str("只看带这个标签的"), "q": action.Str("按名字、备注、字段搜索"), "archived": action.Bool("包括已归档的")},
			"kind", "tag", "q", "archived"),
		action.R("assets.get", action.Read, "GET", base+"/{id}", "资产详情", "读取一项资产。", map[string]any{"id": id}),
		action.R("assets.upcoming", action.Read, "GET", base+"/upcoming", "即将到期",
			"列出若干天内到期（含已过期）的资产和服务器，按剩余天数排序。问「最近有什么要续费」用这个。",
			map[string]any{"days": action.Int("看多少天内的，默认 60")}, "days"),
		action.R("assets.create", action.Write, "POST", base, "登记资产", "登记一项资产。", map[string]any{"body": asset}),
		action.R("assets.update", action.Write, "PUT", base+"/{id}", "修改资产", "修改资产（比如续费后改到期日）。", map[string]any{"id": id, "body": asset}),
		action.R("assets.delete", action.Danger, "DELETE", base+"/{id}", "删除资产", "删除资产和它的敏感字段，不能恢复。只是不用了可以改成 archived。", map[string]any{"id": id}),
		action.R("assets.reveal_secret", action.Danger, "POST", base+"/{id}/reveal", "查看敏感字段",
			"取出资产的一项敏感字段（密码、证件号、恢复码等）。只在用户明确要这一项时用，取到的内容不要写进日志或长期记忆。",
			map[string]any{"id": id, "body": action.Body("{\"field\": 字段名}")}),
	}
}

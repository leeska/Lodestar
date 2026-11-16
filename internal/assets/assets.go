// Package assets is the ledger of what the owner has or pays for beyond the
// servers: domains, subscriptions, devices, documents, accounts, insurance.
// Every asset has the same frame (name, tags, cost and period, expiry date,
// auto-renewal, a note) plus fields of its kind; fields marked secret live in
// the vault and are only shown on an explicit, audited request.
//
// A daily job sends a reminder through the alert channels when an asset gets
// close to its expiry date, and again at 7 days, 1 day and on the day.
package assets

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/storage"
	"github.com/leeska/Lodestar/internal/vault"
)

// Field is one field of a kind.
type Field struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Type   string `json:"type,omitempty"` // "", "url", "date", "email"
	Secret bool   `json:"secret,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// Kind is a type of asset and its fields.
type Kind struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
}

// Kinds are the built-in kinds; modules may add more later.
var Kinds = []Kind{
	{ID: "domain", Name: "域名", Fields: []Field{
		{Key: "domain", Label: "域名", Hint: "可选；留空时安全检查使用资产名称"},
		{Key: "registrar", Label: "注册商"}, {Key: "dns", Label: "DNS 服务商"},
		{Key: "account", Label: "注册商账号"}, {Key: "password", Label: "注册商密码", Secret: true},
	}},
	{ID: "subscription", Name: "订阅服务", Fields: []Field{
		{Key: "service", Label: "服务"}, {Key: "plan", Label: "套餐"}, {Key: "account", Label: "账号"},
		{Key: "url", Label: "管理地址", Type: "url"}, {Key: "password", Label: "密码", Secret: true},
	}},
	{ID: "server", Name: "服务器（未接入）", Fields: []Field{
		{Key: "provider", Label: "商家"}, {Key: "ip", Label: "IP"}, {Key: "location", Label: "位置"},
		{Key: "url", Label: "控制面板", Type: "url"}, {Key: "account", Label: "账号"}, {Key: "password", Label: "密码", Secret: true},
	}},
	{ID: "device", Name: "设备", Fields: []Field{
		{Key: "model", Label: "型号"}, {Key: "serial", Label: "序列号"},
		{Key: "purchased", Label: "购买日期", Type: "date"}, {Key: "warranty", Label: "保修到期", Type: "date"},
	}},
	{ID: "document", Name: "证件", Fields: []Field{
		{Key: "holder", Label: "持有人"}, {Key: "issuer", Label: "签发机关"}, {Key: "number", Label: "证件号", Secret: true},
	}},
	{ID: "account", Name: "账户", Fields: []Field{
		{Key: "provider", Label: "平台"}, {Key: "url", Label: "地址", Type: "url"}, {Key: "username", Label: "用户名"},
		{Key: "password", Label: "密码", Secret: true}, {Key: "recovery", Label: "恢复码 / 两步验证备份", Secret: true},
	}},
	{ID: "insurance", Name: "保险", Fields: []Field{
		{Key: "insurer", Label: "保险公司"}, {Key: "insured", Label: "被保人"}, {Key: "coverage", Label: "保额"},
		{Key: "policy", Label: "保单号", Secret: true},
	}},
	{ID: "other", Name: "其他", Fields: []Field{}},
}

// Periods are the billing periods, the same labels nodes use.
var Periods = []string{"周", "月", "季", "半年", "年", "两年", "三年", "一次性"}

// KindByID finds a kind.
func KindByID(id string) (Kind, bool) {
	i := slices.IndexFunc(Kinds, func(k Kind) bool { return k.ID == id })
	if i < 0 {
		return Kind{}, false
	}
	return Kinds[i], true
}

// Asset is one entry of the ledger.
type Asset struct {
	ID     string            `json:"id"`
	Kind   string            `json:"kind"`
	Name   string            `json:"name"`
	Status string            `json:"status"` // "active" or "archived"
	Tags   []string          `json:"tags"`
	Fields map[string]string `json:"fields"`
	// Secrets says which secret fields are stored (never their values). On
	// input, SetSecrets sets ("value") or clears ("") them; absent keys stay.
	Secrets    map[string]bool   `json:"secrets"`
	SetSecrets map[string]string `json:"set_secrets,omitempty"`
	Cost       float64           `json:"cost"`
	Currency   string            `json:"currency"`
	Period     string            `json:"period"`
	// ExpiresOn is YYYY-MM-DD; empty means it does not expire.
	ExpiresOn string `json:"expires_on"`
	AutoRenew bool   `json:"auto_renew"`
	// RemindDays is how many days ahead the first reminder goes out (0 = 30).
	RemindDays int    `json:"remind_days"`
	Note       string `json:"note"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	// DaysLeft is filled on output for an asset that expires.
	DaysLeft *int `json:"days_left,omitempty"`
}

const dateLayout = "2006-01-02"

// Validate checks an asset from a client and normalizes it.
func (a *Asset) Validate() error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || utf8.RuneCountInString(a.Name) > 80 {
		return model.Invalid("名字要有，最多 80 个字")
	}
	kind, ok := KindByID(a.Kind)
	if !ok {
		return model.Invalid("没有这种资产类型：" + a.Kind)
	}
	if a.Status == "" {
		a.Status = "active"
	}
	if a.Status != "active" && a.Status != "archived" {
		return model.Invalid("状态只能是 active 或 archived")
	}
	tags := []string{}
	for _, t := range a.Tags {
		t = strings.TrimSpace(t)
		if t != "" && !slices.Contains(tags, t) && utf8.RuneCountInString(t) <= 20 {
			tags = append(tags, t)
		}
	}
	if len(tags) > 20 {
		return model.Invalid("标签最多 20 个")
	}
	a.Tags = tags
	fields := map[string]string{}
	for k, v := range a.Fields {
		i := slices.IndexFunc(kind.Fields, func(f Field) bool { return f.Key == k })
		if i < 0 {
			return model.Invalid(fmt.Sprintf("%s 没有 %s 这一项", kind.Name, k))
		}
		if kind.Fields[i].Secret {
			return model.Invalid(fmt.Sprintf("%s 是敏感字段，要放在 set_secrets 里", k))
		}
		if v = strings.TrimSpace(v); v != "" {
			if utf8.RuneCountInString(v) > 500 {
				return model.Invalid(k + " 太长")
			}
			fields[k] = v
		}
	}
	a.Fields = fields
	for k, v := range a.SetSecrets {
		i := slices.IndexFunc(kind.Fields, func(f Field) bool { return f.Key == k && f.Secret })
		if i < 0 {
			return model.Invalid(fmt.Sprintf("%s 没有 %s 这个敏感字段", kind.Name, k))
		}
		if len(v) > 4096 {
			return model.Invalid(k + " 太长")
		}
	}
	if a.Cost < 0 || a.Cost > 1e9 {
		return model.Invalid("费用不对")
	}
	a.Currency = strings.ToUpper(strings.TrimSpace(a.Currency))
	if a.Currency == "" {
		a.Currency = model.DefaultPriceUnit
	}
	if !slices.Contains(model.PriceUnits, a.Currency) {
		return model.Invalid("不认识的币种：" + a.Currency)
	}
	if a.Period != "" && !slices.Contains(Periods, a.Period) {
		return model.Invalid("周期只能是 " + strings.Join(Periods, "、"))
	}
	if a.ExpiresOn != "" {
		if _, err := time.Parse(dateLayout, a.ExpiresOn); err != nil {
			return model.Invalid("到期日要写成 YYYY-MM-DD")
		}
	}
	if a.RemindDays < 0 || a.RemindDays > 365 {
		return model.Invalid("提前提醒天数在 0–365 之间")
	}
	if utf8.RuneCountInString(a.Note) > 2000 {
		return model.Invalid("备注最多 2000 字")
	}
	return nil
}

// daysLeft counts whole days from today (in loc) to the expiry date.
func daysLeft(expiresOn string, now time.Time) (int, bool) {
	d, err := time.ParseInLocation(dateLayout, expiresOn, now.Location())
	if err != nil {
		return 0, false
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return int(d.Sub(today).Hours() / 24), true
}

// Store keeps assets in the main database and their secrets in the vault.
type Store struct {
	DB    *sql.DB
	Vault *vault.Vault
}

func migrations() []storage.Migration {
	return []storage.Migration{{Module: "assets", Version: 1, Name: "assets", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`CREATE TABLE assets(
			id TEXT PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active',
			tags TEXT NOT NULL DEFAULT '[]', fields TEXT NOT NULL DEFAULT '{}',
			cost REAL NOT NULL DEFAULT 0, currency TEXT NOT NULL DEFAULT 'CNY', period TEXT NOT NULL DEFAULT '',
			expires_on TEXT NOT NULL DEFAULT '', auto_renew INTEGER NOT NULL DEFAULT 0, remind_days INTEGER NOT NULL DEFAULT 0,
			note TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
			reminded_for TEXT NOT NULL DEFAULT '', reminded_stage INTEGER NOT NULL DEFAULT 0);
			CREATE INDEX assets_expires ON assets(status, expires_on);`)
		return err
	}}}
}

const columns = `id, kind, name, status, tags, fields, cost, currency, period, expires_on, auto_renew, remind_days, note, created_at, updated_at`

func scan(row interface{ Scan(...any) error }) (Asset, error) {
	var a Asset
	var tags, fields string
	err := row.Scan(&a.ID, &a.Kind, &a.Name, &a.Status, &tags, &fields, &a.Cost, &a.Currency, &a.Period, &a.ExpiresOn, &a.AutoRenew, &a.RemindDays, &a.Note, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return a, err
	}
	json.Unmarshal([]byte(tags), &a.Tags)
	json.Unmarshal([]byte(fields), &a.Fields)
	if a.Tags == nil {
		a.Tags = []string{}
	}
	if a.Fields == nil {
		a.Fields = map[string]string{}
	}
	return a, nil
}

func secretID(asset, field string) string { return "asset:" + asset + ":" + field }

// fill adds the secret flags and days left.
func (s *Store) fill(ctx context.Context, a *Asset, now time.Time) error {
	has, err := s.Vault.Has(ctx, "asset:"+a.ID+":")
	if err != nil {
		return err
	}
	a.Secrets = has
	if a.ExpiresOn != "" {
		if d, ok := daysLeft(a.ExpiresOn, now); ok {
			a.DaysLeft = &d
		}
	}
	return nil
}

// Filter narrows List.
type Filter struct {
	Kind     string
	Tag      string
	Query    string
	Archived bool // include archived ones
}

// List is the assets, soonest expiry first, then by name.
func (s *Store) List(ctx context.Context, f Filter, now time.Time) ([]Asset, error) {
	where, args := []string{"1=1"}, []any{}
	if !f.Archived {
		where = append(where, "status = 'active'")
	}
	if f.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, f.Kind)
	}
	if f.Query != "" {
		where, args = append(where, "(name LIKE ? OR note LIKE ? OR fields LIKE ?)"), append(args, "%"+f.Query+"%", "%"+f.Query+"%", "%"+f.Query+"%")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+columns+` FROM assets WHERE `+strings.Join(where, " AND ")+
		` ORDER BY expires_on = '', expires_on, name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if f.Tag != "" && !slices.Contains(a.Tags, f.Tag) {
			continue
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.fill(ctx, &out[i], now); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Get is one asset.
func (s *Store) Get(ctx context.Context, id string, now time.Time) (Asset, error) {
	a, err := scan(s.DB.QueryRowContext(ctx, `SELECT `+columns+` FROM assets WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, model.ErrNotFound
	}
	if err != nil {
		return a, err
	}
	return a, s.fill(ctx, &a, now)
}

// Save creates (empty ID) or replaces an asset and applies SetSecrets.
func (s *Store) Save(ctx context.Context, a Asset, now time.Time) (Asset, error) {
	if err := a.Validate(); err != nil {
		return a, err
	}
	tags, _ := json.Marshal(a.Tags)
	fields, _ := json.Marshal(a.Fields)
	if a.ID == "" {
		a.ID = model.Secret()[:12]
		a.CreatedAt = now.Unix()
		a.UpdatedAt = a.CreatedAt
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO assets(`+columns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.ID, a.Kind, a.Name, a.Status, string(tags), string(fields), a.Cost, a.Currency, a.Period, a.ExpiresOn, a.AutoRenew, a.RemindDays, a.Note, a.CreatedAt, a.UpdatedAt); err != nil {
			return a, err
		}
	} else {
		old, err := s.Get(ctx, a.ID, now)
		if err != nil {
			return a, err
		}
		if old.Kind != a.Kind {
			// 换类型时旧类型的敏感字段不再有意义，一并删掉。
			if err := s.Vault.DeletePrefix(ctx, "asset:"+a.ID+":"); err != nil {
				return a, err
			}
		}
		a.CreatedAt, a.UpdatedAt = old.CreatedAt, now.Unix()
		if _, err := s.DB.ExecContext(ctx, `UPDATE assets SET kind=?, name=?, status=?, tags=?, fields=?, cost=?, currency=?, period=?, expires_on=?,
			auto_renew=?, remind_days=?, note=?, updated_at=? WHERE id=?`,
			a.Kind, a.Name, a.Status, string(tags), string(fields), a.Cost, a.Currency, a.Period, a.ExpiresOn, a.AutoRenew, a.RemindDays, a.Note, a.UpdatedAt, a.ID); err != nil {
			return a, err
		}
	}
	for k, v := range a.SetSecrets {
		if err := s.Vault.Put(ctx, secretID(a.ID, k), v); err != nil {
			return a, err
		}
	}
	return s.Get(ctx, a.ID, now)
}

// Delete removes an asset and its secrets.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return s.Vault.DeletePrefix(ctx, "asset:"+id+":")
}

// Secret opens one secret field.
func (s *Store) Secret(ctx context.Context, id, field string) (string, error) {
	v, err := s.Vault.Get(ctx, secretID(id, field))
	if errors.Is(err, vault.ErrNotFound) {
		return "", model.ErrNotFound
	}
	return v, err
}

package security

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/assets"
	"github.com/leeska/Lodestar/internal/certs"
	"github.com/leeska/Lodestar/internal/model"
	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
)

type Settings struct {
	PasswordChecks bool `json:"password_checks"`
	DNSChecks      bool `json:"dns_checks"`
	LoginAlerts    bool `json:"login_alerts"`
}
type Profile struct {
	AssetID          string `json:"asset_id"`
	TwoFactor        string `json:"two_factor"`
	RecoveryLocation string `json:"recovery_location"`
	LastReview       int64  `json:"last_review"`
	MonitorPassword  bool   `json:"monitor_password"`
	CheckedAt        int64  `json:"checked_at"`
	LastAttempt      int64  `json:"last_attempt"`
	BreachCount      int64  `json:"breach_count"`
	CheckError       bool   `json:"check_error"`
	ReviewNow        bool   `json:"review_now,omitempty"`
	passwordVersion  []byte
}
type Account struct {
	Profile
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	HasPassword bool   `json:"has_password"`
	HasRecovery bool   `json:"has_recovery"`
	Outdated    bool   `json:"outdated"`
}
type Module struct {
	DB     *sql.DB
	Assets *assets.Store
	Certs  *certs.Store
	Events interface {
		RecordEvent(context.Context, model.Event) error
		Audit(context.Context, model.AuditEntry) error
	}
	Now            func() time.Time
	PasswordClient *http.Client
	LookupIP       func(context.Context, string) ([]net.IPAddr, error)
	check          sync.Mutex
	dns            sync.Mutex
	login          sync.Mutex
}

func (*Module) ID() string { return "security" }
func (*Module) Migrations() []storage.Migration {
	return []storage.Migration{{Module: "security", Version: 1, Name: "account security and observations", Up: func(ctx *storage.MigrationContext) error {
		_, err := ctx.Tx.Exec(`CREATE TABLE security_settings(id INTEGER PRIMARY KEY CHECK(id=1),config TEXT NOT NULL);
		CREATE TABLE security_profiles(asset_id TEXT PRIMARY KEY,two_factor TEXT NOT NULL DEFAULT 'unknown',recovery_location TEXT NOT NULL DEFAULT '',last_review INTEGER NOT NULL DEFAULT 0,monitor_password INTEGER NOT NULL DEFAULT 0,checked_at INTEGER NOT NULL DEFAULT 0,last_attempt INTEGER NOT NULL DEFAULT 0,breach_count INTEGER NOT NULL DEFAULT -1,check_error INTEGER NOT NULL DEFAULT 0,password_version BLOB NOT NULL DEFAULT X'');
		CREATE TABLE security_dns(asset_id TEXT PRIMARY KEY,domain TEXT NOT NULL,checked_at INTEGER NOT NULL,status TEXT NOT NULL);
		CREATE TABLE security_login_notices(bucket TEXT PRIMARY KEY,at INTEGER NOT NULL);`)
		return err
	}}}
}
func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
func (m *Module) Settings(ctx context.Context) (Settings, error) {
	config := Settings{LoginAlerts: true}
	var raw string
	err := m.DB.QueryRowContext(ctx, `SELECT config FROM security_settings WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	err = json.Unmarshal([]byte(raw), &config)
	return config, err
}
func (m *Module) SaveSettings(ctx context.Context, config Settings) error {
	if config.PasswordChecks && !allows(ctx, "assets", action.Danger) {
		return &action.Error{Status: 403, Message: "启用 HIBP 检查需要资产敏感字段权限"}
	}
	if config.DNSChecks && !allows(ctx, "assets", action.Read) {
		return &action.Error{Status: 403, Message: "DNS 检查需要资产读取权限"}
	}
	raw, _ := json.Marshal(config)
	_, err := m.DB.ExecContext(ctx, `INSERT INTO security_settings(id,config) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config`, string(raw))
	return err
}
func allows(ctx context.Context, name string, risk action.Risk) bool {
	actor, ok := action.ActorFrom(ctx)
	return ok && actor.Allows(action.Action{Name: name + ".read", Risk: risk})
}
func (m *Module) profile(ctx context.Context, id string) (Profile, error) {
	profile := Profile{AssetID: id, TwoFactor: "unknown", BreachCount: -1}
	err := m.DB.QueryRowContext(ctx, `SELECT two_factor,recovery_location,last_review,monitor_password,checked_at,last_attempt,breach_count,check_error,password_version FROM security_profiles WHERE asset_id=?`, id).Scan(&profile.TwoFactor, &profile.RecoveryLocation, &profile.LastReview, &profile.MonitorPassword, &profile.CheckedAt, &profile.LastAttempt, &profile.BreachCount, &profile.CheckError, &profile.passwordVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return profile, nil
	}
	return profile, err
}
func (m *Module) Accounts(ctx context.Context) ([]Account, error) {
	if !allows(ctx, "assets", action.Read) {
		return []Account{}, nil
	}
	items, err := m.Assets.List(ctx, assets.Filter{Kind: "account"}, m.now())
	if err != nil {
		return nil, err
	}
	out := []Account{}
	for _, asset := range items {
		profile, err := m.profile(ctx, asset.ID)
		if err != nil {
			return nil, err
		}
		version, err := m.passwordVersion(ctx, asset.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, Account{Profile: profile, Name: asset.Name, Provider: asset.Fields["provider"], HasPassword: asset.Secrets["password"], HasRecovery: asset.Secrets["recovery"], Outdated: profile.CheckedAt > 0 && !bytes.Equal(version, profile.passwordVersion)})
	}
	return out, nil
}
func (m *Module) SaveProfile(ctx context.Context, input Profile) (Profile, error) {
	if !allows(ctx, "assets", action.Read) {
		return Profile{}, &action.Error{Status: 403, Message: "需要资产读取权限"}
	}
	asset, err := m.Assets.Get(ctx, input.AssetID, m.now())
	if err != nil {
		return Profile{}, err
	}
	if asset.Kind != "account" || asset.Status == "archived" {
		return Profile{}, action.Invalid("只支持未归档的账户资产")
	}
	if input.TwoFactor != "enabled" && input.TwoFactor != "disabled" && input.TwoFactor != "unknown" {
		return Profile{}, action.Invalid("two_factor 为 enabled / disabled / unknown")
	}
	input.RecoveryLocation = strings.TrimSpace(input.RecoveryLocation)
	if len(input.RecoveryLocation) > 500 {
		return Profile{}, action.Invalid("恢复方式位置最多 500 字节")
	}
	old, err := m.profile(ctx, input.AssetID)
	if err != nil {
		return Profile{}, err
	}
	if input.MonitorPassword && !old.MonitorPassword && !allows(ctx, "assets", action.Danger) {
		return Profile{}, &action.Error{Status: 403, Message: "启用密码检查还需要资产敏感字段权限"}
	}
	lastReview := old.LastReview
	if input.ReviewNow {
		lastReview = m.now().Unix()
	}
	_, err = m.DB.ExecContext(ctx, `INSERT INTO security_profiles(asset_id,two_factor,recovery_location,last_review,monitor_password) VALUES(?,?,?,?,?) ON CONFLICT(asset_id) DO UPDATE SET two_factor=excluded.two_factor,recovery_location=excluded.recovery_location,last_review=excluded.last_review,monitor_password=excluded.monitor_password`, input.AssetID, input.TwoFactor, input.RecoveryLocation, lastReview, input.MonitorPassword)
	if err != nil {
		return Profile{}, err
	}
	return m.profile(ctx, input.AssetID)
}
func (m *Module) Jobs() []module.Job {
	return []module.Job{
		{Name: "security.passwords", Title: "每周密码泄露检查", Every: 24 * time.Hour, Run: func(ctx context.Context) error { _, err := m.CheckPasswords(ctx, false); return err }},
		{Name: "security.dns", Title: "域名 DNS 风险检查", Every: 24 * time.Hour, Run: m.CheckDNS},
	}
}
func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	if !allows(ctx, "assets", action.Read) {
		return []module.Card{{ID: "security", Title: "数字身份与安全", Page: "security", Value: "未授权", Note: "需要 assets 读取权限"}}, nil
	}
	accounts, err := m.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	attention := 0
	for _, account := range accounts {
		if account.TwoFactor != "enabled" || account.BreachCount > 0 && !account.Outdated && account.HasPassword {
			attention++
		}
	}
	tone := ""
	if attention > 0 {
		tone = "warn"
	}
	return []module.Card{{ID: "security", Title: "数字身份与安全", Page: "security", Value: formatCount(attention) + " 项需检查", Note: formatCount(len(accounts)) + " 个账户；未检查不代表安全", Tone: tone}}, nil
}

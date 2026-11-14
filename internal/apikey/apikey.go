// Package apikey keeps the access keys outside agents use for the action API,
// MCP and OpenAPI. A key is "ls_<id>_<secret>": the id finds the row, only a
// SHA-256 of the whole key is stored, and the key itself is shown once.
package apikey

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/leeska/Lodestar/internal/action"
	"github.com/leeska/Lodestar/internal/model"
)

// Key is one access key as the panel lists it; the secret is never kept.
type Key struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	MaxRisk    action.Risk `json:"max_risk"`
	Modules    []string    `json:"modules"`
	CreatedAt  int64       `json:"created_at"`
	ExpiresAt  int64       `json:"expires_at,omitempty"`
	LastUsedAt int64       `json:"last_used_at,omitempty"`
	LastIP     string      `json:"last_ip,omitempty"`
	RevokedAt  int64       `json:"revoked_at,omitempty"`
	hash       string
}

// Active reports whether the key may be used at now.
func (k Key) Active(now time.Time) bool {
	return k.RevokedAt == 0 && (k.ExpiresAt == 0 || now.Unix() < k.ExpiresAt)
}

// Actor is the key as a caller of actions.
func (k Key) Actor() action.Actor {
	return action.Actor{Name: "key:" + k.Name, KeyID: k.ID, MaxRisk: k.MaxRisk, Modules: k.Modules}
}

// Spec is what a new key is made from.
type Spec struct {
	Name    string      `json:"name"`
	MaxRisk action.Risk `json:"max_risk"`
	Modules []string    `json:"modules"`
	// ExpiresAt is a Unix time; 0 never expires.
	ExpiresAt int64 `json:"expires_at"`
}

// ErrUnknown is a key that does not exist, is revoked or expired.
var ErrUnknown = errors.New("访问密钥无效、已吊销或已过期")

// Store keeps the keys in the main database.
type Store struct {
	DB *sql.DB
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	touched map[string]time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Validate checks a spec against the modules that exist.
func Validate(spec Spec, modules []string) error {
	name := strings.TrimSpace(spec.Name)
	if name == "" || utf8.RuneCountInString(name) > 40 {
		return action.Invalid("名字要有，最多 40 个字")
	}
	if spec.MaxRisk < action.Read || spec.MaxRisk > action.Danger {
		return action.Invalid("权限档只能是 read、write 或 danger")
	}
	for _, m := range spec.Modules {
		if !slices.Contains(modules, m) {
			return action.Invalid("没有 %s 这个模块", m)
		}
	}
	return nil
}

// Create makes a key and returns it in full: the only time it is visible.
func (s *Store) Create(ctx context.Context, spec Spec) (Key, string, error) {
	id := model.Secret()[:12]
	secret := model.Secret()
	full := "ls_" + id + "_" + secret
	modules := spec.Modules
	if modules == nil {
		modules = []string{}
	}
	raw, _ := json.Marshal(modules)
	k := Key{ID: id, Name: strings.TrimSpace(spec.Name), MaxRisk: spec.MaxRisk, Modules: modules, CreatedAt: s.now().Unix(), ExpiresAt: spec.ExpiresAt}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO api_keys(id, name, hash, max_risk, modules, created_at, expires_at) VALUES(?,?,?,?,?,?,?)`,
		k.ID, k.Name, model.Hash(full), int(k.MaxRisk), string(raw), k.CreatedAt, k.ExpiresAt)
	return k, full, err
}

const columns = `id, name, hash, max_risk, modules, created_at, expires_at, last_used_at, last_ip, revoked_at`

func scan(row interface{ Scan(...any) error }) (Key, error) {
	var k Key
	var risk int
	var modules string
	if err := row.Scan(&k.ID, &k.Name, &k.hash, &risk, &modules, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.LastIP, &k.RevokedAt); err != nil {
		return k, err
	}
	k.MaxRisk = action.Risk(risk)
	if json.Unmarshal([]byte(modules), &k.Modules) != nil || k.Modules == nil {
		k.Modules = []string{}
	}
	return k, nil
}

// List is every key, newest first, revoked ones included.
func (s *Store) List(ctx context.Context) ([]Key, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+columns+` FROM api_keys ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		k, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Get is one key.
func (s *Store) Get(ctx context.Context, id string) (Key, error) {
	k, err := scan(s.DB.QueryRowContext(ctx, `SELECT `+columns+` FROM api_keys WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return k, model.ErrNotFound
	}
	return k, err
}

// Revoke ends a key now; revoking twice is not an error.
func (s *Store) Revoke(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE api_keys SET revoked_at = CASE WHEN revoked_at = 0 THEN ? ELSE revoked_at END WHERE id = ?`, s.now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

// Delete removes a revoked key from the list.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ? AND revoked_at <> 0`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w：只能删除已吊销的密钥", model.ErrNotFound)
	}
	return nil
}

// Authenticate finds the active key for a presented token. Keys made before
// this format (the old MCP key) have no id in them and are matched by hash.
func (s *Store) Authenticate(ctx context.Context, token string) (Key, error) {
	var k Key
	var err error
	if rest, ok := strings.CutPrefix(token, "ls_"); ok {
		id, _, found := strings.Cut(rest, "_")
		if !found || len(id) != 12 {
			return k, ErrUnknown
		}
		if _, hexErr := hex.DecodeString(id); hexErr != nil {
			return k, ErrUnknown
		}
		k, err = s.Get(ctx, id)
	} else {
		k, err = scan(s.DB.QueryRowContext(ctx, `SELECT `+columns+` FROM api_keys WHERE hash = ?`, model.Hash(token)))
	}
	if errors.Is(err, model.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		return k, ErrUnknown
	}
	if err != nil {
		return k, err
	}
	if subtle.ConstantTimeCompare([]byte(model.Hash(token)), []byte(k.hash)) != 1 || !k.Active(s.now()) {
		return Key{}, ErrUnknown
	}
	return k, nil
}

// Touch records that the key was used, at most once a minute per key so a
// busy agent does not write on every request.
func (s *Store) Touch(ctx context.Context, id, ip string) {
	now := s.now()
	s.mu.Lock()
	if s.touched == nil {
		s.touched = map[string]time.Time{}
	}
	if last, ok := s.touched[id]; ok && now.Sub(last) < time.Minute {
		s.mu.Unlock()
		return
	}
	s.touched[id] = now
	s.mu.Unlock()
	s.DB.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ?, last_ip = ? WHERE id = ?`, now.Unix(), ip, id)
}

// SetLegacyMCP replaces the key named "MCP" (id "mcp") with a new full-access
// one: what the settings page's "generate MCP key" button does.
func (s *Store) SetLegacyMCP(ctx context.Context, full string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO api_keys(id, name, hash, max_risk, created_at) VALUES('mcp','MCP',?,?,?)
		ON CONFLICT(id) DO UPDATE SET hash = excluded.hash, created_at = excluded.created_at, revoked_at = 0, last_used_at = 0, last_ip = ''`,
		model.Hash(full), int(action.Danger), s.now().Unix())
	return err
}

// RevokeLegacyMCP revokes the "MCP" key if there is one.
func (s *Store) RevokeLegacyMCP(ctx context.Context) error {
	err := s.Revoke(ctx, "mcp")
	if errors.Is(err, model.ErrNotFound) {
		return nil
	}
	return err
}

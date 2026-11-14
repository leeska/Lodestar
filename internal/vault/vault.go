// Package vault keeps secrets (passwords, card numbers, recovery codes, API
// tokens of connectors) encrypted in the main database. The key is a random
// 32-byte file in the data directory (vault.key, 0600): a copy of the
// database alone reveals nothing, and the encrypted backup carries both.
//
// Values are AES-256-GCM sealed with the secret's id as additional data, so a
// ciphertext moved to another id does not open. Callers address secrets by
// id ("asset:<id>:<field>"); listing or reading them through the API is a
// separate, audited decision of the module that owns them.
package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leeska/Lodestar/internal/storage"
)

// KeyFile is the key's name in the data directory.
const KeyFile = "vault.key"

// ErrNotFound is a secret that is not stored.
var ErrNotFound = errors.New("没有这条密文")

// Vault seals and opens secrets.
type Vault struct {
	DB   *sql.DB
	aead cipher.AEAD
}

// Module is the vault's place in the module list: it owns the table.
type Module struct{ *Vault }

func (Module) ID() string { return "vault" }

func (Module) Migrations() []storage.Migration {
	return []storage.Migration{{Module: "vault", Version: 1, Name: "secrets", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`CREATE TABLE vault(id TEXT PRIMARY KEY, nonce BLOB NOT NULL, data BLOB NOT NULL, updated_at INTEGER NOT NULL)`)
		return err
	}}}
}

// LoadKey reads the key from dir, creating it the first time.
func LoadKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, KeyFile)
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(key); err != nil {
			f.Close()
			return nil, err
		}
		return key, f.Close()
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s 不是 32 字节的密钥", path)
	}
	return key, nil
}

// New is a vault over db with key.
func New(db *sql.DB, key []byte) (*Vault, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{DB: db, aead: aead}, nil
}

// Put stores value under id; an empty value deletes it.
func (v *Vault) Put(ctx context.Context, id, value string) error {
	if value == "" {
		return v.Delete(ctx, id)
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	data := v.aead.Seal(nil, nonce, []byte(value), []byte(id))
	_, err := v.DB.ExecContext(ctx, `INSERT INTO vault(id, nonce, data, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET nonce = excluded.nonce, data = excluded.data, updated_at = excluded.updated_at`,
		id, nonce, data, time.Now().Unix())
	return err
}

// Get opens the secret under id.
func (v *Vault) Get(ctx context.Context, id string) (string, error) {
	var nonce, data []byte
	err := v.DB.QueryRowContext(ctx, `SELECT nonce, data FROM vault WHERE id = ?`, id).Scan(&nonce, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	plain, err := v.aead.Open(nil, nonce, data, []byte(id))
	if err != nil {
		return "", fmt.Errorf("密文 %s 打不开（数据目录里的 %s 换过？）", id, KeyFile)
	}
	return string(plain), nil
}

// Has lists which of ids are stored.
func (v *Vault) Has(ctx context.Context, prefix string) (map[string]bool, error) {
	rows, err := v.DB.QueryContext(ctx, `SELECT id FROM vault WHERE id >= ? AND id < ?`, prefix, prefix+"\xff")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[strings.TrimPrefix(id, prefix)] = true
	}
	return out, rows.Err()
}

// Delete removes the secret under id.
func (v *Vault) Delete(ctx context.Context, id string) error {
	_, err := v.DB.ExecContext(ctx, `DELETE FROM vault WHERE id = ?`, id)
	return err
}

// DeletePrefix removes every secret whose id starts with prefix.
func (v *Vault) DeletePrefix(ctx context.Context, prefix string) error {
	_, err := v.DB.ExecContext(ctx, `DELETE FROM vault WHERE id >= ? AND id < ?`, prefix, prefix+"\xff")
	return err
}

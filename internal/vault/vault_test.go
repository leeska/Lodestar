package vault

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/leeska/Lodestar/internal/module"
	"github.com/leeska/Lodestar/internal/storage"
)

func TestSealOpenAndKeyFile(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.OpenWith(filepath.Join(dir, "v.db"), module.Migrations([]module.Module{Module{}}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, KeyFile)); info.Mode().Perm() != 0600 {
		t.Fatalf("key mode %v", info.Mode())
	}
	again, _ := LoadKey(dir)
	if string(again) != string(key) {
		t.Fatal("key changed on reload")
	}
	v, _ := New(s.DB(), key)
	ctx := context.Background()
	v.Put(ctx, "asset:a:password", "secret")
	if got, err := v.Get(ctx, "asset:a:password"); err != nil || got != "secret" {
		t.Fatal(got, err)
	}
	// A ciphertext copied under another id does not open.
	s.DB().Exec(`INSERT INTO vault SELECT 'asset:b:password', nonce, data, updated_at FROM vault WHERE id = 'asset:a:password'`)
	if _, err := v.Get(ctx, "asset:b:password"); err == nil {
		t.Fatal("moved ciphertext opened")
	}
	other := make([]byte, 32)
	w, _ := New(s.DB(), other)
	if _, err := w.Get(ctx, "asset:a:password"); err == nil {
		t.Fatal("opened with another key")
	}
	if has, _ := v.Has(ctx, "asset:a:"); !has["password"] {
		t.Fatal(has)
	}
}

package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func legacyFixture(t *testing.T) (root string, files map[string]string) {
	t.Helper()
	root = t.TempDir()
	files = map[string]string{
		"etc/tza-probe/agent.json":                               `{"endpoint":"https://x"}`,
		"etc/tza-probe/manage.json":                              `{"enabled":true}`,
		"var/lib/tza-agent/migrate-requested":                    "",
		"etc/systemd/system/tza-agent.service":                   "[Unit]\nDescription=TZA ControlCenter Agent\n[Service]\nExecStartPre=-+/usr/local/bin/tza-agent --config /etc/tza-probe/agent.json --apply-update\nExecStart=/usr/local/bin/tza-agent --config /etc/tza-probe/agent.json --self-update\nStateDirectory=tza-agent\n",
		"etc/systemd/system/tza-agent.service.d/50-plugins.conf": "[Service]\nReadWritePaths=/etc/tza-probe\n",
		"etc/systemd/system/tza-proxy-xray.service":              "[Service]\nExecStart=/usr/local/lib/tza-probe/kernels/xray/1.0/xray run -c /etc/tza-proxy/xray.json\nUser=tza-proxy\n",
		"etc/tza-proxy/xray.json":                                "{}",
		"usr/local/lib/tza-probe/kernels/xray/1.0/xray":          "bin",
		"etc/nginx/conf.d/tza-site-ab12.conf":                    "ssl_certificate /etc/tza-certs/a/fullchain.pem;",
		"etc/tza-certs/a/fullchain.pem":                          "pem",
		"opt/tza-compose/web/compose.yaml":                       "services: {}",
		"usr/local/bin/tza-agent":                                "old",
	}
	for p, body := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root, files
}

func TestLegacyMigrateMovesEverything(t *testing.T) {
	root, _ := legacyFixture(t)
	exe := filepath.Join(t.TempDir(), "new-agent")
	os.WriteFile(exe, []byte("new"), 0755)
	var calls []string
	l := &Legacy{Root: root, Exe: exe, Systemd: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil, nil
		}}
	if !l.Needed() {
		t.Fatal("old layout not detected")
	}
	if err := l.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		raw, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return string(raw)
	}
	gone := func(p string) {
		if _, err := os.Lstat(filepath.Join(root, p)); err == nil {
			t.Fatalf("%s still there", p)
		}
	}
	if read("usr/local/bin/lodestar-agent") != "new" || read("etc/lodestar/agent.json") == "" || read("etc/lodestar/manage.json") == "" {
		t.Fatal("binary or configuration not moved")
	}
	unit := read("etc/systemd/system/lodestar-agent.service")
	if strings.Contains(unit, "tza") || !strings.Contains(unit, "/usr/local/bin/lodestar-agent --config /etc/lodestar/agent.json --self-update") || !strings.Contains(unit, "StateDirectory=lodestar-agent") {
		t.Fatal(unit)
	}
	if dropin := read("etc/systemd/system/lodestar-agent.service.d/50-plugins.conf"); !strings.Contains(dropin, "/etc/lodestar") {
		t.Fatal(dropin)
	}
	if proxy := read("etc/systemd/system/lodestar-proxy-xray.service"); strings.Contains(proxy, "tza") || !strings.Contains(proxy, "/usr/local/lib/lodestar/kernels/xray/1.0/xray run -c /etc/lodestar-proxy/xray.json") {
		t.Fatal(proxy)
	}
	read("usr/local/lib/lodestar/kernels/xray/1.0/xray")
	read("etc/lodestar-proxy/xray.json")
	read("etc/nginx/conf.d/lodestar-site-ab12.conf")
	read("etc/lodestar-certs/a/fullchain.pem")
	read("opt/lodestar-compose/web/compose.yaml")
	for _, p := range []string{"etc/tza-certs", "opt/tza-compose"} {
		if target, err := os.Readlink(filepath.Join(root, p)); err != nil || !strings.HasPrefix(target, "/") {
			t.Fatalf("%s should link to the new place: %q %v", p, target, err)
		}
	}
	for _, p := range []string{"etc/tza-probe", "var/lib/tza-agent", "etc/systemd/system/tza-agent.service", "etc/systemd/system/tza-agent.service.d",
		"etc/systemd/system/tza-proxy-xray.service", "etc/tza-proxy", "etc/nginx/conf.d/tza-site-ab12.conf", "usr/local/bin/tza-agent",
		"var/lib/lodestar-agent/migrate-requested"} {
		gone(p)
	}
	for _, want := range []string{"systemctl stop tza-agent", "systemctl disable tza-agent", "systemctl enable lodestar-agent",
		"systemctl disable --now tza-proxy-xray", "systemctl enable --now lodestar-proxy-xray", "systemctl start lodestar-agent"} {
		if !slices.Contains(calls, want) {
			t.Fatalf("missing %q in %q", want, calls)
		}
	}
	if slices.Index(calls, "systemctl start lodestar-agent") < slices.Index(calls, "systemctl enable --now lodestar-proxy-xray") {
		t.Fatal("agent started before its resources were moved")
	}
	if l.Needed() {
		t.Fatal("second run would migrate again")
	}
}

func TestLegacyMigrateFailureRestartsOldAgent(t *testing.T) {
	root, _ := legacyFixture(t)
	var calls []string
	l := &Legacy{Root: root, Exe: filepath.Join(t.TempDir(), "missing"), Systemd: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil, nil
		}}
	if err := l.Migrate(context.Background()); err == nil {
		t.Fatal("migration with a missing binary succeeded")
	}
	if !slices.Contains(calls, "systemctl start tza-agent") {
		t.Fatalf("old agent not restarted: %q", calls)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/tza-probe/agent.json")); err != nil {
		t.Fatal("configuration moved although the migration failed")
	}
}

func TestRequestMigrationOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	if !RequestMigration(dir) {
		t.Fatal("first request refused")
	}
	if RequestMigration(dir) {
		t.Fatal("requested twice: the agent would restart forever")
	}
	os.Rename(filepath.Join(dir, MigrateRequested), filepath.Join(dir, MigrateStarted))
	if RequestMigration(dir) {
		t.Fatal("requested again after the migration was started")
	}
}

func TestIsLegacyConfig(t *testing.T) {
	if !IsLegacyConfig("/etc/tza-probe/agent.json") || IsLegacyConfig("/etc/lodestar/agent.json") {
		t.Fatal("legacy configuration path not recognised")
	}
}

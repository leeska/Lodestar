package agent

// 改名迁移：项目从 TZA 改名为 Lodestar 之前装的 Agent 用 tza-agent 服务、/etc/tza-probe、
// /var/lib/tza-agent，代理、网站、证书、Compose 也都带 tza 前缀。新版本的 Agent 第一次在旧布局
// 里运行时，把这些一次性搬到新名字下（见 MigrateLegacy），之后旧名字不再出现。
//
// 触发：
//   - systemd：沙箱里的新 Agent 发现旧布局，留下 migrate-requested 记号后以状态 3 退出；systemd
//     重启前用 root、不带沙箱执行 ExecStartPre 的 --apply-update，它看到记号就用 systemd-run
//     起一个独立的临时服务跑 --migrate-legacy（独立服务，停掉旧 Agent 时不会把自己一起停掉）。
//   - OpenRC：Agent 本来就不在沙箱里，直接起一个脱离的子进程跑 --migrate-legacy。
//   - 重跑接入命令：安装脚本发现旧布局时，用新下载的二进制同步执行 --migrate-legacy。
//
// 每一步都可以重复执行；中途失败时，旧服务还没被删掉就把它重新启动，节点保持在旧名字上照常工作。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// LegacyConfigDir is /etc/lodestar before the rename.
	LegacyConfigDir = "/etc/tza-probe"
	// LegacyStateDir is DefaultStateDir before the rename.
	LegacyStateDir = "/var/lib/tza-agent"
	// MigrateRequested / MigrateStarted are the markers in the state directory
	// that hand the migration from the sandboxed Agent to the privileged
	// ExecStartPre and keep it from being started twice.
	MigrateRequested = "migrate-requested"
	MigrateStarted   = "migrate-started"
)

// legacyNames maps the old spellings that appear in unit files, init scripts
// and paths to the new ones. Order matters: longer prefixes first.
var legacyNames = strings.NewReplacer(
	"/usr/local/lib/tza-probe", "/usr/local/lib/lodestar",
	"/etc/tza-probe", "/etc/lodestar",
	"/etc/tza-proxy", "/etc/lodestar-proxy",
	"/etc/tza-certs", "/etc/lodestar-certs",
	"/opt/tza-compose", "/opt/lodestar-compose",
	"/var/lib/tza-agent", "/var/lib/lodestar-agent",
	"/var/log/tza-agent", "/var/log/lodestar-agent",
	"/run/tza-agent", "/run/lodestar-agent",
	"StateDirectory=tza-agent", "StateDirectory=lodestar-agent",
	"tza-agent", "lodestar-agent",
	"tza-proxy", "lodestar-proxy",
	"TZA ControlCenter", "Lodestar",
	"TZA Probe", "Lodestar",
)

// IsLegacyConfig reports whether the Agent was started with a configuration
// file in the pre-rename directory.
func IsLegacyConfig(configPath string) bool {
	abs, err := filepath.Abs(configPath)
	return err == nil && strings.HasPrefix(abs, LegacyConfigDir+"/")
}

// StateDir is where upgrades are staged: systemd's StateDirectory when the
// unit sets one (the only writable place in the sandbox), else the default,
// else the pre-rename directory on a host that has not been migrated.
func StateDir() string {
	if dir := os.Getenv("STATE_DIRECTORY"); dir != "" && !strings.Contains(dir, ":") {
		return dir
	}
	if _, err := os.Stat(DefaultStateDir); err != nil {
		if _, err := os.Stat(LegacyStateDir); err == nil {
			return LegacyStateDir
		}
	}
	return DefaultStateDir
}

// Legacy moves a pre-rename installation to the new names.
type Legacy struct {
	// Root prefixes every path (tests); "" is the real filesystem.
	Root string
	// Exe is the new binary to install as /usr/local/bin/lodestar-agent.
	Exe string
	// Systemd tells the service manager; false means OpenRC.
	Systemd bool
	Run     func(ctx context.Context, name string, args ...string) ([]byte, error)
	Log     *slog.Logger
}

// NewLegacy is the migration for this host.
func NewLegacy(exe string, log *slog.Logger) *Legacy {
	_, err := os.Stat("/run/systemd/system")
	return &Legacy{
		Exe:     exe,
		Systemd: err == nil,
		Log:     log,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.WaitDelay = 5 * time.Second
			return cmd.CombinedOutput()
		},
	}
}

func (l *Legacy) path(p string) string {
	return filepath.Join("/"+strings.TrimPrefix(l.Root, "/"), p)
}

func (l *Legacy) exists(p string) bool {
	_, err := os.Lstat(l.path(p))
	return err == nil
}

func (l *Legacy) run(ctx context.Context, name string, args ...string) error {
	out, err := l.Run(ctx, name, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Needed reports whether anything is left under the old names.
func (l *Legacy) Needed() bool {
	for _, p := range []string{LegacyConfigDir, "/etc/systemd/system/tza-agent.service", "/etc/init.d/tza-agent", "/usr/local/bin/tza-agent"} {
		if l.exists(p) {
			return true
		}
	}
	return false
}

// Migrate does the whole move: stop the old service, move the binary, the
// configuration, the state and the service definition, move what the
// management capabilities left (proxy kernels, nginx sites, certificates,
// compose projects, backups), then start the new service.
func (l *Legacy) Migrate(ctx context.Context) (err error) {
	oldService := l.exists("/etc/systemd/system/tza-agent.service") || l.exists("/etc/init.d/tza-agent")
	removedOld := false
	defer func() {
		if err != nil && oldService && !removedOld {
			// 旧服务还在：让节点回到旧名字上照常工作，下次升级再试。
			l.Log.Error("migration failed, restarting the old agent", "error", err)
			if l.Systemd {
				l.run(ctx, "systemctl", "start", "tza-agent")
			} else {
				l.run(ctx, "rc-service", "tza-agent", "start")
			}
		}
	}()
	if oldService {
		l.Log.Info("stopping the old agent")
		if l.Systemd {
			l.run(ctx, "systemctl", "stop", "tza-agent")
		} else {
			l.run(ctx, "rc-service", "tza-agent", "stop")
		}
	}
	if err = l.installBinary(); err != nil {
		return err
	}
	if err = l.moveDir(LegacyConfigDir, "/etc/lodestar", false); err != nil {
		return err
	}
	if err = l.moveDir(LegacyStateDir, DefaultStateDir, false); err != nil {
		return err
	}
	os.Remove(l.path(filepath.Join(DefaultStateDir, MigrateRequested)))
	os.Remove(l.path(filepath.Join(DefaultStateDir, MigrateStarted)))
	if err = l.service(ctx); err != nil {
		return err
	}
	removedOld = true
	// 管理能力留下的东西：失败只记下来，不影响 Agent 本身换名字。
	for name, step := range map[string]func(context.Context) error{
		"proxy": l.proxy, "sites": l.sites, "certs": l.certs, "compose": l.compose, "backups": l.backups,
	} {
		if err := step(ctx); err != nil {
			l.Log.Error("migration step failed", "step", name, "error", err)
		}
	}
	l.Log.Info("starting lodestar-agent")
	if l.Systemd {
		err = l.run(ctx, "systemctl", "start", "lodestar-agent")
	} else {
		err = l.run(ctx, "rc-service", "lodestar-agent", "start")
	}
	if err != nil {
		return err
	}
	for _, p := range []string{"/usr/local/bin/tza-agent", "/usr/local/bin/tza-agent.previous", filepath.Join(DefaultStateDir, "lodestar-agent.migrate")} {
		os.Remove(l.path(p))
	}
	l.Log.Info("migration finished")
	return nil
}

func (l *Legacy) installBinary() error {
	dst := l.path("/usr/local/bin/lodestar-agent")
	if l.Exe == "" {
		return errors.New("no binary to install")
	}
	if same, _ := sameFile(l.Exe, dst); same {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return copyFile(l.Exe, dst, 0755)
}

func sameFile(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ia, ib), nil
}

// moveDir renames from to to. When to already exists the move is refused
// (link false) or skipped; with link a symlink at from points to the new
// place, for configurations that still name the old path.
func (l *Legacy) moveDir(from, to string, link bool) error {
	src, dst := l.path(from), l.path(to)
	info, err := os.Lstat(src)
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.Mode()&os.ModeSymlink != 0) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		if !link {
			return fmt.Errorf("%s and %s both exist", from, to)
		}
		// 新目录已经有东西（比如还没迁移时主控已经往新路径下发了证书）：把旧目录里新目录没有的
		// 条目搬过去，旧目录空了再换成链接；有重名的就整个留在原处。
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if _, err := os.Lstat(filepath.Join(dst, e.Name())); err == nil {
				continue
			}
			if err := os.Rename(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		if err := os.Remove(src); err != nil {
			return fmt.Errorf("%s and %s both have %w", from, to, err)
		}
		l.Log.Info("merged", "from", from, "to", to)
		return os.Symlink(to, src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	l.Log.Info("moved", "from", from, "to", to)
	if link {
		return os.Symlink(to, src)
	}
	return nil
}

// rewrite writes the renamed copy of a unit file or init script and removes
// the old one.
func (l *Legacy) rewrite(from, to string, mode os.FileMode) error {
	raw, err := os.ReadFile(l.path(from))
	if err != nil {
		return err
	}
	if err := os.WriteFile(l.path(to), []byte(legacyNames.Replace(string(raw))), mode); err != nil {
		return err
	}
	return os.Remove(l.path(from))
}

func (l *Legacy) service(ctx context.Context) error {
	if !l.Systemd {
		if !l.exists("/etc/init.d/tza-agent") {
			return nil
		}
		l.run(ctx, "rc-update", "del", "tza-agent", "default")
		if err := l.rewrite("/etc/init.d/tza-agent", "/etc/init.d/lodestar-agent", 0755); err != nil {
			return err
		}
		return l.run(ctx, "rc-update", "add", "lodestar-agent", "default")
	}
	if !l.exists("/etc/systemd/system/tza-agent.service") {
		return nil
	}
	l.run(ctx, "systemctl", "disable", "tza-agent")
	if err := l.rewrite("/etc/systemd/system/tza-agent.service", "/etc/systemd/system/lodestar-agent.service", 0644); err != nil {
		return err
	}
	// Drop-ins（插件需要的额外权限等）跟着搬。
	oldDrop, newDrop := "/etc/systemd/system/tza-agent.service.d", "/etc/systemd/system/lodestar-agent.service.d"
	if entries, err := os.ReadDir(l.path(oldDrop)); err == nil {
		if err := os.MkdirAll(l.path(newDrop), 0755); err != nil {
			return err
		}
		for _, e := range entries {
			if e.Type().IsRegular() {
				if err := l.rewrite(filepath.Join(oldDrop, e.Name()), filepath.Join(newDrop, e.Name()), 0644); err != nil {
					return err
				}
			}
		}
		os.Remove(l.path(oldDrop))
	}
	if err := l.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return l.run(ctx, "systemctl", "enable", "lodestar-agent")
}

// proxy moves the kernel services: user, configuration, kernels and units.
// The proxies are down for the few seconds between stop and start.
func (l *Legacy) proxy(ctx context.Context) error {
	units, _ := filepath.Glob(l.path("/etc/systemd/system/tza-proxy-*.service"))
	if len(units) == 0 && !l.exists("/etc/tza-proxy") {
		return nil
	}
	var kernels []string
	for _, u := range units {
		k := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(u), "tza-proxy-"), ".service")
		kernels = append(kernels, k)
		l.run(ctx, "systemctl", "disable", "--now", "tza-proxy-"+k)
	}
	if l.Root == "" {
		if _, err := l.Run(ctx, "id", "-u", "tza-proxy"); err == nil {
			if _, err := l.Run(ctx, "id", "-u", ProxyUser); err != nil {
				if err := l.run(ctx, "usermod", "-l", ProxyUser, "tza-proxy"); err != nil {
					return err
				}
				l.run(ctx, "groupmod", "-n", ProxyUser, "tza-proxy")
			}
		}
	}
	if err := l.moveDir("/etc/tza-proxy", "/etc/lodestar-proxy", false); err != nil {
		return err
	}
	if err := l.moveDir("/usr/local/lib/tza-probe", "/usr/local/lib/lodestar", false); err != nil {
		return err
	}
	for _, k := range kernels {
		if err := l.rewrite("/etc/systemd/system/tza-proxy-"+k+".service", "/etc/systemd/system/lodestar-proxy-"+k+".service", 0644); err != nil {
			return err
		}
	}
	if len(kernels) == 0 {
		return nil
	}
	if err := l.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	for _, k := range kernels {
		if err := l.run(ctx, "systemctl", "enable", "--now", "lodestar-proxy-"+k); err != nil {
			return err
		}
	}
	return nil
}

// sites renames the managed nginx files. They keep naming certificates under
// /etc/tza-certs until the Server writes them again; certs leaves a link there.
func (l *Legacy) sites(ctx context.Context) error {
	files, _ := filepath.Glob(l.path("/etc/nginx/conf.d/tza-site-*.conf"))
	if len(files) == 0 {
		return nil
	}
	if err := l.certs(ctx); err != nil {
		return err
	}
	for _, f := range files {
		to := filepath.Join(filepath.Dir(f), "lodestar-site-"+strings.TrimPrefix(filepath.Base(f), "tza-site-"))
		if err := os.Rename(f, to); err != nil {
			return err
		}
	}
	if l.Root != "" {
		return nil
	}
	if err := l.run(ctx, "nginx", "-t"); err != nil {
		return err
	}
	return l.run(ctx, "systemctl", "reload", "nginx")
}

func (l *Legacy) certs(context.Context) error {
	return l.moveDir("/etc/tza-certs", "/etc/lodestar-certs", true)
}

// compose keeps a link at the old path: running containers and their labels
// name it.
func (l *Legacy) compose(context.Context) error {
	return l.moveDir("/opt/tza-compose", "/opt/lodestar-compose", true)
}

func (l *Legacy) backups(context.Context) error {
	return l.moveDir("/var/backups/tza-probe", "/var/backups/lodestar", true)
}

// RequestMigration is called by a sandboxed Agent that found the old layout
// under systemd: it leaves the marker for the privileged ExecStartPre and
// reports whether the Agent should restart to hand over. A migration that was
// already started (and failed, since this Agent still runs on the old names)
// is not requested again: the Agent just keeps working.
func RequestMigration(stateDir string) bool {
	if _, err := os.Stat(filepath.Join(stateDir, MigrateStarted)); err == nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(stateDir, MigrateRequested)); err == nil {
		return false
	}
	return os.WriteFile(filepath.Join(stateDir, MigrateRequested), nil, 0600) == nil
}

// StartRequestedMigration runs in the privileged --apply-update: when the
// Agent asked for the migration it starts --migrate-legacy as a transient
// service of its own, so stopping tza-agent does not stop the migration.
func StartRequestedMigration(ctx context.Context, stateDir, exe, configPath string) error {
	requested := filepath.Join(stateDir, MigrateRequested)
	if _, err := os.Stat(requested); err != nil {
		return nil
	}
	if err := os.Rename(requested, filepath.Join(stateDir, MigrateStarted)); err != nil {
		return err
	}
	// 二进制先复制一份：迁移会删掉 /usr/local/bin/tza-agent。
	runner := filepath.Join(stateDir, "lodestar-agent.migrate")
	if err := copyFile(exe, runner, 0700); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "systemd-run", "--no-block", "--collect", "--unit=lodestar-agent-migrate",
		"--description=Lodestar agent rename migration", runner, "--config", configPath, "--migrate-legacy").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

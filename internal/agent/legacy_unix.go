//go:build unix

package agent

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// StartDetachedMigration is the OpenRC path: the Agent is not sandboxed, so
// it starts --migrate-legacy in its own session and keeps running until the
// migration stops it.
func StartDetachedMigration(stateDir, exe, configPath string) error {
	if _, err := os.Stat(filepath.Join(stateDir, MigrateStarted)); err == nil {
		return nil
	}
	if err := os.WriteFile(filepath.Join(stateDir, MigrateStarted), nil, 0600); err != nil {
		return err
	}
	runner := filepath.Join(stateDir, "lodestar-agent.migrate")
	if err := copyFile(exe, runner, 0700); err != nil {
		return err
	}
	log, err := os.OpenFile("/var/log/lodestar-agent-migrate.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log = nil
	}
	cmd := exec.Command(runner, "--config", configPath, "--migrate-legacy")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if log != nil {
		cmd.Stdout, cmd.Stderr = log, log
	} else {
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	}
	return cmd.Start()
}

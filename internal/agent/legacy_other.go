//go:build !unix

package agent

import "errors"

// StartDetachedMigration needs a Unix service manager.
func StartDetachedMigration(stateDir, exe, configPath string) error {
	return errors.New("the rename migration runs on Linux only")
}

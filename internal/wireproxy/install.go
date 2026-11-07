package wireproxy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The warpwp actions this package runs; the vendored manager script has to answer to each of them.
const (
	// actionInstall sets everything up with the scripts as they are on disk. Plain --install would first replace them
	// with the latest signed release of the manager, so a panel build would run scripts it did not ship.
	actionInstall    = "--install-current"
	actionRemove     = "--remove"
	actionStatusJSON = "--status-json"
	actionFixRouting = "--fix-routing"
)

// Install places the vendored warpwp/warp-wireproxy-native.sh at the absolute paths warpwp itself expects, then runs
// the real provisioning: OS packages, a WARP registration, wireproxy as a systemd service and a default scheduler
// (see this package's doc for what that touches on the host). Already installed is no no-op, unlike Tor, AdGuard and
// Psiphon: the action is idempotent, and an admin may want to run it again to repair or update the setup.
func Install(ctx context.Context) error {
	if err := placeScript(managerBinPath, managerScript); err != nil {
		return err
	}
	if err := placeScript(nativeBinPath, nativeScript); err != nil {
		return err
	}
	_, err := run(ctx, actionInstall)
	return err
}

// placeScript replaces the script at path with body, executable. It renames a staged copy over the target: the
// scheduler runs these scripts directly, and bash reads a script as it executes it.
func placeScript(path string, body []byte) error {
	stage, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".new.*")
	if err != nil {
		return fmt.Errorf("staging %s: %w", path, err)
	}
	// After the rename there is nothing left at the staged path, and this does nothing.
	defer os.Remove(stage.Name())
	_, err = stage.Write(body)
	if err == nil {
		err = stage.Chmod(0o755)
	}
	if closeErr := stage.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("staging %s: %w", path, err)
	}
	if err := os.Rename(stage.Name(), path); err != nil {
		return fmt.Errorf("placing %s: %w", path, err)
	}
	return nil
}

// Uninstall runs warpwp's own safe removal (--remove): stops and disables
// the wireproxy service, removes the cron/timer scheduler and this project's
// own files under /etc/wireguard, but leaves warpwp itself in place (its own
// design -- an admin can re-Install without re-fetching it) and does not
// touch unrelated WARP tooling. Deliberately not --purge, which also chases
// down wgcf/warp-cli/fscarmen traces -- a much larger blast radius this
// package has no way to scope down to just what it installed.
func Uninstall(ctx context.Context) error {
	// The script asks y/N before it removes anything; the panel's own dialog is where the admin confirmed.
	_, err := runWithInput(ctx, strings.NewReader("y\n"), actionRemove)
	return err
}

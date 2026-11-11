package tor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/hostaction"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// InstallTimeout is how long a caller waits for the package manager, a fresh VPS's first index refresh included. One
// still running then is left to finish: killing it halfway would leave the package database unfinished.
const InstallTimeout = 6 * time.Minute

// packageManager describes how to drive one package manager non-interactively.
// updateArgs is nil for managers that need no separate index-refresh step.
type packageManager struct {
	bin         string
	updateArgs  []string
	installArgs []string
	removeArgs  []string
}

var packageManagers = []packageManager{
	{bin: "apt-get", updateArgs: []string{"update", "-qq"}, installArgs: []string{"install", "-y", "tor"}, removeArgs: []string{"remove", "-y", "tor"}},
	{bin: "dnf", installArgs: []string{"install", "-y", "tor"}, removeArgs: []string{"remove", "-y", "tor"}},
	{bin: "yum", installArgs: []string{"install", "-y", "tor"}, removeArgs: []string{"remove", "-y", "tor"}},
	{bin: "pacman", installArgs: []string{"-Sy", "--noconfirm", "tor"}, removeArgs: []string{"-R", "--noconfirm", "tor"}},
	{bin: "apk", updateArgs: []string{"update"}, installArgs: []string{"add", "tor"}, removeArgs: []string{"del", "tor"}},
	{bin: "zypper", installArgs: []string{"install", "-y", "tor"}, removeArgs: []string{"remove", "-y", "tor"}},
}

// detectPackageManager returns the first supported package manager found on
// PATH, checked in the fixed order above, or ok=false if none is.
func detectPackageManager() (packageManager, bool) {
	for _, pm := range packageManagers {
		if _, err := exec.LookPath(pm.bin); err == nil {
			return pm, true
		}
	}
	return packageManager{}, false
}

// run executes one package-manager subcommand non-interactively, as a unit of its own that writes where the panel's
// sandbox does not. DEBIAN_FRONTEND is harmless to set for managers that ignore it.
func (pm packageManager) run(ctx context.Context, args []string) error {
	_, err := hostaction.Run(ctx, hostaction.Spec{
		Name: pm.bin + " " + strings.Join(args, " "),
		Argv: append([]string{pm.bin}, args...),
		Env:  []string{"DEBIAN_FRONTEND=noninteractive"},
	})
	return err
}

func noPackageManagerErr(verb string) error {
	return fmt.Errorf("no supported package manager found (tried apt-get, dnf, yum, pacman, apk, zypper) -- %s tor manually", verb)
}

// Install installs the tor package via whichever supported package manager
// is available. A no-op when tor is already on PATH.
func Install(ctx context.Context) error {
	if IsAvailable() {
		return nil
	}
	pm, ok := detectPackageManager()
	if !ok {
		return noPackageManagerErr("install")
	}
	if pm.updateArgs != nil {
		if err := pm.run(ctx, pm.updateArgs); err != nil {
			if ctx.Err() != nil {
				// A refresh still running holds the package manager's lock, so an install beside it would only fail on that.
				return err
			}
			// A stale/unreachable index shouldn't block the install attempt
			// itself -- the install command below will fail on its own if
			// the package genuinely can't be found.
			logger.Warningf("tor: %s %v failed, continuing to install anyway: %v", pm.bin, pm.updateArgs, err)
		}
	}
	if err := pm.run(ctx, pm.installArgs); err != nil {
		return err
	}
	if !IsAvailable() {
		return fmt.Errorf("%s reported success but no tor binary is on PATH afterwards", pm.bin)
	}
	return nil
}

// Uninstall stops the managed daemon (if running) and removes the tor
// package via whichever supported package manager is available. A no-op
// when tor is already absent from PATH.
func Uninstall(ctx context.Context) error {
	GetManager().StopAll()
	if !IsAvailable() {
		return nil
	}
	pm, ok := detectPackageManager()
	if !ok {
		return noPackageManagerErr("remove")
	}
	return pm.run(ctx, pm.removeArgs)
}

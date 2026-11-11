package integration

import (
	"context"
	"fmt"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/unattendedupgrades"
)

// UnattendedUpgradesService manages the host's own unattended-upgrades
// package (internal/unattendedupgrades). Unlike TorService/PsiphonService,
// it needs no AutoStart/boot-time restoration and no DB-persisted enable
// flag: unattended-upgrades runs via its own systemd timer/cron, which the
// OS brings back on its own, and GetStatus always re-reads the real
// apt.conf.d files rather than a setting this service would have to keep in
// sync with them.
type UnattendedUpgradesService struct{}

// unattendedUpgradesInstallTimeout covers `apt-get update && apt-get install -y unattended-upgrades`: a network fetch
// and a package install. One still running then is left to finish.
const unattendedUpgradesInstallTimeout = 3 * time.Minute

// unattendedUpgradesWriteTimeout covers Configure and Disable, which wait only for a unit to write or remove a small file.
const unattendedUpgradesWriteTimeout = time.Minute

// UnattendedUpgradesStatus is the panel-facing status shape.
type UnattendedUpgradesStatus struct {
	Installed  bool                         `json:"installed"`
	Enabled    bool                         `json:"enabled"`
	Mode       string                       `json:"mode"`
	AutoReboot bool                         `json:"autoReboot"`
	LastRun    unattendedupgrades.RunStatus `json:"lastRun"`
}

func (s *UnattendedUpgradesService) Status() UnattendedUpgradesStatus {
	status := unattendedupgrades.GetStatus()
	return UnattendedUpgradesStatus{
		Installed:  status.Installed,
		Enabled:    status.Enabled,
		Mode:       string(status.Mode),
		AutoReboot: status.AutoReboot,
		LastRun:    unattendedupgrades.GetRunStatus(),
	}
}

// Install installs the unattended-upgrades package if it is not already
// present. Configure (a separate action) is what actually turns it on --
// Install alone leaves the periodic timer exactly as the OS shipped it.
func (s *UnattendedUpgradesService) Install() error {
	ctx, cancel := context.WithTimeout(context.Background(), unattendedUpgradesInstallTimeout)
	defer cancel()
	return unattendedupgrades.Install(ctx)
}

// Configure writes this panel's own settings (mode, auto-reboot) and
// enables the periodic timer. mode must be "security" or "full".
func (s *UnattendedUpgradesService) Configure(mode string, autoReboot bool) error {
	m := unattendedupgrades.Mode(mode)
	if m != unattendedupgrades.ModeSecurityOnly && m != unattendedupgrades.ModeFull {
		return fmt.Errorf("unknown mode %q", mode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), unattendedUpgradesWriteTimeout)
	defer cancel()
	return unattendedupgrades.Configure(ctx, m, autoReboot)
}

// Disable turns off the periodic timer and removes this panel's own
// drop-in -- see internal/unattendedupgrades.Disable for why this does not
// remove the OS package itself.
func (s *UnattendedUpgradesService) Disable() error {
	ctx, cancel := context.WithTimeout(context.Background(), unattendedUpgradesWriteTimeout)
	defer cancel()
	return unattendedupgrades.Disable(ctx)
}

// RunNow triggers an immediate unattended-upgrade run in the background and
// returns its run ID -- the frontend polls Status() (LastRun) for the
// outcome, the same runId/poll shape the panel's own self-updater uses.
func (s *UnattendedUpgradesService) RunNow() (string, error) {
	return unattendedupgrades.RunNow()
}

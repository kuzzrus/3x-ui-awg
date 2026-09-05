package integration

import (
	"context"
	"fmt"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// PsiphonService manages the panel's own Psiphon sidecar (internal/psiphon)
// and remembers whether it should run across restarts. Mirrors TorService.
type PsiphonService struct {
	service.SettingService
}

// PsiphonStatus reports process state to the UI: Installed/Configured gate
// whether Start can be tried, Running/Tunnel are the process's own cheap
// view. DefaultConfig tells the UI whether Configured means "this fork's own
// bundled, shared-identity config" rather than something the admin uploaded
// -- that distinction is the whole point of UsesDefaultConfig existing.
type PsiphonStatus struct {
	Installed     bool                 `json:"installed"`
	Configured    bool                 `json:"configured"`
	DefaultConfig bool                 `json:"defaultConfig"`
	Running       bool                 `json:"running"`
	Port          int                  `json:"port"`
	Tunnel        psiphon.TunnelStatus `json:"tunnel"`
	LastLog       string               `json:"lastLog,omitempty"`
}

func (s *PsiphonService) Status() (PsiphonStatus, error) {
	tunnel, err := psiphon.CurrentTunnel()
	if err != nil {
		return PsiphonStatus{}, err
	}
	// Discarded error: unreadable/absent is the expected case whenever
	// Configured is false, not a reason to fail the whole status response.
	usesDefault, _ := psiphon.UsesDefaultConfig()
	return PsiphonStatus{
		Installed:     psiphon.IsInstalled(),
		Configured:    psiphon.IsConfigured(),
		DefaultConfig: usesDefault,
		Running:       psiphon.GetManager().IsRunning(),
		Port:          psiphon.SocksPort,
		Tunnel:        tunnel,
		LastLog:       psiphon.GetManager().LastResult(),
	}, nil
}

// AutoStart brings Psiphon up at boot if enabled; never errors, mirroring
// AdGuardService.AutoStart, plus an IsConfigured gate AdGuard has no equivalent of.
func (s *PsiphonService) AutoStart() {
	enabled, err := s.GetPsiphonEnable()
	if err != nil || !enabled {
		return
	}
	if !psiphon.IsInstalled() {
		logger.Warning("psiphon: enabled but not installed, staying down")
		return
	}
	if !psiphon.IsConfigured() {
		logger.Warning("psiphon: enabled but no config uploaded yet, staying down")
		return
	}
	if err := psiphon.GetManager().Start(); err != nil {
		logger.Warningf("psiphon: failed to auto-start on boot: %v", err)
	}
}

// Start launches the managed Psiphon process and persists the choice so panel
// boot brings it back up automatically (see the auto-start in web.go).
func (s *PsiphonService) Start() error {
	if err := psiphon.GetManager().Start(); err != nil {
		return err
	}
	return s.SetPsiphonEnable(true)
}

// Stop stops the managed Psiphon process and persists the choice, mirroring
// Start.
func (s *PsiphonService) Stop() error {
	if err := psiphon.GetManager().Stop(); err != nil {
		return err
	}
	return s.SetPsiphonEnable(false)
}

// Install downloads the pinned Psiphon ConsoleClient release via the panel's
// own proxied HTTP client. installTimeout is shared with AdGuardService (adguard.go).
func (s *PsiphonService) Install() error {
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	return psiphon.Install(ctx, s.NewProxiedHTTPClient(installTimeout))
}

// Uninstall stops the daemon, removes the binary and config, and clears the
// auto-start flag so a future boot doesn't relaunch something no longer there.
func (s *PsiphonService) Uninstall() error {
	if err := psiphon.Uninstall(); err != nil {
		return err
	}
	return s.SetPsiphonEnable(false)
}

// SaveConfig validates and stores the config, restarting the process if it
// was already running so the change actually takes effect.
func (s *PsiphonService) SaveConfig(raw []byte) error {
	if err := psiphon.SaveConfig(raw); err != nil {
		return err
	}
	if !psiphon.GetManager().IsRunning() {
		return nil
	}
	return psiphon.GetManager().Restart()
}

// AvailableRegions lists ISO 3166-1 codes (states plus common territories),
// not a Psiphon-curated subset -- "Verify" answers "does it work," not this list.
func AvailableRegions() []Region { return isoCountries }

// Region is one entry in the picker: an ISO 3166-1 alpha-2 code and its
// display name.
type Region struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// verifyTimeout bounds the live exit check. No longer stacked behind a
// restart (see SetEgressRegion), so the full budget under the 30s WriteTimeout applies.
const verifyTimeout = 25 * time.Second

// invalidRegion catches a typo without freezing the API to isoCountries' own
// curated set -- an uncommon real code like Kosovo's "XK" must still work.
func invalidRegion(region string) bool {
	if region == "" {
		return false // EgressRegion's own "auto"
	}
	if len(region) != 2 {
		return true
	}
	for _, c := range region {
		if c < 'A' || c > 'Z' {
			return true
		}
	}
	return false
}

// SetEgressRegion patches EgressRegion and restarts the process. Deliberately
// does not also verify here -- stacked behind a restart it risks the panel's WriteTimeout; the UI calls CurrentExit as a separate follow-up.
func (s *PsiphonService) SetEgressRegion(region string) error {
	if invalidRegion(region) {
		return fmt.Errorf("unknown region code %q", region)
	}
	if err := psiphon.SetEgressRegion(region); err != nil {
		return err
	}
	return psiphon.GetManager().Restart()
}

// CurrentExit is the "Verify" button's action outside of a region change --
// e.g. confirming a long-running tunnel hasn't silently drifted.
func (s *PsiphonService) CurrentExit() (psiphon.ExitInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()
	return psiphon.CurrentExit(ctx)
}

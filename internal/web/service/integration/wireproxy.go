package integration

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/wireproxy"
)

// WireproxyService manages the panel's WARP-via-wireproxy sidecar
// (internal/wireproxy). Unlike TorService/PsiphonService, it does not need
// its own AutoStart/boot-time restoration: warp-wireproxy-native.sh's own
// install already does `systemctl enable wireproxy`, so the OS brings it
// back after a host reboot (or a panel restart) on its own -- systemd is the
// durable source of truth here, not a setting this service would have to
// keep in sync with it.
type WireproxyService struct{}

// wireproxyInstallTimeout is longer than the shared installTimeout
// (adguard.go) -- warpwp --install-current does OS package installs, a WARP
// registration, and an initial endpoint scan inline, not just one download.
const wireproxyInstallTimeout = 8 * time.Minute

// wireproxyActionTimeout bounds Start/Stop/Status -- systemctl calls or a single
// quick warpwp invocation, not a package install.
const wireproxyActionTimeout = 30 * time.Second

// wireproxyFixRoutingTimeout covers warpwp --fix-routing --force, whose first step is systemctl disable --now on three
// units that may ignore SIGTERM; a kill mid-cleanup leaves the warp link and its routes behind.
const wireproxyFixRoutingTimeout = 2 * time.Minute

// wireproxyRemoveTimeout covers warpwp --remove at its slowest: two 15s waits on maintenance locks, and systemd's
// default 90s stop timeout for each of the two units (the check and wireproxy) that may ignore SIGTERM.
const wireproxyRemoveTimeout = 5 * time.Minute

// WireproxyStatus is the panel-facing status shape: a curated subset of
// wireproxy.Status (which mirrors `warpwp --status-json`'s own field names
// and snake_case directly, appropriate for that internal layer) translated
// to the camelCase JSON this panel's other status DTOs (PsiphonStatus, etc.)
// already use.
type WireproxyStatus struct {
	Installed bool   `json:"installed"`
	Healthy   bool   `json:"healthy"`
	Scheduler string `json:"scheduler"`
	Service   struct {
		Active bool `json:"active"`
	} `json:"service"`
	Socks5 struct {
		Port      int  `json:"port"`
		Listening bool `json:"listening"`
	} `json:"socks5"`
	Warp struct {
		On   bool   `json:"on"`
		IP   string `json:"ip"`
		Colo string `json:"colo"`
		Loc  string `json:"loc"`
	} `json:"warp"`
	RoutingGuard struct {
		Danger bool `json:"danger"`
	} `json:"routingGuard"`
}

func (s *WireproxyService) Status() (WireproxyStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wireproxyActionTimeout)
	defer cancel()
	raw, err := wireproxy.GetStatus(ctx)
	if err != nil {
		return WireproxyStatus{}, err
	}
	var out WireproxyStatus
	out.Installed = raw.Installed
	out.Healthy = raw.Healthy
	out.Scheduler = raw.Scheduler
	out.Service.Active = raw.Service.Active
	out.Socks5.Port = raw.Socks5.Port
	out.Socks5.Listening = raw.Socks5.Listening
	out.Warp.On = raw.Warp.On
	out.Warp.IP = raw.Warp.IP
	out.Warp.Colo = raw.Warp.Colo
	out.Warp.Loc = raw.Warp.Loc
	out.RoutingGuard.Danger = raw.RoutingGuard.Danger
	return out, nil
}

func (s *WireproxyService) Install() error {
	ctx, cancel := context.WithTimeout(context.Background(), wireproxyInstallTimeout)
	defer cancel()
	return wireproxy.Install(ctx)
}

func (s *WireproxyService) Uninstall() error {
	ctx, cancel := context.WithTimeout(context.Background(), wireproxyRemoveTimeout)
	defer cancel()
	return wireproxy.Uninstall(ctx)
}

func (s *WireproxyService) Start() error {
	ctx, cancel := context.WithTimeout(context.Background(), wireproxyActionTimeout)
	defer cancel()
	return wireproxy.Start(ctx)
}

func (s *WireproxyService) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), wireproxyActionTimeout)
	defer cancel()
	return wireproxy.Stop(ctx)
}

// repairTimeout covers a --deep-scan (up to 150 candidates, each probed with
// a short series of requests) -- the longest-running action this service
// exposes.
const repairTimeout = 5 * time.Minute

// Repair re-runs warpwp's own endpoint scan/repair on demand -- the same
// action its cron/timer scheduler performs periodically on its own.
func (s *WireproxyService) Repair(mode wireproxy.RepairMode) error {
	ctx, cancel := context.WithTimeout(context.Background(), repairTimeout)
	defer cancel()
	return wireproxy.Repair(ctx, mode)
}

// FixRouting clears dangerous system-wide WARP routing the status endpoint
// flagged via routingGuard.danger -- see wireproxy.FixRouting's own doc.
func (s *WireproxyService) FixRouting() error {
	ctx, cancel := context.WithTimeout(context.Background(), wireproxyFixRoutingTimeout)
	defer cancel()
	return wireproxy.FixRouting(ctx)
}

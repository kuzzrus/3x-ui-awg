package job

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/tproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/integration"
)

// telegramConfigTimeout bounds the Telegram provisioning fetch below this
// job's own 10s cadence, so a stalled connection can't pile up ticks even
// though cron's SkipIfStillRunning already prevents them overlapping.
const telegramConfigTimeout = 15 * time.Second

// TproxyJob reconciles running tproxy engines against enabled inbounds and
// records inbound-level online status. No traffic byte counters -- the
// vendored MTProxy binary's /stats has none, per-client or aggregate (see
// tproxy.Manager.CollectOnlineInbounds and tproxy-status.md gap 2).
type TproxyJob struct {
	inboundService    service.InboundService
	settingService    service.SettingService
	frontProxyService integration.FrontProxyService
}

// NewTproxyJob creates a new tproxy reconcile job instance.
func NewTproxyJob() *TproxyJob {
	return new(TproxyJob)
}

// Run reconciles desired tproxy inbounds with the running relay and engines.
func (j *TproxyJob) Run() {
	desired, err := j.inboundService.DesiredTproxyInstances()
	if err != nil {
		logger.Warning("tproxy job: get desired instances failed:", err)
		return
	}

	hostname, err := j.settingService.GetFrontProxyDomain()
	if err != nil {
		logger.Warning("tproxy job: get front proxy domain failed:", err)
		return
	}
	if hostname == "" {
		// Matches the CRUD path (runtime.ensureTproxy): an unconfigured
		// front-proxy domain means the feature isn't set up yet, not an
		// error worth a reconcile attempt every 10s.
		return
	}

	if len(desired) > 0 {
		// Idempotent once both files exist (two stat calls, no network) --
		// only actually fetches on the ticks before Telegram's proxy-secret
		// and proxy-multi.conf first land on disk. Without this call
		// nothing in the tree ever provisions them, and every enabled
		// inbound's engine refuses to start forever.
		//
		// NewProxiedHTTPClient, not http.DefaultClient: core.telegram.org is
		// exactly the kind of destination a server on a filtered network
		// can't reach directly -- confirmed live, a real deployment sat with
		// every tick failing "cannot reach https://core.telegram.org/
		// getProxySecret: context deadline exceeded" and never provisioned
		// at all, the same class of destination this method already exists
		// to route through the admin's configured panel egress outbound for
		// (Discord webhooks, GitHub/update checks, WARP -- see setting.go).
		// Falls back to a direct client automatically when no egress outbound
		// is configured, so this is a no-op change everywhere that already
		// worked.
		client := j.settingService.NewProxiedHTTPClient(telegramConfigTimeout)
		ctx, cancel := context.WithTimeout(context.Background(), telegramConfigTimeout)
		err := tproxy.EnsureTelegramConfigFiles(ctx, client)
		cancel()
		if err != nil {
			logger.Warning("tproxy job: provisioning Telegram config failed:", err)
		}
	}

	changed := tproxy.GetManager().Reconcile(hostname, desired)

	onlineIds := tproxy.GetManager().CollectOnlineInbounds()
	online := make(map[int]bool, len(onlineIds))
	for _, id := range onlineIds {
		online[id] = true
	}
	onlineTags := make([]string, 0, len(onlineIds))
	for _, inst := range desired {
		if online[inst.Id] {
			onlineTags = append(onlineTags, inst.Tag)
		}
	}
	// activeEmails is nil: the vendored engine's stats have no per-client
	// breakdown, only per-inbound (see CollectOnlineInbounds) -- reporting
	// individual client emails here would claim data that doesn't exist.
	// Runs every tick regardless of changed: this is a heartbeat refresh
	// against RefreshLocalOnline's grace window, not a reaction to a state
	// transition.
	j.inboundService.RefreshLocalOnlineClients(nil, onlineTags)

	// Reconcile can restart the relay on a new port; a stale TproxyTarget
	// would 502 every request until the next manual edit. But Reload also
	// rebuilds the decoy's login-lockout tracker from scratch (frontproxy's
	// withLoginMock), so calling it unconditionally on this job's 10s
	// cadence would wipe every login-mock decoy's failed-attempt count
	// before its ban ever takes effect -- gate it on an actual change.
	if !changed {
		return
	}
	if err := j.frontProxyService.Reload(); err != nil {
		logger.Warning("tproxy job: reload front proxy failed:", err)
	}
}

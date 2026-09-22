package job

import (
	"context"
	"net/http"
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

// TproxyJob reconciles running tproxy engines against enabled inbounds. No
// traffic/online step yet -- the engine's stats port exists but is unscraped.
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
		ctx, cancel := context.WithTimeout(context.Background(), telegramConfigTimeout)
		err := tproxy.EnsureTelegramConfigFiles(ctx, http.DefaultClient)
		cancel()
		if err != nil {
			logger.Warning("tproxy job: provisioning Telegram config failed:", err)
		}
	}

	changed := tproxy.GetManager().Reconcile(hostname, desired)

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

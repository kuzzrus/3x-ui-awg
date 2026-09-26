package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/naiveproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/integration"
)

// NaiveProxyJob reconciles running Caddy sidecars against enabled inbounds.
// No traffic/online-status step: forward_proxy exposes neither today.
type NaiveProxyJob struct {
	inboundService    service.InboundService
	frontProxyService integration.FrontProxyService
}

// NewNaiveProxyJob creates a new NaiveProxy reconcile job instance.
func NewNaiveProxyJob() *NaiveProxyJob {
	return new(NaiveProxyJob)
}

// Run reconciles desired NaiveProxy inbounds with the running Caddy set.
func (j *NaiveProxyJob) Run() {
	desired, err := j.inboundService.DesiredNaiveProxyInstances()
	if err != nil {
		logger.Warning("naiveproxy job: get desired instances failed:", err)
		return
	}

	changed := naiveproxy.GetManager().Reconcile(desired)

	// Gated on changed, same as TproxyJob: an unconditional reload every
	// tick would also wipe every login-mock decoy's failed-attempt tracker.
	if !changed {
		return
	}
	if err := j.frontProxyService.Reload(); err != nil {
		logger.Warning("naiveproxy job: reload front proxy failed:", err)
	}
}

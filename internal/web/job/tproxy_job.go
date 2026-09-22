package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/tproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// TproxyJob reconciles running tproxy engines against enabled inbounds. No
// traffic/online step yet -- the engine's stats port exists but is unscraped.
type TproxyJob struct {
	inboundService service.InboundService
	settingService service.SettingService
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

	tproxy.GetManager().Reconcile(hostname, desired)
}

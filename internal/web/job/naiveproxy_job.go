package job

import (
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/naiveproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/integration"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// NaiveProxyJob reconciles running Caddy sidecars against enabled inbounds and folds
// the per-client traffic metered from their access logs into the usual accounting.
type NaiveProxyJob struct {
	inboundService    service.InboundService
	frontProxyService integration.FrontProxyService
}

// NewNaiveProxyJob creates a new NaiveProxy reconcile/traffic job instance.
func NewNaiveProxyJob() *NaiveProxyJob {
	return new(NaiveProxyJob)
}

// Run reconciles desired NaiveProxy inbounds with the running Caddy set, then
// records per-client traffic deltas and online status.
func (j *NaiveProxyJob) Run() {
	desired, err := j.inboundService.DesiredNaiveProxyInstances()
	if err != nil {
		logger.Warning("naiveproxy job: get desired instances failed:", err)
		return
	}

	mgr := naiveproxy.GetManager()
	changed := mgr.Reconcile(desired)
	j.recordTraffic(mgr, desired)

	// Gated on changed, same as TproxyJob: an unconditional reload every
	// tick would also wipe every login-mock decoy's failed-attempt tracker.
	if !changed {
		return
	}
	if err := j.frontProxyService.Reload(); err != nil {
		logger.Warning("naiveproxy job: reload front proxy failed:", err)
	}
}

// recordTraffic mirrors MtprotoJob, minus the live-speed broadcast: a tunnel is
// metered when it closes, so a per-tick speed would spike, not track anything.
func (j *NaiveProxyJob) recordTraffic(mgr *naiveproxy.Manager, desired []naiveproxy.Instance) {
	routedTags := make(map[string]bool)
	activeTags := make([]string, 0, len(desired))
	for _, inst := range desired {
		activeTags = append(activeTags, inst.Tag)
		if inst.RouteThroughXray {
			routedTags[inst.Tag] = true
		}
	}

	deltas, onlineEmails := mgr.CollectTraffic()
	traffics, clientTraffics := naiveTrafficRows(deltas, routedTags)
	if len(traffics) > 0 || len(clientTraffics) > 0 {
		if _, _, err := j.inboundService.AddTraffic(traffics, clientTraffics); err != nil {
			logger.Warning("naiveproxy job: add traffic failed:", err)
		}
	}

	j.inboundService.RefreshLocalOnlineClients(onlineEmails, activeTags)
}

// naiveTrafficRows turns per-client deltas into AddTraffic's rows. A routed inbound's
// total is left to its Xray bridge, which cannot tell users apart, so clients always count.
func naiveTrafficRows(deltas []naiveproxy.Traffic, routedTags map[string]bool) ([]*xray.Traffic, []*xray.ClientTraffic) {
	clientTraffics := make([]*xray.ClientTraffic, 0, len(deltas))
	inboundUp := make(map[string]int64)
	inboundDown := make(map[string]int64)
	for _, d := range deltas {
		clientTraffics = append(clientTraffics, &xray.ClientTraffic{Email: d.Email, Up: d.Up, Down: d.Down})
		if !routedTags[d.Tag] {
			inboundUp[d.Tag] += d.Up
			inboundDown[d.Tag] += d.Down
		}
	}

	traffics := make([]*xray.Traffic, 0, len(inboundUp))
	for tag, up := range inboundUp {
		traffics = append(traffics, &xray.Traffic{IsInbound: true, Tag: tag, Up: up, Down: inboundDown[tag]})
	}
	slices.SortFunc(traffics, func(a, b *xray.Traffic) int { return strings.Compare(a.Tag, b.Tag) })
	return traffics, clientTraffics
}

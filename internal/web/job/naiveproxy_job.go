package job

import (
	"maps"
	"slices"

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

// recordTraffic feeds the metered deltas to AddTraffic like MtprotoJob, but broadcasts no live
// speed: a tunnel is metered when it closes, so a per-tick speed would spike, not track anything.
func (j *NaiveProxyJob) recordTraffic(mgr *naiveproxy.Manager, desired []naiveproxy.Instance) {
	activeTags := make([]string, 0, len(desired))
	for _, inst := range desired {
		activeTags = append(activeTags, inst.Tag)
	}

	deltas, onlineEmails := mgr.CollectTraffic()
	traffics, clientTraffics := naiveTrafficRows(deltas)
	if len(traffics) > 0 || len(clientTraffics) > 0 {
		if _, _, err := j.inboundService.AddTraffic(traffics, clientTraffics); err != nil {
			logger.Warning("naiveproxy job: add traffic failed:", err)
		}
	}

	j.inboundService.RefreshLocalOnlineClients(onlineEmails, activeTags)
}

// naiveTrafficRows turns deltas into AddTraffic's rows: one per client email, as it keeps a single
// row per email, and inbound totals without Routed bytes, which their Xray bridge already counts.
func naiveTrafficRows(deltas []naiveproxy.Traffic) ([]*xray.Traffic, []*xray.ClientTraffic) {
	clients := make(map[string]*xray.ClientTraffic)
	inboundUp := make(map[string]int64)
	inboundDown := make(map[string]int64)
	for _, d := range deltas {
		row := clients[d.Email]
		if row == nil {
			row = &xray.ClientTraffic{Email: d.Email}
			clients[d.Email] = row
		}
		row.Up += d.Up
		row.Down += d.Down
		if !d.Routed {
			inboundUp[d.Tag] += d.Up
			inboundDown[d.Tag] += d.Down
		}
	}

	clientTraffics := make([]*xray.ClientTraffic, 0, len(clients))
	for _, email := range slices.Sorted(maps.Keys(clients)) {
		clientTraffics = append(clientTraffics, clients[email])
	}
	traffics := make([]*xray.Traffic, 0, len(inboundUp))
	for _, tag := range slices.Sorted(maps.Keys(inboundUp)) {
		traffics = append(traffics, &xray.Traffic{IsInbound: true, Tag: tag, Up: inboundUp[tag], Down: inboundDown[tag]})
	}
	return traffics, clientTraffics
}

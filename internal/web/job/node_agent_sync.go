package job

import (
	"context"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// syncAgent is syncOne for an agent node: the master keeps no copy of the node's state to
// reconcile, so it accounts what the agent counted and sees that it runs the config the
// master renders. It returns the emails online on the node.
func (j *NodeTrafficSyncJob) syncAgent(mgr *runtime.Manager, n *model.Node) []string {
	rt, err := mgr.AgentFor(n)
	if err != nil {
		logger.Warningf("node traffic sync: agent lookup failed for %s: %v", n.Name, err)
		return nil
	}
	client, err := rt.Client()
	if err != nil {
		logger.Warningf("node traffic sync: agent %s: %v", n.Name, err)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), nodeTrafficSyncRequestTimeout)
	stats, err := client.Stats(ctx)
	cancel()
	if err != nil {
		// The heartbeat reports a core that does not answer; a push can still go out.
		logger.Debugf("node traffic sync: stats from agent %s failed: %v", n.Name, err)
		j.inboundService.ClearNodeOnlineClients(n.Id)
		stats = nil
	}
	var online []string
	if stats != nil {
		if err := j.inboundService.AddAgentTraffic(n.Id, stats); err != nil {
			logger.Warningf("node traffic sync: account traffic of agent %s failed: %v", n.Name, err)
		} else {
			online = stats.Online
		}
	}
	if err := j.agentSync.Sync(context.Background(), rt, n, stats); err != nil {
		logger.Warningf("node traffic sync: agent %s: %v", n.Name, err)
	}
	return online
}

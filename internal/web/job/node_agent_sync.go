package job

import (
	"context"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// syncAgent is syncOne for an agent node: the master keeps no copy of the node's state to
// reconcile, so it only has to see that the agent runs the config it renders.
func (j *NodeTrafficSyncJob) syncAgent(mgr *runtime.Manager, n *model.Node) {
	rt, err := mgr.AgentFor(n)
	if err != nil {
		logger.Warningf("node traffic sync: agent lookup failed for %s: %v", n.Name, err)
		return
	}
	client, err := rt.Client()
	if err != nil {
		logger.Warningf("node traffic sync: agent %s: %v", n.Name, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), nodeTrafficSyncRequestTimeout)
	stats, err := client.Stats(ctx)
	cancel()
	if err != nil {
		// The heartbeat reports a core that does not answer; a push can still go out.
		logger.Debugf("node traffic sync: stats from agent %s failed: %v", n.Name, err)
		stats = nil
	}
	if err := j.agentSync.Sync(context.Background(), rt, n, stats); err != nil {
		logger.Warningf("node traffic sync: agent %s: %v", n.Name, err)
	}
}

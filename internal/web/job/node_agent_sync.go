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
	// The tick waits for every node, so one agent applying a config cannot hold it past the
	// bound a stock node's reconcile gets. The agent finishes what it started either way.
	pushCtx, cancelPush := context.WithTimeout(context.Background(), nodeReconcileTimeout)
	defer cancelPush()
	if err := j.agentSync.Sync(pushCtx, rt, n, stats); err != nil {
		logger.Warningf("node traffic sync: agent %s: %v", n.Name, err)
	}
}

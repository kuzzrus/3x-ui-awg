package service

import (
	"context"
	"errors"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// probeAgent reads an agent node's status the way probe reads a panel's, into the same
// patch, so the node list and the heartbeat job treat both kinds alike.
func (s *NodeService) probeAgent(ctx context.Context, n *model.Node, proxyURL string) (HeartbeatPatch, error) {
	patch := HeartbeatPatch{LastHeartbeat: time.Now().Unix()}
	client, err := runtime.NewAgentClient(n, proxyURL)
	if err != nil {
		patch.LastError = err.Error()
		return patch, err
	}
	start := time.Now()
	status, err := client.Status(ctx)
	// Like the panel probe, an answer that is not a success still tells how fast it came.
	var answered *runtime.AgentError
	if err == nil || errors.As(err, &answered) {
		patch.LatencyMs = int(time.Since(start) / time.Millisecond)
	}
	if err != nil {
		patch.LastError = err.Error()
		return patch, err
	}
	patch.XrayVersion = status.XrayVersion
	patch.PanelVersion = status.AgentVersion
	patch.Guid = status.Guid
	patch.CpuPct = status.CpuPct
	patch.MemPct = status.MemPct
	patch.UptimeSecs = status.UptimeSecs
	patch.NetUp = status.NetUp
	patch.NetDown = status.NetDown
	patch.XrayState = status.XrayState
	patch.XrayError = status.XrayError
	return patch, nil
}

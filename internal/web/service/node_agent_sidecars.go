package service

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

const agentTooOldForSidecars = "this agent is too old to run helper programs: update it"

// syncSidecars sends an agent the helper programs its node asks for when the agent reports another list.
func (s *AgentSyncService) syncSidecars(ctx context.Context, rt *runtime.AgentRuntime, n *model.Node, stats *agentproto.Stats, now time.Time) error {
	if stats == nil {
		return nil
	}
	want, err := agentproto.NormalizeSidecars(n.Sidecars)
	if err != nil {
		agentSidecarProblems.set(n.Id, err.Error())
		return nil
	}
	return s.keepSetting(ctx, rt, n, now, stats.SidecarsRevision == agentproto.SidecarsRevision(want), agentSetting{
		what:     "helper programs",
		tooOld:   agentTooOldForSidecars,
		problems: &agentSidecarProblems,
		retry:    func(st *agentSyncState) *time.Time { return &st.sidecarsRetry },
		send: func(ctx context.Context, client *runtime.AgentClient) error {
			_, err := client.PutSidecars(ctx, want)
			return err
		},
	})
}
